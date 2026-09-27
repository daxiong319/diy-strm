package cloud189

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client 天翼云盘客户端（SessionKey + AccessToken 双令牌，自动刷新重试）
type Client struct {
	username   string
	password   string
	ssonCookie string

	http    *http.Client
	session TokenSession
	store   *tokenStoreData

	mu            sync.Mutex
	forceRefresh  bool
	onTokenChange func(session TokenSession) // 令牌变化回调（持久化）
}

// NewClient 创建客户端（username/password 登录或 ssonCookie 任一）
func NewClient(username, password, ssonCookie string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		username:   username,
		password:   password,
		ssonCookie: ssonCookie,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}
}

// SetTokenStore 注入持久化令牌（从账号表恢复）
func (c *Client) SetTokenStore(accessToken, refreshToken string, expiresAt time.Time) {
	c.store = &tokenStoreData{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: expiresAt}
}

// SetOnTokenChange 令牌变化回调
func (c *Client) SetOnTokenChange(fn func(TokenSession)) {
	c.onTokenChange = fn
}

// GetSessionKey 获取 sessionKey（触发完整会话链：accessToken→refreshToken→SSON→密码）
func (c *Client) GetSessionKey(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.SessionKey != "" && !c.forceRefresh {
		return c.session.SessionKey, nil
	}
	sess, err := c.getSession(ctx)
	if err != nil {
		return "", err
	}
	c.session = *sess
	c.forceRefresh = false
	if c.onTokenChange != nil {
		c.onTokenChange(*sess)
	}
	return sess.SessionKey, nil
}

// GetAccessToken 获取 accessToken（WEB_URL API 签名用）
func (c *Client) GetAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session.AccessToken != "" && !c.forceRefresh {
		return c.session.AccessToken, nil
	}
	// accessToken 由 sessionKey 换发
	if _, err := c.getSessionKeyLocked(ctx); err != nil {
		return "", err
	}
	at, err := c.getAccessTokenBySsKey(ctx)
	if err != nil {
		return "", err
	}
	c.session.AccessToken = at
	return at, nil
}

// getSessionKeyLocked 内部（已持锁）
func (c *Client) getSessionKeyLocked(ctx context.Context) (string, error) {
	if c.session.SessionKey != "" {
		return c.session.SessionKey, nil
	}
	sess, err := c.getSession(ctx)
	if err != nil {
		return "", err
	}
	c.session = *sess
	if c.onTokenChange != nil {
		c.onTokenChange(*sess)
	}
	return sess.SessionKey, nil
}

// getSession 完整会话链（对齐 SDK getSession：token 恢复 → refreshToken → SSON → 密码）
func (c *Client) getSession(ctx context.Context) (*TokenSession, error) {
	// 1. 持久化 accessToken 直接复用
	if c.store != nil && c.store.AccessToken != "" && !c.forceRefresh && time.Now().Before(c.store.ExpiresAt) {
		if sess, err := c.loginByAccessToken(ctx, c.store.AccessToken); err == nil {
			return sess, nil
		}
	}
	// 2. refreshToken 换发
	if c.store != nil && c.store.RefreshToken != "" {
		if rt, err := c.refreshToken(ctx, c.store.RefreshToken); err == nil {
			c.store = &tokenStoreData{
				AccessToken:  rt.AccessToken,
				RefreshToken: rt.RefreshToken,
				ExpiresAt:    time.Now().Add(6 * 24 * time.Hour),
			}
			return c.loginByAccessToken(ctx, rt.AccessToken)
		}
	}
	// 3. SSON Cookie 登录
	if c.ssonCookie != "" {
		if sess, err := c.loginBySsoCookie(ctx, c.ssonCookie); err == nil {
			c.store = &tokenStoreData{
				AccessToken:  sess.AccessToken,
				RefreshToken: sess.RefreshToken,
				ExpiresAt:    time.Now().Add(6 * 24 * time.Hour),
			}
			return sess, nil
		}
	}
	// 4. 密码登录
	if c.username != "" && c.password != "" {
		res, err := c.LoginByPassword(ctx, c.username, c.password, "")
		if err != nil {
			return nil, err
		}
		if res.Success && res.Session != nil {
			c.store = &tokenStoreData{
				AccessToken:  res.Session.AccessToken,
				RefreshToken: res.Session.RefreshToken,
				ExpiresAt:    time.Now().Add(6 * 24 * time.Hour),
			}
			return res.Session, nil
		}
		return nil, fmt.Errorf("登录失败: %s", res.Message)
	}
	return nil, fmt.Errorf("无法获取会话：无有效凭据")
}

