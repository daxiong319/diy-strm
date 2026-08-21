package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// hiveWatchMinInterval 影巢订阅引擎最小轮询间隔（分钟）
const hiveWatchMinInterval = 5 * time.Minute

// StartHiveWatcher 启动影巢（HDHive）订阅引擎（后台 goroutine 常驻轮询）
func StartHiveWatcher(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("影巢订阅引擎 panic：%v", r)
			}
		}()
		time.Sleep(15 * time.Second) // 避开服务启动高峰
		runAllHiveSubscriptions()
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
				runAllHiveSubscriptions()
			}
		}
	}()
	helpers.AppLogger.Infof("影巢订阅引擎已启动，轮询间隔 %d 分钟（可在影巢设置中修改）", models.GetHivePollInterval())
}

// runAllHiveSubscriptions 执行全部启用中的影巢订阅
func runAllHiveSubscriptions() {
	subs, err := models.ListSubscriptionsByResourceSource("hdhive")
	if err != nil {
		helpers.AppLogger.Errorf("影巢订阅：读取订阅列表失败：%v", err)
		return
	}
	active := 0
	for i := range subs {
		if !subs[i].Enabled {
			continue
		}
		active++
		msg, ok := RunHiveSubscriptionOnce(&subs[i])
		if ok {
			helpers.AppLogger.Infof("影巢订阅：%s", msg)
		} else {
			helpers.AppLogger.Errorf("影巢订阅：%s", msg)
		}
	}
	if active == 0 {
		helpers.AppLogger.Infof("影巢订阅：本轮无启用中的订阅")
	}
}

