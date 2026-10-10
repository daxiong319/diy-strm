package api

import (
	"net/http"
	"strconv"
	"strings"

	"litepan/internal/settings"
	"litepan/internal/tgbot"
)

// telegramWebhook 是 Telegram Bot 的唯一入站端点。
//
// 鉴权在 tgbot.Service.Handler 内部按 secret token 头完成 —— 它是
// Bot API 的标准机制（setWebhook 时带 secret_token），不是本项目的发明。
// 这里刻意不套 requireAdmin：Telegram 没有浏览器会话，
// 挂在管理员会话组内等于这个端点永远收不到消息。
func (h *Handler) telegramWebhook(w http.ResponseWriter, r *http.Request) {
	if h.tgbot == nil {
		writeErr(w, errString("Telegram Bot 未启用"))
		return
	}
	h.tgbot.Handler(h.tgbotWebhookSecret()).ServeHTTP(w, r)
}

// tgbotWebhookSecret 取 Telegram 带来的 secret token。
//
// Telegram 会把 setWebhook 时登记的 secret_token 原样回送在
// X-Telegram-Bot-Api-Secret-Token 头里。我们没有单独登记一个 secret，
// 所以这里把请求头原样交给 tgbot.Handler —— 由它决定「没配就放行」还是
// 「配了就必须对上」。把这个判断留在 Bot 包里而不是这里，
// 是为了让「Bot 未启用 / secret 不对 / 处理成功」三种情况的口径只有一处定义。
// botIDList 让配置以数字数组下行。
//
// 页面要按数字展示和回填（群 ID 是 -100 开头的大整数，当字符串处理迟早有人写成
// "−100…" 这种 Unicode 减号，回填时就被静默丢掉）。
func botIDList(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	return ids
}

func (h *Handler) tgbotWebhookSecret() string {
	if h.tgbot == nil {
		return ""
	}
	return h.tgbot.WebhookSecret()
}

// telegramBotConfigGet 返回 Bot 配置与命令表，供设置页展示。
//
// token 不回传明文（settings 层已按 Sensitive 打码），
// 这里额外回一个 token_configured 布尔：打码后的 "******" 无法区分
// 「配了」与「没配」，页面就只能说「已配置」而没法在用户点保存前提示他。
func (h *Handler) telegramBotConfigGet(w http.ResponseWriter, r *http.Request) {
	if h.tgbot == nil {
		writeErr(w, errString("Telegram Bot 未启用"))
		return
	}
	cfg := h.tgbot.Config()
	commands := make([]map[string]any, 0, 32)
	for _, spec := range h.tgbot.Registry().List() {
		commands = append(commands, map[string]any{
			"name":      spec.Name,
			"aliases":   spec.Aliases,
			"summary":   spec.Summary,
			"usage":     spec.Usage,
			"tier":      spec.Tier.String(),
			"supported": spec.Supported,
		})
	}
	writeOK(w, map[string]any{
		"enabled":          cfg.Enabled,
		"token_configured": cfg.Token != "",
		"allowed_users":    botIDList(cfg.AllowedUsers),
		"allowed_chats":    botIDList(cfg.AllowedChats),
		"group_link":       cfg.GroupLinkEnabled,
		"commands":         commands,
		"webhook_path":     "/api/telegram/webhook",
	})
}

// telegramBotConfigUpdate 保存 Bot 配置并立即同步 webhook。
//
// 保存后才同步：先同步再保存的话，Telegram 侧登记了新 token 而本地还没生效，
// 中间那段时间进来的消息会被本地按旧配置拒绝，而用户刚看到「保存成功」。
func (h *Handler) telegramBotConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if h.tgbot == nil {
		writeErr(w, errString("Telegram Bot 未启用"))
		return
	}
	var req struct {
		Enabled      *bool   `json:"enabled"`
		Token        *string `json:"token"`
		AllowedUsers *string `json:"allowed_users"`
		AllowedChats *string `json:"allowed_chats"`
		GroupLink    *bool   `json:"group_link"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	updates := map[string]string{}
	if req.Enabled != nil {
		updates[settings.KeyMOTelegramBotEnabled] = strconv.FormatBool(*req.Enabled)
	}
	if req.GroupLink != nil {
		updates[settings.KeyMOTelegramBotGroupLinkEnabled] = strconv.FormatBool(*req.GroupLink)
	}
	if req.AllowedUsers != nil {
		updates[settings.KeyMOTelegramBotAllowedUsers] = strings.TrimSpace(*req.AllowedUsers)
	}
	if req.AllowedChats != nil {
		updates[settings.KeyMOTelegramBotAllowedChats] = strings.TrimSpace(*req.AllowedChats)
	}
	if req.Token != nil {
		token := strings.TrimSpace(*req.Token)
		if token != "" {
			updates[settings.KeyMOTelegramBotToken] = token
		}
		// 空串 = 保持原值。前端不回传已保存的 token（它读不到明文），
		// 如果这里把空串当「清空」，用户改一下白名单就会把自己的 token 抹掉。
	}
	if len(updates) > 0 {
		if err := h.settings.Update(r.Context(), updates); err != nil {
			writeErr(w, err)
			return
		}
	}
	cfg := tgbot.ConfigFromSettings(h.settings)
	if err := h.tgbot.SyncConfig(r.Context(), cfg); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"enabled": cfg.Enabled, "token_configured": cfg.Token != ""})
}
