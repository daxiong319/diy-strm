package moviepilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/notificationmanager"
)

// AutoOrganizeResult 一次自动整理运行的结果摘要（同时持久化到配置的 LastResult 供前端展示）
type AutoOrganizeResult struct {
	AccountID        uint            `json:"account_id"`
	Organized        int             `json:"organized"`         // 成功整理（移动+重命名）的视频数
	Failed           int             `json:"failed"`            // 整理失败（流程错误）数
	Unrecognized     int             `json:"unrecognized"`      // 识别失败/TMDB 查不到，已移入失败目录数
	SkippedOverwrite int             `json:"skipped_overwrite"` // 目标已存在同片且非洗版模式，跳过数
	NonMedia         int             `json:"non_media"`         // 非视频条目跳过数
	DeletedEmptySrc  int             `json:"deleted_empty_src"` // 整理后清空的源目录删除数
	MovedToFailed    int             `json:"moved_to_failed"`   // 整体移入失败目录的资源数（目录/文件）
	SuccessDirs      []string        `json:"success_dirs"`      // 整理成功的目标相对目录（相对已整理根目录）
	FailedNames      []string        `json:"failed_names"`      // 移入失败目录/处理失败的资源名
	Details          []string        `json:"details"`           // 明细（前端展示/日志）
	Items            []OrganizedItem `json:"items,omitempty"`   // 成功整理的影视条目（通知展示用）
}

// OrganizedItem 单个成功整理的影视条目（通知展示用，tgto123 风格分组）
type OrganizedItem struct {
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Category     string  `json:"category"`
	MediaType    string  `json:"media_type"` // movie / tv
	Season       int     `json:"season"`
	Episode      int     `json:"episode"`
	Quality      string  `json:"quality"` // 质量标签摘要
	ReleaseGroup string  `json:"release_group"`
	FileName     string  `json:"file_name"` // 整理后文件名
	SizeGB       float64 `json:"size_gb"`
	TmdbID       int64   `json:"tmdb_id"`
}

// leadingIndexRe TG 频道分享名的前导批次序号（如「2-遮.天」的「2-」）
var leadingIndexRe = regexp.MustCompile(`^\d{1,3}[.\-_ ]+\s*`)

// yearFanRe 「年番N」季标记（年番第 N 部 → TMDB 第 N 季）
var yearFanRe = regexp.MustCompile(`年番\s*(\d{1,2})`)

// cjkDotRe 中文之间的点分隔（TG 分享名的敏感词规避符号，如「遮.天」=「遮天」）。
// 必须在 ParseMedia 之前移除：path.Ext 会把名字里最后一个点当扩展名分隔符，
// 导致 stem 被截断（「遮.天 (2026)」→ stem「遮」，「天 (2026)」被当扩展名丢弃）。
var cjkDotRe = regexp.MustCompile(`([\p{Han}])\.([\p{Han}])`)

// stripCjkDots 删除中文夹点场景的点（循环处理多段；英文/数字/扩展名的点不受影响）
func stripCjkDots(s string) string {
	for cjkDotRe.MatchString(s) {
		s = cjkDotRe.ReplaceAllString(s, "$1$2")
	}
	return s
}

// normalizeAutoDirName 归一化 TG 转存目录名：
//  1. 「年番N」提取为季号并从名字移除（「2-遮.天 年番4 (2026)」→「遮.天 (2026)」+ S4）
//  2. 剥前导批次序号「N-」（剥后非空才采用）
//
// 返回归一化后的名字与年番季号（0=非年番）
func normalizeAutoDirName(name string) (string, int) {
	name = strings.TrimSpace(name)
	fanSeason := 0
	if m := yearFanRe.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			fanSeason = n
		}
		name = strings.TrimSpace(yearFanRe.ReplaceAllString(name, " "))
	}
	if leadingIndexRe.MatchString(name) {
		trimmed := leadingIndexRe.ReplaceAllString(name, "")
		if strings.TrimSpace(trimmed) != "" {
			name = trimmed
		}
	}
	// 中文夹点移除（规避 path.Ext 把「遮.天 (2026)」截成「遮」的问题）
	name = stripCjkDots(name)
	// 压缩移除年番/序号后残留的连续空格
	return strings.Join(strings.Fields(name), " "), fanSeason
}

// extractReleaseGroup 从文件名提取发布组（最后一个「-」之后的标签，如 xxx.H.265-Ocat → Ocat）
func extractReleaseGroup(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if idx := strings.LastIndex(base, "-"); idx > 0 {
		g := strings.TrimSpace(base[idx+1:])
		if g != "" && len(g) <= 32 && !strings.ContainsAny(g, ".0123456789") {
			return g
		}
	}
	return ""
}

// formatEpisodeRanges 集号列表转紧凑区间（[1,2,3,7] → "E01-E03、E07"）
func formatEpisodeRanges(episodes []int) string {
	if len(episodes) == 0 {
		return ""
	}
	sorted := append([]int(nil), episodes...)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	start, prev := sorted[0], sorted[0]
	flush := func() {
		if start == prev {
			parts = append(parts, fmt.Sprintf("E%02d", start))
		} else {
			parts = append(parts, fmt.Sprintf("E%02d-E%02d", start, prev))
		}
	}
	for _, e := range sorted[1:] {
		if e == prev+1 {
			prev = e
			continue
		}
		flush()
		start, prev = e, e
	}
	flush()
	return strings.Join(parts, "、")
}

// accountDisplayName 账号显示名：备注（name）优先，回退用户名
func accountDisplayName(account *models.Account) string {
	if account == nil {
		return ""
	}
	if strings.TrimSpace(account.Name) != "" {
		return account.Name
	}
	return account.Username
}

