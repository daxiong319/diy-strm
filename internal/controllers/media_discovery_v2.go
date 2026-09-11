package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
	"diy-strm/internal/models"
	"diy-strm/internal/qbittorrent"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// 影视发现复刻扩展接口：目录流（豆瓣/动漫）/ 热门演员 / 统一搜索 / 作品详情 /
// 猫眼榜单 / 资源动作（离线/转存/磁力）/ 自研订阅 / Emby 批量徽章 / 缺集扫描
// ---------------------------------------------------------------------------

func init() {
	// 注入分享链接转存执行器（接通既有 saveShareByLink 转存链）
	discovery.TransferShareFn = func(ctx context.Context, text, pwd, sourceType, targetDir string) (string, int, error) {
		return saveShareByLink(ctx, text, pwd, sourceType, targetDir)
	}
	// 注入磁力离线执行器（qBittorrent，取 MoviePilot 设置里的 qB 连接）
	discovery.OfflineLinkFn = func(ctx context.Context, link, savePath string) error {
		cfg := models.LoadMoviePilotConfig()
		if cfg == nil || strings.TrimSpace(cfg.QbittorrentURL) == "" {
			return fmt.Errorf("未配置 qBittorrent：请先在 MoviePilot 设置中填写 WebUI 地址与账号")
		}
		client := qbittorrent.NewClient(cfg.QbittorrentURL, cfg.QbittorrentUser, cfg.QbittorrentPass)
		return client.AddMagnet(ctx, link, savePath)
	}
}

// ---------------------------------------------------------------------------
// 目录流：豆瓣 / AniList / Bangumi
// ---------------------------------------------------------------------------

// GetMediaExploreDoubanCatalog GET /media-discovery/explore/douban/catalog
// 豆瓣目录（分类/排序筛选 + 后台预抓 + TMDB 匹配 + 目录状态）
func GetMediaExploreDoubanCatalog(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	wait, _ := strconv.Atoi(c.Query("wait"))
	force := c.Query("force") == "1" || c.Query("force") == "true"
	result, err := discovery.DiscoverDoubanCatalog(c.Query("media_type"), c.Query("tag"), c.Query("sort"), page, wait, force)
	if err != nil && (result == nil || len(result.Items) == 0) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "豆瓣目录加载失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaAnimeCatalog GET /media-discovery/anime/catalog
// 动漫目录（source=anilist/bangumi，类型/地区/年份/排序筛选 + TMDB 匹配）
func GetMediaAnimeCatalog(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	wait, _ := strconv.Atoi(c.Query("wait"))
	force := c.Query("force") == "1" || c.Query("force") == "true"
	filter := discovery.AnimeBrowseFilter{
		Genre:  c.Query("genre"),
		Region: c.Query("region"),
		Year:   c.Query("year"),
		Sort:   c.Query("sort"),
	}
	result, err := discovery.DiscoverAnimeCatalog(c.Request.Context(), c.Query("source"), filter, page, wait, force)
	fallbackSource := ""
	if err != nil && strings.EqualFold(strings.TrimSpace(c.Query("source")), "anilist") {
		// AniList 上游停服/故障时自动回退 Bangumi（同一筛选参数语义兼容）
		result, err = discovery.DiscoverAnimeCatalog(c.Request.Context(), "bangumi", filter, page, wait, force)
		if err == nil {
			fallbackSource = "bangumi"
		}
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "动漫目录加载失败：" + err.Error(), Data: nil})
		return
	}
	result.FallbackSource = fallbackSource
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// ---------------------------------------------------------------------------
// 热门演员 / 统一搜索
// ---------------------------------------------------------------------------

