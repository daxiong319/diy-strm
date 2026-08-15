package pan123

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"resty.dev/v3"
)

const (
	ShareGet      = "/share/get"
	FileCopyAsync = "/file/copy/async"
)

// ListShareDir 列出分享目录（登录态，分页游标 next，起始 "0"）。
// parentFileId 传 "0" 表示分享根目录。
func (c *Client) ListShareDir(ctx context.Context, shareKey, sharePwd, parentFileId string) ([]File, error) {
	if err := c.waitForPermission(ctx, c.api(ShareGet)); err != nil {
		return nil, err
	}
	var all []File
	next := "0"
	for guard := 0; guard < 100; guard++ {
		query := map[string]string{
			"ShareKey":       shareKey,
			"SharePwd":       sharePwd,
			"parentFileId":   parentFileId,
			"Page":           "1",
			"limit":          "100",
			"next":           next,
			"orderBy":        "file_name",
			"orderDirection": "asc",
			"event":          "homeListFile",
		}
		var files Files
		body, err := c.Request(ctx, c.api(ShareGet), http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &files); err != nil {
			return nil, fmt.Errorf("解析分享列表失败：%w", err)
		}
		all = append(all, files.Data.InfoList...)
		nxt := files.Data.Next
		if nxt == "" || nxt == "0" || nxt == "-1" || nxt == next {
			break
		}
		next = nxt
	}
	return all, nil
}

type shareCopyFile struct {
	FileID       int64  `json:"file_id"`
	FileName     string `json:"file_name"`
	Etag         string `json:"etag"`
	Size         int64  `json:"size"`
	ParentFileID string `json:"parent_file_id"`
	DriveID      int    `json:"drive_id"`
	Type         int    `json:"type"`
}

type shareCopyReq struct {
	ShareKey     string          `json:"share_key"`
	SharePwd     string          `json:"share_pwd"`
	CurrentLevel int             `json:"current_level"`
	Event        string          `json:"event"`
	FileList     []shareCopyFile `json:"file_list"`
}

// SaveShare 将分享转存到目标目录（服务端异步复制整棵分享树）。
// 返回分享标题（顶级条目名）与转存项数。
func (c *Client) SaveShare(ctx context.Context, shareKey, sharePwd, targetParentID string) (title string, total int, err error) {
	items, err := c.ListShareDir(ctx, shareKey, sharePwd, "0")
	if err != nil {
		return "", 0, err
	}
	if len(items) == 0 {
		return "", 0, fmt.Errorf("分享为空或已失效")
	}
	fl := make([]shareCopyFile, 0, len(items))
	for _, it := range items {
		typ := 0
		if it.IsDir() {
			typ = 1
		}
		fl = append(fl, shareCopyFile{
			FileID:       it.FileId,
			FileName:     it.FileName,
			Etag:         it.Etag,
			Size:         it.Size,
			ParentFileID: targetParentID,
			DriveID:      0,
			Type:         typ,
		})
	}
	body, err := c.Request(ctx, c.api(FileCopyAsync), http.MethodPost, func(req *resty.Request) {
		req.SetBody(shareCopyReq{
			ShareKey:     shareKey,
			SharePwd:     sharePwd,
			CurrentLevel: 1,
			Event:        "transfer",
			FileList:     fl,
		})
	})
	if err != nil {
		return "", 0, err
	}
	var resp BaseResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", 0, fmt.Errorf("解析转存响应失败：%w", err)
	}
	if resp.Code != 0 {
		return "", 0, fmt.Errorf("转存失败：%s（code=%d）", resp.Message, resp.Code)
	}
	title = shareKey
	if len(items) > 0 && strings.TrimSpace(items[0].FileName) != "" {
		title = items[0].FileName
	}
	return title, len(fl), nil
}

// FindDirByPath 通过路径查找目录 FileId，返回最终目录 ID。
// 123 云盘无路径语义，路径使用 / 分隔逐级查找，如 媒体库/待整理。
// 空路径或 / 返回根目录 ID "0"；任一级目录缺失时返回出错位置。
func (c *Client) FindDirByPath(ctx context.Context, dirName string) (string, error) {
	name := strings.Trim(strings.TrimSpace(dirName), "/")
	if name == "" {
		return "0", nil
	}
	parentID := "0"
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		files, err := c.GetFiles(ctx, parentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, f := range files {
			if f.IsDir() && f.FileName == part {
				parentID = f.GetID()
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("目录 %s 不存在（父目录 %s 下）", part, parentID)
		}
	}
	return parentID, nil
}

// EnsureDirByPath 逐级查找目录，任一级不存在则自动创建，返回最终目录 ID。
// 空路径或 / 返回根目录 ID "0"；创建失败时返回出错位置。
func (c *Client) EnsureDirByPath(ctx context.Context, dirName string) (string, error) {
	name := strings.Trim(strings.TrimSpace(dirName), "/")
	if name == "" {
		return "0", nil
	}
	parentID := "0"
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		files, err := c.GetFiles(ctx, parentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, f := range files {
			if f.IsDir() && f.FileName == part {
				parentID = f.GetID()
				found = true
				break
			}
		}
		if !found {
			parentID, err = c.CreateDir(ctx, parentID, part)
			if err != nil {
				return "", err
			}
		}
	}
	return parentID, nil
}
