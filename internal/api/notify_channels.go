package api

import (
	"encoding/json"
	"net/http"

	"litepan/internal/domain"
	"litepan/internal/notifychannel"
)

// decodeConfigMap 把渠道存储的 JSON 配置字符串解析为 map 返回给前端；失败返回空 map。
func decodeConfigMap(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out
	}
	return out
}

// notifyChannelMeta 下发全部渠道元数据（前端据此渲染配置表单）。
func (h *Handler) notifyChannelMeta(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	writeOK(w, map[string]any{"items": h.notifyChannels.Meta()})
}

func (h *Handler) listNotifyChannels(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	items, err := h.notifyChannels.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": toNotifyChannelDTOs(items)})
}

func (h *Handler) createNotifyChannel(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	var in notifychannel.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := h.notifyChannels.Create(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toNotifyChannelDTO(c))
}

func (h *Handler) updateNotifyChannel(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in notifychannel.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := h.notifyChannels.Update(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toNotifyChannelDTO(c))
}

func (h *Handler) deleteNotifyChannel(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.notifyChannels.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{})
}

func (h *Handler) testNotifyChannel(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyChannels != nil) {
		return
	}
	var in notifychannel.TestPayload
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.notifyChannels.Test(r.Context(), in); err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "发送失败：%v", err))
		return
	}
	writeOK(w, map[string]any{})
}

type notifyChannelDTO struct {
	ID        int64             `json:"id"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Config    map[string]string `json:"config"`
	Enabled   bool              `json:"enabled"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
}

func toNotifyChannelDTO(c *domain.NotifyChannel) map[string]any {
	return map[string]any{
		"id":         c.ID,
		"type":       c.Type,
		"name":       c.Name,
		"config":     decodeConfigMap(c.Config),
		"enabled":    c.Enabled,
		"created_at": FormatAPITime(c.CreatedAt),
		"updated_at": FormatAPITime(c.UpdatedAt),
	}
}

func toNotifyChannelDTOs(items []*domain.NotifyChannel) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		out = append(out, toNotifyChannelDTO(it))
	}
	return out
}
