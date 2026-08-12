package pan139

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// API 路径常量（逆向自中国移动云盘 Web 端，协议参考 alist 139Yun 驱动）
const (
	APIFileList     = "/file/list"
	APIDownloadURL  = "/file/getDownloadUrl"
	APICreateDir    = "/file/create"
	APIDeleteFile   = "/recyclebin/batchTrash"
	APIGetUploadURL = "/file/getUploadUrl"
	APIComplete     = "/file/complete"
	APIUpdate       = "/file/update"
	APIBatchMove    = "/file/batchMove"
	APIBatchCopy    = "/file/batchCopy"
	APIBatchRename  = "/file/batchRename"
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
	newID := strings.TrimSpace(out.Data.FileId)
	// 139 偶发返回未生效/假目录 ID（后续 create 文件会报资源不存在 code=04000010），
	// 创建后立即校验目录可用，校验不过重试（139 列表一致性有延迟），仍不过则报错让调用方重试
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := c.GetFiles(ctx, newID); err == nil {
			return newID, nil
		} else if ctx.Err() != nil {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("中国移动云盘创建目录后校验目录 ID 无效：%s", newID)
}

// Delete 删除文件或目录（移入回收站，异步任务，最多轮询 60 秒等待完成）
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

// taskResp 异步任务状态查询响应
type taskResp struct {
	BaseResp
	TaskID     string `json:"taskId"`
	TaskStatus int    `json:"taskStatus"`
	Status     int    `json:"status"`
}

// WaitTaskDone 轮询异步任务直到完成（status=3 成功；status=2 执行中；其他值视为失败）
// 接口路径先试 /task/get，失败时回退 /hcy/task/get（部分路由域需要 /hcy 前缀）
// 最多等待 timeout（默认 60 秒）
func (c *Client) WaitTaskDone(ctx context.Context, taskID string, timeout time.Duration) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("中国移动云盘任务 ID 为空")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	paths := []string{"/task/get", "/hcy/task/get"}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var lastErr error
		for _, path := range paths {
			var out taskResp
			if err := c.Request(ctx, path, map[string]interface{}{"taskId": taskID}, &out); err != nil {
				lastErr = err
				continue
			}
			if !out.Success && out.Code != "" {
				lastErr = fmt.Errorf("code=%s msg=%s", out.Code, out.Message)
				continue
			}
			status := out.Status
			if status == 0 {
				status = out.TaskStatus
			}
			switch status {
			case 3:
				return nil
			case 2:
				lastErr = nil
			default:
				if status != 0 {
					return fmt.Errorf("中国移动云盘任务 %s 状态异常：status=%d", taskID, status)
				}
				lastErr = nil
			}
			break
		}
		if lastErr == nil {
			time.Sleep(1000 * time.Millisecond)
			continue
		}
		// 两个路径都失败：记录错误后继续轮询（可能只是接口瞬时异常），超时后返回
		time.Sleep(1000 * time.Millisecond)
	}
	return fmt.Errorf("中国移动云盘任务 %s 等待超时", taskID)
}

// Rename 重命名文件或目录
func (c *Client) Rename(ctx context.Context, fileID, newName string) error {
	if strings.TrimSpace(fileID) == "" {
		return errors.New("中国移动云盘文件 ID 为空")
	}
	if strings.TrimSpace(newName) == "" {
		return errors.New("中国移动云盘新文件名不能为空")
	}
	var out BaseResp
	if err := c.Request(ctx, APIUpdate, map[string]interface{}{
		"fileId":      fileID,
		"name":        newName,
		"description": "",
	}, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("中国移动云盘重命名失败：code=%s msg=%s", out.Code, out.Message)
	}
	return nil
}

// MoveBatch 批量移动文件/目录到目标目录
func (c *Client) MoveBatch(ctx context.Context, fileIDs []string, toParentFileID string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	if strings.TrimSpace(toParentFileID) == "" {
		return errors.New("中国移动云盘目标目录 ID 为空")
	}
	var out BaseResp
	if err := c.Request(ctx, APIBatchMove, map[string]interface{}{
		"fileIds":        fileIDs,
		"toParentFileId": toParentFileID,
	}, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("中国移动云盘移动文件失败：code=%s msg=%s", out.Code, out.Message)
	}
	return nil
}

// CopyBatch 批量复制文件/目录到目标目录
func (c *Client) CopyBatch(ctx context.Context, fileIDs []string, toParentFileID string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	if strings.TrimSpace(toParentFileID) == "" {
		return errors.New("中国移动云盘目标目录 ID 为空")
	}
	var out BaseResp
	if err := c.Request(ctx, APIBatchCopy, map[string]interface{}{
		"fileIds":        fileIDs,
		"toParentFileId": toParentFileID,
	}, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("中国移动云盘复制文件失败：code=%s msg=%s", out.Code, out.Message)
	}
	return nil
}
