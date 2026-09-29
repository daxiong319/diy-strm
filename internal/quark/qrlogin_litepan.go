package quark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 夸克扫码登录（严格复刻 LitePan drivers/Quark/qrlogin.go）
// 关键点（此前缺失导致「登录请求已过期」）：
//   1. getToken/poll 用 GET + query(v=1.2, request_id)，Referer=pan.quark.cn
//   2. 手动跟随重定向（≤6 跳），逐跳收集/回带 Cookie（跨域弱化问题）
//   3. 兑换 Cookie：account/info?st=xxx&lw=scan，随后 bootstrap
//      （访问首页 + 列目录一次）补全 drive-pc/.quark.cn 域 Cookie
//   4. Cookie 合并按域打分（drive-pc +80，pan.quark +40，前导点 +5）
// ---------------------------------------------------------------------------

const (
	qrClientID = "532"
	qrBaseURL  = "https://su.quark.cn/4_eMHBJ"

	casHost      = "https://uop.quark.cn"
	casGetToken  = "/cas/ajax/getTokenForQrcodeLogin"
	casGetTicket = "/cas/ajax/getServiceTicketByQrcodeToken"

	panHost        = "https://pan.quark.cn"
	panAccountInfo = "/account/info"
	panHomeReferer = "https://pan.quark.cn/"

	lpQRTimeoutSec = 300
	lpStatusOK     = 2000000
)

var casStatusFail = map[int]bool{50004002: true, 50004003: true, 50004004: true}

const qrLoginUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36"

// lpClient 无痕会话客户端（手动重定向）
type lpClient struct {
	http *http.Client
}

func newLPClient() *lpClient {
	return &lpClient{http: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func isRedirectLP(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// qrFetch 手动跟随重定向并跨域收集/回带 Cookie（对齐 litepan qrFetch）
func (c *Client) qrFetch(ctx context.Context, client *lpClient, rawURL string, extraHeaders map[string]string, col *cookieCollector) (*http.Response, []byte, error) {
	cur := rawURL
	for hop := 0; hop < 6; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", qrLoginUA)
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		if ck := col.string(); ck != "" {
			req.Header.Set("Cookie", ck)
		}

		resp, err := client.http.Do(req)
		if err != nil {
			return nil, nil, err
		}
		col.absorb(req.URL.Hostname(), resp.Header)

		if isRedirectLP(resp.StatusCode) {
			loc := resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if loc == "" {
				return resp, nil, nil
			}
			next, err := req.URL.Parse(loc)
			if err != nil {
				return nil, nil, err
			}
			cur = next.String()
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		return resp, body, nil
	}
	return nil, nil, fmt.Errorf("夸克扫码请求重定向过多")
}

func qrRequestID() string {
	b := make([]byte, 8)
	_, _ = randRead(b)
	return fmt.Sprintf("%x", b)
}

func randRead(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(time.Now().UnixNano() >> (uint(i%8) * 8))
	}
	return len(b), nil
}

// ---------------------------------------------------------------------------
// cookieCollector（对齐 litepan：跨域收集 + 域打分合并）
// ---------------------------------------------------------------------------

type cookieCand struct {
	value string
	score int
}

type cookieCollector struct {
	mu    sync.Mutex
	best  map[string]cookieCand
	order []string
}

func newCookieCollector() *cookieCollector {
	return &cookieCollector{best: map[string]cookieCand{}}
}

func (c *cookieCollector) put(name, value string, score int) {
	if name == "" || value == "" || qrCookieSkip(name) {
		return
	}
	cur, ok := c.best[name]
	if !ok {
		c.order = append(c.order, name)
	}
	if !ok || score >= cur.score {
		c.best[name] = cookieCand{value: value, score: score}
	}
}

func (c *cookieCollector) absorb(reqHost string, header http.Header) {
	cookies := (&http.Response{Header: header}).Cookies()
	for _, ck := range cookies {
		dom := strings.ToLower(ck.Domain)
		if dom == "" {
			dom = strings.ToLower(reqHost)
		}
		if !strings.Contains(dom, "quark") {
			continue
		}
		c.put(ck.Name, ck.Value, domScore(dom))
	}
}

func (c *cookieCollector) absorbPlain(s string, score int) {
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.Index(part, "=")
		if i < 0 {
			continue
		}
		c.put(strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:]), score)
	}
}

