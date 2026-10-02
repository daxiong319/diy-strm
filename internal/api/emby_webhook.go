package api

import (
	"io"
	"net/http"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/embywebhook"
	"litepan/internal/settings"
	"litepan/pkg/safego"
)

// embyWebhookMaxBody 限制 Webhook 请求体大小，防止超大请求打爆内存。
const embyWebhookMaxBody = 1 << 20

// embyWebhookHandler 处理 Emby/Jellyfin 服务端 Webhook 回调。
// 该路由位于 /api/open 免登录分组下，鉴权完全由本处理器根据
// settings.KeyEmbyWebhookAuthEnabled 决定：开启后必须携带
// X-API-Key 请求头或 api_key 查询参数（对齐老实现的两种传法）。
func (h *Handler) embyWebhook(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.embyWebhookSvc != nil) {
		return
	}
	// 总开关关闭时按「未开启」处理，避免 Emby 拿到 200 以为推送成功。
	if h.settings == nil || !h.settings.Bool(settings.KeyEmbyWebhookEnabled) {
		writeErr(w, domain.Errorf(domain.CodePermissionDenied, "Emby Webhook 未开启"))
		return
	}
	if err := h.checkEmbyWebhookAuth(r); err != nil {
		writeErr(w, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, embyWebhookMaxBody))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请求体读取失败：%v", err))
		return
	}
	// 同步解析一次：格式错误要立刻反馈给 Emby，不能静默吞掉。
	event, err := embywebhook.ParseEvent(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 不认识的事件类型快速返回 200，避免 Emby 判定失败后反复重试。
	if !embyWebhookSupportedEvent(event.Event) {
		writeOK(w, map[string]any{"accepted": false, "event": event.Event, "reason": "ignored"})
		return
	}
	// 通知走异步：回查条目详情与 Telegram/Bark 推送都可能很慢，
	// 阻塞会拖垮 Emby 的 Webhook 队列并触发重试风暴。
	svc, body, log := h.embyWebhookSvc, raw, h.log
	eventName := event.Event
	safego.Guard(log, "emby-webhook", func() {
		if err := svc.HandleWebhook(r.Context(), body); err != nil {
			log.Warn("Emby Webhook 事件处理失败", "event", eventName, "error", err)
		}
	})
	writeOK(w, map[string]any{"accepted": true, "event": eventName})
}

// embyWebhookSupportedEvent 判断事件类型是否由本服务处理。
// 与 embywebhook.Service.dispatch 的 case 分支保持一致。
func embyWebhookSupportedEvent(event string) bool {
	switch event {
	case embywebhook.EventLibraryNew,
		embywebhook.EventLibraryModified,
		embywebhook.EventLibraryDeleted,
		embywebhook.EventPlaybackStart,
		embywebhook.EventPlaybackPause,
		embywebhook.EventPlaybackStop:
		return true
	default:
		return false
	}
}

// checkEmbyWebhookAuth 校验 API Key；未开启鉴权时直接放行。
func (h *Handler) checkEmbyWebhookAuth(r *http.Request) error {
	if h.settings == nil || !h.settings.Bool(settings.KeyEmbyWebhookAuthEnabled) {
		return nil
	}
	if h.apiKeys == nil {
		return domain.Errorf(domain.CodeNotImplement, "API Key 服务未就绪")
	}
	key := embyWebhookAPIKey(r)
	if key == "" {
		return domain.Errorf(domain.CodeAdminAuthRequired, "缺少 API Key")
	}
	if _, err := h.apiKeys.Validate(r.Context(), key); err != nil {
		return err
	}
	return nil
}

// embyWebhookAPIKey 依次从 X-API-Key 请求头、Authorization 请求头、
// api_key 查询参数中取 Key，兼容老实现的两种传法。
func embyWebhookAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		return auth
	}
	return strings.TrimSpace(r.URL.Query().Get("api_key"))
}
