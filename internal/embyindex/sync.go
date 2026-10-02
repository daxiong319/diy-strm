package embyindex

import (
	"context"
	"strings"

	"litepan/internal/discover/embyclient"
	"litepan/internal/domain"
)

// SyncResult 是一次全量/增量扫描的结果统计。
type SyncResult struct {
	// Mode 是本次扫描模式：full / incremental。
	Mode string `json:"mode"`
	// ConfigID 是本次扫描使用的 Emby 配置 ID。
	ConfigID string `json:"config_id"`
	// Libraries 是本次实际扫描的媒体库数量。
	Libraries int `json:"libraries"`
	// Processed 是成功写入本地索引的条目数量。
	Processed int64 `json:"processed"`
	// Changed 是其中相对本地索引确实新增/变更的条目数量；
	// 只有这些条目会被登记 Emby 刷新意图。
	Changed int64 `json:"changed"`
	// Skipped 是被跳过的条目数量（缺 ID、类型不符等）。
	Skipped int64 `json:"skipped"`
	// Errors 是逐条写入失败的数量；不中断整轮扫描。
	Errors int64 `json:"errors"`
	// CursorAt 是本次扫描的游标终值（Unix 秒）。
	CursorAt int64 `json:"cursor_at"`
}

// syncRunFull 与 syncRunIncremental 是两种扫描模式标识。
const (
	syncRunFull        = "full"
	syncRunIncremental = "incremental"
)

// PerformEmbySync 执行一次全量扫描。
//
// 与老版 internal/emby/emby.go:46 的差异：
//   - 老版用全局 atomic 标志 + 独立的 sync_run 表记录批次；现版用 Service 内的
//     syncRunning 保证互斥，并把批次标识编码进 last_seen_sync_run 字符串。
//   - 老版扫描结束后按库清理「没在本次批次里出现」的条目；现版保持同样的做法，
//     批次标识形如 "full-<unix>"，因此同一库的陈旧条目会被清理。
func (s *Service) PerformEmbySync(ctx context.Context, cfg Config) (SyncResult, error) {
	return s.performSync(ctx, cfg, syncRunFull)
}

// PerformEmbyIncrementalSync 执行一次增量扫描。
//
// 游标来自 emby_sync_state.last_saved_cursor_at，扫描起点再向前回退
// EmbyIncrementalCursorOverlapSeconds 秒，避免 Emby 侧 DateLastSaved 写入延迟漏条目。
// 扫描成功后把游标推进到本轮开始时间。
func (s *Service) PerformEmbyIncrementalSync(ctx context.Context, cfg Config) (SyncResult, error) {
	return s.performSync(ctx, cfg, syncRunIncremental)
}

