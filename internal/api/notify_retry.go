package api

import (
	"net/http"
	"time"

	"litepan/internal/domain"
	"litepan/internal/notifychannel"
)

// 通知场景（T12）与补发队列（T12）的 API。
//
// ⚠️ 场景清单**唯一真相来源是 domain.NotificationScenes()**，API 层不做任何
// 增删补全 —— 前端渲染的每一行、过滤判定的每一个 category 都来自同一份
// catalog。两边各写一份的场景列表早晚会漂移，而漂移的表现是「用户关了
// 「同步」，但某类通知还在响」这种查不出原因的问题。

// notifySceneDTO 场景下发给前端的形状。
//
// Categories 为空表示「这个场景目前还没有通知产出点」，前端据此显示
// 占位说明而不是渲染一个勾了也没用的复选框 —— 照实说比留白好。
type notifySceneDTO struct {
	Scene      domain.NotificationScene `json:"scene"`
	Label      string                   `json:"label"`
	Trigger    string                   `json:"trigger"`
	Categories []string                 `json:"categories"`
}

// notifySceneList 返回场景清单与当前未被任何场景覆盖的分类。
func (h *Handler) notifySceneList(w http.ResponseWriter, r *http.Request) {
	items := make([]notifySceneDTO, 0, len(domain.NotificationScenes()))
	for _, s := range domain.NotificationScenes() {
		items = append(items, notifySceneDTO{
			Scene:      s.Scene,
			Label:      s.Label,
			Trigger:    s.Trigger,
			Categories: s.Categories,
		})
	}
	writeOK(w, map[string]any{
		"items": items,
		// 未归类分类恒送达（不受场景开关影响），前端要显式告诉用户这件事。
		"unmapped": domain.UnmappedCategories(),
		"notes": []string{
			"场景清单按 参考实现 文档的触发时机表落地，未经逆向确认完整名单。",
			"未归入任何场景的告警（如账号认证失效）始终送达，不受场景开关影响。",
			"未勾选任何场景 = 订阅全部场景，与未配置时行为一致。",
		},
	})
}

// notifyRetryDTO 补发队列的列表项。
type notifyRetryDTO struct {
	ID          int64                    `json:"id"`
	Scene       domain.NotificationScene `json:"scene"`
	SceneLabel  string                   `json:"scene_label"`
	ChannelType string                   `json:"channel_type"`
	ChannelName string                   `json:"channel_name"`
	TargetURL   string                   `json:"target_url"`
	Title       string                   `json:"title"`
	Content     string                   `json:"content"`
	Tone        string                   `json:"tone"`
	Attempts    int                      `json:"attempts"`
	MaxAttempts int                      `json:"max_attempts"`
	NextRetryAt string                   `json:"next_retry_at"`
	LastError   string                   `json:"last_error"`
	Status      string                   `json:"status"`
	Redrivable  bool                     `json:"redrivable"`
	CreatedAt   string                   `json:"created_at"`
	UpdatedAt   string                   `json:"updated_at"`
}

func toNotifyRetryDTOs(entries []domain.NotifyRetryEntry) []notifyRetryDTO {
	out := make([]notifyRetryDTO, 0, len(entries))
	for _, e := range entries {
		item := notifyRetryDTO{
			ID:          e.ID,
			Scene:       e.EventScene,
			ChannelType: e.ChannelType,
			ChannelName: e.ChannelName,
			TargetURL:   e.TargetURL,
			Title:       e.Title,
			Content:     e.Content,
			Tone:        e.Tone,
			Attempts:    e.Attempts,
			MaxAttempts: notifychannel.NotifyRetryMaxAttempts(),
			LastError:   e.LastError,
			Status:      string(e.Status),
			// 只有失败态能重投：pending 还在退避途中，重投等于免费插队。
			Redrivable: e.Status == domain.NotifyRetryStatusFailed,
			CreatedAt:  FormatAPITime(e.CreatedAt),
			UpdatedAt:  FormatAPITime(e.UpdatedAt),
		}
		if label, ok := domain.SceneLabel(e.EventScene); ok {
			item.SceneLabel = label
		}
		if !e.NextRetryAt.IsZero() {
			item.NextRetryAt = FormatAPITime(e.NextRetryAt)
		}
		out = append(out, item)
	}
	return out
}

// notifyRetryList 补发队列列表。status 留空 = 全部。
func (h *Handler) notifyRetryList(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyRetries != nil) {
		return
	}
	limit := int64(0)
	if v := r.URL.Query().Get("limit"); v != "" {
		parsed, err := parseQueryInt64(r, "limit")
		if err != nil {
			writeErr(w, err)
			return
		}
		limit = parsed
	}
	offset := int64(0)
	if v := r.URL.Query().Get("offset"); v != "" {
		parsed, err := parseQueryInt64(r, "offset")
		if err != nil {
			writeErr(w, err)
			return
		}
		offset = parsed
	}
	items, total, err := h.notifyRetries.List(r.Context(), domain.NotifyRetryQuery{
		Status: domain.NotifyRetryStatus(r.URL.Query().Get("status")),
		Limit:  int(limit),
		Offset: int(offset),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	counts, err := h.notifyRetries.Counts(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"items": toNotifyRetryDTOs(items),
		"total": total,
		"counts": map[string]int64{
			string(domain.NotifyRetryStatusPending): counts[domain.NotifyRetryStatusPending],
			string(domain.NotifyRetryStatusSent):    counts[domain.NotifyRetryStatusSent],
			string(domain.NotifyRetryStatusFailed):  counts[domain.NotifyRetryStatusFailed],
		},
		"max_attempts": notifychannel.NotifyRetryMaxAttempts(),
	})
}

// notifyRetryDetail 单条补发记录。
func (h *Handler) notifyRetryDetail(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyRetries != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := h.notifyRetries.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": toNotifyRetryDTOs([]domain.NotifyRetryEntry{e})[0]})
}

// notifyRetryRedrive 手动重投：把失败记录清零并立即到点。
func (h *Handler) notifyRetryRedrive(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyRetries != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.notifyRetries.Redrive(r.Context(), id, time.Now()); err != nil {
		writeErr(w, err)
		return
	}
	// 立刻跑一轮补发：用户点了重投却要等最多 30 秒才看到结果，
	// 会以为按钮坏了。
	if h.notifyRetryRunner != nil {
		h.notifyRetryRunner.RunOnce(r.Context())
	}
	e, err := h.notifyRetries.Get(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": toNotifyRetryDTOs([]domain.NotifyRetryEntry{e})[0]})
}

// notifyRetryClear 清理补发记录。status 留空 = 全清。
func (h *Handler) notifyRetryClear(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.notifyRetries != nil) {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	n, err := h.notifyRetries.Clear(r.Context(), domain.NotifyRetryStatus(in.Status))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"cleared": n})
}
