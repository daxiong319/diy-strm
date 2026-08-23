package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/tgchannel"
)

// channelWatchInterval 频道订阅引擎轮询间隔
const channelWatchInterval = 5 * time.Minute

// StartChannelWatcher 启动 TG 频道订阅引擎（后台 goroutine 常驻轮询）
func StartChannelWatcher(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("TG 频道订阅引擎 panic：%v", r)
			}
		}()
		time.Sleep(10 * time.Second) // 避开服务启动高峰
		runAllSubscriptions()
		ticker := time.NewTicker(channelWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				helpers.AppLogger.Infof("TG 频道订阅引擎已停止")
				return
			case <-ticker.C:
				runAllSubscriptions()
			}
		}
	}()
	helpers.AppLogger.Infof("TG 频道订阅引擎已启动，轮询间隔 %v", channelWatchInterval)
}

// runAllSubscriptions 执行全部启用中的订阅
func runAllSubscriptions() {
	subs, err := models.ListCloudSubscriptions("")
	if err != nil {
		helpers.AppLogger.Errorf("TG 频道订阅：读取订阅列表失败：%v", err)
		return
	}
	active := 0
	for i := range subs {
		if !subs[i].Enabled {
			continue
		}
		active++
		msg, ok := RunSubscriptionOnce(&subs[i])
		if ok {
			helpers.AppLogger.Infof("TG 频道订阅：%s", msg)
		} else {
			helpers.AppLogger.Errorf("TG 频道订阅：%s", msg)
		}
	}
	if active == 0 {
		helpers.AppLogger.Infof("TG 频道订阅：本轮无启用中的订阅")
	}
}

// RunSubscriptionOnce 对单条订阅执行一轮：遍历该网盘全部启用频道 → 增量 → 关键词匹配 → 转存
// 返回结果摘要与是否成功（转存失败也计入成功推进游标，避免死循环）
func RunSubscriptionOnce(sub *models.CloudSubscription) (string, bool) {
	refreshSubscriptionTotalEpisodes(sub)
	channels, err := models.ListEnabledCloudChannels(sub.SourceType)
	if err != nil {
		return fmt.Sprintf("订阅 #%d（%s）读取频道列表失败：%v", sub.ID, sub.SourceType, err), false
	}
	if len(channels) == 0 {
		return fmt.Sprintf("订阅 #%d（%s）没有启用中的资源频道，请先「订阅频道」添加", sub.ID, sub.SourceType), true
	}
	allOK := true
	var parts []string
	for i := range channels {
		msg, ok := runChannelSubscriptionOnce(sub, &channels[i])
		parts = append(parts, msg)
		if !ok {
			allOK = false
		}
	}
	// 游标已推进到频道表；订阅记录运行时间
	now := time.Now()
	sub.LastRunAt = now
	if sub.LastPostID == "" {
		// 兼容迁移前旧订阅：以本次频道最大游标初始化
		for i := range channels {
			if postIDGreater(channels[i].LastPostID, sub.LastPostID) {
				sub.LastPostID = channels[i].LastPostID
			}
		}
	}
	_ = models.SaveCloudSubscription(sub)

	// 自动完结：影片级订阅收录完毕后自动停用，避免重复转存（开关 AutoFinish 关闭时不自动停用）
	finished := false
	if sub.AutoFinish {
		if sub.Wash && sub.MediaType != "" {
			// 洗版模式：所有当前生效记录均达到洗版目标才完结（无目标=永不自动完结）
			finished = washTargetMet(sub)
		} else if sub.MediaType == "movie" {
			finished = models.HasSubscriptionRecord(sub.ID, sub.TMDBID, 0)
		} else if sub.MediaType == "tv" {
			// 优先按 TMDB 总集数判定：全部集已收录且洗版达标才完结
			if sub.TotalEpisodes > 0 {
				finished = int(models.CountDistinctEpisodes(sub.ID, sub.TMDBID, sub.Season)) >= sub.TotalEpisodes
			} else if sub.Season > 0 {
				finished = models.HasSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season)
			} else if sub.TotalSeasons > 0 {
				finished = int(models.CountSubscriptionRecords(sub.ID)) >= sub.TotalSeasons
			}
		}
	}
	if finished {
		now := time.Now()
		sub.Enabled = false
		sub.FinishedAt = &now
		sub.LastRunAt = now
		_ = models.SaveCloudSubscription(sub)
	}

	summary := fmt.Sprintf("订阅 #%d（%s）：%s", sub.ID, sub.SourceType, strings.Join(parts, "；"))
	if finished {
		summary += "；已自动完结（影片已收录完毕，订阅已停用）"
	}
	if !allOK {
		return summary, false
	}
	return summary, true
}

