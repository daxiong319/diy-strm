package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/guangyapan"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// GuangYaPanStatusResp 光鸭云盘账号状态响应
type GuangYaPanStatusResp struct {
	UserId   string `json:"user_id"`
	Username string `json:"username"`
}

// GuangYaPanLogin 光鸭云盘账号登录（手机号+短信验证码 或 令牌方式）。
// @Summary 光鸭云盘账号登录
// @Description 方式一：手机号+短信验证码登录（phone_number + verification_code + verification_id）；方式二：使用访问令牌（access_token）验证，可选提供刷新令牌（refresh_token）用于自动续期
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param phone_number body string false "手机号（+86 13800138000 或 13800138000）"
// @Param verification_code body string false "短信验证码"
// @Param verification_id body string false "发送验证码返回的流水 ID"
// @Param captcha_token body string false "人机验证 token"
// @Param access_token body string false "光鸭云盘访问令牌"
// @Param refresh_token body string false "光鸭云盘刷新令牌（自动续期用）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /guangyapan/login [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func GuangYaPanLogin(c *gin.Context) {
	req := &requests.GuangYaPanLoginRequest{}
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
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}

	// 方式一：手机号+短信验证码登录
	if strings.TrimSpace(req.PhoneNumber) != "" {
		client := guangyapan.NewClient(account.ID, "", "")
		defer client.Close()
		tokenResp, loginErr := client.LoginWithSMS(c.Request.Context(), req.PhoneNumber, req.VerificationCode, req.VerificationID, req.CaptchaToken)
		if loginErr != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: loginErr.Error(), Data: nil})
			return
		}
		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		refreshToken := strings.TrimSpace(tokenResp.RefreshToken)
		// 获取用户信息（验证登录成功）
		userInfo, infoErr := client.GetUserInfo(c.Request.Context())
		if infoErr != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘用户信息失败：" + infoErr.Error(), Data: nil})
			return
		}
		username := strings.TrimSpace(req.PhoneNumber)
		if !account.UpdateGuangYaPanLogin(accessToken, refreshToken, userInfo.Sub, username) {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存光鸭云盘登录凭据失败", Data: nil})
			return
		}
		helpers.AppLogger.Infof("光鸭云盘账号短信登录成功，账号 ID：%d，用户 ID：%s", account.ID, userInfo.Sub)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "光鸭云盘登录成功", Data: gin.H{
			"user_id":  userInfo.Sub,
			"username": username,
		}})
		return
	}

	// 方式二：令牌方式
	accessToken := strings.TrimSpace(req.AccessToken)
	client := guangyapan.NewClient(account.ID, accessToken, strings.TrimSpace(req.RefreshToken))
	defer client.Close()
	// 验证令牌并获取用户信息
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "光鸭云盘令牌验证失败：" + err.Error(), Data: nil})
		return
	}
	// 保存令牌与用户信息
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		// 未提供刷新令牌时使用客户端内已有的
		refreshToken = client.GetRefreshToken()
	}
	if !account.UpdateGuangYaPanLogin(accessToken, refreshToken, userInfo.Sub, userInfo.Sub) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存光鸭云盘登录凭据失败", Data: nil})
		return
	}
	helpers.AppLogger.Infof("光鸭云盘账号登录成功，账号 ID：%d，用户 ID：%s", account.ID, userInfo.Sub)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "光鸭云盘登录成功", Data: gin.H{
		"user_id":  userInfo.Sub,
		"username": userInfo.Sub,
	}})
}

// GuangYaPanSendCode 发送光鸭云盘登录短信验证码。
// @Summary 发送光鸭云盘短信验证码
// @Description 向指定手机号发送光鸭云盘登录短信验证码，返回验证码流水 ID；若需要人工人机验证则返回验证地址
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param phone_number body string true "手机号（+86 13800138000 或 13800138000）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /guangyapan/send-code [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func GuangYaPanSendCode(c *gin.Context) {
	req := &requests.GuangYaPanSendCodeRequest{}
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
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}

	client := guangyapan.NewClient(account.ID, "", "")
	defer client.Close()
	result, sendErr := client.SendSMSCode(c.Request.Context(), req.PhoneNumber)
	if sendErr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "发送光鸭云盘短信验证码失败：" + sendErr.Error(), Data: nil})
		return
	}
	if result.NeedCaptcha {
		// 需要用户完成人机验证（返回验证地址，用户完成后再重试发送）
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "需要完成人机验证", Data: gin.H{
			"need_captcha": true,
			"captcha_url":  result.CaptchaURL,
		}})
		return
	}
	helpers.AppLogger.Infof("光鸭云盘短信验证码已发送，账号 ID：%d，手机号：%s", account.ID, guangyapan.NormalizePhone(req.PhoneNumber))
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "验证码已发送，请注意查收短信", Data: gin.H{
		"verification_id": result.VerificationID,
		"captcha_token":   result.CaptchaToken,
	}})
}

