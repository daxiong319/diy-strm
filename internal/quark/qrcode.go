package quark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 夸克扫码登录（对齐夸克 web 端登录协议）
// 1. getTokenForQrcodeLogin → token
// 2. 二维码 URL = https://su.quark.cn/4_eMHBJ?token={token}&client_id=532&ssb=weblogin&...
// 3. getServiceTicketByQrcodeToken?token={token} 轮询 → status==2000000 时 service_ticket
// 4. https://pan.quark.cn/account/info?st={service_ticket} → set-cookie 登录态（__pus/__puus）
// ---------------------------------------------------------------------------

const (
	qrTokenURL     = "https://uop.quark.cn/cas/ajax/getTokenForQrcodeLogin"
	qrPollURL      = "https://uop.quark.cn/cas/ajax/getServiceTicketByQrcodeToken"
	qrLoginPageURL = "https://su.quark.cn/4_eMHBJ"
	qrExchangeURL  = "https://pan.quark.cn/account/info"
)

// QrSession 一次扫码登录会话（承载 cookie jar，贯穿 token→ticket→cookie 全程）
type QrSession struct {
	client *http.Client
	token  string
}

// NewQrSession 创建扫码登录会话
func NewQrSession() *QrSession {
	jar, _ := cookiejar.New(nil)
	return &QrSession{client: &http.Client{Timeout: 30 * time.Second, Jar: jar}}
}

// QrInit 生成二维码 token，返回二维码 URL
func (q *QrSession) QrInit(ctx context.Context) (token, qrURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qrTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", "https://pan.quark.cn/passport/login")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	var out struct {
		Status int `json:"status"`
		Data   struct {
			Members struct {
				Token string `json:"token"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("解析二维码 token 失败: %s", truncate(string(body), 200))
	}
	if out.Data.Members.Token == "" {
		return "", "", fmt.Errorf("获取二维码 token 失败: %s", truncate(string(body), 200))
	}
	q.token = out.Data.Members.Token
	qrURL = fmt.Sprintf("%s?token=%s&client_id=532&ssb=weblogin&uc_param_str=&uc_biz_str=%s",
		qrLoginPageURL, q.token, url.QueryEscape("S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0"))
	return q.token, qrURL, nil
}

// QrPollStatus 轮询扫码状态。
// 返回 (status, cookie, err)。status: "waiting"(未扫码) / "scanned"(已扫码待确认) / "success"(登录成功)。
// success 时 cookie 为登录后的完整 Cookie（__pus/__puus 等）。
func (q *QrSession) QrPollStatus(ctx context.Context) (status, cookie string, err error) {
	if q.token == "" {
		return "", "", fmt.Errorf("扫码会话未初始化")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qrPollURL+"?token="+url.QueryEscape(q.token), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", "https://pan.quark.cn/passport/login")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	var out struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Data    struct {
			Members struct {
				ServiceTicket string `json:"service_ticket"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("解析扫码状态失败: %s", truncate(string(body), 200))
	}
	switch out.Status {
	case 2000000:
		// 已扫码成功，拿 service_ticket 兑换登录 Cookie
		st := out.Data.Members.ServiceTicket
		if st == "" {
			return "", "", fmt.Errorf("扫码成功但未返回 service_ticket: %s", truncate(string(body), 200))
		}
		cookie, err := q.exchangeTicket(ctx, st)
		if err != nil {
			return "", "", err
		}
		return "success", cookie, nil
	case 50004001:
		return "waiting", "", nil // 未扫码
	case 50004002, 50004003:
		return "scanned", "", nil // 已扫码待确认 / 已过期
	default:
		// 其他状态码：返回原始 message 供前端展示
		return "waiting", "", fmt.Errorf("扫码状态异常（status=%d）：%s", out.Status, out.Message)
	}
}

// exchangeTicket service_ticket → 登录 Cookie（/account/info?st=...）
func (q *QrSession) exchangeTicket(ctx context.Context, serviceTicket string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qrExchangeURL+"?st="+url.QueryEscape(serviceTicket), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", "https://pan.quark.cn/passport/login")
	resp, err := q.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// 从 cookie jar 提取登录态 Cookie
	u, _ := url.Parse("https://pan.quark.cn")
	cookies := q.client.Jar.Cookies(u)
	if len(cookies) == 0 {
		return "", fmt.Errorf("兑换登录态失败：未获取到 Cookie")
	}
	parts := make([]string, 0, len(cookies))
	hasPus := false
	for _, ck := range cookies {
		if ck.Name == "__pus" || ck.Name == "__puus" {
			hasPus = true
		}
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	if !hasPus {
		return "", fmt.Errorf("兑换登录态失败：Cookie 中缺少 __pus/__puus")
	}
	return strings.Join(parts, "; "), nil
}
