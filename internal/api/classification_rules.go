package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"litepan/internal/classifyorganize"
	"litepan/internal/domain"

	"github.com/go-chi/chi/v5"
)

// parseClassifyRuleID 从路径参数解析规则 ID。
func parseClassifyRuleID(raw string) (uint, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, domain.Errorf(domain.CodeValidation, "缺少规则 ID")
	}
	n, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || n == 0 {
		return 0, domain.Errorf(domain.CodeValidation, "非法规则 ID：%s", idStr)
	}
	return uint(n), nil
}

// classifyRuleDTO 是规则的对外表示：条件以结构化列表暴露，避免前端解析 JSON 字符串。
type classifyRuleDTO struct {
	ID         uint                             `json:"id"`
	MediaType  string                           `json:"media_type"`
	TargetPath string                           `json:"target_path"`
	Enabled    bool                             `json:"enabled"`
	Remark     string                           `json:"remark"`
	Conditions []classifyorganize.RuleCondition `json:"conditions"`
	Position   int                              `json:"position"`
	CreatedAt  string                           `json:"created_at"`
	UpdatedAt  string                           `json:"updated_at"`
}

func toClassifyRuleDTO(rule classifyorganize.ClassifyRule) classifyRuleDTO {
	conditions := rule.ConditionList
	if conditions == nil {
		conditions = []classifyorganize.RuleCondition{}
	}
	return classifyRuleDTO{
		ID:         rule.ID,
		MediaType:  rule.MediaType,
		TargetPath: rule.TargetPath,
		Enabled:    rule.Enabled,
		Remark:     rule.Remark,
		Conditions: conditions,
		Position:   rule.Position,
		CreatedAt:  rule.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  rule.UpdatedAt.Format(time.RFC3339),
	}
}

func toClassifyRuleDTOs(rules []classifyorganize.ClassifyRule) []classifyRuleDTO {
	out := make([]classifyRuleDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toClassifyRuleDTO(rule))
	}
	return out
}

type classifyRuleInput struct {
	MediaType  string                           `json:"media_type"`
	TargetPath string                           `json:"target_path"`
	Enabled    *bool                            `json:"enabled"`
	Remark     string                           `json:"remark"`
	Conditions []classifyorganize.RuleCondition `json:"conditions"`
}

func (in classifyRuleInput) toModel() classifyorganize.ClassifyRule {
	rule := classifyorganize.ClassifyRule{
		MediaType:     in.MediaType,
		TargetPath:    in.TargetPath,
		Remark:        in.Remark,
		ConditionList: in.Conditions,
		Enabled:       true,
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
	return rule
}

func (h *Handler) requireClassifyOrganize(w http.ResponseWriter) bool {
	if h.classifyOrganize == nil {
		writeErr(w, domain.Errorf(domain.CodeInternal, "目录整理服务未就绪"))
		return false
	}
	return true
}

func (h *Handler) listClassificationRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	rules, err := h.classifyOrganize.ListRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"rules": toClassifyRuleDTOs(rules)})
}

func (h *Handler) createClassificationRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	var in classifyRuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	rule, err := h.classifyOrganize.CreateRule(r.Context(), in.toModel())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toClassifyRuleDTO(rule))
}

func (h *Handler) updateClassificationRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	id, err := parseClassifyRuleID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var in classifyRuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	rule, err := h.classifyOrganize.UpdateRule(r.Context(), id, in.toModel())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toClassifyRuleDTO(rule))
}

func (h *Handler) deleteClassificationRule(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	id, err := parseClassifyRuleID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.classifyOrganize.DeleteRule(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

type reorderClassificationRulesDTO struct {
	IDs []uint `json:"ids"`
}

func (h *Handler) reorderClassificationRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	var in reorderClassificationRulesDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if len(in.IDs) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "ids 不能为空"))
		return
	}
	rules, err := h.classifyOrganize.ReorderRules(r.Context(), in.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"rules": toClassifyRuleDTOs(rules)})
}

type importClassificationRulesDTO struct {
	YAML    string `json:"yaml"`
	Mode    string `json:"mode"`
	Replace *bool  `json:"replace"`
}

func (h *Handler) importClassificationRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	var in importClassificationRulesDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(in.YAML) == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "yaml 内容不能为空"))
		return
	}
	// replace 默认 true：导入一份完整规则文件时通常是整体替换。
	replace := true
	if in.Replace != nil {
		replace = *in.Replace
	}
	switch strings.ToLower(strings.TrimSpace(in.Mode)) {
	case "":
	case "replace":
		replace = true
	case "append":
		replace = false
	default:
		writeErr(w, domain.Errorf(domain.CodeValidation, "mode 只支持 replace 或 append"))
		return
	}
	rules, warnings, err := h.classifyOrganize.ImportRulesFromYAML(r.Context(), in.YAML, replace)
	if err != nil {
		writeErr(w, err)
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeOK(w, map[string]any{
		"rules":    toClassifyRuleDTOs(rules),
		"warnings": warnings,
		"imported": len(rules),
	})
}

func (h *Handler) exportClassificationRules(w http.ResponseWriter, r *http.Request) {
	if !h.requireClassifyOrganize(w) {
		return
	}
	text, err := h.classifyOrganize.ExportRulesToYAML(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"yaml": text})
}
