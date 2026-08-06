package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/pan123"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// Pan123StatusResp 123 云盘账号状态响应
type Pan123StatusResp struct {
	UserId   string `json:"user_id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

// Pan123Login 123 云盘账号登录（邮箱/手机号 + 密码）。
// @Summary 123 云盘账号登录
// @Description 使用 123 云盘账号的用户名（邮箱或手机号）和密码登录，验证通过后保存访问令牌
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param username body string true "123 云盘用户名（邮箱或手机号）"
// @Param password body string true "123 云盘密码"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan123/login [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func Pan123Login(c *gin.Context) {
	req := &requests.Pan123LoginRequest{}
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

	username := strings.TrimSpace(req.Username)
	client := pan123.NewClient(account.ID, username, strings.TrimSpace(req.Password))
	defer client.Close()
	if err := client.Login(c.Request.Context()); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "123 云盘登录失败：" + err.Error(), Data: nil})
		return
	}
	// 获取用户信息
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		// 用户信息获取失败不阻断登录，令牌已有效
		helpers.AppLogger.Warnf("123 云盘登录成功但获取用户信息失败：%v", err)
	}
	var userId, nickname string
	if userInfo != nil {
		userId = strconv.FormatInt(userInfo.Data.UserId, 10)
		nickname = userInfo.Data.Nickname
	}
	if !account.Update123Login(username, req.Password, client.GetAccessToken()) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存 123 云盘登录凭据失败", Data: nil})
		return
	}
	if userId != "" {
		displayName := userInfo.Data.Username
		if nickname != "" {
			displayName = nickname
		}
		account.UpdateUser(userId, displayName)
	}
	helpers.AppLogger.Infof("123 云盘账号登录成功，账号 ID：%d，用户名：%s", account.ID, username)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "123 云盘登录成功", Data: gin.H{
		"user_id":  userId,
		"username": account.Username,
		"nickname": nickname,
	}})
}

// GetPan123Status 查询 123 云盘账号状态。
// @Summary 查询 123 云盘账号状态
// @Description 校验 123 云盘访问令牌有效性并返回用户信息
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /pan123/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetPan123Status(c *gin.Context) {
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
	if account.SourceType != models.SourceType123 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是 123 云盘", Data: nil})
		return
	}
	client := account.Get123Client()
	defer client.Close()
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 123 云盘用户信息失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[Pan123StatusResp]{Code: Success, Message: "获取 123 云盘状态成功", Data: Pan123StatusResp{
		UserId:   strconv.FormatInt(userInfo.Data.UserId, 10),
		Username: userInfo.Data.Username,
		Nickname: userInfo.Data.Nickname,
	}})
}

// GetPan123UrlByPickCode 获取 123 云盘文件直链。
// @Summary 获取 123 云盘文件直链
// @Description 通过 pickcode（文件 ID）查询 123 云盘文件下载直链并 302 重定向
// @Tags 123 云盘
// @Accept json
// @Produce json
// @Param pickcode query string true "文件 ID"
// @Param userid query string false "用户 ID"
// @Param force query integer false "强制直链播放（1=是）"
// @Success 302 {string} string "重定向到文件直链"
// @Failure 200 {object} object
// @Router /pan123/url/{filename} [get]
func GetPan123UrlByPickCode(c *gin.Context) {
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
	if account.SourceType != models.SourceType123 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号类型不是 123 云盘", Data: nil})
		return
	}

	client := account.Get123Client()
	defer client.Close()
	cacheKey := "pan123url:" + pickCode
	if !keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		helpers.AppLogger.Warnf("获取 123 云盘下载链接缓存锁超时：fileId=%s", pickCode)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 123 云盘下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)

	cachedUrl := string(db.Cache.Get(cacheKey))
	if cachedUrl == "" {
		file, err := findPan123FileById(c.Request.Context(), client, fileId, parentId)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 123 云盘文件信息失败：" + err.Error(), Data: nil})
			return
		}
		downloadInfo, err := client.GetDownloadInfo(c.Request.Context(), *file)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 123 云盘下载信息失败：" + err.Error(), Data: nil})
			return
		}
		cachedUrl, err = client.ResolveDownloadURL(c.Request.Context(), downloadInfo.Data.DownloadUrl)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解析 123 云盘下载链接失败：" + err.Error(), Data: nil})
			return
		}
		helpers.AppLogger.Infof("从接口中查询到 123 云盘下载链接：fileId=%s => %s", pickCode, cachedUrl)
		// 缓存 50 分钟
		db.Cache.Set(cacheKey, []byte(cachedUrl), 3000)
	} else {
		helpers.AppLogger.Infof("从缓存中查询到 123 云盘下载链接：fileId=%s", pickCode)
	}
	localProxy := 0
	if models.SettingsGlobal != nil {
		localProxy = models.SettingsGlobal.LocalProxy
	}
	if req.Force == 0 && localProxy == 1 {
		helpers.AppLogger.Infof("通过本地代理访问 123 云盘下载链接：%s", cachedUrl)
		proxyUrl := fmt.Sprintf("/proxy-115?url=%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
	}
	helpers.AppLogger.Infof("302 重定向到 123 云盘下载链接：%s", cachedUrl)
	c.Redirect(http.StatusFound, cachedUrl)
}

// findPan123FileById 在父目录列表中查找指定文件 ID 的完整信息
// parentId 为空时仅尝试根目录；通过 SyncFile 可提供父目录 ID 避免全盘遍历
func findPan123FileById(ctx context.Context, client *pan123.Client, fileId, parentId string) (*pan123.File, error) {
	parentIds := make([]string, 0, 2)
	if parentId != "" {
		parentIds = append(parentIds, parentId)
	}
	parentIds = append(parentIds, "0")
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
