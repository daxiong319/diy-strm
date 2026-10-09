package api

import (
	"net/http"

	"litepan/internal/cache"
	"litepan/internal/settings"
)

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeOK(w, map[string]any{"categories": nil, "items": nil})
		return
	}
	// include_hidden=1：连带返回 Hidden 键（界面偏好等），敏感值仍打码。
	if r.URL.Query().Get("include_hidden") == "1" {
		writeOK(w, h.settings.SnapshotAll())
		return
	}
	writeOK(w, h.settings.Snapshot())
}

// GET /admin/settings/index  设置项搜索索引（后台 ⌘G 直达）
//
// 只读、纯派生：不查数据库、不依赖 h.settings 是不是装配了。
//
// ⚠️ 刻意不走 settings.Service：索引是**元数据**而不是「元数据 + 当前值」，
// 而 Snapshot() 会带上用户的实际配置值 —— 一个只用来搜索的端点把 190 个
// 设置的实际值全吐出来，等于给任何管理员一个「读全部配置」的旁路。
// 派生不依赖服务装配还有一个好处：注册表是静态声明，服务起没起都答得上来。
func (h *Handler) getSettingsIndex(w http.ResponseWriter, r *http.Request) {
	writeOK(w, settings.BuildIndex())
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var previousActiveRefresh bool
	if _, ok := in[settings.KeyAuthActiveRefresh]; ok && h.settings != nil {
		previousActiveRefresh = h.settings.Bool(settings.KeyAuthActiveRefresh)
	}
	if err := h.settings.Update(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	if h.logs != nil {
		if lv, ok := in[settings.KeyLogLevel]; ok {
			h.logs.SetLevel(lv)
		}
		if _, ok := in[settings.KeyLogRetentionDays]; ok && h.settings != nil {
			days := h.settings.Int(settings.KeyLogRetentionDays)
			h.logs.SetRetentionDays(days)
			if _, err := h.logs.CleanupOldLogs(days); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	if _, ok := in[settings.KeyAuthActiveRefresh]; ok && h.authSched != nil && h.settings != nil {
		h.authSched.SetActiveRefreshEnabled(
			h.settings.Bool(settings.KeyAuthActiveRefresh),
			previousActiveRefresh,
		)
	}
	if _, ok := in[settings.KeyWebDAVCacheEnabled]; ok && h.cache != nil {
		cache.InvalidateAllWebDAVCaches(h.cache)
	}
	if h.onSettingsUpdated != nil {
		h.onSettingsUpdated(in)
	}
	if fnosSettingsTouched(in) && h.fnosProxy != nil {
		if err := h.fnosProxy.Sync(r.Context()); err != nil {
			writeErr(w, err)
			return
		}
	}
	h.applyTaskRuntimeFromSettings(r.Context(), in)
	writeOK(w, h.settings.Snapshot())
}
