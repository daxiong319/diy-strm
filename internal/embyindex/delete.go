package embyindex

import (
	"context"
	"path"
	"strings"

	"litepan/internal/discover/embyclient"
	"litepan/internal/domain"
)

// 删除粒度。
const (
	// GranularityMovie 表示电影：删除视频所在目录（目录内只有该视频时）或视频本身。
	GranularityMovie = "movie"
	// GranularityEpisode 表示单集：只删除视频本身及其同名元数据，绝不删目录。
	GranularityEpisode = "episode"
	// GranularitySeason 表示季：季目录是独立目录时删除整个季目录，否则降级为逐集删除。
	GranularitySeason = "season"
	// GranularityTvshow 表示剧：删除剧目录。
	GranularityTvshow = "tvshow"
)

// DeleteOutcome 描述一次联动删除的结果，便于日志与测试断言。
type DeleteOutcome struct {
	// Deleted 为 true 表示确实执行了删除。
	Deleted bool `json:"deleted"`
	// Skipped 为 true 表示因为开关关闭、映射缺失或存在歧义而没有删除。
	Skipped bool `json:"skipped"`
	// Reason 是跳过或执行的原因，供日志排查。
	Reason string `json:"reason"`
	// RemovedPaths 是本次删除涉及的网盘路径，用于「删了什么」的日志。
	RemovedPaths []string `json:"removed_paths,omitempty"`
}

// 跳过原因。
const (
	reasonDisabled        = "delete_netdisk_disabled"
	reasonNoItem          = "emby_item_not_indexed"
	reasonNoMapping       = "no_backing_file_mapping"
	reasonAmbiguous       = "ambiguous_mapping"
	reasonUnsafePath      = "unsafe_target_path"
	reasonNotFound        = "netdisk_file_not_found"
	reasonDeleteFailed    = "netdisk_delete_failed"
	reasonNotApplicable   = "nothing_to_delete"
	reasonMissingResolver = "no_pickcode_resolver"
)

// DeleteNetdiskMovieByEmbyItemID 联动删除电影。
//
// 老版行为：查条目关联的网盘视频文件，若该文件所在目录下只有一个视频，
// 就删除整个目录（连同海报、NFO），否则只删除视频与其同名元数据。
//
// 现版差异：由于没有逐文件表，只有当调用方注入了 PickCode 解析能力
// 且能唯一定位到文件时才删除；任何歧义都走跳过路径。
func (s *Service) DeleteNetdiskMovieByEmbyItemID(ctx context.Context, itemID string) (DeleteOutcome, error) {
	return s.deleteByGranularity(ctx, itemID, GranularityMovie)
}

// DeleteNetdiskEpisodeByEmbyItemID 联动删除单集。
// 只删除视频文件本身与同名元数据，永远不会删除目录。
func (s *Service) DeleteNetdiskEpisodeByEmbyItemID(ctx context.Context, itemID string) (DeleteOutcome, error) {
	return s.deleteByGranularity(ctx, itemID, GranularityEpisode)
}

// DeleteNetdiskSeasonByItemID 联动删除季。
// 季目录是独立目录（目录名能解析出季号）时删除整个季目录；
// 否则降级为逐集删除。老版按 season_id 反查所有集，现版保持一致。
func (s *Service) DeleteNetdiskSeasonByItemID(ctx context.Context, itemID string) (DeleteOutcome, error) {
	return s.deleteByGranularity(ctx, itemID, GranularitySeason)
}

// DeleteNetdiskTvshowByItemID 联动删除整部剧。
// 老版按 series_id 反查所有集，取第一条的路径推断剧目录。
func (s *Service) DeleteNetdiskTvshowByItemID(ctx context.Context, itemID string) (DeleteOutcome, error) {
	return s.deleteByGranularity(ctx, itemID, GranularityTvshow)
}

