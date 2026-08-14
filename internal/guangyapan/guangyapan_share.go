package guangyapan

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// 分享 API 路径（逆向自光鸭云盘 Web 端分享页 www.guangyapan.com/s/{shareId}）
const (
	APIGetShareAccessToken = "/userres/v1/get_share_access_token"
	APIGetShareSummary     = "/userres/v1/get_share_summary"
	APIGetSharePageFiles   = "/userres/v1/get_share_page_files_list"
	APIRestoreShare        = "/userres/v1/restore_share"
)

// ShareSummary 分享概要响应
type ShareSummary struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ShareID     string `json:"shareId"`
		Title       string `json:"title"`
		ShareStatus int    `json:"shareStatus"` // 1=正常
	} `json:"data"`
}

// ShareAccessTokenResp 分享访问令牌响应
type ShareAccessTokenResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken string `json:"accessToken"`
	} `json:"data"`
}

// ShareFiles 分享文件列表响应
type ShareFiles struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total  int    `json:"total"`
		List   []File `json:"list"`
		Cursor int    `json:"cursor"`
	} `json:"data"`
}

// RestoreShareResp 转存分享响应
type RestoreShareResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		TaskID string `json:"taskId"`
	} `json:"data"`
}

// describeShareErr 将分享接口业务错误码转为人话
func describeShareErr(code int, msg string) string {
	switch code {
	case 157:
		return "分享转存空间不足（免费账号转存大分享受限，请升级会员或转存较小的分享）"
	case 212:
		return "分享转存文件数超限"
	}
	if msg != "" {
		return msg
	}
	return fmt.Sprintf("code=%d", code)
}

// GetShareAccessToken 获取分享访问令牌。code 为提取码，无提取码传空串。
func (c *Client) GetShareAccessToken(ctx context.Context, shareID, code string) (string, error) {
	var out ShareAccessTokenResp
	if err := c.Request(ctx, APIGetShareAccessToken, map[string]any{
		"shareId": shareID,
		"code":    code,
	}, &out); err != nil {
		return "", err
	}
	token := strings.TrimSpace(out.Data.AccessToken)
	if token == "" {
		return "", fmt.Errorf("光鸭云盘获取分享访问令牌失败：%s", describeShareErr(out.Code, out.Msg))
	}
	return token, nil
}

// GetShareSummary 获取分享概要（标题/状态）
func (c *Client) GetShareSummary(ctx context.Context, shareID string) (*ShareSummary, error) {
	var out ShareSummary
	if err := c.Request(ctx, APIGetShareSummary, map[string]any{"shareId": shareID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSharePageFiles 分页获取分享目录文件列表。parentID 为空表示分享根目录。
func (c *Client) GetSharePageFiles(ctx context.Context, shareID, accessToken, parentID string, page int) (*ShareFiles, error) {
	var out ShareFiles
	if err := c.Request(ctx, APIGetSharePageFiles, map[string]any{
		"shareId":    shareID,
		"accessToken": accessToken,
		"parentId":   parentID,
		"page":       page,
		"pageSize":   PageSize,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListShareDir 读取分享根目录全部条目
func (c *Client) ListShareDir(ctx context.Context, shareID, accessToken string) ([]File, error) {
	items := make([]File, 0)
	page := 0
	for {
		out, err := c.GetSharePageFiles(ctx, shareID, accessToken, "", page)
		if err != nil {
			return nil, err
		}
		items = append(items, out.Data.List...)
		if len(out.Data.List) == 0 || len(items) >= out.Data.Total {
			break
		}
		page++
		if page > 100 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return items, nil
}

// RestoreShare 转存分享到目标目录（服务端异步复制）。返回任务 ID。
func (c *Client) RestoreShare(ctx context.Context, shareID, accessToken, shareCode, parentID string, fileIDs []string) (string, error) {
	var out RestoreShareResp
	if err := c.Request(ctx, APIRestoreShare, map[string]any{
		"shareId":    shareID,
		"accessToken": accessToken,
		"fileIds":    fileIDs,
		"parentId":   parentID,
		"shareCode":  shareCode,
	}, &out); err != nil {
		return "", err
	}
	taskID := strings.TrimSpace(out.Data.TaskID)
	if taskID == "" {
		return "", fmt.Errorf("光鸭云盘转存分享失败：%s", describeShareErr(out.Code, out.Msg))
	}
	return taskID, nil
}

// SaveShare 转存分享根目录全部内容到目标目录，等待任务完成。返回分享标题与转存条目数。
func (c *Client) SaveShare(ctx context.Context, shareID, code, parentID string) (title string, total int, err error) {
	token, err := c.GetShareAccessToken(ctx, shareID, code)
	if err != nil {
		return "", 0, err
	}
	summary, err := c.GetShareSummary(ctx, shareID)
	if err != nil {
		return "", 0, err
	}
	title = strings.TrimSpace(summary.Data.Title)
	if title == "" {
		title = shareID
	}
	items, err := c.ListShareDir(ctx, shareID, token)
	if err != nil {
		return "", 0, err
	}
	if len(items) == 0 {
		return "", 0, errors.New("分享内容为空")
	}
	fileIDs := make([]string, 0, len(items))
	for _, it := range items {
		fileIDs = append(fileIDs, it.FileID)
	}
	taskID, err := c.RestoreShare(ctx, shareID, token, code, parentID, fileIDs)
	if err != nil {
		return "", 0, err
	}
	if err := c.waitTaskDone(ctx, taskID); err != nil {
		return "", 0, err
	}
	return title, len(items), nil
}