func (c *cookieCollector) string() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := append([]string(nil), c.order...)
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+c.best[k].value)
	}
	return strings.Join(parts, "; ")
}

func domScore(dom string) int {
	score := len(dom)
	switch {
	case strings.Contains(dom, "drive-pc"):
		score += 80
	case strings.Contains(dom, "pan.quark"):
		score += 40
	}
	if strings.HasPrefix(dom, ".") {
		score += 5
	}
	return score
}

func qrCookieSkip(name string) bool {
	switch name {
	case "_gid", "isg", "l":
		return true
	}
	return strings.HasPrefix(name, "_ga")
}

// ---------------------------------------------------------------------------
// 扫码登录入口（对齐 LitePan StartQRLogin / PollQRLogin / finalizeQRLogin）
// ---------------------------------------------------------------------------

type casResp struct {
	Status  int `json:"status"`
	Message string
	Data    struct {
		Members struct {
			Token         string `json:"token"`
			ServiceTicket string `json:"service_ticket"`
		} `json:"members"`
	} `json:"data"`
}

// qrLoginSession 扫码会话（token + CAS cookie）
type qrLoginSession struct {
	Token     string
	Cookie    string
	Created   time.Time
	RetryDone bool
}

var qrLoginSessions = struct {
	sync.Mutex
	m map[string]*qrLoginSession
}{m: map[string]*qrLoginSession{}}

// QrLoginStart 生成夸克扫码二维码（返回 sessionID + 二维码 URL）
func (c *Client) QrLoginStart(ctx context.Context) (sessionID, qrURL string, err error) {
	client := newLPClient()
	col := newCookieCollector()

	q := url.Values{"client_id": {qrClientID}, "v": {"1.2"}, "request_id": {qrRequestID()}}
	headers := map[string]string{"Accept": "application/json, text/plain, */*", "Referer": panHomeReferer}
	_, body, err := c.qrFetch(ctx, client, casHost+casGetToken+"?"+q.Encode(), headers, col)
	if err != nil {
		return "", "", err
	}
	var resp casResp
	if e := json.Unmarshal(body, &resp); e != nil {
		return "", "", fmt.Errorf("夸克二维码接口返回异常")
	}
	if resp.Status != lpStatusOK || resp.Data.Members.Token == "" {
		msg := resp.Message
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.Status)
		}
		return "", "", fmt.Errorf("获取夸克二维码失败：%s", msg)
	}

	token := resp.Data.Members.Token
	qrURL = fmt.Sprintf(
		"%s?token=%s&client_id=%s&ssb=weblogin&uc_param_str=&uc_biz_str=S%%3Acustom%%7COPT%%3ASAREA%%400%%7COPT%%3AIMMERSIVE%%401%%7COPT%%3ABACK_BTN_STYLE%%400",
		qrBaseURL, token, qrClientID)

	sessionID = token
	qrLoginSessions.Lock()
	qrLoginSessions.m[sessionID] = &qrLoginSession{
		Token:   token,
		Cookie:  col.string(),
		Created: time.Now(),
	}
	// 清理过期会话
	now := time.Now()
	for k, v := range qrLoginSessions.m {
		if now.Sub(v.Created) > 15*time.Minute {
			delete(qrLoginSessions.m, k)
		}
	}
	qrLoginSessions.Unlock()
	return sessionID, qrURL, nil
}

