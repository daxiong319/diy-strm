package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/helpers"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
	"diy-strm/internal/tgchannel"
)

// hiveRefreshingCount 正在后台执行的订阅搜索数（列表接口 refreshing_counts 字段，mediavault 对齐）
var hiveRefreshingCount atomic.Int64

// ---------------------------------------------------------------------------
// 订阅 API 扩展（mediavault /subscription 对齐）
// ---------------------------------------------------------------------------

// submitSubscriptionRun 提交一条订阅的后台执行（防重入 + 刷新计数）。返回是否提交成功。
func submitSubscriptionRun(sub *models.CloudSubscription) bool {
	if _, running := subscriptionRunLocks.LoadOrStore(sub.ID, struct{}{}); running {
		return false
	}
	hiveRefreshingCount.Add(1)
	runAuthBackgroundTask(func() {
		defer subscriptionRunLocks.Delete(sub.ID)
		defer hiveRefreshingCount.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("订阅 #%d 后台执行 panic：%v", sub.ID, r)
			}
		}()
		var msg string
		var ok bool
		if sub.ResourceSource == "hdhive" {
			msg, ok = RunHiveSubscriptionOnce(sub)
		} else {
			msg, ok = RunSubscriptionOnce(sub)
		}
		if ok {
			helpers.AppLogger.Infof("后台执行订阅：%s", msg)
		} else {
			helpers.AppLogger.Errorf("后台执行订阅：%s", msg)
		}
	})
	return true
}

// RunAllSubscriptionsSearchAPI 一键补全搜索（POST /cloud/subscriptions/run-search）
// 后台依次执行全部启用中的影巢订阅；接口立即返回「订阅搜索已触发，后台执行中」
func RunAllSubscriptionsSearchAPI(c *gin.Context) {
	subs, err := models.ListSubscriptionsByResourceSource("hdhive")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取订阅失败：" + err.Error(), Data: nil})
		return
	}
	submitted := 0
	busy := 0
	for i := range subs {
		sub := &subs[i]
		if !sub.Enabled || sub.Status == "paused" {
			continue
		}
		if submitSubscriptionRun(sub) {
			submitted++
		} else {
			busy++
		}
	}
	msg := fmt.Sprintf("订阅搜索已触发，后台执行中（已提交 %d 条；%d 条正在执行已跳过）", submitted, busy)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
}

// RunSingleSubscriptionSearchAPI 单条搜索（POST /cloud/subscriptions/run-search/:id）
func RunSingleSubscriptionSearchAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(id)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	if !submitSubscriptionRun(sub) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该订阅正在执行中，请稍后再试", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅搜索已触发，后台执行中", Data: nil})
}

// ResetTransferredAPI 重置已转存记录（POST /cloud/subscriptions/:id/reset-transferred）
// 清空该订阅的转存记录（保留日志），下次搜索将允许重新转存
func ResetTransferredAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(id)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	if err := models.DeleteSubscriptionRecords(id); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重置失败：" + err.Error(), Data: nil})
		return
	}
	now := time.Now()
	sub.LastSearchAt = &now
	_ = models.SaveCloudSubscription(sub)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已重置已转存记录，下次搜索将重新转存", Data: nil})
}

// BatchSubscriptionPauseAPI 批量暂停（POST /cloud/subscriptions/batch/pause {ids}）
func BatchSubscriptionPauseAPI(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择订阅", Data: nil})
		return
	}
	if err := models.UpdateCloudSubscriptionsStatus(req.IDs, true); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量暂停失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已暂停 %d 条订阅", len(req.IDs)), Data: nil})
}

// BatchSubscriptionResumeAPI 批量恢复（POST /cloud/subscriptions/batch/resume {ids}）
func BatchSubscriptionResumeAPI(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择订阅", Data: nil})
		return
	}
	if err := models.UpdateCloudSubscriptionsStatus(req.IDs, false); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量恢复失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已恢复 %d 条订阅", len(req.IDs)), Data: nil})
}