// GetMediaActors GET /media-discovery/actors?page=
func GetMediaActors(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	force := c.Query("force") == "1" || c.Query("force") == "true"
	result, err := discovery.Actors(page, force)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取热门演员失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaActorWorks GET /media-discovery/actors/:id/works
func GetMediaActorWorks(c *gin.Context) {
	actorID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || actorID <= 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的演员 ID", Data: nil})
		return
	}
	result, err := discovery.ActorWorks(actorID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取演员作品失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// GetMediaSearch GET /media-discovery/search?q=&media_type=movie|tv|person&page=
func GetMediaSearch(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	force := c.Query("force") == "1" || c.Query("force") == "true"
	result, err := discovery.SearchMedia(c.Request.Context(), c.Query("q"), c.Query("media_type"), page, force)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "搜索失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// ---------------------------------------------------------------------------
// 作品详情
// ---------------------------------------------------------------------------

// GetMediaDetails GET /media-discovery/details/:source/:type/:id
func GetMediaDetails(c *gin.Context) {
	result, err := discovery.MediaDetails(c.Param("source"), c.Param("type"), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "作品资料加载失败：" + err.Error(), Data: nil})
		return
	}
	// 附加订阅状态（详情页「已订阅」徽标与订阅入口）
	if entityKey, _ := result["entity_key"].(string); entityKey != "" {
		if sub, err := discovery.GetSubscriptionByKey(entityKey); err == nil {
			result["subscription"] = gin.H{
				"id": sub.ID, "status": sub.Status, "enabled": sub.Enabled,
				"target_provider": sub.TargetProvider, "rules_count": len(sub.Rules),
			}
		}
	}
	// 附加转存目标配置状态（资源卡片按钮可用性）
	result["transfer_targets"] = transferTargetsStatus()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// transferTargetsStatus 三网盘保存目录配置状态（详情页按钮 disabled 用）
func transferTargetsStatus() map[string]any {
	out := map[string]any{}
	for _, provider := range []string{"123", "guangya", "pan139"} {
		out[provider] = gin.H{
			"configured": discovery.TransferTargetConfigured(provider),
			"folder_name": discovery.TransferTargetDir(provider),
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 猫眼榜单（rankings provider=maoyan 扩展）
// ---------------------------------------------------------------------------

// GetMediaRankingsMaoyan GET /media-discovery/rankings/maoyan?category=all|tv|web_tv|variety|movie
func GetMediaRankingsMaoyan(c *gin.Context) {
	force := c.Query("force") == "1" || c.Query("force") == "true"
	groups, ok, err := discovery.MaoyanRankings(c.Request.Context(), c.Query("category"), force)
	if err != nil && !ok {
		// 上游被验证墙拦截时按 pending 语义返回，前端自动重试
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{
			"provider": "maoyan", "provider_label": "猫眼",
			"region": "CN", "category_label": maoyanCategoryLabelOf(c.Query("category")),
			"groups": []gin.H{}, "feed_status": "pending",
			"message": err.Error(),
			"items":   []gin.H{},
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{
		"provider": "maoyan", "provider_label": "猫眼",
		"region": "CN", "category_label": maoyanCategoryLabelOf(c.Query("category")),
		"groups": groups, "feed_status": "ok", "is_stale": false,
		"available_regions": []gin.H{{"key": "CN", "label": "中国"}},
	}})
}

func maoyanCategoryLabelOf(category string) string {
	switch category {
	case "tv":
		return "电视剧"
	case "web_tv":
		return "网络剧"
	case "variety":
		return "综艺"
	case "movie":
		return "网络电影"
	}
	return "全部榜单"
}

// ---------------------------------------------------------------------------
// 资源动作：离线 / 转存 / 磁力
// ---------------------------------------------------------------------------

// OfflineMediaResourceAPI POST /media-discovery/resources/offline {link, provider}
func OfflineMediaResourceAPI(c *gin.Context) {
	var req struct {
		Link     string `json:"link"`
		Provider string `json:"provider"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	link := strings.TrimSpace(req.Link)
	if link == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "链接内容不能为空", Data: nil})
		return
	}
	lower := strings.ToLower(link)
	if !strings.HasPrefix(lower, "magnet:?") && !strings.HasPrefix(lower, "ed2k://") {
		// 视为分享链接 → 走转存
		provider, err := discovery.NormalizeTransferProvider(req.Provider)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		if _, _, err := discovery.TransferShareLink(c.Request.Context(), link, "", provider); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "转存失败：" + err.Error(), Data: nil})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "资源处理成功", Data: gin.H{"message": "资源处理成功"}})
		return
	}
	if strings.HasPrefix(lower, "ed2k://") {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "ED2K 暂无可用离线通道，请使用「复制链接」", Data: nil})
		return
	}
	if err := discovery.SubmitOfflineLink(c.Request.Context(), link, req.Provider); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "离线提交失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "离线任务已提交", Data: gin.H{"message": "离线任务已提交"}})
}

// TransferMediaResourceAPI POST /media-discovery/resources/transfer
// {source, provider, slug, share_url}
func TransferMediaResourceAPI(c *gin.Context) {
	var req struct {
		Source   string `json:"source"`
		Provider string `json:"provider"`
		Slug     string `json:"slug"`
		ShareURL string `json:"share_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	provider, err := discovery.NormalizeTransferProvider(req.Provider)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	text, pwd := strings.TrimSpace(req.ShareURL), ""
	if strings.EqualFold(strings.TrimSpace(req.Source), "re0") && strings.TrimSpace(req.Slug) != "" {
		unlock, err := discovery.UnlockRe0ForTransfer(ctx, strings.TrimSpace(req.Slug))
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解锁失败：" + err.Error(), Data: nil})
			return
		}
		text, pwd = unlock.URL, unlock.AccessCode
		if pan := strings.TrimSpace(unlock.PanType); pan != "" {
			if normalized, perr := discovery.NormalizeTransferProvider(pan); perr == nil {
				provider = normalized
			}
		}
	}
	if text == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少分享链接", Data: nil})
		return
	}
	title, total, err := discovery.TransferShareLink(ctx, text, pwd, provider)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "转存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "转存成功", Data: gin.H{
		"transferred": true, "message": fmt.Sprintf("已转存「%s」（%d 个文件）", title, total), "title": title, "total": total,
	}})
}

// TorrentMediaResourceAPI POST /media-discovery/resources/torrent（multipart: provider + file）
func TorrentMediaResourceAPI(c *gin.Context) {
	provider := c.PostForm("provider")
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少种子文件", Data: nil})
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取种子文件失败", Data: nil})
		return
	}
	defer f.Close()
	content := make([]byte, fileHeader.Size)
	if _, err := f.Read(content); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取种子文件失败", Data: nil})
		return
	}
	magnet, err := discovery.TorrentBytesToMagnet(content)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := discovery.SubmitOfflineLink(c.Request.Context(), magnet, provider); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "离线提交失败：" + err.Error(), Data: gin.H{"magnet": magnet}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "种子离线任务已提交", Data: gin.H{
		"magnet": magnet, "torrent_name": fileHeader.Filename, "message": "种子离线任务已提交",
	}})
}

