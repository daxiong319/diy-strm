package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// GetCloudSettings 查询云盘转存目录设置（?source_type=xxx）
func GetCloudSettings(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Query("source_type"))
	if sourceType == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少参数 source_type", Data: nil})
		return
	}
	vals := models.GetCloudSaveDirWithDefault(sourceType, "/")
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: vals})
}

// SetCloudSettingAPI 设置云盘转存目录（POST {source_type, key, value}）
func SetCloudSettingAPI(c *gin.Context) {
	var req struct {
		SourceType string `json:"source_type"`
		Key        string `json:"key"`
		Value      string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.SourceType == "" || req.Key == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少 source_type 或 key", Data: nil})
		return
	}
	if err := models.SetCloudSaveDir(req.SourceType, req.Key, strings.TrimSpace(req.Value)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "保存成功", Data: nil})
}

// ListCloudSubscriptionsAPI 订阅列表（?resource_source= / ?source_type= 可选）
// 响应对齐 mediavault 订阅页：{items, total, completed, refreshing_counts}
//   - items：按 media_type（movie/tv）与 status（active/completed）过滤后的订阅列表
//   - total / completed：供页头「当前共有 n 个订阅 / 已完成 n 个」统计（不随 status 过滤）
//   - refreshing_counts：正在后台执行的订阅搜索数（前端轮询直至归零）
func ListCloudSubscriptionsAPI(c *gin.Context) {
	resourceSource := strings.TrimSpace(c.Query("resource_source"))
	sourceType := strings.TrimSpace(c.Query("source_type"))
	mediaType := strings.TrimSpace(c.Query("media_type"))
	status := strings.TrimSpace(c.Query("status"))
	var (
		subs []models.CloudSubscription
		err  error
	)
	if resourceSource != "" {
		subs, err = models.ListSubscriptionsByResourceSource(resourceSource)
	} else {
		subs, err = models.ListCloudSubscriptions(sourceType)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	items := make([]models.CloudSubscription, 0, len(subs))
	total := 0
	completed := 0
	for i := range subs {
		sub := subs[i]
		if mediaType != "" && sub.MediaType != mediaType {
			continue
		}
		total++
		finished := sub.FinishedAt != nil || sub.Status == "completed"
		if finished {
			completed++
		}
		if status == "completed" && !finished {
			continue
		}
		if status == "active" && finished {
			continue
		}
		sub.OldCount = models.CountSuperseded(sub.ID)
		// 媒体库已有集数/入库标记（支撑卡片「全」「已入库」角标）
		if sub.MediaType == "tv" {
			sub.ExistingEpisodes = models.CountDistinctEpisodes(sub.ID, sub.TMDBID, sub.Season)
		} else if models.HasSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season) {
			sub.ExistingEpisodes = 1
		}
		items = append(items, sub)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: gin.H{
		"items":             items,
		"total":             total,
		"completed":         completed,
		"refreshing_counts": hiveRefreshingCount.Load(),
	}})
}

// CreateCloudSubscriptionAPI 新增订阅（POST {source_type, channel, keywords, target_dir, enabled}）
// 影巢订阅（resource_source=hdhive）无需频道名，需提供 TMDB 选片信息
func CreateCloudSubscriptionAPI(c *gin.Context) {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	body, _ := json.Marshal(raw)
	var req models.CloudSubscription
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	// 布尔开关默认值（未显式传 false 时）
	if _, ok := raw["auto_finish"]; !ok {
		req.AutoFinish = true
	}
	if _, ok := raw["replace_old"]; !ok {
		req.ReplaceOld = true
	}
	if !ensureSourceTypeValid(req.SourceType) {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "不支持的网盘类型：" + req.SourceType, Data: nil})
		return
	}
	req.ResourceSource = strings.TrimSpace(req.ResourceSource)
	if req.ResourceSource != "hdhive" {
		req.ResourceSource = "" // 空 = TG 频道订阅（在所有已添加频道中搜索）
		req.Channel = ""
	} else {
		// 影巢订阅：必须按 TMDB 影片订阅
		if req.TMDBID <= 0 || (req.MediaType != "movie" && req.MediaType != "tv") {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "影巢订阅必须选择影片（电影或剧集）", Data: nil})
			return
		}
		req.Channel = ""
	}
	req.Keywords = strings.TrimSpace(req.Keywords)
	if err := models.CreateCloudSubscription(&req); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "创建失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已创建", Data: req})
}

