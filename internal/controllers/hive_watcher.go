package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// hiveWatchMinInterval 影巢订阅引擎最小轮询间隔（分钟）
const hiveWatchMinInterval = 5 * time.Minute

// 已完结 TV 订阅的 TMDB 复查参数（借鉴 mediavault _maybe_reactivate_completed_tv）
// 完结宽限期改为从影巢设置读取（tv_completion_grace_days，默认 7 天）
const hiveTVRecheckMinInterval = 24 * time.Hour // 复查最小间隔

// StartHiveWatcher 启动影巢（HDHive）订阅引擎（后台 goroutine 常驻轮询）
func StartHiveWatcher(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("影巢订阅引擎 panic：%v", r)
			}
		}()
		time.Sleep(15 * time.Second) // 避开服务启动高峰
	if models.GetHiveEnabled() && models.GetHiveTimedSearchEnabled() {
		runAllHiveSubscriptions()
	} else {
		helpers.AppLogger.Infof("影巢订阅引擎：定时搜索未启用（影巢搜索=%v，定时搜索=%v），仅保留手动触发", models.GetHiveEnabled(), models.GetHiveTimedSearchEnabled())
	}
	for {
		interval := time.Duration(models.GetHivePollInterval()) * time.Minute
		if interval < hiveWatchMinInterval {
			interval = hiveWatchMinInterval
		}
		select {
		case <-ctx.Done():
			helpers.AppLogger.Infof("影巢订阅引擎已停止")
			return
		case <-time.After(interval):
			if models.GetHiveEnabled() && models.GetHiveTimedSearchEnabled() {
				runAllHiveSubscriptions()
			}
		}
	}
}()
	helpers.AppLogger.Infof("影巢订阅引擎已启动，轮询间隔 %d 分钟（可在影巢设置中修改）", models.GetHivePollInterval())
}

// runAllHiveSubscriptions 执行全部启用中的影巢订阅（按批量上限 run_batch_size 截断，0=不限）
func runAllHiveSubscriptions() {
	subs, err := models.ListSubscriptionsByResourceSource("hdhive")
	if err != nil {
		helpers.AppLogger.Errorf("影巢订阅：读取订阅列表失败：%v", err)
		return
	}
	batch := models.GetHiveRunBatchSize()
	active := 0
	ran := 0
	for i := range subs {
		if batch > 0 && ran >= batch {
			helpers.AppLogger.Infof("影巢订阅：本轮已达到批量上限 %d，剩余订阅下轮处理", batch)
			break
		}
		sub := &subs[i]
		if !sub.Enabled {
			continue
		}
		if sub.Status == "paused" {
			continue // 已暂停：跳过定时检索（借鉴 mediavault paused 态）
		}
		active++
		ran++
		msg, ok := RunHiveSubscriptionOnce(sub)
		if ok {
			helpers.AppLogger.Infof("影巢订阅：%s", msg)
		} else {
			helpers.AppLogger.Errorf("影巢订阅：%s", msg)
		}
	}
	if active == 0 {
		helpers.AppLogger.Infof("影巢订阅：本轮无启用中的订阅")
	}
	reactivateFinishedTVSubscriptions()
}

// reactivateFinishedTVSubscriptions 已完结 TV 订阅的 TMDB 复查与自动复活：
// 完结超过宽限期（tv_completion_grace_days，默认 7 天）且距上次复查超过 24h 时刷新 TMDB 总集数快照，
// 总集数增长（有新季/新集）则自动复活订阅（借鉴 mediavault「TMDB total grew → 重新激活」）
func reactivateFinishedTVSubscriptions() {
	graceDays := models.GetHiveTVCompletionGraceDays()
	if graceDays <= 0 {
		graceDays = 7
	}
	grace := time.Duration(graceDays) * 24 * time.Hour
	subs, err := models.ListSubscriptionsByResourceSource("hdhive")
	if err != nil {
		return
	}
	now := time.Now()
	for i := range subs {
		sub := &subs[i]
		if sub.MediaType != "tv" || sub.FinishedAt == nil || sub.TMDBID <= 0 {
			continue
		}
		if now.Sub(*sub.FinishedAt) < grace {
			continue
		}
		if sub.LastRecheckAt != nil && now.Sub(*sub.LastRecheckAt) < hiveTVRecheckMinInterval {
			continue
		}
		oldTotal := sub.TotalEpisodes
		refreshSubscriptionTotalEpisodes(sub)
		sub.LastRecheckAt = &now
		_ = models.SaveCloudSubscription(sub)
		if oldTotal > 0 && sub.TotalEpisodes > oldTotal {
			sub.FinishedAt = nil
			sub.Enabled = true
			sub.Status = "subscribing"
			_ = models.SaveCloudSubscription(sub)
			helpers.AppLogger.Infof("影巢订阅 #%d（%s）：TMDB 总集数 %d → %d，已自动复活订阅", sub.ID, sub.TMDBTitle, oldTotal, sub.TotalEpisodes)
		}
	}
}

