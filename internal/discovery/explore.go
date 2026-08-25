package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/douban"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"
)

// ---------------------------------------------------------------------------
// 统一条目结构（前端渲染用，兼容 TMDB/豆瓣/番剧/影巢 feed 四类来源）
// ---------------------------------------------------------------------------

// Item 发现页统一条目
type Item struct {
	Source        string   `json:"source"`                  // tmdb/douban/bangumi/anilist/hdhive
	MediaType     string   `json:"media_type"`              // movie/tv
	EntityKey     string   `json:"entity_key"`              // source:type:id（收藏键）
	TMDBID        int64    `json:"tmdb_id,omitempty"`
	DoubanID      string   `json:"douban_id,omitempty"`
	ExternalID    string   `json:"external_id,omitempty"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title,omitempty"`
	Poster        string   `json:"poster"`
	Overview      string   `json:"overview,omitempty"`
	VoteAvg       float64  `json:"vote_avg"`
	ReleaseDate   string   `json:"release_date,omitempty"`
	Year          int      `json:"year,omitempty"`
	Rank          int      `json:"rank,omitempty"`           // 榜单名次
	Providers     []string `json:"providers,omitempty"`      // 流媒体平台（影巢榜单条目）
	Genres        []string `json:"genres,omitempty"`         // 番剧类型标签
	AirDate       string   `json:"air_date,omitempty"`       // 日历播出时间（完整时间戳）
	EpisodeTitle  string   `json:"episode_title,omitempty"`  // 日历集标题
	SeasonNumber  int      `json:"season_number,omitempty"`
	EpisodeNumber int      `json:"episode_number,omitempty"`
}

// PageResult 分页结果
type PageResult struct {
	Items      []Item `json:"items"`
	Page       int    `json:"page"`
	TotalPages int    `json:"total_pages"`
}

// ---------------------------------------------------------------------------
// 内存 TTL 缓存（对应 tgto123 SOURCE_TTLS + _cache_entry 的进程内简化版）
// ---------------------------------------------------------------------------

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

type ttlCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

var globalCache = &ttlCache{entries: map[string]cacheEntry{}}

// cacheGet 读取缓存（未命中或过期返回 nil）
func cacheGet(key string) any {
	globalCache.mu.RLock()
	defer globalCache.mu.RUnlock()
	entry, ok := globalCache.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.value
}

// cacheSet 写入缓存，TTL 取设置值（默认 30 分钟）
func cacheSet(key string, value any) {
	globalCache.mu.Lock()
	defer globalCache.mu.Unlock()
	// 惰性清理：超过 500 条时清空过期项
	if len(globalCache.entries) > 500 {
		now := time.Now()
		for k, e := range globalCache.entries {
			if now.After(e.expiresAt) {
				delete(globalCache.entries, k)
			}
		}
	}
	ttl := time.Duration(SettingInt(SettingCacheTTLMinutes, 30)) * time.Minute
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	globalCache.ttl = ttl
	globalCache.entries[key] = cacheEntry{value: value, expiresAt: time.Now().Add(ttl)}
}

// InvalidateDiscoveryCache 清空发现缓存（设置更新/手动刷新时调用）
func InvalidateDiscoveryCache() {
	globalCache.mu.Lock()
	defer globalCache.mu.Unlock()
	globalCache.entries = map[string]cacheEntry{}
}

// FeedExecFn 影巢 Feed 调用执行器（由 controller 层注入双通道 failover 逻辑）：
// call 收到一个可用通道的 FeedClient 并执行具体接口调用；
// 未注入或无可用通道时返回错误
var FeedExecFn func(ctx context.Context, call func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, error)

// feedExecute 执行一次影巢 Feed 调用（自动主备切换）
func feedExecute(ctx context.Context, call func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, error) {
	if FeedExecFn == nil {
		return nil, fmt.Errorf("影巢 feed 通道未初始化，请先完成 OAuth 授权")
	}
	return FeedExecFn(ctx, call)
}

