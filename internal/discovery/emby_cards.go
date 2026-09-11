package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	embyclientrestgo "diy-strm/internal/embyclient-rest-go"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// Emby 批量徽章 + 剧集进度预热（对齐参考实现 /api/media/emby/cards + preheat）
// ---------------------------------------------------------------------------

// EmbyCardSeriesInfo 卡片索引条目（TMDB ID → Emby 系列）
type EmbyCardSeriesInfo struct {
	ItemID string
	Name   string
	Status string
}

// MediaEmbyClientForCards 卡片查询客户端（未配置返回错误）
func MediaEmbyClientForCards() (*embyclientrestgo.Client, error) {
	return mediaEmbyClient()
}

// BuildEmbyCardIndex 构建 TMDB ID → Emby 条目索引（电影 + 剧集）
func BuildEmbyCardIndex() (map[int64]EmbyCardSeriesInfo, error) {
	client, err := mediaEmbyClient()
	if err != nil {
		return nil, err
	}
	folders, err := client.GetLibraryVirtualFolders()
	if err != nil {
		return nil, err
	}
	index := map[int64]EmbyCardSeriesInfo{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, folder := range folders {
		collectionType := strings.ToLower(folder.CollectionType)
		if collectionType != "movies" && collectionType != "movie" &&
			collectionType != "tvshows" && collectionType != "tv" && collectionType != "series" {
			continue
		}
		_ = client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
			LibraryID:        folder.ItemId,
			IncludeItemTypes: "Movie,Series",
			Fields:           "ProviderIds,ProductionYear,Status",
		}, func(item embyclientrestgo.BaseItemDtoV2) error {
			if tmdbID := parseEmbyTmdbID(item.ProviderIds); tmdbID > 0 {
				index[tmdbID] = EmbyCardSeriesInfo{ItemID: item.Id, Name: item.Name, Status: item.Status}
			}
			return nil
		})
	}
	return index, nil
}

// SeriesAvailableEpisodes 系列已入库集数（本地同步 DB 优先，回退 Emby REST）
func SeriesAvailableEpisodes(seriesItemID string) int {
	if seriesItemID == "" {
		return 0
	}
	var count int64
	if err := db.Db.Model(&models.EmbyMediaItem{}).
		Where("series_id = ? AND type = ?", seriesItemID, "Episode").Count(&count).Error; err == nil && count > 0 {
		return int(count)
	}
	// 本地 DB 无该系列 → 直接查 Emby
	client, err := mediaEmbyClient()
	if err != nil {
		return int(count)
	}
	episodes, err := client.GetSeriesEpisodes(seriesItemID, "IndexNumber")
	if err != nil {
		return int(count)
	}
	seen := map[string]bool{}
	for _, ep := range episodes {
		if ep.ParentIndexNumber <= 0 || ep.IndexNumber <= 0 {
			continue
		}
		seen[fmt.Sprintf("S%dE%d", ep.ParentIndexNumber, ep.IndexNumber)] = true
	}
	return len(seen)
}

// ---------------------------------------------------------------------------
// 剧集进度预热（TMDB 总集数/状态 → external cache，卡片总集数来源）
// ---------------------------------------------------------------------------

var (
	preheatQueue chan int64
	preheatOnce  sync.Once
)

// RequestTvProgressPreheat 投递剧集元数据预热任务
func RequestTvProgressPreheat(tmdbID int64) {
	if tmdbID <= 0 {
		return
	}
	preheatOnce.Do(func() {
		preheatQueue = make(chan int64, 512)
		go tvProgressWorker()
	})
	select {
	case preheatQueue <- tmdbID:
	default:
	}
}

// tvProgressWorker 剧集元数据预热循环
func tvProgressWorker() {
	for tmdbID := range preheatQueue {
		if tmdbTvMetaCached(tmdbID) {
			continue
		}
		client := models_globalTmdbClient()
		language := models_globalTmdbLanguage()
		detail, err := client.GetTvDetail(tmdbID, language)
		if err != nil {
			continue
		}
		externalCacheSet(tvMetaCacheKey(tmdbID), map[string]any{
			"total_episodes": detail.NumberOfEpisodes,
			"series_status":  detail.Status,
		}, 24*time.Hour)
		time.Sleep(200 * time.Millisecond)
	}
}

func tvMetaCacheKey(tmdbID int64) string {
	return fmt.Sprintf("media:emby-tv-meta:v1:%d", tmdbID)
}

func tmdbTvMetaCached(tmdbID int64) bool {
	return externalCacheGet(tvMetaCacheKey(tmdbID)) != ""
}

// TmdbTvTotalEpisodes 读取预热的总集数（卡片批量徽章用）
func TmdbTvTotalEpisodes(tmdbID int64) (int, string) {
	raw := externalCacheGet(tvMetaCacheKey(tmdbID))
	if raw == "" {
		return 0, ""
	}
	var meta struct {
		TotalEpisodes int    `json:"total_episodes"`
		SeriesStatus  string `json:"series_status"`
	}
	if jsonUnmarshal([]byte(raw), &meta) != nil {
		return 0, ""
	}
	return meta.TotalEpisodes, meta.SeriesStatus
}

// TestMediaEmbyConnection 测试 Emby 连接（返回识别的项目数）
func TestMediaEmbyConnection(serverURL, apiKey string) (int, error) {
	client := embyclientrestgo.NewClient(serverURL, apiKey)
	folders, err := client.GetLibraryVirtualFolders()
	if err != nil {
		return 0, fmt.Errorf("无法访问 Emby：%v", err)
	}
	total := 0
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, folder := range folders {
		ct := strings.ToLower(folder.CollectionType)
		if ct != "movies" && ct != "movie" && ct != "tvshows" && ct != "tv" && ct != "series" {
			continue
		}
		_ = client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
			LibraryID:        folder.ItemId,
			IncludeItemTypes: "Movie,Series",
		}, func(item embyclientrestgo.BaseItemDtoV2) error {
			total++
			return nil
		})
	}
	return total, nil
}

// UnlockRe0ForTransfer 详情页手动转存：tgto123 反代解锁
// （tgto123 的解锁与转存一体，此处仅校验 slug 有效并返回占位）
func UnlockRe0ForTransfer(ctx context.Context, slug string) (*hdhiveUnlock, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("缺少资源 slug")
	}
	return &hdhiveUnlock{URL: "tgto123://resource/" + slug, Title: slug}, nil
}

// MarshalJSONForTest 供控制器复用的 JSON 序列化
func MarshalJSONForTest(v any) string { return marshalJSON(v) }

// JSONRaw 导出 JSON 原文解析（控制器渲染 runs.detail 用）
func JSONRaw(raw string, out any) error {
	return json.Unmarshal([]byte(raw), out)
}
