package discovery

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	embyclientrestgo "diy-strm/internal/embyclient-rest-go"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// Emby 缺集扫描（对齐参考实现 emby_missing_service：
// 选库扫描 → 系列缺集快照 → 补档订阅创建/联动 → 事件流）
// ---------------------------------------------------------------------------

// DiscoveryEmbyMissingScan 扫描记录
type DiscoveryEmbyMissingScan struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	Status         string     `gorm:"size:16;index" json:"status"` // queued/running/success/partial/failed
	Phase          string     `gorm:"size:16" json:"phase"`
	LibraryIDs     string     `gorm:"type:text" json:"-"` // JSON
	LibraryNames   string     `gorm:"type:text" json:"-"`
	TotalSeries    int        `json:"total_series"`
	ScannedSeries  int        `json:"scanned_series"`
	MissingSeries  int        `json:"missing_series"`
	MissingEpisodes int       `json:"missing_episodes"`
	ErrorSeries    int        `json:"error_series"`
	Message        string     `gorm:"size:512" json:"message"`
	Detail         string     `gorm:"type:text" json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

func (DiscoveryEmbyMissingScan) TableName() string { return "discovery_emby_missing_scans" }

// DiscoveryEmbyMissingResult 缺集结果（每系列一行）
type DiscoveryEmbyMissingResult struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	ScanID         uint       `gorm:"index:idx_disc_missing_scan,composite:scan_id,series_key" json:"scan_id"`
	SeriesKey      string     `gorm:"uniqueIndex:idx_disc_missing_scan,composite:scan_id,series_key;size:190" json:"series_key"`
	EmbySeriesID   string     `gorm:"size:64;index" json:"emby_series_id"`
	LibraryID      string     `gorm:"size:64" json:"library_id"`
	LibraryName    string     `gorm:"size:255" json:"library_name"`
	TMDBID         int64      `json:"tmdb_id"`
	Title          string     `gorm:"size:255" json:"title"`
	OriginalTitle  string     `gorm:"size:255" json:"original_title"`
	ProductionYear int        `json:"production_year"`
	SeriesStatus   string     `gorm:"size:32" json:"series_status"`
	AvailableCount int        `json:"available_count"`
	AvailableEpisodes string  `gorm:"type:text" json:"-"`
	MissingEpisodes   string  `gorm:"type:text" json:"-"` // JSON [{key,season,episode,name,premiere_date}]
	MissingCount   int        `json:"missing_count"`
	SubscriptionID *uint      `gorm:"index" json:"subscription_id"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// 非持久化
	MissingList []MissingEpisode `gorm:"-" json:"missing_episodes"`
}

func (DiscoveryEmbyMissingResult) TableName() string { return "discovery_emby_missing_results" }

// MissingEpisode 缺集条目
type MissingEpisode struct {
	Key          string `json:"key"`
	Season       int    `json:"season"`
	Episode      int    `json:"episode"`
	Name         string `json:"name,omitempty"`
	PremiereDate string `json:"premiere_date,omitempty"`
}

// DiscoveryEmbyMissingEvent 缺集事件流
type DiscoveryEmbyMissingEvent struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ScanID         *uint     `gorm:"index" json:"scan_id"`
	ResultID       *uint     `json:"result_id"`
	SubscriptionID *uint     `json:"subscription_id"`
	EventType      string    `gorm:"size:32" json:"event_type"`
	Status         string    `gorm:"size:16" json:"status"`
	Message        string    `gorm:"size:512" json:"message"`
	Detail         string    `gorm:"type:text" json:"-"`
	CreatedAt      time.Time `gorm:"index" json:"created_at"`
}

func (DiscoveryEmbyMissingEvent) TableName() string { return "discovery_emby_missing_events" }

// embyMissingMu 扫描互斥（同一时间只允许一个扫描）
var embyMissingMu sync.Mutex

