package api

import (
	"net/http"
	"strconv"
	"strings"

	"litepan/internal/settings"
	"litepan/internal/wecom"
)

// 企业微信智能机器人的入站端点。
//
// ## 为什么不套 requireAdmin
//
// 企微是外部服务，没有浏览器会话。它带的是**签名**（msg_signature），
// 鉴权在 wecom.Bot.Handler 内部完成 —— 挂在管理员会话组内的话，
// 这个端点会永远收不到消息，表现为「配置好了但机器人不回话」。
//
// 这与 Telegram webhook 同一处理，但**签名机制完全不同**：
// Telegram 核对自己登记的 secret token 头，企微核 sha1(token,timestamp,nonce,encrypt)，
// 后者还要求消息必须能解密 —— 换句话说企微这条路的鉴权强度更高。

// wecomCallback 接收企微智能机器人的回调。
func (h *Handler) wecomCallback(w http.ResponseWriter, r *http.Request) {
	if h.wecomBot == nil {
		writeErr(w, errString("企业微信机器人未启用"))
		return
	}
	h.wecomBot.Handler().ServeHTTP(w, r)
}

// wecomBotConfigGet 返回企微机器人配置与命令表，供设置页展示。
//
// Token 与 EncodingAESKey 都**不回传明文**（settings 层按 Sensitive 打码），
// 另外回 configured 布尔：打码后的 "******" 无法区分「配了」与「没配」，
// 页面就没法在用户点保存前提醒他。
func (h *Handler) wecomBotConfigGet(w http.ResponseWriter, r *http.Request) {
	if h.wecomBot == nil {
		writeErr(w, errString("企业微信机器人未启用"))
		return
	}
	cfg := h.wecomBot.Config()
	commands := make([]map[string]any, 0, 32)
	for _, spec := range h.wecomBot.Registry().List() {
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
		"aes_configured":   cfg.AESKey != "",
		"allowed_users":    botIDList(cfg.AllowedUsers),
		"allowed_chats":    botIDList(cfg.AllowedChats),
		"group_link":       cfg.GroupLinkEnabled,
		"corp_id":          cfg.CorpID,
		"agent_id":         cfg.AgentID,
		"commands":         commands,
		"callback_path":    "/api/wecom/callback",
	})
}

// wecomBotConfigUpdate 保存企微机器人配置。
//
// 保存后才生效：先同步再保存的话，中间那段时间进来的消息会按旧配置被拒，
// 而用户刚看到「保存成功」。
func (h *Handler) wecomBotConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if h.wecomBot == nil {
		writeErr(w, errString("企业微信机器人未启用"))
		return
	}
	var req struct {
		Enabled      *bool   `json:"enabled"`
		Token        *string `json:"token"`
		AESKey       *string `json:"aes_key"`
		AllowedUsers *string `json:"allowed_users"`
		AllowedChats *string `json:"allowed_chats"`
		GroupLink    *bool   `json:"group_link"`
		CorpID       *string `json:"corp_id"`
		AgentID      *string `json:"agent_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	updates := map[string]string{}
	if req.Enabled != nil {
		updates[settings.KeyMOWecomBotEnabled] = strconv.FormatBool(*req.Enabled)
	}
	if req.GroupLink != nil {
		updates[settings.KeyMOWecomBotGroupLinkEnabled] = strconv.FormatBool(*req.GroupLink)
	}
	if req.AllowedUsers != nil {
		updates[settings.KeyMOWecomBotAllowedUsers] = strings.TrimSpace(*req.AllowedUsers)
	}
	if req.AllowedChats != nil {
		updates[settings.KeyMOWecomBotAllowedChats] = strings.TrimSpace(*req.AllowedChats)
	}
	if req.CorpID != nil {
		updates[settings.KeyMOWecomBotCorpID] = strings.TrimSpace(*req.CorpID)
	}
	if req.AgentID != nil {
		updates[settings.KeyMOWecomBotAgentID] = strings.TrimSpace(*req.AgentID)
	}
	// Secret 与 EncodingAESKey：空串 = 保持原值。
	//
	// 前端读不到明文（settings 按 Sensitive 打码），所以它不回传已保存的值。
	// 如果这里把空串当「清空」，用户改一下白名单就会把自己的凭证抹掉，
	// 而症状是「保存后机器人突然全拒」，且页面显示一切正常。
	for _, f := range []struct {
		key   string
		value *string
	}{
		{settings.KeyMOWecomBotToken, req.Token},
		{settings.KeyMOWecomBotAESKey, req.AESKey},
	} {
		if f.value == nil {
			continue
		}
		if v := strings.TrimSpace(*f.value); v != "" {
			updates[f.key] = v
		}
	}
	if len(updates) > 0 {
		if err := h.settings.Update(r.Context(), updates); err != nil {
			writeErr(w, err)
			return
		}
	}
	cfg := wecom.BotConfigFromSettings(h.settings)
	h.wecomBot.SyncConfig(cfg)
	writeOK(w, map[string]any{
		"enabled":          cfg.Enabled,
		"token_configured": cfg.Token != "",
		"aes_configured":   cfg.AESKey != "",
		"ready":            cfg.Ready(),
	})
}
