package controllers

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// organizeVideoExts 目录整理识别的视频扩展名（与 STRM 默认视频扩展名一致）
var organizeVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".webm": true, ".m4v": true, ".3gp": true, ".ts": true,
}

var (
	organizeYearRe = regexp.MustCompile(`(19|20)\d{2}`)
	organizeMiscRe = regexp.MustCompile(`(?i)(1080p|720p|2160p|4k|bluray|blu-ray|web-?dl|webrip|hdr|hdr10|dolby|atmos|ddp?|5\.1|7\.1|ac3|aac|hevc|h\.?26[45]|x26[45]|xvid|chd|chdw|hd|sdr|repack|proper|extended|remux|uhd)`)
)

// organizeEntry 扫描到的文件或目录
type organizeEntry struct {
	FileID   string
	Name     string
	IsDir    bool
	ParentID string
	FullPath string // 展示用完整路径（OpenList/百度为真实路径，其余为名称拼接）
}

// organizeAction 规划动作
type organizeAction struct {
	FileID        string `json:"file_id"`
	OldName       string `json:"old_name"`
	NewName       string `json:"new_name"`
	Category      string `json:"category"` // movie / tv / unknown
	Title         string `json:"title"`
	Season        int    `json:"season,omitempty"`
	Episode       int    `json:"episode,omitempty"`
	Year          int    `json:"year,omitempty"`
	TargetRelPath string `json:"target_rel_path"`
	Supported     bool   `json:"supported"` // 该网盘是否支持移动
}

const (
	organizeMaxFiles = 1000
	organizeMaxDepth = 3
)

// listAllNetDir 拉取目录完整列表（循环分页，不走缓存）
func listAllNetDir(ctx context.Context, account *models.Account, parentID string) ([]*FileItem, error) {
	capability, err := getNetFileSourceCapability(account.SourceType, "name", "asc")
	if err != nil {
		return nil, err
	}
	batchSize := capability.BatchSize
	var all []*FileItem
	start := 0
	for {
		batch, err := fetchNetFileBatch(ctx, account, parentID, start, batchSize, "name", "asc", true)
		if err != nil {
			return nil, err
		}
		all = append(all, batch.Items...)
		if !batch.HasMore || len(batch.Items) == 0 {
			break
		}
		start += len(batch.Items)
	}
	return all, nil
}

func collectOrganizeEntries(ctx context.Context, account *models.Account, parentID string, parentPath string, depth int, maxDepth int, counter *int) ([]organizeEntry, error) {
	if depth > maxDepth || *counter > organizeMaxFiles {
		return nil, nil
	}
	files, err := listAllNetDir(ctx, account, parentID)
	if err != nil {
		return nil, err
	}
	var entries []organizeEntry
	for _, file := range files {
		*counter++
		if *counter > organizeMaxFiles {
			break
		}
		fullPath := parentPath + "/" + file.Name
		if parentPath == "" {
			fullPath = file.Name
		}
		entries = append(entries, organizeEntry{
			FileID:   file.Id,
			Name:     file.Name,
			IsDir:    file.IsDirectory,
			ParentID: parentID,
			FullPath: fullPath,
		})
		if file.IsDirectory {
			sub, err := collectOrganizeEntries(ctx, account, file.Id, fullPath, depth+1, maxDepth, counter)
			if err != nil {
				return nil, err
			}
			entries = append(entries, sub...)
		}
	}
	return entries, nil
}

func isOrganizeVideo(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return organizeVideoExts[ext]
}

// parseOrganizeMedia 解析文件名归类：返回分类、标题、季、集、年份。
// movie：含年份；tv：含季集信息；unknown：其他。
func parseOrganizeMedia(name string) (category string, title string, season int, episode int, year int) {
	stem := strings.TrimSuffix(name, path.Ext(name))
	if parsed, ok := parseNameAlignEpisode(stem); ok {
		title = cleanMediaTitle(parsed.Title)
		season = parsed.Season
		episode = parsed.Episode
		if season == 0 {
			season = 1
		}
		if m := organizeYearRe.FindString(title); m != "" {
			year, _ = strconv.Atoi(m)
			title = strings.TrimSpace(strings.Replace(title, m, "", 1))
		}
		return "tv", title, season, episode, year
	}
	title = cleanMediaTitle(stem)
	title = organizeMiscRe.ReplaceAllString(title, " ")
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	if m := organizeYearRe.FindString(stem); m != "" {
		year, _ = strconv.Atoi(m)
		title = cleanMediaTitle(strings.Replace(stem, m, "", 1))
		title = organizeMiscRe.ReplaceAllString(title, " ")
		title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
		return "movie", title, 0, 0, year
	}
	return "unknown", title, 0, 0, 0
}

// buildOrganizeTargetRelPath 构建目标相对路径（相对整理根目录）
func buildOrganizeTargetRelPath(category string, title string, season int, year int) (string, bool) {
	if category == "movie" {
		if title == "" {
			return "", false
		}
		if year > 0 {
			return fmt.Sprintf("电影/%s (%d)", title, year), true
		}
		return fmt.Sprintf("电影/%s", title), true
	}
	if category == "tv" {
		if title == "" {
			return "", false
		}
		base := fmt.Sprintf("剧集/%s", title)
		if year > 0 {
			base = fmt.Sprintf("剧集/%s (%d)", title, year)
		}
		return fmt.Sprintf("%s/Season %02d", base, season), true
	}
	return "", false
}

