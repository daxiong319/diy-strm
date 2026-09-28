package quark

import (
	"context"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 夸克分享转存（协议对齐 quark.pyc / alist quark_uc 驱动）
// 1. POST /share/sharepage/token（share_id+password → stoken）
// 2. POST /share/sharepage/detail（share_id+stoken+passcode → 目录树）
// 3. POST /share/sharepage/save（share_id+stoken+to_pdir_fid+fid_list+fid_token_list）
// ---------------------------------------------------------------------------

// ShareFileEntry 夸克分享目录中的文件条目
type ShareFileEntry struct {
	Fid           string `json:"fid"`
	ShareFidToken string `json:"share_fid_token"`
	Name          string `json:"file_name"`
	Size          int64  `json:"size"`
	IsDir         bool   `json:"dir"`
}

// GetShareStoken 获取分享 stoken
func (c *Client) GetShareStoken(ctx context.Context, shareID, passcode string) (string, error) {
	raw, err := c.request(ctx, "POST", "/share/sharepage/token", map[string]any{
		"pwd_id":   shareID,
		"passcode": passcode,
	})
	if err != nil {
		return "", err
	}
	stoken := anyString(raw["stoken"])
	if stoken == "" {
		return "", fmt.Errorf("获取 stoken 失败")
	}
	return stoken, nil
}

// ListShareFiles 列分享目录文件（含 fid_token，转存必需）
func (c *Client) ListShareFiles(ctx context.Context, shareID, stoken, parentFID string) ([]ShareFileEntry, error) {
	raw, err := c.request(ctx, "POST", "/share/sharepage/detail", map[string]any{
		"pwd_id":        shareID,
		"stoken":        stoken,
		"pdir_fid":      parentFID,
		"_page":         1,
		"_size":         100,
		"_fetch_banner": 1,
		"_fetch_share":  1,
		"_fetch_total":  1,
		"order":         "file_type:asc,updated_at:desc",
	})
	if err != nil {
		return nil, err
	}
	list, _ := raw["list"].([]any)
	entries := make([]ShareFileEntry, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entries = append(entries, ShareFileEntry{
			Fid:           anyString(m["fid"]),
			ShareFidToken: anyString(m["share_fid_token"]),
			Name:          firstNonEmpty(anyString(m["file_name"]), anyString(m["name"])),
			Size:          int64(anyFloat(m["size"])),
			IsDir:         anyBool(m["dir"]),
		})
	}
	return entries, nil
}

// SaveShareFiles 批量转存分享文件到目标目录
func (c *Client) SaveShareFiles(ctx context.Context, shareID, stoken, toPdirFID string, entries []ShareFileEntry) error {
	fids := make([]string, 0, len(entries))
	tokens := make([]string, 0, len(entries))
	for _, e := range entries {
		fids = append(fids, e.Fid)
		tokens = append(tokens, e.ShareFidToken)
	}
	_, err := c.request(ctx, "POST", "/share/sharepage/save", map[string]any{
		"to_pdir_fid":    toPdirFID,
		"stoken":         stoken,
		"fid_list":       fids,
		"fid_token_list": tokens,
	})
	return err
}

// SaveShareTransfer 分享转存入口
func (c *Client) SaveShareTransfer(ctx context.Context, shareID, passcode, targetFolderID string) (title string, total int, err error) {
	// 1. stoken
	stoken, err := c.GetShareStoken(ctx, shareID, passcode)
	if err != nil {
		return "", 0, err
	}
	// 2. 列根目录
	entries, err := c.ListShareFiles(ctx, shareID, stoken, "0")
	if err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		return "空分享", 0, nil
	}
	title = entries[0].Name
	// 3. 批量转存
	if err := c.SaveShareFiles(ctx, shareID, stoken, targetFolderID, entries); err != nil {
		return "", 0, err
	}
	return title, len(entries), nil
}

var _ = strings.Contains