// runChannelSubscriptionOnce 单订阅 × 单频道执行一轮抓取与转存
func runChannelSubscriptionOnce(sub *models.CloudSubscription, ch *models.CloudChannel) (string, bool) {
	channel := ch.ChannelName()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	posts, err := tgchannel.ParseChannelPage(ctx, channel)
	if err != nil {
		return fmt.Sprintf("频道 %s 抓取失败：%v", channel, err), false
	}
	if len(posts) == 0 {
		ch.LastRunAt = time.Now()
		_ = models.SaveCloudChannel(ch)
		return fmt.Sprintf("频道 %s 无内容", channel), true
	}

	lastID := strings.TrimSpace(ch.LastPostID)
	kws := sub.KeywordList()
	targetDir := strings.TrimSpace(sub.TargetDir)
	if targetDir == "" {
		targetDir = "/"
	}

	newMaxID := lastID
	hits := 0
	transferred := 0
	linkFound := 0
	skipped := 0
	var errs []string
	var failedIDs []string
	msgURLFor := func(postID string) string { return buildTGMessageURL(channel, postID) }

	// posts 新到旧；游标推进到最新帖
	for _, p := range posts {
		if lastID != "" && !postIDGreater(p.PostID, lastID) {
			break // 已处理过，更旧的跳过
		}
		if newMaxID == "" || postIDGreater(p.PostID, newMaxID) {
			newMaxID = p.PostID
		}
		if !tgchannel.MatchKeywords(p.Text, kws) {
			continue
		}
		if len(p.Links) == 0 {
			continue
		}
		hits++
		for _, link := range p.Links {
			if link.Type != sub.SourceType {
				continue
			}
			linkFound++
			// 洗版模式（影片级订阅 + 洗版开关）：同片更高规格资源自动替换转存
			if sub.MediaType != "" && sub.Wash && sub.TMDBID > 0 {
				var old *models.CloudTransferRecord
				newSpec := ParseMediaSpec(p.Text)
				epKeys := ParseEpisodeKeys(p.Text, sub.Season)
				if len(epKeys) > 0 {
					// 按集判断：任一集缺失或可升级即转存；全部集已达洗版目标则跳过
					worth := false
					var upgrade []*models.CloudTransferRecord
					for _, ek := range epKeys {
						o := models.LatestEpisodeRecord(sub.ID, sub.TMDBID, sub.Season, ek)
						if o == nil {
							worth = true
							continue
						}
						oldSpec := recordToSpec(o)
						if oldSpec.Score() >= WashTargetScore(sub.WashTarget) {
							continue // 该集已达标，不再升级
						}
						if newSpec.BetterThan(oldSpec) {
							worth = true
							upgrade = append(upgrade, o)
						}
					}
					if !worth {
						skipped++ // 已达标或新资源不优于旧版本
						recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID,
							"洗版跳过：全部集已达标或新资源规格不优于现有版本")
						continue
					}
					if len(upgrade) > 0 {
						old = upgrade[0]
					}
				} else {
					old = models.LatestSubscriptionRecord(sub.ID, sub.TMDBID, sub.Season)
					if old != nil {
						oldSpec := recordToSpec(old)
						if oldSpec.Score() >= WashTargetScore(sub.WashTarget) {
							skipped++
							recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, "洗版跳过：现有版本已达标")
							continue
						}
						if !newSpec.BetterThan(oldSpec) {
							skipped++
							recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, "洗版跳过：新资源规格不优于现有版本")
							continue
						}
					}
				}
				title, total, err := saveShareByLink(ctx, link.URL, link.Pwd, sub.SourceType, targetDir)
				if err != nil {
					errs = append(errs, fmt.Sprintf("帖%s(%s)转存失败：%v", p.PostID, link.URL, err))
					failedIDs = append(failedIDs, p.PostID)
					recordMonitorFailed("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, err)
					sendTransferFailedNotification(sub.SourceType, transferNotifTitle(sub, p.Text), targetDir, err.Error())
					continue
				}
				transferred++
				recTitle := sub.TMDBTitle
				if recTitle == "" {
					recTitle = title
				}
				recordMonitorWash("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, title, targetDir, total, sub.ID, sub.WashTarget)
				_ = models.CreateTransferRecord(&models.CloudTransferRecord{
					SourceType:     sub.SourceType,
					SubscriptionID: sub.ID,
					MediaType:      sub.MediaType,
					TMDBID:         sub.TMDBID,
					Season:         sub.Season,
					Title:          recTitle,
					PostID:         p.PostID,
					LinkURL:        link.URL,
					TargetDir:      targetDir,
					Episode:        JoinEpisodeKeys(epKeys),
					Resolution:     newSpec.Resolution,
					Source:         newSpec.Source,
					Codec:          newSpec.Codec,
					Effect:         newSpec.Effect,
					SizeGB:         newSpec.SizeGB,
				})
				extra := "洗版更新"
				if len(epKeys) > 0 {
					extra += "；剧集：" + JoinEpisodeKeys(epKeys)
				}
				sendTransferSuccessNotification(sub.SourceType, recTitle, targetDir, total, extra)
				if old != nil {
					old.Status = "superseded"
					_ = models.SaveTransferRecord(old)
					if sub.ReplaceOld {
						if n, derr := deleteOldFilesByTitle(ctx, sub.SourceType, targetDir, old.Title); derr != nil {
							errs = append(errs, fmt.Sprintf("帖%s旧版本清理失败：%v", p.PostID, derr))
						} else if n > 0 {
							helpers.AppLogger.Infof("TG 频道订阅 #%d：洗版成功，已删除 %d 个旧版本文件（%s）", sub.ID, n, old.Title)
						}
					}
				}
				helpers.AppLogger.Infof("TG 频道订阅 #%v：洗版命中帖 %s，已转存更高规格「%s」共 %d 项到 %s（源 %s，目标 %s，规格分 %d）", sub.ID, p.PostID, title, total, targetDir, sub.SourceType, sub.WashTarget, newSpec.Score())
				continue
			}
			// 去重：影片级订阅按（订阅,片,+季/集）判定已收录；通用订阅按分享链接 URL 判定
			if sub.MediaType != "" {
				epKeys := ParseEpisodeKeys(p.Text, sub.Season)
				if models.HasEpisodeRecord(sub.ID, sub.TMDBID, sub.Season, epKeys) {
					skipped++
					recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, "去重跳过：该影片/剧集已收录")
					continue
				}
				title, total, err := saveShareByLink(ctx, link.URL, link.Pwd, sub.SourceType, targetDir)
				if err != nil {
					errs = append(errs, fmt.Sprintf("帖%s(%s)转存失败：%v", p.PostID, link.URL, err))
					failedIDs = append(failedIDs, p.PostID)
					recordMonitorFailed("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, err)
					sendTransferFailedNotification(sub.SourceType, transferNotifTitle(sub, p.Text), targetDir, err.Error())
					continue
				}
				transferred++
				recTitle := sub.TMDBTitle
				if recTitle == "" {
					recTitle = title
				}
				recordMonitorSuccess("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, title, targetDir, total, sub.ID)
				_ = models.CreateTransferRecord(&models.CloudTransferRecord{
					SourceType:     sub.SourceType,
					SubscriptionID: sub.ID,
					MediaType:      sub.MediaType,
					TMDBID:         sub.TMDBID,
					Season:         sub.Season,
					Title:          recTitle,
					PostID:         p.PostID,
					LinkURL:        link.URL,
					TargetDir:      targetDir,
					Episode:        JoinEpisodeKeys(epKeys),
				})
				extra := ""
				if len(epKeys) > 0 {
					extra = "剧集：" + JoinEpisodeKeys(epKeys)
				}
				sendTransferSuccessNotification(sub.SourceType, recTitle, targetDir, total, extra)
				helpers.AppLogger.Infof("TG 频道订阅 #%d：命中帖 %s，已转存「%s」共 %d 项到 %s（目标 %s）", sub.ID, p.PostID, title, total, sub.SourceType, targetDir)
			} else if models.HasLinkRecord(link.URL) {
				skipped++
				recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, "去重跳过：该分享链接已转存过")
				continue
			} else {
				// 关键词标题校验（通用订阅）：帖子文本命中关键词不代表链接内容命中——
				// 聚合帖（一帖打包多部剧）会把无关资源整帖转走；转存前用分享顶级目录名复核关键词
				if len(kws) > 0 && !shareTitleMatchesKeywords(ctx, link.URL, link.Pwd, kws) {
					skipped++
					recordMonitorSkipped("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, "跳过：分享标题与关键词不匹配（聚合帖防误转）")
					continue
				}
				title, total, err := saveShareByLink(ctx, link.URL, link.Pwd, sub.SourceType, targetDir)
				if err != nil {
					errs = append(errs, fmt.Sprintf("帖%s(%s)转存失败：%v", p.PostID, link.URL, err))
					failedIDs = append(failedIDs, p.PostID)
					recordMonitorFailed("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, targetDir, sub.ID, err)
					sendTransferFailedNotification(sub.SourceType, transferNotifTitle(sub, p.Text), targetDir, err.Error())
					continue
				}
				transferred++
				recTitle := sub.TMDBTitle
				if recTitle == "" {
					recTitle = title
				}
				recordMonitorSuccess("channel", sub.SourceType, channel, p.PostID, msgURLFor(p.PostID), link.URL, title, targetDir, total, sub.ID)
				_ = models.CreateTransferRecord(&models.CloudTransferRecord{
					SourceType:     sub.SourceType,
					SubscriptionID: sub.ID,
					MediaType:      sub.MediaType,
					TMDBID:         sub.TMDBID,
					Season:         sub.Season,
					Title:          recTitle,
					PostID:         p.PostID,
					LinkURL:        link.URL,
					TargetDir:      targetDir,
				})
				sendTransferSuccessNotification(sub.SourceType, recTitle, targetDir, total, "")
				helpers.AppLogger.Infof("TG 频道订阅 #%d：命中帖 %s，已转存「%s」共 %d 项到 %s（目标 %s）", sub.ID, p.PostID, title, total, sub.SourceType, targetDir)
			}
		}
	}

	if len(failedIDs) > 0 {
		// 转存失败的帖不推进游标：回退到最旧失败帖的前一位，保证下次重扫补转存
		oldestFailed := failedIDs[len(failedIDs)-1]
		if n, err := strconv.ParseInt(oldestFailed, 10, 64); err == nil && n > 0 {
			newMaxID = strconv.FormatInt(n-1, 10)
		}
	}
	ch.LastPostID = newMaxID
	ch.LastRunAt = time.Now()
	if err := models.SaveCloudChannel(ch); err != nil {
		return fmt.Sprintf("频道 %s 游标保存失败：%v", channel, err), false
	}

	summary := fmt.Sprintf("频道 %s：命中 %d 帖，链接 %d 个，转存成功 %d 次，去重跳过 %d 次，游标推进至 %s",
		channel, hits, linkFound, transferred, skipped, newMaxID)
	if len(errs) > 0 {
		summary += "；失败：" + strings.Join(errs, "；")
		return summary, false
	}
	return summary, true
}

// PreviewChannel 抓取频道最近帖用于前端预览
func PreviewChannel(channel string, limit int) ([]tgchannel.ChannelPost, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	posts, err := tgchannel.ParseChannelPage(ctx, channel)
	if err != nil {
		return nil, err
	}
	if len(posts) > limit {
		posts = posts[:limit]
	}
	return posts, nil
}

// postIDGreater 帖子 ID 数值比较（a > b）。字符串数字，按长度+字典序
func postIDGreater(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// saveShareByLink 将分享链接文本转存到指定网盘目录（订阅引擎与 TG 消息共用）
func saveShareByLink(ctx context.Context, text, pwd, sourceType, targetDir string) (title string, total int, err error) {
	switch sourceType {
	case string(models.SourceType123):
		m := pan123ShareLinkPattern.FindStringSubmatch(text)
		if m == nil {
			return "", 0, fmt.Errorf("未识别 123 分享链接：%s", text)
		}
		return savePan123Share(ctx, m[1], pwd, targetDir)
	case string(models.SourceTypeGuangYaPan):
		m := guangyaShareLinkPattern.FindStringSubmatch(text)
		if m == nil {
			return "", 0, fmt.Errorf("未识别光鸭分享链接：%s", text)
		}
		return saveGuangYaShare(ctx, m[1], pwd, targetDir)
	case string(models.SourceTypePan139):
		m := pan139ShareLinkPattern.FindStringSubmatch(text)
		if m == nil {
			return "", 0, fmt.Errorf("未识别 139 分享链接：%s", text)
		}
		return savePan139Share(ctx, m[1], targetDir)
	default:
		return "", 0, fmt.Errorf("不支持的网盘类型：%s", sourceType)
	}
}

// shareTitleMatchesKeywords 通用订阅转存前的关键词复核：用分享顶级条目名匹配关键词。
// 仅 123 分享支持（ListShareDir 轻量查询，不产生转存动作）；其余网盘与查询失败时放行（保持旧行为）。
func shareTitleMatchesKeywords(ctx context.Context, linkURL, pwd string, kws []string) bool {
	if len(kws) == 0 {
		return true
	}
	m := pan123ShareLinkPattern.FindStringSubmatch(linkURL)
	if m == nil {
		return true // 非 123 分享：不拦截
	}
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&account).Error; err != nil {
		return true // 查不到账号（拦截逻辑降级，交给转存环节报错）
	}
	client := account.Get123Client()
	defer client.Close()
	items, err := client.ListShareDir(ctx, m[1], pwd, "0")
	if err != nil || len(items) == 0 {
		return true // 查询失败：不拦截（失效分享由转存环节自然报错）
	}
	name := items[0].FileName
	if strings.TrimSpace(name) == "" {
		return true
	}
	return tgchannel.MatchKeywords(name, kws)
}

// savePan123Share 转存 123 分享到指定目录
func savePan123Share(ctx context.Context, shareKey, sharePwd, targetDir string) (title string, total int, err error) {
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&account).Error; err != nil {
		return "", 0, fmt.Errorf("未配置 123 云盘账号")
	}
	client := account.Get123Client()
	defer client.Close()
	targetParentID, err := client.FindDirByPath(ctx, targetDir)
	if err != nil {
		return "", 0, fmt.Errorf("目标目录不存在 %q（%v）", targetDir, err)
	}
	return client.SaveShare(ctx, shareKey, sharePwd, targetParentID)
}

