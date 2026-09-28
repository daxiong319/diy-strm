package cloud189

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 天翼云盘分享转存（协议对齐 tgto123 parse_189_share_simple.pyc 逆向）
// 1. getShareInfoByCodeV2.action → shareId
// 2. checkAccessCode.action（有密码时）
// 3. listShareDir.action → 分享目录树
// 4. createBatchTask.action(type=SHARE_SAVE) → taskId
// 5. checkBatchTask.action → 任务完成
// ---------------------------------------------------------------------------

// ShareInfo 天翼分享信息
type ShareInfo struct {
	ShareID    string `json:"shareId"`
	AccessCode string `json:"accessCode"`
}

// ShareFileEntry 分享目录中的文件条目
type ShareFileEntry struct {
	FileID string `json:"fileId"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"is_dir"`
}

// getShareInfoByCode 通过分享码获取 shareId
func (c *Client) getShareInfoByCode(ctx context.Context, shareCode string) (*ShareInfo, error) {
	q := url.Values{
		"shareCode": {shareCode},
		"noCache":   {fmt.Sprintf("%d", time.Now().UnixMilli())},
	}
	body, err := c.signedGet(ctx, APIURL, "/getShareInfoByCodeV2.action", q, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
		Data    struct {
			ShareID string `json:"shareId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析分享信息失败：%w", err)
	}
	if out.ResCode != 0 || out.Data.ShareID == "" {
		return nil, fmt.Errorf("获取分享信息失败：res_code=%d %s", out.ResCode, out.ResMsg)
	}
	return &ShareInfo{ShareID: out.Data.ShareID}, nil
}

// checkShareAccessCode 校验分享访问码（有密码时）
func (c *Client) checkShareAccessCode(ctx context.Context, shareID, accessCode string) error {
	form := url.Values{
		"shareId":    {shareID},
		"accessCode": {accessCode},
	}
	body, err := c.signedPost(ctx, APIURL, "/checkAccessCode.action", form, nil)
	if err != nil {
		return err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	if out.ResCode != 0 {
		return fmt.Errorf("访问码校验失败：%s", out.ResMsg)
	}
	return nil
}

// ListShareDir 列分享目录
func (c *Client) ListShareDir(ctx context.Context, shareID, parentFileID, accessCode string) ([]ShareFileEntry, error) {
	form := url.Values{
		"shareId":        {shareID},
		"shareDirFileId": {parentFileID},
		"parentFolderId": {""},
		"accessCode":     {accessCode},
		"pageInfo":       {`{"pageNum":1,"pageSize":1000}`},
		"orderBy":        {"1"},
		"orderDesc":      {"1"},
		"accessCode_":    {""},
	}
	body, err := c.signedPost(ctx, APIURL, "/listShareDir.action", form, nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
		Data    struct {
			ShareFileListAO struct {
				FileList []struct {
					FileId string `json:"fileId"`
					Name   string `json:"name"`
					Size   int64  `json:"size"`
					IsDir  bool   `json:"isFolder"`
				} `json:"fileList"`
			} `json:"shareFileListAO"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.ResCode != 0 {
		return nil, fmt.Errorf("列分享目录失败：res_code=%d %s", out.ResCode, out.ResMsg)
	}
	entries := make([]ShareFileEntry, 0)
	for _, f := range out.Data.ShareFileListAO.FileList {
		entries = append(entries, ShareFileEntry{
			FileID: f.FileId,
			Name:   f.Name,
			Size:   f.Size,
			IsDir:  f.IsDir,
		})
	}
	return entries, nil
}

// createShareSaveBatchTask 创建分享转存批量任务
func (c *Client) createShareSaveBatchTask(ctx context.Context, shareID, targetFolderID string, fileIDs []string) (string, error) {
	taskInfos := make([]map[string]any, 0, len(fileIDs))
	for _, fid := range fileIDs {
		taskInfos = append(taskInfos, map[string]any{
			"fileId":       fid,
			"destFolderId": targetFolderID,
		})
	}
	infosJSON, _ := json.Marshal(taskInfos)
	form := url.Values{
		"type":           {"SHARE_SAVE"},
		"taskInfos":      {string(infosJSON)},
		"targetFolderId": {targetFolderID},
	}
	body, err := c.signedPost(ctx, APIURL, "/createBatchTask.action", form, nil)
	if err != nil {
		return "", err
	}
	var out struct {
		ResCode int    `json:"res_code"`
		ResMsg  string `json:"res_msg"`
		Data    struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ResCode != 0 || out.Data.TaskID == "" {
		return "", fmt.Errorf("创建转存任务失败：res_code=%d %s", out.ResCode, out.ResMsg)
	}
	return out.Data.TaskID, nil
}

// checkBatchTask 检查批量任务状态
func (c *Client) checkBatchTask(ctx context.Context, taskID string) (bool, error) {
	form := url.Values{
		"type":   {"SHARE_SAVE"},
		"taskId": {taskID},
	}
	body, err := c.signedPost(ctx, APIURL, "/checkBatchTask.action", form, nil)
	if err != nil {
		return false, err
	}
	var out struct {
		ResCode int `json:"res_code"`
		Data    struct {
			TaskStatus int `json:"taskStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, err
	}
	// taskStatus: 1=执行中 2=完成
	return out.Data.TaskStatus == 2, nil
}

// SaveShareTransfer 分享转存入口：获取 shareId → 列目录 → 创建批量任务 → 等待完成
func (c *Client) SaveShareTransfer(ctx context.Context, shareCode, accessCode, targetFolderID string) (title string, total int, err error) {
	// 1. 获取 shareId
	info, err := c.getShareInfoByCode(ctx, shareCode)
	if err != nil {
		return "", 0, err
	}
	shareID := info.ShareID

	// 2. 校验访问码（有密码时）
	if accessCode != "" {
		if err := c.checkShareAccessCode(ctx, shareID, accessCode); err != nil {
			return "", 0, err
		}
	}

	// 3. 列分享根目录
	entries, err := c.ListShareDir(ctx, shareID, "", accessCode)
	if err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		return "空分享", 0, nil
	}

	// 4. 收集文件 ID（排除目录）
	fileIDs := make([]string, 0)
	var title_ string
	for _, e := range entries {
		if !e.IsDir {
			fileIDs = append(fileIDs, e.FileID)
			if title_ == "" {
				title_ = strings.TrimSuffix(e.Name, "."+e.Name) // 简化标题
			}
		}
	}
	if len(fileIDs) == 0 {
		return "分享中无文件", 0, nil
	}

	// 5. 创建转存任务
	taskID, err := c.createShareSaveBatchTask(ctx, shareID, targetFolderID, fileIDs)
	if err != nil {
		return "", 0, err
	}

	// 6. 等待完成
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		done, err := c.checkBatchTask(ctx, taskID)
		if err != nil {
			continue
		}
		if done {
			return title_, len(fileIDs), nil
		}
	}
	return title_, len(fileIDs), fmt.Errorf("转存任务超时")
}
