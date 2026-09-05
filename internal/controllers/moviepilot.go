package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/moviepilot"
	"diy-strm/internal/requests"
)

// GetMoviePilotConfig 获取 MoviePilot 配置
// @Summary 获取 MoviePilot 配置
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetMoviePilotConfig(c *gin.Context) {
	cfg := models.LoadMoviePilotConfig()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 MoviePilot 配置成功", Data: cfg})
}

// UpdateMoviePilotConfig 更新 MoviePilot 配置
// @Summary 更新 MoviePilot 配置
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateMoviePilotConfig(c *gin.Context) {
	var req requests.UpdateMoviePilotConfigRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	cfg, ok := models.UpdateMoviePilotConfig(&models.MoviePilotConfig{
		Enabled:                req.Enabled,
		BaseUrl:                req.BaseUrl,
		ApiToken:               req.ApiToken,
		DownloadRoot:           req.DownloadRoot,
		LocalViewRoot:          req.LocalViewRoot,
		UploadAccountId:        req.UploadAccountId,
		UploadRoot:             req.UploadRoot,
		UploadRootId:           req.UploadRootId,
		StrmLocalDir:           req.StrmLocalDir,
		PollInterval:           req.PollInterval,
		NotifyEnabled:          req.NotifyEnabled,
		CategoryConfig:         req.CategoryConfig,
		PromotionOrder:         req.PromotionOrder,
		PromotionPatienceHours: req.PromotionPatienceHours,
		SeedRetentionHours:     req.SeedRetentionHours,
	})
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新 MoviePilot 配置失败", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "MoviePilot 配置已更新", Data: cfg})
}

// TestMoviePilotConnection 测试 MoviePilot 连接
// @Summary 测试 MoviePilot 连接
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot/test [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func TestMoviePilotConnection(c *gin.Context) {
	var req requests.TestMoviePilotConnectionRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	client := moviepilot.NewClient(req.BaseUrl, req.ApiToken)
	ctx, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	if err := client.TestConnection(ctx); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "连接成功", Data: nil})
}

// ListMoviePilotSubscribes 查询 MoviePilot 订阅列表
// @Summary 查询 MoviePilot 订阅列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotSubscribes(c *gin.Context) {
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	subs, err := client.ListSubscribes(c)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取订阅列表成功", Data: subs})
}

// CreateMoviePilotSubscribe 添加订阅
// @Summary 添加 MoviePilot 订阅
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CreateMoviePilotSubscribe(c *gin.Context) {
	var req requests.CreateMoviePilotSubscribeRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	id, err := client.CreateSubscribe(c, &moviepilot.CreateSubscribeRequest{
		Name:         req.Name,
		Year:         string(req.Year),
		Type:         req.Type,
		TmdbId:       req.TmdbId,
		Season:       req.Season,
		TotalEpisode: req.TotalEpisode,
		SavePath:     req.SavePath,
		Sites:        req.Sites,
		// 促销优选由全局促销优先阶梯（applyPromotionLadder）统一按层管理，
		// 新订阅先不限制，下一轮监督自动从最高优先层（默认免费）开始
	})
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 创建成功后立即触发一次订阅搜索，联动 MoviePilot 尽快找源下载；
	// 搜索失败不阻断（MoviePilot 调度器仍会周期搜索，也可在订阅页手动触发）。
	if id > 0 {
		if serr := client.SearchSubscribe(c, id); serr != nil {
			helpers.AppLogger.Warnf("MoviePilot 订阅 %d 自动搜索失败（可稍后手动触发）：%v", id, serr)
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "添加订阅成功", Data: map[string]any{"id": id}})
}

// SearchMoviePilotSubscribe 触发订阅搜索
// @Summary 触发 MoviePilot 订阅搜索
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id/search [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SearchMoviePilotSubscribe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.SearchSubscribe(c, id); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已触发订阅搜索，请稍候查看下载任务", Data: nil})
}

// DeleteMoviePilotSubscribe 删除订阅
// @Summary 删除 MoviePilot 订阅
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id [delete]
// @Security JwtAuth
// @Security ApiKeyAuth
func DeleteMoviePilotSubscribe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.DeleteSubscribe(c, id); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除订阅成功", Data: nil})
}

// UpdateMoviePilotSubscribeStatus 更新订阅状态
// @Summary 更新 MoviePilot 订阅状态
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id/status [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateMoviePilotSubscribeStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	var req requests.UpdateMoviePilotSubscribeStatusRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.UpdateSubscribeStatus(c, id, req.State); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新订阅状态成功", Data: nil})
}

