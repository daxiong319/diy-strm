package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// GuangYaPanLogin 光鸭云盘账号登录（令牌方式）。
// @Summary 光鸭云盘账号登录
// @Description 使用光鸭云盘的访问令牌（access_token）验证账号，可选提供刷新令牌（refresh_token）用于自动续期
// @Tags 光鸭云盘
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param access_token body string true "光鸭云盘访问令牌"
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
