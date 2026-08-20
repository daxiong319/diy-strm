// Package hdhive 提供影巢（HDHive）Open API 与 OAuth 签名客户端。
//
// 本文件实现 OAuth 签名认证客户端（install_id + HMAC-SHA256），
// 参考 tgto123 的 hdhive_user_client 模块实现。
package hdhive

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OAuth 默认常量
// 注意：base URL 为 TgtoDrive 提供的影巢代理服务（tgto123 同款）。
// 签名密钥来自该代理服务，而非 hdhive.com 官方 Open API（官方走 X-API-Key）。
const (
	DefaultOAuthBaseURL = "https://hdhive-open.tgtodrive.top"
	DefaultOAuthTimeout = 30 * time.Second
	OAuthMaxRetries     = 3
)

// 默认共享密钥（从 tgto123 镜像 load_shared_secret_bytes() 提取）
var defaultOAuthSharedSecret = []byte("d45WcKoZp6dk9bXKpHG-mndXIEGhug36f20jYo5jWeuL7MLEIIYavUGEmPHSxCqV7pBCJ3pOr24qAT8nu9bu_A")

// OAuthClient HDHive OAuth 签名客户端
type OAuthClient struct {
	BaseURL    string
	InstallID string
	HTTP      *http.Client
	secret    []byte
}

// oauthUserAgent 与 tgto123 客户端一致的 User-Agent（Cloudflare 校验）
const oauthUserAgent = "TgtoDrive-HDHive-Client/1.0"

// NewOAuthClient 创建 OAuth 客户端。
// installID 为空时仅用于签名计算，调用方需在使用前设置。
func NewOAuthClient(installID string) *OAuthClient {
	secret := defaultOAuthSharedSecret
	if env := os.Getenv("HDHIVE_APP_SHARED_SECRET"); env != "" {
		secret = []byte(env)
	}
	baseURL := DefaultOAuthBaseURL
	if env := os.Getenv("HDHIVE_USER_SERVER_BASE_URL"); env != "" {
		baseURL = strings.TrimRight(env, "/")
	}
	return &OAuthClient{
		BaseURL:    baseURL,
		InstallID: installID,
		HTTP: &http.Client{
			Timeout: DefaultOAuthTimeout,
		},
		secret: secret,
	}
}

// Clone 创建配置相同的客户端副本（可安全修改 InstallID）
func (c *OAuthClient) Clone(installID string) *OAuthClient {
	return &OAuthClient{
		BaseURL:    c.BaseURL,
		InstallID: installID,
		HTTP:      c.HTTP,
		secret:    c.secret,
	}
}

// ---------------------------------------------------------------------------
// 签名基础设施
// ---------------------------------------------------------------------------

// bodySHA256 计算 body 的 SHA-256 十六进制字符串
func bodySHA256(body []byte) string {
	if len(body) == 0 {
		body = []byte{}
	}
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// canonicalQueryString 构造规范查询字符串（剔除 sig/signature，键排序，urlencode）
func canonicalQueryString(query url.Values) string {
	// 剔除签名参数
	clean := make(url.Values)
	for k, v := range query {
		kl := strings.ToLower(k)
		if kl == "sig" || kl == "signature" {
			continue
		}
		clean[k] = v
	}
	// 键排序
	keys := make([]string, 0, len(clean))
	for k := range clean {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		for _, v := range clean[k] {
			pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(pairs, "&")
}

// canonicalRequest 构造规范请求字符串（7 行，\n 分隔）
// 顺序：method、norm_path、canonical_query、install_id、timestamp、nonce、body_sha256
func canonicalRequest(method, path string, installID, timestamp, nonce string, body []byte) string {
	parsed, err := url.Parse(path)
	if err != nil {
		parsed = &url.URL{Path: path}
	}
	normPath := strings.TrimRight(parsed.Path, "/")
	if normPath == "" {
		normPath = "/"
	}
	cq := canonicalQueryString(parsed.Query())
	bs := bodySHA256(body)
	return strings.Join([]string{
		strings.ToUpper(method),
		normPath,
		cq,
		installID,
		timestamp,
		nonce,
		bs,
	}, "\n")
}

// signRequest 计算 HMAC-SHA256 签名（十六进制字符串）
func (c *OAuthClient) signRequest(canonical string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// makeAuthHeaders 生成 OAuth 签名请求头
func (c *OAuthClient) makeAuthHeaders(method, path string, body []byte) map[string]string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 16)
	rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	canonical := canonicalRequest(method, path, c.InstallID, ts, nonce, body)
	sig := c.signRequest(canonical)
	return map[string]string{
		"X-Install-Id": c.InstallID,
		"X-Timestamp":  ts,
		"X-Nonce":      nonce,
		"X-Signature":  sig,
	}
}

// BuildAuthURL 构造 OAuth 授权 URL（带签名的 /auth/start）
func (c *OAuthClient) BuildAuthURL() string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 16)
	rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	path := fmt.Sprintf("/auth/start?install_id=%s&ts=%s&nonce=%s",
		url.QueryEscape(c.InstallID), ts, nonce)
	canonical := canonicalRequest("GET", path, c.InstallID, ts, nonce, nil)
	sig := c.signRequest(canonical)
	return fmt.Sprintf("%s%s&sig=%s", c.BaseURL, path, sig)
}