// ListMoviePilotDownloads 查询 MoviePilot 下载任务列表
// 聚合「下载器当前任务」+「最近完成（下载历史兜底）」：完成后任务会从下载器列表消失，
// 用 MP 下载历史补齐，保证已完成条目仍可见并带上传状态。
// @Summary 查询 MoviePilot 下载任务列表（含最近完成）
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/downloads [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotDownloads(c *gin.Context) {
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	downloads, err := client.ListDownloads(c)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	type downloadRow struct {
		Hash          string `json:"hash"`
		Title         string `json:"title"`
		Name          string `json:"name"`
		SeasonEpisode string `json:"season_episode"`
		State         string `json:"state"`
		Progress      float64 `json:"progress"`
		SavePath      string `json:"save_path"`
		Date          string `json:"date"`
		UploadStatus  string `json:"upload_status"`
		SeedTime      string `json:"seed_time"` // 做种时长（下载完成至今），如 25h3m
	}
	// 做种起点：下载器列表不返回完成时间，用下载历史 date（hash 匹配）近似；
	// 一次查询全量构建映射，避免逐条请求
	seedStart := map[string]time.Time{}
	if histories, herr := client.ListDownloadHistory(c, 1, 50); herr == nil {
		for _, h := range histories {
			if h == nil || h.DownloadHash == "" || h.Date == "" {
				continue
			}
			if t, perr := time.ParseInLocation("2006-01-02 15:04:05", h.Date, time.Local); perr == nil {
				if old, ok := seedStart[h.DownloadHash]; !ok || t.After(old) {
					seedStart[h.DownloadHash] = t
				}
			}
		}
	}
	seedTimeText := func(hash string) string {
		started, ok := seedStart[hash]
		if !ok {
			return ""
		}
		d := time.Since(started)
		if d < 0 {
			return ""
		}
		if d >= 24*time.Hour {
			return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
		}
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	rows := make([]downloadRow, 0, len(downloads)+16)
	activeHashes := make(map[string]bool, len(downloads))
	uploadStatus := func(hash string) string {
		if hash == "" {
			return ""
		}
		if t := models.FindMoviePilotUploadTask(hash); t != nil {
			return string(t.Status)
		}
		return ""
	}
	for _, t := range downloads {
		if t == nil {
			continue
		}
		activeHashes[t.Hash] = true
		rows = append(rows, downloadRow{
			Hash:          t.Hash,
			Title:         t.Title,
			Name:          t.Name,
			SeasonEpisode: t.SeasonEpisode,
			State:         t.State,
			Progress:      t.Progress,
			SavePath:      t.SavePath,
			SeedTime:      seedTimeText(t.Hash),
			UploadStatus:  uploadStatus(t.Hash),
		})
	}
	// 历史兜底：已完成并从下载器列表移除的任务（取最近 50 条）
	if histories, herr := client.ListDownloadHistory(c, 1, 50); herr == nil {
		for _, h := range histories {
			if h == nil || h.DownloadHash == "" || activeHashes[h.DownloadHash] {
				continue
			}
			seasonEp := h.Seasons
			if h.Episodes != "" {
				if seasonEp != "" {
					seasonEp += " "
				}
				seasonEp += h.Episodes
			}
			rows = append(rows, downloadRow{
				Hash:          h.DownloadHash,
				Title:         h.Title,
				Name:          h.TorrentName,
				SeasonEpisode: seasonEp,
				State:         "completed",
				Progress:      100,
				SavePath:      h.Path,
				Date:          h.Date,
				SeedTime:      seedTimeText(h.DownloadHash),
				UploadStatus:  uploadStatus(h.DownloadHash),
			})
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取下载任务成功", Data: rows})
}

// ListMoviePilotUploadTasks 查询 139 上传任务列表
// @Summary 查询 MoviePilot 上传任务列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotUploadTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	status := c.DefaultQuery("status", "")
	tasks, total := models.ListMoviePilotUploadTasks(page, pageSize, status)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取上传任务成功", Data: map[string]any{"list": tasks, "total": total}})
}

// RetryMoviePilotUploadTask 重试上传任务
// @Summary 重试 MoviePilot 上传任务
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks/:id/retry [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func RetryMoviePilotUploadTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "任务 ID 无效", Data: nil})
		return
	}
	if !moviepilot.RetryUploadTask(uint(id)) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重试失败（仅失败/已取消任务可重试）", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已加入重试队列", Data: nil})
}

// CancelMoviePilotUploadTask 取消上传任务
// @Summary 取消 MoviePilot 上传任务
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks/:id/cancel [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CancelMoviePilotUploadTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "任务 ID 无效", Data: nil})
		return
	}
	if !moviepilot.CancelUploadTask(uint(id)) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "取消失败（上传中的任务无法取消）", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已取消", Data: nil})
}

