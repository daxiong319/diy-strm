package controllers

import (
	"net/http"
	"strings"

	"diy-strm/internal/models"
	"diy-strm/internal/visualfilter"

	"github.com/gin-gonic/gin"
)

// 频道订阅白名单可视化（对齐 tgto123 /api/visual-filter/* 形状）：
// 场景 = 123 / guangya / 139 频道订阅；规则卡片与 RawRegex 双向同步；
// 海报补全复用 TMDB 搜索。

// GetVisualFilterConfigAPI GET /api/visual-filter/config?scene=
func GetVisualFilterConfigAPI(c *gin.Context) {
	cfg := visualfilter.GetConfig(c.Query("scene"))
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"current":         cfg,
		"scene":           cfg.Scene,
		"scene_labels":    visualfilter.SceneLabel,
		"image_base_url":  models.GetTmdbImageUrl(""),
	}})
}

// GetAllVisualFilterScenesAPI GET /api/visual-filter/scenes — 全场景
func GetAllVisualFilterScenesAPI(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[*visualfilter.ScenesResponse]{Code: Success, Data: visualfilter.GetAllScenes()})
}

// SaveVisualFilterConfigAPI POST /api/visual-filter/config
// body: {scene, parse_mode, raw_regex?, rules:[{id?,media_name,media_type,tmdb_id,title,poster_url,enabled}]}
func SaveVisualFilterConfigAPI(c *gin.Context) {
	var req visualfilter.Config
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	// raw_regex 非空且与规则不一致时：以 raw_regex 为准重建规则（正则视图编辑兼容）
	if strings.TrimSpace(req.RawRegex) != "" {
		current := visualfilter.GetConfig(req.Scene)
		req.Rules = visualfilter.RawRegexToRules(req.RawRegex, current.Rules)
	}
	saved, err := visualfilter.SaveConfig(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[*visualfilter.Config]{Code: Success, Message: "白名单规则已保存", Data: saved})
}

// VisualFilterPosterLookupAPI GET /api/visual-filter/tmdb/search?query=&type=
// 响应形状对齐 tgto123：{image_base_url, results:[{id,title,poster_path,media_type,year}]}
func VisualFilterPosterLookupAPI(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("query"))
	mediaType := strings.ToLower(strings.TrimSpace(c.Query("type")))
	if keyword == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少 query"})
		return
	}
	if mediaType != "movie" && mediaType != "tv" {
		mediaType = "" // 多类型：电影优先，空则都搜
	}
	tmdbClient := models.GlobalScrapeSettings.GetTmdbClient()
	type posterResult struct {
		ID         int    `json:"id"`
		Title      string `json:"title"`
		PosterPath string `json:"poster_path"`
		MediaType  string `json:"media_type"`
		Year       string `json:"year"`
	}
	results := make([]posterResult, 0, 8)
	if mediaType == "" || mediaType == "movie" {
		if resp, err := tmdbClient.SearchMovie(keyword, 0, models.GlobalScrapeSettings.GetTmdbLanguage(), true, false); err == nil {
			for _, r := range resp.Results {
				results = append(results, posterResult{
					ID:         int(r.ID),
					Title:      r.Title,
					PosterPath: r.PosterPath,
					MediaType:  "movie",
					Year:       firstFour(r.ReleaseDate),
				})
			}
		}
	}
	if mediaType == "" || mediaType == "tv" {
		if resp, err := tmdbClient.SearchTv(keyword, 0, models.GlobalScrapeSettings.GetTmdbLanguage(), true); err == nil {
			for _, r := range resp.Results {
				results = append(results, posterResult{
					ID:         int(r.ID),
					Title:      r.Name,
					PosterPath: r.PosterPath,
					MediaType:  "tv",
					Year:       firstFour(r.FirstAirDate),
				})
			}
		}
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"image_base_url": strings.TrimSuffix(models.GetTmdbImageUrl(""), "/"),
		"results":        results,
	}})
}

// firstFour 取日期字符串前 4 位年份
func firstFour(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return date
}