// ---------------------------------------------------------------------------
// 影视探索（TMDB discover 多条件筛选 + 豆瓣 tag 探索，对应 tgto123 discover/discover_douban）
// ---------------------------------------------------------------------------

// tmdbGenreMap 电影/剧集类型 ID 表（TMDB 官方 genre id）
var tmdbGenreMap = map[string]map[string]string{
	"movie": {
		"动作": "28", "冒险": "12", "动画": "16", "喜剧": "35", "犯罪": "80",
		"纪录片": "99", "剧情": "18", "家庭": "10751", "奇幻": "14", "历史": "36",
		"恐怖": "27", "音乐": "10402", "悬疑": "9648", "爱情": "10749",
		"科幻": "878", "电视电影": "10770", "惊悚": "53", "战争": "10752", "西部": "37",
	},
	"tv": {
		"动作冒险": "10759", "动画": "16", "儿童": "10762", "喜剧": "35", "犯罪": "80",
		"纪录": "99", "剧情": "18", "家庭": "10751", "奇幻": "10765", "历史": "36",
		"悬疑": "9648", "真人秀": "10764", "科幻": "10765", "战争": "10768", "西部": "37",
	},
}

// Genres 返回类型列表（供前端筛选器）
func Genres(mediaType string) map[string]string {
	if mediaType == "tv" {
		return tmdbGenreMap["tv"]
	}
	return tmdbGenreMap["movie"]
}

// ExploreTMDB TMDB 多条件探索
func ExploreTMDB(mediaType, genre, year, region, sortBy string, page int, force bool) (*PageResult, error) {
	if mediaType != "tv" {
		mediaType = "movie"
	}
	if page <= 0 {
		page = 1
	}
	if page > 500 {
		page = 500
	}
	sortKey := strings.TrimSpace(strings.ToLower(sortBy))
	if sortKey == "" {
		sortKey = SettingString(SettingDefaultExploreSort, "popular")
	}
	cacheKey := fmt.Sprintf("discover:tmdb:v1:%s:%d:%s:%s:%s:%s", mediaType, page, genre, region, year, sortKey)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()
	query := tmdb.DiscoverQuery{
		Genre:  normalizeGenre(mediaType, genre),
		Year:   strings.TrimSpace(year),
		Region: strings.TrimSpace(region),
		SortBy: sortKey,
	}
	var (
		items []Item
		total int
		err   error
	)
	if mediaType == "tv" {
		var resp *tmdb.SearchTvResponse
		resp, err = client.DiscoverTvs(query, language, page)
		if err == nil {
			items = tmdbTvToItems(resp.Results)
			total = resp.TotalResults
		}
	} else {
		var resp *tmdb.SearchMovieResponse
		resp, err = client.DiscoverMovies(query, language, page)
		if err == nil {
			items = tmdbMovieToItems(resp.Results)
			total = resp.TotalResults
		}
	}
	if err != nil {
		return nil, err
	}
	result := &PageResult{Items: items, Page: page, TotalPages: estimatePages(total)}
	cacheSet(cacheKey, result)
	return result, nil
}

// normalizeGenre 类型参数：支持中文名或数字 ID
func normalizeGenre(mediaType, genre string) string {
	genre = strings.TrimSpace(genre)
	if genre == "" {
		return ""
	}
	if _, err := strconv.Atoi(genre); err == nil {
		return genre // 已是 ID
	}
	if m, ok := tmdbGenreMap[mediaType]; ok {
		if id, ok2 := m[genre]; ok2 {
			return id
		}
	}
	return ""
}

// ExploreDouban 豆瓣 tag 探索（复用现有 douban 包）
func ExploreDouban(mediaType, tag string, page int, force bool) (*PageResult, error) {
	if mediaType != "tv" {
		mediaType = "movie"
	}
	if page <= 0 {
		page = 1
	}
	if tag == "" {
		tag = "热门"
	}
	cacheKey := fmt.Sprintf("discover:douban:v1:%s:%s:%d", mediaType, tag, page)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	client := douban.NewClient()
	subjects, err := client.GetSubjects(mediaType, tag, (page-1)*30, 30)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(subjects))
	for _, s := range subjects {
		rate, _ := strconv.ParseFloat(s.Rate, 64)
		items = append(items, Item{
			Source:     "douban",
			MediaType:  mediaType,
			DoubanID:   s.ID,
			ExternalID: s.ID,
			EntityKey:  normalizeEntityKey("douban", mediaType, s.ID),
			Title:      s.Title,
			Poster:     s.Cover,
			VoteAvg:    rate,
		})
	}
	result := &PageResult{Items: items, Page: page, TotalPages: 10}
	cacheSet(cacheKey, result)
	return result, nil
}

