package requests

import (
	"strings"

	"diy-strm/internal/models"
	"diy-strm/internal/v115auth"
	"diy-strm/internal/validation"
)

// CreateAccountRequest 创建账号请求。
type CreateAccountRequest struct {
	SourceType     models.SourceType       `json:"source_type" form:"source_type"`
	Name           string                  `json:"name" form:"name"`
	AppID          string                  `json:"app_id" form:"app_id"`
	AuthSourceType v115auth.AuthSourceType `json:"auth_source_type" form:"auth_source_type"`
	AuthProvider   v115auth.AuthProvider   `json:"auth_provider" form:"auth_provider"`
	SelectedApp    string                  `json:"app_id_name" form:"app_id_name"`
	CustomAppName  string                  `json:"custom_app_name" form:"custom_app_name"`
}

// Validate 校验创建账号请求。
func (r CreateAccountRequest) Validate() error {
	if err := validation.OneOfString("source_type", string(r.SourceType), []string{
		string(models.SourceType115),
		string(models.SourceTypeBaiduPan),
		string(models.SourceType123),
		string(models.SourceTypeGuangYaPan),
		string(models.SourceTypePan139),
	}); err != nil {
		return err
	}
	if err := validation.NonBlank("name", r.Name); err != nil {
		return err
	}
	if err := validation.Length("name", r.Name, 1, 64); err != nil {
		return err
	}
	if strings.TrimSpace(r.CustomAppName) != "" {
		if err := validation.Length("custom_app_name", r.CustomAppName, 1, 64); err != nil {
			return err
		}
	}
	if r.SourceType == models.SourceType115 {
		if _, err := v115auth.SourceFromCreateRequest(r.AuthSourceType, r.AuthProvider, r.AppID, r.SelectedApp, r.CustomAppName); err != nil {
			return err
		}
	}
	return nil
}

// GuangYaPanLoginRequest 光鸭云盘账号登录请求（手机号+短信验证码 或 令牌方式）。
// 方式一（手机号+验证码）：phone_number + verification_code + verification_id
// 方式二（令牌）：access_token（必需），refresh_token 用于令牌过期自动刷新（可选但强烈建议）
type GuangYaPanLoginRequest struct {
	AccountID        uint   `json:"account_id" form:"account_id"`
	PhoneNumber      string `json:"phone_number" form:"phone_number"`
	VerificationCode string `json:"verification_code" form:"verification_code"`
	VerificationID   string `json:"verification_id" form:"verification_id"`
	CaptchaToken     string `json:"captcha_token" form:"captcha_token"`
	AccessToken      string `json:"access_token" form:"access_token"`
	RefreshToken     string `json:"refresh_token" form:"refresh_token"`
}

// Validate 校验光鸭云盘账号登录请求（手机号+验证码与令牌两种方式二选一）。
func (r GuangYaPanLoginRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	phoneMode := strings.TrimSpace(r.PhoneNumber) != "" || strings.TrimSpace(r.VerificationCode) != "" || strings.TrimSpace(r.VerificationID) != ""
	if phoneMode {
		if err := validation.NonBlank("phone_number", r.PhoneNumber); err != nil {
			return err
	}
		if err := validation.NonBlank("verification_code", r.VerificationCode); err != nil {
			return err
	}
		if err := validation.NonBlank("verification_id", r.VerificationID); err != nil {
			return err
	}
		if err := validation.Length("phone_number", r.PhoneNumber, 1, 32); err != nil {
			return err
	}
		return validation.Length("verification_code", r.VerificationCode, 1, 16)
	}
	if err := validation.NonBlank("access_token", r.AccessToken); err != nil {
		return err
	}
	return validation.Length("access_token", r.AccessToken, 1, 2048)
}

// GuangYaPanSendCodeRequest 光鸭云盘发送短信验证码请求。
type GuangYaPanSendCodeRequest struct {
	AccountID   uint   `json:"account_id" form:"account_id"`
	PhoneNumber string `json:"phone_number" form:"phone_number"`
}

// Validate 校验光鸭云盘发送短信验证码请求。
func (r GuangYaPanSendCodeRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.NonBlank("phone_number", r.PhoneNumber); err != nil {
		return err
	}
	return validation.Length("phone_number", r.PhoneNumber, 1, 32)
}

// GuangYaPanQRCodeRequest 光鸭云盘创建扫码登录会话请求。
type GuangYaPanQRCodeRequest struct {
	AccountID uint `json:"account_id" form:"account_id"`
}

// Validate 校验光鸭云盘创建扫码登录会话请求。
func (r GuangYaPanQRCodeRequest) Validate() error {
	return validation.PositiveID("account_id", r.AccountID)
}

// GuangYaPanQRCodeStatusRequest 光鸭云盘扫码登录状态轮询请求。
type GuangYaPanQRCodeStatusRequest struct {
	AccountID uint `json:"account_id" form:"account_id"`
}

