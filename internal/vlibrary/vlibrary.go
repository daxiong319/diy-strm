// Package vlibrary Emby 虚拟库/榜单合集（对齐 tgto123 ranking_virtual_libraries 语义）：
// 通过 Emby API 把榜单条目创建为 Emby「合集」（BoxSet/Collections），
// 榜单来自 discovery.Rankings 已聚合的 TMDB/豆瓣/流媒体三源。
// 设置存 discovery_settings（键 ranking_virtual_libraries）。
package vlibrary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"gorm.io/gorm"
)

// Settings 虚拟库设置（对齐 tgto123 basicConfigPayload 形状）
type Settings struct {
	Enabled     bool              `json:"enabled"`
	Rankings    []RankingRef      `json:"rankings"`    // 勾选的榜单 {provider, media_type}
	MaxItems    int               `json:"max_items"`   // 每个合集最多条目数（0=50）
	Collections map[string]string `json:"collections"` // 已创建合集缓存 {key: collection_item_id}
	UpdatedAt   string            `json:"updated_at"`
}

// RankingRef 榜单引用
type RankingRef struct {
	Provider  string `json:"provider"`   // douban / tmdb / netflix / maoyan...
	MediaType string `json:"media_type"` // movie / tv
}

// collectionKey 榜单合集唯一键（provider 可含冒号，如 douban:movie_weekly_best）
func collectionKey(ref RankingRef) string {
	return strings.ToLower(ref.Provider) + "@" + strings.ToLower(ref.MediaType)
}

// GetSettings 读取设置
func GetSettings() *Settings {
	cfg := &Settings{
		MaxItems:    50,
		Collections: map[string]string{},
	}
	var row struct{ Value string }
	if err := db.Db.Table("discovery_settings").Select("value").Where("key = ?", "ranking_virtual_libraries").Take(&row).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			helpers.AppLogger.Warnf("虚拟库设置读取失败：%v", err)
		}
		return cfg
	}
	_ = json.Unmarshal([]byte(row.Value), cfg)
	if cfg.Collections == nil {
		cfg.Collections = map[string]string{}
	}
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 50
	}
	return cfg
}

// SaveSettings 保存设置
func SaveSettings(cfg *Settings) error {
	cfg.UpdatedAt = time.Now().Format(time.RFC3339)
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 50
	}
	if cfg.Collections == nil {
		cfg.Collections = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	setting := discovery.DiscoverySetting{Key: "ranking_virtual_libraries", Value: string(raw), UpdatedAt: time.Now()}
	return db.Db.Save(&setting).Error
}

// EmbyAPI 虚拟库需要的最小 Emby 客户端接口（internal/embyclient-rest-go.Client 满足）
type EmbyAPI interface {
	CreateCollection(ctx context.Context, name string) (string, error)
	AddToCollection(ctx context.Context, collectionID string, itemIDs []string) error
	RefreshLibrary(libraryID string, libraryName string) error
}

// SyncCollections 按设置把勾选榜单同步为 Emby 合集：
// 榜单条目 → Emby 按 TMDB ID 搜索 item → 建/更新合集 → 批量加入。
// 返回每个合集的结果摘要（供前端展示）。
func SyncCollections(ctx context.Context, api EmbyAPI) ([]SyncResult, error) {
	cfg := GetSettings()
	if !cfg.Enabled {
		return nil, fmt.Errorf("虚拟库未启用")
	}
	results := make([]SyncResult, 0, len(cfg.Rankings))
	for _, ref := range cfg.Rankings {
		res := SyncResult{Key: collectionKey(ref), Provider: ref.Provider, MediaType: ref.MediaType}
		page, err := discovery.Rankings(ctx, ref.Provider, "", ref.MediaType, 1, false)

		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		max := cfg.MaxItems
		created := 0
		var itemIDs []string
		for _, item := range page.Items {
			if created >= max {
				break
			}
			tmdbID := item.TMDBID
			if tmdbID <= 0 {
				// 豆瓣等来源无 TMDB ID：按标题+年份搜索 TMDB 补全后再匹配 Emby
				tmdbID = resolveTmdbID(ctx, item)
				if tmdbID <= 0 {
					continue
				}
			}
			if embyID, ok := findEmbyItemByTmdb(ctx, api, tmdbID, item.MediaType); ok {
				itemIDs = append(itemIDs, embyID)
				created++
			}
		}
		if created == 0 {
			res.Error = "榜单条目均未入库，跳过合集创建"
			results = append(results, res)
			continue
		}
		name := collectionName(ref)
		collID, err := ensureCollection(ctx, api, cfg, collectionKey(ref), name)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		if err := api.AddToCollection(ctx, collID, itemIDs); err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		res.Created = created
		res.CollectionName = name
		results = append(results, res)
	}
	return results, nil
}