// RunAutoOrganize 对指定账号执行一轮自动整理：
// 扫描待整理目录顶层资源 → 目录/文件名解析 + TMDB 校验 → 按账号分类策略 yaml 分类 →
// 建目标目录（已整理/{分类}/{标题 (年份) {tmdb=xxx}}[/Season NN]）→ 移动 → 重命名（保留质量标签）。
// 识别失败移入失败目录；目标已存在同片时按配置覆盖（洗版）或跳过。
func RunAutoOrganize(ctx context.Context, cfg *models.AutoOrganizeConfig) *AutoOrganizeResult {
	result := &AutoOrganizeResult{AccountID: cfg.AccountID}
	if cfg == nil || cfg.AccountID == 0 {
		return result
	}
	account, err := models.GetAccountById(cfg.AccountID)
	if err != nil || account == nil {
		result.Details = append(result.Details, fmt.Sprintf("加载账号失败：%v", err))
		return result
	}
	pendingDir := strings.Trim(cfg.PendingDir, "/")
	if pendingDir == "" {
		result.Details = append(result.Details, "未配置待整理目录，跳过")
		return result
	}
	// 待整理根目录只查找不创建（避免自动建目录干扰用户目录结构）
	rootID, err := findRemoteDirID(ctx, account, pendingDir)
	if err != nil {
		result.Details = append(result.Details, fmt.Sprintf("待整理目录 %s 不存在或无法访问：%v", pendingDir, err))
		return result
	}
	organizedRoot := strings.Trim(cfg.OrganizedRoot, "/")
	if organizedRoot == "" {
		organizedRoot = organizeRootPath(pendingDir)
	}
	// 失败目录留空时默认使用 待整理目录同级/整理失败（运行时生效，不写回配置；不存在会自动创建）
	effectiveCfg := *cfg
	if strings.TrimSpace(effectiveCfg.FailedDir) == "" {
		effectiveCfg.FailedDir = failedRootPath(pendingDir)
	}
	cfg = &effectiveCfg
	helpers.AppLogger.Infof("自动整理开始（账号 %d）：待整理目录=%s 已整理根目录=%s 失败目录=%s", cfg.AccountID, pendingDir, organizedRoot, cfg.FailedDir)
	rules := parseCategoryRules(cfg.CategoryConfig)
	dirCache := make(map[string]string)
	aiBudget := aiTryBudget

	entries, err := listNetDirByID(ctx, account, rootID)
	if err != nil {
		result.Details = append(result.Details, fmt.Sprintf("扫描待整理目录 %s 失败：%v", pendingDir, err))
		return result
	}
	if len(entries) == 0 {
		return result
	}

	defer finishAutoOrganizeResult(cfg, result)

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			result.Details = append(result.Details, "上下文取消，本轮中断")
			break
		}
		if e.IsDir {
			processAutoOrganizeDir(ctx, account, cfg, result, &e, organizedRoot, &rules, dirCache, &aiBudget, 0)
		} else {
			if !mediaparse.IsVideoExt(e.Name) {
				result.NonMedia++
				result.Details = append(result.Details, fmt.Sprintf("跳过非视频文件：%s", e.Name))
				continue
			}
			if err := organizeAutoVideoFile(ctx, account, cfg, result, &e, nil, organizedRoot, &rules, dirCache, &aiBudget); err != nil {
				if errors.Is(err, errMediaUnrecognized) {
					result.Unrecognized++
					moveEntryToFailedDir(ctx, account, cfg, &e, result, fmt.Sprintf("识别失败：%v", err))
				} else {
					result.Failed++
					result.FailedNames = append(result.FailedNames, e.Name)
					result.Details = append(result.Details, fmt.Sprintf("整理失败 %s：%v", e.Name, err))
				}
			}
		}
	}
	return result
}

// maxAggregateDescendDepth 聚合容器兜底的最大递归深度（防异常分享出现 剧集/剧集/剧集… 病理嵌套）
const maxAggregateDescendDepth = 5

// genericAggregateNames 云盘分享中常见的「分类容器」目录名：
// 这类目录本身不是影视资源，只是多个资源（通常每个子目录一部剧集/电影）的聚合。
// 目录级识别失败且目录名为通用名时，读取内容逐个子资源独立识别，而不是整目录进失败目录。
var genericAggregateNames = map[string]bool{
	"剧集": true, "电视剧": true, "电视剧集": true, "连续剧": true, "短剧": true,
	"动漫": true, "动画": true, "动画片": true, "番剧": true, "日番": true,
	"电影": true, "影片": true, "综艺": true, "综艺节目": true, "纪录片": true,
	"合集": true, "收藏": true, "其他": true, "其它": true, "未分类": true, "待整理": true,
}

// aggregateCategoryKeywords 分类关键词：目录名含这些词且不带年份时视为分类容器
// （覆盖 国产剧集/日韩剧集/欧美剧集/国产动漫/日番动漫/动画电影 等任意二级分类名，无需穷举）
var aggregateCategoryKeywords = []string{
	"剧集", "电视剧", "连续剧", "短剧", "动漫", "动画", "番剧", "电影", "综艺", "纪录片",
}

// autoYearInNameRe 匹配目录名中的 4 位年份（真实剧名目录通常带年份，如 花开锦绣 (2026)）
var autoYearInNameRe = regexp.MustCompile(`(?:19|20)\d{2}`)