// guangYaPanQRSession 光鸭云盘扫码登录会话（OAuth 设备码）
type guangYaPanQRSession struct {
	deviceCode string
	expiresAt  time.Time
}

// guangYaPanQRSessions 扫码登录会话缓存（键为账号 ID，重启后失效可接受）
var guangYaPanQRSessions = struct {
	sync.Mutex
	m map[uint]*guangYaPanQRSession
}{m: make(map[uint]*guangYaPanQRSession)}

// GuangYaPanQRCode 创建光鸭云盘扫码登录会话（二维码）。
// @Summary 创建光鸭云盘扫码登录会话
// @Description 创建 OAuth 设备码扫码会话，返回二维码地址（verification_uri）与用户码（user_code），用户使用光鸭云盘 App 扫码或访问地址输入用户码确认登录
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /guangyapan/qrcode [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func GuangYaPanQRCode(c *gin.Context) {
	req := &requests.GuangYaPanQRCodeRequest{}
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
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}

	client := guangyapan.NewClient(account.ID, "", "")
	defer client.Close()
	info, createErr := client.CreateQRCode(c.Request.Context())
	if createErr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "创建光鸭云盘扫码会话失败：" + createErr.Error(), Data: nil})
		return
	}
	expiresIn := info.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 600
	}
	guangYaPanQRSessions.Lock()
	guangYaPanQRSessions.m[account.ID] = &guangYaPanQRSession{
		deviceCode: info.DeviceCode,
		expiresAt:  time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
	guangYaPanQRSessions.Unlock()

	helpers.AppLogger.Infof("光鸭云盘扫码会话已创建，账号 ID：%d，有效期：%ds", account.ID, expiresIn)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "请使用光鸭云盘 App 扫码确认登录", Data: gin.H{
		"verification_uri": info.VerificationURI,
		"user_code":        info.UserCode,
		"expires_in":       expiresIn,
		"interval":         info.Interval,
	}})
}

// GuangYaPanQRCodeStatus 轮询光鸭云盘扫码登录状态。
// @Summary 轮询光鸭云盘扫码登录状态
// @Description 轮询扫码登录状态：pending 等待确认、success 登录成功（已保存凭据）、denied 已拒绝、expired 已过期
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /guangyapan/qrcode/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GuangYaPanQRCodeStatus(c *gin.Context) {
	req := &requests.GuangYaPanQRCodeStatusRequest{}
	if err := c.ShouldBindQuery(req); err != nil {
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
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}

	// 取出扫码会话
	guangYaPanQRSessions.Lock()
	sess := guangYaPanQRSessions.m[req.AccountID]
	if sess == nil {
		guangYaPanQRSessions.Unlock()
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "扫码会话不存在，请重新获取二维码", Data: gin.H{"state": "expired"}})
		return
	}
	if time.Now().After(sess.expiresAt) {
		delete(guangYaPanQRSessions.m, req.AccountID)
		guangYaPanQRSessions.Unlock()
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "二维码已过期，请重新获取", Data: gin.H{"state": "expired"}})
		return
	}
	deviceCode := sess.deviceCode
	guangYaPanQRSessions.Unlock()

	client := guangyapan.NewClient(account.ID, "", "")
	defer client.Close()
	result, pollErr := client.PollQRCode(c.Request.Context(), deviceCode)
	if pollErr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "轮询光鸭云盘扫码状态失败：" + pollErr.Error(), Data: gin.H{"state": "error"}})
		return
	}

	if result.State == "success" {
		// 登录成功：获取用户信息并保存凭据
		userInfo, infoErr := client.GetUserInfo(c.Request.Context())
		if infoErr != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘用户信息失败：" + infoErr.Error(), Data: gin.H{"state": "error"}})
			return
		}
		if !account.UpdateGuangYaPanLogin(result.AccessToken, result.RefreshToken, userInfo.Sub, userInfo.Sub) {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存光鸭云盘登录凭据失败", Data: gin.H{"state": "error"}})
			return
		}
		guangYaPanQRSessions.Lock()
		delete(guangYaPanQRSessions.m, req.AccountID)
		guangYaPanQRSessions.Unlock()
		helpers.AppLogger.Infof("光鸭云盘账号扫码登录成功，账号 ID：%d，用户 ID：%s", account.ID, userInfo.Sub)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "光鸭云盘扫码登录成功", Data: gin.H{
			"state":    "success",
			"user_id":  userInfo.Sub,
			"username": userInfo.Sub,
		}})
		return
	}

	// 已拒绝/已过期：清理会话
	if result.State == "denied" || result.State == "expired" {
		guangYaPanQRSessions.Lock()
		delete(guangYaPanQRSessions.m, req.AccountID)
		guangYaPanQRSessions.Unlock()
	}
	message := result.ErrorMessage
	if result.State == "pending" {
		message = "等待用户扫码确认"
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: gin.H{
		"state":   result.State,
		"message": result.ErrorMessage,
	}})
}