// tmdbMovieToItems / tmdbTvToItems TMDB 结果转换
func tmdbMovieToItems(results []tmdb.SearchMovie) []Item {
	items := make([]Item, 0, len(results))
	for _, v := range results {
		item := Item{
			Source:        "tmdb",
			MediaType:     "movie",
			TMDBID:        v.ID,
			ExternalID:    strconv.FormatInt(v.ID, 10),
			EntityKey:     normalizeEntityKey("tmdb", "movie", strconv.FormatInt(v.ID, 10)),
			Title:         v.Title,
			OriginalTitle: v.OriginalTitle,
			Overview:      v.Overview,
			VoteAvg:       v.VoteAverage,
			ReleaseDate:   v.ReleaseDate,
		}
		if v.PosterPath != "" {
			item.Poster = models.GetTmdbImageUrl(v.PosterPath)
		}
		if len(v.ReleaseDate) >= 4 {
			item.Year, _ = strconv.Atoi(v.ReleaseDate[:4])
		}
		items = append(items, item)
	}
	return items
}

func tmdbTvToItems(results []tmdb.SearchTv) []Item {
	items := make([]Item, 0, len(results))
	for _, v := range results {
		item := Item{
			Source:        "tmdb",
			MediaType:     "tv",
			TMDBID:        v.ID,
			ExternalID:    strconv.FormatInt(v.ID, 10),
			EntityKey:     normalizeEntityKey("tmdb", "tv", strconv.FormatInt(v.ID, 10)),
			Title:         v.Name,
			OriginalTitle: v.OriginalName,
			Overview:      v.Overview,
			VoteAvg:       v.VoteAverage,
			ReleaseDate:   v.FirstAirDate,
		}
		if v.PosterPath != "" {
			item.Poster = models.GetTmdbImageUrl(v.PosterPath)
		}
		if len(v.FirstAirDate) >= 4 {
			item.Year, _ = strconv.Atoi(v.FirstAirDate[:4])
		}
		items = append(items, item)
	}
	return items
}

func estimatePages(total int) int {
	if total <= 0 {
		return 1
	}
	pages := (total + 19) / 20
	if pages > 500 {
		pages = 500
	}
	return pages
}

// ---------------------------------------------------------------------------
// 榜单推荐（对应 tgto123 rankings：
// 影巢流媒体榜 streaming-top / TMDB 分类榜 / 豆瓣片单 三源聚合）
// ---------------------------------------------------------------------------

// STREAMING_PROVIDERS 流媒体平台表（key → 显示名），与 HDHive feed 对齐
var StreamingProviders = []map[string]string{
	{"key": "netflix", "label": "Netflix"},
	{"key": "apple", "label": "Apple TV+"},
	{"key": "disney", "label": "Disney+"},
	{"key": "amazon_prime", "label": "Prime Video"},
	{"key": "hulu", "label": "Hulu"},
	{"key": "max", "label": "Max"},
	{"key": "paramount", "label": "Paramount+"},
	{"key": "peacock", "label": "Peacock"},
}

// STREAMING_REGIONS 支持的地区
var StreamingRegions = []map[string]string{
	{"key": "US", "label": "美国"}, {"key": "CN", "label": "中国"},
	{"key": "JP", "label": "日本"}, {"key": "KR", "label": "韩国"},
	{"key": "GB", "label": "英国"}, {"key": "HK", "label": "香港"},
	{"key": "TW", "label": "台湾"},
}

