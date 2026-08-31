package controllers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 影巢四通道管理：通道列表 / nanshare 授权 / 官方直连授权
// ---------------------------------------------------------------------------

// HiveChannelsAPI GET /cloud/hive/channels
// 返回四个通道的账号授权状态与健康度（负载均衡调度视图）
func HiveChannelsAPI(c *gin.Context) {
	channels := make([]gin.H, 0, len(models.HiveAllChannels))
	for _, ch := range models.HiveAllChannels {
		var acc *models.HiveOAuthAccount
		switch ch {
		case models.HiveChannelSymedia:
			acc = models.FindOrCreateHiveSymediaAccount("Symedia 主渠道")
		case models.HiveChannelNanShare:
			acc = models.FindOrCreateHiveNanShareAccount("NanShare 渠道")
		case models.HiveChannelOfficial:
			acc = models.FindOrCreateHiveOfficialAccount("官方直连渠道")
		default:
			// tgtodrive：主账号即该通道账号（历史默认通道）
			if a, err := models.GetHiveMainAccount(); err == nil {
				acc = a
			}
		}
		entry := gin.H{
			"channel": ch,
			"label":   models.HiveChannelLabels[ch],
		}
		if acc != nil {
			entry["account"] = acc.Public()
			entry["account_id"] = acc.ID
		}
		channels = append(channels, entry)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: gin.H{
		"channels":       channels,
		"channel_health": models.HiveChannelHealth(),
	}})
}

// ------------------------- NanShare 通道 -------------------------

// HiveNanShareStatusAPI GET /cloud/hive/nanshare/status
// 返回 nanshare 通道账号（公共信息）+ 未授权时授权 URL（oauth/start）
func HiveNanShareStatusAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveNanShareAccount("NanShare 渠道")
	pub := acc.Public()
	data := gin.H{
		"account": pub,
		"base_url": "https://hdhive.nanl.top",
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if authURL, err := nanShareAuthURL(ctx, acc, c); err == nil && authURL != "" {
		data["auth_url"] = authURL
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: data})
}

// HiveNanShareStartAPI POST /cloud/hive/nanshare/start 生成授权 URL
func HiveNanShareStartAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveNanShareAccount("NanShare 渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	authURL, err := nanShareAuthURL(ctx, acc, c)
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

// nanShareAuthURL 调中转 oauth/start 生成授权 URL（授权完成后浏览器回跳 return_url）
func nanShareAuthURL(ctx context.Context, acc *models.HiveOAuthAccount, c *gin.Context) (string, error) {
	client, ok := models.HiveClientForAccount(acc).(*hdhive.NanShareClient)
	if !ok {
		return "", nil
	}
	if acc.NanShareAccountID == "" {
		acc.NanShareAccountID = hdhive.NewNanShareSDKAccountID()
		_ = models.SaveHiveAccount(acc)
		client = hdhive.NewNanShareClient(acc.NanShareAccountID)
	}
	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		origin = "http://" + c.Request.Host
	}
	returnURL := strings.TrimRight(origin, "/") + "/cloud/hive" // 回跳影巢设置页
	return client.OAuthStart(ctx, acc.Label, returnURL)
}

// HiveNanShareRefreshAPI POST /cloud/hive/nanshare/refresh 轮询授权状态并落库
// （NanShare 中转托管凭据：授权完成后我方轮询 oauth/status 确认）
func HiveNanShareRefreshAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveNanShareAccount("NanShare 渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{
			"authorized": false,
			"account":    pub,
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权状态已刷新", Data: gin.H{"authorized": true, "account": pub}})
}

// HiveNanShareTestAPI POST /cloud/hive/nanshare/test 连通性测试（项目签名 + 账号状态）
func HiveNanShareTestAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveNanShareAccount("NanShare 渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	client := models.HiveClientForAccount(acc)
	meResp, err := client.Me(ctx)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "NanShare 通道测试失败：" + err.Error(), Data: nil})
		return
	}
	if !meResp.Success {
		msg := meResp.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(meResp.StatusCode)
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "NanShare 通道测试失败：" + msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "NanShare 通道连接正常", Data: nil})
}

