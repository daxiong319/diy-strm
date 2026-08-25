package controllers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"diy-strm/internal/discovery"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// 影视发现（复刻 tgto123 media_discovery）：影视探索 / 榜单推荐 / 追剧日历 /
// 番剧目录 / 收藏 / 基础配置。数据层在 internal/discovery 包。
// ---------------------------------------------------------------------------

func init() {
	// 注入影巢 Feed 双通道 failover 执行器（symedia 主渠道 → tgtodrive 备用渠道）
	discovery.FeedExecFn = func(ctx context.Context, call func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error)) (*hdhive.OAuthAPIResponse, error) {
		accs := models.ListHiveAccountsForQuery()
		if len(accs) == 0 {
			return nil, errors.New("没有启用中的影巢授权账号，请先在影巢设置中完成 OAuth 授权")
		}
		var lastErr error
		tried := map[string]bool{}
		for _, acc := range accs {
			channel := acc.Channel
			if channel == "" {
				channel = models.HiveChannelTgtodrive
			}
			if tried[channel] {
				continue // 每个通道只尝试一次
			}
			tried[channel] = true
			client := models.HiveClientForAccount(acc)
			fc, ok := client.(hdhive.FeedClient)
			if !ok {
				continue
			}
			resp, err := call(fc)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode >= 500 || resp.StatusCode == 401 {
				lastErr = errors.New("通道响应异常 HTTP " + strconv.Itoa(resp.StatusCode))
				continue
			}
			return resp, nil
		}
		if lastErr == nil {
			lastErr = errors.New("全部通道均不可用")
		}
		return nil, lastErr
	}
}

// MediaDiscoveryMeta 发现页元数据（类型/平台/地区/片单选项，供前端筛选器）
type MediaDiscoveryMeta struct {
	GenresMovie   map[string]string   `json:"genres_movie"`
	GenresTv      map[string]string   `json:"genres_tv"`
	Providers     []map[string]string `json:"providers"`
	Regions       []map[string]string `json:"regions"`
	Collections   []map[string]string `json:"collections"`
	DoubanTags    map[string][]string `json:"douban_tags"`
	DefaultSource string              `json:"default_source"`
}

// GetMediaDiscoveryMeta 获取发现页元数据
// @Summary 获取发现页元数据
// @Description 返回影视探索/榜单推荐筛选器所需选项与默认设置
// @Success 200 {object} APIResponse[MediaDiscoveryMeta]
// @Router /media-discovery/meta [get]
func GetMediaDiscoveryMeta(c *gin.Context) {
	settings, _ := discovery.GetSettings()
	meta := MediaDiscoveryMeta{
		GenresMovie:   discovery.Genres("movie"),
		GenresTv:      discovery.Genres("tv"),
		Providers:     discovery.StreamingProviders,
		Regions:       discovery.StreamingRegions,
		Collections:   discovery.DoubanCollections(),
		DoubanTags:    doubanTagList,
		DefaultSource: stringValue(settings[discovery.SettingDefaultExploreSource], "tmdb"),
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: meta})
}

// GetMediaExplore 影视探索（TMDB 多条件筛选）
// @Summary 影视探索 TMDB 多条件筛选
// @Param type query string false "movie/tv，默认 movie"
// @Param genre query string false "类型（中文名或 TMDB genre id）"
// @Param year query string false "年份"
// @Param region query string false "地区（语言代码，如 zh-CN/us）"
// @Param sort_by query string false "排序 popular/latest/rating"
// @Param page query integer false "页码"
// @Param force query boolean false "跳过缓存强制刷新"
// @Success 200 {object} APIResponse[discovery.PageResult]
// @Router /media-discovery/explore [get]
func GetMediaExplore(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	force := c.Query("force") == "true" || c.Query("force") == "1"
	result, err := discovery.ExploreTMDB(
		c.Query("type"), c.Query("genre"), c.Query("year"),
		c.Query("region"), c.Query("sort_by"), page, force,
	)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "影视探索失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaExploreDouban 影视探索（豆瓣 tag）
// @Summary 影视探索豆瓣榜
// @Param type query string false "movie/tv，默认 movie"
// @Param tag query string false "豆瓣标签，默认 热门"
// @Param page query integer false "页码"
// @Success 200 {object} APIResponse[discovery.PageResult]
// @Router /media-discovery/explore/douban [get]
func GetMediaExploreDouban(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	result, err := discovery.ExploreDouban(c.Query("type"), c.Query("tag"), page, c.Query("force") == "1")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "豆瓣探索失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaRankings 榜单推荐
// @Summary 榜单推荐三源聚合
// @Description provider: hdhive（影巢流媒体榜）/ hdhive:netflix（指定平台）/ tmdb:popular 等（TMDB 分类）/ douban:movie_hot_gaia 等（豆瓣片单）
// @Param provider query string false "榜单来源，默认 hdhive"
// @Param region query string false "地区（仅流媒体榜）"
// @Param media_type query string false "movie/tv（仅流媒体/TMDB 榜）"
// @Param page query integer false "页码"
// @Success 200 {object} APIResponse[discovery.PageResult]
// @Router /media-discovery/rankings [get]
func GetMediaRankings(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	force := c.Query("force") == "true" || c.Query("force") == "1"
	result, err := discovery.Rankings(c.Request.Context(),
		c.Query("provider"), c.Query("region"), c.Query("media_type"), page, force)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取榜单失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaCalendar 追剧日历
// @Summary 追剧日历（影巢 feed 按天分组，TMDB 兜底）
// @Param days query integer false "天数 1-30，默认取设置值"
// @Param kind query string false "all/tv/movie/upcoming/on-air/airing-today"
// @Success 200 {object} APIResponse[[]discovery.CalendarDay]
// @Router /media-discovery/calendar [get]
func GetMediaCalendar(c *gin.Context) {
	force := c.Query("force") == "true" || c.Query("force") == "1"
	days2, err := discovery.Calendar(c.Request.Context(), c.Query("days"), c.Query("kind"), force)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取追剧日历失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: days2})
}

// GetAnimeCalendar 番剧放送日历（Bangumi）
// @Summary 番剧每周放送日历
// @Success 200 {object} APIResponse[[]discovery.CalendarDay]
// @Router /media-discovery/anime/calendar [get]
func GetAnimeCalendar(c *gin.Context) {
	days2, err := discovery.AnimeCalendarBangumi(c.Query("force") == "true" || c.Query("force") == "1")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取番剧日历失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: days2})
}

