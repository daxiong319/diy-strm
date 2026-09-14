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
	"strconv"
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
	Cookies   string `json:"cookies"`    // 序列化 cookie jar
	Username  string `json:"username"`   // 账号提示（可掩码）
	SavedAt   int64  `json:"saved_at"`   // 保存时间
	ExpiresAt int64  `json:"expires_at"` // 会话过期时间（0=未知）
	LastCheck int64  `json:"last_check"` // 最近校验时间
	LastErr   string `json:"last_error"` // 最近错误
}

// Credentials 自动恢复凭据（加密存储于键 guanying_credentials）
type Credentials struct {
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc"` // helpers.EncryptLocalSecret 密文
}

const (
	sessionKey     = "guanying_session"
	credentialsKey = "guanying_credentials"

	loginURL      = "/user/login" // 新版站点登录（传统表单流）
	captchaURL    = "/auth/captcha"
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
	if err := db.Db.Table("discovery_settings").Select("value").Where("key = ?", key).Take(&row).Error; err != nil {
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

// SharedClient 返回带已保存会话的共享客户端（无会话也可用，仅限公开接口）。
// base URL 从防失联页动态解析（guanying.site 已失效的教训：域名会不定期更换）。
func SharedClient() *Client {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil {
		jar, _ := cookiejar.New(nil)
		shared = &Client{
			http:    &http.Client{Timeout: defaultTimeout, Jar: jar},
			baseURL: EnsureFreshBaseURL(context.Background(), false),
		}
		// 注入域名解析阶段取得的 PoW 验证 cookie（未过 PoW 时业务接口全部 404）
		injectResolutionCookies(shared)
		// 恢复会话（Session.Cookies 为加密存储，需先解密）
		if raw, ok := settingGet(sessionKey); ok {
			var s Session
			if json.Unmarshal([]byte(raw), &s) == nil && s.Cookies != "" {
				if plain, err := helpers.DecryptLocalSecret(s.Cookies); err == nil && plain != "" {
					restoreCookiesAt(shared.http.Jar, plain, shared.baseURL)
				}
			}
		}
	}
	return shared
}

// injectResolutionCookies 把最近一次域名解析携带的 PoW 验证 cookie 注入客户端 jar
func injectResolutionCookies(c *Client) {
	res := ResolveBaseURL(context.Background(), false)
	if res == nil || res.Cookies == "" {
		return
	}
	var pairs []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Domain string `json:"domain"`
		Path   string `json:"path"`
	}
	if json.Unmarshal([]byte(res.Cookies), &pairs) != nil {
		return
	}
	u, _ := url.Parse(c.baseURL + "/")
	cookies := make([]*http.Cookie, 0, len(pairs))
	for _, p := range pairs {
		cookies = append(cookies, &http.Cookie{Name: p.Name, Value: p.Value, Domain: p.Domain, Path: p.Path})
	}
	if len(cookies) > 0 {
		c.http.Jar.SetCookies(u, cookies)
	}
}

// refreshEndpoint 业务请求前调用：解析结果与当前 base 不一致（域名更换/PoW 重验）时
// 迁移 baseURL 并重新注入验证 cookie 与已保存会话。
func (c *Client) refreshEndpoint() {
	res := ResolveBaseURL(context.Background(), false)
	if res == nil || res.BaseURL == "" || res.BaseURL == c.baseURL {
		return
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	c.baseURL = res.BaseURL
	injectResolutionCookies(c)
	if raw, ok := settingGet(sessionKey); ok {
		var s Session
		if json.Unmarshal([]byte(raw), &s) == nil && s.Cookies != "" {
			if plain, err := helpers.DecryptLocalSecret(s.Cookies); err == nil && plain != "" {
				restoreCookiesAt(c.http.Jar, plain, c.baseURL)
			}
		}
	}
}

// restoreCookiesAt 把 cookie 恢复到指定站点域
func restoreCookiesAt(jar http.CookieJar, raw, baseURL string) {
	var pairs []struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Domain string `json:"domain"`
		Path   string `json:"path"`
	}
	if json.Unmarshal([]byte(raw), &pairs) != nil {
		return
	}
	u, _ := url.Parse(baseURL)
	cookies := make([]*http.Cookie, 0, len(pairs))
	for _, p := range pairs {
		cookies = append(cookies, &http.Cookie{Name: p.Name, Value: p.Value, Domain: p.Domain, Path: p.Path})
	}
	jar.SetCookies(u, cookies)
}

// serializeCookiesAt 导出 jar 内指定站点域的 cookie
func serializeCookiesAt(jar http.CookieJar, baseURL string) string {
	u, _ := url.Parse(baseURL)
	cookies := jar.Cookies(u)
	pairs := make([]map[string]string, 0, len(cookies))
	for _, c := range cookies {
		pairs = append(pairs, map[string]string{"name": c.Name, "value": c.Value, "domain": c.Domain, "path": c.Path})
	}
	raw, _ := json.Marshal(pairs)
	return string(raw)
}

// isHTMLBody 响应是否为 HTML 页面（PoW 挑战页/错误页/停放页，上游异常时业务接口会回 HTML）
func isHTMLBody(body []byte) bool {
	s := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

// upstreamPageError 上游返回 HTML 页面时的统一友好错误（不透传整页 HTML 到前端）
func upstreamPageError(action string, status int) error {
	InvalidateDomainCache() // 下次请求强制重新解析防失联页（域名/验证可能已更换）
	return fmt.Errorf("观影%s失败：站点返回了安全验证页或错误页（HTTP %d），域名解析已重置，请稍后重试", action, status)
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

// StartLogin 发起登录（新版站点 /user/login 传统表单流）：
// POST 表单（username/password/cookietime）→ 服务端种会话 cookie；
// 成功与否以「首页是否出现退出登录链接」判定（响应本身是 SPA 页面，无 JSON）。
// upstreamError 为上游返回的错误信息（账号密码错误/IP 限次等）。
func (c *Client) StartLogin(ctx context.Context, username, password, attemptID string) (captchaRequired bool, challenge *CaptchaChallenge, upstreamError string, err error) {
	c.refreshEndpoint()
	// 先 GET 登录页建立匿名会话（PHPSESSID），否则服务端会拒绝后续登录 POST
	if pre, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+loginURL, nil); err == nil {
		pre.Header.Set("User-Agent", userAgentText)
		_, _, _ = c.do(pre)
	}
	// 表单体与站点 SPA 完全一致：缺少 dosubmit/siteid 时服务端只会重新渲染登录页（静默失败）
	form := url.Values{
		"code":       {""},
		"siteid":     {"1"},
		"dosubmit":   {"1"},
		"cookietime": {"10506240"},
		"username":   {username},
		"password":   {password},
	}
	_ = attemptID
	// 请求头对齐 SPA fetch：Origin/Referer 缺失时服务端会静默拒绝（重新渲染登录页）
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL+loginURL)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", userAgentText)
	body, status, err := c.do(req)
	if err != nil {
		return false, nil, "", err
	}
	u, _ := url.Parse(c.baseURL + "/")
	helpers.AppLogger.Infof("观影登录 POST：%s -> %d | body %d 字节 | jar cookies %d（%v）| 响应头 Set-Cookie 存在=%v",
		c.baseURL+loginURL, status, len(body), len(c.http.Jar.Cookies(u)), cookieNames(c.http.Jar, u), status > 0)
	// 新版契约：POST 响应为 JSON {code:200} / {code:xxx, msg:"..."}
	if !isHTMLBody(body) {
		var resp struct {
			Code any    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(body, &resp) == nil && resp.Code != nil {
			codeNum := -1
			switch v := resp.Code.(type) {
			case float64:
				codeNum = int(v)
			case string:
				codeNum, _ = strconv.Atoi(strings.TrimSpace(v))
			}
			if codeNum == 200 {
				if serr := SaveSession(c, username); serr != nil {
					helpers.AppLogger.Warnf("观影会话保存失败：%v", serr)
				}
				return false, nil, "", nil
			}
			return false, nil, firstNonEmpty(resp.Msg, fmt.Sprintf("登录被拒绝（code=%v）", resp.Code)), nil
		}
	}
	// 兜底：HTML 响应时以首页用户名/退出链接判定登录态
	if !c.isLoggedIn(ctx, username) {
		return false, nil, fmt.Sprintf("账号或密码错误（HTTP %d）", status), nil
	}
	if serr := SaveSession(c, username); serr != nil {
		helpers.AppLogger.Warnf("观影会话保存失败：%v", serr)
	}
	return false, nil, "", nil
}

// isLoggedIn 登录态判定：请求首页，HTML 含退出登录链接或当前用户名即视为已登录
func (c *Client) isLoggedIn(ctx context.Context, username string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", userAgentText)
	body, _, err := c.do(req)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), "/user/logout") ||
		(username != "" && strings.Contains(string(body), username))
}