// BatchSubscriptionDeleteAPI 批量删除（POST /cloud/subscriptions/batch/delete {ids}）
func BatchSubscriptionDeleteAPI(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择订阅", Data: nil})
		return
	}
	if err := models.DeleteCloudSubscriptionsBatch(req.IDs); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已删除 %d 条订阅（含转存记录与日志）", len(req.IDs)), Data: nil})
}

// SubscriptionLogsAPI 订阅日志（GET /cloud/subscriptions/:id/logs）
// 时间线：action=search（搜索轮次）/ transfer（转存动作），status=success 或失败原因
func SubscriptionLogsAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	logs, err := models.ListSubscriptionLogs(id)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: gin.H{"logs": logs}})
}

// SubscriptionDetailAPI 订阅详情（GET /cloud/subscriptions/detail/:id?media_type=movie|tv）
// TMDB 详情 + 主创（credits），布局对齐 mediavault 详情弹窗
func SubscriptionDetailAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的 TMDB ID", Data: nil})
		return
	}
	mediaType := strings.TrimSpace(c.Query("media_type"))
	lang := models.GlobalScrapeSettings.GetTmdbLanguage()
	tmdbClient := models.GlobalScrapeSettings.GetTmdbClient()
	type person struct {
		Name      string `json:"name"`
		Character string `json:"character"`
		Job       string `json:"job"`
		Profile   string `json:"profile_url"`
	}
	data := gin.H{}
	if mediaType == "movie" {
		d, err := tmdbClient.GetMovieDetail(int64(id), lang)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取电影详情失败：" + err.Error(), Data: nil})
			return
		}
		credits, _ := tmdbClient.GetMoviePepoles(int64(id), lang)
		genres := make([]string, 0, len(d.Genres))
		for _, g := range d.Genres {
			genres = append(genres, g.Name)
		}
		cast := make([]person, 0, 20)
		if credits != nil {
			for _, cst := range credits.Cast {
				if len(cast) >= 20 {
					break
				}
				cast = append(cast, person{Name: cst.Name, Character: cst.Character, Profile: models.GetTmdbImageUrl(cst.ProfilePath)})
			}
		}
		crew := make([]person, 0, 10)
		if credits != nil {
			for _, cr := range credits.Crew {
				if len(crew) >= 10 {
					break
				}
				crew = append(crew, person{Name: cr.Name, Job: cr.Job})
			}
		}
		data = gin.H{
			"tmdb_id": d.ID, "title": d.Title, "original_title": d.OriginalTitle,
			"vote_average": d.VoteAverage, "poster_url": models.GetTmdbImageUrl(d.PosterPath),
			"backdrop_url": models.GetTmdbImageUrl(d.BackdropPath), "overview": d.Overview,
			"genres": genres, "release_date": d.ReleaseDate, "runtime": d.Runtime,
			"status": d.Status, "crew": crew, "cast": cast,
		}
	} else {
		d, err := tmdbClient.GetTvDetail(int64(id), lang)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取剧集详情失败：" + err.Error(), Data: nil})
			return
		}
		credits, _ := tmdbClient.GetTvCredits(int64(id), lang)
		genres := make([]string, 0, len(d.Genres))
		for _, g := range d.Genres {
			genres = append(genres, g.Name)
		}
		networks := make([]string, 0, len(d.Networks))
		for _, n := range d.Networks {
			networks = append(networks, n.Name)
		}
		cast := make([]person, 0, 20)
		if credits != nil {
			for _, cst := range credits.Cast {
				if len(cast) >= 20 {
					break
				}
				cast = append(cast, person{Name: cst.Name, Character: cst.Character, Profile: models.GetTmdbImageUrl(cst.ProfilePath)})
			}
		}
		crew := make([]person, 0, 10)
		if credits != nil {
			for _, cr := range credits.Crew {
				if len(crew) >= 10 {
					break
				}
				crew = append(crew, person{Name: cr.Name, Job: cr.Job})
			}
		}
		data = gin.H{
			"tmdb_id": d.ID, "title": d.Name, "original_title": d.OriginalName,
			"vote_average": d.VoteAverage, "poster_url": models.GetTmdbImageUrl(d.PosterPath),
			"backdrop_url": models.GetTmdbImageUrl(d.BackdropPath), "overview": d.Overview,
			"genres": genres, "release_date": d.FirstAirDate,
			"number_of_seasons": d.NumberOfSeasons, "number_of_episodes": d.NumberOfEpisodes,
			"status": d.Status, "networks": networks, "crew": crew, "cast": cast,
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: data})
}

