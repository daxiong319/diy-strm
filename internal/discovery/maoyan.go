package discovery

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 猫眼榜单（对齐参考实现 rankings provider=maoyan：电视剧/网络剧/综艺/网络电影
// 四个榜单分组 + TMDB 匹配；上游为猫眼公开网页接口，有验证墙时降级为
// pending 状态由前端自动重试，语义与参考实现 feed_status=pending 一致）
// ---------------------------------------------------------------------------

// MaoyanCategory 猫眼榜单类别
type MaoyanCategory struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// MaoyanCategories 猫眼榜单类别表（meta 接口输出）
var MaoyanCategories = []MaoyanCategory{
	{Key: "tv", Label: "电视剧"},
	{Key: "web_tv", Label: "网络剧"},
	{Key: "variety", Label: "综艺"},
	{Key: "movie", Label: "网络电影"},
}

var maoyanCategories = MaoyanCategories

// maoyanHeatEntry 猫眼热度条目（网页接口解析子集）
type maoyanHeatEntry struct {
	Title string  `json:"title"`
	Heat  float64 `json:"heat"`
	Year  int     `json:"year"`
	Rank  int     `json:"rank"`
}

const (
	maoyanHTTPTimeout   = 12 * time.Second
	maoyanCacheTTL      = 45 * time.Minute
	maoyanEmptyCacheTTL = 5 * time.Minute
	maoyanUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

var maoyanHTTP = &httpTimeoutClient{timeout: maoyanHTTPTimeout}

// maoyanHeatEndpoints 各类别候选公开接口（按序尝试，均失败则降级 pending）
func maoyanHeatEndpoints(category string) []string {
	// 猫眼剧集/综艺热度（网页热度榜 JSON）
	base := "https://heat.maoyan.com/heat/heat-rank"
	switch category {
	case "tv":
		return []string{base + "?heatType=1&showType=1"}
	case "web_tv":
		return []string{base + "?heatType=1&showType=2"}
	case "variety":
		return []string{base + "?heatType=1&showType=3"}
	case "movie":
		return []string{"https://heat.maoyan.com/heat/network-movie-rank?date=&movieType=1"}
	default:
		return []string{base + "?heatType=1&showType=1"}
	}
}

// maoyanFetchCategory 抓取单个类别的热度榜（带外部缓存）
func maoyanFetchCategory(ctx context.Context, category string, force bool) ([]maoyanHeatEntry, error) {
	cacheKey := fmt.Sprintf("maoyan:heat:v1:%s", category)
	if !force {
		if raw := externalCacheGet(cacheKey); raw != "" {
			var cached struct {
				Entries []maoyanHeatEntry `json:"entries"`
				Error   string            `json:"error,omitempty"`
			}
			if jsonUnmarshal([]byte(raw), &cached) == nil {
				if cached.Error != "" && len(cached.Entries) == 0 {
					return nil, fmt.Errorf("%s", cached.Error)
				}
				return cached.Entries, nil
			}
		}
	}
	var entries []maoyanHeatEntry
	var lastErr error
	for _, endpoint := range maoyanHeatEndpoints(category) {
		list, err := maoyanRequestHeat(ctx, endpoint)
		if err == nil && len(list) > 0 {
			entries = list
			break
		}
		if err != nil {
			lastErr = err
		}
	}
	if len(entries) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("猫眼榜单暂无数据（接口可能被验证墙拦截）")
		}
		// 空结果短缓存 + 错误透传（前端按 pending 重试）
		externalCacheSet(cacheKey, map[string]any{"entries": []maoyanHeatEntry{}, "error": lastErr.Error()}, maoyanEmptyCacheTTL)
		return nil, lastErr
	}
	externalCacheSet(cacheKey, map[string]any{"entries": entries}, maoyanCacheTTL)
	return entries, nil
}

// MaoyanRankings 猫眼榜单（category=all 返回 4 组）
func MaoyanRankings(ctx context.Context, category string, force bool) ([]RankingGroup, bool, error) {
	category = strings.TrimSpace(category)
	cats := []string{}
	if category == "" || category == "all" {
		for _, c := range maoyanCategories {
			cats = append(cats, c.Key)
		}
	} else {
		cats = append(cats, category)
	}
	groups := make([]RankingGroup, 0, len(cats))
	var lastErr error
	for _, cat := range cats {
		entries, err := maoyanFetchCategory(ctx, cat, force)
		if err != nil {
			lastErr = err
			continue
		}
		mediaType := "tv"
		if cat == "movie" {
			mediaType = "movie"
		}
		items := make([]Item, 0, len(entries))
		for _, e := range entries {
			if strings.TrimSpace(e.Title) == "" {
				continue
			}
			item := Item{
				Source:    "maoyan",
				MediaType: mediaType,
				// 猫眼条目无稳定外部 ID，以标题哈希为键
				ExternalID: fmt.Sprintf("my-%s-%d", cat, hashString(e.Title)),
				EntityKey:  normalizeEntityKey("maoyan", mediaType, fmt.Sprintf("my-%s-%d", cat, hashString(e.Title))),
				Title:      e.Title,
				Year:       e.Year,
				Rank:       e.Rank,
			}
			items = append(items, item)
		}
		matchMaoyanTMDB(items, mediaType)
		groups = append(groups, RankingGroup{Kind: maoyanCategoryLabel(cat), Category: cat, Items: items})
	}
	if len(groups) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("猫眼榜单暂无数据")
		}
		return nil, false, lastErr
	}
	return groups, true, nil
}

// maoyanCategoryLabel 类别中文
func maoyanCategoryLabel(key string) string {
	for _, c := range maoyanCategories {
		if c.Key == key {
			return c.Label
		}
	}
	return "榜单"
}

var (
	maoyanMatchMu sync.Mutex
)

// matchMaoyanTMDB 猫眼条目批量匹配 TMDB（限速：每次调用最多 8 条、串行 300ms 间隔）
func matchMaoyanTMDB(items []Item, mediaType string) {
	maoyanMatchMu.Lock()
	defer maoyanMatchMu.Unlock()
	client := models_globalTmdbClient()
	language := models_globalTmdbLanguage()
	count := 0
	for i := range items {
		if count >= 8 {
			break
		}
		count++
		id, _ := searchSubjectTMDB(client, language, mediaType, items[i].Title, "", items[i].Year)
		if id > 0 {
			items[i].TMDBID = id
			items[i].EntityKey = normalizeEntityKey("tmdb", mediaType, strconv.FormatInt(id, 10))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// RankingGroup 榜单分组（榜单推荐分组渲染）
type RankingGroup struct {
	Kind     string `json:"kind"`
	Category string `json:"category,omitempty"`
	Items    []Item `json:"items"`
}

// hashString 稳定字符串哈希（无外部 ID 的条目键）
func hashString(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// sortEntriesByHeat 按热度降序（解析后兜底排序）
func sortEntriesByHeat(entries []maoyanHeatEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Rank != entries[j].Rank && entries[i].Rank > 0 && entries[j].Rank > 0 {
			return entries[i].Rank < entries[j].Rank
		}
		return entries[i].Heat > entries[j].Heat
	})
}