// isGenericAggregateDirName 判断目录名是否为通用分类容器名（调用方先 stripTmdbTag）：
//  1. 精确命中通用名（剧集/动漫/电影/其他/合集 等）；
//  2. 或含分类关键词且不带年份（国产剧集/日韩剧集/国产动漫 等二级分类名）。
//     真实剧名目录普遍带年份，不会被误判。
func isGenericAggregateDirName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.Trim(n, " _-.　")
	if n == "" {
		return false
	}
	if genericAggregateNames[n] {
		return true
	}
	if autoYearInNameRe.MatchString(n) {
		return false
	}
	for _, kw := range aggregateCategoryKeywords {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

// processAutoOrganizeDir 整理一个顶层目录资源（转存分享树根目录）。
// 目录名识别优先（保持既有机制）；目录级识别失败且目录名为通用分类容器名时，
// 兜底读取目录内容逐个子资源独立识别。depth 为聚合容器兜底递归深度。
func processAutoOrganizeDir(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, dir *organizeEntry, organizedRoot string, rules *categoryRules, dirCache map[string]string, aiBudget *int, depth int) {
	// 目录名解析（标题/年份优先从目录名取，季集优先从文件名取）；
	// 先归一化目录名（剥前导批次序号、提取「年番N」季标记）并剥离内嵌
	// TMDB 标记（{tmdbid-xxx}），避免污染标题搜索
	normalizedDirName, fanSeason := normalizeAutoDirName(stripTmdbTag(dir.Name))
	cleanDirName := normalizedDirName
	dirCategory, dirTitle, dirSeason, _, dirYear := mediaparse.ParseMedia(cleanDirName)
	if fanSeason > 0 && dirSeason <= 0 {
		dirSeason = fanSeason
	}

	dirStart := time.Now()
	helpers.AppLogger.Infof("自动整理开始目录（账号 %d）：%s（目录级：类别=%s 标题=%s 季=%d 年份=%d，TMDB ID=%d）",
		cfg.AccountID, dir.Name, dirCategory, dirTitle, dirSeason, dirYear, extractTmdbIDFromName(dir.Name))
	defer func() {
		helpers.AppLogger.Infof("自动整理目录结束（账号 %d）：%s（耗时 %.1fs）", cfg.AccountID, dir.Name, time.Since(dirStart).Seconds())
	}()

	videos := make([]*organizeEntry, 0)
	{
		var all []organizeEntry
		counter := 0
		if err := collectOrganizeEntries(ctx, account, dir.ID, &all, &counter, 0); err != nil {
			result.Failed++
			result.Details = append(result.Details, fmt.Sprintf("扫描目录 %s 失败：%v", dir.Name, err))
			moveEntryToFailedDir(ctx, account, cfg, dir, result, fmt.Sprintf("目录扫描失败：%v", err))
			return
		}
		for i := range all {
			if all[i].IsDir || !mediaparse.IsVideoExt(all[i].Name) {
				continue
			}
			entry := all[i]
			videos = append(videos, &entry)
		}
	}
	if len(videos) == 0 {
		result.NonMedia++
		// 无视频也可能因为是聚合容器（子内容暂未下载完/全是图片字幕）：
		// 通用容器名时尝试逐子资源兜底，成功则照常收尾，否则跳过
		if tryOrganizeAggregateChildren(ctx, account, cfg, result, dir, organizedRoot, rules, dirCache, aiBudget, depth) {
			finalizeAutoOrganizeDir(ctx, account, cfg, result, dir)
			return
		}
		result.Details = append(result.Details, fmt.Sprintf("目录 %s 内无视频文件，跳过", dir.Name))
		return
	}

	dirCtx := &autoDirMedia{
		Category: dirCategory,
		Title:    dirTitle,
		Season:   dirSeason,
		Year:     dirYear,
		RawName:  dir.Name,
		TmdbId:   extractTmdbIDFromName(dir.Name),
	}
	for _, v := range videos {
		if err := ctx.Err(); err != nil {
			result.Details = append(result.Details, "上下文取消，本轮中断")
			break
		}
		if err := organizeAutoVideoFile(ctx, account, cfg, result, v, dirCtx, organizedRoot, rules, dirCache, aiBudget); err != nil {
			if errors.Is(err, errMediaUnrecognized) {
				result.Unrecognized++
				// 兜底：目录名为通用分类容器（剧集/动漫/电影等）且无 TMDB 标记时，
				// 目录名不是真实标题，读取目录内容逐个子资源独立识别（子目录按自身剧名识别，
				// 直挂视频按文件名识别）；单个子资源失败单独进失败目录，不再拖垮整个目录。
				if tryOrganizeAggregateChildren(ctx, account, cfg, result, dir, organizedRoot, rules, dirCache, aiBudget, depth) {
					finalizeAutoOrganizeDir(ctx, account, cfg, result, dir)
					return
				}
				moveEntryToFailedDir(ctx, account, cfg, dir, result, fmt.Sprintf("目录 %s 内文件识别失败：%v", dir.Name, err))
				return
			}
			result.Failed++
			result.FailedNames = append(result.FailedNames, v.Name)
			result.Details = append(result.Details, fmt.Sprintf("整理失败 %s：%v", v.Name, err))
		}
	}

	finalizeAutoOrganizeDir(ctx, account, cfg, result, dir)
}

// tryOrganizeAggregateChildren 聚合容器兜底：目录级识别失败且目录名为通用分类容器时，
// 读取目录内容逐个子资源独立整理。
//   - 子目录：按其自身目录名识别（processAutoOrganizeDir 递归，内部仍可再次兜底）；
//   - 直挂视频：按文件名识别（dirCtx 传 nil），失败的单个文件移入失败目录；
//   - 非视频文件：计入 NonMedia，留在原地。
//
// 返回 true 表示已按聚合容器模式处理（调用方随后 finalize 收尾）；false 表示目录名
// 不是通用分类容器（调用方维持原逻辑整目录移入失败目录）。
func tryOrganizeAggregateChildren(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, dir *organizeEntry, organizedRoot string, rules *categoryRules, dirCache map[string]string, aiBudget *int, depth int) bool {
	if !isGenericAggregateDirName(stripTmdbTag(dir.Name)) || extractTmdbIDFromName(dir.Name) != 0 {
		return false
	}
	entries, err := listNetDirByID(ctx, account, dir.ID)
	if err != nil {
		result.Failed++
		result.Details = append(result.Details, fmt.Sprintf("扫描聚合容器 %s 失败：%v", dir.Name, err))
		return true
	}
	for i := range entries {
		e := &entries[i]
		if ctx.Err() != nil {
			result.Details = append(result.Details, "上下文取消，本轮中断")
			break
		}
		switch {
		case e.IsDir:
			processAutoOrganizeDir(ctx, account, cfg, result, e, organizedRoot, rules, dirCache, aiBudget, depth+1)
		case mediaparse.IsVideoExt(e.Name):
			if err := organizeAutoVideoFile(ctx, account, cfg, result, e, nil, organizedRoot, rules, dirCache, aiBudget); err != nil {
				if errors.Is(err, errMediaUnrecognized) {
					result.Unrecognized++
					moveEntryToFailedDir(ctx, account, cfg, e, result, fmt.Sprintf("识别失败：%v", err))
				} else {
					result.Failed++
					result.FailedNames = append(result.FailedNames, e.Name)
					result.Details = append(result.Details, fmt.Sprintf("整理失败 %s：%v", e.Name, err))
				}
			}
		default:
			result.NonMedia++
		}
	}
	return true
}

// finalizeAutoOrganizeDir 整理收尾：源目录已空则删除；有残留则整体移入失败目录（不丢数据）。
func finalizeAutoOrganizeDir(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, dir *organizeEntry) {
	leftovers, err := listNetDirByID(ctx, account, dir.ID)
	if err != nil {
		result.Details = append(result.Details, fmt.Sprintf("整理后复查目录 %s 失败：%v", dir.Name, err))
		return
	}
	if len(leftovers) == 0 {
		if err := deleteNetdiskFileInternal(account, dir.ID, dir.ParentID); err == nil {
			result.DeletedEmptySrc++
			result.Details = append(result.Details, fmt.Sprintf("已删除整理干净的源目录：%s", dir.Name))
		} else {
			result.Details = append(result.Details, fmt.Sprintf("删除源目录 %s 失败：%v", dir.Name, err))
		}
		return
	}
	moveEntryToFailedDir(ctx, account, cfg, dir, result, "目录内存在非视频残留文件")
}

// autoDirMedia 目录级解析出的媒体信息（供文件级识别补全）
type autoDirMedia struct {
	Category string
	Title    string
	Season   int
	Year     int
	TmdbId   int64
	RawName  string // 原始目录名（AI 兜底识别上下文用）
}

var (
	// autoTmdbTagRe 匹配目录/文件名中内嵌的 TMDB ID 标记：{tmdbid-287496} / {tmdb=287496} / {tmdb:287496}
	autoTmdbTagRe = regexp.MustCompile(`(?i)\{tmdb(?:id)?[=:_\- ]*(\d{4,8})\}`)
)

// extractTmdbIDFromName 从目录/文件名提取内嵌的 TMDB ID，无则返回 0
func extractTmdbIDFromName(name string) int64 {
	m := autoTmdbTagRe.FindStringSubmatch(name)
	if len(m) == 2 {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err == nil {
			return id
		}
	}
	return 0
}

// stripTmdbTag 移除目录/文件名中的 TMDB 标记（保留为空格，不影响后续分词）
func stripTmdbTag(name string) string {
	return autoTmdbTagRe.ReplaceAllString(name, " ")
}

// yearFromTMDBDate 从 TMDB 日期字符串取年份，如 "2026-05-01" → 2026
func yearFromTMDBDate(dateStr string) int {
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil && y > 1900 && y < 3000 {
			return y
		}
	}
	return 0
}

