package api

// 洗版（media upgrade）管理接口。
//
// 安全姿态（与 internal/mediaupgrade 包的注释同源，这里只做一次提醒）：
//   - 触发扫描**不会**碰任何文件，只产出待执行记录；
//   - 真正动文件的是 execute，它内部先 CAS 抢提交锁、再复核快照，不一致就整条作废；
//   - 总开关 mo_media_upgrade_enabled 默认关闭，关着时连扫描都拒绝（返回明确的提示而不是空结果）。
//
// 路由一律挂在 /api/admin/media-upgrade 下（管理员鉴权层内）。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/mediaupgrade"
)

// mediaUpgradeRuleDTO 规则行的请求/响应体。
//
// 用独立的 DTO 而不是直接暴露 Rule 模型：decodeJSON 开了 DisallowUnknownFields，
// 直接绑模型的话前端多传一个字段就会 400，很难排查。
type mediaUpgradeRuleDTO struct {
	ID                  uint   `json:"id"`
	Name                string `json:"name"`
	Source              string `json:"source"`
	LibraryRoot         string `json:"library_root"`
	CandidateRoots      string `json:"candidate_roots"`
	MinResolution       int    `json:"min_resolution"`
	MinChannels         int    `json:"min_channels"`
	RequireSubtitle     bool   `json:"require_subtitle"`
	MaxRecordsPerSeries int    `json:"max_records_per_series"`
	LoserAction         string `json:"loser_action"`
	MoveDir             string `json:"move_dir"`
	GroupPriority       string `json:"group_priority"`
	WashRules           string `json:"wash_rules"`
	// Categories 适用分类目录名（T27 C-8）。空数组 = 不按分类筛选。
	//
	// 这里传**数组**而库里那一列存的是 JSON 字符串：前端要的是
	// 一个能直接 v-model 的数组，让它在 UI 里序列化字符串只会
	// 让每个调用点各写一遍 JSON.parse。
	Categories []string `json:"categories"`
	Enabled    bool     `json:"enabled"`
	Builtin    bool     `json:"builtin"`
}

func toMediaUpgradeRuleDTO(row *mediaupgrade.Rule) mediaUpgradeRuleDTO {
	if row == nil {
		return mediaUpgradeRuleDTO{}
	}
	return mediaUpgradeRuleDTO{
		ID:                  row.ID,
		Name:                row.Name,
		Source:              row.Source,
		LibraryRoot:         row.LibraryRoot,
		CandidateRoots:      row.CandidateRoots,
		MinResolution:       row.MinResolution,
		MinChannels:         row.MinChannels,
		RequireSubtitle:     row.RequireSubtitle,
		MaxRecordsPerSeries: row.MaxRecordsPerSeries,
		LoserAction:         row.LoserAction,
		MoveDir:             row.MoveDir,
		GroupPriority:       row.GroupPriority,
		WashRules:           row.WashRules,
		Categories:          mediaupgrade.ParseRuleCategoryScope(row.CategoryScope),
		Enabled:             row.Enabled,
		Builtin:             row.Builtin,
	}
}

func (d mediaUpgradeRuleDTO) toModel() *mediaupgrade.Rule {
	return &mediaupgrade.Rule{
		ID:                  d.ID,
		Name:                d.Name,
		Source:              d.Source,
		LibraryRoot:         strings.TrimSpace(d.LibraryRoot),
		CandidateRoots:      strings.TrimSpace(d.CandidateRoots),
		MinResolution:       d.MinResolution,
		MinChannels:         d.MinChannels,
		RequireSubtitle:     d.RequireSubtitle,
		MaxRecordsPerSeries: d.MaxRecordsPerSeries,
		LoserAction:         strings.TrimSpace(d.LoserAction),
		MoveDir:             strings.TrimSpace(d.MoveDir),
		GroupPriority:       strings.TrimSpace(d.GroupPriority),
		WashRules:           strings.TrimSpace(d.WashRules),
		CategoryScope:       mediaupgrade.EncodeRuleCategoryScope(d.Categories),
		Enabled:             d.Enabled,
	}
}

// mediaUpgradeReady 洗版服务未装配时给出明确响应。
func (h *Handler) mediaUpgradeReady(w http.ResponseWriter) bool {
	return ensureServiceReady(w, h.mediaUpgrade != nil)
}

