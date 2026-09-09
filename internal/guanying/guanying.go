// Package guanying 观影（guanying）资源站接入（对齐 tgto123 新版 guanying_client）：
// 登录（含点选式验证码）→ 会话 Cookie 保存 → 资源检索。
// 凭据与会话用本机密钥 AES-GCM 加密落盘（helpers.EncryptLocalSecret）。
package guanying

import (
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

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
	"diy-strm/internal/helpers"

	"gorm.io/gorm"
)

// Session 观影会话（加密存储于 discovery_settings 键 guanying_session）
type Session struct {
	Cookies    string `json:"cookies"`     // 序列化 cookie jar
	Username   string `json:"username"`    // 账号提示（可掩码）
	SavedAt    int64  `json:"saved_at"`    // 保存时间
	ExpiresAt  int64  `json:"expires_at"`  // 会话过期时间（0=未知）
	LastCheck  int64  `json:"last_check"`  // 最近校验时间
	LastErr    string `json:"last_error"`  // 最近错误
}

// Credentials 自动恢复凭据（加密存储于键 guanying_credentials）
type Credentials struct {
	Username     string `json:"username"`
	PasswordEnc  string `json:"password_enc"` // helpers.EncryptLocalSecret 密文
}

const (
	sessionKey     = "guanying_session"
	credentialsKey = "guanying_credentials"

	loginURL      = "https://guanying.site/auth/login"
	captchaURL    = "https://guanying.site/auth/captcha"
	searchURL     = "https://guanying.site/api/resources/search"
	userAgentText = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126 Safari/537.36"

	defaultTimeout = 30 * time.Second
)

var (
	httpMu   sync.Mutex
	sharedMu sync.Mutex
	shared   *Client
)

// SettingGet / SettingSet 复用 discovery_settings 键值表存加密数据
func settingGet(key string) (string, bool) {
	var row struct {
		Value string
	}
	if err := db.Db.Table("discovery_settings").Select("value").Where("`key` = ?", key).Take(&row).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			helpers.AppLogger.Warnf("观影读取设置 %s 失败：%v", key, err)
		}
		return "", false
	}
	return row.Value, true
}

func settingSet(key, value string) error {
	setting := discovery.DiscoverySetting{Key: key, Value: value, UpdatedAt: time.Now()}
	return db.Db.Save(&setting).Error
}

// Client 观影客户端（登录态 = cookie jar）
type Client struct {
	http    *http.Client
	baseURL string
}

// SharedClient 返回带已保存会话的共享客户端（无会话也可用，仅限公开接口）
func SharedClient() *Client {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		jar, _ := cookiejar.New(nil)
		shared = &Client{
			http:    &http.Client{Timeout: defaultTimeout, Jar: jar},
			baseURL: "https://guanying.site",
		}
		// 恢复会话
		if raw, ok := settingGet(sessionKey); ok {
			var s Session
			if json.Unmarshal([]byte(raw), &s) == nil && s.Cookies != "" {
				restoreCookies(shared.http.Jar, s.Cookies)
			}
		}
	}
	return shared
}

// restoreCookies 把序列化 cookie 字符串恢复进 jar
func restoreCookies(jar http.CookieJar, raw string) {
	var pairs []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Domain string `json:"domain"`
		Path  string `json:"path"`
	}
	if json.Unmarshal([]byte(raw), &pairs) != nil {
		return
	}
	u, _ := url.Parse("https://guanying.site")
	cookies := make([]*http.Cookie, 0, len(pairs))
	for _, p := range pairs {
		cookies = append(cookies, &http.Cookie{Name: p.Name, Value: p.Value, Domain: p.Domain, Path: p.Path})
	}
	jar.SetCookies(u, cookies)
}

// serializeCookies 导出 jar 内 cookie
func serializeCookies(jar http.CookieJar) string {
	u, _ := url.Parse("https://guanying.site")
	cookies := jar.Cookies(u)
	pairs := make([]map[string]string, 0, len(cookies))
	for _, c := range cookies {
		pairs = append(pairs, map[string]string{"name": c.Name, "value": c.Value, "domain": c.Domain, "path": c.Path})
	}
	raw, _ := json.Marshal(pairs)
	return string(raw)
}

