package mediaupgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"litepan/internal/moviepilot"
	"litepan/internal/settings"
)

// ErrDisabled 洗版总开关关闭。
//
// 默认就是关的：这是一个会删用户文件的功能，不该因为升级了版本就自己开始删东西。
var ErrDisabled = errors.New("洗版功能未启用（mo_media_upgrade_enabled 默认关闭）")

// ErrNoLibraryRoot 没有可扫描的媒体库根目录。
var ErrNoLibraryRoot = errors.New("未配置媒体库根目录")

// Settings 是本包读取配置所需的最小面。
// *settings.Service 天然满足；接口化是为了让单测能注入假实现、不必拉起整个配置表。
type Settings interface {
	String(key string) string
	Int(key string) int
	Bool(key string) bool
}

// RuleSet 一次扫描实际生效的规则集合。
//
// 从 media_upgrade_rules 行或全局设置装配而来，两条路径产出同一个结构，
// 这样扫描与提交两个阶段看到的是同一份口径。
type RuleSet struct {
	ID                  uint
	Name                string
	Source              string
	LibraryRoot         string
	CandidateRoots      []string
	MinResolution       int
	MinChannels         int
	RequireSubtitle     bool
	MaxRecordsPerSeries int
	LoserAction         string
	MoveDir             string
	GroupPriority       []string
	WashRules           []moviepilot.WashRule
	// CategoryNames 适用分类目录名（T27 C-8）。空 = 不按分类筛选。
	//
	// 存的是**用户填的字**而不是已归一的小写：归一发生在匹配那一层
	// （CategoryScope 用 nameSet 按小写比）。这样列表页能原样显示
	// 用户当时填的"国产"而不是一个他没见过的形态。
	CategoryNames []string
	Enabled       bool

	// rawWashRules 用户填的 wash_rules 原文（规则行里的那一列）。
	//
	// 留着它只为让 ValidateForTrial 能区分「没填 wash_rules（回落默认，
	// 正常）」与「填了但解析不出来（回落默认，用户不知情）」。
	// 解析失败时 moviepilot.ParseWashRules 返回 nil，两种情况在
	// WashRules 上长得一模一样。
	rawWashRules string
}

// Effective 返回带默认值兜底的副本，供 UI 展示。
func (r *RuleSet) Effective() RuleSet {
	out := *r
	if out.Source == "" {
		out.Source = SourceLocal
	}
	if out.LoserAction == "" {
		out.LoserAction = LoserActionKeep
	}
	if len(out.WashRules) == 0 {
		out.WashRules = moviepilot.DefaultWashRules
	}
	return out
}

// CandidateRootsText 把候选目录列表拍回逗号分隔字符串，供入库。
func (r *RuleSet) CandidateRootsText() string { return strings.Join(r.CandidateRoots, ",") }

// Validate 校验规则；返回错误文案（可直接展示给用户）。
//
// 校验的是**会导致误删或空转**的配置，不是格式洁癖：
//   - loser_action=move 但没给 move_dir：每条都会执行失败。
//   - library_root 为空：扫不到任何东西，白跑一趟还可能让人以为「扫过了没发现」。
func (r *RuleSet) Validate() error {
	switch r.Source {
	case SourceLocal, SourceEmby, SourceJellyfin:
	case "":
		return errors.New("扫描源不能为空")
	default:
		return fmt.Errorf("不支持的扫描源 %q（可选 local、emby、jellyfin）", r.Source)
	}
	if strings.TrimSpace(r.LibraryRoot) == "" {
		return ErrNoLibraryRoot
	}
	switch r.LoserAction {
	case LoserActionKeep, LoserActionDelete:
	case LoserActionMove:
		if strings.TrimSpace(r.MoveDir) == "" {
			return errors.New("败方动作设为 move 时必须填写移动目标目录")
		}
	case "":
		return errors.New("败方动作不能为空")
	default:
		return fmt.Errorf("不支持的败方动作 %q（可选 keep、delete、move）", r.LoserAction)
	}
	if r.MaxRecordsPerSeries < 0 {
		return errors.New("每部剧记录上限不能为负数")
	}
	return nil
}

// ValidateForTrial 规则试算用的校验：只查**会影响判定**的部分。
//
// 刻意不查 library_root / move_dir：试算只比较两个文件名，
// 而前端在「还没选目录、先看看规则会怎么判」这一步就该能试算 —
// 要求先填出一个存在的媒体库根目录，纯属把试算变成一张前置表单。
//
// 与 Validate 的差别必须保持这么窄：这里放宽的每一条都是
// 「不影响 relation/trace/reasons」的，否则试算就会用一份
// 真实扫描不会接受的规则给出结论。
func (r *RuleSet) ValidateForTrial() error {
	switch r.LoserAction {
	case LoserActionKeep, LoserActionDelete, LoserActionMove:
	case "":
		return errors.New("败方动作不能为空")
	default:
		return fmt.Errorf("不支持的败方动作 %q（可选 keep、delete、move）", r.LoserAction)
	}
	if r.MinResolution < 0 {
		return errors.New("最低分辨率不能为负数")
	}
	if r.MinChannels < 0 {
		return errors.New("最低声道数不能为负数")
	}
	// wash_rules 写坏了会静默回落默认规则：用户以为在试「自定义维度」，
	// 看到的却是默认维度的结果，而界面上完全看不出发生过回落。
	//
	// ⚠️ 这里必须自己解 rawWashRules，不能看 r.WashRules：
	// RuleFromRow 会先把解析不出规则的 WashRules 换成 DefaultWashRules，
	// 等校验跑到这一行时 WashRules 已经是非空的默认值 —— 判据永远为假，
	// 这条校验会变成一个永远为真的摆设（写它的动机就是它该拦住的东西）。
	raw := strings.TrimSpace(r.rawWashRules)
	if raw != "" && raw != "null" {
		var parsed []moviepilot.WashRule
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return errors.New("洗版维度规则解析失败，请检查它是不是合法 JSON")
		}
		if len(parsed) == 0 {
			return errors.New("洗版维度规则是空的数组，等同于没填")
		}
		// 合法 JSON 但字段全不认识时 ParseWashRules 也会回落默认；
		// 这种情况同样属于「用户以为配了、实际没生效」。
		for _, wr := range parsed {
			if moviepilot.WashRuleFields()[wr.Field] {
				return nil
			}
		}
		return errors.New("洗版维度规则里没有可识别的维度名")
	}
	return nil
}