// GetCaptcha 拉取点选式验证码图片
func (c *Client) GetCaptcha(ctx context.Context, attemptID string) (*CaptchaChallenge, error) {
	if attemptID == "" {
		return nil, fmt.Errorf("缺少 attempt_id")
	}
	form := url.Values{"attempt_id": {attemptID}}
	body, _, err := c.postForm(ctx, c.baseURL+captchaURL, form)
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
	body, _, err := c.postJSON(ctx, c.baseURL+captchaURL+"/verify", payload)
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
// SearchResources 资源检索（新版站点契约）：
// GET /res/search?q=标题 匹配影片（inlist.i=影片ID, d=目录 mv/tv/ac）→
// GET /res/downurl/{d}/{i} 取资源列表。响应含两段：
//
//	panlist：网盘分享（url/name/type/p 提取码/time/gid，url 空=待审）——直链明文；
//	downlist：磁力/种子（m=infohash, u/t/s/e）。
//
// 映射为统一资源条目（provider=网盘类型或 magnet）。
func (c *Client) SearchResources(ctx context.Context, title, mediaType string, tmdbID int64, year string) ([]map[string]any, error) {
	c.refreshEndpoint()
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("观影检索需要标题")
	}
	u := fmt.Sprintf("%s/res/search?q=%s", c.baseURL, url.QueryEscape(title))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgentText)
	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if isHTMLBody(body) {
		return nil, upstreamPageError("搜索", status)
	}
	var sr struct {
		Inlist map[string]any `json:"inlist"`
	}
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("观影搜索响应解析失败：%s", truncate(body, 160))
	}
	if len(sr.Inlist) == 0 {
		return nil, fmt.Errorf("观影未收录该片：%s", title)
	}
	// 标题校验：/res/search 为模糊匹配，标题完全无关时按未收录处理
	hay := ""
	for _, k := range []string{"title", "name", "ename"} {
		if arr, ok := sr.Inlist[k].([]any); ok && len(arr) > 0 {
			if v, ok := arr[0].(string); ok {
				hay += strings.ToLower(v) + " "
			}
		}
	}
	if hay != "" && !strings.Contains(hay, strings.ToLower(title)) {
		return nil, fmt.Errorf("观影未收录精确匹配：《%s》", title)
	}
	ids, _ := sr.Inlist["i"].([]any)
	dirs, _ := sr.Inlist["d"].([]any)
	if len(ids) == 0 || len(dirs) == 0 {
		return nil, fmt.Errorf("观影已收录《%s》但影片标识缺失", title)
	}
	filmID, _ := ids[0].(string)
	dir, _ := dirs[0].(string)

	du := fmt.Sprintf("%s/res/downurl/%s/%s", c.baseURL, dir, filmID)
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, du, nil)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Accept", "application/json")
	req2.Header.Set("User-Agent", userAgentText)
	body2, status2, err := c.do(req2)
	if err != nil {
		return nil, err
	}
	if isHTMLBody(body2) {
		return nil, upstreamPageError("资源获取", status2)
	}
	dl := map[string]any{}
	if err := json.Unmarshal(body2, &dl); err != nil {
		return nil, fmt.Errorf("观影资源响应解析失败：%s", truncate(body2, 160))
	}
	if code := jsonNum(dl["code"]); code != 0 && code != 200 {
		if msg, _ := dl["msg"].(string); msg != "" {
			return nil, fmt.Errorf("观影资源获取失败：%s", msg)
		}
	}
	// panlist 与 downlist 分别宽容解析：任一结构异常不影响另一段
	var panlist struct {
		ID   []any    `json:"id"`
		Name []string `json:"name"`
		URL  []string `json:"url"`
		Type []any    `json:"type"`
		P    []string `json:"p"`
		Time []string `json:"time"`
		GID  []any    `json:"gid"`
	}
	if raw, err := json.Marshal(dl["panlist"]); err == nil && len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &panlist)
	}
	var downlist struct {
		List struct {
			U []any `json:"u"`
			M []any `json:"m"`
			T []any `json:"t"`
			K []any `json:"k"`
		} `json:"list"`
	}
	if raw, err := json.Marshal(dl["downlist"]); err == nil && len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &downlist)
	}
	items := make([]map[string]any, 0, len(panlist.URL)+len(downlist.List.M))
	// 网盘分享（直链明文）
	for n, link := range panlist.URL {
		if strings.TrimSpace(link) == "" {
			continue // 待审/无链接
		}
		if n < len(panlist.GID) && jsonNum(panlist.GID[n]) == 6 {
			continue // 已失效（站点划线标记）
		}
		name := ""
		if n < len(panlist.Name) {
			name = panlist.Name[n]
		}
		panType := panTypeFromLink(link)
		if panType == "" {
			panType = "guanying"
		}
		remark := ""
		if n < len(panlist.Time) {
			remark = panlist.Time[n]
		}
		if n < len(panlist.P) && strings.TrimSpace(panlist.P[n]) != "" {
			remark = strings.TrimSpace(remark + " 提取码 " + panlist.P[n])
		}
		items = append(items, map[string]any{
			"title":     name,
			"share_url": link,
			"provider":  panType,
			"pan_type":  panType,
			"slug":      fmt.Sprintf("pan:%v", anyAt(panlist.ID, n)),
			"remark":    strings.TrimSpace(remark),
		})
	}
	// 磁力（downlist.list.m=infohash）
	for n := range downlist.List.M {
		ih := anyAt(downlist.List.M, n)
		if strings.TrimSpace(ih) == "" {
			continue
		}
		name := anyAt(downlist.List.T, n)
		items = append(items, map[string]any{
			"title":     name,
			"share_url": "magnet:?xt=urn:btih:" + ih,
			"provider":  "magnet",
			"pan_type":  "magnet",
			"slug":      anyAt(downlist.List.U, n),
			"remark":    "BT 磁力",
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("观影暂无《%s》的有效资源", title)
	}
	return items, nil
}

// jsonNum 宽容数字提取（json 解析后为 float64/string/nil）
func jsonNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var i int
		_, _ = fmt.Sscanf(strings.TrimSpace(n), "%d", &i)
		return i
	}
	return 0
}

