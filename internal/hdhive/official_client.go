// Package hdhive —— 官方直连通道客户端（hdhive.com OpenAPI，mediavault 同款接入）。
//
// 认证模型（官方文档 _hdhive_docs/docs/open/authentication.md）：
//   - 应用认证：X-API-Key: <app secret>（所有 /api/open/* 业务接口必带）
//   - 用户授权：Authorization: Bearer <user access token>（除 meta 外必带）
//   - OAuth：授权页 https://hdhive.com/openapi/authorize?client_id&redirect_uri&scope&state&response_mode=redirect
//     → 回调 redirect_uri?code&state → POST /api/public/openapi/oauth/token 换 Token
//     → 过期后 POST /api/public/openapi/oauth/refresh 刷新
//   - 错误码：OPENAPI_REFRESH_REQUIRED（401，可刷新）/ OPENAPI_REAUTH_REQUIRED（401，需重新授权）
package hdhive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// 官方直连常量（应用凭证来自 mediavault 镜像 .so 字符串，可用环境变量覆盖）
const (
	DefaultOfficialBaseURL = "https://hdhive.com"
	officialRequestTimeout = 20 * time.Second
	officialScope          = "query unlock write"

	OfficialAuthPagePath  = "/openapi/authorize"
	officialTokenPath     = "/api/public/openapi/oauth/token"
	officialRefreshPath   = "/api/public/openapi/oauth/refresh"
	officialResourcesPath = "/api/open/resources/"   // + {type}/{tmdb_id}
	officialSharePath     = "/api/open/shares/"      // + {slug}
	officialUnlockPath    = "/api/open/resources/unlock"
	officialCheckinPath   = "/api/open/checkin"
	officialMePath        = "/api/open/me"
	officialPingPath      = "/api/open/ping"
)

var (
	officialClientID     = "app_e8a2f9f28964c7f1461203ea"
	officialClientSecret = "d753ca764b55e944e8b6554291f961f6"
)

func init() {
	if v := strings.TrimSpace(os.Getenv("HDHIVE_OFFICIAL_CLIENT_ID")); v != "" {
		officialClientID = v
	}
	if v := strings.TrimSpace(os.Getenv("HDHIVE_OFFICIAL_CLIENT_SECRET")); v != "" {
		officialClientSecret = v
	}
}

// OfficialClient 官方直连通道客户端（有状态：持有用户 Access/Refresh Token）
type OfficialClient struct {
	BaseURL      string
	AccessToken  string
	RefreshToken string
	TokenExpiresAt time.Time
	// OnTokenRefresh Token 变化回调（刷新后持久化），失败仅记日志不阻断
	OnTokenRefresh func(accessToken, refreshToken string, expiresAt time.Time) error

	HTTP *http.Client

	mu           sync.Mutex
	reauthNeeded bool
}

// NewOfficialClient 创建官方直连通道客户端
func NewOfficialClient(accessToken, refreshToken string, tokenExpiresAt time.Time) *OfficialClient {
	base := DefaultOfficialBaseURL
	if v := strings.TrimSpace(os.Getenv("HDHIVE_OFFICIAL_BASE_URL")); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return &OfficialClient{
		BaseURL:        base,
		AccessToken:    accessToken,
		RefreshToken:   refreshToken,
		TokenExpiresAt: tokenExpiresAt,
		HTTP:           &http.Client{Timeout: officialRequestTimeout},
	}
}

// BuildAuthURL 构造授权页地址（response_mode=redirect）
func BuildOfficialAuthURL(redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", officialClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", officialScope)
	q.Set("state", state)
	q.Set("response_mode", "redirect")
	return DefaultOfficialBaseURL + OfficialAuthPagePath + "?" + q.Encode()
}

// OfficialClientID 暴露应用 client_id（前端展示用）
func OfficialClientID() string { return officialClientID }

// officialTokenResp OAuth Token 接口响应
type officialTokenResp struct {
	Success         bool   `json:"success"`
	Code            string `json:"code"`
	Message         string `json:"message"`
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
	RefreshExpiresIn int64 `json:"refresh_expires_in"`
}

