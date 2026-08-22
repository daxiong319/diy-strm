// Package hdhive —— 官方 OpenAPI 通道客户端。
//
// HDHive 官方 OpenAPI 采用「应用认证 + 用户授权」双层模型：
//   - 应用 Secret 通过 X-API-Key 请求头证明调用方身份
//   - 用户 Access Token 通过 Authorization: Bearer 证明代表哪个用户
//   - OAuth: /openapi/authorize → /api/public/openapi/oauth/token → /refresh
//
// 与 tgtodrive 中转通道（OAuthClient，install_id + HMAC 签名）互为独立通道，
// 响应信封一致（success/code/message/data），上层通过 ChannelClient 抽象统一调度。
package hdhive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ChannelClient 影巢通道统一接口：tgtodrive 中转（*OAuthClient）与官方 OpenAPI（*OfficialClient）同构实现，
// 上层按账号通道选择客户端并做故障切换
type ChannelClient interface {
	Ping(ctx context.Context) error
	Me(ctx context.Context) (*OAuthAPIResponse, error)
	GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error)
	GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error)
	UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error)
	Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error)
}

// 官方 OpenAPI 常量
const (
	DefaultOfficialBaseURL = "https://hdhive.com"
	OfficialTimeout        = 30 * time.Second
	// OfficialDefaultScope 资源查询/解锁/签到所需 scope（与应用申请的权限一致）
	OfficialDefaultScope = "meta query unlock write"
)

// OfficialConfig 官方通道配置（全局一份，来自应用注册信息）
type OfficialConfig struct {
	BaseURL    string // 默认 https://hdhive.com
	ClientID   string // OpenAPI 应用 client_id（app_ 开头）
	AppSecret  string // 应用 Secret（X-API-Key）
	RedirectURI string // OAuth 回调地址（diy-strm 前端回调页）
}

// OfficialClient 官方 OpenAPI 客户端（有状态：持有一个账号的 token）
type OfficialClient struct {
	cfg    OfficialConfig
	HTTP   *http.Client
	// TokenState 由调用方注入/回写（账号行存储）
	AccessToken  string
	RefreshToken string

	// OnTokenRefreshed token 刷新成功后的回调（用于持久化新 token），可为空
	OnTokenRefreshed func(access, refresh string, accessExpiresIn int)
}

// NewOfficialClient 创建官方通道客户端
func NewOfficialClient(cfg OfficialConfig) *OfficialClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultOfficialBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &OfficialClient{
		cfg:  cfg,
		HTTP: &http.Client{Timeout: OfficialTimeout},
	}
}

// BuildOfficialAuthorizeURL 构造官方授权页 URL
// state 由调用方生成并暂存（回调时校验）
func (c *OfficialClient) BuildOfficialAuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURI)
	q.Set("scope", OfficialDefaultScope)
	q.Set("state", state)
	// 应用固定回调模式为 postmessage；无 opener 时授权页自动降级为 redirect
	q.Set("response_mode", "postmessage")
	return c.cfg.BaseURL + "/openapi/authorize?" + q.Encode()
}

// NewOAuthState 生成随机 state（32 字节 hex）
func NewOAuthState() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ExchangeCode 授权码换取用户 Token（POST /api/public/openapi/oauth/token）
func (c *OfficialClient) ExchangeCode(ctx context.Context, code string) (*OAuthAPIResponse, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  c.cfg.RedirectURI,
	}
	return c.oauthCall(ctx, "/api/public/openapi/oauth/token", body)
}

// RefreshToken 刷新用户 Token（POST /api/public/openapi/oauth/refresh）
func (c *OfficialClient) RefreshAccessToken(ctx context.Context, refreshToken string) (*OAuthAPIResponse, error) {
	body := map[string]string{"refresh_token": refreshToken}
	return c.oauthCall(ctx, "/api/public/openapi/oauth/refresh", body)
}

// oauthCall OAuth 接口调用（仅 X-API-Key，无用户 token）
func (c *OfficialClient) oauthCall(ctx context.Context, path string, body any) (*OAuthAPIResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.cfg.AppSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oauthUserAgent)
	return c.doRequest(ctx, req)
}

