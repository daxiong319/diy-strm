// Package hdhive —— Symedia 中转通道客户端。
//
// hdhive.symedia.top 是 Symedia 提供的影巢（HDHive）OpenAPI 中转服务
// （协议经镜像脱壳 + 黑盒验证完全还原）：
//   - 共享密钥 HMAC proof 握手建立会话（POST /api/v1/auth/session）
//   - HKDF-SHA256 派生会话密钥（注意 Extract 阶段 hmac key=salt、msg=secret）
//   - 每个请求带 X-Proxy-Session / X-Proxy-Sequence（从 1 递增）/ X-Proxy-Body-SHA256
//     / X-Proxy-User-Key / X-Proxy-Signature（HMAC-SHA256(session_key, 签名串)）
//   - 会话有效期 6 小时，提前 60 秒自动重握；403 签名无效时重置会话重试一次
//
// 与 tgtodrive 中转通道（*OAuthClient）互为备份，上层通过 ChannelClient 抽象统一调度，
// symedia 作为主渠道（超时更短，快速失败快速切换）。
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
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Symedia 中转常量
const (
	DefaultSymediaBaseURL = "https://hdhive.symedia.top"
	// SymediaTimeout 主渠道超时：比默认 30s 短，故障时快速切换备用渠道
	SymediaTimeout = 12 * time.Second
	// symediaSessionLinger 会话提前重握余量
	symediaSessionLinger = 60 * time.Second
	// symediaSessionPath 握手端点
	symediaSessionPath = "/api/v1/auth/session"
	// symediaSessionKeyInfo HKDF info 参数
	symediaSessionKeyInfo = "hdhive-openproxy-session-key"
	// symediaSessionKeyLen 会话密钥长度（32 字节）
	symediaSessionKeyLen = 32
	// symediaProofPrefix proof 串前缀（role=client/server 共用）
	symediaProofPrefix = "hdhive-openproxy-proof\n"
)

// 默认共享密钥（symedia 镜像 config 明文，可用环境变量覆盖）
var defaultSymediaSharedSecret = []byte("qXo2bETzfGpJlIBiEkRLGqnB3oY1Sb9NiHrALR5_ggdlnr8409MbApGVZbKrAr3X")

// SymediaClient Symedia 中转通道客户端（有状态：持有一个账号的 userid + proxy_user_key）
type SymediaClient struct {
	BaseURL      string
	UserID       string // hdhive 用户 ID（OAuth 回调回传）
	ProxyUserKey string // 用户密钥（OAuth 回调回传，签名请求必需）
	HTTP         *http.Client
	secret       []byte

	mu            sync.Mutex
	sessionID     string
	sessionKey    []byte
	sessionExpiry time.Time
	sequence      int64 // 下一个请求的序列号（首个请求为 1）
}

// NewSymediaClient 创建 Symedia 通道客户端
func NewSymediaClient(userID, proxyUserKey string) *SymediaClient {
	secret := defaultSymediaSharedSecret
	if env := os.Getenv("HDHIVE_SYMEDIA_SHARED_SECRET"); env != "" {
		secret = []byte(env)
	}
	baseURL := DefaultSymediaBaseURL
	if env := os.Getenv("HDHIVE_SYMEDIA_BASE_URL"); env != "" {
		baseURL = strings.TrimRight(env, "/")
	}
	return &SymediaClient{
		BaseURL:      baseURL,
		UserID:       userID,
		ProxyUserKey: proxyUserKey,
		HTTP:         &http.Client{Timeout: SymediaTimeout},
		secret:       secret,
	}
}

// ---------------------------------------------------------------------------
// HKDF-SHA256（RFC 5869 手写实现，无外部依赖）
// ---------------------------------------------------------------------------

// hkdfSHA256 派生密钥：PRK = HMAC-SHA256(key=salt, msg=secret)，再逐块 Expand。
// 注意：Extract 阶段 hmac 参数顺序是 key=salt、msg=secret（RFC 标准，写反必 403）。
func hkdfSHA256(secret, salt, info []byte, length int) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	extract := hmac.New(sha256.New, salt)
	extract.Write(secret)
	prk := extract.Sum(nil)

	out := make([]byte, 0, length)
	var block []byte
	for counter := byte(1); len(out) < length; counter++ {
		expand := hmac.New(sha256.New, prk)
		expand.Write(block)
		expand.Write(info)
		expand.Write([]byte{counter})
		block = expand.Sum(nil)
		out = append(out, block...)
	}
	return out[:length]
}

// ---------------------------------------------------------------------------
// 会话（握手）
// ---------------------------------------------------------------------------

// handshakeResponse 握手响应 data 结构
type handshakeResponse struct {
	SessionID   string `json:"session_id"`
	ServerNonce string `json:"server_nonce"`
	ServerProof string `json:"server_proof"`
	ExpiresIn   int64  `json:"expires_in"`
}