// RunHiveSubscriptionOnce 对单条影巢订阅执行一轮：查资源 → 规格筛选 → 解锁 → 转存
// 返回结果摘要与是否成功。
// 资源查询与详情/解锁调用均走四通道负载均衡（symedia/tgtodrive/nanshare/官方直连），
// 通道级故障（限流/授权失效/5xx）自动逐个降级尝试。
func RunHiveSubscriptionOnce(sub *models.CloudSubscription) (string, bool) {
	mainAcc, err := models.GetHiveMainAccount()
	if err != nil {
		sub.LastRunAt = time.Now()
		_ = models.SaveCloudSubscription(sub)
		return fmt.Sprintf("订阅 #%d（影巢）获取主账号失败：%v", sub.ID, err), false
	}
	if !mainAcc.Authorized && !models.HasAuthorizedHiveChannelAccount() {
		return fmt.Sprintf("订阅 #%d（影巢）主账号未授权，请先在影巢设置中完成 OAuth 授权（任一通道）", sub.ID), false
	}
	if sub.TMDBID <= 0 || (sub.MediaType != "movie" && sub.MediaType != "tv") {
		return fmt.Sprintf("订阅 #%d（影巢）缺少影片信息（TMDB ID/类型），跳过", sub.ID), false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 查询资源列表：四通道按负载均衡调度逐个尝试，通道级故障自动降级
	query, qerr := models.HiveQueryResourcesWithFailover(ctx, sub.MediaType, strconv.FormatInt(sub.TMDBID, 10))
	if qerr != nil {
		if !mainAcc.Authorized {
			return fmt.Sprintf("订阅 #%d（影巢）主账号未授权且无可用通道，请先在影巢设置中完成 OAuth 授权（%v）", sub.ID, qerr), false
		}
		return fmt.Sprintf("订阅 #%d（影巢 %s %d）查询资源失败：%v", sub.ID, sub.MediaType, sub.TMDBID, qerr), false
	}
	preferredChannel := query.Channel
	resourcesResp := query.Resp
	if !resourcesResp.Success {
		msg := resourcesResp.Message
		if msg == "" {
			msg = resourcesResp.Description
		}
		if msg == "" {
			msg = "请求失败"
		}
		return fmt.Sprintf("订阅 #%d（影巢 %s %d）查询资源失败：%s", sub.ID, sub.MediaType, sub.TMDBID, msg), false
	}
	var resources []hdhive.Resource
	if len(resourcesResp.Data) > 0 && string(resourcesResp.Data) != "null" {
		if err := json.Unmarshal(resourcesResp.Data, &resources); err != nil {
			return fmt.Sprintf("订阅 #%d（影巢 %s %d）解析资源列表失败：%v", sub.ID, sub.MediaType, sub.TMDBID, err), false
		}
	}
	// 过滤无效资源（失效链接）+ 订阅自定义规则（清晰度/特效字幕/包含/排除，对齐 mediavault）
	filteredInvalid := 0
	filteredOfficial := 0
	filteredPublisher := 0
	filteredAttempt := 0
	filteredSpec := 0
	// 借鉴 mediavault：官组过滤 + 发布者白名单 + 失败历史降权（attempt 轮转）
	officialOnly := models.GetHiveOnlyOfficial()
	whitePublishers := models.GetHivePublisherWhitelist()
	maxAttempts := models.GetHiveSlugMaxAttempts()
	candidates := make([]hdhive.Resource, 0, len(resources))
	for _, r := range resources {
		if strings.EqualFold(strings.TrimSpace(r.ValidateStatus), "invalid") {
			filteredInvalid++
			continue
		}
		if officialOnly && !r.IsOfficial {
			filteredOfficial++
			continue
		}
		if len(whitePublishers) > 0 {
			pub := ""
			if r.User != nil {
				pub = r.User.Nickname
			}
			if !hivePublisherAllowed(whitePublishers, pub) {
				filteredPublisher++
				continue
			}
		}
		if !hiveResourceMatchesFilters(&r, sub) {
			filteredSpec++
			continue
		}
		if a := models.GetHiveSlugAttempt(r.Slug); a != nil {
			if ok, reason := models.HiveSlugUsable(a, maxAttempts); !ok {
				filteredAttempt++
				helpers.AppLogger.Debugf("影巢订阅 #%d：资源 %s 跳过（%s）", sub.ID, r.Slug, reason)
				continue
			}
		}
		candidates = append(candidates, r)
	}
	// 按规格分降序取最优候选
	sort.Slice(candidates, func(i, j int) bool {
		return hiveResourceSpec(&candidates[i]).Score() > hiveResourceSpec(&candidates[j]).Score()
	})

	// 自动解锁 / 自动转存开关（默认均开启）。任一关闭 → 本轮只检索不转存
	searchTransfer := models.GetHiveSearchTransfer()
	autoUnlock := models.GetHiveAutoUnlock()
	if !searchTransfer || !autoUnlock {
		offReasons := []string{}
		if !searchTransfer {
			offReasons = append(offReasons, "自动转存已关闭")
		}
		if !autoUnlock {
			offReasons = append(offReasons, "自动解锁已关闭")
		}
		sub.LastRunAt = time.Now()
		_ = models.SaveCloudSubscription(sub)
		logMsg := fmt.Sprintf("搜索完成，找到 %d 个资源（候选 %d 个），%s", len(resources), len(candidates), strings.Join(offReasons, "，"))
		_ = models.CreateSubscriptionLog(&models.SubscriptionLog{
			SubscriptionID: sub.ID, Title: sub.TMDBTitle, Action: "search",
			Status: "success", Message: logMsg,
		})
		msg := fmt.Sprintf("订阅 #%d（影巢 %s %s）：%s", sub.ID, sub.MediaType, sub.TMDBTitle, logMsg)
		helpers.AppLogger.Infof("%s", msg)
		return msg, true
	}

	targetDir := strings.TrimSpace(sub.TargetDir)
	if targetDir == "" {
		targetDir = "/"
	}
	skipped := 0
	unsupported := 0
	transferred := 0
	var errs []string
	// 执行强度（借鉴 mediavault 三档预设 + 转存间隔抖动）
	throttle := models.GetHiveTransferThrottle()

	for i := range candidates {
		res := &candidates[i]
		spec := hiveResourceSpec(res)
		hiveMsgID := res.Slug
		// 单轮转存上限（借鉴 mediavault subscription_transfer_max_per_run）
		if transferred >= throttle.MaxTransfersPerRun {
			helpers.AppLogger.Infof("影巢订阅 #%d：本轮转存已达上限 %d，剩余候选下轮处理", sub.ID, throttle.MaxTransfersPerRun)
			break
		}

		// 从资源标题解析剧集（如 "S01E26"、"S01E22-23"），用于按集去重和 Episode 记录
		epKeys := ParseEpisodeKeys(res.Title, sub.Season)
		hasEpKeys := len(epKeys) > 0
		meta := hiveMonitorMeta(sub, epKeys)

		// 去重/洗版判定：有剧集信息时按集去重，否则整片去重
		var old *models.CloudTransferRecord
		if hasEpKeys {
			// 按集去重（与 TG 订阅一致）：逐集查最新记录，取规格分最高的作为洗版比较基准
			var oldScore int
			for _, ek := range epKeys {
				o := models.LatestEpisodeRecord(sub.ID, sub.TMDBID, sub.Season, ek)
				if o == nil {
					continue
				}
				if s := recordToSpec(o).Score(); old == nil || s > oldScore {
					old = o
					oldScore = s
				}
			}
		} else {
			// 无剧集信息：整片去重（原逻辑）
			old = models.LatestSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season)
		}
		if old != nil {
			oldSpec := recordToSpec(old)
			if oldSpec.Score() >= WashTargetScore(sub.WashTarget) {
				skipped++ // 已达标
				recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, "洗版跳过：现有版本已达标", meta)
				continue
			}
			if !spec.BetterThan(oldSpec) {
				skipped++ // 不比当前版本更优
				recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, "洗版跳过：新资源规格不优于现有版本", meta)
				continue
			}
		} else if !sub.Wash && hasEpKeys && models.HasEpisodeRecord(sub.ID, sub.TMDBID, sub.Season, epKeys) {
			skipped++ // 已收录
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, "去重跳过：该剧集已收录", meta)
			continue
		} else if !sub.Wash && !hasEpKeys && models.HasSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season) {
			skipped++ // 已收录
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, "去重跳过：该影片/剧集已收录", meta)
			continue
		}
		// 通过分享详情获取网盘类型，必须与订阅目标网盘一致（通道级故障自动降级重试）
		shareResp, _, serr := models.HiveCallWithFailover(ctx, preferredChannel, func(cl hdhive.ChannelClient) (*hdhive.OAuthAPIResponse, error) {
			return cl.GetShareDetail(ctx, res.Slug)
		})
		if serr != nil {
			models.RecordHiveSlugFailure(res.Slug, sub.TMDBID, serr.Error())
			errs = append(errs, fmt.Sprintf("%s 详情获取失败：%v", res.Slug, serr))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, serr, meta)
			continue
		}
		if !shareResp.Success {
			failMsg := shareResp.Message
			if failMsg == "" {
				failMsg = shareResp.Description
			}
			models.RecordHiveSlugFailure(res.Slug, sub.TMDBID, "详情获取失败："+failMsg)
			errs = append(errs, fmt.Sprintf("%s 详情获取失败：%s", res.Slug, failMsg))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, fmt.Errorf("%s", failMsg), meta)
			continue
		}
		var detail hdhive.ShareDetail
		if err := json.Unmarshal(shareResp.Data, &detail); err != nil {
			errs = append(errs, fmt.Sprintf("%s 详情解析失败", res.Slug))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, err, meta)
			continue
		}
		panType := hivePanTypeToSourceType(detail.PanType)
		if panType == "" {
			unsupported++
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, fmt.Sprintf("网盘类型 %q 暂不支持，跳过", detail.PanType), meta)
			helpers.AppLogger.Debugf("影巢订阅 #%d：资源 %s 网盘类型 %q 暂不支持，跳过", sub.ID, res.Slug, detail.PanType)
			continue
		}
		if panType != sub.SourceType {
			skipped++ // 网盘类型与订阅目标不一致
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, "网盘类型与订阅目标不一致，跳过", meta)
			continue
		}
		// 解锁积分上限（0=不限，对应 tgto123 的 HDHIVE_MAX_POINTS）
		if maxPts := models.GetHiveMaxPoints(); maxPts > 0 && res.UnlockPoints > maxPts {
			skipped++ // 解锁积分超过上限
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, fmt.Sprintf("解锁积分 %d 超过上限 %d，跳过", res.UnlockPoints, maxPts), meta)
			continue
		}
		// 解锁（先取解锁节流许可，再走通道降级调用）
		if uerr := hdhive.AcquireUnlock(ctx); uerr != nil {
			errs = append(errs, fmt.Sprintf("解锁节流等待失败：%v", uerr))
			continue
		}
		unlockResp, _, uerr := models.HiveCallWithFailover(ctx, preferredChannel, func(cl hdhive.ChannelClient) (*hdhive.OAuthAPIResponse, error) {
			return cl.UnlockResource(ctx, res.Slug)
		})
		if uerr != nil {
			models.RecordHiveSlugFailure(res.Slug, sub.TMDBID, "解锁失败："+uerr.Error())
			errs = append(errs, fmt.Sprintf("%s 解锁失败：%v", res.Slug, uerr))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, uerr, meta)
			continue
		}
		if !unlockResp.Success {
			failMsg := unlockResp.Message
			// 积分不足（借鉴 NanShare 的 INSUFFICIENT_POINTS 处理）：计为跳过而非失败，
			// 并带上所需积分，方便调整积分上限或签到补分后重试
			if code := strings.ToUpper(strings.TrimSpace(unlockResp.Code)); code == "INSUFFICIENT_POINTS" {
				required := 0
				var d struct {
					RequiredPoints *int `json:"required_points"`
				}
				if len(unlockResp.Data) > 0 && json.Unmarshal(unlockResp.Data, &d) == nil && d.RequiredPoints != nil {
					required = *d.RequiredPoints
				}
				if required <= 0 {
					required = res.UnlockPoints
				}
				skipReason := fmt.Sprintf("积分不足：需要 %d 积分，已跳过", required)
				skipped++
				recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, skipReason, meta)
				continue
			}
			if failMsg == "" {
				failMsg = unlockResp.Description
			}
			models.RecordHiveSlugFailure(res.Slug, sub.TMDBID, "解锁失败："+failMsg)
			errs = append(errs, fmt.Sprintf("%s 解锁失败：%s", res.Slug, failMsg))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, fmt.Errorf("%s", failMsg), meta)
			continue
		}
		var unlock hdhive.UnlockResult
		if err := json.Unmarshal(unlockResp.Data, &unlock); err != nil {
			errs = append(errs, fmt.Sprintf("%s 解锁结果解析失败", res.Slug))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", "", targetDir, sub.ID, err, meta)
			continue
		}
		linkURL := strings.TrimSpace(unlock.FullURL)
		if linkURL == "" {
			linkURL = strings.TrimSpace(unlock.URL)
		}
		if models.HasLinkRecord(linkURL) {
			skipped++ // 该链接已转存过
			recordMonitorSkipped("hive", sub.SourceType, "", hiveMsgID, "", linkURL, targetDir, sub.ID, "去重跳过：该分享链接已转存过", meta)
			continue
		}
		// 转存子目录（transfer_use_subdir）：以「标题 (年份)」建子目录后转存
		transferDir := targetDir
		if models.GetHiveUseSubdir() {
			subName := strings.TrimSpace(sub.SearchKeyword)
			if subName == "" {
				subName = sub.TMDBTitle
			}
			if subName == "" {
				subName = shortHiveTitle(res.Title)
			}
			if sub.Year > 0 {
				subName = fmt.Sprintf("%s (%d)", subName, sub.Year)
			}
			d, derr := hiveEnsureSubdir(ctx, panType, targetDir, subName)
			if derr != nil {
				errs = append(errs, fmt.Sprintf("创建转存子目录失败：%v", derr))
				continue
			}
			transferDir = d
		}
		// 转存节流（借鉴 mediavault：转存最小间隔 + 随机抖动，避免固定频率触发风控）
		if terr := awaitHiveTransferSlot(ctx, throttle); terr != nil {
			helpers.AppLogger.Warnf("影巢订阅 #%d：转存节流中断：%v", sub.ID, terr)
			break
		}
		title, total, terr := saveShareByLink(ctx, linkURL, unlock.AccessCode, panType, transferDir)
		if terr != nil {
			models.RecordHiveSlugFailure(res.Slug, sub.TMDBID, "转存失败："+terr.Error())
			errs = append(errs, fmt.Sprintf("%s 转存失败：%v", linkURL, terr))
			recordMonitorFailed("hive", sub.SourceType, "", hiveMsgID, "", linkURL, transferDir, sub.ID, terr, meta)
			failTitle := sub.TMDBTitle
			if failTitle == "" {
				failTitle = shortHiveTitle(res.Title)
			}
			_ = models.CreateSubscriptionLog(&models.SubscriptionLog{
				SubscriptionID: sub.ID, Title: failTitle, Action: "transfer",
				Status: "failed", Message: "转存失败：" + terr.Error(), ShareLink: linkURL,
			})
			sendTransferFailedNotification(sub.SourceType, failTitle, transferDir, terr.Error())
			continue
		}
		transferred++
		models.ClearHiveSlugAttempt(res.Slug) // 成功后清除失败历史
		recTitle := sub.TMDBTitle
		if recTitle == "" {
			recTitle = title
		}
		if old != nil {
			recordMonitorWash("hive", panType, "", hiveMsgID, "", linkURL, title, transferDir, total, sub.ID, sub.WashTarget, meta)
		} else {
			recordMonitorSuccess("hive", panType, "", hiveMsgID, "", linkURL, title, transferDir, total, sub.ID, meta)
		}
		_ = models.CreateTransferRecord(&models.CloudTransferRecord{
			SourceType:     panType,
			SubscriptionID: sub.ID,
			MediaType:      sub.MediaType,
			TMDBID:         sub.TMDBID,
			Season:         sub.Season,
			Episode:        JoinEpisodeKeys(epKeys),
			Title:          recTitle,
			PostID:         res.Slug,
			LinkURL:        linkURL,
			TargetDir:      transferDir,
			Resolution:     spec.Resolution,
			Source:         spec.Source,
			Codec:          spec.Codec,
			Effect:         spec.Effect,
			SizeGB:         spec.SizeGB,
		})
		_ = models.CreateSubscriptionLog(&models.SubscriptionLog{
			SubscriptionID: sub.ID, Title: recTitle, Action: "transfer",
			Status: "success", Message: fmt.Sprintf("已转存 %d 个文件到 %s", total, transferDir),
			ShareLink: linkURL, FileCount: total,
		})
		extra := ""
		if hasEpKeys {
			extra = "剧集：" + JoinEpisodeKeys(epKeys)
		}
		sendTransferSuccessNotification(sub.SourceType, recTitle, transferDir, total, extra)
		if old != nil {
			old.Status = "superseded"
			_ = models.SaveTransferRecord(old)
			if sub.ReplaceOld {
				if n, derr := deleteOldFilesByTitle(ctx, panType, transferDir, old.Title); derr != nil {
					errs = append(errs, fmt.Sprintf("旧版本清理失败：%v", derr))
				} else if n > 0 {
					helpers.AppLogger.Infof("影巢订阅 #%d：洗版成功，已删除 %d 个旧版本文件（%s）", sub.ID, n, old.Title)
				}
			}
		}
		sub.LastPostID = res.Slug
		sub.LastRunAt = time.Now()
		_ = models.SaveCloudSubscription(sub)
		summary := fmt.Sprintf("订阅 #%d（影巢 %s %s → %s）命中资源「%s」规格分 %d，已解锁并转存 %d 项到 %s",
			sub.ID, sub.MediaType, sub.TMDBTitle, panType, shortHiveTitle(res.Title), spec.Score(), total, transferDir)
		_ = models.CreateSubscriptionLog(&models.SubscriptionLog{
			SubscriptionID: sub.ID, Title: sub.TMDBTitle, Action: "search",
			Status: "success", Message: fmt.Sprintf("搜索完成，找到 %d 个资源（候选 %d 个），已转存 %d 项", len(resources), len(candidates), transferred),
		})
		if unsupported > 0 {
			summary += fmt.Sprintf("；%d 个资源网盘类型不支持", unsupported)
		}
		if len(errs) > 0 {
			summary += "；失败：" + strings.Join(errs, "；")
			return summary, false
		}
		return summary, true
	}

	sub.LastRunAt = time.Now()
	_ = models.SaveCloudSubscription(sub)
	summary := fmt.Sprintf("订阅 #%d（影巢 %s %s → %s）：候选资源 %d 个，无新收录（去重/不达标跳过 %d 个，网盘不支持 %d 个）",
		sub.ID, sub.MediaType, sub.TMDBTitle, sub.SourceType, len(candidates), skipped, unsupported)
	if filteredInvalid > 0 || filteredOfficial > 0 || filteredPublisher > 0 || filteredAttempt > 0 || filteredSpec > 0 {
		summary += fmt.Sprintf("；候选过滤：失效 %d / 非官组 %d / 非白名单发布者 %d / 订阅规则 %d / 失败历史 %d",
			filteredInvalid, filteredOfficial, filteredPublisher, filteredSpec, filteredAttempt)
	}
	if transferred > 0 {
		summary += fmt.Sprintf("；本轮已转存 %d 项", transferred)
	}
	searchMsg := fmt.Sprintf("搜索完成，找到 %d 个资源（候选 %d 个），本轮无新收录", len(resources), len(candidates))
	_ = models.CreateSubscriptionLog(&models.SubscriptionLog{
		SubscriptionID: sub.ID, Title: sub.TMDBTitle, Action: "search",
		Status: "success", Message: searchMsg,
	})
	summary += "；" + searchMsg
	if len(errs) > 0 {
		summary += "；失败：" + strings.Join(errs, "；")
		return summary, false
	}
	return summary, true
}