// organizeAutoVideoFile 整理单个视频文件：
// 目录级信息 + 文件级季集解析 → TMDB 校验 → 分类 → 建目录 → 移动 → 重命名（保留质量标签）。
// 返回 errMediaUnrecognized 表示识别失败/TMDB 查不到（调用方负责移入失败目录）。
func organizeAutoVideoFile(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, entry *organizeEntry, dirCtx *autoDirMedia, organizedRoot string, rules *categoryRules, dirCache map[string]string, aiBudget *int) error {
	vidStart := time.Now()
	helpers.AppLogger.Infof("自动整理开始文件（账号 %d）：%s", cfg.AccountID, entry.Name)
	defer func() {
		helpers.AppLogger.Infof("自动整理文件结束（账号 %d）：%s（耗时 %.1fs）", cfg.AccountID, entry.Name, time.Since(vidStart).Seconds())
	}()
	extra := baseOrganizeExtra(account, entry.ParentID, 0)
	sourcePath := strings.TrimRight(cfg.PendingDir, "/") + "/" + entry.Name
	media, err := buildAutoMedia(entry.Name, dirCtx)
	if err != nil {
		recordSkipped(account, *entry, sourcePath, "", "", 0, 0, 0, 0, "", "文件名无法识别", extra)
		return err
	}
	if strings.TrimSpace(media.Title) == "" && *aiBudget > 0 {
		*aiBudget--
		if ai, ok := IdentifyFileWithAI(ctx, entry.Name); ok {
			media = &ai
			if dirCtx != nil && dirCtx.Year > 0 && media.Year <= 0 {
				media.Year = dirCtx.Year
			}
			if media.TmdbId <= 0 && dirCtx != nil && dirCtx.TmdbId > 0 {
				media.TmdbId = dirCtx.TmdbId
			}
		}
	}
	if strings.TrimSpace(media.Title) == "" {
		recordSkipped(account, *entry, sourcePath, "", "", 0, 0, 0, 0, "", "文件名无法识别（AI 未启用或未命中）", extra)
		return errMediaUnrecognized
	}

	officialTitle, tmdbID, tmdbYear, categoryName, tmdbScore, err := lookupTmdbMediaWithRules(ctx, media, *rules)
	if err != nil && dirCtx != nil && strings.TrimSpace(dirCtx.RawName) != "" && *aiBudget > 0 {
		// 目录级标题可能解析损坏（批次序号/杂讯）导致 TMDB 搜不到；
		// 用 AI 对「目录名 + 文件名」重新识别，命中则替换 media 重试一次
		*aiBudget--
		helpers.AppLogger.Infof("自动整理：TMDB 未命中（%s），尝试 AI 兜底（目录：%s）", media.Title, dirCtx.RawName)
		if ai, ok := IdentifyFileWithAIContext(ctx, dirCtx.RawName, entry.Name); ok {
			ai.Season = media.Season
			ai.Episode = media.Episode
			if ai.Category == "" {
				ai.Category = media.Category
			}
			media = &ai
			officialTitle, tmdbID, tmdbYear, categoryName, tmdbScore, err = lookupTmdbMediaWithRules(ctx, media, *rules)
		}
	}
	if err != nil {
		recordSkipped(account, *entry, sourcePath, media.Category, media.Title, media.Year, media.Season, media.Episode, 0, "", "TMDB 未找到匹配结果："+err.Error(), extra)
		return fmt.Errorf("%w：%v", errMediaUnrecognized, err)
	}
	year := tmdbYear
	if year <= 0 {
		year = media.Year
	}
	relDir, ok := buildOrganizeRelDir(media.Category, officialTitle, year, media.Season, tmdbID, categoryName)
	if !ok {
		recordSkipped(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "", "媒体信息不完整，无法构建目标目录", extra)
		return fmt.Errorf("%w：媒体信息不完整", errMediaUnrecognized)
	}
	newQ := ParseQualityFromName(entry.Name)
	// 洗版延后处置：旧文件删除/归档动作在新文件成功移入后执行（wash_apply 先删后移的缺集窗口修复）
	var deferredWash washDecision

	// 入库前置过滤（P2-1/P2-3）
	// 屏蔽词：命中垃圾词表的资源（广告/特典/PV 类）不整理，源文件保留原位
	if word := matchWashWords(cfg.BlockedWords, entry.Name, media.Title, officialTitle); word != "" {
		msg := fmt.Sprintf("命中屏蔽词「%s」，不收录", word)
		result.Details = append(result.Details, fmt.Sprintf("屏蔽词过滤跳过：%s（%s）", entry.Name, msg))
		_ = models.AddWashLog(&models.WashLog{AccountID: cfg.AccountID, Action: "filter_blocked", TargetPath: relDir, Title: officialTitle, MediaType: media.Category, SeasonNum: media.Season, EpisodeNum: media.Episode, TMDBID: tmdbID, NewName: entry.Name, Message: msg, EventTime: time.Now()})
		recordSkipped(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, msg, "", extra)
		return nil
	}
	// 定制词：仅注入命名模板 customization 变量（供用户模板使用），不拦整理
	customWord := matchWashWords(cfg.CustomizationWords, entry.Name, media.Title, officialTitle)
	// 同名低分过滤：TMDB 评分低于阈值的内容不收录（参考 symedia v1.0.30.2 低分<3 过滤）
	if cfg.MinTMDBScore > 0 && tmdbScore > 0 && tmdbScore < cfg.MinTMDBScore {
		msg := fmt.Sprintf("TMDB 评分 %.1f 低于阈值 %.1f，不收录", tmdbScore, cfg.MinTMDBScore)
		result.Details = append(result.Details, fmt.Sprintf("TMDB 低分过滤跳过：%s（%s）", entry.Name, msg))
		_ = models.AddWashLog(&models.WashLog{AccountID: cfg.AccountID, Action: "score_filter", TargetPath: relDir, Title: officialTitle, MediaType: media.Category, SeasonNum: media.Season, EpisodeNum: media.Episode, TMDBID: tmdbID, NewName: entry.Name, Message: msg, EventTime: time.Now()})
		recordSkipped(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, msg, "", extra)
		return nil
	}

	// 重复/洗版检测：目标影片目录（不含 Season 段）已存在
	baseRel := relDir
	if idx := strings.Index(baseRel, "/Season "); idx >= 0 {
		baseRel = baseRel[:idx]
	}
	baseFull := strings.TrimRight(organizedRoot, "/") + "/" + baseRel
	existingBaseID, findErr := findRemoteDirID(ctx, account, baseFull)

	// 追更模式提示（P2-3）：剧集已有整理目录 → 记录追更日志（新增集数正常入库）
	if cfg.TrackRenewal && existingBaseID != "" && media.Category == "tv" {
		_ = models.AddWashLog(&models.WashLog{AccountID: cfg.AccountID, Action: "renewal_tip", TargetPath: baseRel, Title: officialTitle, MediaType: "tv", SeasonNum: media.Season, EpisodeNum: media.Episode, TMDBID: tmdbID, NewName: entry.Name, Message: fmt.Sprintf("追更到第 %d 集", media.Episode), EventTime: time.Now()})
	}

	// 非洗版模式：目标已存在 → 跳过（保留现有版本，不删除任何旧文件）
	if findErr == nil && existingBaseID != "" && !cfg.Overwrite && !cfg.WashCompare {
		result.SkippedOverwrite++
		result.Details = append(result.Details, fmt.Sprintf("目标已存在且非洗版模式，跳过：%s → %s", entry.Name, baseRel))
		recordSkipped(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "目标已存在且非洗版模式，跳过（保留现有版本）", "", extra)
		return nil
	}
	// 旧版「整目录覆盖」兼容：开启 Overwrite 但显式关闭更优才覆盖 →
	// 仅当用户明确关闭 WashCompare 时才整目录替换（保留历史行为，且记洗版日志）
	if findErr == nil && existingBaseID != "" && cfg.Overwrite && !cfg.WashCompare {
		removed := deleteVideosUnderDir(ctx, account, existingBaseID)
		result.Details = append(result.Details, fmt.Sprintf("整目录覆盖删除旧文件 %d 个：%s", removed, baseRel))
		extra["replace"] = true
		_ = models.AddWashLog(&models.WashLog{AccountID: cfg.AccountID, Action: "wash_legacy_full", TargetPath: baseRel, Title: officialTitle, MediaType: media.Category, SeasonNum: media.Season, EpisodeNum: media.Episode, TMDBID: tmdbID, OldName: "整目录", NewName: entry.Name, NewQuality: newQ.Summary(), Message: fmt.Sprintf("整目录覆盖（更优才覆盖已关闭）：删除旧文件 %d 个", removed), EventTime: time.Now()})
	}

	targetDirID, err := ensureOrganizeDirInternal(ctx, account, organizedRoot, relDir, dirCache)
	if err != nil {
		recordFailed(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "创建目标目录失败："+err.Error(), extra)
		return fmt.Errorf("创建目标目录 %s 失败：%v", relDir, err)
	}
	newName := buildAutoOrganizeNewNameEx(cfg, media.Category, officialTitle, media.Season, media.Episode, year, tmdbID, entry.Name, customWord, newQ)

	// 更优才覆盖（P0-1/P0-2/P0-3）：仅在目标影片目录已存在时比较
	// 「同片同名/同集（忽略扩展名与质量后缀）」的旧文件；新文件质量更优才覆盖，
	// 否则不做任何删除，按 loser_source_action 处置来源文件（默认保留在待整理目录）。
	if cfg.WashCompare && existingBaseID != "" {
		oldEntries, lErr := listNetDirByID(ctx, account, targetDirID)
		if lErr != nil {
			helpers.AppLogger.Warnf("洗版比较：列出目标目录失败（账号 %d）：%s：%v", cfg.AccountID, relDir, lErr)
		} else if len(oldEntries) > 0 {
			washDecision := washCompareAndApply(ctx, account, cfg, entry, media, officialTitle, year, tmdbID, relDir, newName, newQ, oldEntries)
			deferredWash = washDecision
			for _, t := range washDecision.treatments {
				result.Details = append(result.Details, t)
			}
			if !washDecision.proceed {
				msg := washDecision.skipMessage
				if msg == "" {
					msg = "新版本质量不高于现版本，跳过"
				}
				result.Details = append(result.Details, msg)
				recordSkipped(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, msg, "", extra)
				return nil
			}
		}
	}
	newName = resolveNameConflict(ctx, account, targetDirID, newName)

	// 成功收尾（移动+重命名成功后共用）：延后洗版处置、计数、明细、记录
	finishSuccess := func() {
		applyDeferredWashLosers(ctx, account, cfg, deferredWash)
		result.Organized++
		found := false
		for _, d := range result.SuccessDirs {
			if d == relDir {
				found = true
				break
			}
		}
		if !found {
			result.SuccessDirs = append(result.SuccessDirs, relDir)
		}
		result.Details = append(result.Details, fmt.Sprintf("✓ %s → %s/%s", entry.Name, relDir, newName))
		helpers.AppLogger.Infof("自动整理成功：%s → %s/%s", entry.Name, relDir, newName)
		result.Items = append(result.Items, OrganizedItem{
			Title:        officialTitle,
			Year:         year,
			Category:     strings.SplitN(relDir, "/", 2)[0],
			MediaType:    media.Category,
			Season:       media.Season,
			Episode:      media.Episode,
			Quality:      extractQualityTags(entry.Name, officialTitle),
			ReleaseGroup: extractReleaseGroup(entry.Name),
			FileName:     newName,
			SizeGB:       float64(entry.Size) / (1 << 30),
			TmdbID:       tmdbID,
		})
		recordSuccess(account, *entry, sourcePath, relDir+"/"+newName, media.Category, officialTitle, year, media.Season, media.Episode, tmdbID, newName, "整理成功", extra)
	}

	if err := moveNetdiskFileInternal(account, entry.ID, entry.ParentID, targetDirID); err != nil {
		recordFailed(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "移动失败："+err.Error(), extra)
		return fmt.Errorf("移动 %s 失败：%v", entry.Name, err)
	}
	if err := renameNetdiskFileInternal(account, entry.ID, entry.ParentID, targetDirID, newName); err != nil {
		// 同名冲突（123 列表一致性延迟导致前置比较/改名未察觉）：
		// 按用户规则比较目标目录已有文件与片源质量——更优则覆盖重试，更差/持平移入失败目录
		if strings.Contains(err.Error(), "重名文件") {
			switch handleRenameDuplicate(ctx, account, cfg, result, entry, targetDirID, newName, newQ, media, officialTitle, year, tmdbID, relDir, sourcePath, extra) {
			case "success":
				finishSuccess()
				return nil
			case "skip":
				return nil
			}
		}
		recordFailed(account, *entry, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "重命名失败："+err.Error(), extra)
		return fmt.Errorf("重命名 %s 失败：%v", entry.Name, err)
	}
	finishSuccess()
	return nil
}