// moviePilotClientFromConfig 从配置创建 MP 客户端；失败时已写响应并返回 false
func moviePilotClientFromConfig(c *gin.Context) (*moviepilot.Client, bool) {
	cfg := models.LoadMoviePilotConfig()
	if cfg.BaseUrl == "" || cfg.ApiToken == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先配置 MoviePilot 地址和 API Token", Data: nil})
		return nil, false
	}
	client := moviepilot.NewClient(cfg.BaseUrl, cfg.ApiToken)
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "MoviePilot 客户端创建失败", Data: nil})
		return nil, false
	}
	return client, true
}

// ListMoviePilotFailedFiles 查询识别失败文件列表（识别失败独立菜单）
// @Summary 查询识别失败文件列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/failed-files [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotFailedFiles(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	status := c.DefaultQuery("status", "")
	files, total := models.ListMoviePilotFailedFiles(page, pageSize, status)
	taskTitles := map[uint]string{}
	var tasks []models.MoviePilotUploadTask
	if err := db.Db.Select("id", "title").Find(&tasks).Error; err == nil {
		for _, t := range tasks {
			taskTitles[t.ID] = t.Title
		}
	}
	type failedItem struct {
		models.MoviePilotFailedFile
		TaskTitle string `json:"task_title"`
	}
	list := make([]failedItem, 0, len(files))
	for _, f := range files {
		list = append(list, failedItem{MoviePilotFailedFile: f, TaskTitle: taskTitles[f.TaskID]})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取识别失败文件成功", Data: map[string]any{"list": list, "total": total}})
}

// IdentifyMoviePilotFailedFile 识别失败文件：AI 优先、正则兜底，返回建议媒体信息 + TMDB 候选列表
// （对齐 tgto123 识别测试：解析结果与候选并出，候选可一键选中避免同名歧义）
// @Summary 识别失败文件（AI + 正则 + TMDB 候选）
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/failed-files/:id/identify [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func IdentifyMoviePilotFailedFile(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "记录 ID 无效", Data: nil})
		return
	}
	f := models.GetMoviePilotFailedFile(uint(id))
	if f == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录不存在", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c, 120*time.Second)
	defer cancel()

	// 解析来源标记：AI 命中 / 正则命中 / 均未命中
	source := "none"
	parsed := moviepilot.IdentifyResult{Episode: 1}
	if res, ok := moviepilot.IdentifyFileWithAI(ctx, f.FileName); ok {
		parsed = res
		source = "ai"
	} else if category, title, season, episode, year := mediaparse.ParseMedia(f.FileName); strings.TrimSpace(title) != "" {
		parsed = moviepilot.IdentifyResult{Category: category, Title: title, Season: season, Episode: episode, Year: year, TmdbId: 0}
		if parsed.Episode <= 0 {
			parsed.Episode = 1
		}
		source = "regex"
	}
	if parsed.Title == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "AI 与正则均未识别出标题，请手动填写并搜索 TMDB", Data: nil})
		return
	}
	candidates := searchTmdbCandidatesForFailed(ctx, parsed.Title, parsed.Year, parsed.Category)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "识别完成", Data: map[string]any{
		"category":    parsed.Category,
		"title":       parsed.Title,
		"year":        parsed.Year,
		"season":      parsed.Season,
		"episode":     parsed.Episode,
		"tmdb_id":     parsed.TmdbId,
		"source":      source,
		"candidates":  candidates,
	}})
}

// searchTmdbCandidatesForFailed 按标题+年份搜 TMDB 候选（电影+剧集并查，带海报/简介）
func searchTmdbCandidatesForFailed(ctx context.Context, title string, year int, category string) []gin.H {
	client := models.GlobalScrapeSettings.GetTmdbClient()
	lang := models.GlobalScrapeSettings.GetTmdbLanguage()
	imageBase := strings.TrimRight(models.GlobalScrapeSettings.GetTmdbImageUrl(), "/")
	out := make([]gin.H, 0, 10)
	add := func(t, original string, y int, tmdbID int64, mediaType, posterPath, overview string) {
		item := gin.H{
			"title":        t,
			"year":         y,
			"tmdb_id":      tmdbID,
			"media_type":   mediaType,
			"poster_url":   "",
			"poster_path":  posterPath,
			"overview":     overview,
		}
		if posterPath != "" {
			item["poster_url"] = imageBase + "/t/p/w185" + posterPath
		}
		if original != "" && original != t {
			item["original_title"] = original
		}
		out = append(out, item)
	}
	if category != "tv" {
		if resp, err := client.SearchMovie(title, year, lang, true, true); err == nil && resp != nil {
			for i, m := range resp.Results {
				if i >= 5 {
					break
				}
				add(m.Title, m.OriginalTitle, tmdbYearFromDate(m.ReleaseDate), m.ID, "movie", m.PosterPath, m.Overview)
			}
		}
	}
	if category != "movie" {
		if resp, err := client.SearchTv(title, year, lang, true); err == nil && resp != nil {
			for i, t := range resp.Results {
				if i >= 5 {
					break
				}
				add(t.Name, t.OriginalName, tmdbYearFromDate(t.FirstAirDate), t.ID, "tv", t.PosterPath, t.Overview)
			}
		}
	}
	return out
}

