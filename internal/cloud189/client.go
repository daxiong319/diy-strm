package cloud189

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"crypto/rand"
)

// ---------------------------------------------------------------------------
// 天翼云盘客户端核心（严格复刻 litepan drivers/189Cloud 架构）
//
// 凭据模型：accessToken(32位) + refreshToken(32位) 为持久凭据（DB account 表），
//          sessionKey/sessionSecret 为内存会话（refreshSession 维护，可随时重建）。
// 会话刷新链：refreshToken.do(POST open.e.189.cn) → 新 accessToken →
//          refreshSession(GET getSessionForPC.action?accessToken=&clientSuffix
//          &X-Request-ID) → sessionKey/sessionSecret。
// 签名：HMAC-SHA1(sessionSecret, "SessionKey=..&Operate=..&RequestURI=..&Date=..&params=..")
// 业务 API 统一走 apiURL + HMAC 签名（litepan apiRequest 模式）。
// ---------------------------------------------------------------------------

const (
	pcType = "TELEPC"
	apiVer = "6.2"
	chanID = "web_cloud.189.cn"
)

// Client 天翼云盘客户端
type Client struct {
	mu sync.Mutex

	// 持久凭据（DB 恢复）
	accessToken  string
	refreshToken string

	// 内存会话（可重建）
	sessionKey    string
	sessionSecret string
	loginName     string

	username      string
	password      string
	http          *http.Client
	onTokenChange func(sess TokenSession)
	forceRefresh  bool
}

// NewClient 创建客户端（tokenOrSSON=DB Token 字段，即 accessToken）
func NewClient(username, password, tokenOrSSON string) *Client {
	return &Client{
		username:     username,
		password:     password,
		accessToken:  tokenOrSSON,
		http:         &http.Client{Timeout: 30 * time.Second},
		forceRefresh: tokenOrSSON == "",
	}
}

// SetTokenStore 注入持久化凭据（兼容旧接口名）
func (c *Client) SetTokenStore(accessToken, refreshToken string, _ time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = accessToken
	c.refreshToken = refreshToken
	c.forceRefresh = false
}

