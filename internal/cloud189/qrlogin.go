package cloud189

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 天翼云盘扫码登录（协议对齐 litepan drivers/189Cloud/qrlogin.go）
// 1. unifyLoginForPC → lt/reqId/paramId/captchaToken（cloud.189.cn，webURL）
// 2. getUUID.do → uuid/encryuuid（open.e.189.cn，authURL）
// 3. qrcodeLoginState.do 轮询扫码状态（open.e.189.cn）
// 4. getSessionForPC.action（api.cloud.189.cn）redirectURL 换 session
// ---------------------------------------------------------------------------

const qrCodeTimeoutSec = 300

// QrSession 一次扫码登录会话（cookie jar 贯穿全流程）
type QrSession struct {
	client       *http.Client
	Created      int64
	LT           string
	ReqID        string
	ParamID      string
	CaptchaToken string
	QRURL        string // uuid
	EncryUUID    string
}

// NewQrSession 创建扫码会话
func NewQrSession() *QrSession {
	jar, _ := cookiejar.New(nil)
	return &QrSession{
		client:  &http.Client{Timeout: 30 * time.Second, Jar: jar},
		Created: time.Now().Unix(),
	}
}

// QrInit 生成二维码，返回二维码内容（uuid 字符串，前端编码成二维码图片）
func (q *QrSession) QrInit(ctx context.Context) (qrContent string, err error) {
	// 1. 初始化登录参数
	params, err := q.initBaseParams(ctx)
	if err != nil {
		return "", err
	}
	q.LT = params["lt"]
	q.ReqID = params["req_id"]
	q.ParamID = params["param_id"]
	q.CaptchaToken = params["captcha_token"]

	// 2. getUUID 生成二维码 uuid
	var uuidResp struct {
		UUID      string `json:"uuid"`
		EncryUUID string `json:"encryuuid"`
	}
	if err := q.postForm(ctx, AuthURL+"/api/logbox/oauth2/getUUID.do", url.Values{"appId": {AppID}}, nil, &uuidResp); err != nil {
		return "", fmt.Errorf("获取二维码失败：%w", err)
	}
	if uuidResp.UUID == "" || uuidResp.EncryUUID == "" {
		return "", fmt.Errorf("获取二维码失败：响应缺少 uuid/encryuuid")
	}
	q.QRURL = uuidResp.UUID
	q.EncryUUID = uuidResp.EncryUUID
	return uuidResp.UUID, nil
}

// QrPollStatus 轮询扫码状态。
// 返回 (status, session, error)。
// status: "waiting"(未扫码/已扫码待确认) / "success"(登录成功，session 非空) / "expired" / "failed"。
func (q *QrSession) QrPollStatus(ctx context.Context) (status string, session *TokenSession, err error) {
	if time.Now().Unix()-q.Created > qrCodeTimeoutSec {
		return "expired", nil, nil
	}
	now := time.Now()
	form := url.Values{
		"appId":       {AppID},
		"clientType":  {ClientType},
		"returnUrl":   {ReturnURL},
		"paramId":     {q.ParamID},
		"uuid":        {q.QRURL},
		"encryuuid":   {q.EncryUUID},
		"date":        {now.Format("2006-01-0215:04:05.") + fmt.Sprintf("%03d", now.Nanosecond()/1e6)},
		"timeStamp":   {fmt.Sprintf("%d", now.UnixMilli())},
		"cb_SaveName": {"0"},
		"isOauth2":    {"true"},
		"state":       {""},
	}
	headers := map[string]string{
		"Referer": AuthURL,
		"Reqid":   q.ReqID,
		"lt":      q.LT,
		"Accept":  "application/json;charset=UTF-8",
	}
	var resp map[string]any
	if err := q.postForm(ctx, AuthURL+"/api/logbox/oauth2/qrcodeLoginState.do", form, headers, &resp); err != nil {
		// 网络错误/未扫码时上游可能返回非 JSON，按等待处理
		return "waiting", nil, nil
	}
	code, ok := qrStatusCode(resp)
	if !ok {
		return "waiting", nil, nil
	}
	switch code {
	case 0:
		redirectURL := qrRedirectURL(resp)
		if redirectURL == "" {
			return "failed", nil, fmt.Errorf("扫码成功但未返回授权地址")
		}
		sess, err := q.finalizeLogin(ctx, redirectURL)
		if err != nil {
			return "failed", nil, err
		}
		return "success", sess, nil
	case -11001:
		return "expired", nil, nil
	case -106, -11002:
		return "waiting", nil, nil
	default:
		msg := firstNonEmpty(anyString(resp["msg"]), anyString(resp["message"]))
		if msg == "" {
			msg = fmt.Sprintf("扫码登录失败，状态码 %d", code)
		}
		return "failed", nil, fmt.Errorf("%s", msg)
	}
}