// tmdbYearFromDate 从 TMDB 日期字符串取年份
func tmdbYearFromDate(dateStr string) int {
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil {
			return y
		}
	}
	return 0
}

// ResolveMoviePilotFailedFile 确认媒体信息并重新整理识别失败的文件
// @Summary 确认整理识别失败文件
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/failed-files/:id/resolve [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ResolveMoviePilotFailedFile(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "记录 ID 无效", Data: nil})
		return
	}
	var req requests.ResolveMoviePilotFailedFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	f := models.GetMoviePilotFailedFile(uint(id))
	if f == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录不存在", Data: nil})
		return
	}
	var account models.Account
	if err := db.Db.First(&account, f.AccountID).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "上传账号不存在（ID=%d），请检查账号配置", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c, 10*time.Minute)
	defer cancel()
	dir, err := moviepilot.ResolveFailedFile(ctx, &account, f, req.MediaType, req.Title, req.Year, req.Season, req.TmdbID)
	if err != nil {
		f.Reason = "确认整理失败：" + err.Error()
		f.Status = string(models.MoviePilotFailedPending)
		_ = models.UpdateMoviePilotFailedFile(f)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	f.Status = string(models.MoviePilotFailedResolved)
	f.MediaType = req.MediaType
	f.Title = req.Title
	f.Year = req.Year
	f.Season = req.Season
	f.TmdbId = req.TmdbID
	f.Reason = ""
	_ = models.UpdateMoviePilotFailedFile(f)
	// 整理成功的目标目录触发 STRM 同步
	cfg := models.LoadMoviePilotConfig()
	if strings.TrimSpace(cfg.StrmLocalDir) != "" {
		sourcePath := strings.TrimRight(f.RootPath, "/") + "/" + dir
		moviepilot.TriggerStrmSyncForDir(&account, sourcePath, cfg.StrmLocalDir)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "整理完成", Data: map[string]any{"dir": dir}})
}

// SkipMoviePilotFailedFile 跳过识别失败文件
// @Summary 跳过识别失败文件
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/failed-files/:id/skip [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SkipMoviePilotFailedFile(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "记录 ID 无效", Data: nil})
		return
	}
	f := models.GetMoviePilotFailedFile(uint(id))
	if f == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录不存在", Data: nil})
		return
	}
	f.Status = string(models.MoviePilotFailedSkipped)
	if err := models.UpdateMoviePilotFailedFile(f); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "操作失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已跳过", Data: nil})
}

// TrendingMoviePilot TMDB 热门趋势列表（发现页数据源）
// @Summary TMDB 热门趋势列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/trending [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func TrendingMoviePilot(c *gin.Context) {
	mediaType := c.DefaultQuery("type", "movie")
	window := c.DefaultQuery("window", "day")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	lang := models.GlobalScrapeSettings.GetTmdbLanguage()
	tmdbClient := models.GlobalScrapeSettings.GetTmdbClient()

	resp := make([]TmdbSearchResp, 0)
	if mediaType == "tv" {
		tvResp, err := tmdbClient.GetTrendingTv(window, lang, page)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取热门剧集失败：" + err.Error(), Data: nil})
			return
		}
		for _, r := range tvResp.Results {
			resp = append(resp, TmdbSearchResp{
				TmdbID:        int(r.ID),
				Title:         r.Name,
				OriginalTitle: r.OriginalName,
				Year:          helpers.ParseYearFromDate(r.FirstAirDate),
				PosterUrl:     models.GetTmdbImageUrl(r.PosterPath),
				Overview:      r.Overview,
				VoteAverage:   r.VoteAverage,
			})
		}
	} else {
		movieResp, err := tmdbClient.GetTrendingMovies(window, lang, page)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取热门电影失败：" + err.Error(), Data: nil})
			return
		}
		for _, r := range movieResp.Results {
			resp = append(resp, TmdbSearchResp{
				TmdbID:        int(r.ID),
				Title:         r.Title,
				OriginalTitle: r.OriginalTitle,
				Year:          helpers.ParseYearFromDate(r.ReleaseDate),
				PosterUrl:     models.GetTmdbImageUrl(r.PosterPath),
				Overview:      r.Overview,
				VoteAverage:   r.VoteAverage,
			})
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取热门趋势成功", Data: resp})
}