package controllers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 影巢 symedia 主渠道：授权启动 / 回调落库 / 状态刷新 / 签到 / 连通性测试
// （hdhive.symedia.top 中转，与 tgtodrive 备用渠道互为备份，symedia 优先调度）
// ---------------------------------------------------------------------------

// HiveSymediaStatusAPI GET /cloud/hive/symedia/status
// 返回 symedia 通道账号（公共信息）+ 未授权时授权 URL + 各通道健康度
func HiveSymediaStatusAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	pub := acc.Public()
	data := gin.H{
		"account":         pub,
		"channel_health":  models.HiveChannelHealth(),
	}
	if !pub.Authorized {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			origin = "http://" + c.Request.Host
		}
		authURL, aerr := hiveAuthURLFor(ctx, acc, origin)
		if aerr == nil {
			data["auth_url"] = authURL
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: data})
}

// HiveSymediaStartAPI POST /cloud/hive/symedia/start 生成授权 URL（会话握手 + OAuth start）
func HiveSymediaStartAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		origin = "http://" + c.Request.Host
	}
	authURL, err := hiveAuthURLFor(ctx, acc, origin)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "发起授权失败：" + err.Error(), Data: nil})
		return
	}
	if authURL == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未取得授权地址", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "生成成功", Data: gin.H{"authorize_url": authURL}})
}

// HiveSymediaCallbackAPI POST /cloud/hive/symedia/callback {userid, proxy_user_key, refresh_expires_at}
// symedia 服务端授权后回传用户密钥到回调地址，前端回调页转发到本接口完成落库
func HiveSymediaCallbackAPI(c *gin.Context) {
	var req struct {
		UserID           string `json:"userid"`
		ProxyUserKey     string `json:"proxy_user_key"`
		RefreshExpiresAt int64  `json:"refresh_expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.UserID == "" || req.ProxyUserKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少 userid/proxy_user_key"})
		return
	}
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	acc.SymediaUserID = req.UserID
	acc.ProxyUserKey = req.ProxyUserKey
	now := time.Now()
	acc.Authorized = true
	acc.AuthorizedAt = &now
	acc.UserFetchedAt = &now
	acc.Enabled = true
	if err := models.SaveHiveAccount(acc); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存账号失败：" + err.Error(), Data: nil})
		return
	}

	// 拉取用户信息落库
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc); !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权信息已保存，" + msg, Data: gin.H{"authorized": false, "account": pub}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "主渠道授权成功", Data: nil})
}

// HiveSymediaRefreshAPI POST /cloud/hive/symedia/refresh 刷新主渠道授权状态
func HiveSymediaRefreshAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		origin = "http://" + c.Request.Host
	}
	pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc)
	authURL, _ := hiveAuthURLFor(ctx, acc, origin)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{
			"authorized": false,
			"account":    pub,
			"auth_url":   authURL,
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权状态已刷新", Data: gin.H{"authorized": true, "account": pub}})
}

// HiveSymediaCheckinAPI POST /cloud/hive/symedia/checkin {mode} 主渠道签到
func HiveSymediaCheckinAPI(c *gin.Context) {
	var req struct {
		Mode string `json:"mode"`
	}
	_ = c.ShouldBindJSON(&req)
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	mode := hdhive.ResolveCheckinMode(req.Mode)
	ok, msg := RunHiveCheckin(ctx, acc, mode)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": false}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{"success": true}})
}

// HiveSymediaTestAPI POST /cloud/hive/symedia/test 连通性测试（握手 + 用户状态）
func HiveSymediaTestAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	client := models.HiveClientForAccount(acc)
	meResp, err := client.Me(ctx)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "主渠道测试失败：" + err.Error(), Data: nil})
		return
	}
	if !meResp.Success {
		msg := meResp.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(meResp.StatusCode)
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "主渠道测试失败：" + msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "主渠道连接正常", Data: nil})
}