// handleRenameDuplicate 重命名遇「当前目录有重名文件」时的质量比较处置。
// 此时源文件已移入目标目录（原名单独占位）。流程：
//  1. 重新列出目标目录（123 列表有一致性延迟，重试 3 次），按「同名/同集（忽略扩展名与质量后缀）」
//     匹配旧文件（排除源文件自身）；
//  2. 新文件质量更优 → 删除旧文件并重试重命名，返回 "success"；
//  3. 质量不高于现版本 → 源文件移入失败目录，返回 "skip"；
//  4. 无法匹配/删除失败/重试仍失败 → 返回 "failed"（调用方按原失败逻辑处理）。
func handleRenameDuplicate(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, entry *organizeEntry, targetDirID, newName string, newQ *FileQuality, media *IdentifyResult, officialTitle string, year int, tmdbID int64, relDir, sourcePath string, extra map[string]any) string {
	if account == nil || cfg == nil || entry == nil || newQ == nil || media == nil {
		return "failed"
	}
	var targets []organizeEntry
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "failed"
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		entries, lErr := listNetDirByID(ctx, account, targetDirID)
		if lErr != nil {
			helpers.AppLogger.Warnf("同名冲突比较：列出目标目录失败（账号 %d）：%s：%v", cfg.AccountID, relDir, lErr)
			continue
		}
		targets = targets[:0]
		for _, i := range findWashTargets(newName, newQ, entries) {
			if entries[i].ID == entry.ID {
				continue // 排除源文件自身
			}
			targets = append(targets, entries[i])
		}
		if len(targets) > 0 {
			break
		}
	}
	if len(targets) == 0 {
		return "failed"
	}
	rules := ParseWashRules(cfg.WashRulesJSON)
	groupPrio := splitGroupPriority(cfg.GroupPriority)
	newBetter := true
	for i := range targets {
		oldQ := ParseQualityFromName(targets[i].Name)
		if CompareQuality(newQ, oldQ, groupPrio, rules) <= 0 {
			newBetter = false
			break
		}
	}
	if !newBetter {
		// 质量不高于现版本：源文件（已位于目标目录）移入失败目录
		src := *entry
		src.ParentID = targetDirID
		moveEntryToFailedDir(ctx, account, cfg, &src, result, "目标已存在同名/同集文件且质量不高于现有版本")
		recordSkipped(account, src, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "同名冲突：新版本质量不高于现有版本，已移入失败目录", "", extra)
		_ = models.AddWashLog(&models.WashLog{
			AccountID:  cfg.AccountID,
			Action:     "rename_duplicate_skip",
			TargetPath: relDir,
			Title:      officialTitle,
			MediaType:  media.Category,
			SeasonNum:  media.Season,
			EpisodeNum: media.Episode,
			TMDBID:     tmdbID,
			NewName:    entry.Name,
			NewQuality: newQ.Summary(),
			Message:    fmt.Sprintf("目标已存在同名/同集文件（%s）且质量不高于现有版本，源文件移入失败目录", targets[0].Name),
			EventTime:  time.Now(),
		})
		return "skip"
	}
	// 新文件更优：删除旧文件后重试重命名
	for i := range targets {
		oldQ := ParseQualityFromName(targets[i].Name)
		if err := deleteNetdiskFileInternal(account, targets[i].ID, targets[i].ParentID); err != nil {
			helpers.AppLogger.Warnf("同名冲突覆盖：删除旧文件失败（账号 %d）：%s：%v", cfg.AccountID, targets[i].Name, err)
			return "failed"
		}
		_ = models.AddWashLog(&models.WashLog{
			AccountID:    cfg.AccountID,
			Action:       "wash_replace",
			TargetPath:   relDir,
			Title:        officialTitle,
			MediaType:    media.Category,
			SeasonNum:    media.Season,
			EpisodeNum:   media.Episode,
			TMDBID:       tmdbID,
			OldName:      targets[i].Name,
			OldQuality:   oldQ.Summary(),
			NewName:      newName,
			NewQuality:   newQ.Summary(),
			LoserTreated: "delete",
			Message:      "重命名同名冲突且新版本质量更优，已删除旧文件",
			EventTime:    time.Now(),
		})
	}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "failed"
			case <-time.After(2 * time.Second):
			}
		}
		if err := renameNetdiskFileInternal(account, entry.ID, entry.ParentID, targetDirID, newName); err == nil {
			helpers.AppLogger.Infof("同名冲突覆盖成功：%s（删除旧版本 %d 个后重命名）", newName, len(targets))
			return "success"
		}
	}
	helpers.AppLogger.Warnf("同名冲突覆盖：重试重命名仍失败（账号 %d）：%s", cfg.AccountID, newName)
	return "failed"
}