// finalizeLogin redirectURL → getSessionForPC 换完整会话
// 对齐 litepan：POST + clientSuffix（clientType=TELEPC/version/channelId/rand）；
// 缺 clientSuffix 或用 GET 时天翼只返回部分字段（无 refreshToken），
// 后续 API 签名（HMAC-SHA1 需 sessionSecret）与刷新均依赖完整四件套。
func (q *QrSession) finalizeLogin(ctx context.Context, redirectURL string) (*TokenSession, error) {
	params := url.Values{
		"appId":       {AppID},
		"clientType":  {loginPCClientType},
		"version":     {loginVersion},
		"channelId":   {loginChannelID},
		"rand":        {fmt.Sprintf("%d_%d", rand.Intn(100000), rand.Int63n(10000000000))},
		"redirectURL": {redirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, APIURL+"/getSessionForPC.action?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Referer", WebURL)
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("换取会话失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out TokenSession
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析会话响应失败：%w", err)
	}
	if out.SessionKey == "" || out.SessionSecret == "" {
		return nil, fmt.Errorf("换取会话失败：%s", truncateStr(string(body), 200))
	}
	if out.RefreshToken == "" {
		return nil, fmt.Errorf("登录完成但未收到 refreshToken：%s", truncateStr(string(body), 200))
	}
	return &out, nil
}

// initBaseParams 从 unifyLoginForPC 页面提取登录参数（lt/reqId/paramId/captchaToken）
func (q *QrSession) initBaseParams(ctx context.Context) (map[string]string, error) {
	params := url.Values{
		"appId":      {AppID},
		"clientType": {ClientType},
		"returnURL":  {ReturnURL},
		"timeStamp":  {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, WebURL+"/api/portal/unifyLoginForPC.action?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return extractQRBaseParams(string(body))
}

// postForm 扫码登录专用 POST（带 cookie jar 保会话）
func (q *QrSession) postForm(ctx context.Context, rawURL string, form url.Values, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// extractQRBaseParams 从登录页 HTML 提取参数（对齐 litepan）
func extractQRBaseParams(html string) (map[string]string, error) {
	patterns := map[string]string{
		"captcha_token": `'captchaToken'\s+value='(.+?)'`,
		"lt":            `lt\s*=\s*"(.+?)"`,
		"param_id":      `paramId\s*=\s*"(.+?)"`,
		"req_id":        `reqId\s*=\s*"(.+?)"`,
	}
	out := map[string]string{}
	for key, pattern := range patterns {
		match := regexp.MustCompile(pattern).FindStringSubmatch(html)
		if len(match) < 2 {
			return nil, fmt.Errorf("解析天翼登录参数失败：缺少 %s", key)
		}
		out[key] = match[1]
	}
	return out, nil
}

func qrStatusCode(resp map[string]any) (int, bool) {
	for _, key := range []string{"status", "result", "code"} {
		if v, exists := resp[key]; exists {
			return anyInt(v), true
		}
	}
	return 0, false
}

func qrRedirectURL(resp map[string]any) string {
	for _, key := range []string{"redirectUrl", "redirectURL", "redirect_url", "RedirectUrl", "RedirectURL", "url", "loginUrl"} {
		if value := anyString(resp[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"data", "result"} {
		if nested, ok := resp[key].(map[string]any); ok {
			if value := qrRedirectURL(nested); value != "" {
				return value
			}
		}
	}
	return ""
}

func anyInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

// QrContentToDataURL 把二维码内容（uuid）转成 dataURL（前端可直显）。
// 用 skip2/go-qrcode 编码；项目前端已有 qrcode 库，也可直接返回内容由前端渲染。
func QrContentToDataURL(content string) string {
	return "cloud189://qr/" + base64.StdEncoding.EncodeToString([]byte(content))
}
