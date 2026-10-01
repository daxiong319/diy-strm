package api

import (
	"net/http"
	"strings"

	"litepan/internal/discover/discovery"
	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// Emby 缺集扫描（接线 internal/discover/discovery/emby_missing.go 的现成引擎：
// 选库扫描 → 系列缺集快照 → 补档订阅创建 → 事件流）。
// ---------------------------------------------------------------------------

// embyMissingStatus 缺集扫描状态
// GET /api/admin/discovery/emby-missing/status
func (h *Handler) embyMissingStatus(w http.ResponseWriter, r *http.Request) {
	status, err := discovery.EmbyMissingStatus()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, status)
}

// embyMissingLibraries Emby 可选媒体库
// GET /api/admin/discovery/emby-missing/libraries
func (h *Handler) embyMissingLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := discovery.EmbyMissingLibraries()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": libs})
}

// embyMissingScanStart 启动一次缺集扫描
// POST /api/admin/discovery/emby-missing/scan {library_ids: ["..."]}
func (h *Handler) embyMissingScanStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LibraryIDs []string `json:"library_ids"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
			return
		}
	}
	scan, err := discovery.StartEmbyMissingScan(req.LibraryIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"scan": scan})
}

// embyMissingScans 扫描历史
// GET /api/admin/discovery/emby-missing/scans?limit=20
func (h *Handler) embyMissingScans(w http.ResponseWriter, r *http.Request) {
	scans := discovery.EmbyMissingScansList(queryInt(r, "limit", 20))
	writeOK(w, map[string]any{"items": scans})
}

// embyMissingResults 某次扫描的缺集结果
// GET /api/admin/discovery/emby-missing/scans/{id}/results?limit=200
func (h *Handler) embyMissingResults(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	results, err := discovery.EmbyMissingResultsList(uint(id), queryInt(r, "limit", 200))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": results})
}

// embyMissingEvents 缺集事件流
// GET /api/admin/discovery/emby-missing/scans/{id}/events?limit=100
func (h *Handler) embyMissingEvents(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, err)
		return
	}
	events, err := discovery.EmbyMissingEventsList(uint(id), queryInt(r, "limit", 100))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": events})
}

// embyMissingSubscribe 为缺集结果创建补档订阅
// POST /api/admin/discovery/emby-missing/subscriptions
func (h *Handler) embyMissingSubscribe(w http.ResponseWriter, r *http.Request) {
	var req discovery.MissingSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误"))
		return
	}
	req.TargetProvider = strings.TrimSpace(req.TargetProvider)
	result, err := discovery.CreateMissingSubscriptions(&req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}