// SyncResult 单个合集同步结果
type SyncResult struct {
	Key            string `json:"key"`
	Provider       string `json:"provider"`
	MediaType      string `json:"media_type"`
	CollectionName string `json:"collection_name,omitempty"`
	Created        int    `json:"created"`
	Error          string `json:"error,omitempty"`
}

// collectionName 合集名（榜单来源+类型）
func collectionName(ref RankingRef) string {
	providerLabels := map[string]string{
		"douban": "豆瓣", "maoyan": "猫眼", "tencent": "腾讯视频",
		"netflix": "Netflix", "hbo": "HBO", "apple": "Apple TV+",
		"disney": "Disney+", "crunchyroll": "Crunchyroll", "prime": "Prime Video",
		"popular": "热门", "tmdb": "TMDB",
	}
	providerKey := strings.ToLower(ref.Provider)
	if strings.Contains(providerKey, ":") {
		providerKey = strings.SplitN(providerKey, ":", 2)[0] + ":" + strings.SplitN(providerKey, ":", 2)[1]
	}
	// 完整 provider（如 douban:movie_weekly_best）直接映射收藏名
	fullLabels := map[string]string{
		"douban:movie_weekly_best": "豆瓣一周口碑电影", "douban:tv_weekly_best": "豆瓣一周口碑剧集",
		"douban:movie_top250": "豆瓣 Top250",
	}
	if label, ok := fullLabels[strings.ToLower(ref.Provider)]; ok {
		mediaLabel := "电影"
		if ref.MediaType == "tv" {
			mediaLabel = "剧集"
		}
		_ = mediaLabel
		return label
	}
	label, ok := providerLabels[strings.ToLower(strings.SplitN(ref.Provider, ":", 2)[0])]
	if !ok {
		label = ref.Provider
	}
	mediaLabel := "电影"
	if ref.MediaType == "tv" {
		mediaLabel = "剧集"
	}
	return label + "·" + mediaLabel + "榜单"
}

// ensureCollection 找/建合集（按名字搜索；缓存记录 item id）
func ensureCollection(ctx context.Context, api EmbyAPI, cfg *Settings, key, name string) (string, error) {
	if id, ok := cfg.Collections[key]; ok && id != "" {
		return id, nil
	}
	id, err := api.CreateCollection(ctx, name)
	if err != nil {
		return "", err
	}
	cfg.Collections[key] = id
	if err := SaveSettings(cfg); err != nil {
		helpers.AppLogger.Warnf("虚拟库合集缓存写入失败：%v", err)
	}
	return id, nil
}

// findEmbyItemByTmdb 按 TMDB ID 查 Emby 条目 id；EmbyAPI 同时承担合集创建，
// 按 TMDB 找条目通过 optional 接口（Client 实现了 TmdbFinder）。
// doubanTmdbCache 豆瓣条目 → TMDB ID 映射缓存（进程级，避免重复搜索）
var doubanTmdbCache sync.Map

// resolveTmdbID 为无 TMDB ID 的榜单条目（豆瓣等）按标题+年份搜索 TMDB 补全。
// 失败返回 0（该条目跳过）。
func resolveTmdbID(ctx context.Context, item discovery.Item) int64 {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return 0
	}
	cacheKey := item.MediaType + ":" + title + ":" + fmt.Sprintf("%d", item.Year)
	if v, ok := doubanTmdbCache.Load(cacheKey); ok {
		return v.(int64)
	}
	client := models.GlobalScrapeSettings.GetTmdbClient()
	if client == nil {
		return 0
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()

	var tmdbID int64
	if item.MediaType == "tv" {
		if resp, err := client.SearchTv(title, item.Year, language, false); err == nil && len(resp.Results) > 0 {
			tmdbID = int64(resp.Results[0].ID)
		}
	} else {
		if resp, err := client.SearchMovie(title, item.Year, language, false, false); err == nil && len(resp.Results) > 0 {
			tmdbID = int64(resp.Results[0].ID)
		}
	}
	if tmdbID > 0 {
		helpers.AppLogger.Infof("虚拟库：豆瓣条目「%s (%d)」已映射 TMDB %d", title, item.Year, tmdbID)
		doubanTmdbCache.Store(cacheKey, tmdbID)
	}
	return tmdbID
}

func findEmbyItemByTmdb(ctx context.Context, api EmbyAPI, tmdbID int64, mediaType string) (string, bool) {
	finder, ok := api.(TmdbFinder)
	if !ok {
		return "", false
	}
	id, err := finder.FindItemByTmdb(ctx, tmdbID, mediaType == "tv")
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// TmdbFinder 按 TMDB 查条目的可选能力（embyclientrestgo.Client 实现）
type TmdbFinder interface {
	FindItemByTmdb(ctx context.Context, tmdbID int64, isTV bool) (string, error)
}