// Validate 校验光鸭云盘扫码登录状态轮询请求。
func (r GuangYaPanQRCodeStatusRequest) Validate() error {
	return validation.PositiveID("account_id", r.AccountID)
}

// Pan123LoginRequest 123 云盘账号登录请求。
type Pan123LoginRequest struct {
	AccountID uint   `json:"account_id" form:"account_id"`
	Username  string `json:"username" form:"username"`
	Password  string `json:"password" form:"password"`
}

// Validate 校验 123 云盘账号登录请求。
func (r Pan123LoginRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.NonBlank("username", r.Username); err != nil {
		return err
	}
	if err := validation.NonBlank("password", r.Password); err != nil {
		return err
	}
	return validation.Length("password", r.Password, 1, 128)
}

// Pan139LoginRequest 中国移动云盘（139）账号登录请求。
// 登录方式：authorization（浏览器抓取的 Base64 凭据，内含令牌与过期时间，到期自动刷新）
type Pan139LoginRequest struct {
	AccountID     uint   `json:"account_id" form:"account_id"`
	Authorization string `json:"authorization" form:"authorization"`
	Username      string `json:"username" form:"username"` // 账号显示名（手机号/邮箱），可选
}

// Validate 校验中国移动云盘账号登录请求。
func (r Pan139LoginRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.NonBlank("authorization", r.Authorization); err != nil {
		return err
	}
	return validation.Length("authorization", r.Authorization, 1, 4096)
}

// UpdateAccountInfoRequest 更新账号资料请求。
type UpdateAccountInfoRequest struct {
	ID            uint   `json:"id" form:"id"`
	Name          string `json:"name" form:"name"`
	CustomAppName string `json:"app_id_name" form:"app_id_name"`
}

// Validate 校验账号资料更新请求。
func (r UpdateAccountInfoRequest) Validate() error {
	if err := validation.PositiveID("id", r.ID); err != nil {
		return err
	}
	if err := validation.NonBlank("name", r.Name); err != nil {
		return err
	}
	if err := validation.Length("name", r.Name, 1, 64); err != nil {
		return err
	}
	if strings.TrimSpace(r.CustomAppName) != "" {
		return validation.Length("app_id_name", r.CustomAppName, 1, 64)
	}
	return nil
}

// DeleteAccountRequest 删除账号请求。
type DeleteAccountRequest struct {
	ID uint `json:"id" form:"id"`
}

// Validate 校验删除账号请求。
func (r DeleteAccountRequest) Validate() error {
	return validation.PositiveID("id", r.ID)
}

// CreateOpenListAccountRequest 创建或更新 OpenList 账号请求。
type CreateOpenListAccountRequest struct {
	ID       uint   `json:"id" form:"id"`
	BaseURL  string `json:"base_url" form:"base_url"`
	AuthType string `json:"auth_type" form:"auth_type"`
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
	Token    string `json:"token" form:"token"`
}

// Validate 校验 OpenList 账号请求。
func (r *CreateOpenListAccountRequest) Validate() error {
	r.BaseURL = strings.TrimSpace(r.BaseURL)
	if r.BaseURL == "" {
		return validation.New("base_url", "不能为空")
	}
	if !strings.HasPrefix(r.BaseURL, "http://") && !strings.HasPrefix(r.BaseURL, "https://") {
		r.BaseURL = "http://" + r.BaseURL
	}
	r.BaseURL = strings.TrimSuffix(r.BaseURL, "/")
	if err := validation.HTTPURL("base_url", r.BaseURL, false); err != nil {
		return err
	}

	r.AuthType = strings.TrimSpace(r.AuthType)
	switch r.AuthType {
	case "":
		if strings.TrimSpace(r.Token) != "" {
			return nil
		}
	case "password":
		if strings.TrimSpace(r.Token) != "" {
			return nil
		}
	case "token":
		return validation.NonBlank("token", r.Token)
	default:
		return validation.New("auth_type", "不是允许的取值")
	}

	if strings.TrimSpace(r.Token) == "" {
		if err := validation.NonBlank("username", r.Username); err != nil {
			return err
		}
		if err := validation.NonBlank("password", r.Password); err != nil {
			return err
		}
	}
	return nil
}

// CreateAPIKeyRequest 创建 API Key 请求。
type CreateAPIKeyRequest struct {
	Name string `json:"name" binding:"required"`
}

// Validate 校验创建 API Key 请求。
func (r CreateAPIKeyRequest) Validate() error {
	return validation.Length("name", r.Name, 1, 64)
}

// UpdateAPIKeyStatusRequest 更新 API Key 状态请求。
type UpdateAPIKeyStatusRequest struct {
	IsActive *bool `json:"is_active" binding:"required"`
}

// Validate 校验 API Key 状态更新请求。
func (r UpdateAPIKeyStatusRequest) Validate() error {
	if r.IsActive == nil {
		return validation.New("is_active", "不能为空")
	}
	return nil
}