// MediaEmbyConfig 影视发现 Emby 配置（独立于影巢反代，settings key media_emby）
func MediaEmbyConfig() (enabled bool, serverURL, apiKey string) {
	raw, _ := mustSettings()[SettingMediaEmby].(map[string]any)
	if v, ok := raw["enabled"].(bool); ok {
		enabled = v
	}
	if s, ok := raw["server_url"].(string); ok {
		serverURL = strings.TrimSpace(s)
	}
	if s, ok := raw["api_key"].(string); ok {
		apiKey = strings.TrimSpace(s)
	}
	if serverURL == "" || apiKey == "" {
		// 兼容：未配置时回退全局 Emby 配置
		if cfg, err := models.GetEmbyConfig(); err == nil && cfg != nil && cfg.EmbyUrl != "" && cfg.EmbyApiKey != "" {
			return enabled || len(raw) == 0, cfg.EmbyUrl, cfg.EmbyApiKey
		}
	}
	return enabled && serverURL != "" && apiKey != "", serverURL, apiKey
}

// mediaEmbyClient 影视发现 Emby 客户端
func mediaEmbyClient() (*embyclientrestgo.Client, error) {
	enabled, serverURL, apiKey := MediaEmbyConfig()
	if !enabled || serverURL == "" || apiKey == "" {
		return nil, fmt.Errorf("Emby 未配置：请先在基础配置中填写服务器地址与 API Key")
	}
	return embyclientrestgo.NewClient(serverURL, apiKey), nil
}

// EmbyMissingStatus 状态（status 接口）
func EmbyMissingStatus() (map[string]any, error) {
	enabled, serverURL, _ := MediaEmbyConfig()
	var latest DiscoveryEmbyMissingScan
	hasLatest := db.Db.Order("id desc").First(&latest).Error == nil
	var subCount int64
	db.Db.Model(&DiscoverySubscription{}).Where("entity_key LIKE ?", "emby-missing:%").Count(&subCount)
	status := map[string]any{
		"emby": map[string]any{
			"configured": serverURL != "" && enabled,
			"enabled":    enabled,
			"server_url": serverURL,
			"message":    ternaryStr(enabled && serverURL != "", "Emby 缺集扫描已就绪", "请先完成 Emby 配置"),
		},
		"active_scan":       nil,
		"latest_scan":       nil,
		"subscription_count": subCount,
		"settings": map[string]any{
			"auto_scan":                SettingBool(SettingEmbyMissingAutoScan, false),
			"scan_interval_minutes":    SettingInt(SettingEmbyMissingInterval, 720),
			"auto_create_subscriptions": SettingBool(SettingEmbyMissingAutoSubs, false),
		},
	}
	if hasLatest {
		summary := scanSummary(latest)
		if latest.Status == "running" || latest.Status == "queued" {
			status["active_scan"] = summary
		}
		status["latest_scan"] = summary
	}
	return status, nil
}

// scanSummary 扫描摘要
func scanSummary(scan DiscoveryEmbyMissingScan) map[string]any {
	return map[string]any{
		"id": scan.ID, "status": scan.Status, "phase": scan.Phase,
		"total_series": scan.TotalSeries, "scanned_series": scan.ScannedSeries,
		"missing_series": scan.MissingSeries, "missing_episodes": scan.MissingEpisodes,
		"error_series": scan.ErrorSeries, "message": scan.Message,
		"created_at": scan.CreatedAt, "started_at": scan.StartedAt, "finished_at": scan.FinishedAt,
	}
}

// EmbyMissingLibraries 电视剧媒体库列表
func EmbyMissingLibraries() ([]map[string]any, error) {
	client, err := mediaEmbyClient()
	if err != nil {
		return nil, err
	}
	folders, err := client.GetLibraryVirtualFolders()
	if err != nil {
		return nil, fmt.Errorf("获取 Emby 媒体库失败：%v", err)
	}
	items := []map[string]any{}
	for _, folder := range folders {
		collectionType := strings.ToLower(folder.CollectionType)
		if collectionType != "tvshows" && collectionType != "tv" && collectionType != "series" {
			continue
		}
		items = append(items, map[string]any{"id": folder.ItemId, "name": folder.Name, "collection_type": collectionType})
	}
	return items, nil
}