// ---------------------------------------------------------------------------
// 自研订阅
// ---------------------------------------------------------------------------

// MediaSubscriptionsAPI GET/POST /media-discovery/subscriptions
func MediaSubscriptionsAPI(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		var payload discovery.SubscriptionUpsertPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
			return
		}
		sub, warning, err := discovery.SaveSubscription(&payload)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅保存失败：" + err.Error(), Data: nil})
			return
		}
		message := "订阅已保存"
		if warning != "" {
			message = warning
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: gin.H{
			"subscription": sub, "target_warning": warning,
		}})
		return
	}
	enabledParam := c.Query("enabled")
	var enabledFilter *bool
	if enabledParam == "true" || enabledParam == "1" {
		v := true
		enabledFilter = &v
	} else if enabledParam == "false" || enabledParam == "0" {
		v := false
		enabledFilter = &v
	}
	list, err := discovery.ListSubscriptions(enabledFilter)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取订阅失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": list}})
}

// MediaSubscriptionDetailAPI PATCH/DELETE /media-discovery/subscriptions/:id
func MediaSubscriptionDetailAPI(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	if c.Request.Method == http.MethodDelete {
		if err := discovery.DeleteSubscription(uint(id)); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除订阅失败：" + err.Error(), Data: nil})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已删除", Data: true})
		return
	}
	// PATCH：部分更新（启用状态/间隔/规则）
	var payload discovery.SubscriptionUpsertPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	sub, err := discovery.GetSubscription(uint(id))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	upsert := &discovery.SubscriptionUpsertPayload{
		Source: sub.Source, EntityType: sub.EntityType, ExternalID: sub.ExternalID,
		TMDBID: sub.TMDBID, MediaType: sub.MediaType,
		Title: sub.Title, OriginalTitle: sub.OriginalTitle, Poster: sub.Poster,
		TargetProvider: firstNonEmptyStr(payload.TargetProvider, sub.TargetProvider),
		Enabled:        payload.Enabled, IntervalMinutes: payload.IntervalMinutes,
		Preferences: payload.Preferences, Rules: payload.Rules, Metadata: payload.Metadata,
	}
	if upsert.IntervalMinutes <= 0 {
		upsert.IntervalMinutes = sub.IntervalMinutes
	}
	updated, warning, err := discovery.SaveSubscription(upsert)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅更新失败：" + err.Error(), Data: nil})
		return
	}
	message := "订阅已更新"
	if warning != "" {
		message = warning
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: gin.H{"subscription": updated}})
}