// ---------------------------------------------------------------------------
// 手动资源搜索（SSE 流式，mediavault /resource-search/search/stream 对齐）
// ---------------------------------------------------------------------------

// 中文数字（mediavault iF：关键词多写法构造用）
var hiveCNNumerals = []string{"一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一", "十二", "十三", "十四", "十五", "十六", "十七", "十八", "十九", "二十", "二十一", "二十二", "二十三", "二十四", "二十五", "二十六", "二十七", "二十八", "二十九", "三十"}

func hiveCNNum(n int) string {
	if n >= 1 && n <= len(hiveCNNumerals) {
		return hiveCNNumerals[n-1]
	}
	return strconv.Itoa(n)
}

// buildHiveSearchKeywords 构造多写法搜索关键词（对齐 mediavault aF）
// movie / tv 第 1 季 → 原词；tv + 指定季 → 「标题 第N季 / 标题 S0N / 标题」
func buildHiveSearchKeywords(title, mediaType string, season int) []string {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	if mediaType == "tv" && season > 1 {
		return []string{
			fmt.Sprintf("%s 第%s季", title, hiveCNNum(season)),
			fmt.Sprintf("%s S%02d", title, season),
			title,
		}
	}
	return []string{title}
}

type hiveSearchSSE struct {
	Type    string `json:"type"`
	Engine  string `json:"engine,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func writeHiveSSE(c *gin.Context, ev hiveSearchSSE) {
	b, _ := json.Marshal(ev)
	c.Writer.WriteString("data: " + string(b) + "\n\n")
	c.Writer.Flush()
}

// HiveManualSearchAPI 手动资源搜索（POST /cloud/hive/search/stream）
// body: {keyword, media_type, tmdb_id, season, engines?}
// SSE 事件：init {labels,engines} → progress {engine,status} → result {engine,data} → done {enabled}
func HiveManualSearchAPI(c *gin.Context) {
	var req struct {
		Keyword   string   `json:"keyword"`
		MediaType string   `json:"media_type"` // movie / tv（识别选片后；原词搜索为空）
		TMDBID    int64    `json:"tmdb_id"`
		Season    int      `json:"season"`
		Engines   []string `json:"engines"` // 指定引擎（缺省=全部启用）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	req.Keyword = strings.TrimSpace(req.Keyword)
	if req.Keyword == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请输入搜索关键词", Data: nil})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	hdhiveOn := models.GetHiveEnabled() && req.TMDBID > 0
	telegramOn := manualTelegramSearchable()

	engines := []string{}
	labels := map[string]string{"hdhive": "影巢", "telegram": "Telegram"}
	requested := map[string]bool{}
	for _, e := range req.Engines {
		requested[e] = true
	}
	if len(requested) > 0 {
		hdhiveOn = hdhiveOn && requested["hdhive"]
		telegramOn = telegramOn && requested["telegram"]
	}
	if hdhiveOn {
		engines = append(engines, "hdhive")
	}
	if telegramOn {
		engines = append(engines, "telegram")
	}
	writeHiveSSE(c, hiveSearchSSE{Type: "init", Data: gin.H{"labels": labels, "engines": engines}})

	keywords := buildHiveSearchKeywords(req.Keyword, req.MediaType, req.Season)
	if len(keywords) == 0 {
		keywords = []string{req.Keyword}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	done := make(chan struct{})
	var wg sync.WaitGroup

	if hdhiveOn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hiveManualSearchHDHive(ctx, c, req.MediaType, req.TMDBID)
		}()
	}
	if telegramOn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hiveManualSearchTelegram(ctx, c, keywords)
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	writeHiveSSE(c, hiveSearchSSE{Type: "done", Data: gin.H{"enabled": gin.H{
		"hdhive": hdhiveOn, "telegram": telegramOn, "pansou": false,
	}}})
}

// hiveManualSearchHDHive 影巢引擎：按 TMDB 资源查询（四通道负载均衡）
func hiveManualSearchHDHive(ctx context.Context, c *gin.Context, mediaType string, tmdbID int64) {
	writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "searching"})
	if tmdbID <= 0 {
		writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "error", Message: "缺少 TMDB ID"})
		return
	}
	query, err := models.HiveQueryResourcesWithFailover(ctx, mediaType, strconv.FormatInt(tmdbID, 10))
	if err != nil {
		writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "error", Message: err.Error()})
		return
	}
	resp := query.Resp
	if !resp.Success {
		msg := resp.Message
		if msg == "" {
			msg = resp.Description
		}
		if msg == "" {
			msg = "搜索失败"
		}
		writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "error", Message: msg})
		return
	}
	var resources []hdhive.Resource
	if len(resp.Data) > 0 && string(resp.Data) != "null" {
		if err := json.Unmarshal(resp.Data, &resources); err != nil {
			writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "error", Message: "解析资源列表失败"})
			return
		}
	}
	items := make([]gin.H, 0, len(resources))
	for _, r := range resources {
		user := gin.H{"nickname": "", "avatar_url": ""}
		if r.User != nil {
			user = gin.H{"nickname": r.User.Nickname, "avatar_url": r.User.AvatarURL}
		}
		items = append(items, gin.H{
			"slug": r.Slug, "title": r.Title, "pan_type": r.PanType, "media_type": mediaType,
			"user": user, "is_official": r.IsOfficial, "is_unlocked": r.IsUnlocked,
			"unlock_points": r.UnlockPoints, "validate_status": r.ValidateStatus,
			"size": r.ShareSize, "video_resolution": r.VideoResolution, "source": r.Source,
			"subtitle_language": r.SubtitleLanguage, "remark": r.Remark, "created_at": r.CreatedAt,
		})
	}
	writeHiveSSE(c, hiveSearchSSE{Type: "result", Engine: "hdhive", Data: items})
	writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "hdhive", Status: "done"})
}

// manualTelegramSearchable 是否有可搜索的启用频道
func manualTelegramSearchable() bool {
	channels, err := models.ListCloudChannels("")
	if err != nil {
		return false
	}
	for i := range channels {
		if channels[i].Enabled {
			return true
		}
	}
	return false
}

// hiveManualSearchTelegram Telegram 引擎：遍历全部启用频道抓取并匹配关键词
func hiveManualSearchTelegram(ctx context.Context, c *gin.Context, keywords []string) {
	writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "telegram", Status: "searching"})
	channels, err := models.ListCloudChannels("")
	if err != nil {
		writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "telegram", Status: "error", Message: "读取频道失败"})
		return
	}
	items := make([]gin.H, 0, 100)
	perChannel := 30
	for i := range channels {
		ch := &channels[i]
		if !ch.Enabled || ctx.Err() != nil {
			continue
		}
		channel := ch.ChannelName()
		posts, err := tgchannel.ParseChannelPage(ctx, channel)
		if err != nil {
			continue
		}
		count := 0
		for _, p := range posts {
			if count >= perChannel {
				break
			}
			if !tgchannel.MatchKeywords(p.Text, keywords) {
				continue
			}
			count++
			links := make([]gin.H, 0, len(p.Links))
			shareLink := ""
			for _, l := range p.Links {
				links = append(links, gin.H{"type": l.Type, "url": l.URL, "pwd": l.Pwd})
				if shareLink == "" {
					shareLink = l.URL
				}
			}
			items = append(items, gin.H{
				"channel": channel, "channel_title": "@" + channel,
				"message_id": p.PostID, "title": strings.TrimSpace(p.Text),
				"url": buildTGMessageURL(channel, p.PostID),
				"share_link": shareLink, "links": links, "date": p.Time,
			})
			if len(items) >= 100 {
				break
			}
		}
		if len(items) >= 100 {
			break
		}
	}
	writeHiveSSE(c, hiveSearchSSE{Type: "result", Engine: "telegram", Data: items})
	writeHiveSSE(c, hiveSearchSSE{Type: "progress", Engine: "telegram", Status: "done"})
}

// ---------------------------------------------------------------------------
// 影巢解锁与手动转存（mediavault hdhive/unlock + 转存操作对齐）
// ---------------------------------------------------------------------------

// HiveUnlockAPI 解锁影巢资源（POST /cloud/hive/unlock {slug}）
// 走四通道负载均衡；返回 {url, full_url, pan_type, title, access_code}
func HiveUnlockAPI(c *gin.Context) {
	var req struct {
		Slug string `json:"slug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Slug) == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少资源 slug", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	if err := hdhive.AcquireUnlock(ctx); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解锁节流等待失败：" + err.Error(), Data: nil})
		return
	}
	unlockResp, _, uerr := models.HiveCallWithFailover(ctx, "", func(cl hdhive.ChannelClient) (*hdhive.OAuthAPIResponse, error) {
		return cl.UnlockResource(ctx, req.Slug)
	})
	if uerr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解锁失败：" + uerr.Error(), Data: nil})
		return
	}
	if !unlockResp.Success {
		msg := unlockResp.Message
		if msg == "" {
			msg = unlockResp.Description
		}
		if msg == "" {
			msg = "解锁失败"
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "影巢接口返回：" + msg, Data: nil})
		return
	}
	var unlock hdhive.UnlockResult
	if err := json.Unmarshal(unlockResp.Data, &unlock); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解锁结果解析失败", Data: nil})
		return
	}
	fullURL := strings.TrimSpace(unlock.FullURL)
	if fullURL == "" {
		fullURL = strings.TrimSpace(unlock.URL)
	}
	panicType := strings.TrimSpace(unlock.PanType)
	// 解锁响应未带网盘类型时，用分享详情补一次
	if panicType == "" {
		if detailResp, _, derr := models.HiveCallWithFailover(ctx, "", func(cl hdhive.ChannelClient) (*hdhive.OAuthAPIResponse, error) {
			return cl.GetShareDetail(ctx, req.Slug)
		}); derr == nil && detailResp.Success {
			var detail struct {
				PanType string `json:"pan_type"`
			}
			if json.Unmarshal(detailResp.Data, &detail) == nil {
				panicType = detail.PanType
			}
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "解锁成功", Data: gin.H{
		"url": fullURL, "full_url": fullURL, "pan_type": panicType,
		"title": unlock.Title, "access_code": unlock.AccessCode,
	}})
}

// HiveManualTransferAPI 手动转存（POST /cloud/hive/transfer {url, access_code, source_type, target_dir}）
// 对齐订阅引擎转存：解析分享链接并保存到目标网盘目录；返回 {title, total}
func HiveManualTransferAPI(c *gin.Context) {
	var req struct {
		URL        string `json:"url"`
		AccessCode string `json:"access_code"`
		SourceType string `json:"source_type"` // 目标网盘：123 / guangyapan / pan139
		TargetDir  string `json:"target_dir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	req.TargetDir = strings.TrimSpace(req.TargetDir)
	if req.URL == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少分享链接", Data: nil})
		return
	}
	if req.TargetDir == "" {
		req.TargetDir = "/"
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	title, total, err := saveShareByLink(ctx, req.URL, req.AccessCode, req.SourceType, req.TargetDir)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "转存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("转存成功，%d 个文件", total), Data: gin.H{
		"title": title, "total": total,
	}})
}