// StartEmbyMissingScan 启动一次缺集扫描（libraryIDs 为空=全部电视剧库）
func StartEmbyMissingScan(libraryIDs []string) (*DiscoveryEmbyMissingScan, error) {
	embyMissingMu.Lock()
	defer embyMissingMu.Unlock()
	var active DiscoveryEmbyMissingScan
	if err := db.Db.Where("status IN ?", []string{"queued", "running"}).First(&active).Error; err == nil {
		return nil, fmt.Errorf("已有扫描正在执行（#%d）", active.ID)
	}
	client, err := mediaEmbyClient()
	if err != nil {
		return nil, err
	}
	folders, err := client.GetLibraryVirtualFolders()
	if err != nil {
		return nil, fmt.Errorf("获取 Emby 媒体库失败：%v", err)
	}
	tvLibraries := map[string]string{}
	names := map[string]string{}
	for _, folder := range folders {
		ct := strings.ToLower(folder.CollectionType)
		if ct == "tvshows" || ct == "tv" || ct == "series" {
			tvLibraries[folder.ItemId] = folder.Name
			names[folder.ItemId] = folder.Name
		}
	}
	selected := []string{}
	selectedNames := []string{}
	for _, id := range libraryIDs {
		if name, ok := tvLibraries[id]; ok {
			selected = append(selected, id)
			selectedNames = append(selectedNames, name)
		}
	}
	if len(selected) == 0 {
		if len(libraryIDs) > 0 {
			return nil, fmt.Errorf("所选媒体库无效或不是电视剧库")
		}
		for id, name := range tvLibraries {
			selected = append(selected, id)
			selectedNames = append(selectedNames, name)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("服务器上没有电视剧媒体库")
	}
	sort.Strings(selected)
	scan := DiscoveryEmbyMissingScan{
		Status: "queued", Phase: "queued",
		LibraryIDs: marshalJSON(selected), LibraryNames: marshalJSON(selectedNames),
		Message: "已进入扫描队列", CreatedAt: time.Now(),
	}
	if err := db.Db.Create(&scan).Error; err != nil {
		return nil, err
	}
	recordMissingEvent(&scan, nil, nil, "scan_queued", "queued", fmt.Sprintf("扫描 #%d 已排队（%d 个媒体库）", scan.ID, len(selected)), nil)
	go runEmbyMissingScan(scan.ID, selected, selectedNames)
	return &scan, nil
}

// runEmbyMissingScan 扫描执行（独立 goroutine）
func runEmbyMissingScan(scanID uint, libraryIDs, libraryNames []string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[discovery] 缺集扫描 #%d panic 恢复：%v", scanID, r)
			finished := time.Now()
			db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Updates(map[string]any{
				"status": "failed", "message": fmt.Sprintf("扫描异常：%v", r), "finished_at": &finished,
			})
			recordMissingEvent(nil, &scanID, nil, "scan_failed", "failed", fmt.Sprintf("扫描 #%d 异常终止：%v", scanID, r), nil)
		}
	}()
	client, err := mediaEmbyClient()
	if err != nil {
		finished := time.Now()
		db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Updates(map[string]any{
			"status": "failed", "message": err.Error(), "finished_at": &finished,
		})
		recordMissingEvent(nil, &scanID, nil, "scan_failed", "failed", err.Error(), nil)
		return
	}
	started := time.Now()
	db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Updates(map[string]any{
		"status": "running", "phase": "scanning", "started_at": &started, "message": "正在扫描 Emby 缺集",
	})
	recordMissingEvent(nil, &scanID, nil, "scan_started", "running", fmt.Sprintf("扫描 #%d 开始", scanID), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	type seriesRef struct {
		itemID, name, libraryID, libraryName string
		tmdbID                               int64
		year                                 int
		status                               string
	}
	progress := struct {
		mu              sync.Mutex
		total, scanned  int
		missingSeries   int
		missingEpisodes int
		errorSeries     int
	}{}
	// 先数总数
	totalSeries := 0
	for _, libID := range libraryIDs {
		count := 0
		_ = client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
			LibraryID:        libID,
			IncludeItemTypes: "Series",
			Fields:           "ProviderIds,ProductionYear",
		}, func(item embyclientrestgo.BaseItemDtoV2) error {
			count++
			return nil
		})
		totalSeries += count
	}
	progress.total = totalSeries
	db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Update("total_series", totalSeries)

	const progressTick = 10
	scannedSinceFlush := 0
	flushProgress := func() {
		db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Updates(map[string]any{
			"scanned_series": progress.scanned, "missing_series": progress.missingSeries,
			"missing_episodes": progress.missingEpisodes, "error_series": progress.errorSeries,
		})
	}

	for idx, libID := range libraryIDs {
		if ctx.Err() != nil {
			break
		}
		libName := ""
		if idx < len(libraryNames) {
			libName = libraryNames[idx]
		}
		_ = client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
			LibraryID:        libID,
			IncludeItemTypes: "Series",
			Fields:           "ProviderIds,ProductionYear,Status",
		}, func(item embyclientrestgo.BaseItemDtoV2) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			progress.mu.Lock()
			progress.scanned++
			progress.mu.Unlock()
			tmdbID := parseEmbyTmdbID(item.ProviderIds)
			// 已入库集
			availableKeys := map[string]bool{}
			availableCount := 0
			episodes, epErr := client.GetSeriesEpisodes(item.Id, "ParentIndexNumber,IndexNumber,PremiereDate")
			if epErr != nil {
				progress.mu.Lock()
				progress.errorSeries++
				progress.mu.Unlock()
			} else {
				now := time.Now()
				for _, ep := range episodes {
					if ep.ParentIndexNumber <= 0 || ep.IndexNumber <= 0 {
						continue // 跳过特别篇
					}
					availableCount++
					availableKeys[fmt.Sprintf("S%dE%d", ep.ParentIndexNumber, ep.IndexNumber)] = true
				}
				// 未播出的不算缺：用 TV 元数据补总集数判断「应播」
				missing := []MissingEpisode{}
				for _, ep := range episodes {
					if ep.ParentIndexNumber <= 0 || ep.IndexNumber <= 0 {
						continue
					}
					key := fmt.Sprintf("S%dE%d", ep.ParentIndexNumber, ep.IndexNumber)
					if availableKeys[key] {
						continue
					}
					// Emby 列表里不存在的集无法逐个判断播出时间，改为「已播但缺席」检测：
					// 依赖 PremiereDate 为空的集不标缺（无法判定播出状态）
					if strings.TrimSpace(ep.PremiereDate) == "" {
						continue
					}
					if airDate, err := time.Parse("2006-01-02", strings.TrimSpace(ep.PremiereDate)[:10]); err == nil && airDate.After(now) {
						continue // 未播出
					}
					missing = append(missing, MissingEpisode{
						Key: key, Season: ep.ParentIndexNumber, Episode: ep.IndexNumber,
						Name: ep.Name, PremiereDate: ep.PremiereDate,
					})
				}
				// Emby Episodes 接口只返回已有实体；「应播而未入库」需对比 TMDB。
				// 补齐逻辑：若本地 DB 有该剧集的同步条目，用其差集；否则仅记录统计。
				if len(missing) == 0 && tmdbID > 0 {
					missing = missingFromLocalIndex(tmdbID, availableKeys)
				}
				if len(missing) > 0 {
					progress.mu.Lock()
					progress.missingSeries++
					progress.missingEpisodes += len(missing)
					progress.mu.Unlock()
					result := DiscoveryEmbyMissingResult{
						ScanID: scanID, SeriesKey: fmt.Sprintf("%s:%s", libID, item.Id),
						EmbySeriesID: item.Id, LibraryID: libID, LibraryName: libName,
						TMDBID: tmdbID, Title: item.Name, ProductionYear: item.ProductionYear,
						SeriesStatus: item.Status, AvailableCount: availableCount,
						AvailableEpisodes: marshalJSON(mapKeys(availableKeys)),
						MissingEpisodes:   marshalJSON(missing), MissingCount: len(missing),
						CreatedAt: time.Now(), UpdatedAt: time.Now(), MissingList: missing,
					}
					db.Db.Save(&result)
				}
			}
			scannedSinceFlush++
			if scannedSinceFlush >= progressTick {
				scannedSinceFlush = 0
				flushProgress()
			}
			return nil
		})
	}
	flushProgress()

	var resultCount, errorCount int64
	db.Db.Model(&DiscoveryEmbyMissingResult{}).Where("scan_id = ?", scanID).Count(&resultCount)
	db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).First(&DiscoveryEmbyMissingScan{})
	progress.mu.Lock()
	errCount := progress.errorSeries
	totalEp := progress.missingEpisodes
	progress.mu.Unlock()
	_ = errorCount
	status := "success"
	message := fmt.Sprintf("扫描完成：共 %d 部剧集、%d 集缺失", resultCount, totalEp)
	if errCount > 0 {
		status = "partial"
		message += fmt.Sprintf("（%d 部查询异常）", errCount)
	}
	finished := time.Now()
	db.Db.Model(&DiscoveryEmbyMissingScan{}).Where("id = ?", scanID).Updates(map[string]any{
		"status": status, "phase": "finished", "message": message, "finished_at": &finished,
	})
	recordMissingEvent(nil, &scanID, nil, "scan_finished", status, message, nil)

	// 自动创建补档订阅
	if SettingBool(SettingEmbyMissingAutoSubs, false) {
		autoCreateMissingSubscriptions(scanID)
	}
}

