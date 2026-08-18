package pan123

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"resty.dev/v3"

	"diy-strm/internal/helpers"
)

// ListFiles 分页获取目录下的文件列表
// parentFileId 为目录 ID（根目录为 0；空字符串按根目录处理，
// /file/list/new 接口对空 parentFileId 会报「请输入ParentFileId」）
func (c *Client) ListFiles(ctx context.Context, parentFileId string, page int) (*Files, error) {
	if err := c.waitForPermission(ctx, c.api("/file/list/new")); err != nil {
		return nil, err
	}
	if strings.TrimSpace(parentFileId) == "" {
		parentFileId = "0"
	}
	query := map[string]string{
		"driveId":              "0",
		"limit":                "100",
		"next":                 "0",
		"orderBy":              "file_id",
		"orderDirection":       "desc",
		"parentFileId":         parentFileId,
		"trashed":              "false",
		"SearchData":           "",
		"Page":                 strconv.Itoa(page),
		"OnlyLookAbnormalFile": "0",
		"event":                "homeListFile",
		"operateType":          "4",
		"inDirectSpace":        "false",
	}

	var files Files
	body, err := c.Request(ctx, c.api("/file/list/new"), http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
	})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, fmt.Errorf("解析文件列表失败：%w", err)
	}
	return &files, nil
}

// GetFiles 获取目录下所有文件（自动分页）
func (c *Client) GetFiles(ctx context.Context, parentFileId string) ([]File, error) {
	page := 1
	res := make([]File, 0)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		resp, err := c.ListFiles(ctx, parentFileId, page)
		if err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "safe box") || strings.Contains(err.Error(), "保险箱") {
				// 需要解锁保险箱：调用方应配置保险箱密码
				return nil, fmt.Errorf("目录 %s 为保险箱，需要保险箱密码：%w", parentFileId, err)
			}
			return nil, err
		}
		page++
		res = append(res, resp.Data.InfoList...)
		if len(resp.Data.InfoList) == 0 || resp.Data.Next == "-1" {
			break
		}
	}
	return res, nil
}

// CreateDir 创建目录，返回新目录 ID
func (c *Client) CreateDir(ctx context.Context, parentFileId, dirName string) (string, error) {
	data := map[string]interface{}{
		"driveId":      0,
		"etag":         "",
		"fileName":     dirName,
		"parentFileId": parentFileId,
		"size":         0,
		"type":         1,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	respBody, err := c.Request(ctx, c.api("/file/upload_request"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body)
	})
	if err != nil {
		helpers.AppLogger.Errorf("123 创建目录失败（parentFileId=%q dirName=%q）：%v", parentFileId, dirName, err)
		return "", err
	}
	// 调试：记录建目录原始响应，便于核对空目录返回结构
	log.Printf("pan123 CreateDir parentFileId=%q dirName=%q resp=%s", parentFileId, dirName, string(respBody))
	var resp UploadResp
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("解析创建目录响应失败：%w", err)
	}
	// 123 的 upload_request 对空目录返回 data.FileId=0，真实目录 ID 在 data.Info.FileId 中
	if resp.Data.Info != nil && resp.Data.Info.FileId > 0 {
		return strconv.FormatInt(resp.Data.Info.FileId, 10), nil
	}
	return strconv.FormatInt(resp.Data.FileId, 10), nil
}

// Rename 重命名文件或目录
func (c *Client) Rename(ctx context.Context, fileId, newName string) error {
	data := map[string]interface{}{
		"driveId":  0,
		"fileId":   fileId,
		"fileName": newName,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, c.api("/file/rename"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body)
	})
	return err
}

// Move 移动文件到指定目录
func (c *Client) Move(ctx context.Context, fileId, parentFileId string) error {
	data := map[string]interface{}{
		"fileIdList":   []map[string]interface{}{{"FileId": fileId}},
		"parentFileId": parentFileId,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, c.api("/file/mod_pid"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body)
	})
	return err
}

// Delete 删除文件或目录（放入回收站）
// fileIds 为文件 ID 列表
func (c *Client) Delete(ctx context.Context, fileIds []string) error {
	fileTrashInfoList := make([]map[string]interface{}, 0, len(fileIds))
	for _, fileId := range fileIds {
		fileTrashInfoList = append(fileTrashInfoList, map[string]interface{}{"FileId": fileId})
	}
	data := map[string]interface{}{
		"driveId":           0,
		"operation":         true,
		"fileTrashInfoList": fileTrashInfoList,
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = c.Request(ctx, c.api("/file/trash"), http.MethodPost, func(req *resty.Request) {
		req.SetBody(body)
	})
	return err
}

// GetFileById 获取文件完整信息（含 Etag/S3KeyFlag，供下载信息接口使用）
// 优先在指定父目录中查找（可避免全盘遍历），失败后回退根目录
func (c *Client) GetFileById(ctx context.Context, fileId, parentId string) (*File, error) {
	parentIds := make([]string, 0, 2)
	if parentId != "" && parentId != "0" {
		parentIds = append(parentIds, parentId)
	}
	parentIds = append(parentIds, "0")
	seen := make(map[string]bool)
	for _, pid := range parentIds {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		files, err := c.GetFiles(ctx, pid)
		if err != nil {
			continue
		}
		for i := range files {
			if files[i].GetID() == fileId {
				return &files[i], nil
			}
		}
	}
	return nil, fmt.Errorf("未找到文件 ID %s", fileId)
}

// GetPathIdByPath 通过路径查找目录 ID
// 从根目录（0）开始逐级查找；路径使用 / 分隔，如 /电影/科幻
// 如果路径为空或 /，返回根目录 ID "0"
func (c *Client) GetPathIdByPath(ctx context.Context, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "0", nil
	}
	parts := strings.Split(path, "/")
	parentId := "0"
	for _, part := range parts {
		if part == "" {
			continue
		}
		files, err := c.GetFiles(ctx, parentId)
		if err != nil {
			return "", err
		}
		found := false
		for _, file := range files {
			if file.IsDir() && file.FileName == part {
				parentId = file.GetID()
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("路径 %s 不存在：找不到目录 %s", path, part)
		}
	}
	return parentId, nil
}