// writeMediaUpgradeErr 把包内错误翻译成统一响应。
//
// writeErr 对非 AppError 一律吞成「服务内部错误」，但洗版的拒绝原因
// （功能未启用、规则没配媒体库目录、败方动作非法）恰恰是用户要看的，
// 所以在这里显式包成 CodeValidation/CodeNotImplement。
func writeMediaUpgradeErr(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if _, ok := domain.AsAppError(err); ok {
		writeErr(w, err)
		return
	}
	if isMediaUpgradeDisabled(err) {
		writeErr(w, domain.Errorf(domain.CodeNotImplement, "%s", err.Error()))
		return
	}
	switch {
	case strings.Contains(err.Error(), "未配置媒体库根目录"):
		writeErr(w, domain.Errorf(domain.CodeValidation, "%s", err.Error()))
	default:
		// 校验类错误原样回显，底层错误仍走统一兜底。
		if ae, ok := mediaUpgradeValidationErr(err); ok {
			writeErr(w, ae)
			return
		}
		writeErr(w, err)
	}
}

func isMediaUpgradeDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "洗版功能未启用")
}

// mediaUpgradeValidationErr 识别规则校验错误。
//
// 包里用的是 errors.New / fmt.Errorf（要跨包复用、不想把 API 层的
// domain 依赖带进业务包），所以这里按文案前缀识别。
// 宁可漏判（落回 500）也不误判：把系统错误包装成 400 只会掩盖真问题。
func mediaUpgradeValidationErr(err error) (*domain.AppError, bool) {
	if err == nil {
		return nil, false
	}
	msg := err.Error()
	for _, prefix := range []string{
		"不支持的扫描源",
		"不支持的败方动作",
		"扫描源不能为空",
		"败方动作不能为空",
		"败方动作设为 move 时必须填写移动目标目录",
		"每部剧记录上限不能为负数",
	} {
		if strings.HasPrefix(msg, prefix) {
			return domain.Errorf(domain.CodeValidation, "%s", msg), true
		}
	}
	return nil, false
}

// ---- 扫描 ----

// GET /api/admin/media-upgrade/scans
func (h *Handler) listMediaUpgradeScans(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	limit := queryIntOr(r, "limit", 50)
	scans, err := h.mediaUpgrade.Scanner().ListScans(r.Context(), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]mediaupgrade.Scan, 0, len(scans))
	out = append(out, scans...)
	writeOK(w, map[string]any{"items": out})
}

// GET /api/admin/media-upgrade/scans/{id}
func (h *Handler) getMediaUpgradeScan(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	id, err := mediaUpgradePathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	scan, err := h.mediaUpgrade.Scanner().GetScan(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": scan})
}

// POST /api/admin/media-upgrade/scans  触发扫描（只判定，不删文件）
func (h *Handler) createMediaUpgradeScan(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	var in struct {
		RuleID uint `json:"rule_id"`
	}
	// 请求体允许为空：{ } 与完全无 body 都表示「用全局规则扫描」。
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	scan, err := h.mediaUpgrade.Scan(r.Context(), in.RuleID)
	if err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": scan})
}

// POST /api/admin/media-upgrade/scans/{id}/execute  提交（复核快照后才动文件）
func (h *Handler) executeMediaUpgradeScan(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	id, err := mediaUpgradePathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := h.mediaUpgrade.Execute(r.Context(), id)
	if err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}
	writeOK(w, res)
}

// GET /api/admin/media-upgrade/records
func (h *Handler) listMediaUpgradeRecords(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	scanID := uint(queryIntOr(r, "scan_id", 0))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := queryIntOr(r, "limit", 200)
	records, err := h.mediaUpgrade.Scanner().ListRecords(r.Context(), scanID, status, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": records})
}

// GET /api/admin/media-upgrade/records/{id}
func (h *Handler) getMediaUpgradeRecord(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	id, err := mediaUpgradePathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	rec, err := h.mediaUpgrade.GetRecord(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": rec})
}

// mediaUpgradeTrialDTO 规则试算请求体。
type mediaUpgradeTrialDTO struct {
	Rule   *mediaUpgradeRuleDTO `json:"rule"`
	RuleID uint                 `json:"rule_id"`
	// NewSize / OldSize 的「是否参与」用独立布尔位表达，不能拿 0 当缺省：
	// 体积为 0 的文件是合法输入，而「没填体积」意味着体积这一步不参与比较。
	NewName    string `json:"new_name"`
	NewSize    int64  `json:"new_size"`
	HasNewSize bool   `json:"has_new_size"`
	OldName    string `json:"old_name"`
	OldSize    int64  `json:"old_size"`
	HasOldSize bool   `json:"has_old_size"`
}

// POST /api/admin/media-upgrade/rule-trial  规则试算（纯函数，不落库）
//
// body: {rule:{…} | rule_id:N, new_name, new_size, has_new_size, old_name, old_size, has_old_size}
//
// 解决「用户配好规则不知道规则会怎么判」：填两个文件名就能看到逐维度得分
// 与结构化驳回理由，不用等真实扫描。
//
// ⚠️ 纯函数：不落库、不碰网盘、不发网络请求。除按 rule_id 取规则那一读之外
// 不访问数据库。判定走 mediaupgrade.TrialVerdict，而它内部复用 compareOne ——
// 试算与真实扫描必须是同一套判定，否则用户照着试算配规则、
// 真实扫描给出相反结论，就再也不会相信试算了。
func (h *Handler) mediaUpgradeRuleTrial(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	var in mediaUpgradeTrialDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(in.NewName) == "" || strings.TrimSpace(in.OldName) == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "新文件和现版的文件名都不能为空"))
		return
	}

	// 规则来源二选一：给了 rule 就用请求里的（前端能试算还没保存的草稿），
	// 否则按 rule_id 读库。
	// ⚠️ 刻意不回落全局设置：试算必须能试**任意一版**规则，
	// 一个随全局设置漂移的隐式输入会让「昨天试算能过、今天同一份规则不过」
	// 变成查不出来的问题。
	rs, err := h.mediaUpgradeTrialRule(r, in)
	if err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}

	res := mediaupgrade.TrialVerdict(rs, mediaupgrade.TrialInput{
		NewName:    in.NewName,
		NewSize:    in.NewSize,
		HasNewSize: in.HasNewSize,
		OldName:    in.OldName,
		OldSize:    in.OldSize,
		HasOldSize: in.HasOldSize,
	})
	writeOK(w, map[string]any{"item": res})
}