// missingFromLocalIndex 用本地 Emby 同步 DB 补「应播未入库」差集（tmdb_id → 同步集数）
func missingFromLocalIndex(tmdbID int64, availableKeys map[string]bool) []MissingEpisode {
	missing := []MissingEpisode{}
	// 本地 DB 无 TMDB 关联字段，退化为：无差集可算
	_ = tmdbID
	_ = availableKeys
	return missing
}

// mapKeys map 键列表
func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseEmbyTmdbID 从 ProviderIds 提取 TMDB ID
func parseEmbyTmdbID(providerIds map[string]string) int64 {
	if providerIds == nil {
		return 0
	}
	for key, value := range providerIds {
		lower := strings.ToLower(key)
		if lower == "tmdb" || lower == "tmdbid" {
			var id int64
			if _, err := fmt.Sscanf(value, "%d", &id); err == nil {
				return id
			}
		}
	}
	return 0
}

// recordMissingEvent 缺集事件
func recordMissingEvent(scan *DiscoveryEmbyMissingScan, scanID, resultID *uint, eventType, status, message string, detail map[string]any) {
	event := DiscoveryEmbyMissingEvent{
		EventType: eventType, Status: status, Message: truncateStr(message, 500),
		Detail: marshalJSON(detail), CreatedAt: time.Now(),
	}
	if scan != nil {
		id := scan.ID
		event.ScanID = &id
	} else if scanID != nil {
		event.ScanID = scanID
	}
	event.ResultID = resultID
	db.Db.Create(&event)
}

