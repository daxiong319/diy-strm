package moviepilot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
			return "", err
		}
		found := ""
		for _, e := range entries {
			if e.isDir && e.name == part {
				found = e.id
				break
			}
		}
		if found == "" {
			found, err = mkdir(ctx, parentID, part)
			if err != nil {
				return "", fmt.Errorf("创建目录 %s 失败：%v", part, err)
			}
		}
		parentID = found
	}
	return parentID, nil
}