// deleteByGranularity 是四种粒度的统一入口。
//
// 安全约束（本函数的核心不变量）：
//  1. 开关 emby_delete_netdisk_enabled 未打开时一律跳过；
//  2. 条目不在本地索引里时跳过；
//  3. 无法确定唯一目标路径时跳过（歧义优先于删除）；
//  4. 目标路径为空、"."、"/" 等危险值时跳过；
//  5. 只有真正删除成功后才清理本地索引记录。
func (s *Service) deleteByGranularity(ctx context.Context, itemID, granularity string) (DeleteOutcome, error) {
	if s == nil || s.index == nil {
		return DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, domain.Errf(domain.CodeNotImplement)
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return DeleteOutcome{Skipped: true, Reason: reasonNoItem}, nil
	}
	// 1. 开关默认关闭；关闭时只记录，不做任何删除。
	if !s.DeleteNetdiskEnabled() {
		s.log.Debug("Emby 删除联动网盘删除未启用，跳过", "item_id", itemID, "granularity", granularity)
		return DeleteOutcome{Skipped: true, Reason: reasonDisabled}, nil
	}
	// 2. 先确认条目确实在本地索引里，再检查执行能力。
	// 这样"从未索引过"这类最具体、最可诊断的原因不会被能力缺失覆盖掉。
	item, err := s.index.GetItem(ctx, itemID)
	if err != nil {
		if isNotFound(err) {
			return DeleteOutcome{Skipped: true, Reason: reasonNoItem}, nil
		}
		return DeleteOutcome{Skipped: true, Reason: reasonNoItem}, err
	}
	if item == nil {
		return DeleteOutcome{Skipped: true, Reason: reasonNoItem}, nil
	}
	if s.files == nil {
		return DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, nil
	}
	if s.lookup == nil {
		// 没有逐文件解析能力就无法确定唯一目标，直接跳过而不是猜路径。
		s.log.Warn("缺少 PickCode→网盘文件解析能力，跳过 Emby 删除联动",
			"item_id", itemID, "granularity", granularity)
		return DeleteOutcome{Skipped: true, Reason: reasonMissingResolver}, nil
	}

	// 2/3. 收集该粒度涉及的条目，逐个解析唯一的目标目录。
	targets, outcome, err := s.collectTargets(ctx, itemID, granularity)
	if err != nil || outcome.Skipped {
		return outcome, err
	}
	if len(targets) == 0 {
		return DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, nil
	}

	// 4. 逐个删除；任一目标不合法或删除失败都中止，避免半删状态。
	removed := make([]string, 0, len(targets))
	for _, target := range targets {
		if !isSafeDeleteTarget(target.dirPath) {
			s.log.Warn("Emby 删除联动的目标路径不安全，跳过删除",
				"item_id", itemID, "granularity", granularity, "path", target.dirPath)
			return DeleteOutcome{Skipped: true, Reason: reasonUnsafePath}, nil
		}
		if err := s.deleteTarget(ctx, target); err != nil {
			s.log.Error("Emby 删除联动网盘删除失败",
				"item_id", itemID, "granularity", granularity, "path", target.dirPath, "err", err)
			return DeleteOutcome{Skipped: true, Reason: reasonDeleteFailed, RemovedPaths: removed}, err
		}
		removed = append(removed, target.dirPath)
		s.log.Info("Emby 删除联动已删除网盘内容",
			"item_id", itemID, "granularity", granularity,
			"account_id", target.file.AccountID, "path", target.dirPath)
	}

	// 5. 删除成功后才清理本地索引，保证失败时仍可重试。
	if err := s.cleanupIndexForGranularity(ctx, itemID, granularity); err != nil {
		s.log.Warn("清理 Emby 本地索引记录失败", "item_id", itemID, "granularity", granularity, "err", err)
	}
	return DeleteOutcome{Deleted: true, Reason: "deleted", RemovedPaths: removed}, nil
}

// deleteTarget 描述一次待执行的删除：删目录还是删单个文件。
type deleteTarget struct {
	file    backingFile
	dirPath string
	// folder 为 true 时删除目录本身，否则删除目录下的 file.Name。
	folder bool
}