// LoadRuleSet 装配一次扫描要用的规则集。
//
// ruleID > 0 时取库里的规则行（UI 建的具名规则）；
// ruleID == 0 时回落到全局设置（mo_media_upgrade_* 系列），
// 并且总开关关着就直接返回 ErrDisabled —— 调用方不该在这里默默跳过，
// 「因为总开关关了所以什么都没发生」必须让用户看见。
func LoadRuleSet(db *gorm.DB, svc Settings, ruleID uint) (*RuleSet, error) {
	if ruleID > 0 {
		if db == nil {
			return nil, errors.New("规则 ID 需要数据库句柄")
		}
		var row Rule
		if err := db.Where("id = ?", ruleID).First(&row).Error; err != nil {
			return nil, fmt.Errorf("读取洗版规则 %d 失败: %w", ruleID, err)
		}
		rs := ruleFromRow(&row)
		if err := rs.Validate(); err != nil {
			return nil, err
		}
		return &rs, nil
	}
	if svc == nil || !svc.Bool(settings.KeyMOMediaUpgradeEnabled) {
		return nil, ErrDisabled
	}
	rs := RuleSet{
		ID:                  0,
		Name:                "默认规则",
		Enabled:             true,
		Source:              svc.String(settings.KeyMOMediaUpgradeSource),
		LibraryRoot:         strings.TrimSpace(svc.String(settings.KeyMOMediaUpgradeLibraryRoot)),
		CandidateRoots:      SplitRoots(svc.String(settings.KeyMOMediaUpgradeCandidateRoots)),
		MinResolution:       svc.Int(settings.KeyMOMediaUpgradeMinResolution),
		MinChannels:         svc.Int(settings.KeyMOMediaUpgradeMinChannels),
		RequireSubtitle:     svc.Bool(settings.KeyMOMediaUpgradeRequireSubtitle),
		MaxRecordsPerSeries: svc.Int(settings.KeyMOMediaUpgradeMaxRecordsPerSeries),
		LoserAction:         svc.String(settings.KeyMOMediaUpgradeLoserAction),
		MoveDir:             strings.TrimSpace(svc.String(settings.KeyMOMediaUpgradeMoveDir)),
		GroupPriority:       SplitRoots(svc.String(settings.KeyMOMediaUpgradeGroupPriority)),
	}
	rs = rs.Effective()
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	return &rs, nil
}

// ruleFromRow 把库里的规则行转成 RuleSet。
func ruleFromRow(row *Rule) RuleSet {
	return RuleSet{
		ID:                  row.ID,
		Name:                row.Name,
		Enabled:             row.Enabled,
		Source:              row.Source,
		LibraryRoot:         strings.TrimSpace(row.LibraryRoot),
		CandidateRoots:      SplitRoots(row.CandidateRoots),
		MinResolution:       row.MinResolution,
		MinChannels:         row.MinChannels,
		RequireSubtitle:     row.RequireSubtitle,
		MaxRecordsPerSeries: row.MaxRecordsPerSeries,
		LoserAction:         row.LoserAction,
		MoveDir:             strings.TrimSpace(row.MoveDir),
		GroupPriority:       SplitRoots(row.GroupPriority),
		WashRules:           moviepilot.ParseWashRules(row.WashRules),
		CategoryNames:       ParseRuleCategoryScope(row.CategoryScope),
		rawWashRules:        row.WashRules,
	}
}

// RuleFromRow 导出给 API 层复用（把用户提交的一行规则转成生效规则集）。
func RuleFromRow(row *Rule) RuleSet {
	rs := ruleFromRow(row)
	if len(rs.WashRules) == 0 {
		rs.WashRules = moviepilot.DefaultWashRules
	}
	return rs
}

// rootListSeps 目录/列表配置的切分符：中英文逗号、顿号、分号、空白与换行。
const rootListSeps = "，,、;；\n\r\t "

// isRootListSep 判断一个字符是否属于列表分隔符。
func isRootListSep(r rune) bool { return strings.ContainsRune(rootListSeps, r) }

// SplitRoots 把逗号/换行分隔的列表切成去重后的有序切片。
//
// 去重 + 排序是刻意的：扫描与提交两个阶段各自调一次这个函数，
// 如果同一批目录两次进来顺序不同，「候选目录集合」的比较就会误判成发生了变化。
func SplitRoots(raw string) []string {
	items := strings.FieldsFunc(raw, isRootListSep)
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	sort.Strings(out)
	return out
}

// JoinRoots 把目录列表拍回逗号分隔串。
func JoinRoots(list []string) string { return strings.Join(list, ",") }
