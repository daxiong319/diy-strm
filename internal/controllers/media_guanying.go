package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/discovery"
	"diy-strm/internal/guanying"

	"github.com/gin-gonic/gin"
)

// 观影（guanying）接入端点（对齐 tgto123 新版 /api/media/guanying/* 形状）。
// 凭据与会话在本机加密保存；状态接口只返回脱敏信息。

// guanyingEnabled 是否启用观影源
func guanyingEnabled() bool {
	return discovery.SettingBool("guanying_enabled", false)
}

// GetGuanyingSessionAPI GET /media-discovery/guanying/session — 会话状态（脱敏）
func GetGuanyingSessionAPI(c *gin.Context) {
	data := guanying.SessionStatus()
	data["enabled"] = guanyingEnabled()
	data["message"] = "启用后可在影视详情中检索观影的 115、123、光鸭与磁力资源"
	c.JSON(http.StatusOK, APIResponse[map[string]any]{Code: Success, Data: data})
}

// LoginGuanyingAPI POST /media-discovery/guanying/login
// body: {username, password, attempt_id?}
// → 200 {captcha_required, attempt_id} | 200 settings | 4xx {code, error}
func LoginGuanyingAPI(c *gin.Context) {
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AttemptID string `json:"attempt_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	username := strings.TrimSpace(req.Username)
	if l := len([]rune(username)); l < 2 || l > 30 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "观影账号长度应为 2-30 位"})
		return
	}
	if !guanyingEnabled() {
		if _, uerr := discovery.UpdateSettings(map[string]any{"guanying_enabled": true}); uerr != nil {
			_ = uerr
		}
	}
	client := guanying.SharedClient()
	captchaRequired, challenge, upstreamError, err := client.StartLogin(c.Request.Context(), username, req.Password, req.AttemptID)
	if err != nil {
		c.JSON(http.StatusBadGateway, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	if captchaRequired {
		c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
			"captcha_required": true,
			"attempt_id":       challenge.AttemptID,
			"captcha":          challenge,
		}})
		return
	}
	if upstreamError != "" {
		// 上游登录失败（含 IP 限次提示），透传给前端展示
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: upstreamError})
		return
	}
	if serr := guanying.SaveCredentials(username, req.Password); serr != nil {
		c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "观影登录成功，但自动恢复凭据保存失败：" + serr.Error(), Data: gin.H{"settings": guanying.SessionStatus()}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "观影登录成功，登录态已安全保存", Data: gin.H{"settings": guanying.SessionStatus()}})
}

// GuanyingCaptchaAPI POST /media-discovery/guanying/captcha {attempt_id}
func GuanyingCaptchaAPI(c *gin.Context) {
	var req struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.AttemptID) == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "登录验证码已过期，请重新登录"})
		return
	}
	ch, err := guanying.SharedClient().GetCaptcha(c.Request.Context(), req.AttemptID)
	if err != nil {
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"attempt_id": ch.AttemptID, "text": ch.Text, "image": ch.Image,
		"type": ch.Type, "width": ch.Width, "height": ch.Height,
	}})
}

// GuanyingCaptchaVerifyAPI POST /media-discovery/guanying/captcha/verify {attempt_id, points}
func GuanyingCaptchaVerifyAPI(c *gin.Context) {
	var req struct {
		AttemptID string               `json:"attempt_id"`
		Points    []map[string]float64 `json:"points"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AttemptID == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	if err := guanying.SharedClient().VerifyCaptcha(c.Request.Context(), req.AttemptID, req.Points); err != nil {
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "验证码校验通过"})
}

// ReloginGuanyingAPI POST /media-discovery/guanying/relogin — 用已保存凭据自动恢复会话
func ReloginGuanyingAPI(c *gin.Context) {
	var req struct {
		AttemptID string `json:"attempt_id"`
	}
	_ = c.ShouldBindJSON(&req)
	username, password, ok := guanying.LoadCredentials()
	if !ok {
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: "未保存观影凭据，请重新登录"})
		return
	}
	client := guanying.SharedClient()
	captchaRequired, challenge, upstreamError, err := client.StartLogin(c.Request.Context(), username, password, req.AttemptID)
	if err != nil {
		c.JSON(http.StatusBadGateway, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	if captchaRequired {
		c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
			"captcha_required": true,
			"attempt_id":       challenge.AttemptID,
			"captcha":          challenge,
		}})
		return
	}
	if upstreamError != "" {
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: upstreamError})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "观影登录已恢复", Data: gin.H{"settings": guanying.SessionStatus()}})
}