// CaptchaChallenge 验证码挑战（点选式）
type CaptchaChallenge struct {
	AttemptID string `json:"attempt_id"`
	Text      string `json:"text"`  // 提示文字（按顺序点击的字符）
	Image     string `json:"image"` // dataURL 或 base64
	Type      string `json:"type"`  // click
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// StartLogin 发起登录：服务端生成 attempt_id，如需验证码返回挑战。
// upstreamError 为上游返回的错误信息（账号密码错误/IP 限次等）。
func (c *Client) StartLogin(ctx context.Context, username, password, attemptID string) (captchaRequired bool, challenge *CaptchaChallenge, upstreamError string, err error) {
	form := url.Values{"username": {username}, "password": {password}}
	if attemptID != "" {
		form.Set("attempt_id", attemptID)
	}
	body, status, err := c.postForm(ctx, loginURL, form)
	if err != nil {
		return false, nil, "", err
	}
	var resp struct {
		Success         bool              `json:"success"`
		Code            string            `json:"code"`
		Error           string            `json:"error"`
		CaptchaRequired bool              `json:"captcha_required"`
		AttemptID       string            `json:"attempt_id"`
		Captcha         *CaptchaChallenge `json:"captcha"`
	}
	if uerr := json.Unmarshal(body, &resp); uerr != nil {
		return false, nil, "", fmt.Errorf("观影登录响应解析失败（HTTP %d）：%s", status, truncate(body, 200))
	}
	if resp.CaptchaRequired {
		ch := resp.Captcha
		if ch == nil {
			ch = &CaptchaChallenge{AttemptID: resp.AttemptID, Type: "click", Width: 350, Height: 200}
		}
		if ch.AttemptID == "" {
			ch.AttemptID = resp.AttemptID
		}
		return true, ch, "", nil
	}
	if !resp.Success {
		return false, nil, firstNonEmpty(resp.Error, resp.Code, "账号或密码错误"), nil
	}
	// 登录成功：保存会话
	if serr := SaveSession(c, username); serr != nil {
		helpers.AppLogger.Warnf("观影会话保存失败：%v", serr)
	}
	return false, nil, "", nil
}

// GetCaptcha 拉取点选式验证码图片
func (c *Client) GetCaptcha(ctx context.Context, attemptID string) (*CaptchaChallenge, error) {
	if attemptID == "" {
		return nil, fmt.Errorf("缺少 attempt_id")
	}
	form := url.Values{"attempt_id": {attemptID}}
	body, _, err := c.postForm(ctx, captchaURL, form)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			AttemptID string `json:"attempt_id"`
			Text      string `json:"text"`
			Image     string `json:"image"`
			Type      string `json:"type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.Success {
		return nil, fmt.Errorf("观影验证码获取失败：%s", truncate(body, 160))
	}
	return &CaptchaChallenge{
		AttemptID: firstNonEmpty(resp.Data.AttemptID, attemptID),
		Text:      resp.Data.Text,
		Image:     resp.Data.Image,
		Type:      firstNonEmpty(resp.Data.Type, "click"),
		Width:     resp.Data.Width,
		Height:    resp.Data.Height,
	}, nil
}

// VerifyCaptcha 提交点选坐标（顺序与提示字符一致）
func (c *Client) VerifyCaptcha(ctx context.Context, attemptID string, points []map[string]float64) error {
	payload, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "points": points})
	body, _, err := c.postJSON(ctx, captchaURL+"/verify", payload)
	if err != nil {
		return err
	}
	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &resp) != nil || !resp.Success {
		return fmt.Errorf("观影验证码校验失败：%s", firstNonEmpty(resp.Error, truncate(body, 160)))
	}
	return nil
}

// SearchResources 资源检索（聚合 115/123/光鸭/磁力，由上游返回）
func (c *Client) SearchResources(ctx context.Context, title, mediaType string, tmdbID int64, year string) ([]map[string]any, error) {
	payload, _ := json.Marshal(map[string]any{
		"title": title, "media_type": mediaType, "tmdb_id": tmdbID, "year": year,
	})
	body, _, err := c.postJSON(ctx, searchURL, payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success bool              `json:"success"`
		Error   string            `json:"error"`
		Items   []map[string]any  `json:"items"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return nil, fmt.Errorf("观影搜索响应解析失败：%s", truncate(body, 160))
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", firstNonEmpty(resp.Error, "观影搜索失败"))
	}
	return resp.Items, nil
}

