package requests

import (
	"diy-strm/internal/validation"
)

// Pan123QRConfirmRequest 123 云盘扫码登录确认请求。
type Pan123QRConfirmRequest struct {
	AccountID uint   `json:"account_id" form:"account_id"`
	Token     string `json:"token" form:"token"`
}

// Validate 校验 123 云盘扫码登录确认请求。
func (r Pan123QRConfirmRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	return validation.NonBlank("token", r.Token)
}