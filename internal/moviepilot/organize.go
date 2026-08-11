package moviepilot

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"diy-strm/internal/baidupan"
	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
)

// organizeMediaResult 网盘端整理结果
type organizeMediaResult struct {
	Organized     int      // 整理成功（移动+重命名）的视频数
	Failed        int      // 整理失败数
	Unrecognized  int      // 文件名无法识别数（不移动，保留原目录）
	FailedNames   []string // 失败/无法识别的文件名（前端展示）
	SuccessDirs   []string // 整理成功的目标相对目录（相对整理根目录），用于 STRM 同步
}

// organizeEntry 网盘目录项
type organizeEntry struct {
	ID       string
	Name     string
	ParentID string
	IsDir    bool
}

const (
	organizeMaxEntries = 500
	organizeMaxDepth   = 4
)

// organizeUploadedDir 对上传完成的网盘目录执行文件名解析整理（同"目录整理"规则）：
// 建目标目录 → 移动 → 重命名；只处理视频文件，无法识别的不移动。
// rootID 为整理根目录 ID（或路径语义字符串），rootPath 为整理根目录路径。
func organizeUploadedDir(ctx context.Context, account *models.Account, rootID, rootPath string) *organizeMediaResult {
	result := &organizeMediaResult{}
	var entries []organizeEntry
	counter := 0
	if err := collectOrganizeEntries(ctx, account, rootID, &entries, &counter, 0); err != nil {
		helpers.AppLogger.Errorf("MoviePilot 整理扫描目录 %s 失败：%v", rootPath, err)
		return result
	}

	dirIDCache := map[string]string{} // relPath -> 目录 ID
	for _, e := range entries {
		if e.IsDir || !mediaparse.IsVideoExt(e.Name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			break
		}
		category, title, season, episode, year := mediaparse.ParseMedia(e.Name)
		relDir, ok := mediaparse.BuildTargetRelPath(category, title, season, year)
		if !ok {
			result.Unrecognized++
			result.FailedNames = append(result.FailedNames, e.Name)
			continue
		}
		newName := buildOrganizeNewName(category, title, season, episode, year, path.Ext(e.Name))

		targetDirID, err := ensureOrganizeDirInternal(ctx, account, rootPath, relDir, dirIDCache)
		if err != nil {
			result.Failed++
			result.FailedNames = append(result.FailedNames, e.Name)
			helpers.AppLogger.Errorf("MoviePilot 整理创建目标目录 %s 失败：%v", relDir, err)
			continue
		}
		if err := moveNetdiskFileInternal(account, e.ID, e.ParentID, targetDirID); err != nil {
			result.Failed++
			result.FailedNames = append(result.FailedNames, e.Name)
			helpers.AppLogger.Errorf("MoviePilot 整理移动 %s 失败：%v", e.Name, err)
			continue
		}
		if err := renameNetdiskFileInternal(account, e.ID, e.ParentID, targetDirID, newName); err != nil {
			result.Failed++
			result.FailedNames = append(result.FailedNames, e.Name)
			helpers.AppLogger.Errorf("MoviePilot 整理重命名 %s 失败：%v", e.Name, err)
			continue
		}
		result.Organized++
		dir := relDir
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

// buildOrganizeNewName 按目录整理规则生成规范化文件名
func buildOrganizeNewName(category, title string, season, episode, year int, ext string) string {
	if category == "tv" {
		return fmt.Sprintf("%s S%02dE%02d%s", title, season, episode, ext)
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
	dirCache[relDir] = id
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