// --- 通用请求 ---

func (c *Client) postForm(ctx context.Context, rawURL string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgentText)
	req.Header.Set("Referer", c.baseURL+"/")
	return c.do(req)
}

func (c *Client) postJSON(ctx context.Context, rawURL string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgentText)
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, int, error) {
	httpMu.Lock()
	defer httpMu.Unlock()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return body, resp.StatusCode, err
}

// --- 会话与凭据持久化 ---

// SaveSession 导出当前 cookie jar 加密落盘（cookie 值本身敏感 → 整体加密）
func SaveSession(c *Client, username string) error {
	plain := serializeCookies(c.http.Jar)
	enc, err := helpers.EncryptLocalSecret(plain)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(Session{
		Cookies:   enc,
		Username:  username,
		SavedAt:   time.Now().Unix(),
		LastCheck: time.Now().Unix(),
	})
	return settingSet(sessionKey, string(raw))
}

// SaveCredentials 保存自动恢复凭据（密码本机加密）
func SaveCredentials(username, password string) error {
	enc, err := helpers.EncryptLocalSecret(password)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(Credentials{Username: username, PasswordEnc: enc})
	return settingSet(credentialsKey, string(raw))
}

// LoadCredentials 读取凭据（无则返回 false）
func LoadCredentials() (string, string, bool) {
	raw, ok := settingGet(credentialsKey)
	if !ok {
		return "", "", false
	}
	var cred Credentials
	if json.Unmarshal([]byte(raw), &cred) != nil || cred.Username == "" {
		return "", "", false
	}
	password, err := helpers.DecryptLocalSecret(cred.PasswordEnc)
	if err != nil {
		helpers.AppLogger.Warnf("观影凭据解密失败：%v", err)
		return "", "", false
	}
	return cred.Username, password, true
}

// SessionStatus 返回会话状态（脱敏，不回传任何凭据/cookie）
func SessionStatus() map[string]any {
	raw, ok := settingGet(sessionKey)
	if !ok {
		return map[string]any{"configured": false, "session_saved": false}
	}
	var s Session
	if json.Unmarshal([]byte(raw), &s) != nil {
		return map[string]any{"configured": false, "session_saved": false}
	}
	saved := s.Cookies != ""
	return map[string]any{
		"configured":        saved,
		"session_saved":     saved,
		"credentials_saved": hasCredentials(),
		"account_hint":      maskAccount(s.Username),
		"session_expires_at": func() string {
			if s.ExpiresAt <= 0 {
				return ""
			}
			return time.Unix(s.ExpiresAt, 0).Format(time.RFC3339)
		}(),
		"last_checked_at": func() string {
			if s.LastCheck <= 0 {
				return ""
			}
			return time.Unix(s.LastCheck, 0).Format(time.RFC3339)
		}(),
		"last_error": s.LastErr,
	}
}

// ClearSession 清除会话与凭据
func ClearSession() error {
	for _, key := range []string{sessionKey, credentialsKey} {
		if err := db.Db.Table("discovery_settings").Where("`key` = ?", key).Delete(nil).Error; err != nil {
			return err
		}
	}
	sharedMu.Lock()
	shared = nil
	sharedMu.Unlock()
	return nil
}

func hasCredentials() bool {
	raw, ok := settingGet(credentialsKey)
	return ok && raw != ""
}

func maskAccount(name string) string {
	if name == "" {
		return ""
	}
	r := []rune(name)
	if len(r) <= 3 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-1:])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(b []byte, n int) string {
	s := string(b)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