// saveGuangYaShare 转存光鸭分享到指定目录
func saveGuangYaShare(ctx context.Context, shareID, code, targetDir string) (title string, total int, err error) {
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceTypeGuangYaPan).Order("id asc").First(&account).Error; err != nil {
		return "", 0, fmt.Errorf("未配置光鸭云盘账号")
	}
	client := account.GetGuangYaPanClient()
	parentID := ""
	if targetDir != "" && targetDir != "/" {
		parentID, err = client.GetPathIdByPath(ctx, targetDir)
		if err != nil {
			return "", 0, fmt.Errorf("目标目录不存在 %q（%v）", targetDir, err)
		}
	}
	return client.SaveShare(ctx, shareID, code, parentID)
}

// savePan139Share 转存 139 分享到指定目录
func savePan139Share(ctx context.Context, linkID, saveDir string) (title string, total int, err error) {
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceTypePan139).Order("id asc").First(&account).Error; err != nil {
		return "", 0, fmt.Errorf("未配置中国移动云盘账号")
	}
	client := account.GetPan139Client()
	targetCatalogID, err := client.GetPathIdByPath(ctx, saveDir)
	if err != nil {
		return "", 0, fmt.Errorf("目标目录不存在 %q（%v）", saveDir, err)
	}
	info, err := client.ListShareDir(ctx, linkID, "", "root")
	if err != nil {
		return "", 0, fmt.Errorf("查询分享链接失败：%v", err)
	}
	coPaths := make([]string, 0, len(info.FileList))
	for _, item := range info.FileList {
		if item.Path != "" {
			coPaths = append(coPaths, item.Path)
		}
	}
	caPaths := make([]string, 0, len(info.FolderList))
	for _, item := range info.FolderList {
		if item.Path != "" {
			caPaths = append(caPaths, item.Path)
		}
	}
	if len(coPaths) == 0 && len(caPaths) == 0 {
		return "", 0, fmt.Errorf("分享链接内容为空")
	}
	taskID, err := client.SaveShareFiles(ctx, linkID, "", targetCatalogID, coPaths, caPaths)
	if err != nil {
		return "", 0, fmt.Errorf("转存失败：%v", err)
	}
	_ = taskID
	return info.LinkName, len(coPaths) + len(caPaths), nil
}