// MediaSubscriptionRunAPI POST /media-discovery/subscriptions/:id/run
func MediaSubscriptionRunAPI(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	run, err := discovery.ProcessSubscription(uint(id), "manual")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅检查失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已完成检查", Data: gin.H{
		"run": gin.H{"id": run.ID, "status": run.Status, "message": run.Message,
			"resource_count": run.ResourceCount, "transferred_count": run.TransferredCount},
	}})
}

// MediaSubscriptionRunDueAPI POST /media-discovery/subscriptions/run-due
func MediaSubscriptionRunDueAPI(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	results := discovery.RunDueSubscriptions(limit)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"results": results}})
}

// MediaSubscriptionHistoryAPI GET /media-discovery/subscriptions/history?subscription_id=&limit=
func MediaSubscriptionHistoryAPI(c *gin.Context) {
	subscriptionID, _ := strconv.ParseUint(c.Query("subscription_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	runs, err := discovery.ListSubscriptionRuns(uint(subscriptionID), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取执行历史失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": runs}})
}

// MediaSubscriptionEventsAPI GET /media-discovery/subscriptions/events?subscription_id=&limit=
func MediaSubscriptionEventsAPI(c *gin.Context) {
	subscriptionID, _ := strconv.ParseUint(c.Query("subscription_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := discovery.ListSubscriptionEvents(uint(subscriptionID), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取事件失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": events}})
}

// MediaSubscriptionCandidatesAPI GET /media-discovery/subscriptions/candidates?subscription_id=&status=&limit=
func MediaSubscriptionCandidatesAPI(c *gin.Context) {
	subscriptionID, _ := strconv.ParseUint(c.Query("subscription_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := discovery.ListSubscriptionItems(uint(subscriptionID), c.Query("status"), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取候选失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": items}})
}

// ---------------------------------------------------------------------------
// Emby 批量徽章
// ---------------------------------------------------------------------------

type embyCardInput struct {
	Key          string `json:"key"`
	MediaType    string `json:"media_type"`
	TmdbID       int64  `json:"tmdb_id"`
	Title        string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year         int    `json:"year"`
	TotalEpisodes int   `json:"total_episodes"`
}

type embyCardResult struct {
	State        string `json:"state"` // in_library/missing/serializing/error/not_found
	DisplayLabel string `json:"display_label"`
	AvailableCount int  `json:"available_count"`
	MissingCount int    `json:"missing_count"`
	Message      string `json:"message,omitempty"`
}

