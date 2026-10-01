package api

import (
	"net/http"
	"strconv"

	"litepan/internal/discover/discovery"
)

// ---------------------------------------------------------------------------
// 资源订阅追更（接线 internal/discover/discovery/subscriptions.go 的现成引擎：
// ProcessSubscription / RunDueSubscriptions / subscriptionWorker 已随
// StartDiscoveryWorkers 常驻，这里只补 API 层）。
// ---------------------------------------------------------------------------

// subscriptionList 订阅列表
// GET /api/admin/discovery/subscriptions?enabled=true
func (h *Handler) subscriptionList(w http.ResponseWriter, r *http.Request) {
	var enabled *bool
	if raw := r.URL.Query().Get("enabled"); raw != "" {
		v := raw == "true" || raw == "1"
		enabled = &v
	}
	list, err := discovery.ListSubscriptions(enabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": list})
}

// subscriptionGet 订阅详情
// GET /api/admin/discovery/subscriptions/{id}
func (h *Handler) subscriptionGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	sub, err := discovery.GetSubscription(uint(id))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": sub})
}

// subscriptionByKey 按实体键查订阅（详情页判断是否已订阅）
// GET /api/admin/discovery/subscriptions/by-key?entity_key=tmdb:tv:12345
func (h *Handler) subscriptionByKey(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("entity_key")
	if key == "" {
		writeErr(w, errString("缺少 entity_key"))
		return
	}
	sub, err := discovery.GetSubscriptionByKey(key)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": sub, "subscribed": sub != nil})
}

// subscriptionSave 创建/更新订阅
// POST /api/admin/discovery/subscriptions
func (h *Handler) subscriptionSave(w http.ResponseWriter, r *http.Request) {
	var payload discovery.SubscriptionUpsertPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeErr(w, err)
		return
	}
	sub, warning, err := discovery.SaveSubscription(&payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": sub, "warning": warning})
}

// subscriptionDelete 删除订阅
// DELETE /api/admin/discovery/subscriptions/{id}
func (h *Handler) subscriptionDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	if err := discovery.DeleteSubscription(uint(id)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// subscriptionToggle 启用/停用订阅
// POST /api/admin/discovery/subscriptions/{id}/toggle
func (h *Handler) subscriptionToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sub, err := discovery.GetSubscription(uint(id))
	if err != nil {
		writeErr(w, err)
		return
	}
	target := !sub.Enabled
	if req.Enabled != nil {
		target = *req.Enabled
	}
	payload := subscriptionToPayload(sub)
	payload.Enabled = &target
	updated, _, err := discovery.SaveSubscription(payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": updated})
}

// subscriptionRun 立即执行一次订阅检查
// POST /api/admin/discovery/subscriptions/{id}/run
func (h *Handler) subscriptionRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	run, err := discovery.ProcessSubscription(uint(id), "manual")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"run": run})
}

// subscriptionRuns 订阅执行记录
// GET /api/admin/discovery/subscriptions/{id}/runs?limit=20
func (h *Handler) subscriptionRuns(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	limit := queryInt(r, "limit", 20)
	runs, err := discovery.ListSubscriptionRuns(uint(id), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": runs})
}

// subscriptionEvents 订阅事件流（监控历史）
// GET /api/admin/discovery/subscriptions/{id}/events?limit=50
func (h *Handler) subscriptionEvents(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	limit := queryInt(r, "limit", 50)
	events, err := discovery.ListSubscriptionEvents(uint(id), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": events})
}

// subscriptionItems 订阅条目（转存明细）
// GET /api/admin/discovery/subscriptions/{id}/items?status=&limit=100
func (h *Handler) subscriptionItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	limit := queryInt(r, "limit", 100)
	items, err := discovery.ListSubscriptionItems(uint(id), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": items})
}

// subscriptionRunDue 执行全部到期订阅（手动触发调度）
// POST /api/admin/discovery/subscriptions/run-due
func (h *Handler) subscriptionRunDue(w http.ResponseWriter, r *http.Request) {
	results := discovery.RunDueSubscriptions(queryInt(r, "limit", 5))
	writeOK(w, map[string]any{"items": results})
}

// subscriptionToPayload 模型 → UPSERT 载荷（改状态时保留全部字段）
func subscriptionToPayload(sub *discovery.DiscoverySubscription) *discovery.SubscriptionUpsertPayload {
	payload := &discovery.SubscriptionUpsertPayload{
		Source:          sub.Source,
		EntityType:      sub.EntityType,
		ExternalID:      sub.ExternalID,
		TMDBID:          sub.TMDBID,
		MediaType:       sub.MediaType,
		Title:           sub.Title,
		OriginalTitle:   sub.OriginalTitle,
		Poster:          sub.Poster,
		TargetProvider:  sub.TargetProvider,
		TransferMode:    sub.TransferMode,
		IntervalMinutes: sub.IntervalMinutes,
		Preferences:     sub.Pref,
		Metadata:        sub.Meta,
	}
	enabled := sub.Enabled
	payload.Enabled = &enabled
	if sub.EntityKey != "" {
		payload.ExternalID = sub.ExternalID
	}
	for _, rule := range sub.Rules {
		ruleEnabled := rule.Enabled
		payload.Rules = append(payload.Rules, discovery.SubscriptionRulePayload{
			Name:            rule.Name,
			Enabled:         &ruleEnabled,
			TargetProvider:  rule.TargetProvider,
			MaxPoints:       rule.MaxPoints,
			Preferences:     rule.Pref,
			MessageKeywords: stringListFromAnyLocal(rule.MatchData["message_keywords"]),
			MustContain:     stringListFromAnyLocal(rule.MatchData["must_contain"]),
			MustNotContain:  stringListFromAnyLocal(rule.MatchData["must_not_contain"]),
		})
	}
	return payload
}

// queryInt 读取整型查询参数（缺省/非法回落默认值）
func queryInt(r *http.Request, name string, def int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// stringListFromAnyLocal any → []string（前端提交的规则词表）
func stringListFromAnyLocal(v any) []string {
	switch typed := v.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// errString 简易错误类型（避免额外引入 errors 包装）
type errString string

func (e errString) Error() string { return string(e) }