// parseSourceTypeName 网盘类型显示名
func parseSourceTypeName(sourceType string) string {
	switch sourceType {
	case string(models.SourceType123):
		return "123 云盘"
	case string(models.SourceTypeGuangYaPan):
		return "光鸭云盘"
	case string(models.SourceTypePan139):
		return "移动云盘"
	default:
		return sourceType
	}
}

// ensureSourceTypeValid 校验网盘类型是否支持订阅
func ensureSourceTypeValid(sourceType string) bool {
	switch sourceType {
	case string(models.SourceType123), string(models.SourceTypeGuangYaPan), string(models.SourceTypePan139):
		return true
	}
	return false
}

// strToUint 字符串转 uint（订阅命令用）
func strToUint(s string) uint {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return uint(v)
}

// washTargetMet 洗版模式自动完结判定：剧集已全部收录且所有当前生效记录均达到洗版目标（无目标=永不自动完结）
func washTargetMet(sub *models.CloudSubscription) bool {
	target := WashTargetScore(sub.WashTarget)
	if target == 0 {
		return false
	}
	recs := models.ActiveRecords(sub.ID)
	if len(recs) == 0 {
		return false
	}
	if sub.MediaType == "tv" && sub.Season == 0 && sub.TotalSeasons > 0 && len(recs) < sub.TotalSeasons {
		return false
	}
	if sub.MediaType == "tv" && sub.TotalEpisodes > 0 && int(models.CountDistinctEpisodes(sub.ID, sub.TMDBID, sub.Season)) < sub.TotalEpisodes {
		return false // 剧集尚未全部收录
	}
	for i := range recs {
		if recordToSpec(&recs[i]).Score() < target {
			return false
		}
	}
	return true
}

// refreshSubscriptionTotalEpisodes 从 TMDB 刷新 TV 订阅的目标总集数快照（Season>0 取该季集数；Season=0 取全剧总集数）
func refreshSubscriptionTotalEpisodes(sub *models.CloudSubscription) {
	if sub.MediaType != "tv" || sub.TMDBID <= 0 {
		return
	}
	tmdbClient := models.GlobalScrapeSettings.GetTmdbClient()
	lang := models.GlobalScrapeSettings.GetTmdbLanguage()
	total := 0
	if sub.Season > 0 {
		if sd, err := tmdbClient.GetTvSeasonDetail(sub.TMDBID, sub.Season, lang); err == nil {
			total = sd.EpisodeCount
		}
	} else if td, err := tmdbClient.GetTvDetail(sub.TMDBID, lang); err == nil {
		total = td.NumberOfEpisodes
	}
	if total > 0 && total != sub.TotalEpisodes {
		sub.TotalEpisodes = total
		_ = models.SaveCloudSubscription(sub)
	}
}
