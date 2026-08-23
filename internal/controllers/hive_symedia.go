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
	msg := saveSymediaCallback(c, req.UserID, req.ProxyUserKey, req.RefreshExpiresAt)
	if msg != "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权信息已保存，" + msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "主渠道授权成功", Data: nil})
}

// HiveSymediaDirectCallback GET /hive-symedia/callback?userid=...&proxy_user_key=...&refresh_expires_at=...
// hdhive.com 授权后直接重定向到该地址（按 app 注册的回调地址），将参数保存并返回成功页
func HiveSymediaDirectCallback(c *gin.Context) {
	userID := c.Query("userid")
	proxyUserKey := c.Query("proxy_user_key")
	refreshExpiresAt := c.Query("refresh_expires_at")
	var refreshSec int64
	if refreshExpiresAt != "" {
		refreshSec, _ = strconv.ParseInt(refreshExpiresAt, 10, 64)
	}
	if userID == "" || proxyUserKey == "" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusBadRequest, `<html><body><h3>授权失败</h3><p>缺少 userid 或 proxy_user_key 参数</p></body></html>`)
		return
	}
	msg := saveSymediaCallback(c, userID, proxyUserKey, refreshSec)
	status := "授权成功"
	extra := "您现在可以关闭此窗口，返回影巢设置页刷新授权状态。"
	if msg != "" {
		status = "授权已保存，部分信息等待刷新"
		extra = msg
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>Symedia 授权 - QMediaSync</title>
<style>body{font-family:sans-serif;display:flex;align-items:center;justify-content:center;min-height:60vh;margin:0;background:#f5f7fa}
.card{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.1);padding:40px;text-align:center;max-width:420px}
h3{color:#67c23a;margin:0 0 12px}.hint{color:#909399;font-size:13px;margin-top:8px}
</style></head>
<body><div class="card"><h3>%s</h3><p>影巢主渠道（Symedia）已收到授权密钥。</p><p class="hint">%s</p></div></body></html>`, status, extra)
}

// saveSymediaCallback 保存 symedia 回调参数到账号，返回非空字符串表示需要额外提示
func saveSymediaCallback(c *gin.Context, userID, proxyUserKey string, refreshExpiresAt int64) string {
	acc := models.FindOrCreateHiveSymediaAccount("主渠道")
	acc.SymediaUserID = userID
	acc.ProxyUserKey = proxyUserKey
	now := time.Now()
	acc.Authorized = true
	acc.AuthorizedAt = &now
	acc.UserFetchedAt = &now
	acc.Enabled = true
	if err := models.SaveHiveAccount(acc); err != nil {
		return "保存账号失败：" + err.Error()
	}

	// 拉取用户信息落库
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc); !ok {
		return "授权信息已保存，" + msg
	} else {
		_ = pub
	}
	return ""
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