// RunHiveSubscriptionOnce 对单条影巢订阅执行一轮：查资源 → 规格筛选 → 解锁 → 转存
// 返回结果摘要与是否成功。
// 资源查询走 OAuth 代理通道（hdhive-open.tgtodrive.top，与 tgto123 一致），
// 不再依赖 hdhive.com Open API 的 X-API-Key（该密钥可能被上游禁用）。
func RunHiveSubscriptionOnce(sub *models.CloudSubscription) (string, bool) {
	mainAcc, err := models.GetHiveMainAccount()
	if err != nil {
		sub.LastRunAt = time.Now()
		_ = models.SaveCloudSubscription(sub)
		return fmt.Sprintf("订阅 #%d（影巢）获取主账号失败：%v", sub.ID, err), false
	}
	if !mainAcc.Authorized {
		return fmt.Sprintf("订阅 #%d（影巢）主账号未授权，请先在影巢设置中完成 OAuth 授权", sub.ID), false
	}
	if sub.TMDBID <= 0 || (sub.MediaType != "movie" && sub.MediaType != "tv") {
		return fmt.Sprintf("订阅 #%d（影巢）缺少影片信息（TMDB ID/类型），跳过", sub.ID), false
	}
	client := hdhive.NewOAuthClient(mainAcc.InstallID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 查询资源列表（OAuth 代理通道）
	resourcesResp, err := client.GetResources(ctx, sub.MediaType, strconv.FormatInt(sub.TMDBID, 10))
	if err != nil {
		return fmt.Sprintf("订阅 #%d（影巢 %s %d）查询资源失败：%v", sub.ID, sub.MediaType, sub.TMDBID, err), false
	}
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
	// 过滤无效资源（失效链接），并按规格分降序取最优候选
	candidates := make([]hdhive.Resource, 0, len(resources))
	for _, r := range resources {
		if strings.EqualFold(strings.TrimSpace(r.ValidateStatus), "invalid") {
			continue
		}
		candidates = append(candidates, r)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return hiveResourceSpec(&candidates[i]).Score() > hiveResourceSpec(&candidates[j]).Score()
	})

	targetDir := strings.TrimSpace(sub.TargetDir)
	if targetDir == "" {
		targetDir = "/"
	}
	skipped := 0
	unsupported := 0
	var errs []string

	for i := range candidates {
		res := &candidates[i]
		spec := hiveResourceSpec(res)
		// 去重/洗版判定（与 TG 订阅一致）
		old := models.LatestSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season)
		if old != nil {
			oldSpec := recordToSpec(old)
			if oldSpec.Score() >= WashTargetScore(sub.WashTarget) {
				skipped++ // 已达标
				continue
			}
			if !spec.BetterThan(oldSpec) {
				skipped++ // 不比当前版本更优
				continue
			}
		} else if !sub.Wash && models.HasSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season) {
			skipped++ // 已收录
			continue
		}
		// 通过分享详情获取网盘类型，必须与订阅目标网盘一致
		shareResp, serr := client.GetShareDetail(ctx, res.Slug)
		if serr != nil {
			errs = append(errs, fmt.Sprintf("%s 详情获取失败：%v", res.Slug, serr))
			continue
		}
		if !shareResp.Success {
			errs = append(errs, fmt.Sprintf("%s 详情获取失败：%s", res.Slug, shareResp.Message))
			continue
		}
		var detail hdhive.ShareDetail
		if err := json.Unmarshal(shareResp.Data, &detail); err != nil {
			errs = append(errs, fmt.Sprintf("%s 详情解析失败", res.Slug))
			continue
		}
		panType := hivePanTypeToSourceType(detail.PanType)
		if panType == "" {
			unsupported++
			helpers.AppLogger.Debugf("影巢订阅 #%d：资源 %s 网盘类型 %q 暂不支持，跳过", sub.ID, res.Slug, detail.PanType)
			continue
		}
		if panType != sub.SourceType {
			skipped++ // 网盘类型与订阅目标不一致
			continue
		}
		// 解锁
		unlockResp, uerr := client.UnlockResource(ctx, res.Slug)
		if uerr != nil {
			errs = append(errs, fmt.Sprintf("%s 解锁失败：%v", res.Slug, uerr))
			continue
		}
		if !unlockResp.Success {
			errs = append(errs, fmt.Sprintf("%s 解锁失败：%s", res.Slug, unlockResp.Message))
			continue
		}
		var unlock hdhive.UnlockResult
		if err := json.Unmarshal(unlockResp.Data, &unlock); err != nil {
			errs = append(errs, fmt.Sprintf("%s 解锁结果解析失败", res.Slug))
			continue
		}
		linkURL := strings.TrimSpace(unlock.FullURL)
		if linkURL == "" {
			linkURL = strings.TrimSpace(unlock.URL)
		}
		if models.HasLinkRecord(linkURL) {
			skipped++ // 该链接已转存过
			continue
		}
		title, total, terr := saveShareByLink(ctx, linkURL, unlock.AccessCode, panType, targetDir)
		if terr != nil {
			errs = append(errs, fmt.Sprintf("%s 转存失败：%v", linkURL, terr))
			continue
		}
		recTitle := sub.TMDBTitle
		if recTitle == "" {
			recTitle = title
		}
		_ = models.CreateTransferRecord(&models.CloudTransferRecord{
			SourceType:     panType,
			SubscriptionID: sub.ID,
			MediaType:      sub.MediaType,
			TMDBID:         sub.TMDBID,
			Season:         sub.Season,
			Title:          recTitle,
			PostID:         res.Slug,
			LinkURL:        linkURL,
			TargetDir:      targetDir,
			Resolution:     spec.Resolution,
			Source:         spec.Source,
			Codec:          spec.Codec,
			Effect:         spec.Effect,
			SizeGB:         spec.SizeGB,
		})
		if old != nil {
			old.Status = "superseded"
			_ = models.SaveTransferRecord(old)
			if sub.ReplaceOld {
				if n, derr := deleteOldFilesByTitle(ctx, panType, targetDir, old.Title); derr != nil {
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
			sub.ID, sub.MediaType, sub.TMDBTitle, panType, shortHiveTitle(res.Title), spec.Score(), total, targetDir)
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
	if len(errs) > 0 {
		summary += "；失败：" + strings.Join(errs, "；")
		return summary, false
	}
	return summary, true
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