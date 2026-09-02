package moviepilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
)

// WashScanSummary 一次违规扫描的结果摘要（同时持久化到配置 LastResult 供前端展示 + 通知）
type WashScanSummary struct {
	AccountID    uint   `json:"account_id"`
	OrgRoot      string `json:"org_root"`
	ScannedFiles int    `json:"scanned_files"` // 扫描到的视频文件数
	ViolationNum int    `json:"violation_num"` // 判定为待洗版（违规）的条目数
	CleanRemoved int    `json:"clean_removed"` // 本次扫描清除的已达标/已消失条目数
	Errors       int    `json:"errors"`
	Details      []string `json:"details"`
}

// RunWashScan 对指定账号执行违规扫描（P1-1）：
// 遍历已整理根目录 → 解析每个视频的媒体信息与质量 → 按 min_resolution / preferred_codecs 判定违规 →
// upsert WashScanItem（达标文件清除历史条目）；扫描结束清理已消失文件条目。
// cfg 为账号的自动整理配置（含洗版参数）。不要求启用自动整理。
func RunWashScan(ctx context.Context, cfg *models.AutoOrganizeConfig) (WashScanSummary, error) {
	summary := WashScanSummary{AccountID: cfg.AccountID}
	if cfg == nil || cfg.AccountID == 0 {
		return summary, fmt.Errorf("配置为空")
	}
	account, err := models.GetAccountById(cfg.AccountID)
	if err != nil || account == nil {
		return summary, fmt.Errorf("加载账号失败：%v", err)
	}
	organizedRoot := strings.Trim(cfg.OrganizedRoot, "/")
	if organizedRoot == "" {
		organizedRoot = organizeRootPath(strings.Trim(cfg.PendingDir, "/"))
	}
	summary.OrgRoot = organizedRoot
	rootID, err := findRemoteDirID(ctx, account, organizedRoot)
	if err != nil {
		return summary, fmt.Errorf("已整理目录 %s 不存在或无法访问：%v", organizedRoot, err)
	}
	helpers.AppLogger.Infof("违规扫描开始（账号 %d）：已整理根目录=%s", cfg.AccountID, organizedRoot)

	seen := make(map[string]bool)
	var fileCount int

	var walk func(dirID, relPath string, depth int) error
	walk = func(dirID, relPath string, depth int) error {
		if depth > 6 {
			return nil
		}
		entries, err := listNetDirByID(ctx, account, dirID)
		if err != nil {
			summary.Errors++
			summary.Details = append(summary.Details, fmt.Sprintf("扫描目录 %s 失败：%v", relPath, err))
			return nil // 单个目录失败不中断整树
		}
		for i := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			fileCount++
			if fileCount > 30000 {
				return fmt.Errorf("扫描条目超过 30000 上限，中止")
			}
			e := &entries[i]
			if e.IsDir {
				sub := e.Name
				if relPath != "" {
					sub = relPath + "/" + e.Name
				}
				if err := walk(e.ID, sub, depth+1); err != nil {
					return err
				}
				continue
			}
			if !isVideoFile(e.Name) {
				continue
			}
			summary.ScannedFiles++
			item := buildWashScanItem(cfg, e.Name, relPath)
			key := strings.TrimRight(relPath, "/") + "|" + e.Name
			seen[key] = true
			if item.Violations == "" {
				// 达标：清除历史待洗版条目
				if err := models.DeleteWashItemForFile(cfg.AccountID, relPath, e.Name); err != nil {
					helpers.AppLogger.Warnf("违规扫描清除达标条目失败（账号 %d）：%v", cfg.AccountID, err)
				}
				continue
			}
			summary.ViolationNum++
			if err := models.UpsertWashItem(item); err != nil {
				helpers.AppLogger.Warnf("违规扫描写入条目失败（账号 %d，%s）：%v", cfg.AccountID, e.Name, err)
				summary.Errors++
				continue
			}
		}
		return nil
	}

	if err := walk(rootID, "", 0); err != nil {
		return summary, err
	}

	before := models.CountWashItems(cfg.AccountID, "")
	if err := models.DeleteWashItemMissing(cfg.AccountID, seen); err != nil {
		helpers.AppLogger.Warnf("违规扫描清理消失条目失败（账号 %d）：%v", cfg.AccountID, err)
	} else {
		summary.CleanRemoved = int(before - models.CountWashItems(cfg.AccountID, ""))
		if summary.CleanRemoved < 0 {
			summary.CleanRemoved = 0
		}
	}
	if data, err := json.Marshal(summary); err == nil {
		models.UpdateAutoOrganizeLastScan(cfg.AccountID, string(data))
	}
	helpers.AppLogger.Infof("违规扫描完成（账号 %d）：扫描文件 %d / 待洗版 %d", cfg.AccountID, summary.ScannedFiles, summary.ViolationNum)
	return summary, nil
}