// handshake 建立会话（带重试：一次失败后清空重置再试一次）
func (c *SymediaClient) handshake(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		sess, err := c.handshakeOnce(ctx)
		if err == nil {
			c.mu.Lock()
			c.sessionID = sess.SessionID
			c.sessionKey = sess.Key
			c.sessionExpiry = time.Now().Add(time.Duration(sess.ExpiresIn)*time.Second - symediaSessionLinger)
			c.sequence = 0 // 下次请求从 1 开始
			c.mu.Unlock()
			return nil
		}
		c.mu.Lock()
		c.sessionID = ""
		c.sessionKey = nil
		c.sessionExpiry = time.Time{}
		c.mu.Unlock()
		if attempt == 0 {
			continue
		}
		return err
	}
	return fmt.Errorf("Symedia 会话握手失败")
}

// handshakeOnce 单次握手
func (c *SymediaClient) handshakeOnce(ctx context.Context) (*sessionDerived, error) {
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	clientNonce := base64.RawURLEncoding.EncodeToString(nonceBytes) // token_urlsafe(32) 等价
	proof := c.proof(clientNonce)

	body, _ := json.Marshal(map[string]string{
		"client_nonce": clientNonce,
		"client_proof": proof,
	})

	u := c.BaseURL + symediaSessionPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Symedia 握手请求失败：%v", err)
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("Symedia 握手响应读取失败：%v", err)
	}

	var env struct {
		Success *bool           `json:"success"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(text, &env); err != nil {
		return nil, fmt.Errorf("Symedia 握手响应解析失败（HTTP %d）：%v", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || (env.Success != nil && !*env.Success) || len(env.Data) == 0 {
		msg := env.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("Symedia 握手失败：%s", msg)
	}
	var hs handshakeResponse
	if err := json.Unmarshal(env.Data, &hs); err != nil || hs.SessionID == "" || hs.ServerNonce == "" {
		return nil, fmt.Errorf("Symedia 握手响应缺少会话字段")
	}

	// 校验服务端 proof（role=server）
	expect := c.proofWithRole("server", hs.ServerNonce)
	if !hmac.Equal([]byte(expect), []byte(hs.ServerProof)) {
		return nil, fmt.Errorf("Symedia 握手服务端 proof 校验失败")
	}

	salt := []byte("hdhive-openproxy-session:" + clientNonce + ":" + hs.ServerNonce)
	key := hkdfSHA256(c.secret, salt, []byte(symediaSessionKeyInfo), symediaSessionKeyLen)
	expires := hs.ExpiresIn
	if expires <= 0 {
		expires = 21600
	}
	return &sessionDerived{SessionID: hs.SessionID, Key: key, ExpiresIn: expires}, nil
}

// sessionDerived 握手派生结果
type sessionDerived struct {
	SessionID string
	Key       []byte
	ExpiresIn int64
}

// proof 计算 client 角色 proof
func (c *SymediaClient) proof(clientNonce string) string {
	return c.proofWithRole("client", clientNonce)
}

// proofWithRole 计算指定角色的 proof：HMAC-SHA256(secret, prefix+role+"\n"+nonce)
func (c *SymediaClient) proofWithRole(role, nonce string) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(symediaProofPrefix + role + "\n" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// ensureSession 确保会话有效（无会话或过期则握手）
func (c *SymediaClient) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	valid := c.sessionID != "" && time.Now().Before(c.sessionExpiry)
	c.mu.Unlock()
	if valid {
		return nil
	}
	return c.handshake(ctx)
}

// ---------------------------------------------------------------------------
// 签名请求
// ---------------------------------------------------------------------------

// request 发送签名请求并解析统一响应。
// 响应为 {"success","code","message","data"} 信封；HTTP 200 且无 success 字段时
// 视为成功并把整个响应体作为 data（部分端点平铺返回如 status）。
func (c *SymediaClient) request(ctx context.Context, method, path string, payload any) (*OAuthAPIResponse, error) {
	var bodyBytes []byte
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("序列化请求 body 失败：%v", err)
		}
		bodyBytes = data
	}

	// 会话失效（403 签名无效）时清空重握重试一次
	for attempt := 0; attempt < 2; attempt++ {
		if err := c.ensureSession(ctx); err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.sequence++
		seq := c.sequence
		sessionID := c.sessionID
		sessionKey := c.sessionKey
		c.mu.Unlock()

		bodyHash := bodySHA256(bodyBytes)
		parsed, _ := url.Parse(path)
		pathURL := parsed.Path
		if pathURL == "" {
			pathURL = path
		}
		signStr := strings.Join([]string{
			strings.ToUpper(method),
			pathURL,
			sessionID,
			fmt.Sprintf("%d", seq),
			bodyHash,
			c.ProxyUserKey,
		}, "\n")

		mac := hmac.New(sha256.New, sessionKey) // 原始 32 字节，非 hex
		mac.Write([]byte(signStr))
		sig := hex.EncodeToString(mac.Sum(nil))

		u := c.BaseURL + path
		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), u, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Proxy-Session", sessionID)
		req.Header.Set("X-Proxy-Sequence", fmt.Sprintf("%d", seq))
		req.Header.Set("X-Proxy-Body-SHA256", bodyHash)
		req.Header.Set("X-Proxy-User-Key", c.ProxyUserKey)
		req.Header.Set("X-Proxy-Signature", sig)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Symedia 请求失败：%v", err)
		}
		text, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("Symedia 响应读取失败：%v", rerr)
		}

		out := parseSymediaResponse(text, resp.StatusCode)
		if resp.StatusCode == http.StatusForbidden && strings.Contains(out.Message, "密钥错误") {
			c.mu.Lock()
			c.sessionID = ""
			c.sessionKey = nil
			c.sessionExpiry = time.Time{}
			c.mu.Unlock()
			continue // 重置会话重试一次
		}
		return out, nil
	}
	return nil, fmt.Errorf("Symedia 请求失败：会话重置后仍无效")
}

// parseSymediaResponse 宽容解析响应：支持 success 信封与平铺对象两种格式
func parseSymediaResponse(text []byte, statusCode int) *OAuthAPIResponse {
	out := &OAuthAPIResponse{StatusCode: statusCode}
	var env struct {
		Success *bool           `json:"success"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(text, &env); err != nil {
		// 非 JSON：透传原始文本到 message
		out.Success = false
		out.Message = strings.TrimSpace(string(text))
		if out.Message == "" {
			out.Message = fmt.Sprintf("HTTP %d", statusCode)
		}
		return out
	}
	if env.Success != nil {
		out.Success = *env.Success
	} else {
		out.Success = statusCode >= 200 && statusCode < 300
	}
	out.Code = env.Code
	out.Message = env.Message
	if len(env.Data) > 0 {
		out.Data = env.Data
	} else if out.Success && len(text) > 0 && env.Success == nil {
		// 无 data 字段的平铺对象/数组（如 {"authorized":true,...}），整体作为 data
		out.Data = text
	}
	return out
}

