package guangyapan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// 短信验证码登录相关 API 路径（逆向自光鸭云盘 Web 端，参考 alist GuangYaPan 驱动）
const (
	APICaptchaInit  = "/v1/shield/captcha/init"
	APIVerification = "/v1/auth/verification"
	APIVerifyCode   = "/v1/auth/verification/verify"
	APISignIn       = "/v1/auth/signin"
)

// captchaInitResp 人机验证初始化响应
type captchaInitResp struct {
	CaptchaToken string `json:"captcha_token"`
	URL          string `json:"url"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// verificationResp 发送短信验证码响应
type verificationResp struct {
	VerificationID string `json:"verification_id"`
	Error          string `json:"error"`
	ErrorDesc      string `json:"error_description"`
}

// verifyCodeResp 校验短信验证码响应
type verifyCodeResp struct {
	VerificationToken string `json:"verification_token"`
	Error             string `json:"error"`
	ErrorDesc         string `json:"error_description"`
}

// SendSMSCodeResult 发送短信验证码结果
type SendSMSCodeResult struct {
	VerificationID string `json:"verification_id"` // 发送成功后的验证码流水 ID
	CaptchaToken   string `json:"captcha_token"`   // 人机验证 token（后续登录需要携带）
	CaptchaURL     string `json:"captcha_url"`     // 若需人工人机验证时的地址
	NeedCaptcha    bool   `json:"need_captcha"`    // 是否需要用户完成人机验证
}

// NormalizePhone 规范化手机号为光鸭云盘要求的 "+86 13800138000" 格式
// 支持 "+8613800138000"、"13800138000" 等输入
func NormalizePhone(phone string) string {
	p := strings.TrimSpace(phone)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, " ", "")
	if strings.HasPrefix(p, "+") {
		// "+86xxxxxxxxxxx" -> "+86 xxxxxxxxxxx"
		if strings.HasPrefix(p, "+86") && len(p) > 3 {
			return "+86 " + strings.TrimPrefix(p, "+86")
		}
		return p
	}
	// 纯数字：11 位大陆手机号补 +86 前缀
	digits := strings.TrimPrefix(p, "+")
	b := make([]rune, 0, len(digits))
	for _, ch := range digits {
		if ch >= '0' && ch <= '9' {
			b = append(b, ch)
		}
	}
	d := string(b)
	if len(d) == 11 {
		return "+86 " + d
	}
	return p
}

// accountDo 发送账号服务请求（带可选 x-captcha-token 头），并解析 JSON 到 out
func (c *Client) accountDo(ctx context.Context, path string, captchaToken string, body map[string]any, out any) error {
	if err := c.waitForPermission(ctx, path); err != nil {
		return err
	}
	req := c.client.R().
		SetContext(ctx).
		SetHeaders(c.accountHeaders()).
		SetBody(body)
	if captchaToken != "" {
		req.SetHeader("X-Captcha-Token", captchaToken)
	}
	res, err := req.Post(c.accountBase + path)
	if err != nil {
		return fmt.Errorf("光鸭云盘账号服务请求失败：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode() >= 400 {
		return fmt.Errorf("光鸭云盘账号服务 HTTP %d：%s", res.StatusCode(), strings.TrimSpace(res.String()))
	}
	if out != nil {
		if err := json.Unmarshal(res.Bytes(), out); err != nil {
			return fmt.Errorf("光鸭云盘账号服务响应解析失败：%w", err)
		}
	}
	return nil
}

// SendSMSCode 发送短信验证码
// 流程：人机验证初始化（captcha init）-> 发送短信验证码（verification）
// 若 captcha init 返回 url（需人工人机验证），则返回 NeedCaptcha=true 及地址，由用户完成后再携带 captcha_token 重试
func (c *Client) SendSMSCode(ctx context.Context, phone string) (*SendSMSCodeResult, error) {
	phone = NormalizePhone(phone)
	if phone == "" {
		return nil, fmt.Errorf("手机号不能为空")
	}

	// 1. 人机验证初始化（若服务端直接返回 captcha_token 则无需人工验证）
	var initResp captchaInitResp
	initBody := map[string]any{
		"client_id": c.clientID,
		"action":    "POST:" + APIVerification,
		"device_id": c.deviceID,
		"meta": map[string]any{
			"username":           phone,
			"phone_number":       phone,
			"VERIFICATION_PHONE": phone,
		},
	}
	if err := c.accountDo(ctx, APICaptchaInit, "", initBody, &initResp); err != nil {
		return nil, err
	}
	if initResp.Error != "" || strings.TrimSpace(initResp.CaptchaToken) == "" {
		// 可能需要人工人机验证（滑块/图形验证码）
		if initResp.URL != "" {
			return &SendSMSCodeResult{NeedCaptcha: true, CaptchaURL: initResp.URL}, nil
		}
		return nil, fmt.Errorf("光鸭云盘人机验证初始化失败：%s", describeAccountErr(initResp.Error, initResp.ErrorDesc))
	}
	captchaToken := strings.TrimSpace(initResp.CaptchaToken)

	// 2. 发送短信验证码
	var sendResp verificationResp
	if err := c.accountDo(ctx, APIVerification, captchaToken, map[string]any{
		"phone_number": phone,
		"target":       "ANY",
		"client_id":    c.clientID,
	}, &sendResp); err != nil {
		return nil, err
	}
	if sendResp.Error != "" || strings.TrimSpace(sendResp.VerificationID) == "" {
		// captcha 失效时自动重试一次
		if strings.Contains(sendResp.Error, "captcha_invalid") || strings.Contains(sendResp.ErrorDesc, "captcha_token expired") {
			var retryInit captchaInitResp
			if err := c.accountDo(ctx, APICaptchaInit, "", initBody, &retryInit); err != nil {
				return nil, err
			}
			if retryInit.Error != "" || strings.TrimSpace(retryInit.CaptchaToken) == "" {
				return nil, fmt.Errorf("光鸭云盘人机验证初始化失败：%s", describeAccountErr(retryInit.Error, retryInit.ErrorDesc))
			}
			captchaToken = strings.TrimSpace(retryInit.CaptchaToken)
			if err := c.accountDo(ctx, APIVerification, captchaToken, map[string]any{
				"phone_number": phone,
				"target":       "ANY",
				"client_id":    c.clientID,
			}, &sendResp); err != nil {
				return nil, err
			}
		}
		if sendResp.Error != "" || strings.TrimSpace(sendResp.VerificationID) == "" {
			return nil, fmt.Errorf("光鸭云盘发送短信验证码失败：%s", describeAccountErr(sendResp.Error, sendResp.ErrorDesc))
		}
	}
	return &SendSMSCodeResult{
		VerificationID: strings.TrimSpace(sendResp.VerificationID),
		CaptchaToken:   captchaToken,
		NeedCaptcha:    false,
	}, nil
}

// LoginWithSMS 使用手机号+短信验证码完成登录，返回访问令牌与刷新令牌
// verificationID 为 SendSMSCode 返回的验证码流水 ID；captchaToken 为 SendSMSCode 返回的人机验证 token
func (c *Client) LoginWithSMS(ctx context.Context, phone, code, verificationID, captchaToken string) (*TokenResp, error) {
	phone = NormalizePhone(phone)
	code = strings.TrimSpace(code)
	verificationID = strings.TrimSpace(verificationID)
	if phone == "" || code == "" || verificationID == "" {
		return nil, fmt.Errorf("手机号、验证码、验证流水 ID 均不能为空")
	}

	// 1. 校验短信验证码
	var verifyResp verifyCodeResp
	if err := c.accountDo(ctx, APIVerifyCode, "", map[string]any{
		"verification_id":   verificationID,
		"verification_code": code,
		"client_id":         c.clientID,
	}, &verifyResp); err != nil {
		return nil, err
	}
	if verifyResp.Error != "" || strings.TrimSpace(verifyResp.VerificationToken) == "" {
		return nil, fmt.Errorf("光鸭云盘验证码校验失败：%s", describeAccountErr(verifyResp.Error, verifyResp.ErrorDesc))
	}

	// 2. 提交登录
	var out TokenResp
	if err := c.accountDo(ctx, APISignIn, captchaToken, map[string]any{
		"verification_code":  code,
		"verification_token": verifyResp.VerificationToken,
		"username":           phone,
		"client_id":          c.clientID,
	}, &out); err != nil {
		return nil, err
	}
	if out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return nil, fmt.Errorf("光鸭云盘登录失败：%s", describeAccountErr(out.Error, out.ErrorDesc))
	}
	// 登录成功后保存令牌（供后续自动刷新使用）
	c.SetTokens(strings.TrimSpace(out.AccessToken), strings.TrimSpace(out.RefreshToken))
	return &out, nil
}

// describeAccountErr 生成账号服务错误描述
func describeAccountErr(errCode, errDesc string) string {
	if errDesc != "" {
		return errDesc
	}
	if errCode != "" {
		return errCode
	}
	return "未知错误"
}