func anyAt(list []any, n int) string {
	if n >= 0 && n < len(list) {
		if v, ok := list[n].(string); ok {
			return v
		}
		if list[n] != nil {
			return fmt.Sprintf("%v", list[n])
		}
	}
	return ""
}

func sliceAt(list []string, n int) string {
	if n >= 0 && n < len(list) {
		return list[n]
	}
	return ""
}

// panTypeFromLink 从网盘链接推断类型（provider 标签用）
func panTypeFromLink(link string) string {
	l := strings.ToLower(link)
	switch {
	case strings.Contains(l, "115.com"), strings.Contains(l, "115vod"), strings.Contains(l, "anxia.com"):
		return "115"
	case strings.Contains(l, "123pan"), strings.Contains(l, "123684.com"), strings.Contains(l, "123965.com"), strings.Contains(l, "123"):
		return "123"
	case strings.Contains(l, "quark.cn"):
		return "quark"
	case strings.Contains(l, "alipan.com"), strings.Contains(l, "aliyundrive.com"):
		return "aliyun"
	case strings.Contains(l, "pan.xunlei.com"), strings.Contains(l, "xunlei.com"):
		return "xunlei"
	case strings.Contains(l, "cloud.189.cn"):
		return "tianyi"
	case strings.Contains(l, "caiyun.139.com"), strings.Contains(l, "caiyun.com"):
		return "mobile"
	case strings.Contains(l, "pan.baidu.com"):
		return "baidu"
	case strings.HasPrefix(l, "magnet:"):
		return "magnet"
	}
	return ""
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
	resp, err := c.http.Do(req)
	if err != nil {
		httpMu.Unlock()
		// 域名可能已更换（DNS 解析失败/连接失败）→ 重新解析防失联页并重试一次
		if isConnError(err) {
			if newBase := EnsureFreshBaseURL(req.Context(), true); newBase != "" && newBase != c.baseURL {
				req.URL.Host = strings.TrimPrefix(strings.TrimPrefix(newBase, "https://"), "http://")
				req.Host = req.URL.Host
				httpMu.Lock()
				resp2, err2 := c.http.Do(req)
				if err2 != nil {
					httpMu.Unlock()
					return nil, 0, err2
				}
				defer resp2.Body.Close()
				body, err3 := io.ReadAll(io.LimitReader(resp2.Body, 4<<20))
				httpMu.Unlock()
				return body, resp2.StatusCode, err3
			}
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	httpMu.Unlock()
	return body, resp.StatusCode, err
}

// isConnError 连接层错误（DNS 解析失败/拒连/超时）
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such host") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "dial tcp") || strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "server misbehaving")
}

// --- 会话与凭据持久化 ---

// SaveSession 导出当前 cookie jar 加密落盘（cookie 值本身敏感 → 整体加密）
func SaveSession(c *Client, username string) error {
	plain := serializeCookiesAt(c.http.Jar, c.baseURL)
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
		if err := db.Db.Table("discovery_settings").Where("key = ?", key).Delete(nil).Error; err != nil {
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

// cookieNames 调试用：列出 jar 内 cookie 名
func cookieNames(jar http.CookieJar, u *url.URL) []string {
	names := make([]string, 0, 4)
	for _, ck := range jar.Cookies(u) {
		names = append(names, ck.Name)
	}
	return names
}
