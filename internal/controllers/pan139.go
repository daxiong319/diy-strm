package controllers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/pan139"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// Pan139Login 中国移动云盘（139）账号登录。
// @Summary 中国移动云盘账号登录
// @Description 使用浏览器抓取的 Authorization（base64 凭据）验证并保存登录状态；凭据内含令牌与过期时间，到期自动刷新
// @Tags 中国移动云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param authorization body string true "浏览器抓取的 Authorization（base64 编码）"
// @Param username body string false "账号显示名（手机号/邮箱），可选，缺省时自动从凭据解析"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan139/login [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan139Login(c *gin.Context) {
	req := &requests.Pan139LoginRequest{}
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
	if account.SourceType != models.SourceTypePan139 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是中国移动云盘", Data: nil})
		return
	}

	authorization := strings.TrimSpace(req.Authorization)
	client := pan139.NewClient(account.ID, authorization)
	defer client.Close()
	client.SetAuthChanged(func(newAuth string) {
		account.UpdatePan139Login(newAuth, "")
	})
	// 验证凭据：路由查询 + 根目录列表（凭据无效会在此报错）
	if _, err := client.GetFiles(c.Request.Context(), "root"); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "中国移动云盘凭据验证失败：" + err.Error(), Data: nil})
		return
	}
	// 账号显示名：优先用户填写，否则从凭据解析（手机号/邮箱）
	accountName := strings.TrimSpace(req.Username)
	if accountName == "" {
		accountName = client.GetAccount()
	}
	if !account.UpdatePan139Login(authorization, accountName) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存中国移动云盘登录凭据失败", Data: nil})
		return
	}
	helpers.AppLogger.Infof("中国移动云盘账号登录成功，账号 ID：%d，账号：%s", account.ID, accountName)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "中国移动云盘登录成功", Data: gin.H{
		"username": accountName,
	}})
}

// GetPan139Status 查询中国移动云盘账号状态。
// @Summary 查询中国移动云盘账号状态
// @Description 校验中国移动云盘 Authorization 凭据有效性并返回账号信息
// @Tags 中国移动云盘
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan139/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetPan139Status(c *gin.Context) {
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
	if account.SourceType != models.SourceTypePan139 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是中国移动云盘", Data: nil})
		return
	}
	client := account.GetPan139Client()
	defer client.Close()
	// 验证凭据有效性（路由查询 + 根目录列表）
	if _, err := client.GetFiles(c.Request.Context(), "root"); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "中国移动云盘凭据验证失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取中国移动云盘状态成功", Data: gin.H{
		"user_id":  "",
		"username": account.Username,
	}})
}

// GetPan139UrlByFileId 获取中国移动云盘文件直链。
// @Summary 获取中国移动云盘文件直链
// @Description 通过文件 ID（fileid）查询中国移动云盘文件下载直链并 302 重定向
// @Tags 中国移动云盘
// @Accept json
// @Produce json
// @Param pickcode query string true "文件 ID"
// @Param userid query string false "用户 ID"
// @Param force query integer false "强制直链播放（1=是）"
// @Success 302 {string} string "重定向到文件直链"
// @Failure 200 {object} object
// @Router /pan139/url/{filename} [get]
func GetPan139UrlByFileId(c *gin.Context) {
	var req requests.RemoteFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	fileId := req.PickCode
	var account *models.Account
	if req.UserID == "" {
		syncFile := models.GetFileByPickCode(fileId)
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
	} else {
		var err error
		account, err = models.GetAccountByUserId(req.UserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "用户 ID 不存在", Data: nil})
			return
		}
	}
	if account.SourceType != models.SourceTypePan139 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是中国移动云盘", Data: nil})
		return
	}

	client := account.GetPan139Client()
	defer client.Close()
	cacheKey := "pan139url:" + fileId
	if !keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		helpers.AppLogger.Warnf("获取中国移动云盘下载链接缓存锁超时：fileId=%s", fileId)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取中国移动云盘下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)

	cachedUrl := string(db.Cache.Get(cacheKey))
	var err error
	if cachedUrl == "" {
		cachedUrl, err = client.GetDownloadURL(c.Request.Context(), fileId)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取中国移动云盘下载链接失败：" + err.Error(), Data: nil})
			return
		}
		helpers.AppLogger.Infof("从接口中查询到中国移动云盘下载链接：fileId=%s", fileId)
		// 缓存 50 分钟
		db.Cache.Set(cacheKey, []byte(cachedUrl), 3000)
	} else {
		helpers.AppLogger.Infof("从缓存中查询到中国移动云盘下载链接：fileId=%s", fileId)
	}
	localProxy := 0
	if models.SettingsGlobal != nil {
		localProxy = models.SettingsGlobal.LocalProxy
	}
	if req.Force == 0 && localProxy == 1 {
		helpers.AppLogger.Infof("通过本地代理访问中国移动云盘下载链接：%s", cachedUrl)
		proxyUrl := fmt.Sprintf("/proxy-115?url=%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
	}
	helpers.AppLogger.Infof("302 重定向到中国移动云盘下载链接：%s", cachedUrl)
	c.Redirect(http.StatusFound, cachedUrl)
}

// Pan139QRCode 生成中国移动云盘扫码登录二维码会话。
// @Summary 生成中国移动云盘扫码登录二维码
// @Description 创建扫码登录会话，返回二维码内容 URL 与轮询令牌；前端将 URL 渲染为二维码，扫码确认后自动获取 Authorization 凭据
// @Tags 中国移动云盘
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan139/qrcode [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan139QRCode(c *gin.Context) {
	result, err := pan139.StartQRLogin()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取中国移动云盘二维码失败：" + err.Error(), Data: nil})
		return
	}
	helpers.AppLogger.Infof("生成中国移动云盘扫码登录二维码，有效期 %d 秒", result.ExpiresIn)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "二维码已生成", Data: gin.H{
		"token":      result.Token,
		"qr_url":     result.QRURL,
		"expires_in": result.ExpiresIn,
	}})
}

// Pan139QRCodeStatus 轮询中国移动云盘扫码登录状态。
// @Summary 轮询中国移动云盘扫码登录状态
// @Description 轮询扫码结果；成功时返回 Authorization 凭据与账号，可直接用于账号登录
// @Tags 中国移动云盘
// @Accept json
// @Produce json
// @Param token query string true "扫码会话令牌（二维码生成接口返回）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan139/qrcode/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan139QRCodeStatus(c *gin.Context) {
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少扫码会话 token", Data: nil})
		return
	}
	result, err := pan139.PollQRLogin(token)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询扫码状态失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询扫码状态成功", Data: gin.H{
		"status":        result.Status,
		"message":       result.Message,
		"authorization": result.Authorization,
		"username":      result.Username,
		"expires_at":    result.ExpiresAt,
	}})
}
