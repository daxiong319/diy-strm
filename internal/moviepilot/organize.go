package moviepilot

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"diy-strm/internal/baidupan"
	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
)

// organizeMediaResult 网盘端整理结果
type organizeMediaResult struct {
	Organized    int      // 整理成功（移动+重命名）的视频数
	Failed       int      // 整理失败数
	Unrecognized int      // 文件名无法识别数（不移动，保留原目录）
	FailedNames  []string // 失败/无法识别的文件名（前端展示）
	SuccessDirs  []string // 整理成功的目标相对目录（相对整理根目录），用于 STRM 同步
}

// organizeEntry 网盘目录项
type organizeEntry struct {
	ID       string
	Name     string
	ParentID string
	IsDir    bool
}

// errMediaUnrecognized 文件名无法识别（正则与 AI 均未命中）
var errMediaUnrecognized = errors.New("文件名无法识别")

// aiTryBudget 每个整理任务最多尝试 AI 识别的文件数，避免批量文件逐个调用 AI
const aiTryBudget = 20

// aiConsecutiveFailStop 连续 AI 识别失败次数达到该值时停止后续 AI 尝试
const aiConsecutiveFailStop = 3

const (
	organizeMaxEntries = 500
	organizeMaxDepth   = 4
)

// organizeUploadedDir 对上传完成的网盘目录执行 TMDB 校验与分类整理：
// 建目标目录（已整理/{分类}/{标题 (年份) {tmdb=xxx}}[/Season NN]）→ 移动 → 重命名；
// 只处理视频文件，TMDB 校验失败或无法识别的不移动。
// rootID 为整理根目录 ID（或路径语义字符串），rootPath 为整理根目录路径，
// organizeRoot 为已整理根目录路径（如 影视/已整理）。
// task 非空时，识别失败的文件会尝试 AI 兜底并记录到 movie_pilot_failed_files 供手动确认。
func organizeUploadedDir(ctx context.Context, account *models.Account, rootID, rootPath, organizeRoot string, task *models.MoviePilotUploadTask) *organizeMediaResult {
	result := &organizeMediaResult{}
	var entries []organizeEntry
	counter := 0
	if err := collectOrganizeEntries(ctx, account, rootID, &entries, &counter, 0); err != nil {
		helpers.AppLogger.Errorf("MoviePilot 整理扫描目录 %s 失败：%v", rootPath, err)
		return result
	}

	dirIDCache := map[string]string{} // relPath -> 目录 ID
	aiBudget := aiTryBudget
	aiConsecutiveFail := 0
	for _, e := range entries {
		if e.IsDir || !mediaparse.IsVideoExt(e.Name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			break
		}
		category, title, season, episode, year := mediaparse.ParseMedia(e.Name)
		media := &IdentifyResult{Category: category, Title: title, Season: season, Episode: episode, Year: year}
		dir, err := organizeOneFile(ctx, account, e, organizeRoot, dirIDCache, media)
		if err == errMediaUnrecognized && aiBudget > 0 {
			// 正则识别失败：AI 兜底
			aiBudget--
			if ai, ok := IdentifyFileWithAI(ctx, e.Name); ok {
				aiConsecutiveFail = 0
				dir, err = organizeOneFile(ctx, account, e, organizeRoot, dirIDCache, &ai)
			} else if aiConsecutiveFail++; aiConsecutiveFail >= aiConsecutiveFailStop {
				aiBudget = 0
			}
		}
		if err != nil {
			if errors.Is(err, errMediaUnrecognized) {
				result.Unrecognized++
			} else {
				result.Failed++
			}
			result.FailedNames = append(result.FailedNames, e.Name)
			helpers.AppLogger.Warnf("MoviePilot 整理 %s 失败：%v", e.Name, err)
			saveFailedFile(task, account, e, err)
			continue
		}
		result.Organized++
		found := false
		for _, d := range result.SuccessDirs {
			if d == dir {
				found = true
				break
			}
		}
		if !found {
			result.SuccessDirs = append(result.SuccessDirs, dir)
		}
	}
	return result
}