// OrganizePreviewRequest 目录整理预览请求
type OrganizePreviewRequest struct {
	AccountID  uint   `json:"account_id" binding:"required"`
	Path       string `json:"path" binding:"required"` // 源目录
	TargetPath string `json:"target_path"`             // 整理根目录，默认源目录
	Depth      int    `json:"depth"`                   // 扫描深度 1-3，默认 2
}

// OrganizePreview 目录整理预览：扫描目录并规划整理动作。
func OrganizePreview(c *gin.Context) {
	var req OrganizePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.Depth <= 0 || req.Depth > organizeMaxDepth {
		req.Depth = 2
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	switch account.SourceType {
	case models.SourceTypeGuangYaPan:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该网盘暂不支持目录整理（不支持移动）", Data: nil})
		return
	}

	counter := 0
	entries, err := collectOrganizeEntries(c.Request.Context(), account, req.Path, "", 1, req.Depth, &counter)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "扫描目录失败：" + err.Error(), Data: nil})
		return
	}

	type organizeGroup struct {
		category string
		title    string
		season   int
		year     int
		actions  []*organizeAction
	}
	groups := map[string]*organizeGroup{}
	var actions []*organizeAction
	var skipped []string
	for _, entry := range entries {
		if entry.IsDir || !isOrganizeVideo(entry.Name) {
			continue
		}
		category, title, season, episode, year := parseOrganizeMedia(entry.Name)
		action := &organizeAction{
			FileID:    entry.FileID,
			OldName:   entry.Name,
			Category:  category,
			Title:     title,
			Season:    season,
			Episode:   episode,
			Year:      year,
			Supported: true,
		}
		relPath, ok := buildOrganizeTargetRelPath(category, title, season, year)
		if !ok {
			skipped = append(skipped, entry.Name)
			continue
		}
		action.TargetRelPath = relPath
		// 规范化文件名
		ext := path.Ext(entry.Name)
		if category == "tv" {
			action.NewName = fmt.Sprintf("%s S%02dE%02d%s", title, season, episode, ext)
		} else if category == "movie" {
			if year > 0 {
				action.NewName = fmt.Sprintf("%s (%d)%s", title, year, ext)
			} else {
				action.NewName = title + ext
			}
		} else {
			action.NewName = entry.Name
		}
		groupKey := relPath
		if _, exists := groups[groupKey]; !exists {
			groups[groupKey] = &organizeGroup{
				category: category,
				title:    title,
				season:   season,
				year:     year,
			}
		}
		groups[groupKey].actions = append(groups[groupKey].actions, action)
		actions = append(actions, action)
	}

	// 汇总统计
	type organizeGroupSummary struct {
		Category   string `json:"category"`
		Title      string `json:"title"`
		Season     int    `json:"season"`
		Year       int    `json:"year"`
		RelPath    string `json:"rel_path"`
		FileCount  int    `json:"file_count"`
		EpisodeMin int    `json:"episode_min"`
		EpisodeMax int    `json:"episode_max"`
	}
	var summaries []organizeGroupSummary
	for _, g := range groups {
		summary := organizeGroupSummary{
			Category:  g.category,
			Title:     g.title,
			Season:    g.season,
			Year:      g.year,
			RelPath:   buildGroupRelPath(g.category, g.title, g.season, g.year),
			FileCount: len(g.actions),
		}
		for _, a := range g.actions {
			if summary.EpisodeMin == 0 || a.Episode < summary.EpisodeMin {
				summary.EpisodeMin = a.Episode
			}
			if a.Episode > summary.EpisodeMax {
				summary.EpisodeMax = a.Episode
			}
		}
		summaries = append(summaries, summary)
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "扫描完成", Data: map[string]any{
		"actions": actions,
		"groups":  summaries,
		"skipped": skipped,
		"scanned": counter,
		"total":   len(actions),
	}})
}

func buildGroupRelPath(category string, title string, season int, year int) string {
	rel, _ := buildOrganizeTargetRelPath(category, title, season, year)
	return rel
}

// OrganizeApplyRequest 目录整理执行请求
type OrganizeApplyRequest struct {
	AccountID  uint                `json:"account_id" binding:"required"`
	Path       string              `json:"path" binding:"required"` // 源目录
	TargetPath string              `json:"target_path"`             // 整理根目录，默认源目录
	Items      []organizeApplyItem `json:"items" binding:"required"`
}

type organizeApplyItem struct {
	FileID  string `json:"file_id" binding:"required"`
	NewName string `json:"new_name" binding:"required"`
	RelPath string `json:"rel_path" binding:"required"` // 相对整理根目录的目标目录，如 电影/蜘蛛侠 (2025)
}

type organizeApplyResult struct {
	Success []organizeApplyItem   `json:"success"`
	Failed  []organizeApplyFailed `json:"failed"`
}

