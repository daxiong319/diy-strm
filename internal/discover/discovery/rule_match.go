package discovery

import "strings"

// ---------------------------------------------------------------------------
// 视觉过滤规则（订阅规则的标题/正文词表过滤）
//
// ★ 这是唯一的一套匹配实现。生产转存路径 planAndTransferRuleCandidates
//   （subscriptions.go:591）与前端预览接口 EvaluateRuleMatch（本文件）都调用
//   这里的函数。任何一方单独改语义都会让 UI 对用户撒谎 —— 预览说"通过"、
//   转存却跳过，或反之。因此禁止在别处复制一份匹配逻辑。
//
// 三条词表的语义（与生产一致，顺序即优先级）：
//   message_keywords  任意命中即放行（any-of，白名单式闸门）；列表为空 = 不做此项过滤
//   must_contain      全部命中才放行（all-of，缺任一条即拦截）
//   must_not_contain  任意命中即拦截（any-of 排除）
//
// 匹配文本 = 标题 + "\n" + 备注，两边统一 ToLower（大小写不敏感，中文不受影响）。
// ---------------------------------------------------------------------------

// 生产路径实际写入 candidate 状态的中文原因串。预览接口必须逐字返回同样的字符串，
// 否则用户看到的拦截原因和候选明细里显示的对不上。
const (
	// RuleSkipReasonKeywords 未命中 message_keywords
	RuleSkipReasonKeywords = "未命中消息正文关键词"
	// RuleSkipReasonMustContainPrefix 未命中 must_contain 中某条，后接该词
	RuleSkipReasonMustContainPrefix = "标题正文未包含："
	// RuleSkipReasonMustNotContainPrefix 命中 must_not_contain 中某条，后接该词
	RuleSkipReasonMustNotContainPrefix = "标题正文命中排除词："
)

// RuleFilter 一次规则匹配所需的三个词表（已由 stringListFromAny 归一）
type RuleFilter struct {
	Keywords       []string `json:"message_keywords"`
	MustContain    []string `json:"must_contain"`
	MustNotContain []string `json:"must_not_contain"`
}

// RuleFilterFromMatch 从规则的 MatchData（解析后的 JSON 对象）取词表。
// 与生产路径 subscriptions.go:593-595 使用完全相同的取值方式。
func RuleFilterFromMatch(match map[string]any) RuleFilter {
	return RuleFilter{
		Keywords:       stringListFromAny(match["message_keywords"]),
		MustContain:    stringListFromAny(match["must_contain"]),
		MustNotContain: stringListFromAny(match["must_not_contain"]),
	}
}

// RuleFilterActive 三条词表是否至少有一条非空（全空 = 不做过滤，全部放行）
func (f RuleFilter) RuleFilterActive() bool {
	return len(f.Keywords) > 0 || len(f.MustContain) > 0 || len(f.MustNotContain) > 0
}

// RuleMatchText 构造规则匹配文本：标题 + 换行 + 备注，统一小写。
// 与生产路径 subscriptions.go:608 的 matchText 逐字一致。
func RuleMatchText(title, remark string) string {
	return strings.ToLower(title + "\n" + remark)
}

// RuleSkipReason 返回该候选是否被规则拦截：
// 放行时 blocked=false 且 reason 为空；被拦截时 blocked=true 且 reason 为生产路径
// 会写入候选状态的原文（含被命中的具体词）。
func (f RuleFilter) RuleSkipReason(title, remark string) (reason string, blocked bool) {
	text := RuleMatchText(title, remark)
	if len(f.Keywords) > 0 && !containsAny(text, f.Keywords) {
		return RuleSkipReasonKeywords, true
	}
	if missing := firstMissing(text, f.MustContain); missing != "" {
		return RuleSkipReasonMustContainPrefix + missing, true
	}
	if hit := firstHit(text, f.MustNotContain); hit != "" {
		return RuleSkipReasonMustNotContainPrefix + hit, true
	}
	return "", false
}

// RulePreviewInput 预览请求：一次提交一批待测文本
type RulePreviewInput struct {
	// Keywords / MustContain / MustNotContain 即订阅规则的三条词表
	Keywords       []string `json:"message_keywords"`
	MustContain    []string `json:"must_contain"`
	MustNotContain []string `json:"must_not_contain"`
	// Titles 待测条目。用户在 UI 里粘贴一批标题；Remark 可选（生产匹配文本含备注）
	Titles []RulePreviewTitle `json:"titles"`
}

// RulePreviewTitle 单条待测文本
type RulePreviewTitle struct {
	Title  string `json:"title"`
	Remark string `json:"remark"`
}

// RulePreviewResult 单条判定结果
type RulePreviewResult struct {
	Title string `json:"title"`
	// Remark 回显，便于前端按行对齐
	Remark string `json:"remark"`
	// Blocked true = 生产转存路径会在此条上跳过
	Blocked bool `json:"blocked"`
	// Reason 与生产 setSubscriptionItemState 写入的原因逐字一致；放行时为空串
	Reason string `json:"reason"`
	// MatchText 展示用的匹配文本（小写），让用户看清实际被比对的内容
	MatchText string `json:"match_text"`
}

// RulePreviewOutput 预览结果
type RulePreviewOutput struct {
	Items []RulePreviewResult `json:"items"`
	// Total / BlockedCount / PassedCount 汇总，前端直接展示
	Total        int `json:"total"`
	BlockedCount int `json:"blocked_count"`
	PassedCount  int `json:"passed_count"`
	// Active 三条词表是否非空；false 表示不做任何过滤，全部放行
	Active bool `json:"active"`
}

// EvaluateRuleMatch 规则预览：对一批标题逐个给出"放行 / 拦截 + 原因"。
//
// ★ 判定复用的是生产路径同一组函数（RuleFilter.RuleSkipReason ->
// containsAny / firstMissing / firstHit），因此预览结论与
// planAndTransferRuleCandidates 的实际行为不可能分叉。
func EvaluateRuleMatch(in *RulePreviewInput) *RulePreviewOutput {
	if in == nil {
		return &RulePreviewOutput{Items: []RulePreviewResult{}}
	}
	out := &RulePreviewOutput{Items: make([]RulePreviewResult, 0, len(in.Titles))}
	filter := RuleFilter{
		Keywords:       normalizeStringList(in.Keywords),
		MustContain:    normalizeStringList(in.MustContain),
		MustNotContain: normalizeStringList(in.MustNotContain),
	}
	out.Active = filter.RuleFilterActive()
	for _, t := range in.Titles {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			continue
		}
		reason, blocked := filter.RuleSkipReason(title, t.Remark)
		if blocked {
			out.BlockedCount++
		} else {
			out.PassedCount++
		}
		out.Items = append(out.Items, RulePreviewResult{
			Title: title, Remark: t.Remark, Blocked: blocked, Reason: reason,
			MatchText: RuleMatchText(title, t.Remark),
		})
	}
	out.Total = len(out.Items)
	return out
}
