package quark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"

	"diy-strm/internal/helpers"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 夸克短信验证码登录（后端全代理模式）
// 为什么不用 iframe：uop 登录页会话 cookie 未设 SameSite，跨站 iframe 中不发送，
// sendSmsCode 与 commit 的服务端会话不连续导致「短信验证码无效或过期」。
// 这里由后端持有会话 cookie jar，前端滑块（阿里云 nc）通过回调产生 sig/session_id
// 随 send 请求传入，send/commit 走同一后端会话。
// ---------------------------------------------------------------------------

const (
	smsLoginPageURL = "https://uop.quark.cn/cas/custom/login"
	smsSendPath     = "/cas/custom/login/sendSmsCode?custom_login_type=mobile"
	smsCommitPath   = "/cas/custom/login/commit?custom_login_type=mobile"
	smsFormOrigin   = "https://uop.quark.cn"
	smsReferer      = smsLoginPageURL + "?custom_login_type=mobile&client_id=532&display=pc"
)

// SmsSession 一次短信登录会话（cookie jar 保证 send/commit 同服务端会话）
type SmsSession struct {
	client   *http.Client
	Phone    string
	Created  time.Time
	SentCode bool
}

// SmsSessionStore 全局会话存储（sessionID → *SmsSession）
var SmsSessionStore = struct {
	sync.RWMutex
	m map[string]*SmsSession
}{m: make(map[string]*SmsSession)}

// NewSmsSession 创建会话并登记
func NewSmsSession(sessionID string) *SmsSession {
	jar, _ := cookiejar.New(nil)
	s := &SmsSession{
		client:  &http.Client{Timeout: 30 * time.Second, Jar: jar},
		Created: time.Now(),
	}
	SmsSessionStore.Lock()
	SmsSessionStore.m[sessionID] = s
	// 顺手清理 15 分钟前的旧会话
	now := time.Now()
	for k, v := range SmsSessionStore.m {
		if now.Sub(v.Created) > 15*time.Minute {
			delete(SmsSessionStore.m, k)
		}
	}
	SmsSessionStore.Unlock()
	return s
}

// GetSmsSession 取会话
func GetSmsSession(sessionID string) *SmsSession {
	SmsSessionStore.RLock()
	defer SmsSessionStore.RUnlock()
	return SmsSessionStore.m[sessionID]
}

// smsFormPayload 官方页 u_form 的固定字段
func smsFormPayload(phone, sig, sessionID, token, key, nvcData, captchaData string) url.Values {
	return url.Values{
		"display":           {"pc"},
		"client_id":         {"532"},
		"redirect_uri":      {"http://id.uc.cn/"},
		"change_uid":        {"0"},
		"transmission_mode": {"pm"},
		"nc_scene":          {"nvc_login"},
		"nc_appkey":         {"CF_APP_uc_usercenter"},
		"nvc_appkey":        {"FFFF0N0000000000ABDE"},
		"nvc_data":          {nvcData},
		"captcha_data":      {captchaData},
		"scene_v2":          {"1ml1ondj"},
		"sig":               {sig},
		"session_id":        {sessionID},
		"token":             {token},
		"key":               {key},
		"login_name":        {phone},
		"origin":            {"*"},
	}
}

// SendSmsCode 转发官方 sendSmsCode（后端会话）
// 返回 (ok, needSlider, serverMessage)
func (s *SmsSession) SendSmsCode(ctx context.Context, phone, sig, sessionID, token, key, nvcData, captchaData string) (bool, bool, string) {
	form := smsFormPayload(phone, sig, sessionID, token, key, nvcData, captchaData)
	form.Set("captcha_id", "")
	form.Set("captcha_val", "")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, smsFormOrigin+smsSendPath, strings.NewReader(form.Encode()))
	if err != nil {
		return false, false, err.Error()
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", smsReferer)
	req.Header.Set("Origin", smsFormOrigin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return false, false, err.Error()
	}
	defer resp.Body.Close()
	body := readAll(resp)
	out := parseStatus(body)
	helpers.AppLogger.Infof("[夸克短信] sendSmsCode: phone=%s status=%d resp=%s", maskPhone(phone), out.Status, truncateStr(string(body), 300))
	// 2000000 成功；20180001 需滑块（data 带 token/key 重置滑块）
	if out.Status == 2000000 {
		s.Phone = phone
		s.SentCode = true
		return true, false, ""
	}
	if out.Status == 20180001 {
		return false, true, out.Message
	}
	return false, false, fmt.Sprintf("发送失败（%d）：%s", out.Status, out.Message)
}

// CommitSmsLogin 提交验证码登录 → service_ticket
func (s *SmsSession) CommitSmsLogin(ctx context.Context, phone, smsCode, sig, sessionID, token, key, nvcData, captchaData string) (string, error) {
	form := smsFormPayload(phone, sig, sessionID, token, key, nvcData, captchaData)
	form.Set("captcha_id", "")
	form.Set("captcha_val", "")
	form.Set("sms_code", smsCode)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, smsFormOrigin+smsCommitPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", smsReferer)
	req.Header.Set("Origin", smsFormOrigin)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body := readAll(resp)
	out := parseStatus(body)
	helpers.AppLogger.Infof("[夸克短信] commit: phone=%s status=%d resp=%s", maskPhone(phone), out.Status, truncateStr(string(body), 300))
	if out.Status != 20000 {
		return "", fmt.Errorf("登录失败（%d）：%s", out.Status, out.Message)
	}
	if out.Data == "" {
		return "", fmt.Errorf("登录成功但未返回 service_ticket")
	}
	return out.Data, nil
}

// smsResp uop 表单接口的通用响应
type smsResp struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func readAll(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 2048)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 1<<20 {
			break
		}
	}
	return buf
}

// parseStatus 解析 {status, message, data}；data 为字符串（service_ticket）时提取
func parseStatus(body []byte) (out struct {
	Status  int
	Message string
	Data    string
}) {
	raw := strings.TrimSpace(string(body))
	// 简易解析（避免引依赖差异）：先 json.Unmarshal
	var m map[string]any
	if err := jsonUnmarshalBytes(body, &m); err != nil {
		return
	}
	if v, ok := m["status"].(float64); ok {
		out.Status = int(v)
	}
	if v, ok := m["message"].(string); ok {
		out.Message = v
	}
	switch d := m["data"].(type) {
	case string:
		out.Data = d
	case map[string]any:
		if v, ok := d["service_ticket"].(string); ok {
			out.Data = v
		}
	}
	_ = raw
	return
}

func jsonUnmarshalBytes(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// DeleteSmsSession 删除会话
func DeleteSmsSession(sessionID string) {
	SmsSessionStore.Lock()
	delete(SmsSessionStore.m, sessionID)
	SmsSessionStore.Unlock()
}

func maskPhone(phone string) string {
	if len(phone) >= 7 {
		return phone[:3] + "****" + phone[len(phone)-4:]
	}
	return phone
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