// buildAutoMedia 由文件名 + 目录级信息组装媒体信息。
// 标题/年份优先目录级（目录名通常更规范），季/集优先文件级。
func buildAutoMedia(fileName string, dirCtx *autoDirMedia) (*IdentifyResult, error) {
	cleanFileName := stripCjkDots(stripTmdbTag(fileName))
	fileCategory, fileTitle, _, fileEpisode, fileYear := mediaparse.ParseMedia(cleanFileName)
	fileParsed, hasEp := mediaparse.ParseEpisode(cleanFileName)

	media := &IdentifyResult{Category: "tv", Season: 1, Episode: 0}
	// 内嵌 TMDB ID：目录级优先，其次文件级
	if dirCtx != nil && dirCtx.TmdbId > 0 {
		media.TmdbId = dirCtx.TmdbId
	} else if id := extractTmdbIDFromName(fileName); id > 0 {
		media.TmdbId = id
	}
	// 标题：目录级优先
	if dirCtx != nil && strings.TrimSpace(dirCtx.Title) != "" {
		media.Title = dirCtx.Title
	} else {
		media.Title = strings.TrimSpace(fileTitle)
	}
	// 分类：目录级为 movie（仅年份、文件无季集）视为电影；否则按文件是否有季集判定
	if dirCtx != nil && dirCtx.Category == "movie" && !hasEp {
		media.Category = "movie"
		media.Episode = 0
	} else if hasEp || fileCategory == "tv" {
		media.Category = "tv"
	} else if fileCategory == "movie" {
		media.Category = "movie"
		media.Episode = 0
	} else {
		media.Category = "tv"
	}
	// 季/集
	media.Season = 1
	if hasEp {
		if fileParsed.Season > 0 {
			media.Season = fileParsed.Season
		}
		if fileParsed.Episode > 0 {
			media.Episode = fileParsed.Episode
		} else {
			media.Episode = fileEpisode
		}
	} else if fileEpisode > 0 {
		media.Episode = fileEpisode
	}
	if dirCtx != nil && dirCtx.Season > 0 {
		media.Season = dirCtx.Season
	}
	if media.Category == "movie" {
		media.Episode = 0
	} else if media.Episode <= 0 {
		// 电视类必须能确定集号才能命名
		return nil, errMediaUnrecognized
	}
	// 年份：目录级优先，其次文件级
	if dirCtx != nil && dirCtx.Year > 0 {
		media.Year = dirCtx.Year
	} else {
		media.Year = fileYear
	}
	return media, nil
}

// buildAutoOrganizeNewName 生成整理后的文件名，保留原始文件的质量标签（如 2160p.WEB-DL.H.265.60fps-Ocat）。
// 剧集：标题.年份.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4
// 电影：标题 (年份).2160p.WEB-DL.H.265.60fps-Ocat.mp4
func buildAutoOrganizeNewName(category, title string, season, episode, year int, origFileName string) string {
	ext := path.Ext(origFileName)
	tags := extractQualityTags(origFileName, title)
	if category == "tv" {
		if episode <= 0 {
			episode = 1
		}
		name := ""
		if year > 0 {
			name = fmt.Sprintf("%s.%d.S%02dE%02d.第%d集", title, year, season, episode, episode)
		} else {
			name = fmt.Sprintf("%s.S%02dE%02d.第%d集", title, season, episode, episode)
		}
		if tags != "" {
			name += "." + tags
		}
		return name + ext
	}
	name := ""
	if year > 0 {
		name = fmt.Sprintf("%s (%d)", title, year)
	} else {
		name = title
	}
	if tags != "" {
		name += "." + tags
	}
	return name + ext
}

var (
	autoBracketYearRe = regexp.MustCompile(`\([^()]*\d{4}[^()]*\)`)
	autoSxxExxRe      = regexp.MustCompile(`(?i)\bs\d{1,2}\s*[ex]\d{1,3}\b`)
	autoEpRe          = regexp.MustCompile(`(?i)\bep\s*\.?\s*\d{1,3}\b`)
	autoChineseEpRe   = regexp.MustCompile(`第\s*\d{1,3}\s*[集話话]`)
	autoYearRe        = regexp.MustCompile(`(19|20)\d{2}`)
	autoNxNRe         = regexp.MustCompile(`(?:^|[^a-z0-9])\d{1,2}[xX]\d{1,3}(?:$|[^a-z0-9])`)
)

// extractQualityTags 从原始文件名中提取质量标签段（标题/年份/季集之外的部分），
// 如 "花开锦绣.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4" → "2160p.WEB-DL.H.265.60fps-Ocat"。
// 在原始字符串上定位并剥离标题/季集/年份标记，保留质量标签内部的连字符与点号（如 WEB-DL、H.265、60fps-Ocat）。
func extractQualityTags(fileName, title string) string {
	stem := strings.TrimSuffix(fileName, path.Ext(fileName))
	// 1. 剥离标题：按分词顺序（允许 . _ - 空格等分隔符）在原名中定位标题区间
	t := strings.TrimSpace(title)
	if t != "" {
		if loc := titleSpanInStem(stem, t); loc[0] >= 0 {
			stem = stem[:loc[0]] + " " + stem[loc[1]:]
		}
	}
	// 2. 剥离结构标记与年份
	stem = autoBracketYearRe.ReplaceAllString(stem, " ")
	stem = autoSxxExxRe.ReplaceAllString(stem, " ")
	stem = autoEpRe.ReplaceAllString(stem, " ")
	stem = autoChineseEpRe.ReplaceAllString(stem, " ")
	stem = autoNxNRe.ReplaceAllString(stem, " ")
	stem = autoYearRe.ReplaceAllString(stem, " ")
	// 3. 归一化分隔符：空格/下划线 → 点，再按点拆分；内部的 . / - 由 join 还原（DDP5.1、WEB-DL 自洽）
	stem = strings.NewReplacer("_", ".", " ", ".").Replace(stem)
	var parts []string
	for _, p := range strings.Split(stem, ".") {
		p = strings.Trim(p, " ._()-[]")
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".")
}

