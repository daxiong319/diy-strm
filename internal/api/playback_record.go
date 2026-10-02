package api

import (
	"net/http"

	"litepan/internal/domain"
)

// 播放记录面板（对齐老版 GET /api/emby302/playback-records 契约）：
// 老版该路由挂在 JWT 鉴权组下（不是公开路由），现版后台页面整体走管理员会话，
// 因此归入 /api/admin 的 requireAdmin 分组。
//
// 过滤参数：id=反代规则 ID、user_id、provider、keyword（条目名/路径模糊匹配）。
// 老版只支持 id/page/page_size，这里保留三者并扩展为面板可用的筛选。

// playbackRecords 播放记录分页列表。
// GET /api/admin/playback-records?id=&user_id=&provider=&keyword=&page=&page_size=
func (h *Handler) playbackRecords(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.storePlaybackRecords != nil) {
		return
	}
	page := queryPage(r, "page", 1)
	pageSize := queryPage(r, "page_size", 20)
	items, total, err := h.storePlaybackRecords.List(r.Context(), domain.PlaybackRecordQuery{
		RuleID:   r.URL.Query().Get("id"),
		UserID:   r.URL.Query().Get("user_id"),
		Provider: r.URL.Query().Get("provider"),
		Keyword:  r.URL.Query().Get("keyword"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// playbackRecordsStats 播放记录概览（总条数 / 最近播放 / 去重用户与条目数）。
// GET /api/admin/playback-records/stats
func (h *Handler) playbackRecordsStats(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.storePlaybackRecords != nil) {
		return
	}
	stats, err := h.storePlaybackRecords.Stats(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, stats)
}

// playbackRecordDelete 删除单条播放记录。
// DELETE /api/admin/playback-records/{id}
func (h *Handler) playbackRecordDelete(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.storePlaybackRecords != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.storePlaybackRecords.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": 1})
}

// playbackRecordsClear 按条件清空播放记录。
// POST /api/admin/playback-records/clear {rule_id, user_id}
// 两个条件都为空即清空全部，前端在执行前会二次确认。
func (h *Handler) playbackRecordsClear(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.storePlaybackRecords != nil) {
		return
	}
	var req struct {
		RuleID string `json:"rule_id"`
		UserID string `json:"user_id"`
	}
	// 允许空请求体：等价于清空全部，避免前端为了「清空」还要造一个 body。
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, err)
			return
		}
	}
	n, err := h.storePlaybackRecords.Clear(r.Context(), req.RuleID, req.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": n})
}

// queryPage 读取分页参数，非法或非正值回退默认值。
func queryPage(r *http.Request, name string, def int) int {
	v := queryInt(r, name, def)
	if v < 1 {
		return def
	}
	return v
}