// doubanCollectionList 豆瓣片单（与 controllers/discover.go 保持一致并扩展）
var doubanRankingCollections = map[string]string{
	"movie_hot_gaia":       "电影热门",
	"tv_hot_gaia":          "剧集热门",
	"movie_weekly_best":    "一周口碑电影榜",
	"tv_weekly_best":       "一周口碑剧集榜",
	"movie_new_movie":      "新片速递",
	"tv_chinese_best_weekly": "华语口碑剧集榜",
	"tv_global_best_weekly":  "全球口碑剧集榜",
	"movie_top250":         "豆瓣 Top250",
}

// DoubanCollections 片单列表（供前端选择器）
func DoubanCollections() []map[string]string {
	list := make([]map[string]string, 0, len(doubanRankingCollections))
	for key, label := range doubanRankingCollections {
		list = append(list, map[string]string{"key": key, "label": label})
	}
	return list
}

// Rankings 榜单推荐统一入口。
// provider: hdhive（流媒体平台榜）/ tmdb:<category>（TMDB 分类）/ douban:<collection>（豆瓣片单）
func Rankings(ctx context.Context, provider, region, mediaType string, page int, force bool) (*PageResult, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	switch {
	case provider == "" || provider == "hdhive":
		return hdhiveStreamingTop(ctx, "", region, mediaType, force)
	case strings.HasPrefix(provider, "hdhive:"):
		// hdhive:netflix 指定平台的流媒体榜
		return hdhiveStreamingTop(ctx, strings.TrimPrefix(provider, "hdhive:"), region, mediaType, force)
	case strings.HasPrefix(provider, "tmdb"):
		category := strings.TrimPrefix(provider, "tmdb")
		if category == "" {
			category = "popular"
		}
		return tmdbCategoryRanking(category, mediaType, page, force)
	case strings.HasPrefix(provider, "douban:"):
		collection := strings.TrimPrefix(provider, "douban:")
		return doubanCollectionRanking(collection, page, force)
	default:
		return nil, fmt.Errorf("不支持的榜单来源：%s", provider)
	}
}

// FeedItemPayload 影巢 streaming-top feed 响应结构（data 字段）
type FeedItemPayload struct {
	Items []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		MediaType   string `json:"media_type"`
		PosterPath  string `json:"poster_path"`
		Backdrop    string `json:"backdrop_path"`
		VoteAvg     float64 `json:"vote_average"`
		ReleaseDate string `json:"release_date"`
		Overview    string `json:"overview"`
		TMDBID      int64  `json:"tmdb_id"`
		Provider    string `json:"provider"`
		Rank        int    `json:"rank"`
	} `json:"items"`
	AvailableRegions []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	} `json:"available_regions"`
}

// hdhiveStreamingTop 影巢流媒体榜（走 OAuth 双通道 feed）
func hdhiveStreamingTop(ctx context.Context, provider, region, mediaType string, force bool) (*PageResult, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == "all" {
		provider = SettingString(SettingRankingProvider, "netflix")
	}
	if region == "" {
		region = SettingString(SettingRankingRegion, "US")
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		mediaType = SettingString(SettingRankingMediaType, "movie")
	}
	cacheKey := fmt.Sprintf("rankings:hdhive:v1:%s:%s:%s", provider, region, mediaType)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	resp, err := feedExecute(ctx, func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error) {
		return fc.GetStreamingTop(ctx, provider, region, mediaType)
	})
	if err != nil {
		return nil, fmt.Errorf("获取影巢流媒体榜失败：%v", err)
	}
	if !resp.Success && len(resp.Data) == 0 {
		msg := firstNonEmptyStr(resp.Message, resp.Description, "上游接口无数据")
		return nil, fmt.Errorf("获取影巢流媒体榜失败：%s", msg)
	}
	var payload FeedItemPayload
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &payload); err != nil {
			return nil, fmt.Errorf("解析影巢流媒体榜失败：%v", err)
		}
	}
	items := make([]Item, 0, len(payload.Items))
	for _, raw := range payload.Items {
		media := strings.ToLower(raw.MediaType)
		if media != "tv" {
			media = "movie"
		}
		id := raw.TMDBID
		extID := raw.ID
		if id <= 0 {
			id, _ = strconv.ParseInt(extID, 10, 64)
		}
		item := Item{
			Source:      "hdhive",
			MediaType:   media,
			TMDBID:      id,
			ExternalID:  extID,
			EntityKey:   normalizeEntityKey("tmdb", media, strconv.FormatInt(id, 10)),
			Title:       raw.Title,
			Poster:      absoluteTMDBImage(raw.PosterPath),
			Overview:    raw.Overview,
			VoteAvg:     raw.VoteAvg,
			ReleaseDate: raw.ReleaseDate,
			Rank:        raw.Rank,
			Providers:   []string{raw.Provider},
		}
		if len(raw.ReleaseDate) >= 4 {
			item.Year, _ = strconv.Atoi(raw.ReleaseDate[:4])
		}
		items = append(items, item)
	}
	result := &PageResult{Items: items, Page: 1, TotalPages: 1}
	cacheSet(cacheKey, result)
	return result, nil
}

