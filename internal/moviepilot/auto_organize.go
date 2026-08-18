package moviepilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
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
	AccountID        uint     `json:"account_id"`
	Organized        int      `json:"organized"`         // 成功整理（移动+重命名）的视频数
	Failed           int      `json:"failed"`            // 整理失败（流程错误）数
	Unrecognized     int      `json:"unrecognized"`      // 识别失败/TMDB 查不到，已移入失败目录数
	SkippedOverwrite int      `json:"skipped_overwrite"` // 目标已存在同片且非洗版模式，跳过数
	NonMedia         int      `json:"non_media"`         // 非视频条目跳过数
	DeletedEmptySrc  int      `json:"deleted_empty_src"` // 整理后清空的源目录删除数
	MovedToFailed    int      `json:"moved_to_failed"`   // 整体移入失败目录的资源数（目录/文件）
	SuccessDirs      []string `json:"success_dirs"`      // 整理成功的目标相对目录（相对已整理根目录）
	FailedNames      []string `json:"failed_names"`      // 移入失败目录/处理失败的资源名
	Details          []string `json:"details"`           // 明细（前端展示/日志）
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
			processAutoOrganizeDir(ctx, account, cfg, result, &e, organizedRoot, &rules, dirCache, &aiBudget)
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

// processAutoOrganizeDir 整理一个顶层目录资源（转存分享树根目录）。
// 收集目录内视频文件逐个整理；整理结束后空目录删除、有残留则整体移入失败目录。
func processAutoOrganizeDir(ctx context.Context, account *models.Account, cfg *models.AutoOrganizeConfig, result *AutoOrganizeResult, dir *organizeEntry, organizedRoot string, rules *categoryRules, dirCache map[string]string, aiBudget *int) {
	// 目录名解析（标题/年份优先从目录名取，季集优先从文件名取）；
	// 先剥离目录名中内嵌的 TMDB 标记（{tmdbid-xxx}），避免污染标题搜索
	cleanDirName := stripTmdbTag(dir.Name)
	dirCategory, dirTitle, dirSeason, _, dirYear := mediaparse.ParseMedia(cleanDirName)

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
		result.Details = append(result.Details, fmt.Sprintf("目录 %s 内无视频文件，跳过", dir.Name))
		return
	}

	dirCtx := &autoDirMedia{
		Category: dirCategory,
		Title:    dirTitle,
		Season:   dirSeason,
		Year:     dirYear,
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
				moveEntryToFailedDir(ctx, account, cfg, dir, result, fmt.Sprintf("目录 %s 内文件识别失败：%v", dir.Name, err))
				return
			}
			result.Failed++
			result.FailedNames = append(result.FailedNames, v.Name)
			result.Details = append(result.Details, fmt.Sprintf("整理失败 %s：%v", v.Name, err))
		}
	}

	// 收尾：源目录已空则删除；有残留则整体移入失败目录（不丢数据）
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
	media, err := buildAutoMedia(entry.Name, dirCtx)
	if err != nil {
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
		return errMediaUnrecognized
	}

	officialTitle, tmdbID, tmdbYear, categoryName, err := lookupTmdbMediaWithRules(ctx, media, *rules)
	if err != nil {
		return fmt.Errorf("%w：%v", errMediaUnrecognized, err)
	}
	year := tmdbYear
	if year <= 0 {
		year = media.Year
	}
	relDir, ok := buildOrganizeRelDir(media.Category, officialTitle, year, media.Season, tmdbID, categoryName)
	if !ok {
		return fmt.Errorf("%w：媒体信息不完整", errMediaUnrecognized)
	}

	// 重复/洗版检测：目标影片目录（不含 Season 段）已存在
	baseRel := relDir
	if idx := strings.Index(baseRel, "/Season "); idx >= 0 {
		baseRel = baseRel[:idx]
	}
	baseFull := strings.TrimRight(organizedRoot, "/") + "/" + baseRel
	existingBaseID, findErr := findRemoteDirID(ctx, account, baseFull)
	if findErr == nil && existingBaseID != "" {
		if !cfg.Overwrite {
			result.SkippedOverwrite++
			result.Details = append(result.Details, fmt.Sprintf("目标已存在且非洗版模式，跳过：%s → %s", entry.Name, baseRel))
			return nil
		}
		removed := deleteVideosUnderDir(ctx, account, existingBaseID)
		result.Details = append(result.Details, fmt.Sprintf("洗版删除旧文件 %d 个：%s", removed, baseRel))
	}

	targetDirID, err := ensureOrganizeDirInternal(ctx, account, organizedRoot, relDir, dirCache)
	if err != nil {
		return fmt.Errorf("创建目标目录 %s 失败：%v", relDir, err)
	}
	newName := buildAutoOrganizeNewName(media.Category, officialTitle, media.Season, media.Episode, year, entry.Name)
	newName = resolveNameConflict(ctx, account, targetDirID, newName)

	if err := moveNetdiskFileInternal(account, entry.ID, entry.ParentID, targetDirID); err != nil {
		return fmt.Errorf("移动 %s 失败：%v", entry.Name, err)
	}
	if err := renameNetdiskFileInternal(account, entry.ID, entry.ParentID, targetDirID, newName); err != nil {
		return fmt.Errorf("重命名 %s 失败：%v", entry.Name, err)
	}
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
	return nil
}

// buildAutoMedia 由文件名 + 目录级信息组装媒体信息。
// 标题/年份优先目录级（目录名通常更规范），季/集优先文件级。
func buildAutoMedia(fileName string, dirCtx *autoDirMedia) (*IdentifyResult, error) {
	cleanFileName := stripTmdbTag(fileName)
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
	if len(result.Details) > 0 {
		lines = append(lines, "── 明细 ──")
		const maxDetails = 40
		if len(result.Details) > maxDetails {
			lines = append(lines, result.Details[:maxDetails]...)
			lines = append(lines, fmt.Sprintf("…共 %d 条，其余详见系统日志", len(result.Details)))
		} else {
			lines = append(lines, result.Details...)
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