// NotifyWashScan 违规扫描完成后的系统通知（P1-2）
func NotifyWashScan(summary WashScanSummary) {
	account, _ := models.GetAccountById(summary.AccountID)
	accountName := "?"
	if account != nil {
		accountName = account.Username
	}
	title := "🔍 违规扫描完成（待洗版清单）"
	lines := []string{
		fmt.Sprintf("账号：%s", accountName),
		fmt.Sprintf("已整理根目录：%s", summary.OrgRoot),
		fmt.Sprintf("扫描视频文件：%d 个", summary.ScannedFiles),
		fmt.Sprintf("判定待洗版（不达标）：%d 个", summary.ViolationNum),
		fmt.Sprintf("清除已达标/消失条目：%d 个", summary.CleanRemoved),
	}
	if summary.Errors > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 扫描异常目录：%d 处", summary.Errors))
	}
	if summary.ViolationNum > 0 {
		lines = append(lines, "可在「云盘整理 → 待洗版清单」中查看，或执行一键洗版（需要新的更优资源）。")
	} else if summary.ViolationNum == 0 {
		lines = append(lines, "媒体库暂无不达标条目 ✅")
	}
	lines = append(lines, fmt.Sprintf("⏰ 时间：%s", time.Now().Format("2006-01-02 15:04:05")))
	sendSystemNotification(title, strings.Join(lines, "\n"))
}

// seasonDirRe 匹配整理目录中的 Season 段（Season 01 / Season.01 等）
var seasonDirRe = regexp.MustCompile(`(?i)^season[\s._-]*(\d{1,3})$`)

// buildWashScanItem 从整理目录条目构建待洗版条目（媒体信息：目录段优先，文件名补全）
func buildWashScanItem(cfg *models.AutoOrganizeConfig, fileName, relPath string) *models.WashScanItem {
	item := &models.WashScanItem{
		AccountID: cfg.AccountID,
		RelPath:   strings.Trim(relPath, "/"),
		FileName:  fileName,
	}
	// 目录段解析：{tmdb=xxx} 段提供标题/年份/ID；Season 段提供季号
	var dirTmdbTitle string
	for _, seg := range strings.Split(relPath, "/") {
		if seg == "" {
			continue
		}
		if id := extractTmdbIDFromName(seg); id > 0 {
			item.TMDBID = id
			dirTmdbTitle = strings.TrimSpace(stripTmdbTag(seg))
		}
		if m := seasonDirRe.FindStringSubmatch(seg); m != nil {
			n, _ := strconv.Atoi(m[1])
			item.SeasonNum = n
			continue
		}
		_, t, _, _, y := mediaparse.ParseMedia(seg)
		if t != "" && item.Title == "" {
			item.Year = y
		}
	}
	// 文件名解析：季/集与标题补全（电影无季集）
	_, fTitle, _, fEpisode, fYear := mediaparse.ParseMedia(fileName)
	if parsed, ok := mediaparse.ParseEpisode(fileName); ok && parsed.Season > 0 && item.SeasonNum <= 0 {
		item.SeasonNum = parsed.Season
	}
	if fEpisode > 0 {
		item.EpisodeNum = fEpisode
	}
	if item.Title == "" {
		item.Title = fTitle
	}
	if item.Year <= 0 {
		item.Year = fYear
	}
	if dirTmdbTitle != "" {
		item.Title = dirTmdbTitle // 整理目录标题优先（通常更规范，已去 TMDB 标记）
	}
	if item.SeasonNum > 0 {
		item.MediaType = "TV"
	} else if item.EpisodeNum > 0 {
		item.MediaType = "TV"
	} else {
		item.MediaType = "Movie"
	}
	// 质量快照
	q := ParseQualityFromName(fileName)
	item.Resolution = q.Resolution
	item.ResTag = q.ResTag
	item.Codec = q.Codec
	item.CodecTag = q.CodecTag
	item.AudioTag = q.AudioTag
	item.Channels = q.Channels
	item.Violations = buildWashViolations(cfg, q)
	return item
}

// buildWashViolations 判定文件是否不达标（P1-1 违规规则：分辨率 + 首选编码）
func buildWashViolations(cfg *models.AutoOrganizeConfig, q *FileQuality) string {
	var v []string
	if cfg.MinResolution > 0 {
		if q.Resolution > 0 && q.Resolution < cfg.MinResolution {
			v = append(v, fmt.Sprintf("分辨率 %s 低于 %dp", q.ResTag, cfg.MinResolution))
		} else if q.Resolution == 0 {
			v = append(v, "未识别分辨率（低于或未知）")
		}
	}
	if strings.TrimSpace(cfg.PreferredCodecs) != "" {
		pref := splitWordList(cfg.PreferredCodecs)
		norm := func(s string) string {
			return strings.NewReplacer(".", "", "-", "", " ", "").Replace(strings.ToLower(s))
		}
		matched := false
		if q.Codec != "" || q.CodecTag != "" {
			for _, c := range pref {
				cn := norm(c)
				if cn != "" && (norm(q.Codec) == cn || norm(q.CodecTag) == cn) {
					matched = true
					break
				}
			}
			if !matched {
				codecShow := q.CodecTag
				if codecShow == "" {
					codecShow = q.Codec
				}
				v = append(v, fmt.Sprintf("编码 %s 非首选 %s", codecShow, cfg.PreferredCodecs))
			}
		} else {
			v = append(v, "未识别编码（非首选列表）")
		}
	}
	return strings.Join(v, "；")
}