// UpdateCloudSubscriptionAPI 更新订阅（PUT /cloud/subscriptions/:id，仅更新请求中出现的字段）
func UpdateCloudSubscriptionAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	body, _ := json.Marshal(raw)
	var req models.CloudSubscription
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	fields := map[string]bool{}
	for k := range raw {
		fields[k] = true
	}
	if err := models.UpdateCloudSubscription(id, &req, fields); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "更新失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已更新", Data: nil})
}

// DeleteCloudSubscriptionAPI 删除订阅（DELETE /cloud/subscriptions/:id）
func DeleteCloudSubscriptionAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	if err := models.DeleteCloudSubscription(id); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	_ = models.DeleteSubscriptionRecords(id)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已删除", Data: nil})
}

// CleanOldVersionsAPI 清理订阅的旧版本（共存模式下手动删除被洗版替换的文件）
// POST /cloud/subscriptions/clean-old {id}
func CleanOldVersionsAPI(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(req.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	recs := models.SupersededRecords(sub.ID)
	if len(recs) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "没有待清理的旧版本", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	deleted := 0
	var errs []string
	var removedIDs []uint
	for _, r := range recs {
		n, err := deleteOldFilesByTitle(ctx, sub.SourceType, r.TargetDir, r.Title)
		if err != nil {
			errs = append(errs, fmt.Sprintf("「%s」清理失败：%v", r.Title, err))
			continue
		}
		deleted += n
		removedIDs = append(removedIDs, r.ID)
	}
	if len(removedIDs) > 0 {
		if err := models.DeleteTransferRecords(removedIDs); err != nil {
			errs = append(errs, fmt.Sprintf("清理记录失败：%v", err))
		}
	}
	msg := fmt.Sprintf("已清理 %d 个旧版本文件，移除 %d 条旧记录", deleted, len(removedIDs))
	if len(errs) > 0 {
		msg += "；失败：" + strings.Join(errs, "；")
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
}

// ListSubscriptionRecordsAPI 订阅转存纪录（GET /cloud/subscriptions/:id/records?page=&page_size=）
func ListSubscriptionRecordsAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	page, _ := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(c.Query("page_size")))
	records, total, err := models.ListTransferRecords(id, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: gin.H{
		"records":   records,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}})
}

// PreviewChannelAPI 预览频道最近内容（POST /cloud/subscriptions/preview {channel, limit}）
func PreviewChannelAPI(c *gin.Context) {
	var req struct {
		Channel string `json:"channel"`
		Limit   int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少频道名", Data: nil})
		return
	}
	posts, err := PreviewChannel(channel, req.Limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "抓取失败：" + err.Error(), Data: nil})
		return
	}
	list := make([]gin.H, 0, len(posts))
	for _, p := range posts {
		links := make([]string, 0, len(p.Links))
		for _, l := range p.Links {
			links = append(links, fmt.Sprintf("%s（%s）", l.URL, parseSourceTypeName(l.Type)))
		}
		list = append(list, gin.H{
			"post_id": p.PostID,
			"text":    p.Text,
			"time":    p.Time.Format("2006-01-02 15:04"),
			"links":   links,
		})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "抓取成功", Data: list})
}

// subscriptionRunLocks 手动执行防重入锁（key: 订阅 ID，执行中再次点击直接提示）
var subscriptionRunLocks sync.Map