type organizeApplyFailed struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// OrganizeApply 目录整理执行：建目录 + 移动 + 重命名。
func OrganizeApply(c *gin.Context) {
	var req OrganizeApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if len(req.Items) > 500 {
		req.Items = req.Items[:500]
	}

	result := organizeApplyResult{
		Success: make([]organizeApplyItem, 0, len(req.Items)),
		Failed:  make([]organizeApplyFailed, 0),
	}
	dirCache := map[string]string{} // relPath -> 目录 ID
	ctx := context.Background()
	for _, item := range req.Items {
		// 1. 确保目标目录存在
		targetDirID, err := ensureOrganizeDir(ctx, account, req.Path, req.TargetPath, item.RelPath, dirCache)
		if err != nil {
			result.Failed = append(result.Failed, organizeApplyFailed{FileID: item.FileID, Name: item.RelPath, Reason: "创建目标目录失败：" + err.Error()})
			continue
		}
		// 2. 移动
		if err := moveNetdiskFile(account, item.FileID, targetDirID); err != nil {
			result.Failed = append(result.Failed, organizeApplyFailed{FileID: item.FileID, Name: item.RelPath, Reason: "移动失败：" + err.Error()})
			continue
		}
		// 3. 重命名
		if err := renameNetdiskFile(account, item.FileID, item.NewName); err != nil {
			result.Failed = append(result.Failed, organizeApplyFailed{FileID: item.FileID, Name: item.RelPath, Reason: "重命名失败：" + err.Error()})
			continue
		}
		result.Success = append(result.Success, item)
	}
	invalidateNetFileCacheForDeletedPath(account.SourceType, req.AccountID, req.Path, "")
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("整理完成：成功 %d 个，失败 %d 个", len(result.Success), len(result.Failed)), Data: result})
}

// ensureOrganizeDir 确保目标目录存在，返回目录 ID（或路径语义）。
func ensureOrganizeDir(ctx context.Context, account *models.Account, sourcePath string, targetPath string, relPath string, dirCache map[string]string) (string, error) {
	if cached, ok := dirCache[relPath]; ok {
		return cached, nil
	}
	parts := strings.Split(relPath, "/")
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		currentID := targetPath
		if currentID == "" {
			currentID = sourcePath
		}
		built := ""
		for _, part := range parts {
			built = built + "/" + part
			if id, ok := dirCache[built]; ok {
				currentID = id
				continue
			}
			id, err := client.MkDir(ctx, currentID, part)
			if err != nil {
				return "", err
			}
			dirCache[built] = id
			currentID = id
		}
		dirCache[relPath] = currentID
		return currentID, nil
	case models.SourceType123:
		client := account.Get123Client()
		currentID := targetPath
		if currentID == "" {
			currentID = sourcePath
		}
		built := ""
		for _, part := range parts {
			built = built + "/" + part
			if id, ok := dirCache[built]; ok {
				currentID = id
				continue
			}
			id, err := client.CreateDir(ctx, currentID, part)
			if err != nil {
				return "", err
			}
			dirCache[built] = id
			currentID = id
		}
		dirCache[relPath] = currentID
		return currentID, nil
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return "", fmt.Errorf("获取中国移动云盘客户端失败")
		}
		currentID := targetPath
		if currentID == "" {
			currentID = sourcePath
		}
		built := ""
		for _, part := range parts {
			built = built + "/" + part
			if id, ok := dirCache[built]; ok {
				currentID = id
				continue
			}
			id, err := client.CreateDir(ctx, currentID, part)
			if err != nil {
				return "", err
			}
			dirCache[built] = id
			currentID = id
		}
		dirCache[relPath] = currentID
		return currentID, nil
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		base := targetPath
		if base == "" {
			base = sourcePath
		}
		base = strings.TrimRight(normalizeOpenListPath(base), "/")
		built := ""
		for _, part := range parts {
			built = built + "/" + part
			fullPath := base + built
			if _, ok := dirCache[built]; ok {
				continue
			}
			if err := client.Mkdir(ctx, fullPath); err != nil {
				// 已存在或重名时忽略，继续
				helpers.AppLogger.Warnf("百度网盘创建目录失败（可能已存在）：%s，%v", fullPath, err)
			}
			dirCache[built] = fullPath
		}
		dirCache[relPath] = base + "/" + relPath
		return base + "/" + relPath, nil
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		base := targetPath
		if base == "" {
			base = sourcePath
		}
		base = strings.TrimRight(normalizeOpenListPath(base), "/")
		built := ""
		for _, part := range parts {
			built = built + "/" + part
			fullPath := base + built
			if _, ok := dirCache[built]; ok {
				continue
			}
			if err := client.Mkdir(fullPath); err != nil {
				helpers.AppLogger.Warnf("OpenList 创建目录失败（可能已存在）：%s，%v", fullPath, err)
			}
			dirCache[built] = fullPath
		}
		dirCache[relPath] = base + "/" + relPath
		return base + "/" + relPath, nil
	default:
		return "", fmt.Errorf("不支持的网盘类型")
	}
}