// mediaUpgradeTrialRule 解析试算要用的规则集。
func (h *Handler) mediaUpgradeTrialRule(r *http.Request, in mediaUpgradeTrialDTO) (*mediaupgrade.RuleSet, error) {
	if in.Rule != nil {
		rs := mediaupgrade.RuleFromRow(in.Rule.toModel())
		if err := rs.ValidateForTrial(); err != nil {
			return nil, domain.Errorf(domain.CodeValidation, "%s", err.Error())
		}
		return &rs, nil
	}
	if in.RuleID == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "必须提供 rule 或 rule_id 其中一个")
	}
	row, err := h.mediaUpgrade.GetRule(r.Context(), in.RuleID)
	if err != nil {
		return nil, err
	}
	rs := mediaupgrade.RuleFromRow(row)
	if err := rs.ValidateForTrial(); err != nil {
		return nil, domain.Errorf(domain.CodeValidation, "%s", err.Error())
	}
	return &rs, nil
}

// ---- 规则 ----

// GET /api/admin/media-upgrade/rules  返回全局设置视图 + 库内具名规则
func (h *Handler) listMediaUpgradeRules(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	global, err := h.mediaUpgrade.GlobalRule()
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := h.mediaUpgrade.ListRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]mediaUpgradeRuleDTO, 0, len(rows))
	for i := range rows {
		items = append(items, toMediaUpgradeRuleDTO(&rows[i]))
	}
	writeOK(w, map[string]any{"global": toMediaUpgradeRuleDTO(global), "items": items})
}

// POST /api/admin/media-upgrade/rules  新建
func (h *Handler) createMediaUpgradeRule(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	var in mediaUpgradeRuleDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.ID != 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "新建规则不应携带 id"))
		return
	}
	row, err := h.mediaUpgrade.SaveRule(r.Context(), in.toModel())
	if err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": toMediaUpgradeRuleDTO(row)})
}

// PUT /api/admin/media-upgrade/rules/{id}  更新
func (h *Handler) updateMediaUpgradeRule(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	id, err := mediaUpgradePathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	var in mediaUpgradeRuleDTO
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.ID != 0 && in.ID != id {
		writeErr(w, domain.Errorf(domain.CodeValidation, "路径 id 与请求体 id 不一致"))
		return
	}
	in.ID = id
	row, err := h.mediaUpgrade.SaveRule(r.Context(), in.toModel())
	if err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"item": toMediaUpgradeRuleDTO(row)})
}

// DELETE /api/admin/media-upgrade/rules/{id}
func (h *Handler) deleteMediaUpgradeRule(w http.ResponseWriter, r *http.Request) {
	if !h.mediaUpgradeReady(w) {
		return
	}
	id, err := mediaUpgradePathID(r, "id")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.mediaUpgrade.DeleteRule(r.Context(), id); err != nil {
		writeMediaUpgradeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

// mediaUpgradePathID 解析路径上的正整数 ID。
func mediaUpgradePathID(r *http.Request, name string) (uint, error) {
	raw := chi.URLParam(r, name)
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		return 0, domain.Errorf(domain.CodeValidation, "非法 %s：%s", name, raw)
	}
	return uint(v), nil
}

// queryIntOr 读取整数查询参数，非法或缺省时用 fallback。
func queryIntOr(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
