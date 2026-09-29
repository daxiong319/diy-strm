package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"litepan/internal/discover/discovery"
	"litepan/internal/discover/guanying"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// 观影（guanying）接入端点（移植自 diy-strm media_guanying 控制器）。
// 凭据与会话本机加密保存；状态接口只返回脱敏信息。
// ---------------------------------------------------------------------------

func guanyingEnabled() bool {
	return discovery.SettingBool("guanying_enabled", false)
}

// guanyingSession 会话状态（脱敏）
func (h *Handler) guanyingSession(w http.ResponseWriter, r *http.Request) {
	data := guanying.SessionStatus()
	data["enabled"] = guanyingEnabled()
	data["message"] = "启用后可在影视详情中检索观影的 115、123、光鸭与磁力资源"
	writeOK(w, data)
}

// guanyingLogin 登录（body: username/password/attempt_id?）→ 可能要求验证码
func (h *Handler) guanyingLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AttemptID string `json:"attempt_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	username := strings.TrimSpace(req.Username)
	if l := len([]rune(username)); l < 2 || l > 30 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "观影账号长度应为 2-30 位"))
		return
	}
	if !guanyingEnabled() {
		_, _ = discovery.UpdateSettings(map[string]any{"guanying_enabled": true})
	}
	client := guanying.SharedClient()
	captchaRequired, challenge, upstreamError, err := client.StartLogin(r.Context(), username, req.Password, req.AttemptID)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%v", err))
		return
	}
	if captchaRequired {
		writeOK(w, map[string]any{
			"captcha_required": true,
			"attempt_id":       challenge.AttemptID,
			"captcha":          challenge,
		})
		return
	}
	if upstreamError != "" {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%s", upstreamError))
		return
	}
	if serr := guanying.SaveCredentials(username, req.Password); serr != nil {
		writeOK(w, map[string]any{"settings": guanying.SessionStatus(), "warning": "观影登录成功，但自动恢复凭据保存失败：" + serr.Error()})
		return
	}
	writeOK(w, map[string]any{"settings": guanying.SessionStatus(), "message": "观影登录成功，登录态已安全保存"})
}

// guanyingCaptcha 获取验证码
func (h *Handler) guanyingCaptcha(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.AttemptID) == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "登录验证码已过期，请重新登录"))
		return
	}
	ch, err := guanying.SharedClient().GetCaptcha(r.Context(), req.AttemptID)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%v", err))
		return
	}
	writeOK(w, map[string]any{
		"attempt_id": ch.AttemptID, "text": ch.Text, "image": ch.Image,
		"type": ch.Type, "width": ch.Width, "height": ch.Height,
	})
}

// guanyingCaptchaVerify 提交验证码点位校验
func (h *Handler) guanyingCaptchaVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttemptID string               `json:"attempt_id"`
		Points    []map[string]float64 `json:"points"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AttemptID == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	if err := guanying.SharedClient().VerifyCaptcha(r.Context(), req.AttemptID, req.Points); err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%v", err))
		return
	}
	writeOK(w, map[string]any{"message": "验证码校验通过"})
}

// guanyingRelogin 用已保存凭据自动恢复会话
func (h *Handler) guanyingRelogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttemptID string `json:"attempt_id"`
	}
	_ = decodeJSON(r, &req)
	username, password, ok := guanying.LoadCredentials()
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeInternal, "未保存观影凭据，请重新登录"))
		return
	}
	client := guanying.SharedClient()
	captchaRequired, challenge, upstreamError, err := client.StartLogin(r.Context(), username, password, req.AttemptID)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%v", err))
		return
	}
	if captchaRequired {
		writeOK(w, map[string]any{
			"captcha_required": true,
			"attempt_id":       challenge.AttemptID,
			"captcha":          challenge,
		})
		return
	}
	if upstreamError != "" {
		writeErr(w, domain.Errorf(domain.CodeInternal, "%s", upstreamError))
		return
	}
	writeOK(w, map[string]any{"settings": guanying.SessionStatus(), "message": "观影登录已恢复"})
}

// guanyingTest 检查会话有效性
func (h *Handler) guanyingTest(w http.ResponseWriter, r *http.Request) {
	client := guanying.SharedClient()
	raw, ok := guanying.SessionStatus()["session_saved"].(bool)
	if !ok || !raw {
		writeErr(w, domain.Errorf(domain.CodeInternal, "尚未保存观影会话，请先登录"))
		return
	}
	if _, err := client.SearchResources(r.Context(), "__ping__", "movie", 0, ""); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "超时") || strings.Contains(msg, "connection") {
			writeErr(w, domain.Errorf(domain.CodeInternal, "%s", msg))
			return
		}
	}
	writeOK(w, map[string]any{"settings": guanying.SessionStatus(), "message": "观影登录态有效"})
}