// QrLoginPoll 轮询扫码状态（对齐 LitePan PollQRLogin + finalizeQRLogin）
// 返回 (status, cookie)，status: waiting / success / expired / failed
func (c *Client) QrLoginPoll(ctx context.Context, sessionID string) (string, string, error) {
	qrLoginSessions.Lock()
	sess, ok := qrLoginSessions.m[sessionID]
	qrLoginSessions.Unlock()
	if !ok || sess.Token == "" {
		return "failed", "", fmt.Errorf("扫码会话无效，请重新获取二维码")
	}
	if time.Since(sess.Created) > lpQRTimeoutSec*time.Second {
		return "expired", "", nil
	}

	client := newLPClient()
	col := newCookieCollector()
	col.absorbPlain(sess.Cookie, 0)

	q := url.Values{"client_id": {qrClientID}, "v": {"1.2"}, "token": {sess.Token}, "request_id": {qrRequestID()}}
	headers := map[string]string{"Accept": "application/json, text/plain, */*", "Referer": panHomeReferer}
	resp, body, err := c.qrFetch(ctx, client, casHost+casGetTicket+"?"+q.Encode(), headers, col)
	if err != nil || resp.StatusCode != http.StatusOK {
		// 网络波动按等待处理（litepan 同款）
		return "waiting", "", nil
	}
	var cr casResp
	if e := json.Unmarshal(body, &cr); e != nil {
		return "waiting", "", nil
	}

	ticket := cr.Data.Members.ServiceTicket
	switch {
	case cr.Status == lpStatusOK && ticket != "":
		cookie, err := c.finalizeQRLogin(ctx, client, col, ticket)
		if err != nil {
			return "failed", "", err
		}
		qrLoginSessions.Lock()
		delete(qrLoginSessions.m, sessionID)
		qrLoginSessions.Unlock()
		return "success", cookie, nil
	case casStatusFail[cr.Status]:
		// 50004002 偶发于扫码确认瞬间（uop 状态翻转窗口），宽限重试一次
		if !sess.RetryDone {
			sess.RetryDone = true
			sess.Created = time.Now().Add(-lpQRTimeoutSec * time.Second / 2) // 续 90 秒宽限
			return "waiting", "", nil
		}
		msg := cr.Message
		if msg == "" {
			msg = "扫码登录失败"
		}
		return "failed", "", fmt.Errorf("%s", msg)
	default:
		return "waiting", "", nil
	}
}

// finalizeQRLogin service_ticket → 登录 Cookie + bootstrap（对齐 LitePan）
func (c *Client) finalizeQRLogin(ctx context.Context, client *lpClient, col *cookieCollector, ticket string) (string, error) {
	q := url.Values{"st": {ticket}, "lw": {"scan"}}
	headers := map[string]string{"Accept": "application/json, text/plain, */*", "Referer": panHomeReferer}
	resp, _, err := c.qrFetch(ctx, client, panHost+panAccountInfo+"?"+q.Encode(), headers, col)
	if err != nil {
		return "", fmt.Errorf("获取登录 Cookie 失败")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("获取登录 Cookie 失败，HTTP %d", resp.StatusCode)
	}

	// bootstrap：访问首页 + 列目录一次，补全 drive-pc/.quark.cn 域 Cookie
	homeHeaders := map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Upgrade-Insecure-Requests": "1",
	}
	_, _, _ = c.qrFetch(ctx, client, panHost+"/", homeHeaders, col)
	c.bootstrapList(ctx, client, col)

	cookie := col.string()
	if strings.TrimSpace(cookie) == "" {
		return "", fmt.Errorf("登录完成但未获取到 Cookie，请重试")
	}
	return cookie, nil
}

// bootstrapList 列目录一次补全 drive-pc cookie（litepan bootstrapList）
func (c *Client) bootstrapList(ctx context.Context, client *lpClient, col *cookieCollector) {
	q := url.Values{
		"pr": {"ucpro"}, "fr": {"pc"},
		"pdir_fid": {"0"}, "_page": {"1"}, "_size": {"1"}, "_fetch_total": {"1"},
	}
	headers := map[string]string{
		"User-Agent": qrLoginUA,
		"Referer":    panHost + "/",
		"Origin":     panHost,
		"Accept":     "application/json, text/plain, */*",
	}
	_, _, _ = c.qrFetch(ctx, client, "https://drive-pc.quark.cn/1/clouddrive/file/sort?"+q.Encode(), headers, col)
}

// ---------------------------------------------------------------------------
// 全局会话管理器（controllers 使用）
// ---------------------------------------------------------------------------

type qrLoginManager struct{}

var globalQrLogin = &qrLoginManager{}

// GlobalQrLogin 返回全局扫码登录管理器
func GlobalQrLogin() *qrLoginManager { return globalQrLogin }

// Start 生成二维码
func (m *qrLoginManager) Start(ctx context.Context) (string, string, error) {
	c := &Client{}
	return c.QrLoginStart(ctx)
}

// Poll 轮询扫码状态
func (m *qrLoginManager) Poll(ctx context.Context, sessionID string) (string, string, error) {
	c := &Client{}
	return c.QrLoginPoll(ctx, sessionID)
}