// TestGuanyingAPI POST /media-discovery/guanying/test — 检查会话有效性
func TestGuanyingAPI(c *gin.Context) {
	client := guanying.SharedClient()
	raw, ok := guanying.SessionStatus()["session_saved"].(bool)
	if !ok || !raw {
		c.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: "尚未保存观影会话，请先登录"})
		return
	}
	if _, err := client.SearchResources(c.Request.Context(), "__ping__", "movie", 0, ""); err != nil {
		// 搜索接口连通但未命中属正常；网络/会话错误才上报
		msg := err.Error()
		if strings.Contains(msg, "超时") || strings.Contains(msg, "connection") {
			c.JSON(http.StatusBadGateway, APIResponse[any]{Code: BadRequest, Message: msg})
			return
		}
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "观影登录态有效", Data: gin.H{"settings": guanying.SessionStatus()}})
}

// ClearGuanyingSessionAPI DELETE /media-discovery/guanying/session
func ClearGuanyingSessionAPI(c *gin.Context) {
	if err := guanying.ClearSession(); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "清除观影会话失败：" + err.Error()})
		return
	}
	if _, uerr := discovery.UpdateSettings(map[string]any{"guanying_enabled": false}); uerr != nil {
		_ = uerr
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "观影登录信息已清除", Data: guanying.SessionStatus()})
}

// GetGuanyingCatalogAPI GET /media-discovery/guanying/catalog?media_type=movie|tv&page=N
// 观影最近更新目录（影视探索「观影」源）：page=1 解析站点首页板块，page≥2 走翻页接口。
// 条目映射为发现页卡片结构（source=guanying，external_id=观影影片 ID，detail_url=观影站详情页）。
func GetGuanyingCatalogAPI(c *gin.Context) {
	if !guanyingEnabled() {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "观影未启用：请先在发现-基础配置中开启观影"})
		return
	}
	ty := "mv"
	if c.Query("media_type") == "tv" {
		ty = "tv"
	}
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	recents, hasNext, err := guanying.SharedClient().RecentUpdates(ctx, ty, page)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "观影目录加载失败：" + err.Error()})
		return
	}
	items := make([]gin.H, 0, len(recents))
	for _, r := range recents {
		vote := r.Douban
		if vote == 0 {
			vote = r.IMDB
		}
		if vote == 0 {
			vote = r.MAL
		}
		meta := r.Status
		if len(r.Quality) > 0 {
			meta = strings.TrimSpace(meta + " " + strings.Join(r.Quality, " "))
		}
		items = append(items, gin.H{
			"source":      "guanying",
			"media_type":  map[string]string{"mv": "movie", "tv": "tv", "ac": "tv"}[r.Dir],
			"entity_key":  fmt.Sprintf("guanying:%s:%s", r.Dir, r.ID),
			"external_id": r.ID,
			"title":       r.Title,
			"poster":      r.Poster,
			"vote_avg":    vote,
			"year":        r.Year,
			"rank":        0,
			"genres":      r.Quality,
			"overview":    meta,
			"detail_url":  r.DetailURL,
			"air_date":    "",
		})
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "", Data: gin.H{
		"items":         items,
		"has_next_page": hasNext,
		"page":          page,
	}})
}

// GetGuanyingPlayPageAPI GET /guanying/play/:line/:episode
// 观影在线播放内嵌代理：后端带会话抓取观影站播放页 HTML 原样返回，
// 静态资源走 filejin CDN、HLS 直连外站，项目域内直接渲染播放器（免登录）。
func GetGuanyingPlayPageAPI(c *gin.Context) {
	if !guanyingEnabled() {
		c.String(http.StatusForbidden, "观影未启用：请先在发现-基础配置中开启观影")
		return
	}
	line := c.Param("line")
	episode, _ := strconv.Atoi(c.Param("episode"))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	html, err := guanying.SharedClient().PlayPageHTML(ctx, line, episode)
	if err != nil {
		c.String(http.StatusBadGateway, "观影播放页加载失败：%s", err.Error())
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", html)
}