// GetGuangYaPanStatus 查询光鸭云盘账号状态。
// @Summary 查询光鸭云盘账号状态
// @Description 校验光鸭云盘访问令牌有效性并返回用户信息
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /guangyapan/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetGuangYaPanStatus(c *gin.Context) {
	var req requests.AccountIDRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}
	client := account.GetGuangYaPanClient()
	defer client.Close()
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘用户信息失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[GuangYaPanStatusResp]{Code: Success, Message: "获取光鸭云盘状态成功", Data: GuangYaPanStatusResp{
		UserId:   userInfo.Sub,
		Username: account.Username,
	}})
}

// GetGuangYaPanUrlByPickCode 获取光鸭云盘文件直链。
// @Summary 获取光鸭云盘文件直链
// @Description 通过文件 ID（pickcode）查询光鸭云盘文件下载直链并 302 重定向
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param pickcode query string true "文件 ID"
// @Param userid query string false "用户 ID"
// @Param force query integer false "强制直链播放（1=是）"
// @Success 302 {string} string "重定向到文件直链"
// @Failure 200 {object} object
// @Router /guangyapan/url/{filename} [get]
func GetGuangYaPanUrlByPickCode(c *gin.Context) {
	var req requests.RemoteFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	pickCode := req.PickCode
	userId := req.UserID
	fileId := pickCode
	parentId := ""
	var account *models.Account
	if userId == "" {
		syncFile := models.GetFileByPickCode(pickCode)
		if syncFile == nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "文件 ID 不存在", Data: nil})
			return
		}
		var err error
		account, err = models.GetAccountById(syncFile.AccountId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
			return
		}
		fileId = syncFile.FileId
		parentId = syncFile.ParentId
	} else {
		var err error
		account, err = models.GetAccountByUserId(userId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "用户 ID 不存在", Data: nil})
			return
		}
		fileId = pickCode
	}
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是光鸭云盘", Data: nil})
		return
	}

	client := account.GetGuangYaPanClient()
	defer client.Close()
	cacheKey := "guangyapanurl:" + pickCode
	if !keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		helpers.AppLogger.Warnf("获取光鸭云盘下载链接缓存锁超时：fileId=%s", pickCode)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)

	cachedUrl := string(db.Cache.Get(cacheKey))
	var err error
	if cachedUrl == "" {
		// 先尝试按文件 ID 直接获取下载链接
		cachedUrl, err = client.GetDownloadURL(c.Request.Context(), fileId)
		if err != nil || cachedUrl == "" {
			// 回退：在父目录/根目录列表中定位文件后再获取
			file, ferr := findGuangYaPanFileById(c.Request.Context(), client, fileId, parentId)
			if ferr != nil {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘文件信息失败：" + ferr.Error(), Data: nil})
				return
			}
			cachedUrl, err = client.GetDownloadURL(c.Request.Context(), file.GetID())
			if err != nil {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取光鸭云盘下载链接失败：" + err.Error(), Data: nil})
				return
			}
		}
		helpers.AppLogger.Infof("从接口中查询到光鸭云盘下载链接：fileId=%s", pickCode)
		// 缓存 50 分钟
		db.Cache.Set(cacheKey, []byte(cachedUrl), 3000)
	} else {
		helpers.AppLogger.Infof("从缓存中查询到光鸭云盘下载链接：fileId=%s", pickCode)
	}
	localProxy := 0
	if models.SettingsGlobal != nil {
		localProxy = models.SettingsGlobal.LocalProxy
	}
	if req.Force == 0 && localProxy == 1 {
		helpers.AppLogger.Infof("通过本地代理访问光鸭云盘下载链接：%s", cachedUrl)
		proxyUrl := fmt.Sprintf("/proxy-115?url=%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
	}
	helpers.AppLogger.Infof("302 重定向到光鸭云盘下载链接：%s", cachedUrl)
	c.Redirect(http.StatusFound, cachedUrl)
}

// findGuangYaPanFileById 在父目录列表中查找指定文件 ID 的完整信息
// parentId 为空时仅尝试根目录；通过 SyncFile 可提供父目录 ID 避免全盘遍历
func findGuangYaPanFileById(ctx context.Context, client *guangyapan.Client, fileId, parentId string) (*guangyapan.File, error) {
	parentIds := make([]string, 0, 2)
	if parentId != "" {
		parentIds = append(parentIds, parentId)
	}
	parentIds = append(parentIds, "")
	seen := make(map[string]bool)
	for _, pid := range parentIds {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		files, err := client.GetFiles(ctx, pid)
		if err != nil {
			continue
		}
		for i := range files {
			if files[i].GetID() == fileId {
				return &files[i], nil
			}
		}
	}
	return nil, fmt.Errorf("未找到文件 ID %s", fileId)
}