// doRequest 执行请求并解析统一响应
func (c *OfficialClient) doRequest(_ context.Context, req *http.Request) (*OAuthAPIResponse, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 HDHive 官方 OpenAPI 失败：%v", err)
	}
	defer resp.Body.Close()
	bodyText, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%v", err)
	}
	var out OAuthAPIResponse
	if err := json.Unmarshal(bodyText, &out); err != nil {
		return nil, fmt.Errorf("解析 HDHive 官方响应失败（HTTP %d）：%v", resp.StatusCode, err)
	}
	out.StatusCode = resp.StatusCode
	return &out, nil
}

// apiCall 业务接口调用：携带 X-API-Key + Bearer；
// 命中 OPENAPI_REFRESH_REQUIRED 时自动刷新一次并重试，OPENAPI_REAUTH_REQUIRED 返回可识别错误
func (c *OfficialClient) apiCall(ctx context.Context, method, path string, body any) (*OAuthAPIResponse, error) {
	resp, err := c.apiCallOnce(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && resp.Code == "OPENAPI_REFRESH_REQUIRED" && c.RefreshToken != "" {
		refreshed, rerr := c.RefreshAccessToken(ctx, c.RefreshToken)
		if rerr != nil {
			return nil, fmt.Errorf("刷新官方通道 Token 失败：%v", rerr)
		}
		if !refreshed.Success {
			return refreshed, nil
		}
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.Unmarshal(refreshed.Data, &tok); err == nil && tok.AccessToken != "" {
			c.AccessToken = tok.AccessToken
			if tok.RefreshToken != "" {
				c.RefreshToken = tok.RefreshToken
			}
			if c.OnTokenRefreshed != nil {
				c.OnTokenRefreshed(c.AccessToken, c.RefreshToken, tok.ExpiresIn)
			}
		}
		// 用新 token 重试一次
		return c.apiCallOnce(ctx, method, path, body)
	}
	return resp, nil
}

// apiCallOnce 单次业务接口调用
func (c *OfficialClient) apiCallOnce(ctx context.Context, method, path string, body any) (*OAuthAPIResponse, error) {
	var bodyBytes []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyBytes = data
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), c.cfg.BaseURL+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.cfg.AppSecret)
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oauthUserAgent)
	return c.doRequest(ctx, req)
}

// ---------------------------------------------------------------------------
// 与 OAuthClient 对齐的业务方法（路径按官方 OpenAPI 文档）
// ---------------------------------------------------------------------------

// Ping 验证应用 Secret（GET /api/open/ping，仅 X-API-Key）
func (c *OfficialClient) Ping(ctx context.Context) error {
	resp, err := c.apiCall(ctx, http.MethodGet, "/api/open/ping", nil)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("HDHive 官方通道 Ping 失败：%s（%s）", resp.Message, resp.Code)
	}
	return nil
}

// Me 当前授权用户信息（GET /api/open/me）
func (c *OfficialClient) Me(ctx context.Context) (*OAuthAPIResponse, error) {
	return c.apiCall(ctx, http.MethodGet, "/api/open/me", nil)
}

// GetResources 按 TMDB 查询资源（GET /api/open/resources/{type}/{tmdb_id}）
func (c *OfficialClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/open/resources/%s/%s", mediaType, tmdbID)
	return c.apiCall(ctx, http.MethodGet, path, nil)
}

// GetShareDetail 获取分享详情（GET /api/open/shares/{slug}）
func (c *OfficialClient) GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	path := "/api/open/shares/" + url.PathEscape(slug)
	return c.apiCall(ctx, http.MethodGet, path, nil)
}

// UnlockResource 解锁资源（POST /api/open/resources/unlock {slug}）
func (c *OfficialClient) UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.apiCall(ctx, http.MethodPost, "/api/open/resources/unlock", map[string]string{"slug": slug})
}

// Checkin 每日签到（POST /api/open/checkin {is_gambler}）
func (c *OfficialClient) Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error) {
	var payload any
	if isGambler {
		payload = map[string]bool{"is_gambler": true}
	}
	return c.apiCall(ctx, http.MethodPost, "/api/open/checkin", payload)
}

// OfficialTokenResult OAuth token 响应解析
type OfficialTokenResult struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
}

// ParseOfficialToken 从 token/refresh 响应 data 中解析 token 结果
func ParseOfficialToken(data json.RawMessage) (*OfficialTokenResult, error) {
	var r OfficialTokenResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("响应缺少 access_token")
	}
	return &r, nil
}