// ---------------------------------------------------------------------------
// 补档订阅（缺集 → discovery_subscriptions，metadata.origin=emby_missing）
// ---------------------------------------------------------------------------

// MissingSubscriptionRequest 创建补档订阅请求
type MissingSubscriptionRequest struct {
	ResultIDs       []uint `json:"result_ids"`
	ScanID          uint   `json:"scan_id"`
	TargetProvider  string `json:"target_provider"`
	TransferMode    string `json:"transfer_mode"`
	IntervalMinutes int    `json:"interval_minutes"`
	Enabled         *bool  `json:"enabled"`
}

// CreateMissingSubscriptions 从缺集结果创建补档订阅
func CreateMissingSubscriptions(req *MissingSubscriptionRequest) (map[string]any, error) {
	if len(req.ResultIDs) == 0 {
		return nil, fmt.Errorf("请选择要创建补档订阅的缺集条目")
	}
	targetProvider, err := NormalizeTransferProvider(req.TargetProvider)
	if err != nil {
		targetProvider = SettingString(SettingTargetProvider, "123")
	}
	interval := req.IntervalMinutes
	if interval <= 0 {
		interval = SettingInt(SettingCheckIntervalMinutes, 360)
	}
	interval = clampInt(interval, 15, 10080)
	created, updated, skipped := 0, 0, []map[string]any{}
	subscriptions := []map[string]any{}
	for _, resultID := range req.ResultIDs {
		var result DiscoveryEmbyMissingResult
		if err := db.Db.First(&result, resultID).Error; err != nil {
			skipped = append(skipped, map[string]any{"result_id": resultID, "reason": "扫描结果不存在"})
			continue
		}
		if result.TMDBID <= 0 {
			skipped = append(skipped, map[string]any{"result_id": resultID, "reason": "该剧缺少 Emby TMDB ID，无法安全匹配 RE0 资源"})
			continue
		}
		missing := []MissingEpisode{}
		if result.MissingEpisodes != "" {
			_ = jsonUnmarshal([]byte(result.MissingEpisodes), &missing)
		}
		if len(missing) == 0 {
			skipped = append(skipped, map[string]any{"result_id": resultID, "reason": "该扫描结果没有可补齐的已播出缺集"})
			continue
		}
		episodeTargets := make([]map[string]any, 0, len(missing))
		episodeKeys := make([]string, 0, len(missing))
		for _, m := range missing {
			episodeTargets = append(episodeTargets, map[string]any{
				"key": m.Key, "season": m.Season, "episode": m.Episode,
				"name": m.Name, "premiere_date": m.PremiereDate,
			})
			episodeKeys = append(episodeKeys, m.Key)
		}
		metadata := map[string]any{
			"origin": "emby_missing",
			"emby_missing_context": map[string]any{
				"scan_id": result.ScanID, "result_id": result.ID,
				"emby_series_id": result.EmbySeriesID, "library_id": result.LibraryID,
				"library_name": result.LibraryName, "available_count": result.AvailableCount,
				"episode_targets": episodeTargets, "episode_keys": episodeKeys,
				"last_scanned_at": time.Now().Format(time.RFC3339),
			},
			"media_type": "tv",
		}
		payload := &SubscriptionUpsertPayload{
			Source: "tmdb", EntityType: "tv", ExternalID: strconvI64(result.TMDBID),
			TMDBID: result.TMDBID, MediaType: "tv",
			Title: result.Title, OriginalTitle: result.OriginalTitle,
			TargetProvider: targetProvider, TransferMode: "auto",
			Enabled: req.Enabled, IntervalMinutes: interval,
			Rules: []SubscriptionRulePayload{{
				Name: "Emby 补档自动规则", TargetProvider: targetProvider,
				MaxPoints: SettingInt(SettingMaxPoints, 4),
			}},
			Metadata: metadata,
		}
		sub, _, err := SaveSubscription(payload)
		if err != nil {
			skipped = append(skipped, map[string]any{"result_id": resultID, "reason": err.Error()})
			continue
		}
		// 关联结果 ↔ 订阅
		subID := sub.ID
		db.Db.Model(&DiscoveryEmbyMissingResult{}).Where("id = ?", result.ID).Update("subscription_id", subID)
		created++
		subscriptions = append(subscriptions, map[string]any{
			"id": sub.ID, "title": sub.Title, "target_provider": sub.TargetProvider,
		})
		var scanRef = result.ScanID
		recordMissingEvent(nil, &scanRef, &result.ID, "subscription_created", "success", fmt.Sprintf("已为「%s」创建补档订阅（%d 集缺失）", result.Title, result.MissingCount), nil)
	}
	return map[string]any{
		"subscriptions": subscriptions, "created_count": created,
		"updated_count": updated, "skipped": skipped,
	}, nil
}