var (
	embyCardIndex     map[int64]embyCardSeriesInfo
	embyCardIndexAt   time.Time
	embyCardIndexLock = &sync.Mutex{}
)

type embyCardSeriesInfo = discovery.EmbyCardSeriesInfo

// EmbyCardsAPI POST /media-discovery/emby/cards
// 批量查询条目本地入库状态：已入库/连载中/缺集/未入库
func EmbyCardsAPI(c *gin.Context) {
	var req struct {
		Items []embyCardInput `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	_, clientErr := discovery.MediaEmbyClientForCards()
	configured := clientErr == nil
	results := make([]gin.H, 0, len(req.Items))
	pendingKeys := []string{}
	if configured && len(req.Items) > 0 {
		refreshEmbyCardIndex()
	}
	for _, item := range req.Items {
		key := item.Key
		if key == "" {
			key = fmt.Sprintf("%s:%s", item.MediaType, firstNonEmptyStr(item.Title, strconv.FormatInt(item.TmdbID, 10)))
		}
		if !configured {
			results = append(results, gin.H{"key": key, "result": embyCardResult{State: "error", Message: "Emby 未配置"}})
			continue
		}
		info, ok := embyCardIndex[item.TmdbID]
		if !ok {
			results = append(results, gin.H{"key": key, "result": embyCardResult{State: "not_found", DisplayLabel: "未入库"}})
			continue
		}
		if item.MediaType == "tv" {
			if item.TotalEpisodes <= 0 {
				// 总集数未知：预热 TMDB 元数据后前端延迟刷新
				pendingKeys = append(pendingKeys, key)
				discovery.RequestTvProgressPreheat(item.TmdbID)
				results = append(results, gin.H{"key": key, "result": embyCardResult{State: "in_library", DisplayLabel: "已入库"}})
				continue
			}
			available := discovery.SeriesAvailableEpisodes(info.ItemID)
			missing := item.TotalEpisodes - available
			state, label := "in_library", "已完整入库"
			switch {
			case missing <= 0:
				missing = 0
			case strings.EqualFold(info.Status, "Returning Series") || strings.EqualFold(info.Status, "Continuing"):
				state, label = "serializing", fmt.Sprintf("连载中 · 已入库%d集", available)
			default:
				state, label = "missing", fmt.Sprintf("有缺集 · 缺失%d/%d集", missing, item.TotalEpisodes)
			}
			results = append(results, gin.H{"key": key, "result": embyCardResult{
				State: state, DisplayLabel: label, AvailableCount: available, MissingCount: missing,
			}})
			continue
		}
		results = append(results, gin.H{"key": key, "result": embyCardResult{State: "in_library", DisplayLabel: "已入库"}})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{
		"configured": configured, "items": results,
		"progress_pending_keys": pendingKeys,
		"message": map[bool]string{true: "", false: "Emby 未配置：请在发现-基础配置中启用 Emby 媒体库"}[configured],
	}})
}

// refreshEmbyCardIndex 刷新 TMDB→系列 索引（5 分钟 TTL）
func refreshEmbyCardIndex() {
	embyCardIndexLock.Lock()
	defer embyCardIndexLock.Unlock()
	if embyCardIndex != nil && time.Since(embyCardIndexAt) < 5*time.Minute {
		return
	}
	index, err := discovery.BuildEmbyCardIndex()
	if err != nil {
		return
	}
	embyCardIndex = index
	embyCardIndexAt = time.Now()
}

// TvProgressPreheatAPI POST /media-discovery/emby/tv-progress/preheat {tmdb_ids, priority}
func TvProgressPreheatAPI(c *gin.Context) {
	var req struct {
		TmdbIDs  []int64 `json:"tmdb_ids"`
		Priority int     `json:"priority"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if len(req.TmdbIDs) > 48 {
		req.TmdbIDs = req.TmdbIDs[:48]
	}
	for _, id := range req.TmdbIDs {
		discovery.RequestTvProgressPreheat(id)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"queued": len(req.TmdbIDs)}})
}

