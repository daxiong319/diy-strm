package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
)

// chiPathParam 读取 chi 路径参数。
func chiPathParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

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
	writeDiscoveryOK(w, result)
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
	writeDiscoveryOK(w, result)
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
	writeDiscoveryOK(w, result)
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
	writeDiscoveryOK(w, days)
}

// discoverAnimeCalendar 番剧放送日历（Bangumi）
func (h *Handler) discoverAnimeCalendar(w http.ResponseWriter, r *http.Request) {
	days, err := discovery.AnimeCalendarBangumi(r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, days)
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
	writeDiscoveryOK(w, result)
}

// discoverDoubanCatalog 豆瓣目录（分类/排序筛选 + 后台预抓 + TMDB 匹配）
func (h *Handler) discoverDoubanCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	wait, _ := strconv.Atoi(q.Get("wait"))
	force := q.Get("force") == "1" || q.Get("force") == "true"
	result, err := discovery.DiscoverDoubanCatalog(q.Get("media_type"), q.Get("tag"), q.Get("sort"), page, wait, force)
	if err != nil && (result == nil || len(result.Items) == 0) {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, result)
}

// discoverAnimeCatalog 动漫目录（source=anilist/bangumi，AniList 故障自动回退 Bangumi）
func (h *Handler) discoverAnimeCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	wait, _ := strconv.Atoi(q.Get("wait"))
	force := q.Get("force") == "1" || q.Get("force") == "true"
	filter := discovery.AnimeBrowseFilter{
		Genre:  q.Get("genre"),
		Region: q.Get("region"),
		Year:   q.Get("year"),
		Sort:   q.Get("sort"),
	}
	result, err := discovery.DiscoverAnimeCatalog(r.Context(), q.Get("source"), filter, page, wait, force)
	fallbackSource := ""
	if err != nil && strings.EqualFold(strings.TrimSpace(q.Get("source")), "anilist") {
		result, err = discovery.DiscoverAnimeCatalog(r.Context(), "bangumi", filter, page, wait, force)
		if err == nil {
			fallbackSource = "bangumi"
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	result.FallbackSource = fallbackSource
	writeDiscoveryOK(w, result)
}

// discoverActors 热门演员
func (h *Handler) discoverActors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	force := q.Get("force") == "1" || q.Get("force") == "true"
	result, err := discovery.Actors(page, force)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, result)
}

// discoverActorWorks 演员作品
func (h *Handler) discoverActorWorks(w http.ResponseWriter, r *http.Request) {
	actorID, err := parsePathInt64(r, "id")
	if err != nil || actorID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "无效的演员 ID"))
		return
	}
	result, err := discovery.ActorWorks(actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, result)
}

// discoverSearch 统一搜索（TMDB 电影/剧集/演员）
func (h *Handler) discoverSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	force := q.Get("force") == "1" || q.Get("force") == "true"
	result, err := discovery.SearchMedia(r.Context(), q.Get("q"), q.Get("media_type"), page, force)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, result)
}

// discoverDetails 作品详情（附订阅状态与转存目标配置）
func (h *Handler) discoverDetails(w http.ResponseWriter, r *http.Request) {
	source := chiPathParam(r, "source")
	entityType := chiPathParam(r, "type")
	externalID := chiPathParam(r, "id")
	result, err := discovery.MediaDetails(source, entityType, externalID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if entityKey, _ := result["entity_key"].(string); entityKey != "" {
		if sub, err := discovery.GetSubscriptionByKey(entityKey); err == nil && sub != nil {
			result["subscription"] = map[string]any{
				"id": sub.ID, "status": sub.Status, "enabled": sub.Enabled,
				"target_provider": sub.TargetProvider, "rules_count": len(sub.Rules),
			}
		}
	}
	result["transfer_targets"] = transferTargetsStatus()
	writeDiscoveryOK(w, result)
}

// transferTargetsStatus 三网盘保存目录配置状态（详情页按钮 disabled 用）
func transferTargetsStatus() map[string]any {
	out := map[string]any{}
	for _, provider := range []string{"123", "guangya", "pan139"} {
		out[provider] = map[string]any{
			"configured":  discovery.TransferTargetConfigured(provider),
			"folder_name": discovery.TransferTargetDir(provider),
		}
	}
	return out
}

// discoverMaoyanRankings 猫眼榜单（被验证墙拦截时按 pending 语义返回，前端自动重试）
func (h *Handler) discoverMaoyanRankings(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	category := r.URL.Query().Get("category")
	groups, ok, err := discovery.MaoyanRankings(r.Context(), category, force)
	if err != nil && !ok {
		writeDiscoveryOK(w, map[string]any{
			"provider": "maoyan", "provider_label": "猫眼",
			"region": "CN", "category_label": maoyanCategoryLabelOf(category),
			"groups": []any{}, "feed_status": "pending",
			"message": err.Error(),
			"items":   []any{},
		})
		return
	}
	writeDiscoveryOK(w, map[string]any{
		"provider": "maoyan", "provider_label": "猫眼",
		"region": "CN", "category_label": maoyanCategoryLabelOf(category),
		"groups": groups, "feed_status": "ok", "is_stale": false,
		"available_regions": []map[string]string{{"key": "CN", "label": "中国"}},
	})
}

func maoyanCategoryLabelOf(category string) string {
	switch category {
	case "tv":
		return "电视剧"
	case "web_tv":
		return "网剧"
	case "variety":
		return "综艺"
	case "movie":
		return "电影"
	default:
		return "热播榜"
	}
}

// discoverAnimeMatch 番剧条目匹配 TMDB
func (h *Handler) discoverAnimeMatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityKey string `json:"entity_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.EntityKey == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少 entity_key"))
		return
	}
	tmdbID, err := discovery.MatchAnimeTMDB(req.EntityKey)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"entity_key": req.EntityKey, "tmdb_id": tmdbID})
}

// discoverFavorites 收藏列表
func (h *Handler) discoverFavorites(w http.ResponseWriter, r *http.Request) {
	list, err := discovery.ListFavorites()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeDiscoveryOK(w, map[string]any{"items": list})
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