// SetOnTokenChange 凭据变化持久化回调
func (c *Client) SetOnTokenChange(fn func(sess TokenSession)) {
	c.onTokenChange = fn
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func randIntn(n int) int {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	v := 0
	for _, x := range b {
		v = v*256 + int(x)
	}
	if v < 0 {
		v = -v
	}
	return v % n
}

func randInt63(n int64) int64 {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var v int64
	for _, x := range b {
		v = v*256 + int64(x)
	}
	if v < 0 {
		v = -v
	}
	return v % n
}

func set189Headers(req *http.Request) {
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", WebURL)
}

func clientSuffix() url.Values {
	return url.Values{
		"clientType": {pcType},
		"version":    {apiVer},
		"channelId":  {chanID},
		"rand":       {fmt.Sprintf("%d_%d", randIntn(100000), randInt63(10000000000))},
	}
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func successResCodeRaw(raw json.RawMessage) bool {
	s := strings.Trim(string(raw), "\" ")
	return s == "0" || s == "0.0"
}

// ---------------------------------------------------------------------------
// 会话刷新链（对齐 litepan doRefresh / refreshSession）
// ---------------------------------------------------------------------------

// doRefresh refreshToken.do → 新 accessToken → refreshSession
func (c *Client) doRefresh(ctx context.Context) error {
	c.mu.Lock()
	refresh := strings.TrimSpace(c.refreshToken)
	c.mu.Unlock()
	if refresh == "" {
		return fmt.Errorf("缺少 refresh_token，请重新扫码登录")
	}

	form := url.Values{}
	form.Set("clientId", AppID)
	form.Set("refreshToken", refresh)
	form.Set("grantType", "refresh_token")
	form.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthURL+"/api/oauth2/refreshToken.do", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	set189Headers(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("refreshToken 请求失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		AccessToken2 string `json:"access_token"`
		RefreshToken string `json:"refreshToken"`
		RefreshTk2   string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("解析 refreshToken 响应失败：%w（%s）", err, truncateStr(string(body), 160))
	}
	access := firstStr(out.AccessToken, out.AccessToken2)
	if access == "" {
		return fmt.Errorf("刷新令牌失败：%s", truncateStr(string(body), 160))
	}
	newRefresh := firstStr(out.RefreshToken, out.RefreshTk2, refresh)

	// 新凭据先落库（litepan：新 refresh_token 先落库）
	c.mu.Lock()
	c.accessToken = access
	c.refreshToken = newRefresh
	c.mu.Unlock()
	if c.onTokenChange != nil {
		c.onTokenChange(TokenSession{AccessToken: access, RefreshToken: newRefresh})
	}
	return c.refreshSession(ctx, access)
}

// refreshSession accessToken → sessionKey/sessionSecret（GET + clientSuffix + X-Request-ID）
func (c *Client) refreshSession(ctx context.Context, accessToken string) error {
	params := clientSuffix()
	params.Set("appId", AppID)
	params.Set("accessToken", accessToken)
	u := APIURL + "/getSessionForPC.action?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	set189Headers(req)
	req.Header.Set("X-Request-ID", newRequestID())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("刷新会话请求失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		ResCode       json.RawMessage `json:"res_code"`
		ResMessage    string          `json:"res_message"`
		AccessToken   string          `json:"accessToken"`
		SessionKey    string          `json:"sessionKey"`
		SessionSecret string          `json:"sessionSecret"`
		LoginName     string          `json:"loginName"`
		RefreshToken  string          `json:"refreshToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("解析会话响应失败：%w（%s）", err, truncateStr(string(body), 160))
	}
	if !successResCodeRaw(out.ResCode) {
		msg := strings.TrimSpace(out.ResMessage)
		if msg == "" {
			msg = "刷新会话失败"
		}
		return fmt.Errorf("%s（%s）", msg, truncateStr(string(body), 120))
	}
	if out.SessionKey == "" || out.SessionSecret == "" {
		return fmt.Errorf("刷新会话响应缺少个人云会话信息：%s", truncateStr(string(body), 160))
	}
	c.mu.Lock()
	c.sessionKey = out.SessionKey
	c.sessionSecret = out.SessionSecret
	c.loginName = out.LoginName
	if out.RefreshToken != "" {
		c.refreshToken = out.RefreshToken
	}
	if out.AccessToken != "" {
		c.accessToken = out.AccessToken
	}
	c.mu.Unlock()
	if c.onTokenChange != nil && out.RefreshToken != "" {
		at := out.AccessToken
		if at == "" {
			at = accessToken
		}
		c.onTokenChange(TokenSession{AccessToken: at, RefreshToken: out.RefreshToken})
	}
	return nil
}

// ---------------------------------------------------------------------------
// 签名（对齐 litepan signatureHeaders / cloud189Signature）
// ---------------------------------------------------------------------------

func (c *Client) hasSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey != "" && c.sessionSecret != ""
}

func (c *Client) signatureHeaders(method, rawURL string) (map[string]string, error) {
	c.mu.Lock()
	sessionKey := c.sessionKey
	sessionSecret := c.sessionSecret
	c.mu.Unlock()
	if sessionKey == "" || sessionSecret == "" {
		return nil, fmt.Errorf("天翼云盘会话未初始化")
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	return map[string]string{
		"Date":         date,
		"SessionKey":   sessionKey,
		"X-Request-ID": newRequestID(),
		"Signature":    cloud189Signature(sessionSecret, sessionKey, method, rawURL, date),
	}, nil
}

func cloud189Signature(secret, sessionKey, method, rawURL, date string) string {
	u, _ := url.Parse(rawURL)
	requestURI := "/"
	if u != nil && u.Path != "" {
		requestURI = u.Path
	}
	text := "SessionKey=" + sessionKey + "&Operate=" + strings.ToUpper(method) + "&RequestURI=" + requestURI + "&Date=" + date
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(text))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

func isSessionExpiredErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "会话已失效") ||
		strings.Contains(err.Error(), "InvalidSessionKey") ||
		strings.Contains(err.Error(), "会话未初始化"))
}

// ---------------------------------------------------------------------------
// API 请求（对齐 litepan apiRequest/signedJSON：会话失效自动刷新重试一次）
// ---------------------------------------------------------------------------

// apiRequest 签名 GET/POST API 请求（业务方法统一入口）
func (c *Client) apiRequest(ctx context.Context, method, rawURL string, params url.Values, out any) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}
	body, err := c.signedJSON(ctx, method, rawURL, params)
	if err == nil {
		if out != nil && len(body) > 0 {
			_ = json.Unmarshal(body, out)
		}
		return nil
	}
	if isSessionExpiredErr(err) {
		if rerr := c.doRefresh(ctx); rerr != nil {
			return rerr
		}
		body, err = c.signedJSON(ctx, method, rawURL, params)
		if err != nil {
			return err
		}
		if out != nil && len(body) > 0 {
			_ = json.Unmarshal(body, out)
		}
		return nil
	}
	return err
}

func (c *Client) signedJSON(ctx context.Context, method, rawURL string, params url.Values) ([]byte, error) {
	query := clientSuffix()
	for k, vs := range params {
		for _, v := range vs {
			query.Add(k, v)
		}
	}
	headers, err := c.signatureHeaders(method, rawURL)
	if err != nil {
		return nil, err
	}
	u := rawURL + "?" + query.Encode()
	var bodyReader io.Reader
	if method == http.MethodPost {
		// POST：litepan 对列表类 API 也用 query 传参；带 form 时用 form
		if len(params) > 0 {
			bodyReader = strings.NewReader(params.Encode())
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, err
	}
	set189Headers(req)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("天翼云盘认证会话已失效")
	}
	if resp.StatusCode != http.StatusOK {
		c.mu.Lock()
		kk := c.sessionKey
		c.mu.Unlock()
		return nil, fmt.Errorf("天翼云盘 API HTTP %d: %s（sessKey=%s）", resp.StatusCode, truncateStr(string(body), 200), truncateStr(kk, 12))
	}
	return body, nil
}

// ensureSession 确保会话可用
func (c *Client) ensureSession(ctx context.Context) error {
	if c.hasSession() && !c.forceRefresh {
		return nil
	}
	return c.doRefresh(ctx)
}

// ---------------------------------------------------------------------------
// 兼容旧接口
// ---------------------------------------------------------------------------

// GetSessionKey 兼容：确保会话并返回 sessionKey
func (c *Client) GetSessionKey(ctx context.Context) (string, error) {
	if err := c.ensureSession(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey, nil
}

// GetSessionKeyWithSecret 返回 sessionKey/secret 对
func (c *Client) GetSessionKeyWithSecret(ctx context.Context) (key, secret string, err error) {
	if err = c.ensureSession(ctx); err != nil {
		return "", "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey, c.sessionSecret, nil
}

// signedGet 兼容旧业务方法签名（内部走 litepan apiRequest）
func (c *Client) signedGet(ctx context.Context, base, path string, query url.Values, _ any) ([]byte, error) {
	return c.apiRequestBytes(ctx, http.MethodGet, base+path, query)
}

// signedPost 兼容旧业务方法签名（form 走 POST body）
func (c *Client) signedPost(ctx context.Context, base, path string, form url.Values, _ any) ([]byte, error) {
	return c.apiRequestFormBytes(ctx, http.MethodPost, base+path, form)
}

// apiRequestBytes GET 版本（带刷新重试），返回原始 body
func (c *Client) apiRequestBytes(ctx context.Context, method, rawURL string, params url.Values) ([]byte, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	body, err := c.signedJSON(ctx, method, rawURL, params)
	if err == nil {
		return body, nil
	}
	if isSessionExpiredErr(err) {
		if rerr := c.doRefresh(ctx); rerr != nil {
			return nil, rerr
		}
		return c.signedJSON(ctx, method, rawURL, params)
	}
	return nil, err
}

// apiRequestFormBytes 表单 POST 版本（带刷新重试），返回原始 body
func (c *Client) apiRequestFormBytes(ctx context.Context, method, rawURL string, form url.Values) ([]byte, error) {
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	do := func() ([]byte, error) {
		query := clientSuffix()
		headers, err := c.signatureHeaders(method, rawURL)
		if err != nil {
			return nil, err
		}
		u := rawURL + "?" + query.Encode()
		req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		set189Headers(req)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("天翼云盘认证会话已失效")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("天翼云盘 API HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))
		}
		return body, nil
	}
	_ = do
	body, err := do()
	if err == nil {
		return body, nil
	}
	if isSessionExpiredErr(err) {
		if rerr := c.doRefresh(ctx); rerr != nil {
			return nil, rerr
		}
		return do()
	}
	return nil, err
}

// LoginByRedirectURL 密码登录最后一步：redirectURL → getSessionForPC → 会话
// 对齐 litepan finalizeQRLogin（POST + redirectURL），成功后凭据齐全并回调持久化
func (c *Client) LoginByRedirectURL(ctx context.Context, redirectURL string) (*TokenSession, error) {
	params := clientSuffix()
	params.Set("appId", AppID)
	params.Set("redirectURL", redirectURL)
	u := APIURL + "/getSessionForPC.action?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	set189Headers(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("换取会话失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		ResCode       json.RawMessage `json:"res_code"`
		ResMessage    string          `json:"res_message"`
		AccessToken   string          `json:"accessToken"`
		SessionKey    string          `json:"sessionKey"`
		SessionSecret string          `json:"sessionSecret"`
		LoginName     string          `json:"loginName"`
		RefreshToken  string          `json:"refreshToken"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析会话响应失败：%w", err)
	}
	if !successResCodeRaw(out.ResCode) {
		msg := strings.TrimSpace(out.ResMessage)
		if msg == "" {
			msg = "换取会话失败"
		}
		return nil, fmt.Errorf("%s（%s）", msg, truncateStr(string(body), 120))
	}
	if out.SessionKey == "" || out.SessionSecret == "" || out.RefreshToken == "" {
		return nil, fmt.Errorf("换取会话响应不完整：%s", truncateStr(string(body), 160))
	}
	c.mu.Lock()
	c.sessionKey = out.SessionKey
	c.sessionSecret = out.SessionSecret
	c.loginName = out.LoginName
	c.accessToken = out.AccessToken
	c.refreshToken = out.RefreshToken
	c.forceRefresh = false
	c.mu.Unlock()
	if c.onTokenChange != nil {
		c.onTokenChange(TokenSession{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken})
	}
	return &TokenSession{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		SessionKey:   out.SessionKey,
	}, nil
}

// LoginByAccessToken accessToken 直接登录（绕开 open.e.189.cn）：
// GET getSessionForPC?accessToken=xxx + clientSuffix + X-Request-ID → 会话四件套
func (c *Client) LoginByAccessToken(ctx context.Context, accessToken string) (*LoginResult, error) {
	if err := c.refreshSession(ctx, accessToken); err != nil {
		return nil, err
	}
	c.mu.Lock()
	sess := &TokenSession{
		AccessToken:  c.accessToken,
		RefreshToken: c.refreshToken,
		SessionKey:   c.sessionKey,
	}
	c.mu.Unlock()
	return &LoginResult{Success: true, Session: sess}, nil
}