// organizeOneFile 整理单个网盘文件：TMDB 校验 → 分类 → 建目标目录 → 移动 → 重命名。
// media 为已解析的媒体信息；无法识别（含 TMDB 校验失败）返回 errMediaUnrecognized（不移动）。
// rootPath 为已整理根目录路径；返回整理成功的目标相对目录（相对已整理根目录）。
func organizeOneFile(ctx context.Context, account *models.Account, e organizeEntry, rootPath string, dirCache map[string]string, media *IdentifyResult) (string, error) {
	extra := baseOrganizeExtra(account, e.ParentID, 0)
	sourcePath := e.ParentID + "/" + e.Name
	if media == nil || strings.TrimSpace(media.Title) == "" {
		recordSkipped(account, e, sourcePath, "", "", 0, 0, 0, 0, "", "文件名无法识别", extra)
		return "", errMediaUnrecognized
	}
	officialTitle, tmdbID, tmdbYear, categoryName, err := lookupTmdbMedia(ctx, media)
	if err != nil {
		recordSkipped(account, e, sourcePath, media.Category, media.Title, media.Year, media.Season, media.Episode, 0, "", "TMDB 未找到匹配结果："+err.Error(), extra)
		return "", fmt.Errorf("%w：%v", errMediaUnrecognized, err)
	}
	year := tmdbYear
	if year <= 0 {
		year = media.Year
	}
	relDir, ok := buildOrganizeRelDir(media.Category, officialTitle, year, media.Season, tmdbID, categoryName)
	if !ok {
		recordSkipped(account, e, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "", "媒体信息不完整，无法构建目标目录", extra)
		return "", fmt.Errorf("%w：媒体信息不完整", errMediaUnrecognized)
	}
	newName := buildOrganizeNewName(media.Category, officialTitle, media.Season, media.Episode, year, path.Ext(e.Name))

	targetDirID, err := ensureOrganizeDirInternal(ctx, account, rootPath, relDir, dirCache)
	if err != nil {
		recordFailed(account, e, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "创建目标目录失败："+err.Error(), extra)
		return "", fmt.Errorf("创建目标目录 %s 失败：%v", relDir, err)
	}
	if err := moveNetdiskFileInternal(account, e.ID, e.ParentID, targetDirID); err != nil {
		recordFailed(account, e, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "移动失败："+err.Error(), extra)
		return "", fmt.Errorf("移动 %s 失败：%v", e.Name, err)
	}
	if err := renameNetdiskFileInternal(account, e.ID, e.ParentID, targetDirID, newName); err != nil {
		recordFailed(account, e, sourcePath, media.Category, media.Title, year, media.Season, media.Episode, tmdbID, "重命名失败："+err.Error(), extra)
		return "", fmt.Errorf("重命名 %s 失败：%v", e.Name, err)
	}
	recordSuccess(account, e, sourcePath, relDir+"/"+newName, media.Category, officialTitle, year, media.Season, media.Episode, tmdbID, newName, "整理成功", extra)
	return relDir, nil
}

// saveFailedFile 记录识别失败/整理失败的文件到 movie_pilot_failed_files（任务下同一文件已有待处理记录则不重复插入）
func saveFailedFile(task *models.MoviePilotUploadTask, account *models.Account, e organizeEntry, err error) {
	if task == nil || account == nil {
		return
	}
	var count int64
	if err := db.Db.Model(&models.MoviePilotFailedFile{}).
		Where("task_id = ? AND file_name = ? AND status = ?", task.ID, e.Name, models.MoviePilotFailedPending).
		Count(&count); err == nil && count > 0 {
		return
	}
	reason := "文件名无法识别"
	if !errors.Is(err, errMediaUnrecognized) && err != nil {
		reason = err.Error()
	}
	f := &models.MoviePilotFailedFile{
		TaskID:    task.ID,
		FileName:  e.Name,
		ParentID:  e.ParentID,
		RootPath:  task.RemotePath,
		AccountID: account.ID,
		Status:    string(models.MoviePilotFailedPending),
		Reason:    reason,
	}
	if err := models.CreateMoviePilotFailedFile(f); err != nil {
		helpers.AppLogger.Errorf("保存 MoviePilot 识别失败记录失败：%v", err)
	}
}