// performSync 是全量/增量扫描的公共实现。
func (s *Service) performSync(ctx context.Context, cfg Config, mode string) (SyncResult, error) {
	result := SyncResult{Mode: mode, ConfigID: cfg.ConfigID}
	if s == nil || s.index == nil {
		return result, domain.Errf(domain.CodeNotImplement)
	}
	if strings.TrimSpace(cfg.EmbyURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return result, domain.Errorf(domain.CodeValidation, "请先配置 Emby/Jellyfin 地址与 API Key")
	}
	// 同一时刻只允许一轮扫描：两轮并发的批次会互相把对方写入的条目当成陈旧条目清理。
	if !s.syncRunning.CompareAndSwap(false, true) {
		s.log.Warn("已有 Emby 条目同步任务正在运行，跳过本次执行", "mode", mode)
		return result, nil
	}
	defer s.syncRunning.Store(false)

	client := s.clientFor(cfg)
	selection, err := s.loadLibrarySelection(client, cfg)
	if err != nil {
		return result, err
	}
	result.Libraries = len(selection.libraries)

	scanStartedAt := s.nowUnix()
	syncRunID := mode + "-" + int64Text(scanStartedAt)

	// 本轮发生变化的条目聚合器：只有真正新增/变更的条目会被登记刷新意图，
	// 避免每轮扫描都把整个媒体库刷一遍。
	refresh := newRefreshAggregator(s.refreshSink, s.refreshThreshold)

	fields := embyIncrementalFields
	minDateLastSaved := ""
	var cursorAt int64
	if mode == syncRunIncremental {
		cursor, err := s.index.GetSyncCursor(ctx, cfg.ConfigID)
		if err != nil {
			return result, err
		}
		minDateLastSaved = BuildMinDateLastSaved(cursor.LastSavedCursorAt, EmbyIncrementalCursorOverlapSeconds)
		cursorAt = scanStartedAt
	}

	for _, library := range selection.libraries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		query := embyclient.EmbyItemsQuery{
			LibraryID:        library.ID,
			Limit:            embyInitialScanLimit,
			IncludeItemTypes: embyScannedItemTypes,
			Fields:           fields,
			MinDateLastSaved: minDateLastSaved,
			SortBy:           "DateLastSaved",
			SortOrder:        "Descending",
		}
		// 增量扫描的游标与排序字段要一致；全量扫描按创建时间倒序，便于观察进度。
		if mode == syncRunFull {
			query.SortBy = "DateCreated"
		}
		err := client.FetchMediaItemsByLibraryID(ctx, query, func(item embyclient.BaseItemDtoV2) error {
			if !isScannedType(item.Type) {
				result.Skipped++
				return nil
			}
			changed, err := s.indexOneItem(ctx, item, library.ID, library.Name, syncRunID)
			if err != nil {
				// 单条失败不中断整轮扫描，与老版回调里的处理一致。
				result.Errors++
				s.log.Warn("保存 Emby 媒体项失败", "item_id", item.Id, "name", item.Name, "err", err)
				return nil
			}
			result.Processed++
			if changed {
				result.Changed++
				refresh.add(library.ID, library.Name, item.Id)
			}
			return nil
		})
		if err != nil {
			// 单个库拉取失败不阻断其它库，记录后继续。
			result.Errors++
			s.log.Warn("拉取 Emby 媒体库条目失败", "library_id", library.ID, "library_name", library.Name, "err", err)
			continue
		}
		// 清理该库里没在本次批次出现过的陈旧条目。
		if removed, cerr := s.index.CleanupStaleItemsByLibrarySyncRun(ctx, library.ID, syncRunID); cerr != nil {
			result.Errors++
			s.log.Warn("清理 Emby 陈旧条目失败", "library_id", library.ID, "err", cerr)
		} else if removed > 0 {
			s.log.Info("已清理 Emby 陈旧索引条目", "library_id", library.ID, "removed", removed)
		}
	}

	// 媒体库集合变化时同步删除本地已不存在的库记录。
	activeLibraryIDs := make([]string, 0, len(selection.libraries))
	for _, library := range selection.libraries {
		if id := strings.TrimSpace(library.ID); id != "" {
			activeLibraryIDs = append(activeLibraryIDs, id)
		}
	}
	if len(activeLibraryIDs) > 0 {
		if err := s.index.CleanupDeletedLibraries(ctx, activeLibraryIDs); err != nil {
			s.log.Warn("清理已删除的 Emby 媒体库失败", "err", err)
		}
	}

	if err := s.index.TouchSyncTime(ctx, cfg.ConfigID, scanStartedAt); err != nil {
		s.log.Warn("更新 Emby 同步时间失败", "err", err)
	}
	if mode == syncRunIncremental {
		if err := s.index.AdvanceSyncCursor(ctx, cfg.ConfigID, cursorAt, scanStartedAt); err != nil {
			s.log.Warn("推进 Emby 增量同步游标失败", "err", err)
		}
		result.CursorAt = cursorAt
	}
	// 扫描结果已经落库，最后再把「哪些条目变了」投递给刷新队列，
	// 让 Emby 去感知新生成的 STRM。投递失败只记日志，不影响扫描结果。
	if registered := refresh.flush(ctx, s.logWarn); registered > 0 {
		s.log.Info("已登记 Emby 刷新意图", "count", registered, "changed", result.Changed)
	}

	s.log.Info("Emby 条目同步完成",
		"mode", mode, "libraries", result.Libraries,
		"processed", result.Processed, "changed", result.Changed,
		"skipped", result.Skipped, "errors", result.Errors)
	return result, nil
}

// logWarn 把聚合器里的告警接到本包日志上，保持日志口径统一。
func (s *Service) logWarn(msg string, args ...any) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warn(msg, args...)
}

