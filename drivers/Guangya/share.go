package guangya

import (
	"context"
	"net/url"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/driver"
)

// 光鸭云盘分享 API 路径（逆向自 Web 端分享页 https://www.guangyapan.com/s/{shareId}）。
const (
	pathGetShareAccessToken = "/userres/v1/get_share_access_token"
	pathGetShareSummary     = "/userres/v1/get_share_summary"
	pathGetSharePageFiles   = "/userres/v1/get_share_page_files_list"
	pathRestoreShare        = "/userres/v1/restore_share"
)

// SaveShareLink 实现 driver.ShareLinkSaver：光鸭分享链接一键转存。
func (d *Driver) SaveShareLink(ctx context.Context, req driver.ShareLinkSaveRequest) (*driver.ShareLinkSaveResult, error) {
	shareID, code := parseGuangyaShareURL(req.ShareURL, req.SharePwd)
	if shareID == "" {
		return nil, domain.Errorf(domain.CodeValidation, "无法从分享链接解析出光鸭分享标识：%s", req.ShareURL)
	}
	parentID := d.resolveParent(req.TargetParentID)

	// 1) 分享访问令牌（提取码可为空）
	var tokenResp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := d.apiRequest(ctx, pathGetShareAccessToken, map[string]any{
		"shareId": shareID,
		"code":    code,
	}, &tokenResp); err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	accessToken := strings.TrimSpace(tokenResp.AccessToken)

	// 2) 分享概要（标题）
	var summary struct {
		Title string `json:"title"`
	}
	_ = d.apiRequest(ctx, pathGetShareSummary, map[string]any{"shareId": shareID}, &summary)

	// 3) 分享根目录文件列表（分页）
	fileIDs := make([]string, 0, 16)
	for page := 1; page <= 100; page++ {
		var files struct {
			Total int `json:"total"`
			List  []struct {
				FileID string `json:"fileId"`
			} `json:"list"`
		}
		if err := d.apiRequest(ctx, pathGetSharePageFiles, map[string]any{
			"shareId":     shareID,
			"accessToken": accessToken,
			"parentId":    "",
			"page":        page,
		}, &files); err != nil {
			return nil, domain.Wrap(domain.CodeDriverError, err)
		}
		for _, f := range files.List {
			if id := strings.TrimSpace(f.FileID); id != "" {
				fileIDs = append(fileIDs, id)
			}
		}
		if len(files.List) == 0 || len(fileIDs) >= files.Total {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(fileIDs) == 0 {
		return nil, domain.Errorf(domain.CodeDriverError, "分享链接内容为空")
	}

	// 4) 转存（服务端异步），等待任务完成
	var restore struct {
		TaskID string `json:"taskId"`
	}
	if err := d.apiRequest(ctx, pathRestoreShare, map[string]any{
		"shareId":     shareID,
		"accessToken": accessToken,
		"fileIds":     fileIDs,
		"parentId":    parentID,
		"shareCode":   code,
	}, &restore); err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	taskID := strings.TrimSpace(restore.TaskID)
	if taskID == "" {
		return nil, domain.Errorf(domain.CodeDriverError, "光鸭云盘转存失败：未返回任务 ID")
	}
	if err := d.waitTaskDone(ctx, taskID); err != nil {
		return nil, err
	}

	title := strings.TrimSpace(summary.Title)
	if title == "" {
		title = shareID
	}
	return &driver.ShareLinkSaveResult{
		Title:    title,
		Total:    len(fileIDs),
		ParentID: parentID,
		Message:  "转存成功",
	}, nil
}

// parseGuangyaShareURL 解析光鸭分享链接：shareId + 提取码。
// 支持 https://www.guangyapan.com/s/{shareId}?pwd=xxxx、guangyapan.com/s/{shareId} 等写法。
func parseGuangyaShareURL(rawURL, explicitPwd string) (shareID, code string) {
	code = strings.TrimSpace(explicitPwd)
	link := strings.TrimSpace(rawURL)
	if link == "" {
		return "", code
	}
	if idx := strings.Index(link, "http"); idx > 0 {
		link = link[idx:]
	}
	if code == "" {
		for _, marker := range []string{"提取码", "密码", "访问码", "pwd", "Pwd", "code"} {
			idx := strings.Index(link, marker)
			if idx < 0 {
				continue
			}
			rest := strings.TrimLeft(link[idx+len(marker):], "：:，, 	=")
			fields := strings.FieldsFunc(rest, func(r rune) bool {
				return r == ' ' || r == '\t' || r == '\n' || r == '，' || r == ','
			})
			if len(fields) > 0 {
				code = strings.TrimSpace(fields[0])
			}
			link = link[:idx]
			break
		}
	}
	if u, err := url.Parse(strings.TrimSpace(link)); err == nil && code == "" {
		q := u.Query()
		code = firstNonEmptyGY(q.Get("pwd"), q.Get("password"), q.Get("code"))
	}
	link = strings.TrimSpace(link)
	if idx := strings.IndexAny(link, "?#"); idx >= 0 {
		link = link[:idx]
	}
	link = strings.TrimSuffix(strings.TrimSuffix(link, "/"), ".html")
	// URL 可能被散文包裹（「分享给你 https://… 快去」），在首个空白或中文标点处截断。
	if idx := strings.IndexFunc(link, shareURLBoundary); idx >= 0 {
		link = link[:idx]
	}
	parts := strings.Split(link, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.TrimSpace(parts[i])
		if seg == "" || strings.Contains(seg, ":") {
			continue
		}
		shareID = seg
		break
	}
	return shareID, code
}

// firstNonEmptyGY 首个非空字符串
func firstNonEmptyGY(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// shareURLBoundary 判定 URL 结束的边界字符（空白/中文标点），
// 用于剥掉分享文本里粘在链接尾部的说明文字。
func shareURLBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '，', ',', '。', '；', ';', '！', '!', '、', '）', '】':
		return true
	}
	return false
}
