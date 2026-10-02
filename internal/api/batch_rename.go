package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
	"litepan/internal/renamerule"
)

// 批量重命名（对齐老版 internal/controllers/batch_rename.go）。
// 老版按登录用户隔离历史与常用组合，现版 API 只有管理员会话、没有多用户概念，
// 因此统一写入 userID=0；表结构保留 user_id 列以便后续扩展。

const (
	// batchRenameMaxItems 单次批量重命名条目上限。
	batchRenameMaxItems = 2000
	// batchRenameMaxRules 单次批量重命名规则条数上限。
	batchRenameMaxRules = 20
	// batchRenameDefaultLabel 历史记录缺省名称。
	batchRenameDefaultLabel = "批量重命名"
	// batchRenameTimeLayout 历史记录时间输出格式，与项目其它接口一致。
	batchRenameTimeLayout = "2006-01-02 15:04:05"
)

type batchRenameItem struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
}

// batchRenameApplyItem 应用请求的条目：NewName 是预览阶段算好的目标名。
type batchRenameApplyItem struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	NewName  string `json:"new_name"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
}

type batchRenamePreviewReq struct {
	AccountID     int64             `json:"account_id"`
	ParentID      string            `json:"parent_id"`
	FolderName    string            `json:"folder_name"`
	KeepExt       *bool             `json:"keep_ext"`
	Rules         []renamerule.Rule `json:"rules"`
	Items         []batchRenameItem `json:"items"`
	ExistingNames []string          `json:"existing_names"`
}

type batchRenameApplyReq struct {
	AccountID int64                  `json:"account_id"`
	ParentID  string                 `json:"parent_id"`
	Label     string                 `json:"label"`
	KeepExt   *bool                  `json:"keep_ext"`
	Rules     []renamerule.Rule      `json:"rules"`
	Items     []batchRenameApplyItem `json:"items"`
}

type batchRenameRollbackReq struct {
	AccountID int64 `json:"account_id"`
	HistoryID int64 `json:"history_id"`
}

type batchRenamePresetSaveReq struct {
	Name    string            `json:"name"`
	KeepExt *bool             `json:"keep_ext"`
	Rules   []renamerule.Rule `json:"rules"`
}

type batchRenamePresetDeleteReq struct {
	ID int64 `json:"id"`
}

// batchRenameKeepExt 未显式传入时默认保留扩展名，与老版 GORM 默认值一致。
func batchRenameKeepExt(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

// batchRenameTargets 把请求条目转换为规则引擎目标。
func batchRenameTargets(items []batchRenameItem) []renamerule.Target {
	targets := make([]renamerule.Target, 0, len(items))
	for _, item := range items {
		targets = append(targets, renamerule.Target{
			ID:       item.FileID,
			Name:     item.Name,
			Type:     item.Type,
			ParentID: item.ParentID,
		})
	}
	return targets
}

// batchRenameExistingNames 把同目录已有文件名包装成校验函数所需的形状。
func batchRenameExistingNames(existingNames []string, parentID string) map[string][]string {
	if len(existingNames) == 0 {
		return nil
	}
	return map[string][]string{parentID: existingNames}
}

// batchRenameNormalizeParentID 空 parent 统一按根目录处理，与老版一致。
func batchRenameNormalizeParentID(parentID string) string {
	if parentID == "" {
		return "0"
	}
	return parentID
}

// validateBatchRenameRules 规则数量与类型校验。
func validateBatchRenameRules(rules []renamerule.Rule) error {
	if len(rules) == 0 {
		return domain.Errorf(domain.CodeValidation, "rules 不能为空")
	}
	if len(rules) > batchRenameMaxRules {
		return domain.Errorf(domain.CodeValidation, "单次最多支持 %d 条规则", batchRenameMaxRules)
	}
	for i, rule := range rules {
		if !renamerule.IsValidType(rule.Type) {
			return domain.Errorf(domain.CodeValidation, "第 %d 条规则类型无效", i+1)
		}
	}
	return nil
}

// validateBatchRenameItems 条目数量与必填字段校验。
func validateBatchRenameItems(items []batchRenameItem) error {
	if len(items) == 0 {
		return domain.Errorf(domain.CodeValidation, "items 不能为空")
	}
	if len(items) > batchRenameMaxItems {
		return domain.Errorf(domain.CodeValidation, "单次最多支持 %d 个项目", batchRenameMaxItems)
	}
	for _, item := range items {
		if strings.TrimSpace(item.FileID) == "" {
			return domain.Errorf(domain.CodeValidation, "file_id 不能为空")
		}
		if strings.TrimSpace(item.Name) == "" {
			return domain.Errorf(domain.CodeValidation, "name 不能为空")
		}
	}
	return nil
}

// validateBatchRenameName 校验单个新文件名，禁止路径分隔符与保留名。
func validateBatchRenameName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return domain.Errorf(domain.CodeValidation, "新文件名不能为空")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return domain.Errorf(domain.CodeValidation, `文件名不能包含 / 或 \`)
	}
	if trimmed == "." || trimmed == ".." {
		return domain.Errorf(domain.CodeValidation, "文件名不能为 . 或 ..")
	}
	return nil
}

// batchRenameStringSlice 保证 errors 字段永远序列化为数组而非 null。
func batchRenameStringSlice(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// batchRenameRawJSON 输出可直接嵌入响应的 JSON，损坏时回退为空数组。
func batchRenameRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("[]")
	}
	return raw
}

// previewBatchRename 预览批量重命名结果，不触碰任何真实文件。
func (h *Handler) previewBatchRename(w http.ResponseWriter, r *http.Request) {
	var req batchRenamePreviewReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.AccountID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 account_id"))
		return
	}
	if err := validateBatchRenameRules(req.Rules); err != nil {
		writeErr(w, err)
		return
	}
	if err := validateBatchRenameItems(req.Items); err != nil {
		writeErr(w, err)
		return
	}

	parentID := batchRenameNormalizeParentID(req.ParentID)
	keepExt := batchRenameKeepExt(req.KeepExt)
	rows := renamerule.Preview(batchRenameTargets(req.Items), req.Rules, keepExt, req.FolderName)

	items := make([]renamerule.Target, 0, len(rows))
	changedTargets := make([]renamerule.Target, 0, len(rows))
	for _, row := range rows {
		target := row.Target
		if target.ParentID == "" {
			target.ParentID = parentID
		}
		items = append(items, target)
		if row.Changed {
			changedTargets = append(changedTargets, target)
		}
	}

	// 只有真正产生变更时才做冲突校验，否则"未命中任何规则"会误报重名。
	issues := renamerule.ValidateRules(req.Rules)
	if len(changedTargets) > 0 {
		issues = append(issues, renamerule.ValidateTargets(
			changedTargets, batchRenameExistingNames(req.ExistingNames, parentID))...)
	}

	writeJSON(w, http.StatusOK, Resp{
		Success: true,
		Message: "批量重命名预览生成成功",
		Data: map[string]any{
			"items":         items,
			"errors":        batchRenameStringSlice(issues),
			"changed_count": len(changedTargets),
			"total_count":   len(items),
		},
	})
}

// applyBatchRename 按预览结果逐条重命名，单条失败不中断整体流程，成功后写入历史。
func (h *Handler) applyBatchRename(w http.ResponseWriter, r *http.Request) {
	var req batchRenameApplyReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.AccountID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 account_id"))
		return
	}
	if err := validateBatchRenameRules(req.Rules); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "items 不能为空"))
		return
	}
	if len(req.Items) > batchRenameMaxItems {
		writeErr(w, domain.Errorf(domain.CodeValidation, "单次最多支持 %d 个项目", batchRenameMaxItems))
		return
	}
	for _, item := range req.Items {
		if strings.TrimSpace(item.FileID) == "" {
			writeErr(w, domain.Errorf(domain.CodeValidation, "file_id 不能为空"))
			return
		}
		if strings.TrimSpace(item.Name) == "" {
			writeErr(w, domain.Errorf(domain.CodeValidation, "name 不能为空"))
			return
		}
		if err := validateBatchRenameName(item.NewName); err != nil {
			writeErr(w, err)
			return
		}
	}

	success := make([]map[string]any, 0, len(req.Items))
	failed := make([]map[string]any, 0)
	// historyTargets 记录的是回滚目标：Name=改名后的名字、NewName=改名前的名字。
	historyTargets := make([]renamerule.Target, 0, len(req.Items))
	changedCount := 0

	for _, item := range req.Items {
		parentID := batchRenameNormalizeParentID(item.ParentID)
		if item.NewName == item.Name {
			success = append(success, map[string]any{
				"file_id":  item.FileID,
				"old_name": item.Name,
				"new_name": item.Name,
			})
			continue
		}
		if err := h.files.RenameFile(r.Context(), req.AccountID, item.FileID, item.NewName, parentID); err != nil {
			failed = append(failed, map[string]any{
				"file_id": item.FileID,
				"name":    item.Name,
				"reason":  err.Error(),
			})
			continue
		}
		success = append(success, map[string]any{
			"file_id":  item.FileID,
			"old_name": item.Name,
			"new_name": item.NewName,
		})
		historyTargets = append(historyTargets, renamerule.Target{
			ID:       item.FileID,
			Name:     item.NewName,
			NewName:  item.Name,
			ParentID: parentID,
		})
		changedCount++
	}

	if changedCount > 0 {
		h.saveBatchRenameHistory(r, req, historyTargets, len(req.Items), changedCount)
	}

	writeJSON(w, http.StatusOK, Resp{
		Success: true,
		Message: "重命名完成：成功 " + strconv.Itoa(len(success)) + " 个，失败 " + strconv.Itoa(len(failed)) + " 个",
		Data: map[string]any{
			"success":       success,
			"failed":        failed,
			"success_count": len(success),
			"fail_count":    len(failed),
		},
	})
}

// saveBatchRenameHistory 写入历史记录并累计常用组合使用次数。
// 历史写失败不影响重命名结果本身（文件已经改完了），只记录日志。
func (h *Handler) saveBatchRenameHistory(r *http.Request, req batchRenameApplyReq, targets []renamerule.Target, itemCount, changeCount int) {
	rulesJSON, err := json.Marshal(req.Rules)
	if err != nil {
		return
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = batchRenameDefaultLabel
	}
	history := &domain.RenameHistory{
		UserID:      0,
		Name:        label,
		Rules:       rulesJSON,
		KeepExt:     batchRenameKeepExt(req.KeepExt),
		Targets:     targetsJSON,
		ItemCount:   itemCount,
		ChangeCount: changeCount,
	}
	if err := h.renames.CreateHistory(r.Context(), history); err != nil && h.log != nil {
		h.log.Warn("写入批量重命名历史失败", "error", err)
	}
	if err := h.renames.IncrementPresetUse(r.Context(), 0, rulesJSON, batchRenameKeepExt(req.KeepExt)); err != nil && h.log != nil {
		h.log.Warn("累计常用组合使用次数失败", "error", err)
	}
}

// listBatchRenameHistory 历史记录列表。
func (h *Handler) listBatchRenameHistory(w http.ResponseWriter, r *http.Request) {
	items, err := h.renames.ListHistories(r.Context(), 0, 80)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		createdAt := ""
		if !item.CreatedAt.IsZero() {
			createdAt = item.CreatedAt.Format(batchRenameTimeLayout)
		}
		out = append(out, map[string]any{
			"id":           item.ID,
			"name":         item.Name,
			"rules":        batchRenameRawJSON(item.Rules),
			"keep_ext":     item.KeepExt,
			"item_count":   item.ItemCount,
			"change_count": item.ChangeCount,
			"created_at":   createdAt,
		})
	}
	writeOK(w, map[string]any{"items": out})
}

// rollbackBatchRename 按历史记录逐条改回原名。
// 这是破坏性最小的实现：只调用 RenameFile 改名字，不删除任何文件；
// 单条失败（文件已被移动/删除）视为已失效并跳过，不会中断其余回滚。
func (h *Handler) rollbackBatchRename(w http.ResponseWriter, r *http.Request) {
	var req batchRenameRollbackReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.AccountID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "非法 account_id"))
		return
	}
	if req.HistoryID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "history_id 不能为空"))
		return
	}

	history, err := h.renames.GetHistory(r.Context(), req.HistoryID, 0)
	if err != nil {
		writeErr(w, err)
		return
	}

	var targets []renamerule.Target
	if len(history.Targets) > 0 {
		if err := json.Unmarshal(history.Targets, &targets); err != nil {
			writeErr(w, domain.Errorf(domain.CodeValidation, "历史记录中的回滚目标已损坏"))
			return
		}
	}

	restored := make([]map[string]any, 0, len(targets))
	restoredIndexes := make(map[int]bool, len(targets))
	failCount := 0
	for i, target := range targets {
		if target.ID == "" || strings.TrimSpace(target.Name) == "" {
			failCount++
			continue
		}
		parentID := batchRenameNormalizeParentID(target.ParentID)
		if err := h.files.RenameFile(r.Context(), req.AccountID, target.ID, target.Name, parentID); err != nil {
			failCount++
			continue
		}
		restored = append(restored, map[string]any{
			"file_id":  target.ID,
			"old_name": target.NewName,
			"new_name": target.Name,
		})
		restoredIndexes[i] = true
	}

	// 已还原条目从历史中剔除，避免重复回滚；全部还原后删除整条记录。
	remaining := make([]renamerule.Target, 0, len(targets))
	for i, target := range targets {
		if !restoredIndexes[i] {
			remaining = append(remaining, target)
		}
	}
	switch {
	case len(restoredIndexes) == 0:
		// 一条都没还原成功，历史保持原样，方便用户排查后重试。
	case len(remaining) == 0:
		if err := h.renames.DeleteHistory(r.Context(), history.ID, 0); err != nil {
			writeErr(w, err)
			return
		}
	default:
		encoded, err := json.Marshal(remaining)
		if err != nil {
			writeErr(w, domain.Wrap(domain.CodeInternal, err))
			return
		}
		if err := h.renames.UpdateHistoryTargets(r.Context(), history.ID, encoded, len(remaining)); err != nil {
			writeErr(w, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, Resp{
		Success: true,
		Message: "回滚完成：成功 " + strconv.Itoa(len(restored)) + " 个，失败 " + strconv.Itoa(failCount) + " 个",
		Data: map[string]any{
			"success":    restored,
			"fail_count": failCount,
		},
	})
}

// listBatchRenamePresets 常用组合列表。
func (h *Handler) listBatchRenamePresets(w http.ResponseWriter, r *http.Request) {
	items, err := h.renames.ListPresets(r.Context(), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"id":        item.ID,
			"name":      item.Name,
			"rules":     batchRenameRawJSON(item.Rules),
			"keep_ext":  item.KeepExt,
			"use_count": item.UseCount,
		})
	}
	writeOK(w, map[string]any{"items": out})
}

// saveBatchRenamePreset 保存常用组合，同名覆盖。
func (h *Handler) saveBatchRenamePreset(w http.ResponseWriter, r *http.Request) {
	var req batchRenamePresetSaveReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "名称不能为空"))
		return
	}
	if len([]rune(name)) > 64 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "名称不能超过 64 个字符"))
		return
	}
	if err := validateBatchRenameRules(req.Rules); err != nil {
		writeErr(w, err)
		return
	}
	encoded, err := json.Marshal(req.Rules)
	if err != nil {
		writeErr(w, domain.Wrap(domain.CodeInternal, err))
		return
	}
	preset := &domain.RenamePreset{
		UserID:  0,
		Name:    name,
		Rules:   encoded,
		KeepExt: batchRenameKeepExt(req.KeepExt),
	}
	if err := h.renames.CreatePreset(r.Context(), preset); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Resp{
		Success: true,
		Message: "常用组合已保存",
		Data:    map[string]any{"id": preset.ID},
	})
}

// deleteBatchRenamePreset 删除常用组合。
func (h *Handler) deleteBatchRenamePreset(w http.ResponseWriter, r *http.Request) {
	var req batchRenamePresetDeleteReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.ID <= 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "id 不能为空"))
		return
	}
	if err := h.renames.DeletePreset(r.Context(), req.ID, 0); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Resp{Success: true, Message: "常用组合已删除"})
}

// 保留 time 引用：历史时间格式化依赖 time.Time，此处显式断言便于后续扩展。
var _ = time.Time{}
