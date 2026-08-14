package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/pan123"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// Pan123QRCode 生成 123 云盘扫码登录二维码会话。
// @Summary 生成 123 云盘扫码登录二维码
// @Description 创建扫码登录会话，返回二维码内容与轮询令牌；前端将内容渲染为二维码，使用 123 云盘 App 扫码确认后自动获取访问令牌
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan123/qrcode [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan123QRCode(c *gin.Context) {
	result, err := pan123.StartQRLogin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 123 云盘二维码失败：" + err.Error(), Data: nil})
		return
	}
	helpers.AppLogger.Infof("生成 123 云盘扫码登录二维码，uniID=%s", result.UniID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "二维码已生成", Data: gin.H{
		"token":      result.UniID,
		"qr_url":     result.QRURL,
		"expires_in": 300,
	}})
}

// Pan123QRCodeStatus 轮询 123 云盘扫码登录状态。
// @Summary 轮询 123 云盘扫码登录状态
// @Description 轮询扫码结果；成功时返回 90 天有效期的访问令牌
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Param token query string true "扫码会话令牌（二维码生成接口返回）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan123/qrcode/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan123QRCodeStatus(c *gin.Context) {
	uniID := strings.TrimSpace(c.Query("token"))
	if uniID == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少扫码会话 token", Data: nil})
		return
	}
	result, err := pan123.PollQRLogin(c.Request.Context(), uniID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询扫码状态失败：" + err.Error(), Data: nil})
		return
	}
	message := map[string]string{
		"waiting":   "等待扫码",
		"scanned":   "已扫码，请在手机确认",
		"confirmed": "扫码成功",
		"cancelled": "扫码已取消",
		"expired":   "二维码已失效，请重新获取",
		"failed":    "扫码登录失败",
	}[result.Status]
	if message == "" {
		message = result.Status
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询扫码状态成功", Data: gin.H{
		"status":  result.Status,
		"message": message,
		"token":   result.Token,
	}})
}

// Pan123QRCodeConfirm 使用扫码登录获取的令牌完成 123 云盘账号授权。
// @Summary 完成 123 云盘扫码登录授权
// @Description 使用扫码轮询返回的访问令牌校验并保存账号凭据（无需密码，90 天有效）
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Param request body requests.Pan123QRConfirmRequest true "扫码确认请求"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan123/qrcode/confirm [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan123QRCodeConfirm(c *gin.Context) {
	req := &requests.Pan123QRConfirmRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusOK, err)
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	if account.SourceType != models.SourceType123 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是 123 云盘", Data: nil})
		return
	}
	token := strings.TrimSpace(req.Token)
	client := pan123.NewClient(account.ID, "", "")
	defer client.Close()
	client.SetAccessToken(token)
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		helpers.AppLogger.Warnf("123 云盘扫码令牌校验/获取用户信息失败：%v", err)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "令牌无效或已过期：" + err.Error(), Data: nil})
		return
	}
	var userId, username, nickname string
	if userInfo != nil {
		userId = strconv.FormatInt(userInfo.Data.UserId, 10)
		username = userInfo.Data.Username
		nickname = userInfo.Data.Nickname
	}
	if !account.Update123Login(username, "", token) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存 123 云盘登录凭据失败", Data: nil})
		return
	}
	if userId != "" {
		displayName := username
		if nickname != "" {
			displayName = nickname
		}
		account.UpdateUser(userId, displayName)
	}
	helpers.AppLogger.Infof("123 云盘扫码登录成功，账号 ID：%d，用户名：%s", account.ID, username)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "123 云盘扫码登录成功", Data: gin.H{
		"user_id":  userId,
		"username": username,
		"nickname": nickname,
	}})
}
