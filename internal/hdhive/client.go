// Package hdhive 提供影巢（HDHive Open API）资源搜索与解锁客户端。
//
// 上游接口（https://hdhive.com/api/open）：
//   - GET  /resources/{movie|tv}/{tmdb_id}  按 TMDB ID 取资源列表
//   - GET  /shares/{slug}                   分享详情（含网盘类型/是否免费/实际积分）
//   - POST /resources/unlock                解锁资源，返回分享链接
//   - POST /check/resource                  检查分享链接所属网盘类型
//   - GET  /ping                            验证 API Key
package hdhive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL 影巢 Open API 默认地址
const DefaultBaseURL = "https://hdhive.com/api/open"

// Client HDHive HTTP 客户端
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient 创建 HDHive 客户端
func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		APIKey:  strings.TrimSpace(apiKey),
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Response HDHive 统一响应壳
type Response struct {
	Success bool            `json:"success"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Resource 影巢资源条目
type Resource struct {
	Slug               string   `json:"slug"`
	Title              string   `json:"title"`
	ShareSize          string   `json:"share_size"`
	VideoResolution    []string `json:"video_resolution"`
	Source             []string `json:"source"`
	SubtitleLanguage   []string `json:"subtitle_language"`
	SubtitleType       []string `json:"subtitle_type"`
	Remark             string   `json:"remark"`
	UnlockPoints       int      `json:"unlock_points"`
	UnlockedUsersCount int      `json:"unlocked_users_count"`
	ValidateStatus     string   `json:"validate_status"`
	ValidateMessage    string   `json:"validate_message"`
	LastValidatedAt    string   `json:"last_validated_at"`
	IsOfficial         bool     `json:"is_official"`
	IsUnlocked         bool     `json:"is_unlocked"`
	User               *struct {
		ID        int    `json:"id"`
		Nickname  string `json:"nickname"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
}

// ShareDetail 分享详情（解锁前免费/积分判断）
type ShareDetail struct {
	Slug              string   `json:"slug"`
	Title             string   `json:"title"`
	PanType           string   `json:"pan_type"`
	ShareSize         string   `json:"share_size"`
	VideoResolution   []string `json:"video_resolution"`
	Source            []string `json:"source"`
	UnlockPoints      int      `json:"unlock_points"`
	ActualUnlockPoints int      `json:"actual_unlock_points"`
	IsUnlocked        bool     `json:"is_unlocked"`
	IsFreeForUser     bool     `json:"is_free_for_user"`
	UnlockMessage     string   `json:"unlock_message"`
	Media             *struct {
		Type    string `json:"type"`
		TMDBID  string `json:"tmdb_id"`
		Title   string `json:"title"`
		Season  string `json:"season"`
	} `json:"media"`
}

// UnlockResult 解锁结果
type UnlockResult struct {
	URL          string `json:"url"`
	AccessCode   string `json:"access_code"`
	FullURL      string `json:"full_url"`
	AlreadyOwned bool   `json:"already_owned"`
}

// CheckResult 分享链接网盘类型检查结果
type CheckResult struct {
	Website             string `json:"website"`
	URL                 string `json:"url"`
	BaseLink            string `json:"base_link"`
	AccessCode          string `json:"access_code"`
	DefaultUnlockPoints int    `json:"default_unlock_points"`
}

// do 发送请求，附加 X-API-Key，非 2xx/业务失败时返回错误
func (c *Client) do(ctx context.Context, method, path string, body any) (*Response, error) {
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return nil, fmt.Errorf("影巢 API Key 未配置（请在影巢设置中填写）")
	}
	u := c.BaseURL + path
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求影巢失败：%v", err)
	}
	defer resp.Body.Close()
	bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("影巢接口 %s %s 返回 %d：%s", method, path, resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}
	var out Response
	if err := json.Unmarshal(bodyText, &out); err != nil {
		return nil, fmt.Errorf("解析影巢响应失败：%v", err)
	}
	if !out.Success || (out.Code != "" && out.Code != "200") {
		msg := out.Message
		if msg == "" {
			msg = strings.TrimSpace(string(bodyText))
		}
		return nil, fmt.Errorf("影巢接口 %s 失败：%s", path, msg)
	}
	return &out, nil
}

// Ping 验证 API Key 是否有效
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/ping", nil)
	return err
}

// GetResources 按媒体类型与 TMDB ID 获取资源列表
func (c *Client) GetResources(ctx context.Context, mediaType, tmdbID string) ([]Resource, error) {
	path := fmt.Sprintf("/resources/%s/%s", mediaType, tmdbID)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var list []Resource
	if len(resp.Data) > 0 && string(resp.Data) != "null" {
		if err := json.Unmarshal(resp.Data, &list); err != nil {
			return nil, fmt.Errorf("解析影巢资源列表失败：%v", err)
		}
	}
	return list, nil
}

// GetShare 获取分享详情（含网盘类型与免费/积分信息）
func (c *Client) GetShare(ctx context.Context, slug string) (*ShareDetail, error) {
	resp, err := c.do(ctx, http.MethodGet, "/shares/"+slug, nil)
	if err != nil {
		return nil, err
	}
	var d ShareDetail
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, fmt.Errorf("解析影巢分享详情失败：%v", err)
	}
	return &d, nil
}

// Unlock 解锁资源，返回分享链接。
// allowPoints=true 时允许扣积分解锁收费资源。
func (c *Client) Unlock(ctx context.Context, slug string, allowPoints bool) (*UnlockResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/resources/unlock", map[string]any{
		"slug":         slug,
		"allow_points": allowPoints,
	})
	if err != nil {
		return nil, err
	}
	var r UnlockResult
	if err := json.Unmarshal(resp.Data, &r); err != nil {
		return nil, fmt.Errorf("解析影巢解锁结果失败：%v", err)
	}
	if strings.TrimSpace(r.FullURL) == "" && strings.TrimSpace(r.URL) == "" {
		return nil, fmt.Errorf("影巢解锁未返回分享链接（%s）", resp.Message)
	}
	return &r, nil
}

// CheckResource 检查分享链接所属网盘类型
func (c *Client) CheckResource(ctx context.Context, url string) (*CheckResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/check/resource", map[string]any{"url": url})
	if err != nil {
		return nil, err
	}
	var r CheckResult
	if err := json.Unmarshal(resp.Data, &r); err != nil {
		return nil, fmt.Errorf("解析影巢链接检查结果失败：%v", err)
	}
	return &r, nil
}
