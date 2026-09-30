package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/cas"
	"litepan/internal/domain"
	"litepan/internal/playback"
)

// ---------------------------------------------------------------------------
// CAS 秒传管理 API（清单记录 CRUD + 秒传恢复 + 配置）
// ---------------------------------------------------------------------------

// casListRecords 分页查询 CAS 清单记录
func (h *Handler) casListRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	rows, total, err := cas.ListRecords(page, pageSize, q.Get("status"), q.Get("keyword"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": rows, "total": total})
}

// casGetRecord 取单条记录
func (h *Handler) casGetRecord(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法记录 id"))
		return
	}
	rec, err := cas.GetRecordByID(uint(id))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rec)
}

// casDeleteRecord 删除记录
func (h *Handler) casDeleteRecord(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法记录 id"))
		return
	}
	if err := cas.DeleteRecord(uint(id)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{})
}

// casExportRecord 导出 .cas 文本
func (h *Handler) casExportRecord(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法记录 id"))
		return
	}
	fileName, content, err := cas.ExportRecordText(uint(id))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+".cas\"")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(content))
}

// casRestoreRecord 从记录秒传恢复
func (h *Handler) casRestoreRecord(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法记录 id"))
		return
	}
	var req struct {
		TargetFolderID string `json:"target_folder_id"`
	}
	_ = decodeJSON(r, &req)
	result, err := cas.RestoreFromRecord(r.Context(), uint(id), req.TargetFolderID)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "恢复失败：%v", err))
		return
	}
	writeOK(w, result)
}

// casRestoreFromText 从 .cas 文本恢复
func (h *Handler) casRestoreFromText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID      int64  `json:"account_id"`
		SourceType     string `json:"source_type"`
		TargetFolderID string `json:"target_folder_id"`
		CasText        string `json:"cas_text"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.CasText == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少 cas_text"))
		return
	}
	result, err := cas.RestoreFromCasText(r.Context(), req.AccountID, req.SourceType, req.TargetFolderID, req.CasText)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "恢复失败：%v", err))
		return
	}
	writeOK(w, result)
}

// casRunOnce 手动触发一轮 CAS 化（扫描已完成影视上传任务）
func (h *Handler) casRunOnce(w http.ResponseWriter, r *http.Request) {
	if h.casRunner == nil {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "CAS 运行器未就绪"))
		return
	}
	generated, deleted, skipped, failed, err := h.casRunner.RunOnce(r.Context())
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "CAS 化执行失败：%v", err))
		return
	}
	writeOK(w, map[string]any{
		"generated": generated, "deleted": deleted,
		"skipped": skipped, "failed": failed,
	})
}

// casGetConfig 读 CAS 配置
func (h *Handler) casGetConfig(w http.ResponseWriter, r *http.Request) {
	writeOK(w, cas.GetConfigForAPI())
}

// casSaveConfig 保存 CAS 配置
func (h *Handler) casSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg cas.CasConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeErr(w, err)
		return
	}
	cas.SaveConfig(cfg)
	writeOK(w, cas.GetConfigForAPI())
}

// parseCasIDFromRequest 灵活提取 CAS ID（兼容 /cas/play/{id}、/cas/play/*?cas_id=N、?id=N 等格式）
func parseCasIDFromRequest(r *http.Request) (uint, error) {
	idStr := strings.TrimSpace(chi.URLParam(r, "id"))
	if idStr == "" {
		idStr = strings.TrimSpace(r.URL.Query().Get("cas_id"))
	}
	if idStr == "" {
		idStr = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if idStr == "" {
		// 尝试从通配路径提取数字前缀，例如 /cas/play/123-film.mkv 或 /cas/play/123/film.mkv
		path := strings.Trim(chi.URLParam(r, "*"), "/")
		if parts := strings.Split(path, "/"); len(parts) > 0 {
			first := parts[0]
			if idx := strings.Index(first, "-"); idx > 0 {
				first = first[:idx]
			}
			if n, err := strconv.ParseUint(first, 10, 64); err == nil && n > 0 {
				return uint(n), nil
			}
		}
	}
	if idStr == "" {
		return 0, domain.Errorf(domain.CodeValidation, "缺少有效 cas_id")
	}
	n, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || n <= 0 {
		return 0, domain.Errorf(domain.CodeValidation, "非法 cas_id：%s", idStr)
	}
	return uint(n), nil
}

// casPlay CAS 播放直链/代理入口：源文件若被删则自动秒传恢复，并登记延时清理，支持 302 重定向或边转边播
func (h *Handler) casPlay(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeErr(w, domain.Errf(domain.CodeNotImplement))
		return
	}

	casID, err := parseCasIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}

	// 探测并恢复文件
	rec, playFileID, restored, err := cas.ProbeAndRestoreFile(r.Context(), casID)
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeDriverError, "CAS 播放恢复失败：%v", err))
		return
	}

	fileName := rec.FileName
	if custom := chi.URLParam(r, "filename"); custom != "" {
		if unescaped, err := url.PathUnescape(custom); err == nil && unescaped != "" {
			fileName = unescaped
		}
	}

	h.log.Info("CAS 播放交付", "cas_id", casID, "file", fileName, "fid", playFileID, "restored", restored)

	// 交付播放（自动支持 302 重定向、Range 分片代理）
	if err := h.playback.ServeHTTP(w, r, playback.Request{
		AccountID: rec.AccountID,
		FileID:    playFileID,
	}, playback.Intent{FileName: fileName}); err != nil {
		writeErr(w, err)
	}
}

// casPlayURL 获取 CAS 播放地址和恢复状态（后台管理或测试调用）
func (h *Handler) casPlayURL(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法记录 id"))
		return
	}

	rec, playFileID, restored, err := cas.ProbeAndRestoreFile(r.Context(), uint(id))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeDriverError, "CAS 播放恢复失败：%v", err))
		return
	}

	playURL := fmt.Sprintf("/cas/play/%d/%s", rec.ID, url.PathEscape(rec.FileName))
	writeOK(w, map[string]any{
		"cas_id":       rec.ID,
		"file_name":    rec.FileName,
		"play_file_id": playFileID,
		"restored":     restored,
		"play_url":     playURL,
	})
}
