package api

import (
	"net/http"

	"litepan/internal/domain"
	"litepan/internal/inspection"
)

// inspectionCheckers 列出已装配的巡检项。
//
// 页面靠它决定渲染哪几块，而不是把六个名字写死在前端：后端少装配一个，
// 前端就少显示一块，而不是显示一块点进去发现永远空。
func (h *Handler) inspectionCheckers(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.inspection != nil) {
		return
	}
	writeOK(w, map[string]any{"checkers": h.inspection.Checkers()})
}

// inspectionScan 跑一轮巡检，只读。
//
// 它返回的 snapshot_id 是后续 preview / repair 的唯一凭据：
// 修复只认「本次扫描报出来的那些条」，不接受客户端自带的路径或文件名。
// 这不是洁癖 —— 用户点的是预览里的某一行，服务端照着快照里那份记录执行，
// 才能保证「点的那行」和「删的那个目录」是同一个。
func (h *Handler) inspectionScan(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.inspection != nil) {
		return
	}
	report, err := h.inspection.ScanAll(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, report)
}

// inspectionPreview 按快照重新取一遍预览行。
//
// 预览是独立端点而不是直接用扫描结果里的那份：扫描报告可能已经过期
// （用户开着页面放了一下午），而修复前必须看到「现在」的这批行。
// 拿不到快照时如实报「已过期，请重新扫描」，不返回空列表假装没事。
func (h *Handler) inspectionPreview(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.inspection != nil) {
		return
	}
	snapshotID := r.URL.Query().Get("snapshot_id")
	lines, ok := h.inspection.GetPreview(r.Context(), snapshotID)
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "该巡检快照已过期或已被执行，请重新扫描"))
		return
	}
	writeOK(w, map[string]any{"snapshot_id": snapshotID, "preview": lines})
}

// inspectionRepairPreviewAck 是修复请求里必须带上的确认字段。
type inspectionRepairRequest struct {
	SnapshotID string   `json:"snapshot_id"`
	FindingIDs []string `json:"finding_ids"`
	// PreviewAck 必须为 true。它不是「前端记得弹过框」的意思，
	// 而是服务端对「用户看过这批修复动作」的确认：没有它，
	// 一个写错的客户端就能跳过预览直接删目录。
	PreviewAck bool `json:"preview_ack"`
}

func (h *Handler) inspectionRepair(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.inspection != nil) {
		return
	}
	var request inspectionRepairRequest
	if err := decodeJSON(r, &request); err != nil {
		writeErr(w, err)
		return
	}
	if !request.PreviewAck {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请先查看修复预览再执行"))
		return
	}
	if len(request.FindingIDs) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "没有选中任何待修复项"))
		return
	}
	results, err := h.inspection.Repair(r.Context(), request.SnapshotID, request.FindingIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"results": results})
}

// 编译期钉住巡检的两个破坏性动作，让「哪些修复会真删东西」在改动时
// 必须经过一次显式修改 —— 而不是某天顺手把 delete_dir 写成可逆的。
var (
	_ = inspection.IsDestructive(inspection.RepairDeleteDir)
	_ = inspection.IsDestructive(inspection.RepairDropIndexRow)
)