// RunSubscriptionAPI 手动立即执行一条订阅（POST /cloud/subscriptions/run {id}）
// 订阅执行耗时可能远超前端请求超时（逐资源查询/解锁/转存），故改为异步执行：
// 接口立即返回“已提交”，执行在后台完成，结果写入日志并更新订阅的最近执行时间（列表刷新可见）。
func RunSubscriptionAPI(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(req.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	if _, running := subscriptionRunLocks.LoadOrStore(req.ID, struct{}{}); running {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该订阅正在执行中，请稍后再试", Data: nil})
		return
	}
	hiveRefreshingCount.Add(1)
	runAuthBackgroundTask(func() {
		defer subscriptionRunLocks.Delete(req.ID)
		defer hiveRefreshingCount.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("订阅 #%d 手动执行 panic：%v", req.ID, r)
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
			helpers.AppLogger.Infof("手动执行订阅：%s", msg)
		} else {
			helpers.AppLogger.Errorf("手动执行订阅：%s", msg)
		}
	})
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已提交执行，稍后刷新列表可查看最近执行时间", Data: nil})
}

// GetHiveSettingsAPI 获取影巢设置（GET /cloud/hive/settings）
// 与 tgto123 一致：自动签到（主/子账号的开关/时间/模式）、订阅引擎轮询间隔、解锁积分上限。
func GetHiveSettingsAPI(c *gin.Context) {
	throttle := models.GetHiveTransferThrottle()
	checkinWinStart, checkinWinEnd := models.GetHiveCheckinWindow()
	subWinStart, subWinEnd := models.GetHiveSubCheckinWindow()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: gin.H{
		"poll_interval":         models.GetHivePollInterval(),
		"daily_checkin_enabled": models.GetHiveCheckinEnabled(),
		"daily_checkin_mode":    models.GetHiveCheckinMode(),
		"daily_checkin_hour":    models.GetHiveCheckinHour(),
		"sub_checkin_enabled":   models.GetHiveSubCheckinEnabled(),
		"sub_checkin_mode":      models.GetHiveSubCheckinMode(),
		"sub_checkin_hour":      models.GetHiveSubCheckinHour(),
		"max_points":            models.GetHiveMaxPoints(),
		// 执行强度与过滤（借鉴 mediavault）
		"only_official":         models.GetHiveOnlyOfficial(),
		"publisher_whitelist":   strings.Join(models.GetHivePublisherWhitelist(), ","),
		"exec_preset":           throttle.Preset,
		"max_transfers_per_run": models.GetHiveTransferThrottle().MaxTransfersPerRun,
		"transfer_min_interval": int(models.GetHiveTransferThrottle().MinInterval.Seconds()),
		"transfer_jitter":       int(models.GetHiveTransferThrottle().Jitter.Seconds()),
		"slug_max_attempts":     models.GetHiveSlugMaxAttempts(),
		// 影巢搜索与订阅引擎（对齐 mediavault resource_search）
		"hive_enabled":                models.GetHiveEnabled(),
		"timed_search_enabled":        models.GetHiveTimedSearchEnabled(),
		"search_transfer":             models.GetHiveSearchTransfer(),
		"auto_unlock":                 models.GetHiveAutoUnlock(),
		"transfer_use_subdir":         models.GetHiveUseSubdir(),
		"transfer_media":              models.GetHiveTransferMedia(),
		"transfer_subtitle":           models.GetHiveTransferSubtitle(),
		"transfer_non_media":          models.GetHiveTransferNonMedia(),
		"run_batch_size":              models.GetHiveRunBatchSize(),
		"tv_completion_grace_days":    models.GetHiveTVCompletionGraceDays(),
		"sync_library":                models.GetHiveSyncLibrary(),
		"sync_wait":                   models.GetHiveSyncWait(),
		"subscription_defaults_movie": models.GetHiveSubscriptionDefaults("movie"),
		"subscription_defaults_tv":    models.GetHiveSubscriptionDefaults("tv"),
		// pansou 盘搜（对齐 mediavault pansou 设置；password 只回传是否已设置）
		"pansou_enabled":      models.GetHivePansouEnabled(),
		"pansou_base_url":     models.GetHivePansouBaseURL(),
		"pansou_username":     models.GetHivePansouUsername(),
		"pansou_password_set": models.GetHivePansouPassword() != "",
		// S1 签到随机窗口 / S2 refresh 到期提醒 / U2 解锁限额
		"checkin_window_start":     checkinWinStart,
		"checkin_window_end":       checkinWinEnd,
		"sub_checkin_window_start": subWinStart,
		"sub_checkin_window_end":   subWinEnd,
		"refresh_remind_days":      models.GetHiveRefreshRemindDays(),
		"unlock_daily_limit":       models.GetHiveUnlockDailyLimit(),
	}})
}

