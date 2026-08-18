package moviepilot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// EnsureRemoteDir 确保各网盘上存在指定路径，返回父目录 ID（或路径语义字符串）
// 逐级查找，不存在则逐级创建；path 为空或 / 时返回根目录标识（空字符串）
func EnsureRemoteDir(ctx context.Context, account *models.Account, path string) (string, error) {
	if account == nil {
		return "", errors.New("账号为空")
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return "", nil
	}
	switch account.SourceType {
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return "", errors.New("获取中国移动云盘客户端失败")
		}
		return ensureDirById(ctx, path, client.CreateDir, func(parentID string) ([]dirEntry, error) {
			files, err := client.GetFiles(ctx, parentID)
			if err != nil {
				return nil, err
			}
			entries := make([]dirEntry, 0, len(files))
			for i := range files {
				entries = append(entries, dirEntry{id: files[i].GetID(), name: files[i].FileName, isDir: files[i].Type == "folder"})
			}
			return entries, nil
		})
	case models.SourceType123:
		client := account.Get123Client()
		return ensureDirById(ctx, path, client.CreateDir, func(parentID string) ([]dirEntry, error) {
			files, err := client.GetFiles(ctx, parentID)
			if err != nil {
				return nil, err
			}
			entries := make([]dirEntry, 0, len(files))
			for i := range files {
				entries = append(entries, dirEntry{id: strconv.FormatInt(files[i].FileId, 10), name: files[i].FileName, isDir: files[i].Type == 1})
			}
			return entries, nil
		})
	case models.SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		return ensureDirById(ctx, path, client.CreateDir, func(parentID string) ([]dirEntry, error) {
			files, err := client.GetFiles(ctx, parentID)
			if err != nil {
				return nil, err
			}
			entries := make([]dirEntry, 0, len(files))
			for i := range files {
				entries = append(entries, dirEntry{id: files[i].GetID(), name: files[i].FileName, isDir: files[i].ResType == 2})
			}
			return entries, nil
		})
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		cur := ""
		for _, part := range strings.Split(path, "/") {
			if part == "" {
				continue
			}
			cur = cur + "/" + part
			if err := client.Mkdir(ctx, cur); err != nil {
				return "", fmt.Errorf("百度网盘创建目录 %s 失败：%v", cur, err)
			}
		}
		return "/" + path, nil
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		cur := ""
		for _, part := range strings.Split(path, "/") {
			if part == "" {
				continue
			}
			cur = cur + "/" + part
			if err := client.Mkdir(cur); err != nil {
				return "", fmt.Errorf("OpenList 创建目录 %s 失败：%v", cur, err)
			}
		}
		return "/" + path, nil
	case models.SourceType115:
		client := account.Get115Client()
		return ensureDirById(ctx, path, client.MkDir, func(parentID string) ([]dirEntry, error) {
			resp, err := client.GetFsList(ctx, parentID, true, true, false, 0, 100)
			if err != nil {
				return nil, err
			}
			entries := make([]dirEntry, 0, len(resp.Data))
			for i := range resp.Data {
				entries = append(entries, dirEntry{id: resp.Data[i].FileId, name: resp.Data[i].FileName, isDir: resp.Data[i].FileCategory == "0"})
			}
			return entries, nil
		})
	default:
		return "", fmt.Errorf("该网盘类型暂不支持上传：%s", account.SourceType)
	}
}

// dirEntry 目录项的最小抽象
type dirEntry struct {
	id    string
	name  string
	isDir bool
}

// ensureDirById 按目录 ID 语义逐级查找/创建
func ensureDirById(ctx context.Context, path string, mkdir func(ctx context.Context, parentID, dirName string) (string, error), list func(parentID string) ([]dirEntry, error)) (string, error) {
	parentID := ""
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		entries, err := list(parentID)
		if err != nil {
			helpers.AppLogger.Warnf("建目录路径处理：路径=%s 当前层级=%s 父ID=%q 列表失败：%v", path, part, parentID, err)
			return "", err
		}
		found := ""
		for _, e := range entries {
			if e.isDir && e.name == part {
				found = e.id
				break
			}
		}
		helpers.AppLogger.Infof("建目录路径处理：路径=%s 当前层级=%s 父ID=%q 已存在=%s", path, part, parentID, found)
		if found == "" {
			createdID, err := mkdir(ctx, parentID, part)
			if err != nil {
				helpers.AppLogger.Warnf("建目录路径处理：路径=%s 当前层级=%s 父ID=%q 创建失败：%v", path, part, parentID, err)
				return "", fmt.Errorf("创建目录 %s 失败：%v", part, err)
			}
			// 部分网盘创建接口返回的 ID 不可靠（123 空目录 upload_request 响应不含 FileId，返回 0），
			// 且列表接口存在最终一致性：创建后立即列出可能看不到新目录。
			// 重试按名称在父目录中定位，取得真实 ID（每次间隔 1 秒，最多 4 次）
			found = createdID
			if createdID == "" || createdID == "0" {
				found = retryFindDirID(list, parentID, part, 4)
				helpers.AppLogger.Infof("建目录 %s 返回 ID 无效（%q），重试按名称定位 → %q", part, createdID, found)
			}
		}
		if found == "" || found == "0" {
			return "", fmt.Errorf("目录 %s 创建后无法取得有效 ID", part)
		}
		parentID = found
	}
	return parentID, nil
}

// retryFindDirID 在父目录中按名称查找目录 ID，找不到时短暂等待后重试，
// 应对部分网盘（123）列表接口的最终一致性延迟。
func retryFindDirID(list func(parentID string) ([]dirEntry, error), parentID, name string, attempts int) string {
	for i := 0; i < attempts; i++ {
		if entries, err := list(parentID); err == nil {
			for _, e := range entries {
				if e.isDir && e.name == name {
					return e.id
				}
			}
		}
		if i < attempts-1 {
			time.Sleep(time.Second)
		}
	}
	return ""
}