// ---------------------------------------------------------------------------
// OAuth 授权
// ---------------------------------------------------------------------------

// StartOAuth 发起 OAuth 授权，返回 hdhive.com 授权页 URL
// POST /api/v1/oauth/start?callback={回调URL}；服务端授权后回传
// userid/proxy_user_key/refresh_expires_at 到 callback。
func (c *SymediaClient) StartOAuth(ctx context.Context, callbackURL string) (string, error) {
	path := "/api/v1/oauth/start"
	if callbackURL != "" {
		path += "?callback=" + url.QueryEscape(callbackURL)
	}
	resp, err := c.request(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		msg := resp.Message
		if msg == "" {
			msg = "发起授权失败"
		}
		return "", fmt.Errorf("Symedia OAuth 发起失败：%s", msg)
	}
	var d struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil || d.AuthorizeURL == "" {
		return "", fmt.Errorf("Symedia OAuth 响应缺少 authorize_url")
	}
	return d.AuthorizeURL, nil
}

// ---------------------------------------------------------------------------
// 与 ChannelClient 对齐的业务方法（路径带 userid）
// ---------------------------------------------------------------------------

// Ping 健康检查：会话握手成功即视为通道可用
func (c *SymediaClient) Ping(ctx context.Context) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}
	return nil
}

// Me 当前授权用户信息（GET /api/v1/users/{userid}/status）
func (c *SymediaClient) Me(ctx context.Context) (*OAuthAPIResponse, error) {
	if c.UserID == "" {
		return nil, fmt.Errorf("Symedia 账号未绑定 userid，请先完成授权")
	}
	return c.request(ctx, http.MethodGet, "/api/v1/users/"+url.PathEscape(c.UserID)+"/status", nil)
}

// GetResources 按 TMDB 查询资源（GET /api/v1/open/{userid}/resources/{type}/{tmdb_id}）
func (c *SymediaClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/v1/open/%s/resources/%s/%s", c.UserID, mediaType, tmdbID)
	return c.request(ctx, http.MethodGet, path, nil)
}

// GetShareDetail 获取分享详情（GET /api/v1/open/{userid}/shares/{slug}）
func (c *SymediaClient) GetShareDetail(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/v1/open/%s/shares/%s", c.UserID, url.PathEscape(slug))
	return c.request(ctx, http.MethodGet, path, nil)
}

// UnlockResource 解锁资源（POST /api/v1/open/{userid}/resources/unlock {slug}）
func (c *SymediaClient) UnlockResource(ctx context.Context, slug string) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/v1/open/%s/resources/unlock", c.UserID)
	return c.request(ctx, http.MethodPost, path, map[string]string{"slug": slug})
}

// Checkin 每日签到（POST /api/v1/open/{userid}/checkin?is_gambler=）
func (c *SymediaClient) Checkin(ctx context.Context, isGambler bool) (*OAuthAPIResponse, error) {
	path := fmt.Sprintf("/api/v1/open/%s/checkin", c.UserID)
	if isGambler {
		path += "?is_gambler=1"
	}
	return c.request(ctx, http.MethodPost, path, nil)
}
