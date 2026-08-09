package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"diy-strm/internal/douban"
	"diy-strm/internal/embycheck"
	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"

	"github.com/gin-gonic/gin"
)

// DiscoverItem 发现页统一影片条目
type DiscoverItem struct {
	Source        string  `json:"source"`         // tmdb / douban
	MediaType     string  `json:"media_type"`     // movie / tv
	TmdbID        int64   `json:"tmdb_id,omitempty"`
	DoubanID      string  `json:"douban_id,omitempty"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title,omitempty"`
	Poster        string  `json:"poster"`
	Backdrop      string  `json:"backdrop,omitempty"`
	Overview      string  `json:"overview,omitempty"`
	VoteAvg       float64 `json:"vote_avg"`
	ReleaseDate   string  `json:"release_date,omitempty"`
	Year          int     `json:"year,omitempty"`
	InEmby        bool    `json:"in_emby"`
}

var tmdbMovieCategories = map[string]bool{
	"popular": true, "top_rated": true, "now_playing": true, "upcoming": true,
}
var tmdbTvCategories = map[string]bool{
	"popular": true, "top_rated": true, "on_the_air": true, "airing_today": true,
}

// GetDiscoverTmdb 获取 TMDB 热门影片列表
// @Summary 获取 TMDB 热门影片列表
// @Description 获取 TMDB 榜单（电影/剧集：popular/top_rated/now_playing/upcoming/on_the_air/airing_today/trending_day/trending_week）
// @Param type query string false "媒体类型：movie/tv，默认 movie"
// @Param category query string false "榜单分类，默认 popular"
// @Param page query integer false "页码，默认 1"
// @Success 200 {object} APIResponse[[]DiscoverItem]
// @Router /discover/tmdb [get]
func GetDiscoverTmdb(c *gin.Context) {
	mediaType := c.Query("type")
	if mediaType != "tv" {
		mediaType = "movie"
	}
	category := c.Query("category")
	if category == "" {
		category = "popular"
	}
	page, _ := strconv.Atoi(c.Query("page"))
	if page <= 0 {
		page = 1
	}
	if page > 20 {
		page = 20
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	tmdbClient := models.GlobalScrapeSettings.GetTmdbClient()

	var items []DiscoverItem
	var err error
	if mediaType == "tv" {
		switch {
		case strings.HasPrefix(category, "trending"):
			var resp *tmdb.SearchTvResponse
			resp, err = tmdbClient.GetTrendingTv(strings.TrimPrefix(category, "trending_"), language, page)
			if err == nil {
				items = toDiscoverItems(resp.Results, "tmdb", "tv", language)
			}
		case tmdbTvCategories[category]:
			var resp *tmdb.SearchTvResponse
			resp, err = tmdbClient.GetTvList(category, language, page)
			if err == nil {
				items = toDiscoverItems(resp.Results, "tmdb", "tv", language)
			}
		default:
			err = errUnknownCategory(category)
		}
	} else {
		switch {
		case strings.HasPrefix(category, "trending"):
			var resp *tmdb.SearchMovieResponse
			resp, err = tmdbClient.GetTrendingMovies(strings.TrimPrefix(category, "trending_"), language, page)
			if err == nil {
				items = toDiscoverItems(resp.Results, "tmdb", "movie", language)
			}
		case tmdbMovieCategories[category]:
			var resp *tmdb.SearchMovieResponse
			resp, err = tmdbClient.GetMovieList(category, language, page)
			if err == nil {
				items = toDiscoverItems(resp.Results, "tmdb", "movie", language)
			}
		default:
			err = errUnknownCategory(category)
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取影片列表失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: items})
}

func errUnknownCategory(category string) error {
	return &unknownCategoryError{category: category}
}

type unknownCategoryError struct {
	category string
}

func (e *unknownCategoryError) Error() string {
	return "不支持的榜单分类：" + e.category
}

func toDiscoverItems[T any](results []T, source string, mediaType string, language string) []DiscoverItem {
	items := make([]DiscoverItem, 0, len(results))
	for _, r := range results {
		switch v := any(r).(type) {
		case tmdb.SearchMovie:
			item := DiscoverItem{
				Source:        source,
				MediaType:     mediaType,
				TmdbID:        v.ID,
				Title:         v.Title,
				OriginalTitle: v.OriginalTitle,
				Overview:      v.Overview,
				VoteAvg:       v.VoteAverage,
				ReleaseDate:   v.ReleaseDate,
			}
			if v.PosterPath != "" {
				item.Poster = models.GetTmdbImageUrl(v.PosterPath)
			}
			if v.BackdropPath != "" {
				item.Backdrop = models.GetTmdbImageUrl(v.BackdropPath)
			}
			if len(v.ReleaseDate) >= 4 {
				if year, parseErr := strconv.Atoi(v.ReleaseDate[:4]); parseErr == nil {
					item.Year = year
				}
			}
			items = append(items, item)
		case tmdb.SearchTv:
			item := DiscoverItem{
				Source:        source,
				MediaType:     mediaType,
				TmdbID:        v.ID,
				Title:         v.Name,
				OriginalTitle: v.OriginalName,
				Overview:      v.Overview,
				VoteAvg:       v.VoteAverage,
				ReleaseDate:   v.FirstAirDate,
			}
			if v.PosterPath != "" {
				item.Poster = models.GetTmdbImageUrl(v.PosterPath)
			}
			if v.BackdropPath != "" {
				item.Backdrop = models.GetTmdbImageUrl(v.BackdropPath)
			}
			if len(v.FirstAirDate) >= 4 {
				if year, parseErr := strconv.Atoi(v.FirstAirDate[:4]); parseErr == nil {
					item.Year = year
				}
			}
			items = append(items, item)
		}
	}
	return items
}

// doubanTagList 豆瓣榜单标签（j/search_subjects 接口）
var doubanTagList = map[string][]string{
	"movie": {"热门", "最新", "经典", "可播放", "豆瓣高分", "冷门佳片", "华语", "欧美", "韩国", "日本", "动作", "喜剧", "爱情", "科幻", "悬疑", "恐怖", "剧情", "纪录片", "动画"},
	"tv":    {"热门", "最新", "经典", "国产剧", "美剧", "英剧", "韩剧", "日剧", "动漫", "悬疑", "科幻", "爱情", "喜剧", "动作"},
}

// doubanCollectionList 豆瓣片单（rexxar 接口）
var doubanCollectionList = map[string]string{
	"movie_hot_gaia":     "电影热门",
	"tv_hot_gaia":        "剧集热门",
	"movie_weekly_best":  "一周口碑电影榜",
	"tv_weekly_best":     "一周口碑剧集榜",
	"movie_new_movie":    "新片速递",
	"movie_discussion":   "豆瓣电影讨论",
	"tv_weekly_discussion": "本周热议剧集",
}

// GetDiscoverDouban 获取豆瓣热门影片列表
// @Summary 获取豆瓣热门影片列表
// @Description 获取豆瓣榜单（tag）或片单（collection）
// @Param source query string true "数据源：tag（榜单）/ collection（片单）"
// @Param type query string false "媒体类型：movie/tv，默认 movie（仅榜单用）"
// @Param tag query string false "榜单标签（仅榜单用），默认 热门"
// @Param collection query string false "片单标识（仅片单用），默认 movie_hot_gaia"
// @Param page query integer false "页码，默认 1"
// @Success 200 {object} APIResponse[[]DiscoverItem]
// @Router /discover/douban [get]
func GetDiscoverDouban(c *gin.Context) {
	source := c.Query("source")
	if source != "collection" {
		source = "tag"
	}
	mediaType := c.Query("type")
	if mediaType != "tv" {
		mediaType = "movie"
	}
	page, _ := strconv.Atoi(c.Query("page"))
	if page <= 0 {
		page = 1
	}
	if page > 5 {
		page = 5
	}
	client := douban.NewClient()
	var items []DiscoverItem
	var err error
	if source == "collection" {
		collection := c.Query("collection")
		if _, ok := doubanCollectionList[collection]; !ok {
			collection = "movie_hot_gaia"
		}
		var rawItems []douban.CollectionItem
		rawItems, err = client.GetCollectionItems(collection, (page-1)*30, 30)
		if err == nil {
			items = make([]DiscoverItem, 0, len(rawItems))
			for _, item := range rawItems {
				year := 0
				if len(item.ReleaseDate) >= 4 {
					year, _ = strconv.Atoi(item.ReleaseDate[:4])
				}
				items = append(items, DiscoverItem{
					Source:    "douban",
					MediaType: mediaType,
					DoubanID:  item.ID,
					Title:     item.Title,
					Poster:    item.Pic.Normal,
					VoteAvg:   item.Rating.Value,
					ReleaseDate: item.ReleaseDate,
					Year:      year,
				})
			}
		}
	} else {
		tag := c.Query("tag")
		if tag == "" {
			tag = "热门"
		}
		var rawItems []douban.Subject
		rawItems, err = client.GetSubjects(mediaType, tag, (page-1)*30, 30)
		if err == nil {
			items = make([]DiscoverItem, 0, len(rawItems))
			for _, item := range rawItems {
				rate := 0.0
				if v, parseErr := strconv.ParseFloat(item.Rate, 64); parseErr == nil {
					rate = v
				}
				items = append(items, DiscoverItem{
					Source:    "douban",
					MediaType: mediaType,
					DoubanID:  item.ID,
					Title:     item.Title,
					Poster:    item.Cover,
					VoteAvg:   rate,
				})
			}
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取豆瓣影片列表失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: items})
}

// GetDiscoverEmbyCheck 检测影片是否已入库 Emby
// @Summary 检测影片是否已入库 Emby
// @Description 批量检测影片（tmdb_id/标题+年份）是否已在 Emby 媒体库
// @Accept json
// @Produce json
// @Param items body []embycheck.CheckItem true "检测项列表"
// @Success 200 {object} APIResponse[[]embycheck.CheckResult]
// @Router /discover/emby-check [post]
func GetDiscoverEmbyCheck(c *gin.Context) {
	var req struct {
		Items []embycheck.CheckItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: []embycheck.CheckResult{}})
		return
	}
	if len(req.Items) > 200 {
		req.Items = req.Items[:200]
	}
	results := embycheck.Check(req.Items)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: results})
}
