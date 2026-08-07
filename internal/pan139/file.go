package pan139

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// API 路径常量（逆向自中国移动云盘 Web 端，协议参考 alist 139Yun 驱动）
const (
	APIFileList      = "/file/list"
	APIDownloadURL   = "/file/getDownloadUrl"
	APICreateDir     = "/file/create"
	APIDeleteFile    = "/recyclebin/batchTrash"
	APIGetUploadURL  = "/file/getUploadUrl"
	APIComplete      = "/file/complete"
	APIUpdate        = "/file/update"
	APIBatchMove     = "/file/batchMove"
	APIBatchCopy     = "/file/batchCopy"
	APIBatchRename   = "/file/batchRename"
)

// PageSize 单页列表大小（与 Web 端一致）
const PageSize = 100

// ListPage 获取一页文件列表
// parentID 为目录 ID（根目录为空字符串或 "root"）
// cursor 为上一页返回的 nextPageCursor（第一页传空字符串）
// 返回文件列表与下一页游标（为空表示没有更多）
func (c *Client) ListPage(ctx context.Context, parentID, cursor string) ([]File, string, error) {
	if strings.TrimSpace(parentID) == "" {
		parentID = "root"
	}
	body := map[string]interface{}{
		"imageThumbnailStyleList": []string{"Small", "Large"},
		"orderBy":                 "updated_at",
		"orderDirection":          "DESC",
		"pageInfo": map[string]interface{}{
			"pageCursor": cursor,
			"pageSize":   PageSize,
		},
		"parentFileId": parentID,
	}
	var out ListResp
	if err := c.Request(ctx, APIFileList, body, &out); err != nil {
		return nil, "", err
	}
	if !out.Success {
		return nil, "", fmt.Errorf("中国移动云盘获取文件列表失败：code=%s msg=%s", out.Code, out.Message)
	}
	files := make([]File, 0, len(out.Data.Items))
	for _, item := range out.Data.Items {
		files = append(files, item.toFile())
	}
	return files, out.Data.NextPageCursor, nil
}

// GetFiles 获取目录下所有文件（自动翻页）
func (c *Client) GetFiles(ctx context.Context, parentID string) ([]File, error) {
	res := make([]File, 0)
	cursor := ""
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		page, nextCursor, err := c.ListPage(ctx, parentID, cursor)
		if err != nil {
			return nil, err
		}
		res = append(res, page...)
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	return res, nil
}

// GetDownloadURL 获取文件下载直链
// 优先返回 cdnUrl，其次 url
func (c *Client) GetDownloadURL(ctx context.Context, fileID string) (string, error) {
	if strings.TrimSpace(fileID) == "" {
		return "", errors.New("中国移动云盘文件 ID 为空")
	}
	var out DownloadURLResp
	if err := c.Request(ctx, APIDownloadURL, map[string]interface{}{
		"fileId": fileID,
	}, &out); err != nil {
		return "", err
	}
	if !out.Success {
		return "", fmt.Errorf("中国移动云盘获取下载链接失败：code=%s msg=%s", out.Code, out.Message)
	}
	u := strings.TrimSpace(out.Data.CdnURL)
	if u == "" {
		u = strings.TrimSpace(out.Data.URL)
	}
	if u == "" {
		return "", errors.New("中国移动云盘获取下载链接失败：返回链接为空")
	}
	return u, nil
}

// GetPathIdByPath 通过路径查找目录 ID
// 从根目录（root）开始逐级查找；路径使用 / 分隔，如 /电影/科幻
// 如果路径为空或 /，返回根目录 ID（root）
func (c *Client) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "root", nil
	}
	parts := strings.Split(path, "/")
	parentID := "root"
	for _, part := range parts {
		if part == "" {
			continue
		}
		files, err := c.GetFiles(ctx, parentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, file := range files {
			if file.IsDir() && file.FileName == part {
				parentID = file.GetID()
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("中国移动云盘路径 %s 不存在：找不到目录 %s", path, part)
		}
	}
	return parentID, nil
}

// CreateDir 创建目录，返回新目录 ID
func (c *Client) CreateDir(ctx context.Context, parentID, dirName string) (string, error) {
	if strings.TrimSpace(parentID) == "" {
		parentID = "root"
	}
	var out CreateResp
	if err := c.Request(ctx, APICreateDir, map[string]interface{}{
		"parentFileId":   parentID,
		"name":           dirName,
		"description":    "",
		"type":           "folder",
		"fileRenameMode": "force_rename",
	}, &out); err != nil {
		return "", err
	}
	if !out.Success {
		return "", fmt.Errorf("中国移动云盘创建目录失败：code=%s msg=%s", out.Code, out.Message)
	}
	if strings.TrimSpace(out.Data.FileId) == "" {
		return "", errors.New("中国移动云盘创建目录成功但未返回目录 ID")
	}
	return out.Data.FileId, nil
}

// Delete 删除文件或目录（移入回收站）
func (c *Client) Delete(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	var out BaseResp
	if err := c.Request(ctx, APIDeleteFile, map[string]interface{}{
		"fileIds": fileIDs,
	}, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("中国移动云盘删除文件失败：code=%s msg=%s", out.Code, out.Message)
	}
	return nil
}