// ExchangeToken 授权码换取用户 Token（X-API-Key 认证）
func ExchangeOfficialToken(ctx context.Context, code, redirectURI string) (*officialTokenResp, error) {
	body, _ := json.Marshal(map[string]any{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": redirectURI,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultOfficialBaseURL+officialTokenPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", officialClientSecret)
	return doOfficialTokenRequest(req)
}

// RefreshOfficialToken 用 Refresh Token 换新 Access Token
func RefreshOfficialToken(ctx context.Context, refreshToken string) (*officialTokenResp, error) {
	body, _ := json.Marshal(map[string]any{"refresh_token": refreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DefaultOfficialBaseURL+officialRefreshPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", officialClientSecret)
	return doOfficialTokenRequest(req)
}

func doOfficialTokenRequest(req *http.Request) (*officialTokenResp, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out officialTokenResp
	// 官方 Token 接口 data 字段包一层：{success, code, message, data:{access_token,...}}
	var wrapped struct {
		officialTokenResp
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("解析 Token 响应失败：%w", err)
	}
	out = wrapped.officialTokenResp
	if len(wrapped.Data) > 0 && string(wrapped.Data) != "null" {
		var d officialTokenResp
		if json.Unmarshal(wrapped.Data, &d) == nil && d.AccessToken != "" {
			out.AccessToken = d.AccessToken
			out.RefreshToken = firstNonEmptyStr(d.RefreshToken, out.RefreshToken)
			out.ExpiresIn = d.ExpiresIn
			out.RefreshExpiresIn = d.RefreshExpiresIn
		}
	}
	if resp.StatusCode >= 400 || (!out.Success && out.AccessToken == "") {
		msg := firstNonEmptyStr(out.Message, out.Code, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil, fmt.Errorf("官方通道 Token 请求失败：%s", msg)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("官方通道 Token 响应缺少 access_token")
	}
	return &out, nil
}

// refreshAndRetry Access Token 过期时刷新并重试当前请求
func (c *OfficialClient) refreshAndRetry(ctx context.Context, do func(token string) (*OAuthAPIResponse, error)) (*OAuthAPIResponse, error) {
	c.mu.Lock()
	refreshToken := c.RefreshToken
	c.mu.Unlock()
	if refreshToken == "" {
		return nil, fmt.Errorf("官方通道 Token 已过期且无 Refresh Token，请重新授权")
	}
	tokenResp, err := RefreshOfficialToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	c.mu.Lock()
	c.AccessToken = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		c.RefreshToken = tokenResp.RefreshToken
	}
	c.TokenExpiresAt = expiresAt
	c.reauthNeeded = false
	c.mu.Unlock()
	if c.OnTokenRefresh != nil {
		_ = c.OnTokenRefresh(tokenResp.AccessToken, c.RefreshToken, expiresAt)
	}
	return do(tokenResp.AccessToken)
}

// request 执行业务请求（自动带 X-API-Key + Bearer；401 REFRESH_REQUIRED 自动刷新重试一次）
func (c *OfficialClient) request(ctx context.Context, method, path string, payload any) (*OAuthAPIResponse, error) {
	do := func(token string) (*OAuthAPIResponse, error) {
		var bodyReader io.Reader
		if payload != nil {
			raw, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			bodyReader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-API-Key", officialClientSecret)
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		var out OAuthAPIResponse
		_ = json.Unmarshal(raw, &out)
		out.StatusCode = resp.StatusCode
		if resp.StatusCode == http.StatusTooManyRequests && out.Message == "" {
			out.Message = "rate limit exceeded"
		}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			out.RateLimitIdentity = "retry_after:" + ra
		}
		switch strings.TrimSpace(out.Code) {
		case "OPENAPI_REFRESH_REQUIRED":
			// 保留业务码，外层据此刷新重试
		case "OPENAPI_REAUTH_REQUIRED":
			c.mu.Lock()
			c.reauthNeeded = true
			c.mu.Unlock()
		}
		return &out, nil
	}

	c.mu.Lock()
	token := c.AccessToken
	c.mu.Unlock()
	resp, err := do(token)
	if err == nil && resp != nil && resp.Code == "OPENAPI_REFRESH_REQUIRED" {
		return c.refreshAndRetry(ctx, do)
	}
	// 兼容：HTTP 401 且未带业务码时也尝试刷新一次
	if err == nil && resp != nil && resp.StatusCode == http.StatusUnauthorized && resp.Code == "" {
		return c.refreshAndRetry(ctx, do)
	}
	return resp, err
}

// ------------------------- ChannelClient 实现 -------------------------

// Ping 验证应用 Secret（meta 接口，无需用户 Token）
func (c *OfficialClient) Ping(ctx context.Context) error {
	resp, err := c.request(ctx, http.MethodGet, officialPingPath, nil)
	if err != nil {
		return err
	}
	if !resp.Success {
		msg := firstNonEmptyStr(resp.Message, resp.Description, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return fmt.Errorf("官方通道 Ping 失败：%s", msg)
	}
	return nil
}

// Me 获取当前授权用户信息
func (c *OfficialClient) Me(ctx context.Context) (*OAuthAPIResponse, error) {
	return c.request(ctx, http.MethodGet, officialMePath, nil)
}

// GetResources 按 TMDB 查询资源列表
func (c *OfficialClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error) {
	return c.request(ctx, http.MethodGet, officialResourcesPath+mediaType+"/"+url.PathEscape(tmdbID), nil)
}

// GetShareDetail 获取分享详情（解锁前积分预检）
func (c *OfficialClient) GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.request(ctx, http.MethodGet, officialSharePath+url.PathEscape(slug), nil)
}

// UnlockResource 解锁资源
func (c *OfficialClient) UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.request(ctx, http.MethodPost, officialUnlockPath, map[string]any{"slug": slug})
}

// Checkin 每日签到
func (c *OfficialClient) Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error) {
	payload := map[string]any{}
	if isGambler {
		payload["is_gambler"] = true
	}
	return c.request(ctx, http.MethodPost, officialCheckinPath, payload)
}

// ReauthNeeded 是否需要重新授权（REAUTH_REQUIRED 后置位）
func (c *OfficialClient) ReauthNeeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reauthNeeded
}
