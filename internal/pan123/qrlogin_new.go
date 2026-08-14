package pan123

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 123 云盘扫码登录（接口实测验证，参照 mediary-scout Pan123QrLoginClient 与官方 SDK p123client）：
//   GET  https://login.123pan.com/api/user/qr-code/generate → {code:0, data:{uniID, url}}
//   二维码内容 = url?env=production&uniID=<uniID>&source=123pan&type=login
//   POST https://login.123pan.com/api/user/qr-code/login {uniID} → 确认扫码登录（官方 SDK 关键步骤）
//   GET  https://login.123pan.com/api/user/qr-code/result?uniID=<uniID> → {code, data:{loginStatus, token?}}
// loginStatus: 0 等待扫码 / 1 已扫码待确认 / 2 已取消 / 3 已确认 / 4 二维码已失效
// 注意：result 的 token 在未调 qr-code/login 确认前回显的是 uniID 本身；
// 先 POST qr-code/login 确认后，result 才返回真正的 90 天 Bearer 令牌（JWT）。

const (
	QRGenerateURL = "https://login.123pan.com/api/user/qr-code/generate"
	QRCodeLoginURL = "https://login.123pan.com/api/user/qr-code/login"
	QRResultURL   = "https://login.123pan.com/api/user/qr-code/result"
)

// QRStartResult 扫码会话生成结果。
type QRStartResult struct {
	UniID string // 轮询令牌
	QRURL string // 二维码内容（前端渲染）
}

// QRPollResult 扫码轮询结果。
type QRPollResult struct {
	Status string // waiting | scanned | confirmed | cancelled | expired | failed
	Token  string // 确认后的 Bearer 令牌（仅 confirmed 非空）
}

var qrHTTPClient = &http.Client{Timeout: 20 * time.Second}

// truncToken 日志脱敏：仅显示前 16 字符。
func truncToken(tok string) string {
	if len(tok) <= 16 {
		return tok
	}
	return tok[:16] + "..."
}

func qrRequest(ctx context.Context, method, rawURL string, body map[string]any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json;charset=UTF-8")
	req.Header.Set("platform", "web")
	req.Header.Set("app-version", "3")
	req.Header.Set("origin", "https://login.123pan.com")
	req.Header.Set("referer", "https://login.123pan.com/")
	req.Header.Set("user-agent", "Mozilla/5.0")
	resp, err := qrHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		trunc := raw
		if len(trunc) > 160 {
			trunc = trunc[:160]
		}
		return nil, fmt.Errorf("扫码接口返回非 JSON（可能触发风控页面）：%s", trunc)
	}
	return env, nil
}

// StartQRLogin 生成扫码登录会话。
func StartQRLogin(ctx context.Context) (*QRStartResult, error) {
	env, err := qrRequest(ctx, http.MethodGet, QRGenerateURL, nil)
	if err != nil {
		return nil, err
	}
	if code, _ := env["code"].(float64); code != 0 {
		return nil, fmt.Errorf("生成二维码失败：code=%v msg=%v", env["code"], env["message"])
	}
	data, _ := env["data"].(map[string]any)
	uniID, _ := data["uniID"].(string)
	rawURL, _ := data["url"].(string)
	if uniID == "" || rawURL == "" {
		return nil, fmt.Errorf("生成二维码失败：接口未返回 uniID/url")
	}
	qrContent := fmt.Sprintf("%s?env=production&uniID=%s&source=123pan&type=login", rawURL, url.QueryEscape(uniID))
	return &QRStartResult{UniID: uniID, QRURL: qrContent}, nil
}

// isJWTToken 简单判断令牌是否为 JWT（三段式）。扫码确认前的占位 token 是 uniID（UUID），不含 "."。
func isJWTToken(tok string) bool {
	return strings.Count(tok, ".") == 2
}

// confirmQRLogin 确认扫码登录（POST user/qr-code/login {uniID}）。
// 返回确认后可能携带的令牌（部分实现会在确认响应中直接返回）。
func confirmQRLogin(ctx context.Context, uniID string) (string, error) {
	env, err := qrRequest(ctx, http.MethodPost, QRCodeLoginURL, map[string]any{"uniID": uniID})
	if err != nil {
		return "", err
	}
	if code, _ := env["code"].(float64); code != 0 && code != 200 {
		return "", fmt.Errorf("确认扫码登录失败：code=%v msg=%v", env["code"], env["message"])
	}
	if data, _ := env["data"].(map[string]any); data != nil {
		if token, _ := data["token"].(string); token != "" && isJWTToken(token) {
			return token, nil
		}
	}
	return "", nil
}

// PollQRLogin 轮询扫码登录状态。
// 已确认（loginStatus=3）且 token 为占位的 uniID 时，自动先 POST qr-code/login 确认
// 再取 result 中的真实 Bearer 令牌。
func PollQRLogin(ctx context.Context, uniID string) (*QRPollResult, error) {
	if strings.TrimSpace(uniID) == "" {
		return nil, fmt.Errorf("缺少扫码会话令牌")
	}
	env, err := qrRequest(ctx, http.MethodGet, QRResultURL+"?uniID="+url.QueryEscape(uniID), nil)
	if err != nil {
		return nil, err
	}
	data, _ := env["data"].(map[string]any)
	if token, _ := data["token"].(string); token != "" {
		log.Printf("pan123 qr poll: loginStatus=%v tokenLen=%d tokenHead=%q isJWT=%v", data["loginStatus"], len(token), truncToken(token), isJWTToken(token))
		if !isJWTToken(token) {
			// 占位 token（uniID 回显）：先确认登录，再取真实令牌
			tok2, cerr := confirmQRLogin(ctx, uniID)
			log.Printf("pan123 qr confirm: err=%v tokenHead=%q", cerr, truncToken(tok2))
			if cerr == nil {
				if env2, err2 := qrRequest(ctx, http.MethodGet, QRResultURL+"?uniID="+url.QueryEscape(uniID), nil); err2 == nil {
					if d2, _ := env2["data"].(map[string]any); d2 != nil {
						if t2, _ := d2["token"].(string); t2 != "" && isJWTToken(t2) {
							log.Printf("pan123 qr re-poll: got real JWT token")
							return &QRPollResult{Status: "confirmed", Token: t2}, nil
						}
					}
				}
			}
		}
		return &QRPollResult{Status: "confirmed", Token: token}, nil
	}
	code, _ := env["code"].(float64)
	rawStatus, hasStatus := data["loginStatus"]
	if !hasStatus {
		// 无 loginStatus：code!=0 说明 uniID 已失效
		if code != 0 {
			return &QRPollResult{Status: "expired"}, nil
		}
		return &QRPollResult{Status: "waiting"}, nil
	}
	switch int(rawStatus.(float64)) {
	case 0:
		return &QRPollResult{Status: "waiting"}, nil
	case 1:
		return &QRPollResult{Status: "scanned"}, nil
	case 2:
		return &QRPollResult{Status: "cancelled"}, nil
	case 3:
		return &QRPollResult{Status: "confirmed"}, nil
	case 4:
		return &QRPollResult{Status: "expired"}, nil
	default:
		return &QRPollResult{Status: "waiting"}, nil
	}
}
