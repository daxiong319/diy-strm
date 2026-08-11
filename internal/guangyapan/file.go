package guangyapan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 光鸭云盘 API 路径常量（逆向自 Web 端，协议参考 alist GuangYaPan 驱动）
const (
	APIFileList      = "/userres/v1/file/get_file_list"
	APIDownloadURL   = "/nd.bizuserres.s/v1/get_res_download_url"
	APICreateDir     = "/nd.bizuserres.s/v1/file/create_dir"
	APIDeleteFile    = "/nd.bizuserres.s/v1/file/delete_file"
	APIGetTaskStatus = "/nd.bizuserres.s/v1/get_task_status"
	APIRename        = "/userres/v1/file/rename"
	APIMoveFile      = "/userres/v1/file/move_file"
)

// PageSize 单页列表大小（与 Web 端一致）
const PageSize = 100

// isSuccess 判断业务响应是否成功
func isSuccess(code int, msg string) bool {
	return code == 0 || strings.EqualFold(strings.TrimSpace(msg), "success")
}

// ListFiles 分页获取目录下的文件列表
// parentID 为目录 ID（根目录为空字符串）
// page 从 0 开始
func (c *Client) ListFiles(ctx context.Context, parentID string, page int) (*Files, error) {
	body := map[string]interface{}{
		"parentId":  parentID,
		"page":      page,
		"pageSize":  PageSize,
		"orderBy":   3,
		"sortType":  1,
		"fileTypes": []int{},
	}
	var out Files
	if err := c.Request(ctx, APIFileList, body, &out); err != nil {
		return nil, err
	}
	if !isSuccess(out.Code, out.Msg) {
		return nil, fmt.Errorf("光鸭云盘获取文件列表失败：code=%d msg=%s", out.Code, out.Msg)
	}
	return &out, nil
}

// GetFiles 获取目录下所有文件（自动分页）
func (c *Client) GetFiles(ctx context.Context, parentID string) ([]File, error) {
	page := 0
	res := make([]File, 0)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		resp, err := c.ListFiles(ctx, parentID, page)
		if err != nil {
			return nil, err
		}
		res = append(res, resp.Data.List...)
		if len(resp.Data.List) < PageSize {
			break
		}
		if resp.Data.Total > 0 && len(res) >= resp.Data.Total {
			break
		}
		page++
	}
	return res, nil
}

// GetDownloadURL 获取文件下载直链
// 优先返回 signedURL，其次 downloadUrl
func (c *Client) GetDownloadURL(ctx context.Context, fileID string) (string, error) {
	if strings.TrimSpace(fileID) == "" {
		return "", errors.New("光鸭云盘文件 ID 为空")
	}
	var out DownloadURLResp
	if err := c.Request(ctx, APIDownloadURL, map[string]interface{}{
		"fileId": fileID,
	}, &out); err != nil {
		return "", err
	}
	if !isSuccess(out.Code, out.Msg) {
		return "", fmt.Errorf("光鸭云盘获取下载链接失败：code=%d msg=%s", out.Code, out.Msg)
	}
	url := strings.TrimSpace(out.Data.SignedURL)
	if url == "" {
		url = strings.TrimSpace(out.Data.DownloadURL)
	}
	if url == "" {
		return "", errors.New("光鸭云盘获取下载链接失败：返回链接为空")
	}
	return url, nil
}

// GetFileById 在指定父目录中查找文件（可避免全盘遍历），失败后回退根目录
func (c *Client) GetFileById(ctx context.Context, fileID, parentID string) (*File, error) {
	parentIDs := make([]string, 0, 2)
	if strings.TrimSpace(parentID) != "" {
		parentIDs = append(parentIDs, parentID)
	}
	parentIDs = append(parentIDs, "")
	seen := make(map[string]bool)
	for _, pid := range parentIDs {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		files, err := c.GetFiles(ctx, pid)
		if err != nil {
			continue
		}
		for i := range files {
			if files[i].GetID() == fileID {
				return &files[i], nil
			}
		}
	}
	return nil, fmt.Errorf("光鸭云盘未找到文件 ID %s", fileID)
}