// GetAnimeSearch 番剧搜索
// @Summary 番剧搜索（Bangumi 主源 + AniList 回退）
// @Param keyword query string true "关键词"
// @Param source query string false "bangumi/anilist，默认 bangumi"
// @Param page query integer false "页码（AniList 用）"
// @Success 200 {object} APIResponse[discovery.PageResult]
// @Router /media-discovery/anime/search [get]
func GetAnimeSearch(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	result, err := discovery.AnimeSearch(c.Request.Context(), c.Query("keyword"), c.Query("source"), page, c.Query("force") == "1")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "番剧搜索失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// MatchAnimeTMDBAPI 番剧条目匹配 TMDB
// @Summary 将番剧条目与 TMDB 剧集匹配并保存
// @Accept json
// @Param req body object true "{entity_key}"
// @Success 200 {object} APIResponse[map[string]any]
// @Router /media-discovery/anime/match [post]
func MatchAnimeTMDBAPI(c *gin.Context) {
	var req struct {
		EntityKey string `json:"entity_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	tmdbID, err := discovery.MatchAnimeTMDB(req.EntityKey)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "匹配失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{
		"entity_key": req.EntityKey,
		"tmdb_id":    tmdbID,
	}})
}

// ---------------------------------------------------------------------------
// 收藏
// ---------------------------------------------------------------------------

// favoriteRequest POST/收藏请求体（兼容 discovery.Item 结构）
type favoriteRequest struct {
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

// GetMediaFavorites 收藏列表
// @Summary 获取发现页收藏列表
// @Success 200 {object} APIResponse[[]discovery.DiscoveryFavorite]
// @Router /media-discovery/favorites [get]
func GetMediaFavorites(c *gin.Context) {
	list, err := discovery.ListFavorites()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取收藏失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: list})
}

// AddMediaFavorite 添加收藏
// @Summary 添加发现页收藏
// @Accept json
// @Param req body favoriteRequest true "收藏条目"
// @Success 200 {object} APIResponse[bool]
// @Router /media-discovery/favorites [post]
func AddMediaFavorite(c *gin.Context) {
	var req favoriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加收藏失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: true})
}

// DeleteMediaFavorite 删除收藏
// @Summary 删除发现页收藏
// @Param id path integer true "收藏 ID"
// @Success 200 {object} APIResponse[bool]
// @Router /media-discovery/favorites/{id} [delete]
func DeleteMediaFavorite(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的收藏 ID", Data: nil})
		return
	}
	if err := discovery.DeleteFavorite(uint(id)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除收藏失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: true})
}

// CheckMediaFavorites 批量查询收藏状态
// @Summary 批量判断 entity_key 是否已收藏
// @Accept json
// @Param req body object true "{keys: []}"
// @Success 200 {object} APIResponse[map[string]bool]
// @Router /media-discovery/favorites/check [post]
func CheckMediaFavorites(c *gin.Context) {
	var req struct {
		Keys []string `json:"keys"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.Keys) > 500 {
		req.Keys = req.Keys[:500]
	}
	result, err := discovery.FavoriteKeysByIDs(req.Keys)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询收藏状态失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// splitFavoriteKey 解析 entity_key 为 source/mediaType/externalID
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

// ---------------------------------------------------------------------------
// 基础配置
// ---------------------------------------------------------------------------

// GetMediaDiscoverySettings 获取发现页设置
// @Summary 获取发现页基础配置（含默认值）
// @Success 200 {object} APIResponse[map[string]any]
// @Router /media-discovery/settings [get]
func GetMediaDiscoverySettings(c *gin.Context) {
	settings, err := discovery.GetSettings()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取设置失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: settings})
}

// UpdateMediaDiscoverySettings 更新发现页设置
// @Summary 更新发现页基础配置（仅接受已知键），更新后清空发现缓存
// @Accept json
// @Param values body map[string]any true "设置键值对"
// @Success 200 {object} APIResponse[map[string]any]
// @Router /media-discovery/settings [post]
func UpdateMediaDiscoverySettings(c *gin.Context) {
	var values map[string]any
	if err := c.ShouldBindJSON(&values); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	settings, err := discovery.UpdateSettings(values)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存设置失败：" + err.Error(), Data: nil})
		return
	}
	discovery.InvalidateDiscoveryCache()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: settings})
}

func stringValue(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}