// NewInstallID 生成新的 install_id（secrets.token_urlsafe(48) 等价）
func NewInstallID() string {
	b := make([]byte, 48)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// InstallHash 计算 install_id 的 SHA-256 哈希
func InstallHash(installID string) string {
	h := sha256.Sum256([]byte(installID))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// 请求执行
// ---------------------------------------------------------------------------

// OAuthAPIResponse HDHive OAuth API 统一响应（泛型 data）
type OAuthAPIResponse struct {
	Success     bool            `json:"success"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Description string          `json:"description"`
	Data        json.RawMessage `json:"data"`
	StatusCode  int             `json:"status_code"`

	// 透传字段（由 _compact_api_result 保留）
	AuthRequired         *bool  `json:"auth_required,omitempty"`
	HasAccessToken       *bool  `json:"has_access_token,omitempty"`
	HasRefreshToken      *bool  `json:"has_refresh_token,omitempty"`
	ExpiresAt            *int64 `json:"expires_at,omitempty"`
	ExpiresInSeconds     *int64 `json:"expires_in_seconds,omitempty"`
	RefreshExpiresAt     *int64 `json:"refresh_expires_at,omitempty"`
	RefreshExpiresInSec  *int64 `json:"refresh_expires_in_seconds,omitempty"`
	WillRefreshWithinSec *int64 `json:"will_refresh_within_seconds,omitempty"`
	InstallHash          string `json:"install_hash,omitempty"`
	RateLimitIdentity    string `json:"rate_limit_identity,omitempty"`
	HDHiveUserHash       string `json:"hdhive_user_hash,omitempty"`
	AuthURL              string `json:"auth_url,omitempty"`
}

// requestJSON 发送 OAuth 签名请求，返回原始响应
func (c *OAuthClient) requestJSON(ctx context.Context, method, path string, payload any, params map[string]string) (*OAuthAPIResponse, error) {
	// 组装 URL
	u := c.BaseURL + path
	if len(params) > 0 {
		p := url.Values{}
		for k, v := range params {
			p.Set(k, v)
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + p.Encode()
	}

	// 序列化 body
	var bodyBytes []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("序列化请求 body 失败：%v", err)
		}
		bodyBytes = data
	}

	// 构造签名头
	headers := c.makeAuthHeaders(method, u, bodyBytes)
	headers["Content-Type"] = "application/json"
	headers["User-Agent"] = oauthUserAgent
	headers["Accept"] = "application/json"

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), u, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求影巢 OAuth 失败：%v", err)
	}
	defer resp.Body.Close()

	bodyText, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%v", err)
	}

	var out OAuthAPIResponse
	if err := json.Unmarshal(bodyText, &out); err != nil {
		return nil, fmt.Errorf("解析影巢 OAuth 响应失败：%v", err)
	}
	out.StatusCode = resp.StatusCode
	return &out, nil
}

// requestResult 发送请求并处理 429 重试
func (c *OAuthClient) requestResult(ctx context.Context, method, path string, payload any, params map[string]string) (*OAuthAPIResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= OAuthMaxRetries; attempt++ {
		resp, err := c.requestJSON(ctx, method, path, payload, params)
		if err != nil {
			return nil, err
		}
		// 429 重试
		if resp.StatusCode == http.StatusTooManyRequests && attempt < OAuthMaxRetries {
			retryAfter := retryAfterSeconds(resp)
			time.Sleep(time.Duration(retryAfter) * time.Second)
			continue
		}
		// 检查 auth_required
		if resp.AuthRequired != nil && *resp.AuthRequired && c.InstallID != "" {
			resp.AuthURL = c.BuildAuthURL()
		}
		return resp, nil
	}
	return nil, lastErr
}

// retryAfterSeconds 解析重试等待秒数
func retryAfterSeconds(resp *OAuthAPIResponse) int {
	// 默认最少 5 秒 + 随机
	base := 5
	if resp == nil {
		return base
	}
	// 尝试从 data 中取 retry_after（标准做法是先解析 data 为 map）
	if len(resp.Data) > 0 {
		var m map[string]any
		if json.Unmarshal(resp.Data, &m) == nil {
			if ra, ok := m["retry_after"]; ok {
				if f, ok := ra.(float64); ok {
					return int(math.Max(float64(base), f))
				}
			}
		}
	}
	return base
}

// ---------------------------------------------------------------------------
// API 方法
// ---------------------------------------------------------------------------

// Ping 健康检查（GET /api/ping）
func (c *OAuthClient) Ping(ctx context.Context) error {
	resp, err := c.requestResult(ctx, http.MethodGet, "/api/ping", nil, nil)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("影巢 OAuth Ping 失败：%s", resp.Message)
	}
	return nil
}

// TokenStatus 获取 OAuth 授权状态（GET /api/token/status）
func (c *OAuthClient) TokenStatus(ctx context.Context) (*OAuthAPIResponse, error) {
	return c.requestResult(ctx, http.MethodGet, "/api/token/status", nil, nil)
}

// Me 获取当前用户信息（GET /api/me）
func (c *OAuthClient) Me(ctx context.Context) (*OAuthAPIResponse, error) {
	return c.requestResult(ctx, http.MethodGet, "/api/me", nil, nil)
}

// GetResources 按 TMDB 查询资源（GET /api/resources/{mediaType}/{tmdbID}）
func (c *OAuthClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/resources/%s/%s", mediaType, tmdbID)
	return c.requestResult(ctx, http.MethodGet, path, nil, nil)
}

// GetStreamingTop 获取流媒体榜单（GET /api/feeds/streaming-top）
func (c *OAuthClient) GetStreamingTop(ctx context.Context, provider, region, mediaType string) (*OAuthAPIResponse, error) {
	params := map[string]string{
		"provider":   strings.ToLower(provider),
		"region":     strings.ToUpper(region),
		"media_type": strings.ToLower(mediaType),
	}
	return c.requestResult(ctx, http.MethodGet, "/api/feeds/streaming-top", nil, params)
}

// GetCalendar 获取追剧日历（GET /api/feeds/calendar?days=N）
func (c *OAuthClient) GetCalendar(ctx context.Context, days int) (*OAuthAPIResponse, error) {
	params := map[string]string{"days": strconv.Itoa(days)}
	return c.requestResult(ctx, http.MethodGet, "/api/feeds/calendar", nil, params)
}

// GetShareDetail 获取分享详情（GET /api/shares/{slug}）
func (c *OAuthClient) GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	path := "/api/shares/" + url.PathEscape(slug)
	return c.requestResult(ctx, http.MethodGet, path, nil, nil)
}

// UnlockResource 解锁资源（POST /api/resources/unlock）
func (c *OAuthClient) UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	return c.requestResult(ctx, http.MethodPost, "/api/resources/unlock", map[string]string{
		"slug": slug,
	}, nil)
}

// Checkin 每日签到（POST /api/checkin）
// isGambler=true 时为赌狗模式（body: {"is_gambler": true}），false 为普通模式（body: 空）
func (c *OAuthClient) Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error) {
	var payload any
	if isGambler {
		payload = map[string]bool{"is_gambler": true}
	}
	return c.requestResult(ctx, http.MethodPost, "/api/checkin", payload, nil)
}