// SetHiveSettingsAPI 保存影巢设置（POST /cloud/hive/settings）
// 整型字段用指针：未提交的字段保持原值，避免部分更新被默认值覆盖。
func SetHiveSettingsAPI(c *gin.Context) {
	var req struct {
		PollInterval        *int   `json:"poll_interval"`
		DailyCheckinEnabled *bool  `json:"daily_checkin_enabled"`
		DailyCheckinMode    string `json:"daily_checkin_mode"`
		DailyCheckinHour    *int   `json:"daily_checkin_hour"`
		SubCheckinEnabled   *bool  `json:"sub_checkin_enabled"`
		SubCheckinMode      string `json:"sub_checkin_mode"`
		SubCheckinHour      *int   `json:"sub_checkin_hour"`
		MaxPoints           *int   `json:"max_points"`
		// 执行强度与过滤（借鉴 mediavault）
		OnlyOfficial        *bool  `json:"only_official"`
		PublisherWhitelist  string `json:"publisher_whitelist"`
		ExecPreset          string `json:"exec_preset"`
		MaxTransfersPerRun  *int   `json:"max_transfers_per_run"`
		TransferMinInterval *int   `json:"transfer_min_interval"`
		TransferJitter      *int   `json:"transfer_jitter"`
		SlugMaxAttempts     *int   `json:"slug_max_attempts"`
		// 影巢搜索与订阅引擎（对齐 mediavault resource_search）
		HiveEnabled        *bool  `json:"hive_enabled"`
		TimedSearchEnabled *bool  `json:"timed_search_enabled"`
		SearchTransfer     *bool  `json:"search_transfer"`
		AutoUnlock         *bool  `json:"auto_unlock"`
		UseSubdir          *bool  `json:"transfer_use_subdir"`
		TransferMedia      *bool  `json:"transfer_media"`
		TransferSubtitle   *bool  `json:"transfer_subtitle"`
		TransferNonMedia   *bool  `json:"transfer_non_media"`
		RunBatchSize       *int   `json:"run_batch_size"`
		GraceDays          *int   `json:"tv_completion_grace_days"`
		SyncLibrary        *bool  `json:"sync_library"`
		SyncWait           *int   `json:"sync_wait"`
		DefaultsMovie      string `json:"subscription_defaults_movie"`
		DefaultsTV         string `json:"subscription_defaults_tv"`
		// pansou 盘搜（password 留空=保持原密码，不改动）
		PansouEnabled  *bool  `json:"pansou_enabled"`
		PansouBaseURL  string `json:"pansou_base_url"`
		PansouUsername string `json:"pansou_username"`
		PansouPassword string `json:"pansou_password"`
		// S1 签到随机窗口（空=恢复 daily_checkin_hour 小时策略）/ S2 refresh 到期提醒 / U2 解锁限额
		CheckinWindowStart    string `json:"checkin_window_start"`
		CheckinWindowEnd      string `json:"checkin_window_end"`
		SubCheckinWindowStart string `json:"sub_checkin_window_start"`
		SubCheckinWindowEnd   string `json:"sub_checkin_window_end"`
		RefreshRemindDays     *int   `json:"refresh_remind_days"`
		UnlockDailyLimit      *int   `json:"unlock_daily_limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.PollInterval != nil && *req.PollInterval > 0 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveInterval, strconv.Itoa(*req.PollInterval)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存轮询间隔失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.DailyCheckinEnabled != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveCheckinEnabled, strconv.FormatBool(*req.DailyCheckinEnabled)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存主账号签到设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.DailyCheckinMode == "daily" || req.DailyCheckinMode == "gamble" {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveCheckinMode, req.DailyCheckinMode); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存主账号签到模式失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.DailyCheckinHour != nil && *req.DailyCheckinHour >= 0 && *req.DailyCheckinHour <= 23 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveCheckinHour, strconv.Itoa(*req.DailyCheckinHour)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存主账号签到时间失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SubCheckinEnabled != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSubCheckinEnabled, strconv.FormatBool(*req.SubCheckinEnabled)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子账号签到设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SubCheckinMode == "daily" || req.SubCheckinMode == "gamble" {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSubCheckinMode, req.SubCheckinMode); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子账号签到模式失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SubCheckinHour != nil && *req.SubCheckinHour >= 0 && *req.SubCheckinHour <= 23 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSubCheckinHour, strconv.Itoa(*req.SubCheckinHour)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子账号签到时间失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.MaxPoints != nil && *req.MaxPoints >= 0 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveMaxPoints, strconv.Itoa(*req.MaxPoints)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存解锁积分上限失败：" + err.Error(), Data: nil})
			return
		}
	}
	// 执行强度与过滤（借鉴 mediavault）
	if req.OnlyOfficial != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveOnlyOfficial, strconv.FormatBool(*req.OnlyOfficial)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存官组过滤设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.PublisherWhitelist != "" || req.OnlyOfficial != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHivePublisherWhitelist, strings.TrimSpace(req.PublisherWhitelist)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存发布者白名单失败：" + err.Error(), Data: nil})
			return
		}
	}
	switch req.ExecPreset {
	case "conservative", "balanced", "aggressive", "custom":
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveExecPreset, req.ExecPreset); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存执行强度预设失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.MaxTransfersPerRun != nil && *req.MaxTransfersPerRun >= 1 && *req.MaxTransfersPerRun <= 50 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveMaxTransfersPerRun, strconv.Itoa(*req.MaxTransfersPerRun)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存单轮转存上限失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TransferMinInterval != nil && *req.TransferMinInterval >= 5 && *req.TransferMinInterval <= 300 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTransferMinInterval, strconv.Itoa(*req.TransferMinInterval)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存转存最小间隔失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TransferJitter != nil && *req.TransferJitter >= 0 && *req.TransferJitter <= 120 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTransferJitter, strconv.Itoa(*req.TransferJitter)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存转存抖动失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SlugMaxAttempts != nil && *req.SlugMaxAttempts >= 1 && *req.SlugMaxAttempts <= 10 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSlugMaxAttempts, strconv.Itoa(*req.SlugMaxAttempts)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存资源尝试上限失败：" + err.Error(), Data: nil})
			return
		}
	}
	// 影巢搜索与订阅引擎（对齐 mediavault resource_search）
	if req.HiveEnabled != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveEnabled, strconv.FormatBool(*req.HiveEnabled)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存影巢搜索开关失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TimedSearchEnabled != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTimedSearch, strconv.FormatBool(*req.TimedSearchEnabled)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存定时搜索开关失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SearchTransfer != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSearchTransfer, strconv.FormatBool(*req.SearchTransfer)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存自动转存开关失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.AutoUnlock != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveAutoUnlock, strconv.FormatBool(*req.AutoUnlock)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存自动解锁开关失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.UseSubdir != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveUseSubdir, strconv.FormatBool(*req.UseSubdir)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子目录开关失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TransferMedia != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTransferMedia, strconv.FormatBool(*req.TransferMedia)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存媒体文件转存设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TransferSubtitle != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTransferSubtitle, strconv.FormatBool(*req.TransferSubtitle)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存字幕文件转存设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.TransferNonMedia != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveTransferNonMedia, strconv.FormatBool(*req.TransferNonMedia)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存非媒体文件转存设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.RunBatchSize != nil && *req.RunBatchSize >= 0 && *req.RunBatchSize <= 200 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveRunBatchSize, strconv.Itoa(*req.RunBatchSize)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存每轮订阅数上限失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.GraceDays != nil && *req.GraceDays >= 1 && *req.GraceDays <= 365 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveGraceDays, strconv.Itoa(*req.GraceDays)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存完结宽限期失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SyncLibrary != nil {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSyncLibrary, strconv.FormatBool(*req.SyncLibrary)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存媒体库同步设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.SyncWait != nil && *req.SyncWait >= 0 && *req.SyncWait <= 120 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSyncWait, strconv.Itoa(*req.SyncWait)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存同步等待时长失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.DefaultsMovie != "" {
		if err := models.SaveHiveSubscriptionDefaults("movie", req.DefaultsMovie); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存电影默认订阅配置失败：" + err.Error(), Data: nil})
			return
		}
	}
	if req.DefaultsTV != "" {
		if err := models.SaveHiveSubscriptionDefaults("tv", req.DefaultsTV); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存剧集默认订阅配置失败：" + err.Error(), Data: nil})
			return
		}
	}
	// pansou 盘搜（password 留空=保留原密码）
	if err := models.SaveHivePansouSettings(req.PansouEnabled, req.PansouBaseURL, req.PansouUsername, req.PansouPassword); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存盘搜设置失败：" + err.Error(), Data: nil})
		return
	}
	// S1 签到随机窗口（空值=恢复 daily_checkin_hour 小时策略，由 getter 回落）
	if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveCheckinWindowStart, strings.TrimSpace(req.CheckinWindowStart)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存签到窗口失败：" + err.Error(), Data: nil})
		return
	}
	if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveCheckinWindowEnd, strings.TrimSpace(req.CheckinWindowEnd)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存签到窗口失败：" + err.Error(), Data: nil})
		return
	}
	if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSubCheckinWindowStart, strings.TrimSpace(req.SubCheckinWindowStart)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子账号签到窗口失败：" + err.Error(), Data: nil})
		return
	}
	if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveSubCheckinWindowEnd, strings.TrimSpace(req.SubCheckinWindowEnd)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存子账号签到窗口失败：" + err.Error(), Data: nil})
		return
	}
	// S2 refresh token 到期提醒天数（0=关闭）
	if req.RefreshRemindDays != nil && *req.RefreshRemindDays >= 0 && *req.RefreshRemindDays <= 30 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveRefreshRemindDays, strconv.Itoa(*req.RefreshRemindDays)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存到期提醒设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	// U2 全局每日自动解锁次数上限（0=不限）
	if req.UnlockDailyLimit != nil && *req.UnlockDailyLimit >= 0 && *req.UnlockDailyLimit <= 1000 {
		if err := models.SetCloudSetting("hdhive", models.CloudSettingKeyHiveUnlockDailyLimit, strconv.Itoa(*req.UnlockDailyLimit)); err != nil {
			c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存解锁限额设置失败：" + err.Error(), Data: nil})
			return
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "影巢设置已保存", Data: nil})
}

// SetCloudSubscriptionPausedAPI 订阅暂停/恢复（POST /cloud/subscriptions/pause {id, paused}）
// paused=true 置为 paused（跳过定时检索，配置保留）；false 恢复 subscribing（借鉴 mediavault 状态机）
func SetCloudSubscriptionPausedAPI(c *gin.Context) {
	var req struct {
		ID     int64 `json:"id"`
		Paused bool  `json:"paused"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID <= 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(uint(req.ID))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	if req.Paused {
		sub.Status = "paused"
	} else {
		sub.Status = "subscribing"
	}
	if err := models.SaveCloudSubscription(sub); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	msg := "订阅已恢复"
	if req.Paused {
		msg = "订阅已暂停（跳过定时检索）"
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
}

// ListChannelJobsPlaceholder 占位：订阅任务状态列表
func ListChannelJobsPlaceholder(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: []gin.H{}})
}

// TestPan123Account 测试 123 账号连通性
func TestPan123Account(c *gin.Context) {
	ctx := c.Request.Context()
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&account).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未配置 123 云盘账号", Data: nil})
		return
	}
	client := account.Get123Client()
	defer client.Close()
	info, err := client.GetUserInfo(ctx)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "测试失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("连接成功，账号：%s", info.Data.Nickname), Data: nil})
}