// guanyingClearSession 清除会话
func (h *Handler) guanyingClearSession(w http.ResponseWriter, r *http.Request) {
	if err := guanying.ClearSession(); err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "清除观影会话失败：%v", err))
		return
	}
	_, _ = discovery.UpdateSettings(map[string]any{"guanying_enabled": false})
	writeOK(w, map[string]any{"message": "观影登录信息已清除", "settings": guanying.SessionStatus()})
}

// guanyingCatalog 观影最近更新目录（影视探索「观影」源）
func (h *Handler) guanyingCatalog(w http.ResponseWriter, r *http.Request) {
	if !guanyingEnabled() {
		writeErr(w, domain.Errorf(domain.CodeValidation, "观影未启用：请先在发现-基础配置中开启观影"))
		return
	}
	ty := "mv"
	if r.URL.Query().Get("media_type") == "tv" {
		ty = "tv"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	recents, hasNext, err := guanying.SharedClient().RecentUpdates(ctx, ty, page)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "观影目录加载失败：%v", err))
		return
	}
	items := make([]map[string]any, 0, len(recents))
	for _, rec := range recents {
		vote := rec.Douban
		if vote == 0 {
			vote = rec.IMDB
		}
		if vote == 0 {
			vote = rec.MAL
		}
		meta := rec.Status
		if len(rec.Quality) > 0 {
			meta = strings.TrimSpace(meta + " " + strings.Join(rec.Quality, " "))
		}
		items = append(items, map[string]any{
			"source":      "guanying",
			"media_type":  map[string]string{"mv": "movie", "tv": "tv", "ac": "tv"}[rec.Dir],
			"entity_key":  fmt.Sprintf("guanying:%s:%s", rec.Dir, rec.ID),
			"external_id": rec.ID,
			"title":       rec.Title,
			"poster":      rec.Poster,
			"vote_avg":    vote,
			"year":        rec.Year,
			"rank":        0,
			"genres":      rec.Quality,
			"overview":    meta,
			"detail_url":  rec.DetailURL,
			"air_date":    "",
		})
	}
	writeOK(w, map[string]any{
		"items":         items,
		"has_next_page": hasNext,
		"page":          page,
	})
}

// guanyingPlayPage 在线播放内嵌代理（免登录渲染播放器）
func (h *Handler) guanyingPlayPage(w http.ResponseWriter, r *http.Request) {
	if !guanyingEnabled() {
		http.Error(w, "观影未启用：请先在发现-基础配置中开启观影", http.StatusForbidden)
		return
	}
	line := chi.URLParam(r, "line")
	episode, _ := strconv.Atoi(chi.URLParam(r, "episode"))
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	html, err := guanying.SharedClient().PlayPageHTML(ctx, line, episode)
	if err != nil {
		http.Error(w, "观影播放页加载失败："+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

// guanyingDetailPage 内嵌详情代理（免登录渲染观影详情页）
func (h *Handler) guanyingDetailPage(w http.ResponseWriter, r *http.Request) {
	if !guanyingEnabled() {
		http.Error(w, "观影未启用：请先在发现-基础配置中开启观影", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	html, err := guanying.SharedClient().DetailPageHTML(ctx, chi.URLParam(r, "dir"), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "观影详情页加载失败："+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(html)
}

// guanyingImage 海报代理（tutu.pm 防盗链：带观影域 Referer 抓图转发）
func (h *Handler) guanyingImage(w http.ResponseWriter, r *http.Request) {
	if !guanyingEnabled() {
		http.Error(w, "观影未启用", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	body, ct, err := guanying.SharedClient().FetchImage(ctx, chi.URLParam(r, "dir"), chi.URLParam(r, "id"), chi.URLParam(r, "size"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(body)
}

// guanyingResProxy 观影站 /res/* 通配代理
func (h *Handler) guanyingResProxy(w http.ResponseWriter, r *http.Request) {
	if !guanyingEnabled() {
		http.Error(w, "观影未启用", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// chi 通配参数 "*" 为 /guanying/res/ 之后的部分，代理时补回 /res 前缀。
	body, ct, err := guanying.SharedClient().ProxyRes(ctx, "/res/"+chi.URLParam(r, "*"))
	if err != nil {
		http.Error(w, "观影接口代理失败："+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(body)
}
