package api

import (
	"net/http"
	"strconv"
	"strings"

	"litepan/internal/discover/discovery"
)

// ---------------------------------------------------------------------------
// 影视发现（移植自 diy-strm media_discovery + discover 控制器）。
// 数据层在 internal/discover/discovery；统一用 writeOK/writeErr 返回。
// ---------------------------------------------------------------------------

// discoverMeta 发现页元数据（筛选器选项）。
type discoverMeta struct {
	GenresMovie    map[string]string            `json:"genres_movie"`
	GenresTv       map[string]string            `json:"genres_tv"`
	Providers      []map[string]string          `json:"providers"`
	Regions        []map[string]string          `json:"regions"`
	Collections    []map[string]string          `json:"collections"`
	DoubanTags     map[string][]string          `json:"douban_tags"`
	DefaultSource  string                       `json:"default_source"`
	DoubanCategory map[string][]string          `json:"douban_category"`
	DoubanSort     []map[string]string          `json:"douban_sort"`
	AnimeGenres    []string                     `json:"anime_genres"`
	AnimeRegions   []map[string]string          `json:"anime_regions"`
	AnimeSort      []map[string]string          `json:"anime_sort"`
	MaoyanCategory []discovery.MaoyanCategory   `json:"maoyan_category"`
}

// discoverMetaHandler 发现页元数据
func (h *Handler) discoverMeta(w http.ResponseWriter, r *http.Request) {
	settings, _ := discovery.GetSettings()
	meta := discoverMeta{
		GenresMovie:    discovery.Genres("movie"),
		GenresTv:       discovery.Genres("tv"),
		Providers:      discovery.StreamingProviders,
		Regions:        discovery.StreamingRegions,
		Collections:    discovery.DoubanCollections(),
		DoubanTags:     doubanTagList,
		DefaultSource:  stringValueAny(settings[discovery.SettingDefaultExploreSource], "tmdb"),
		DoubanCategory: discovery.DoubanCategoryTags,
		DoubanSort:     discovery.DoubanSortOptions,
		AnimeGenres:    discovery.AnimeGenreOptions,
		AnimeRegions:   discovery.AnimeRegionOptions,
		AnimeSort:      discovery.AnimeSortOptions,
		MaoyanCategory: discovery.MaoyanCategories,
	}
	writeOK(w, meta)
}

// discoverExplore 影视探索（TMDB 多条件筛选）
func (h *Handler) discoverExplore(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	force := q.Get("force") == "true" || q.Get("force") == "1"
	result, err := discovery.ExploreTMDB(q.Get("type"), q.Get("genre"), q.Get("year"), q.Get("region"), q.Get("sort_by"), page, force)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

// discoverExploreDouban 影视探索（豆瓣 tag）
func (h *Handler) discoverExploreDouban(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	result, err := discovery.ExploreDouban(q.Get("type"), q.Get("tag"), page, q.Get("force") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

// discoverRankings 榜单推荐三源聚合
func (h *Handler) discoverRankings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	force := q.Get("force") == "true" || q.Get("force") == "1"
	result, err := discovery.Rankings(r.Context(), q.Get("provider"), q.Get("region"), q.Get("media_type"), page, force)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

// discoverCalendar 追剧日历（RE0 feed 按天分组，TMDB 兜底）
func (h *Handler) discoverCalendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	force := q.Get("force") == "true" || q.Get("force") == "1"
	days, err := discovery.Calendar(r.Context(), q.Get("days"), q.Get("kind"), force)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, days)
}

// discoverAnimeCalendar 番剧放送日历（Bangumi）
func (h *Handler) discoverAnimeCalendar(w http.ResponseWriter, r *http.Request) {
	days, err := discovery.AnimeCalendarBangumi(r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, days)
}

// discoverAnimeSearch 番剧搜索（Bangumi 主源 + AniList 回退）
func (h *Handler) discoverAnimeSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	result, err := discovery.AnimeSearch(r.Context(), q.Get("keyword"), q.Get("source"), page, q.Get("force") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

// discoverFavorites 收藏列表
func (h *Handler) discoverFavorites(w http.ResponseWriter, r *http.Request) {
	list, err := discovery.ListFavorites()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": list})
}

// discoverFavoriteAdd 添加收藏
func (h *Handler) discoverFavoriteAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityKey     string  `json:"entity_key"`
		Source        string  `json:"source"`
		MediaType     string  `json:"media_type"`
		ExternalID    string  `json:"external_id"`
		TMDBID        int64   `json:"tmdb_id"`
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Poster        string  `json:"poster"`
		Overview      string  `json:"overview"`
		VoteAvg       float64 `json:"vote_avg"`
		Year          int     `json:"year"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	fav := &discovery.DiscoveryFavorite{
		EntityKey:     req.EntityKey,
		Source:        req.Source,
		MediaType:     defaultMediaType(req.MediaType),
		ExternalID:    req.ExternalID,
		TMDBID:        req.TMDBID,
		Title:         req.Title,
		OriginalTitle: req.OriginalTitle,
		Poster:        req.Poster,
		Overview:      req.Overview,
		VoteAvg:       req.VoteAvg,
		Year:          req.Year,
	}
	if fav.Source == "" || fav.ExternalID == "" {
		source, _, extID := splitFavoriteKey(req.EntityKey)
		fav.Source = source
		fav.ExternalID = extID
	}
	if err := discovery.UpsertFavorite(fav); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// discoverFavoriteDelete 删除收藏
func (h *Handler) discoverFavoriteDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	if err := discovery.DeleteFavorite(uint(id)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// discoverFavoriteCheck 批量查询收藏状态
func (h *Handler) discoverFavoriteCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []string `json:"keys"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Keys) > 500 {
		req.Keys = req.Keys[:500]
	}
	result, err := discovery.FavoriteKeysByIDs(req.Keys)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"favorited": result})
}

// discoverSettingsGet 获取发现页设置
func (h *Handler) discoverSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := discovery.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, settings)
}

// discoverSettingsUpdate 更新发现页设置
func (h *Handler) discoverSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var values map[string]any
	if err := decodeJSON(r, &values); err != nil {
		writeErr(w, err)
		return
	}
	settings, err := discovery.UpdateSettings(values)
	if err != nil {
		writeErr(w, err)
		return
	}
	discovery.InvalidateDiscoveryCache()
	writeOK(w, settings)
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// doubanTagList 豆瓣榜单标签（j/search_subjects 接口）
var doubanTagList = map[string][]string{
	"movie": {"热门", "最新", "经典", "可播放", "豆瓣高分", "冷门佳片", "华语", "欧美", "韩国", "日本", "动作", "喜剧", "爱情", "科幻", "悬疑", "恐怖", "剧情", "纪录片", "动画"},
	"tv":    {"热门", "最新", "经典", "国产剧", "美剧", "英剧", "韩剧", "日剧", "动漫", "悬疑", "科幻", "爱情", "喜剧", "动作"},
}

func splitFavoriteKey(key string) (string, string, string) {
	segs := strings.SplitN(key, ":", 3)
	switch len(segs) {
	case 3:
		return segs[0], segs[1], segs[2]
	case 2:
		return segs[0], "", segs[1]
	default:
		return "", "", ""
	}
}

func defaultMediaType(t string) string {
	if t == "tv" {
		return "tv"
	}
	return "movie"
}

func stringValueAny(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}
