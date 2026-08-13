package pan123

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 123 云盘扫码登录（接口实测验证，参照 mediary-scout Pan123QrLoginClient）：
//   GET https://login.123pan.com/api/user/qr-code/generate → {code:0, data:{uniID, url}}
//   二维码内容 = url?env=production&uniID=<uniID>&source=123pan&type=login
//   GET https://login.123pan.com/api/user/qr-code/result?uniID=<uniID> → {code, data:{loginStatus, token?}}
// loginStatus: 0 等待扫码 / 1 已扫码待确认 / 2 已取消 / 3 已确认 / 4 二维码已失效
// token 非空即已确认（90 天 Bearer 令牌）

const (
	QRGenerateURL = "https://login.123pan.com/api/user/qr-code/generate"
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

func qrGet(ctx context.Context, rawURL string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		trunc := body
		if len(trunc) > 160 {
			trunc = trunc[:160]
		}
		return nil, fmt.Errorf("扫码接口返回非 JSON（可能触发风控页面）：%s", trunc)
	}
	return env, nil
}

// StartQRLogin 生成扫码登录会话。
func StartQRLogin(ctx context.Context) (*QRStartResult, error) {
	env, err := qrGet(ctx, QRGenerateURL)
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

// PollQRLogin 轮询扫码登录状态。
func PollQRLogin(ctx context.Context, uniID string) (*QRPollResult, error) {
	if strings.TrimSpace(uniID) == "" {
		return nil, fmt.Errorf("缺少扫码会话令牌")
	}
	env, err := qrGet(ctx, QRResultURL+"?uniID="+url.QueryEscape(uniID))
	if err != nil {
		return nil, err
	}
	data, _ := env["data"].(map[string]any)
	if token, _ := data["token"].(string); token != "" {
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