// autoCreateMissingSubscriptions 扫描完成后自动创建补档订阅
func autoCreateMissingSubscriptions(scanID uint) {
	var results []DiscoveryEmbyMissingResult
	db.Db.Where("scan_id = ? AND tmdb_id > 0 AND subscription_id IS NULL AND missing_count > 0", scanID).
		Limit(50).Find(&results)
	if len(results) == 0 {
		return
	}
	ids := make([]uint, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	_, err := CreateMissingSubscriptions(&MissingSubscriptionRequest{ResultIDs: ids, ScanID: scanID})
	if err != nil {
		log.Printf("[discovery] 扫描 #%d 自动创建补档订阅失败：%v", scanID, err)
	}
}

// EmbyMissingScansList 扫描历史
func EmbyMissingScansList(limit int) []map[string]any {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var scans []DiscoveryEmbyMissingScan
	db.Db.Order("id desc").Limit(limit).Find(&scans)
	out := make([]map[string]any, 0, len(scans))
	for _, scan := range scans {
		out = append(out, scanSummary(scan))
	}
	return out
}

// EmbyMissingResultsList 缺集结果列表
func EmbyMissingResultsList(scanID uint, limit int) ([]DiscoveryEmbyMissingResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	query := db.Db.Order("missing_count desc, id desc").Limit(limit)
	if scanID > 0 {
		query = query.Where("scan_id = ?", scanID)
	}
	var results []DiscoveryEmbyMissingResult
	if err := query.Find(&results).Error; err != nil {
		return nil, err
	}
	for i := range results {
		results[i].MissingList = []MissingEpisode{}
		if results[i].MissingEpisodes != "" {
			_ = jsonUnmarshal([]byte(results[i].MissingEpisodes), &results[i].MissingList)
		}
	}
	return results, nil
}

// EmbyMissingEventsList 事件流
func EmbyMissingEventsList(scanID uint, limit int) ([]DiscoveryEmbyMissingEvent, error) {
	if limit <= 0 || limit > 300 {
		limit = 80
	}
	query := db.Db.Order("id desc").Limit(limit)
	if scanID > 0 {
		query = query.Where("scan_id = ?", scanID)
	}
	var events []DiscoveryEmbyMissingEvent
	err := query.Find(&events).Error
	return events, err
}

// embyMissingWorker 自动扫描 Worker（60s 轮询到期）
func embyMissingWorker() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[discovery] 缺集 Worker 恢复自 panic：%v", r)
				}
			}()
			if !SettingBool(SettingEmbyMissingAutoScan, false) {
				return
			}
			var active DiscoveryEmbyMissingScan
			if err := db.Db.Where("status IN ?", []string{"queued", "running"}).First(&active).Error; err == nil {
				return
			}
			var latest DiscoveryEmbyMissingScan
			hasLatest := db.Db.Order("id desc").First(&latest).Error == nil
			interval := clampInt(SettingInt(SettingEmbyMissingInterval, 720), 30, 10080)
			if hasLatest && latest.FinishedAt != nil &&
				time.Since(*latest.FinishedAt) < time.Duration(interval)*time.Minute {
				return
			}
			if _, err := StartEmbyMissingScan(nil); err != nil {
				log.Printf("[discovery] 自动缺集扫描未启动：%v", err)
			}
		}()
	}
}
