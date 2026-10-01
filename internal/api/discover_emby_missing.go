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

// embyMissingConfigGet 缺集扫描专用的 Emby 配置
// GET /api/admin/discovery/emby-missing/config
//
// ★ 该配置与别的功能使用的全局 Emby 配置完全隔离（settings key media_emby），
// 未配置时如实返回空串，前端因此显示为空表单而不是被别人预填。
// API Key 不回显明文，只回传是否已填写，避免凭据出现在响应体里。
func (h *Handler) embyMissingConfigGet(w http.ResponseWriter, r *http.Request) {
	enabled, serverURL, apiKey := discovery.MediaEmbyConfig()
	writeOK(w, map[string]any{
		"enabled":      enabled,
		"server_url":   serverURL,
		"api_key_set":  apiKey != "",
		"configured":   serverURL != "" && apiKey != "",
		"independent":  true,
		"settings_key": discovery.SettingMediaEmby,
	})
}

// embyMissingConfigSave 保存缺集扫描专用的 Emby 配置
// PUT /api/admin/discovery/emby-missing/config {enabled, server_url, api_key}
//
// api_key 留空 = 保持原有 Key 不变（前端不回显明文，所以无法回传原值）。
func (h *Handler) embyMissingConfigSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled   bool   `json:"enabled"`
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "参数错误：%v", err))
		return
	}
	serverURL := strings.TrimSpace(req.ServerURL)
	apiKey := strings.TrimSpace(req.APIKey)

	// 地址一旦填写必须是 http(s) 绝对地址，否则扫描阶段才会以 dial 错误暴露，体验差。
	if serverURL != "" && !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		writeErr(w, domain.Errorf(domain.CodeValidation, "服务器地址需以 http:// 或 https:// 开头"))
		return
	}
	if serverURL != "" && apiKey == "" {
		// 未传新 Key 时沿用已存的 Key（避免用户只改地址就把 Key 清掉）
		if _, _, existing := discovery.MediaEmbyConfig(); existing != "" {
			apiKey = existing
		} else {
			writeErr(w, domain.Errorf(domain.CodeValidation, "首次配置需同时填写 API Key"))
			return
		}
	}
	if req.Enabled && (serverURL == "" || apiKey == "") {
		writeErr(w, domain.Errorf(domain.CodeValidation, "启用前请先填写服务器地址与 API Key"))
		return
	}
	if _, err := discovery.UpdateSettings(map[string]any{
		discovery.SettingMediaEmby: map[string]any{
			"enabled":    req.Enabled,
			"server_url": serverURL,
			"api_key":    apiKey,
		},
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"enabled":     req.Enabled,
		"server_url":  serverURL,
		"api_key_set": apiKey != "",
		"configured":  serverURL != "" && apiKey != "",
	})
}

// embyMissingConfigTest 用当前（或请求体中的）配置探活 Emby
// POST /api/admin/discovery/emby-missing/config/test {server_url?, api_key?}
func (h *Handler) embyMissingConfigTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &req)
	}
	serverURL := strings.TrimSpace(req.ServerURL)
	apiKey := strings.TrimSpace(req.APIKey)
	if serverURL == "" || apiKey == "" {
		// 回落到已保存的配置
		_, savedURL, savedKey := discovery.MediaEmbyConfig()
		if serverURL == "" {
			serverURL = savedURL
		}
		if apiKey == "" {
			apiKey = savedKey
		}
	}
	if serverURL == "" || apiKey == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请先填写服务器地址与 API Key"))
		return
	}
	libs, err := discovery.EmbyMissingProbe(serverURL, apiKey)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation,
			"无法连接 Emby（%s）：请确认地址、API Key 与服务运行状态：%v", serverURL, err))
		return
	}
	writeOK(w, map[string]any{"ok": true, "library_count": len(libs), "libraries": libs})
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