// ResolveFailedFile 手动确认媒体信息后重新整理识别失败的文件（识别失败独立菜单"确认整理"）。
// 返回整理成功的目标相对目录（相对整理根目录）。
func ResolveFailedFile(ctx context.Context, account *models.Account, f *models.MoviePilotFailedFile, mediaType, title string, year, season int, tmdbID int64) (string, error) {
	if f.Status == string(models.MoviePilotFailedResolved) {
		return "", fmt.Errorf("该文件已整理完成")
	}
	files, err := listNetDirByID(ctx, account, f.ParentID)
	if err != nil {
		return "", fmt.Errorf("读取源目录失败：%v", err)
	}
	var entry *organizeEntry
	for i := range files {
		if files[i].Name == f.FileName && !files[i].IsDir {
			entry = &files[i]
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("网盘中已找不到文件 %s（可能已被移动或删除）", f.FileName)
	}
	if mediaType != "tv" && mediaType != "movie" {
		mediaType = "movie"
	}
	media := &IdentifyResult{Category: mediaType, Title: title, Year: year, Season: season, Episode: 1, TmdbId: tmdbID}
	if parsed, ok := mediaparse.ParseEpisode(f.FileName); ok {
		if parsed.Season > 0 {
			media.Season = parsed.Season
		}
		if parsed.Episode > 0 {
			media.Episode = parsed.Episode
		}
	}
	if media.Title == "" {
		return "", fmt.Errorf("请填写媒体标题")
	}
	dirCache := map[string]string{}
	organizeRoot := organizeRootPath(models.MoviePilotConfigGlobal.UploadRoot)
	return organizeOneFile(ctx, account, *entry, organizeRoot, dirCache, media)
}

// buildOrganizeRelDir 构建整理目标相对目录（相对已整理根目录），格式：
// 剧集：{分类}/{标题 (年份) {tmdb=xxx}}/Season NN
// 电影：{分类}/{标题 (年份) {tmdb=xxx}}
func buildOrganizeRelDir(category, title string, year, season int, tmdbID int64, categoryName string) (string, bool) {
	if strings.TrimSpace(title) == "" || tmdbID <= 0 {
		return "", false
	}
	titlePart := fmt.Sprintf("%s (%d) {tmdb=%d}", title, year, tmdbID)
	if year <= 0 {
		titlePart = fmt.Sprintf("%s {tmdb=%d}", title, tmdbID)
	}
	if categoryName == "" {
		categoryName = "未分类"
	}
	base := fmt.Sprintf("%s/%s", categoryName, titlePart)
	if category == "tv" {
		if season <= 0 {
			season = 1
		}
		return fmt.Sprintf("%s/Season %02d", base, season), true
	}
	return base, true
}

// buildOrganizeNewName 按目录整理规则生成规范化文件名
// 剧集：标题.年份.S01E01.第1集.ext；电影：标题 (年份).ext
func buildOrganizeNewName(category, title string, season, episode, year int, ext string) string {
	if category == "tv" {
		if episode <= 0 {
			episode = 1
		}
		if year > 0 {
			return fmt.Sprintf("%s.%d.S%02dE%02d.第%d集%s", title, year, season, episode, episode, ext)
		}
		return fmt.Sprintf("%s.S%02dE%02d.第%d集%s", title, season, episode, episode, ext)
	}
	if year > 0 {
		return fmt.Sprintf("%s (%d)%s", title, year, ext)
	}
	return title + ext
}

// collectOrganizeEntries 递归收集目录项（限深度与数量）
func collectOrganizeEntries(ctx context.Context, account *models.Account, parentID string, out *[]organizeEntry, counter *int, depth int) error {
	if depth > organizeMaxDepth || *counter > organizeMaxEntries {
		return nil
	}
	files, err := listNetDirByID(ctx, account, parentID)
	if err != nil {
		return err
	}
	for _, f := range files {
		*counter++
		if *counter > organizeMaxEntries {
			break
		}
		*out = append(*out, f)
		if f.IsDir {
			if err := collectOrganizeEntries(ctx, account, f.ID, out, counter, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// listNetDirByID 按目录 ID（或路径）列出网盘目录内容
func listNetDirByID(ctx context.Context, account *models.Account, parentID string) ([]organizeEntry, error) {
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		resp, err := client.GetFsList(ctx, parentID, true, true, false, 0, 100)
		if err != nil {
			return nil, err
		}
		entries := make([]organizeEntry, 0, len(resp.Data))
		for i := range resp.Data {
			entries = append(entries, organizeEntry{ID: resp.Data[i].FileId, Name: resp.Data[i].FileName, ParentID: parentID, IsDir: resp.Data[i].FileCategory == "0"})
		}
		return entries, nil
	case models.SourceType123:
		client := account.Get123Client()
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return nil, err
		}
		entries := make([]organizeEntry, 0, len(files))
		for i := range files {
			entries = append(entries, organizeEntry{ID: fmt.Sprintf("%d", files[i].FileId), Name: files[i].FileName, ParentID: parentID, IsDir: files[i].Type == 1})
		}
		return entries, nil
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		files, err := client.GetFileList(ctx, parentID, 0, 0, 0, 1000)
		if err != nil {
			return nil, err
		}
		entries := make([]organizeEntry, 0, len(files))
		for i := range files {
			entries = append(entries, organizeEntry{ID: files[i].Path, Name: files[i].ServerFilename, ParentID: parentID, IsDir: files[i].IsDir == 1})
		}
		return entries, nil
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		p := normalizeOpenListPath(parentID)
		entries := make([]organizeEntry, 0)
		for page := 1; page <= 10; page++ {
			resp, err := client.FileList(ctx, p, page, 100)
			if err != nil {
				return nil, err
			}
			if len(resp.Content) == 0 {
				break
			}
			for _, item := range resp.Content {
				entries = append(entries, organizeEntry{ID: joinOpenListPath(p, item.Name), Name: item.Name, ParentID: p, IsDir: item.IsDir})
			}
			if int64(len(entries)) >= resp.Total {
				break
			}
		}
		return entries, nil
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return nil, fmt.Errorf("获取中国移动云盘客户端失败")
		}
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return nil, err
		}
		entries := make([]organizeEntry, 0, len(files))
		for i := range files {
			entries = append(entries, organizeEntry{ID: files[i].GetID(), Name: files[i].FileName, ParentID: parentID, IsDir: files[i].Type == "folder"})
		}
		return entries, nil
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		files, err := client.GetFiles(ctx, parentID)
		if err != nil {
			return nil, err
		}
		entries := make([]organizeEntry, 0, len(files))
		for i := range files {
			entries = append(entries, organizeEntry{ID: files[i].GetID(), Name: files[i].FileName, ParentID: parentID, IsDir: files[i].ResType == 2})
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("该网盘类型暂不支持目录整理：%s", account.SourceType)
	}
}

// ensureOrganizeDirInternal 确保整理目标目录存在，返回目录 ID（或路径语义字符串）
func ensureOrganizeDirInternal(ctx context.Context, account *models.Account, rootPath, relDir string, dirCache map[string]string) (string, error) {
	if id, ok := dirCache[relDir]; ok {
		return id, nil
	}
	fullPath := strings.TrimRight(rootPath, "/") + "/" + relDir
	id, err := EnsureRemoteDir(ctx, account, fullPath)
	if err != nil {
		return "", err
	}
	// 无效 ID（空/0）不入缓存，避免后续视频误用无效父目录导致移动失败
	if id != "" && id != "0" {
		dirCache[relDir] = id
	}
	return id, nil
}

// moveNetdiskFileInternal 按账号类型移动网盘文件到目标目录
func moveNetdiskFileInternal(account *models.Account, fileID, oldParentID, targetParentID string) error {
	ctx := context.Background()
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err := client.Move(ctx, []string{fileID}, targetParentID)
		return err
	case models.SourceType123:
		client := account.Get123Client()
		return client.Move(ctx, fileID, targetParentID)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		return client.MoveBatch(ctx, []baidupan.MoveOrCopyItem{{
			Path:    fileID,
			Dest:    targetParentID,
			NewName: path.Base(fileID),
		}})
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		oldPath := normalizeOpenListPath(oldParentID)
		if oldPath == "" || oldPath == "." {
			oldPath = "/"
		}
		return client.Move(oldPath, normalizeOpenListPath(targetParentID), []string{path.Base(fileID)})
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("获取中国移动云盘客户端失败")
		}
		return client.MoveBatch(ctx, []string{fileID}, targetParentID)
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		return client.Move(ctx, []string{fileID}, targetParentID)
	default:
		return fmt.Errorf("该网盘暂不支持移动")
	}
}

// renameNetdiskFileInternal 按账号类型重命名网盘文件
func renameNetdiskFileInternal(account *models.Account, fileID, oldParentID, newParentID, newName string) error {
	ctx := context.Background()
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err := client.ReName(ctx, fileID, newName)
		return err
	case models.SourceType123:
		client := account.Get123Client()
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		parentPath := normalizeOpenListPath(newParentID)
		if parentPath == "" || parentPath == "." {
			parentPath = "/"
		}
		return client.Rename(parentPath, path.Base(fileID), newName)
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("获取中国移动云盘客户端失败")
		}
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		return client.Rename(ctx, fileID, newName)
	default:
		return fmt.Errorf("该网盘暂不支持重命名")
	}
}

// normalizeOpenListPath 归一化 OpenList 路径
func normalizeOpenListPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return filepath.ToSlash(path.Clean(p))
}

// joinOpenListPath 拼接 OpenList 完整路径
func joinOpenListPath(parent, name string) string {
	parent = strings.TrimRight(normalizeOpenListPath(parent), "/")
	if parent == "" {
		return "/" + name
	}
	return parent + "/" + name
}