// tmdbCategoryRanking TMDB 分类榜
func tmdbCategoryRanking(category, mediaType string, page int, force bool) (*PageResult, error) {
	if mediaType != "tv" {
		mediaType = "movie"
	}
	if page <= 0 {
		page = 1
	}
	cacheKey := fmt.Sprintf("rankings:tmdb:v1:%s:%s:%d", mediaType, category, page)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()
	var items []Item
	var total int
	var err error
	if mediaType == "tv" {
		var resp *tmdb.SearchTvResponse
		resp, err = client.GetTvList(category, language, page)
		if err == nil {
			items = tmdbTvToItems(resp.Results)
			total = resp.TotalResults
		}
	} else {
		var resp *tmdb.SearchMovieResponse
		resp, err = client.GetMovieList(category, language, page)
		if err == nil {
			items = tmdbMovieToItems(resp.Results)
			total = resp.TotalResults
		}
	}
	if err != nil {
		return nil, err
	}
	result := &PageResult{Items: withRanks(items, (page-1)*20+1), Page: page, TotalPages: estimatePages(total)}
	cacheSet(cacheKey, result)
	return result, nil
}

// doubanCollectionRanking 豆瓣片单榜
func doubanCollectionRanking(collection string, page int, force bool) (*PageResult, error) {
	if page <= 0 {
		page = 1
	}
	if _, ok := doubanRankingCollections[collection]; !ok {
		collection = "movie_hot_gaia"
	}
	cacheKey := fmt.Sprintf("rankings:douban:v1:%s:%d", collection, page)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	client := douban.NewClient()
	rawItems, err := client.GetCollectionItems(collection, (page-1)*30, 30)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(rawItems))
	for _, c := range rawItems {
		media := "movie"
		if strings.Contains(collection, "tv") {
			media = "tv"
		}
		year := 0
		if c.Year != "" {
			year, _ = strconv.Atoi(c.Year)
		} else if len(c.ReleaseDate) >= 4 {
			year, _ = strconv.Atoi(c.ReleaseDate[:4])
		}
		items = append(items, Item{
			Source:     "douban",
			MediaType:  media,
			DoubanID:   c.ID,
			ExternalID: c.ID,
			EntityKey:  normalizeEntityKey("douban", media, c.ID),
			Title:      c.Title,
			Poster:     c.Cover.URL,
			VoteAvg:    c.Rating.Value,
			ReleaseDate: c.ReleaseDate,
			Year:       year,
		})
	}
	result := &PageResult{Items: withRanks(items, (page-1)*30+1), Page: page, TotalPages: 10}
	cacheSet(cacheKey, result)
	return result, nil
}

// withRanks 为列表补名次
func withRanks(items []Item, start int) []Item {
	for i := range items {
		items[i].Rank = start + i
	}
	return items
}

// absoluteTMDBImage 相对路径转绝对 URL
func absoluteTMDBImage(path string) string {
	if path == "" || strings.HasPrefix(path, "http") {
		return path
	}
	return models.GetTmdbImageUrl(path)
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