// ---------------------------------------------------------------------------
// 转存节流（借鉴 mediavault subscription_transfer_min_interval + jitter）：
// 跨订阅/跨轮共享的转存时间闸，两次转存之间至少间隔 MinInterval + rand(0, Jitter)
// ---------------------------------------------------------------------------

var hiveTransferGateMu sync.Mutex
var hiveLastTransferAt time.Time

func awaitHiveTransferSlot(ctx context.Context, th models.HiveTransferThrottle) error {
	hiveTransferGateMu.Lock()
	defer hiveTransferGateMu.Unlock()
	if !hiveLastTransferAt.IsZero() {
		jitter := time.Duration(0)
		if th.Jitter > 0 {
			jitter = time.Duration(rand.Int63n(int64(th.Jitter)))
		}
		wait := th.MinInterval + jitter - time.Since(hiveLastTransferAt)
		if wait > 0 {
			helpers.AppLogger.Infof("影巢订阅：转存节流，等待 %s 后继续（预设 %s）", wait.Truncate(time.Second), th.Preset)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	hiveLastTransferAt = time.Now()
	return nil
}

// hivePublisherAllowed 发布者昵称是否在白名单（忽略大小写）
func hivePublisherAllowed(whitelist []string, publisher string) bool {
	publisher = strings.ToLower(strings.TrimSpace(publisher))
	if publisher == "" {
		return false
	}
	for _, w := range whitelist {
		if strings.ToLower(strings.TrimSpace(w)) == publisher {
			return true
		}
	}
	return false
}

// hiveResourceSpec 将影巢资源映射为媒体规格（复用文本规格解析）
func hiveResourceSpec(res *hdhive.Resource) MediaSpec {
	var b strings.Builder
	b.WriteString(res.Title)
	for _, v := range res.VideoResolution {
		b.WriteString(" ")
		b.WriteString(v)
	}
	for _, v := range res.Source {
		b.WriteString(" ")
		b.WriteString(v)
	}
	b.WriteString(" ")
	b.WriteString(res.ShareSize)
	b.WriteString(" ")
	b.WriteString(res.Remark)
	return ParseMediaSpec(b.String())
}

// hivePanTypeToSourceType 影巢网盘类型 → diy-strm 网盘类型（不支持返回空串）
func hivePanTypeToSourceType(panType string) string {
	switch strings.ToLower(strings.TrimSpace(panType)) {
	case "123", "123pan", "123云盘", "123pan.com":
		return string(models.SourceType123)
	case "guangyapan", "光鸭", "光鸭云盘":
		return string(models.SourceTypeGuangYaPan)
	case "139", "pan139", "139云盘", "中国移动云盘", "移动云盘", "cloud.189.cn":
		return string(models.SourceTypePan139)
	default:
		return ""
	}
}

// shortHiveTitle 截断资源标题用于日志
func shortHiveTitle(title string) string {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 60 {
		r := []rune(title)
		return string(r[:60]) + "..."
	}
	return title
}

// hiveMonitorMeta 构造影巢订阅监控历史的影片关联元信息（tmdb_id/类型/季集/影片名）
func hiveMonitorMeta(sub *models.CloudSubscription, epKeys []string) MonitorMediaMeta {
	return MonitorMediaMeta{
		TMDBID:    sub.TMDBID,
		MediaType: sub.MediaType,
		Season:    strconv.Itoa(sub.Season),
		Episode:   JoinEpisodeKeys(epKeys),
		Title:     sub.TMDBTitle,
	}
}

// hiveResourceMatchesFilters 订阅自定义筛选（对齐 mediavault 订阅规则字段）：
// 清晰度（video_resolution 精确匹配，忽略大小写）/ 特效字幕（标题+字幕类型+语言+备注包含）/
// 包含正则 / 排除正则（对资源标题生效）。字段为空表示不限制。
func hiveResourceMatchesFilters(res *hdhive.Resource, sub *models.CloudSubscription) bool {
	if resolution := strings.ToLower(strings.TrimSpace(sub.Resolution)); resolution != "" {
		ok := false
		for _, v := range res.VideoResolution {
			if strings.ToLower(strings.TrimSpace(v)) == resolution {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if effect := strings.ToLower(strings.TrimSpace(sub.Effect)); effect != "" {
		hay := strings.ToLower(res.Title + " " +
			strings.Join(res.SubtitleType, " ") + " " +
			strings.Join(res.SubtitleLanguage, " ") + " " + res.Remark)
		if !strings.Contains(hay, effect) {
			return false
		}
	}
	if inc := strings.TrimSpace(sub.IncludeRegex); inc != "" {
		if re, err := regexp.Compile("(?i)" + inc); err == nil && !re.MatchString(res.Title) {
			return false
		}
	}
	if exc := strings.TrimSpace(sub.ExcludeRegex); exc != "" {
		if re, err := regexp.Compile("(?i)" + exc); err == nil && re.MatchString(res.Title) {
			return false
		}
	}
	return true
}

// hiveEnsureSubdir 在目标目录下创建（或确认已存在）「标题 (年份)」子目录，返回完整子目录路径。
// 与 saveShareByLink 的解析方式一致：先解析父目录 ID，再调用对应网盘的建目录接口。
func hiveEnsureSubdir(ctx context.Context, panType, parentDir, subName string) (string, error) {
	full := strings.TrimSuffix(parentDir, "/") + "/" + subName
	parent := strings.TrimSpace(parentDir)
	if parent == "" {
		parent = "/"
	}
	var acc models.Account
	switch panType {
	case string(models.SourceType123):
		if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&acc).Error; err != nil {
			return "", fmt.Errorf("未配置 123 云盘账号")
		}
		cl := acc.Get123Client()
		defer cl.Close()
		parentID, err := cl.FindDirByPath(ctx, parent)
		if err != nil {
			return "", fmt.Errorf("目标目录不存在 %q（%v）", parent, err)
		}
		if _, err := cl.CreateDir(ctx, parentID, subName); err != nil {
			return "", fmt.Errorf("创建子目录 %q 失败：%v", full, err)
		}
	case string(models.SourceTypeGuangYaPan):
		if err := db.Db.Where("source_type = ?", models.SourceTypeGuangYaPan).Order("id asc").First(&acc).Error; err != nil {
			return "", fmt.Errorf("未配置光鸭云盘账号")
		}
		cl := acc.GetGuangYaPanClient()
		parentID := ""
		if parent != "/" {
			pid, err := cl.GetPathIdByPath(ctx, parent)
			if err != nil {
				return "", fmt.Errorf("目标目录不存在 %q（%v）", parent, err)
			}
			parentID = pid
		}
		if _, err := cl.CreateDir(ctx, parentID, subName); err != nil {
			return "", fmt.Errorf("创建子目录 %q 失败：%v", full, err)
		}
	case string(models.SourceTypePan139):
		if err := db.Db.Where("source_type = ?", models.SourceTypePan139).Order("id asc").First(&acc).Error; err != nil {
			return "", fmt.Errorf("未配置中国移动云盘账号")
		}
		cl := acc.GetPan139Client()
		parentID, err := cl.GetPathIdByPath(ctx, parent)
		if err != nil {
			return "", fmt.Errorf("目标目录不存在 %q（%v）", parent, err)
		}
		if _, err := cl.CreateDir(ctx, parentID, subName); err != nil {
			return "", fmt.Errorf("创建子目录 %q 失败：%v", full, err)
		}
	default:
		return "", fmt.Errorf("不支持的网盘类型：%s", panType)
	}
	return full, nil
}
