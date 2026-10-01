package api

import (
	"net/http"
	"strings"
	"time"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// TG 频道订阅管理（接线 internal/discover/discovery/channels.go 数据层 +
// channel_watcher.go 引擎）。频道是「增量拉帖的入口」，订阅是「筛选与转存的目标」，
// 两者通过 source_type（目标网盘）关联：每网盘可挂多个频道，订阅跨频道共享。
// ---------------------------------------------------------------------------

// channelList 频道列表
// GET /api/admin/discovery/channels?source_type=123
func (h *Handler) channelList(w http.ResponseWriter, r *http.Request) {
	sourceType, err := normalizeChannelSource(r.URL.Query().Get("source_type"))
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := discovery.ListChannels(sourceType)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": list})
}

// channelSave 新增/更新频道
// POST /api/admin/discovery/channels {source_type, channel, enabled}
func (h *Handler) channelSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         uint   `json:"id"`
		SourceType string `json:"source_type"`
		Channel    string `json:"channel"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	sourceType, err := discovery.NormalizeTransferProvider(req.SourceType)
	if err != nil {
		writeErr(w, err)
		return
	}
	ch := &discovery.DiscoveryChannel{
		ID:         req.ID,
		SourceType: sourceType,
		Channel:    req.Channel,
	}
	// 归一化后再落库：用户粘 t.me 链接与手输 @名 很常见，
	// 不归一化会让唯一索引 (source_type, channel) 拦不住重复添加。
	ch.Channel = ch.ChannelName()
	if ch.Channel == "" {
		writeErr(w, errString("缺少频道名"))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ch.Enabled = enabled
	if err := discovery.SaveChannel(ch); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": ch})
}

// channelDelete 删除频道
// DELETE /api/admin/discovery/channels/{id}
func (h *Handler) channelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	if err := discovery.DeleteChannel(uint(id)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// channelToggle 启用/停用频道
// POST /api/admin/discovery/channels/{id}/toggle {enabled}
func (h *Handler) channelToggle(w http.ResponseWriter, r *http.Request) {
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
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else {
		// 未显式传值时取反（前端开关直接点击的场景）
		ch, err := discovery.GetChannel(uint(id))
		if err != nil {
			writeErr(w, err)
			return
		}
		enabled = !ch.Enabled
	}
	if err := discovery.SetChannelEnabled(uint(id), enabled); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"enabled": enabled})
}

// channelResetCursor 重置频道游标（清空 LastPostID 后下一轮从头回溯）。
// 排查漏帖时用：游标一旦推进过头，只能重置后重扫。
// POST /api/admin/discovery/channels/{id}/reset-cursor
func (h *Handler) channelResetCursor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	if _, err := discovery.GetChannel(uint(id)); err != nil {
		writeErr(w, err)
		return
	}
	if err := discovery.UpdateChannelCursor(uint(id), "", time.Now()); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"ok": true})
}

// channelPreview 预览频道最新帖（不改游标、不转存，用于添加频道前确认内容质量）
// GET /api/admin/discovery/channels/preview?channel=xxx&limit=10
func (h *Handler) channelPreview(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		writeErr(w, errString("缺少 channel"))
		return
	}
	posts, err := discovery.PreviewChannel(channel, queryInt(r, "limit", 10))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": posts})
}

// channelRunNow 立即执行一次频道抓取（不限网盘，跑全部启用频道）。
// 与「订阅立即运行」的区别：这里只抓帖并按订阅分发，不推进单订阅语义。
// POST /api/admin/discovery/channels/run-now
func (h *Handler) channelRunNow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceType string `json:"source_type"`
	}
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	sourceType, err := normalizeChannelSource(req.SourceType)
	if err != nil {
		writeErr(w, err)
		return
	}
	summary, err := discovery.RunAllChannelSubscriptions(sourceType)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"summary": summary})
}

// monitorRecordList 监控历史列表（频道订阅的每一次转存尝试留痕）
// GET /api/admin/discovery/monitor-records?source_type=&status=&keyword=&page=1&page_size=20
func (h *Handler) monitorRecordList(w http.ResponseWriter, r *http.Request) {
	sourceType, err := normalizeChannelSource(r.URL.Query().Get("source_type"))
	if err != nil {
		writeErr(w, err)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	keyword := r.URL.Query().Get("keyword")
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 20)
	records, total, err := discovery.ListMonitorRecords(sourceType, status, keyword, page, pageSize)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": records, "total": total, "page": page, "page_size": pageSize})
}

// monitorRecordSources 各来源记录数（前端来源标签角标）
// GET /api/admin/discovery/monitor-records/sources
func (h *Handler) monitorRecordSources(w http.ResponseWriter, r *http.Request) {
	counts, err := discovery.CountMonitorRecordsBySource()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": counts})
}

// monitorRecordDelete 批量删除监控历史
// POST /api/admin/discovery/monitor-records/delete {ids:[1,2]}
func (h *Handler) monitorRecordDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, errString("缺少 ids"))
		return
	}
	if err := discovery.DeleteMonitorRecords(req.IDs); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": len(req.IDs)})
}

// monitorRecordClear 清空监控历史（可按来源与时间范围过滤）
// POST /api/admin/discovery/monitor-records/clear {source_type,start,end}
func (h *Handler) monitorRecordClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceType string `json:"source_type"`
		Start      string `json:"start"`
		End        string `json:"end"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
			return
		}
	}
	sourceType, err := normalizeChannelSource(req.SourceType)
	if err != nil {
		writeErr(w, err)
		return
	}
	var start, end *time.Time
	if req.Start != "" {
		t, perr := time.Parse(time.RFC3339, req.Start)
		if perr != nil {
			writeErr(w, errString("start 时间格式应为 RFC3339"))
			return
		}
		start = &t
	}
	if req.End != "" {
		t, perr := time.Parse(time.RFC3339, req.End)
		if perr != nil {
			writeErr(w, errString("end 时间格式应为 RFC3339"))
			return
		}
		end = &t
	}
	affected, err := discovery.ClearMonitorRecords(sourceType, start, end)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": affected})
}

// normalizeChannelSource 频道/监控筛选的来源网盘归一化（空值 = 全部网盘，用于列表筛选）
func normalizeChannelSource(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return discovery.NormalizeTransferProvider(raw)
}