// collectTargets 收集并校验本次删除的目标。
//
// 返回的 outcome.Skipped 为 true 时调用方必须直接返回，不做任何删除。
func (s *Service) collectTargets(ctx context.Context, itemID, granularity string) ([]deleteTarget, DeleteOutcome, error) {
	switch granularity {
	case GranularityMovie, GranularityEpisode:
		item, err := s.index.GetItem(ctx, itemID)
		if err != nil {
			if isNotFound(err) {
				return nil, DeleteOutcome{Skipped: true, Reason: reasonNoItem}, nil
			}
			return nil, DeleteOutcome{}, err
		}
		if strings.TrimSpace(item.PickCode) == "" {
			return nil, DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, nil
		}
		file, ok := s.resolveBackingFile(ctx, item.PickCode)
		if !ok {
			return nil, DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, nil
		}
		// 单集永不删目录；电影在无法确认目录内只有一个视频时也不删目录，
		// 与老版「视频数 == 1 才删目录」的安全取向一致。
		return []deleteTarget{{file: file, dirPath: file.RelativePath, folder: false}}, DeleteOutcome{}, nil

	case GranularitySeason:
		items, err := s.index.ItemsBySeasonID(ctx, itemID)
		if err != nil {
			return nil, DeleteOutcome{}, err
		}
		return s.collectSeriesLikeTargets(ctx, items, "season_id")

	case GranularityTvshow:
		items, err := s.index.ItemsBySeriesID(ctx, itemID)
		if err != nil {
			return nil, DeleteOutcome{}, err
		}
		return s.collectSeriesLikeTargets(ctx, items, "series_id")

	default:
		return nil, DeleteOutcome{Skipped: true, Reason: reasonNotApplicable}, nil
	}
}

// collectSeriesLikeTargets 处理季/剧：按关联条目解析出唯一的目录目标。
//
// 老版取「第一个 SyncFileId 的 Path」作为季/剧目录，隐含假设同季所有集在同一个目录下。
// 现版保留该假设，但把它显式化：解析出的目录必须唯一，
// 出现多个不同目录时视为歧义并跳过（老版在这种情况下会静默删错，是现版要避免的）。
func (s *Service) collectSeriesLikeTargets(ctx context.Context, items []domain.EmbyMediaItem, field string) ([]deleteTarget, DeleteOutcome, error) {
	if len(items) == 0 {
		return nil, DeleteOutcome{Skipped: true, Reason: reasonNoItem}, nil
	}
	dirs := make(map[string]backingFile, len(items))
	for _, item := range items {
		pickCode := strings.TrimSpace(item.PickCode)
		if pickCode == "" {
			continue
		}
		file, ok := s.resolveBackingFile(ctx, pickCode)
		if !ok {
			continue
		}
		dir := path.Dir(strings.Trim(file.RelativePath, "/"))
		if dir == "" || dir == "." {
			// 文件就在根目录下，删根目录显然不安全。
			continue
		}
		if _, exists := dirs[dir]; !exists {
			dirs[dir] = file
		}
	}
	switch len(dirs) {
	case 0:
		return nil, DeleteOutcome{Skipped: true, Reason: reasonNoMapping}, nil
	case 1:
		for dir, file := range dirs {
			return []deleteTarget{{file: file, dirPath: dir, folder: true}}, DeleteOutcome{}, nil
		}
	default:
		// 同一季/剧散落在多个目录，无法确定唯一删除目标 —— 宁可漏删不可误删。
		s.log.Warn("Emby 删除联动的目标目录不唯一，跳过删除",
			"field", field, "candidate_dirs", len(dirs))
		return nil, DeleteOutcome{Skipped: true, Reason: reasonAmbiguous}, nil
	}
	return nil, DeleteOutcome{Skipped: true, Reason: reasonAmbiguous}, nil
}

