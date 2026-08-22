package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 影巢官方 OpenAPI 通道：配置 / 授权启动 / 回调换 token / 连通性测试
// （hdhive.com 官方接入，与 tgtodrive 中转通道互为备份）
// ---------------------------------------------------------------------------

// 官方授权 state 暂存（内存，10 分钟有效）
var (
	hiveOfficialStatesMu sync.Mutex
	hiveOfficialStates   = map[string]time.Time{}
)

func hiveOfficialStateValid(state string) bool {
	hiveOfficialStatesMu.Lock()
	defer hiveOfficialStatesMu.Unlock()
	exp, ok := hiveOfficialStates[state]
	if !ok || time.Now().After(exp) {
		return false
	}
	delete(hiveOfficialStates, state) // 一次性
	return true
}

func hiveOfficialStatePut(state string) {
	hiveOfficialStatesMu.Lock()
	defer hiveOfficialStatesMu.Unlock()
	hiveOfficialStates[state] = time.Now().Add(10 * time.Minute)
	// 顺手清理过期项
	for k, exp := range hiveOfficialStates {
		if time.Now().After(exp) {
			delete(hiveOfficialStates, k)
		}
	}
}

// HiveOfficialConfigAPI GET /cloud/hive/official/config 官方通道配置（secret 脱敏）
func HiveOfficialConfigAPI(c *gin.Context) {
	cfg := models.GetHiveOfficialConfig()
	secret := ""
	if cfg.AppSecret != "" {
		if len(cfg.AppSecret) > 8 {
			secret = cfg.AppSecret[:8] + "****"
		} else {
			secret = "****"
		}
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Data: map[string]any{
		"client_id":    cfg.ClientID,
		"app_secret":   secret,
		"base_url":     cfg.BaseURL,
		"redirect_uri": cfg.RedirectURI,
		"configured":   cfg.ClientID != "" && cfg.AppSecret != "",
		"channel_health": models.HiveChannelHealth(),
	}})
}

// HiveOfficialConfigSaveAPI POST /cloud/hive/official/config {client_id, app_secret, base_url, redirect_uri}
func HiveOfficialConfigSaveAPI(c *gin.Context) {
	var req struct {
		ClientID    string `json:"client_id"`
		AppSecret   string `json:"app_secret"`
		BaseURL     string `json:"base_url"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	if strings.TrimSpace(req.ClientID) == "" || strings.TrimSpace(req.AppSecret) == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "client_id 与应用 Secret 不能为空"})
		return
	}
	if err := models.SetHiveOfficialConfig(
		strings.TrimSpace(req.ClientID),
		strings.TrimSpace(req.AppSecret),
		strings.TrimSpace(req.BaseURL),
		strings.TrimSpace(req.RedirectURI),
	); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "官方通道配置已保存"})
}

// HiveOfficialTestAPI POST /cloud/hive/official/test 连通性测试（ping）
func HiveOfficialTestAPI(c *gin.Context) {
	cfg := models.GetHiveOfficialConfig()
	if cfg.ClientID == "" || cfg.AppSecret == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先保存官方通道配置"})
		return
	}
	client := hdhive.NewOfficialClient(cfg)
	ctx, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "官方通道测试失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "官方通道连接正常"})
}

// HiveOfficialStartAPI POST /cloud/hive/official/start 生成授权 URL
func HiveOfficialStartAPI(c *gin.Context) {
	cfg := models.GetHiveOfficialConfig()
	if cfg.ClientID == "" || cfg.AppSecret == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先保存官方通道配置（client_id + 应用 Secret）"})
		return
	}
	// redirect_uri 为空时用请求 Origin 推导（授权码换 token 时必须与发起时完全一致，state 暂存保证一致）
	if cfg.RedirectURI == "" {
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			origin = "http://" + c.Request.Host
		}
		cfg.RedirectURI = strings.TrimRight(origin, "/") + "/hive-official/callback"
	}
	state := hdhive.NewOAuthState()
	hiveOfficialStatePut(state)
	client := hdhive.NewOfficialClient(cfg)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Data: map[string]any{
		"authorize_url": client.BuildOfficialAuthorizeURL(state),
		"state":         state,
		"redirect_uri":  cfg.RedirectURI,
	}})
}

// HiveOfficialCallbackAPI POST /cloud/hive/official/callback {code, state, redirect_uri}
// 前端回调页收到 postMessage / redirect 参数后转发到本接口完成换 token 与账号落库
func HiveOfficialCallbackAPI(c *gin.Context) {
	var req struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	if req.Code == "" || req.State == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少 code/state"})
		return
	}
	if !hiveOfficialStateValid(req.State) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "state 无效或已过期，请重新发起授权"})
		return
	}
	cfg := models.GetHiveOfficialConfig()
	if req.RedirectURI != "" {
		cfg.RedirectURI = req.RedirectURI
	}
	if cfg.ClientID == "" || cfg.AppSecret == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "官方通道配置缺失"})
		return
	}
	client := hdhive.NewOfficialClient(cfg)
	ctx, cancel := context.WithTimeout(c, 60*time.Second)
	defer cancel()

	resp, err := client.ExchangeCode(ctx, req.Code)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "换取 Token 失败：" + err.Error()})
		return
	}
	if !resp.Success {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "换取 Token 失败：" + resp.Message + "（" + resp.Code + "）"})
		return
	}
	tok, err := hdhive.ParseOfficialToken(resp.Data)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解析 Token 失败：" + err.Error()})
		return
	}

	// 用 access token 拉用户信息
	client.AccessToken = tok.AccessToken
	client.RefreshToken = tok.RefreshToken
	meResp, err := client.Me(ctx)
	var meInfo map[string]any
	if err == nil && meResp.Success {
		_ = json.Unmarshal(meResp.Data, &meInfo)
	}

	// 落库：官方通道账号（同通道复用一条，按 username 标识）
	label := "官方通道账号"
	if meInfo != nil {
		if un, ok := meInfo["username"].(string); ok && un != "" {
			label = "官方通道 · " + un
		}
	}
	userJSON, _ := json.Marshal(meInfo)
	now := time.Now()
	expAt := now.Add(time.Duration(tok.ExpiresIn) * time.Second)

	acc := models.FindOrCreateHiveOfficialAccount(label)
	acc.Channel = models.HiveChannelOfficial
	acc.AccessToken = tok.AccessToken
	acc.RefreshToken = tok.RefreshToken
	acc.TokenExpiresAt = &expAt
	acc.Authorized = true
	acc.AuthorizedAt = &now
	acc.UserInfo = string(userJSON)
	acc.UserFetchedAt = &now
	acc.Enabled = true
	if err := models.SaveHiveAccount(acc); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存账号失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "官方通道授权成功", Data: map[string]any{"user": meInfo}})
}