// titleSpanInStem 按标题分词顺序（允许 . _ - 空格等分隔符）定位标题在原名中的区间，
// 找不到返回 [ -1, -1 ]。大小写不敏感。
func titleSpanInStem(stem, title string) [2]int {
	tokens := strings.Fields(normalizeTagToken(title))
	if len(tokens) == 0 {
		return [2]int{-1, -1}
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted = append(quoted, regexp.QuoteMeta(tok))
	}
	reSrc := strings.Join(quoted, `[._\-\s]*`)
	re := regexp.MustCompile(`(?i)` + reSrc)
	match := re.FindStringSubmatchIndex(stem)
	if match == nil {
		// 中文无分隔符标题直接整体匹配
		pos := strings.Index(strings.ToLower(stem), strings.ToLower(title))
		if pos >= 0 {
			return [2]int{pos, pos + len(title)}
		}
		return [2]int{-1, -1}
	}
	return [2]int{match[0], match[1]}
}

func normalizeTagToken(s string) string {
	repl := strings.NewReplacer(".", " ", "_", " ", "-", " ", "·", " ", "(", " ", ")", " ", "[", " ", "]", " ")
	return strings.Join(strings.Fields(repl.Replace(s)), " ")
}

// resolveNameConflict 目标目录下已存在同名文件（忽略扩展名）时追加序号，避免移动后重命名冲突
func resolveNameConflict(ctx context.Context, account *models.Account, targetDirID, newName string) string {
	if targetDirID == "" {
		return newName
	}
	stem := strings.TrimSuffix(newName, path.Ext(newName))
	ext := path.Ext(newName)
	files, err := listNetDirByID(ctx, account, targetDirID)
	if err != nil {
		return newName
	}
	taken := make(map[string]bool)
	for i := range files {
		if files[i].IsDir {
			continue
		}
		taken[strings.TrimSuffix(files[i].Name, path.Ext(files[i].Name))] = true
	}
	name := stem
	n := 2
	for taken[name] {
		name = fmt.Sprintf("%s (%d)", stem, n)
		n++
	}
	return name + ext
}

// moveEntryToFailedDir 把识别失败的资源（文件或目录）整体移入失败目录（用户手动设定）。
// 失败目录为空时不移动（原地保留，仅记录），避免擅自改变用户目录结构。
func moveEntryToFailedDir(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, entry *organizeEntry, result *AutoOrganizeResult, reason string) {
	helpers.AppLogger.Warnf("自动整理识别失败（账号 %d）：%s（%s）", cfg.AccountID, entry.Name, reason)
	failedDir := strings.Trim(cfg.FailedDir, "/")
	if failedDir == "" {
		result.Details = append(result.Details, fmt.Sprintf("[识别失败] %s（%s，失败目录未配置，原地保留）", entry.Name, reason))
		return
	}
	if err := ctx.Err(); err != nil {
		result.Details = append(result.Details, fmt.Sprintf("[识别失败] %s（%s，上下文取消无法移动）", entry.Name, reason))
		return
	}
	// 失败目录由用户显式配置，不存在时自动创建。
	// 部分网盘创建接口返回的 ID 不可靠（123 空目录返回 0），创建后重新按路径解析真实 ID，避免移动时报「请输入ParentFileId」
	targetID, err := EnsureRemoteDir(ctx, account, failedDir)
	if err == nil && (targetID == "" || targetID == "0") {
		if id, fErr := findRemoteDirID(ctx, account, failedDir); fErr == nil && id != "" && id != "0" {
			targetID = id
		}
	}
	helpers.AppLogger.Infof("自动整理失败目录解析（账号 %d）：%s → targetID=%q err=%v", cfg.AccountID, failedDir, targetID, err)
	if err != nil {
		helpers.AppLogger.Warnf("自动整理移入失败目录失败（账号 %d）：%s → %s：%v", cfg.AccountID, entry.Name, failedDir, err)
		result.Details = append(result.Details, fmt.Sprintf("[识别失败] %s 移入失败目录失败：%v", entry.Name, err))
		return
	}
	if err := moveNetdiskFileInternal(account, entry.ID, entry.ParentID, targetID); err != nil {
		helpers.AppLogger.Warnf("自动整理移动失败（账号 %d）：%s → %s：%v", cfg.AccountID, entry.Name, failedDir, err)
		result.Details = append(result.Details, fmt.Sprintf("[识别失败] 移动 %s 到失败目录失败：%v", entry.Name, err))
		return
	}
	// 失败目录下同名冲突时追加序号（移动后 ID 不变，重命名仍有效）
	newName := resolveNameConflict(ctx, account, targetID, entry.Name)
	if newName != entry.Name {
		_ = renameNetdiskFileInternal(account, entry.ID, entry.ParentID, targetID, newName)
	}
	result.MovedToFailed++
	result.FailedNames = append(result.FailedNames, entry.Name)
	result.Details = append(result.Details, fmt.Sprintf("[识别失败] %s 已移入失败目录（%s）", entry.Name, reason))
}

// finishAutoOrganizeResult 汇总并持久化运行结果
func finishAutoOrganizeResult(cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult) {
	data, err := json.Marshal(result)
	if err == nil {
		models.UpdateAutoOrganizeLastRun(cfg.ID, string(data))
	}
	// 明细逐条写日志，便于在日志页/通知中定位失败原因
	for _, d := range result.Details {
		helpers.AppLogger.Infof("自动整理明细（账号 %d）：%s", cfg.AccountID, d)
	}
	if result.Organized+result.Unrecognized+result.MovedToFailed+result.Failed > 0 {
		sendAutoOrganizeNotify(cfg, result)
	}
	// 整理成功目录联动 STRM 同步（与 MP 订阅上传链路同语义）：配置了 STRM 输出目录才触发
	if result.Organized > 0 && strings.TrimSpace(cfg.StrmLocalDir) != "" {
		triggerStrmSyncAfterAutoOrganize(cfg, result)
	}
}

// triggerStrmSyncAfterAutoOrganize 对本轮整理成功的目录逐个触发手动 STRM 同步任务
func triggerStrmSyncAfterAutoOrganize(cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult) {
	account, err := models.GetAccountById(cfg.AccountID)
	if err != nil || account == nil {
		helpers.AppLogger.Warnf("STRM 联动：账号 %d 加载失败，跳过：%v", cfg.AccountID, err)
		return
	}
	organizedRoot := strings.Trim(cfg.OrganizedRoot, "/")
	if organizedRoot == "" {
		organizedRoot = organizeRootPath(strings.Trim(cfg.PendingDir, "/"))
	}
	for _, dir := range result.SuccessDirs {
		sourcePath := strings.TrimRight(organizedRoot, "/") + "/" + dir
		TriggerStrmSyncForDir(account, sourcePath, cfg.StrmLocalDir)
	}
}