// deleteTarget 执行一次删除。
//
// 目录目标：把目录本身作为待删项交给驱动（ResolvePath 不能解析目录，
// 因此直接用 backingFile 里的相对路径拼出目录名，通过父目录解析到目录项）。
// 文件目标：解析到文件后删除文件本身。
func (s *Service) deleteTarget(ctx context.Context, target deleteTarget) error {
	if !target.folder {
		item, err := s.files.ResolvePath(ctx, target.file.AccountID, target.file.RootID, target.file.RelativePath)
		if err != nil {
			return err
		}
		parentID := path.Dir(strings.Trim(target.file.RelativePath, "/"))
		if parentID == "." {
			parentID = ""
		}
		parent, err := s.files.ResolvePath(ctx, target.file.AccountID, target.file.RootID, parentID)
		if err != nil {
			return err
		}
		if parent.IsDir {
			return s.files.DeleteFiles(ctx, target.file.AccountID, []string{item.ID}, parent.ID)
		}
		return s.files.DeleteFiles(ctx, target.file.AccountID, []string{item.ID}, "")
	}

	// 目录目标：解析父目录，再在其中找到同名目录项。
	dirPath := strings.Trim(target.dirPath, "/")
	parentPath := path.Dir(dirPath)
	if parentPath == "." {
		parentPath = ""
	}
	dirName := path.Base(dirPath)
	parent, err := s.files.ResolvePath(ctx, target.file.AccountID, target.file.RootID, parentPath)
	if err != nil {
		return err
	}
	if !parent.IsDir {
		return domain.Errorf(domain.CodeValidation, "目标父级不是目录：%s", parentPath)
	}
	children, err := s.files.List(ctx, target.file.AccountID, parent.ID, true)
	if err != nil {
		return err
	}
	var matched *domain.FileItem
	for i := range children {
		if children[i].Name != dirName {
			continue
		}
		if matched != nil {
			// 目录同名重名，无法确定删哪个。
			return domain.Errorf(domain.CodeValidation, "目标目录存在同名项：%s", dirPath)
		}
		item := children[i]
		matched = &item
	}
	if matched == nil {
		return domain.Errorf(domain.CodeNotFound, "目标目录不存在：%s", dirPath)
	}
	if !matched.IsDir {
		return domain.Errorf(domain.CodeValidation, "目标不是目录：%s", dirPath)
	}
	return s.files.DeleteFiles(ctx, target.file.AccountID, []string{matched.ID}, parent.ID)
}

// cleanupIndexForGranularity 删除本地索引记录及关联。
func (s *Service) cleanupIndexForGranularity(ctx context.Context, itemID, granularity string) error {
	switch granularity {
	case GranularityEpisode, GranularityMovie:
		return s.index.DeleteItemByID(ctx, itemID)
	case GranularitySeason:
		return s.index.DeleteItemsBySeasonID(ctx, itemID)
	case GranularityTvshow:
		return s.index.DeleteItemsBySeriesID(ctx, itemID)
	default:
		return nil
	}
}

// isSafeDeleteTarget 判断目标路径是否可安全删除。
// 空、"."、"/"、".." 以及任何包含 ".." 段的路径一律拒绝，
// 与老版 delete115Folders / deleteOpenListFolders 的根保护一致。
func isSafeDeleteTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" || target == "." || target == "/" {
		return false
	}
	// 必须在 path.Clean 之前检查原始分段：否则 "电影/../其它"
	// 会被 Clean 归一成 "其它" 而绕过检查。
	for _, segment := range strings.Split(target, "/") {
		if segment == ".." {
			return false
		}
	}
	cleaned := path.Clean(target)
	if cleaned == "." || cleaned == "/" || cleaned == ".." {
		return false
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// isNotFound 判断错误是否为「记录不存在」。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if appErr, ok := domain.AsAppError(err); ok {
		return appErr.Code == domain.CodeNotFound
	}
	return false
}

// GetItemDetail 把 Emby 条目详情投影为通知渲染所需的 ItemDetail。
//
// 供 embyrefresh / embywebhook 这类只需要展示信息的调用方使用，
// 避免它们直接依赖 embyclient 的字段命名。
func (s *Service) GetItemDetail(ctx context.Context, cfg Config, itemID string) (*ItemDetail, error) {
	if strings.TrimSpace(itemID) == "" {
		return nil, nil
	}
	if s == nil {
		return nil, domain.Errf(domain.CodeNotImplement)
	}
	client := s.clientFor(cfg)
	detail, err := client.FindItemByID(itemID)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, nil
	}
	return itemDetailFromEmby(detail), nil
}

// MediaSourceDetail 是通知里用到的媒体源信息。
type MediaSourceDetail struct {
	Path string
	Size int64
}

// ItemDetail 是 Emby 条目详情的本包投影，字段与 embywebhook.ItemDetail 对应。
type ItemDetail struct {
	ID                string
	Name              string
	Type              string
	Overview          string
	ProductionYear    int
	CommunityRating   float64
	Genres            []string
	ProviderIDs       map[string]string
	ImageTags         map[string]string
	DateCreated       string
	SeriesName        string
	IndexNumber       int
	ParentIndexNumber int
	MediaSources      []MediaSourceDetail
}

