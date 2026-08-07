package guangyapan

import (
	"context"
	"fmt"
	"strings"
)

// 扫码登录（OAuth 设备码流程）相关 API 路径
// 参考官方客户端 guangyapan.login_with_app 实现
const (
	APIDeviceCode       = "/v1/auth/device/code"
	APIToken            = "/v1/auth/token"
	grantTypeDeviceCode = "urn:ietf:params:oauth:grant-type:device_code"
)

// QRCodeInfo 创建扫码登录会话（设备码）响应
type QRCodeInfo struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Error           string `json:"error"`
	ErrorDesc       string `json:"error_description"`
}

// PollQRCodeResult 扫码登录轮询结果
type PollQRCodeResult struct {
	State        string // success / pending / denied / expired / error
	AccessToken  string
	RefreshToken string
	ErrorMessage string
}

// CreateQRCode 创建扫码登录会话（OAuth 设备码流程第一步）。
// 用户使用光鸭云盘 App 扫描 verification_uri 对应二维码，或访问该地址输入 user_code 确认登录。
// 返回的设备码需在服务端保存，用于后续轮询令牌。
func (c *Client) CreateQRCode(ctx context.Context) (*QRCodeInfo, error) {
	var out QRCodeInfo
	if err := c.accountDo(ctx, APIDeviceCode, "", map[string]any{
		"client_id": c.clientID,
		"scope":     "user",
	}, &out); err != nil {
		return nil, err
	}
	if out.Error != "" || strings.TrimSpace(out.DeviceCode) == "" {
		return nil, fmt.Errorf("光鸭云盘创建扫码会话失败：%s", describeAccountErr(out.Error, out.ErrorDesc))
	}
	return &out, nil
}

// PollQRCode 轮询扫码登录状态（OAuth 设备码流程第三步）。
// 用户扫码并确认后返回 success 与访问令牌、刷新令牌；未确认时返回 pending；拒绝/过期返回对应状态。
func (c *Client) PollQRCode(ctx context.Context, deviceCode string) (*PollQRCodeResult, error) {
	deviceCode = strings.TrimSpace(deviceCode)
	if deviceCode == "" {
		return nil, fmt.Errorf("device_code 为空")
	}
	var out TokenResp
	if err := c.accountDo(ctx, APIToken, "", map[string]any{
		"client_id":   c.clientID,
		"grant_type":  grantTypeDeviceCode,
		"device_code": deviceCode,
	}, &out); err != nil {
		return nil, err
	}
	if out.Error == "" && strings.TrimSpace(out.AccessToken) != "" {
		// 登录成功，保存令牌供后续自动刷新使用
		c.SetTokens(strings.TrimSpace(out.AccessToken), strings.TrimSpace(out.RefreshToken))
		return &PollQRCodeResult{
			State:        "success",
			AccessToken:  strings.TrimSpace(out.AccessToken),
			RefreshToken: strings.TrimSpace(out.RefreshToken),
		}, nil
	}
	desc := describeAccountErr(out.Error, out.ErrorDesc)
	switch strings.TrimSpace(out.Error) {
	case "authorization_pending", "slow_down":
		// 用户尚未完成扫码确认
		return &PollQRCodeResult{State: "pending"}, nil
	case "access_denied":
		return &PollQRCodeResult{State: "denied", ErrorMessage: desc}, nil
	case "expired_token":
		return &PollQRCodeResult{State: "expired", ErrorMessage: desc}, nil
	default:
		return &PollQRCodeResult{State: "error", ErrorMessage: desc}, nil
	}
}