func sendAutoOrganizeNotify(cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult) {
	account, _ := models.GetAccountById(cfg.AccountID)
	accountName := "?"
	if account != nil {
		accountName = account.Username
	}
	title := "📦 云盘自动整理完成"
	lines := []string{
		fmt.Sprintf("账号：%s（待整理目录：%s）", accountName, cfg.PendingDir),
		fmt.Sprintf("整理成功：%d 个文件", result.Organized),
	}
	if result.Unrecognized > 0 {
		lines = append(lines, fmt.Sprintf("识别失败：%d 个（已移入失败目录）", result.Unrecognized))
	}
	if result.MovedToFailed > 0 {
		lines = append(lines, fmt.Sprintf("整体移入失败目录：%d 个", result.MovedToFailed))
	}
	if result.SkippedOverwrite > 0 {
		lines = append(lines, fmt.Sprintf("目标已存在跳过（非洗版）：%d 个", result.SkippedOverwrite))
	}
	if result.Failed > 0 {
		lines = append(lines, fmt.Sprintf("整理失败：%d 个", result.Failed))
	}
	if result.DeletedEmptySrc > 0 {
		lines = append(lines, fmt.Sprintf("清理空源目录：%d 个", result.DeletedEmptySrc))
	}

	// tgto123 风格：按影视条目分组展示（同一部影片/季合并为一张卡片）
	if len(result.Items) > 0 {
		lines = append(lines, "", "━━ 新片入库 ━━")
		type itemGroup struct {
			item     OrganizedItem
			episodes []int
			files    int
			sizeGB   float64
		}
		groups := make([]*itemGroup, 0, len(result.Items))
		index := make(map[string]*itemGroup)
		for _, it := range result.Items {
			key := fmt.Sprintf("%d:%d", it.TmdbID, it.Season)
			g, ok := index[key]
			if !ok {
				g = &itemGroup{item: it}
				index[key] = g
				groups = append(groups, g)
			}
			g.episodes = append(g.episodes, it.Episode)
			g.files++
			g.sizeGB += it.SizeGB
			if it.Quality != "" && g.item.Quality == "" {
				g.item.Quality = it.Quality
			}
		}
		const maxGroups = 20
		for i, g := range groups {
			if i >= maxGroups {
				lines = append(lines, fmt.Sprintf("…其余 %d 部影片详见系统日志", len(groups)-maxGroups))
				break
			}
			it := g.item
			var card []string
			card = append(card, fmt.Sprintf("🎬 新片入库：%s (%d)", it.Title, it.Year))
			if sourceName := accountDisplayName(account); sourceName != "" {
				card = append(card, fmt.Sprintf("📡 来源：%s", sourceName))
			}
			if it.Category != "" {
				card = append(card, fmt.Sprintf("📂 分类：%s", it.Category))
			}
			if it.Quality != "" {
				card = append(card, fmt.Sprintf("🎞 版本：%s", it.Quality))
			}
			if it.MediaType == "tv" && len(g.episodes) > 0 {
				line := fmt.Sprintf("📺 本季：S%02d", it.Season)
				if r := formatEpisodeRanges(g.episodes); r != "" {
					line += " · " + r
				}
				line += fmt.Sprintf("（%d 个文件 · %.2f GB）", g.files, g.sizeGB)
				card = append(card, line)
			} else if it.FileName != "" {
				card = append(card, fmt.Sprintf("📄 文件：%s（%.2f GB）", it.FileName, g.sizeGB))
			}
			if it.TmdbID > 0 {
				card = append(card, fmt.Sprintf("🆔 影号：TMDB %d", it.TmdbID))
			}
			if it.ReleaseGroup != "" {
				card = append(card, fmt.Sprintf("🏷 组名：%s", it.ReleaseGroup))
			}
			card = append(card, "又有新片可以看了，快来探索吧🎉")
			lines = append(lines, strings.Join(card, "\n"))
		}
	}

	lines = append(lines, fmt.Sprintf("⏰ 时间：%s", time.Now().Format("2006-01-02 15:04:05")))
	sendSystemNotification(title, strings.Join(lines, "\n"))
}

// findRemoteDirID 只查找不创建：按路径解析目录 ID（或路径语义字符串）。
// path 为空或 / 时返回空串（根目录语义，与 EnsureRemoteDir 一致）。
func findRemoteDirID(ctx context.Context, account *models.Account, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", nil
	}
	switch account.SourceType {
	case models.SourceType123:
		client := account.Get123Client()
		return client.GetPathIdByPath(ctx, path)
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		id, err := client.GetPathIdByPath(ctx, path)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("目录 %s 不存在", path)
		}
		return id, nil
	case models.SourceType115:
		client := account.Get115Client()
		cur := ""
		for _, part := range strings.Split(path, "/") {
			if part == "" {
				continue
			}
			resp, err := client.GetFsList(ctx, cur, true, true, false, 0, 100)
			if err != nil {
				return "", err
			}
			found := false
			for i := range resp.Data {
				if resp.Data[i].FileCategory == "0" && resp.Data[i].FileName == part {
					cur = resp.Data[i].FileId
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("目录 %s 不存在", path)
			}
		}
		return cur, nil
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return "", fmt.Errorf("获取中国移动云盘客户端失败")
		}
		cur := ""
		for _, part := range strings.Split(path, "/") {
			if part == "" {
				continue
			}
			files, err := client.GetFiles(ctx, cur)
			if err != nil {
				return "", err
			}
			found := false
			for i := range files {
				if files[i].Type == "folder" && files[i].FileName == part {
					cur = files[i].GetID()
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("目录 %s 不存在", path)
			}
		}
		return cur, nil
	default:
		return "", fmt.Errorf("该网盘类型暂不支持自动整理：%s", account.SourceType)
	}
}

// deleteNetdiskFileInternal 按账号类型删除网盘文件/目录（覆盖洗版用）
func deleteNetdiskFileInternal(account *models.Account, fileID, parentID string) error {
	ctx := context.Background()
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err := client.Del(ctx, []string{fileID}, parentID)
		return err
	case models.SourceType123:
		client := account.Get123Client()
		return client.Delete(ctx, []string{fileID})
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("获取中国移动云盘客户端失败")
		}
		return client.Delete(ctx, []string{fileID})
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		return client.Delete(ctx, []string{fileID})
	default:
		return fmt.Errorf("该网盘类型暂不支持删除")
	}
}

// deleteVideosUnderDir 递归删除目标影片目录下的所有视频文件（洗版），
// 删除后为空的目录一并清理；返回删除的视频文件数。
func deleteVideosUnderDir(ctx context.Context, account *models.Account, dirID string) int {
	if dirID == "" {
		return 0
	}
	removed := 0
	entries, err := listNetDirByID(ctx, account, dirID)
	if err != nil {
		return 0
	}
	for i := range entries {
		if entries[i].IsDir {
			removed += deleteVideosUnderDir(ctx, account, entries[i].ID)
			continue
		}
		if !mediaparse.IsVideoExt(entries[i].Name) {
			continue
		}
		if err := deleteNetdiskFileInternal(account, entries[i].ID, entries[i].ParentID); err == nil {
			removed++
		}
	}
	left, err := listNetDirByID(ctx, account, dirID)
	if err == nil && len(left) == 0 {
		_ = deleteNetdiskFileInternal(account, dirID, "")
	}
	return removed
}

// sendSystemNotification 通用系统通知（复用全局通知管理器）
func sendSystemNotification(title, content string) {
	notif := &models.Notification{
		Type:      models.SystemAlert,
		Title:     title,
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
			helpers.AppLogger.Warnf("发送自动整理通知失败：%v", err)
		}
	}
}