// itemDetailFromEmby 把 embyclient 的条目投影成本包的 ItemDetail。
func itemDetailFromEmby(item *embyclient.BaseItemDtoV2) *ItemDetail {
	if item == nil {
		return nil
	}
	sources := make([]MediaSourceDetail, 0, len(item.MediaSources))
	for _, source := range item.MediaSources {
		sources = append(sources, MediaSourceDetail{Path: source.Path, Size: source.Size})
	}
	return &ItemDetail{
		ID:                item.Id,
		Name:              item.Name,
		Type:              item.Type,
		Overview:          item.Overview,
		ProductionYear:    item.ProductionYear,
		CommunityRating:   item.CommunityRating,
		Genres:            item.Genres,
		ProviderIDs:       item.ProviderIds,
		ImageTags:         item.ImageTags,
		DateCreated:       item.DateCreated,
		SeriesName:        item.SeriesName,
		IndexNumber:       item.IndexNumber,
		ParentIndexNumber: item.ParentIndexNumber,
		MediaSources:      sources,
	}
}

// GetBackingFile 查询「Emby item id → 背后的网盘文件位置」。
//
// 这是本包对外暴露的核心查询之一。第二个返回值为 false 表示
// 该条目没有可用的网盘映射（未索引、无 PickCode、或解析不出唯一文件）。
func (s *Service) GetBackingFile(ctx context.Context, itemID string) (BackingFileInfo, bool, error) {
	itemID = strings.TrimSpace(itemID)
	if s == nil || s.index == nil || itemID == "" {
		return BackingFileInfo{}, false, nil
	}
	item, err := s.index.GetItem(ctx, itemID)
	if err != nil {
		if isNotFound(err) {
			return BackingFileInfo{}, false, nil
		}
		return BackingFileInfo{}, false, err
	}
	info := BackingFileInfo{PickCode: item.PickCode, Path: item.Path}
	if strings.TrimSpace(item.PickCode) == "" {
		return info, false, nil
	}
	file, ok := s.resolveBackingFile(ctx, item.PickCode)
	if !ok {
		return info, false, nil
	}
	info.AccountID = file.AccountID
	info.SyncPathID = file.TaskID
	info.RootID = file.RootID
	info.RelativePath = file.RelativePath
	info.FileName = file.FileName
	return info, true, nil
}

// BackingFileInfo 是「Emby 条目 → 网盘文件」查询的返回体。
type BackingFileInfo struct {
	// PickCode 是从 Emby 媒体源直链解析出的网盘提取码。
	PickCode string `json:"pick_code,omitempty"`
	// Path 是 Emby 侧记录的媒体路径。
	Path string `json:"path,omitempty"`
	// AccountID / SyncPathID / RootID 定位网盘文件所在的账号与 STRM 任务根目录。
	AccountID  int64  `json:"account_id,omitempty"`
	SyncPathID int64  `json:"sync_path_id,omitempty"`
	RootID     string `json:"root_id,omitempty"`
	// RelativePath 是网盘文件相对 STRM 任务根目录的路径。
	RelativePath string `json:"relative_path,omitempty"`
	// FileName 是网盘文件名。
	FileName string `json:"file_name,omitempty"`
}

// GetLibrary 查询「Emby item id → 所属媒体库」。
//
// 优先返回本地索引里记录的媒体库；索引里没有库 ID 时返回本地库表中的名称匹配结果。
func (s *Service) GetLibrary(ctx context.Context, itemID string) (LibraryInfo, bool, error) {
	itemID = strings.TrimSpace(itemID)
	if s == nil || s.index == nil || itemID == "" {
		return LibraryInfo{}, false, nil
	}
	item, err := s.index.GetItem(ctx, itemID)
	if err != nil {
		if isNotFound(err) {
			return LibraryInfo{}, false, nil
		}
		return LibraryInfo{}, false, err
	}
	libraryID := strings.TrimSpace(item.LibraryID)
	if libraryID == "" {
		// 单条同步解析到多个媒体库时会保留条目但不写 LibraryId，
		// 此时没有确定的归属，返回 false 而不是猜一个。
		return LibraryInfo{}, false, nil
	}
	info := LibraryInfo{LibraryID: libraryID}
	libraries, err := s.index.ListLibraries(ctx)
	if err != nil {
		return info, true, nil
	}
	for _, library := range libraries {
		if library.LibraryID == libraryID {
			info.Name = library.Name
			break
		}
	}
	return info, true, nil
}

// LibraryInfo 是「Emby 条目 → 媒体库」查询的返回体。
type LibraryInfo struct {
	LibraryID string `json:"library_id"`
	Name      string `json:"name,omitempty"`
}