// ---------------------------------------------------------------------------
// 签名请求中间件（对齐 SDK beforeRequest hooks）
// ---------------------------------------------------------------------------

// signedGet 带签名 GET（path 相对 base）
// base: WEB_URL → sessionKey query + open appkey 签名；API_URL → accessToken 签名
func (c *Client) signedGet(ctx context.Context, base, path string, query url.Values, jsonBody any) ([]byte, error) {
	return c.signedRequest(ctx, http.MethodGet, base, path, query, jsonBody)
}

func (c *Client) signedPost(ctx context.Context, base, path string, form url.Values, jsonBody any) ([]byte, error) {
	return c.signedRequest(ctx, http.MethodPost, base, path, form, jsonBody)
}

func (c *Client) signedRequest(ctx context.Context, method, base, path string, query url.Values, jsonBody any) ([]byte, error) {
	u, err := url.Parse(base + path)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, vs := range query {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())

	var bodyReader io.Reader
	sigParams := map[string]string{}
	if method == http.MethodGet {
		for k, vs := range q {
			if len(vs) > 0 {
				sigParams[k] = vs[0]
			}
		}
	} else {
		if jsonBody != nil {
			raw, _ := json.Marshal(jsonBody)
			bodyReader = bytes.NewReader(raw)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			for k, v := range m {
				sigParams[k] = fmt.Sprintf("%v", v)
			}
		} else if query != nil {
			bodyReader = strings.NewReader(query.Encode())
			for k, vs := range query {
				if len(vs) > 0 {
					sigParams[k] = vs[0]
				}
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", WebURL+"/web/main/")
	req.Header.Set("Accept", "application/json;charset=UTF-8")

	if base == APIURL {
		// API_URL 走 accessToken 签名
		accessToken, err := c.GetAccessToken(ctx)
		if err != nil {
			return nil, err
		}
		sigParams["Timestamp"] = timestamp
		sigParams["AccessToken"] = accessToken
		req.Header.Set("Sign-Type", "1")
		req.Header.Set("Signature", getSignature(sigParams))
		req.Header.Set("Timestamp", timestamp)
		req.Header.Set("Accesstoken", accessToken)
	} else if base == WebURL {
		// WEB_URL：/open 前缀加 appkey 签名 + sessionKey query
		if strings.Contains(path, "/open") {
			sigParams["Timestamp"] = timestamp
			sigParams["AppKey"] = OpenAppKey
			req.Header.Set("Sign-Type", "1")
			req.Header.Set("Signature", getSignature(sigParams))
			req.Header.Set("Timestamp", timestamp)
			req.Header.Set("AppKey", OpenAppKey)
		}
		sessionKey, err := c.GetSessionKey(ctx)
		if err != nil {
			return nil, err
		}
		qq := req.URL.Query()
		qq.Set("sessionKey", sessionKey)
		req.URL.RawQuery = qq.Encode()
	}

	if method == http.MethodPost {
		if jsonBody != nil {
			req.Header.Set("Content-Type", "application/json;charset=UTF-8")
		} else {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
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

	// 400 错误处理：InvalidAccessToken / InvalidSessionKey 自动刷新重试一次
	if resp.StatusCode == 400 {
		var e struct {
			ErrorCode string `json:"errorCode"`
			ErrorMsg  string `json:"errorMsg"`
		}
		if json.Unmarshal(body, &e) == nil {
			if e.ErrorCode == "InvalidAccessToken" || e.ErrorCode == "InvalidSessionKey" {
				c.mu.Lock()
				c.session.AccessToken = ""
				c.session.SessionKey = ""
				c.forceRefresh = true
				c.mu.Unlock()
				// 重试一次（重建请求）
				return c.signedRequest(ctx, method, base, path, query, jsonBody)
			}
		}
		return body, fmt.Errorf("天翼云盘接口错误（HTTP 400）: %s", truncateStr(string(body), 200))
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("天翼云盘接口错误（HTTP %d）: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	return body, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