// SyncEmbyItemByID 同步单个 Emby 条目，返回该条目是否被写入/更新。
//
// 与老版 internal/emby/emby.go:409 一致：条目不存在、类型不在
// Movie/Video/Episode 之内、或所属媒体库未被选中时返回 false 且不报错。
func (s *Service) SyncEmbyItemByID(ctx context.Context, cfg Config, itemID string) (changed bool, err error) {
	if s == nil || s.index == nil {
		return false, domain.Errf(domain.CodeNotImplement)
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return false, nil
	}
	if strings.TrimSpace(cfg.EmbyURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return false, domain.Errorf(domain.CodeValidation, "请先配置 Emby/Jellyfin 地址与 API Key")
	}
	client := s.clientFor(cfg)
	found, err := client.FindItemByID(itemID)
	if err != nil {
		return false, err
	}
	if found == nil {
		return false, nil
	}
	if !isScannedType(found.Type) {
		return false, nil
	}

	// 解析所属媒体库：多个候选时老版保留条目但不写 LibraryId，这里沿用。
	libraryID, libraryName, err := s.resolveItemLibrary(client, itemID)
	if err != nil {
		// 解析不到媒体库不阻断单条同步，条目仍然保留。
		s.log.Warn("解析 Emby 条目所属媒体库失败，保留条目但不写入 LibraryId",
			"item_id", itemID, "err", err)
		libraryID, libraryName = "", ""
	}
	selected := cfg.LibraryIDs
	if !cfg.AllSelected && !librarySelected(selected, libraryID) {
		s.log.Debug("Emby 条目所属媒体库未被选中，跳过", "item_id", itemID, "library_id", libraryID)
		return false, nil
	}

	// 单条同步不参与批次清理，syncRunID 传空串。
	changed, err = s.indexOneItem(ctx, *found, libraryID, libraryName, "")
	if err != nil {
		return false, err
	}
	if changed && s.refreshSink != nil && libraryID != "" {
		// 只有确实新增/变更的条目才登记刷新，且必须有确定的库归属
		// （刷新队列对条目刷新的降级依赖库 ID）。
		if rerr := s.refreshSink.RegisterRefreshIntent(ctx, RefreshIntent{
			TargetType:  RefreshTargetItem,
			LibraryID:   libraryID,
			LibraryName: libraryName,
			ItemID:      itemID,
		}); rerr != nil {
			s.log.Warn("登记 Emby 条目刷新意图失败", "item_id", itemID, "err", rerr)
		}
	}
	return changed, nil
}

// loadLibrarySelection 拉取媒体库列表并按配置过滤。
//
// 老版语义：SyncAllLibraries != 0（现版 AllSelected）时扫描全部；
// 否则只扫描 SelectedLibraries 里列出的库。库列表变化时会写入 emby_libraries。
func (s *Service) loadLibrarySelection(client *embyclient.Client, cfg Config) (librarySelection, error) {
	libraries, err := client.GetAllMediaLibraries()
	if err != nil {
		return librarySelection{}, err
	}
	if len(libraries) == 0 {
		return librarySelection{}, domain.Errorf(domain.CodeNotFound, "未获取到任何 Emby 媒体库")
	}
	records := make([]domain.EmbyLibrary, 0, len(libraries))
	for _, library := range libraries {
		records = append(records, domain.EmbyLibrary{
			Name:      library.Name,
			LibraryID: library.ID,
		})
	}
	if err := s.index.UpsertLibraries(context.Background(), records); err != nil {
		s.log.Warn("保存 Emby 媒体库信息失败", "err", err)
	}
	selection := librarySelection{allLibraries: cfg.AllSelected || len(cfg.LibraryIDs) == 0}
	if selection.allLibraries {
		selection.libraries = libraries
		return selection, nil
	}
	byID := make(map[string]embyclient.EmbyLibrary, len(libraries))
	for _, library := range libraries {
		byID[library.ID] = library
	}
	filtered := make([]embyclient.EmbyLibrary, 0, len(cfg.LibraryIDs))
	for _, id := range cfg.LibraryIDs {
		if library, ok := byID[id]; ok {
			filtered = append(filtered, library)
		}
	}
	selection.libraries = filtered
	return selection, nil
}

// isScannedType 判断条目类型是否纳入本地索引。
func isScannedType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "Movie", "Video", "Episode":
		return true
	default:
		return false
	}
}

// int64Text 是无分配依赖的整数转字符串。
func int64Text(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