// GetPathIdByPath 通过路径查找目录 ID
// 从根目录开始逐级查找；路径使用 / 分隔，如 /电影/科幻
// 如果路径为空或 /，返回根目录 ID（空字符串）
func (c *Client) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", nil
	}
	parts := strings.Split(path, "/")
	parentID := ""
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
			return "", fmt.Errorf("光鸭云盘路径 %s 不存在：找不到目录 %s", path, part)
		}
	}
	return parentID, nil
}

// CreateDir 创建目录，返回新目录 ID
func (c *Client) CreateDir(ctx context.Context, parentID, dirName string) (string, error) {
	var out CommonResp
	if err := c.Request(ctx, APICreateDir, map[string]interface{}{
		"parentId": parentID,
		"dirName":  dirName,
	}, &out); err != nil {
		return "", err
	}
	if !isSuccess(out.Code, out.Msg) {
		return "", fmt.Errorf("光鸭云盘创建目录失败：code=%d msg=%s", out.Code, out.Msg)
	}
	if strings.TrimSpace(out.Data.FileID) == "" {
		return "", errors.New("光鸭云盘创建目录成功但未返回目录 ID")
	}
	return out.Data.FileID, nil
}

// waitTaskDone 轮询异步任务状态直到完成
// status：2=成功，-1/3=失败
func (c *Client) waitTaskDone(ctx context.Context, taskID string) error {
	const (
		maxTry   = 30
		interval = 300 * time.Millisecond
	)
	for i := 0; i < maxTry; i++ {
		var out TaskStatusResp
		if err := c.Request(ctx, APIGetTaskStatus, map[string]interface{}{
			"taskId": taskID,
		}, &out); err != nil {
			return err
		}
		if !isSuccess(out.Code, out.Msg) {
			return fmt.Errorf("光鸭云盘查询任务状态失败：code=%d msg=%s", out.Code, out.Msg)
		}
		switch out.Data.Status {
		case 2:
			return nil
		case -1, 3:
			return fmt.Errorf("光鸭云盘任务 %s 失败：status=%d", taskID, out.Data.Status)
		}
		if i == maxTry-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("光鸭云盘任务 %s 超时", taskID)
}

// Rename 重命名文件或目录
func (c *Client) Rename(ctx context.Context, fileID, newName string) error {
	fileID = strings.TrimSpace(fileID)
	newName = strings.TrimSpace(newName)
	if fileID == "" {
		return errors.New("光鸭云盘重命名失败：fileID 为空")
	}
	if newName == "" {
		return errors.New("光鸭云盘重命名失败：newName 为空")
	}
	var out CommonResp
	if err := c.Request(ctx, APIRename, map[string]interface{}{
		"fileId":  fileID,
		"newName": newName,
	}, &out); err != nil {
		return err
	}
	if !isSuccess(out.Code, out.Msg) {
		return fmt.Errorf("光鸭云盘重命名失败：code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

// Move 移动文件或目录到目标目录，等待异步任务完成
func (c *Client) Move(ctx context.Context, fileIDs []string, targetParentID string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	var out CommonResp
	if err := c.Request(ctx, APIMoveFile, map[string]interface{}{
		"fileIds":  fileIDs,
		"parentId": targetParentID,
	}, &out); err != nil {
		return err
	}
	if !isSuccess(out.Code, out.Msg) {
		return fmt.Errorf("光鸭云盘移动失败：code=%d msg=%s", out.Code, out.Msg)
	}
	taskID := strings.TrimSpace(out.Data.TaskID)
	if taskID == "" {
		return nil
	}
	return c.waitTaskDone(ctx, taskID)
}

// Delete 删除文件或目录（放入回收站），等待异步任务完成
func (c *Client) Delete(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	var out CommonResp
	if err := c.Request(ctx, APIDeleteFile, map[string]interface{}{
		"fileIds": fileIDs,
	}, &out); err != nil {
		return err
	}
	if !isSuccess(out.Code, out.Msg) {
		return fmt.Errorf("光鸭云盘删除文件失败：code=%d msg=%s", out.Code, out.Msg)
	}
	taskID := strings.TrimSpace(out.Data.TaskID)
	if taskID == "" {
		// 部分后端同步删除，无任务 ID
		return nil
	}
	return c.waitTaskDone(ctx, taskID)
}