// ------------------------- 官方直连通道 -------------------------

// officialRedirectURI 官方通道 OAuth 回调地址
func officialRedirectURI(c *gin.Context) string {
	origin := c.Request.Header.Get("Origin")
	if origin == "" {
		origin = "http://" + c.Request.Host
	}
	return strings.TrimRight(origin, "/") + "/hive-official/callback"
}

// HiveOfficialStatusAPI GET /cloud/hive/official/status
// 返回官方通道账号（公共信息）+ 授权 URL（未授权时）
func HiveOfficialStatusAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveOfficialAccount("官方直连渠道")
	pub := acc.Public()
	data := gin.H{
		"account":    pub,
		"client_id":  hdhive.OfficialClientID(),
		"auth_scope": "query unlock write",
	}
	if !pub.Authorized {
		data["auth_url"] = hdhive.BuildOfficialAuthURL(officialRedirectURI(c), "official")
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: data})
}

// HiveOfficialStartAPI POST /cloud/hive/official/start 生成授权 URL
func HiveOfficialStartAPI(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "生成成功", Data: gin.H{
		"authorize_url": hdhive.BuildOfficialAuthURL(officialRedirectURI(c), "official"),
	}})
}

// HiveOfficialCallback GET /hive-official/callback?code=...&state=...
// hdhive.com 授权页回跳：授权码换取 Token 并落库，返回成功页
func HiveOfficialCallback(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusBadRequest, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>官方通道授权 - QMediaSync</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h3>授权失败</h3><p>缺少 code 参数</p></body></html>`)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	tokenResp, err := hdhive.ExchangeOfficialToken(ctx, code, officialRedirectURI(c))
	if err != nil {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>官方通道授权 - QMediaSync</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h3>授权失败</h3><p>`+htmlEscape(err.Error())+`</p></body></html>`)
		return
	}
	acc := models.FindOrCreateHiveOfficialAccount("官方直连渠道")
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	acc.AccessToken = tokenResp.AccessToken
	acc.RefreshToken = tokenResp.RefreshToken
	acc.TokenExpiresAt = &expiresAt
	now := time.Now()
	acc.Authorized = true
	acc.AuthorizedAt = &now
	acc.UserFetchedAt = &now
	acc.Enabled = true
	if err := models.SaveHiveAccount(acc); err != nil {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>官方通道授权 - QMediaSync</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h3>授权信息保存失败</h3><p>`+htmlEscape(err.Error())+`</p></body></html>`)
		return
	}
	// 拉取用户信息落库
	if _, ok, _ := HiveOAuthStatusByAccount(ctx, acc); ok {
		_ = ok
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>官方通道授权 - QMediaSync</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h3 style="color:#67c23a">授权成功</h3><p>官方直连通道（hdhive.com OpenAPI）已授权。</p><p style="color:#909399;font-size:13px">您现在可以关闭此窗口，返回影巢设置页刷新授权状态。</p></body></html>`)
}

// HiveOfficialRefreshAPI POST /cloud/hive/official/refresh 刷新授权状态（Token 过期自动刷新）
func HiveOfficialRefreshAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveOfficialAccount("官方直连渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	pub, ok, msg := HiveOAuthStatusByAccount(ctx, acc)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: gin.H{
			"authorized": false,
			"account":    pub,
			"auth_url":   hdhive.BuildOfficialAuthURL(officialRedirectURI(c), "official"),
		}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权状态已刷新", Data: gin.H{"authorized": true, "account": pub}})
}

// HiveOfficialTestAPI POST /cloud/hive/official/test 连通性测试（应用 Secret + 用户 Token）
func HiveOfficialTestAPI(c *gin.Context) {
	acc := models.FindOrCreateHiveOfficialAccount("官方直连渠道")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	client := models.HiveClientForAccount(acc)
	meResp, err := client.Me(ctx)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "官方通道测试失败：" + err.Error(), Data: nil})
		return
	}
	if !meResp.Success {
		msg := meResp.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(meResp.StatusCode)
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "官方通道测试失败：" + msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "官方通道连接正常", Data: nil})
}