// EmbyTestMediaAPI POST /media-discovery/emby/test {media_emby:{server_url, api_key, enabled}}
func EmbyTestMediaAPI(c *gin.Context) {
	var req struct {
		MediaEmby struct {
			ServerURL string `json:"server_url"`
			ApiKey    string `json:"api_key"`
			Enabled   *bool  `json:"enabled"`
		} `json:"media_emby"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	serverURL := strings.TrimSpace(req.MediaEmby.ServerURL)
	apiKey := strings.TrimSpace(req.MediaEmby.ApiKey)
	if serverURL == "" || apiKey == "" {
		// 留空用已保存配置测试
		if _, savedURL, savedKey := discovery.MediaEmbyConfig(); savedURL != "" && savedKey != "" {
			if serverURL == "" {
				serverURL = savedURL
			}
			if apiKey == "" {
				apiKey = savedKey
			}
		}
	}
	if serverURL == "" || apiKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请填写 Emby 服务器地址与 API Key", Data: nil})
		return
	}
	total, err := discovery.TestMediaEmbyConnection(serverURL, apiKey)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("连接成功 · 已识别 %d 个电影或剧集项目", total), Data: gin.H{"total": total}})
}

// ---------------------------------------------------------------------------
// Emby 缺集扫描
// ---------------------------------------------------------------------------

// EmbyMissingStatusAPI GET /media-discovery/emby-missing/status
func EmbyMissingStatusAPI(c *gin.Context) {
	result, err := discovery.EmbyMissingStatus()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: result})
}

// EmbyMissingLibrariesAPI GET /media-discovery/emby-missing/libraries
func EmbyMissingLibrariesAPI(c *gin.Context) {
	items, err := discovery.EmbyMissingLibraries()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": items}})
}

// EmbyMissingScansAPI GET/POST /media-discovery/emby-missing/scans
func EmbyMissingScansAPI(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		var req struct {
			LibraryIDs []string `json:"library_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
			return
		}
		scan, err := discovery.StartEmbyMissingScan(req.LibraryIDs)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已进入扫描队列", Data: gin.H{"scan": scan}})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": discovery.EmbyMissingScansList(limit)}})
}

// EmbyMissingResultsAPI GET /media-discovery/emby-missing/results?scan_id=&limit=
func EmbyMissingResultsAPI(c *gin.Context) {
	scanID, _ := strconv.ParseUint(c.Query("scan_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	results, err := discovery.EmbyMissingResultsList(uint(scanID), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": results}})
}

// EmbyMissingEventsAPI GET /media-discovery/emby-missing/events?scan_id=&limit=
func EmbyMissingEventsAPI(c *gin.Context) {
	scanID, _ := strconv.ParseUint(c.Query("scan_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := discovery.EmbyMissingEventsList(uint(scanID), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": events}})
}

// EmbyMissingSubscriptionsAPI GET/POST /media-discovery/emby-missing/subscriptions
func EmbyMissingSubscriptionsAPI(c *gin.Context) {
	if c.Request.Method == http.MethodPost {
		var req discovery.MissingSubscriptionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
			return
		}
		result, err := discovery.CreateMissingSubscriptions(&req)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "补档订阅已创建", Data: result})
		return
	}
	var subs []discovery.DiscoverySubscription
	query := db.Db.Where("entity_key LIKE ?", "emby-missing:%").Order("id desc")
	if err := query.Find(&subs).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"items": subs}})
}

// EmbyMissingSubscriptionRunAPI POST /media-discovery/emby-missing/subscriptions/:id/run
func EmbyMissingSubscriptionRunAPI(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	run, err := discovery.ProcessSubscription(uint(id), "emby_missing_manual")
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "补档检查失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已开始检查该补档订阅", Data: gin.H{
		"run": gin.H{"id": run.ID, "status": run.Status, "message": run.Message},
	}})
}
