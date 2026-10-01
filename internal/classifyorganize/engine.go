package classifyorganize

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 自定义规则求值引擎
//
// 语义对齐 C 佬二级分类规则文件（Symedia）：
//   1. 规则按 Position 升序评估，**首个命中胜出**（文件注释描述的优先级）。
//   2. 一条规则内：无 `?` 的硬条件必须全部满足（AND）；若存在 `?` 软条件，
//      则软条件之间是 OR —— 至少一个软条件满足即可，硬条件仍需全部满足。
//   3. 条件值语法：
//        "a,b"      或语义，任一命中
//        "-x"       否定，必须不含 x
//        "1550-1999" 纯数字范围（闭区间），用于 year
//        "+16"      强制包含，必须含 16（与普通值的“或”区分）
// ---------------------------------------------------------------------------

// ruleEvaluation 单条规则的求值结果，附带命中证据。
type ruleEvaluation struct {
	Matched     bool
	SoftMatched bool
	Evidence    []string
}

// matchRule 按上述语义判断一条规则是否命中。
func matchRule(rule ClassifyRule, mediaType string, values map[string][]string) ruleEvaluation {
	if !rule.Enabled {
		return ruleEvaluation{}
	}
	if rule.MediaType != "" && !strings.EqualFold(rule.MediaType, mediaType) {
		return ruleEvaluation{}
	}
	if len(rule.ConditionList) == 0 {
		// 无条件规则只在被评估到且前面无人命中时兜底：
		// 参考文件尾部存在被截断的无条件目录，导入后仍应可用。
		return ruleEvaluation{Matched: true, Evidence: []string{"无条件规则"}}
	}

	evidence := make([]string, 0, len(rule.ConditionList))
	softTotal := 0
	softHits := 0
	for _, condition := range rule.ConditionList {
		actual := values[condition.Key]
		ok, detail := matchCondition(condition, actual)
		if condition.Optional {
			softTotal++
			if ok {
				softHits++
				evidence = append(evidence, "? "+detail)
			}
			continue
		}
		if !ok {
			return ruleEvaluation{}
		}
		evidence = append(evidence, detail)
	}
	if softTotal > 0 && softHits == 0 {
		return ruleEvaluation{}
	}
	return ruleEvaluation{Matched: true, SoftMatched: softTotal > 0, Evidence: evidence}
}

// matchCondition 求值单条条件。
func matchCondition(condition RuleCondition, actual []string) (bool, string) {
	tokens := splitRuleValues(condition.Values)
	if len(tokens) == 0 {
		return false, ""
	}
	if condition.Key == "year" {
		return matchYearCondition(condition.Key, tokens, actual)
	}
	hasPositive := false
	positiveHit := false
	required := make([]string, 0, len(tokens))
	for _, token := range tokens {
		switch {
		case token == "":
			continue
		case strings.HasPrefix(token, "-") && len(token) > 1:
			target := strings.TrimSpace(token[1:])
			if target == "" {
				continue
			}
			if containsFold(actual, target) {
				return false, fmt.Sprintf("%s 不得包含 %s", condition.Key, target)
			}
		case strings.HasPrefix(token, "+") && len(token) > 1:
			target := strings.TrimSpace(token[1:])
			if target == "" {
				continue
			}
			// `+` 表示强制包含：必须出现，且不参与“或”。
			required = append(required, target)
			if !containsFold(actual, target) {
				return false, fmt.Sprintf("%s 必须包含 %s", condition.Key, target)
			}
		default:
			hasPositive = true
			if containsFold(actual, token) {
				positiveHit = true
			}
		}
	}
	if len(required) > 0 {
		return true, fmt.Sprintf("%s 包含 %s", condition.Key, strings.Join(required, "/"))
	}
	if hasPositive && !positiveHit {
		return false, ""
	}
	// 只写了否定条件（例如 keywords: "-战马"）时，未命中任何否定项即为满足。
	return true, fmt.Sprintf("%s 匹配 %s", condition.Key, condition.Values)
}

// matchYearCondition 求值 year：支持单值、范围 a-b、以及否定。
func matchYearCondition(key string, tokens []string, actual []string) (bool, string) {
	years := make([]int, 0, len(actual))
	for _, item := range actual {
		value, err := strconv.Atoi(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		years = append(years, value)
	}
	hasPositive := false
	positiveHit := false
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "-") && len(token) > 1 {
			_, high, isRange, err := parseYearToken(strings.TrimSpace(token[1:]))
			if err != nil {
				return false, ""
			}
			for _, year := range years {
				if isRange {
					low, _ := splitYearRange(strings.TrimSpace(token[1:]))
					if year >= low && year <= high {
						return false, fmt.Sprintf("%s 不得落在 %s", key, token[1:])
					}
					continue
				}
				if year == high {
					return false, fmt.Sprintf("%s 不得为 %s", key, token[1:])
				}
			}
			continue
		}
		low, high, _, err := parseYearToken(token)
		if err != nil {
			return false, ""
		}
		hasPositive = true
		for _, year := range years {
			if year >= low && year <= high {
				positiveHit = true
				break
			}
		}
	}
	if !hasPositive {
		// 只写了否定条件：没有年份数据时视为不命中（无法确认），有年份则已通过。
		return len(years) > 0, fmt.Sprintf("%s 未命中否定区间", key)
	}
	if !positiveHit {
		return false, ""
	}
	return true, fmt.Sprintf("%s 匹配 %s", key, strings.Join(tokens, ","))
}

// splitYearRange 拆出范围下界；非范围时下界等于上界。
func splitYearRange(token string) (int, int) {
	low, high, _, _ := parseYearToken(token)
	return low, high
}

// parseYearToken 解析 "2000" 或 "2000-2099"。
func parseYearToken(token string) (int, int, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, 0, false, fmt.Errorf("年份不能为空")
	}
	if lowText, highText, found := strings.Cut(token, "-"); found {
		low, err := strconv.Atoi(strings.TrimSpace(lowText))
		if err != nil {
			return 0, 0, false, fmt.Errorf("年份范围起始值无效")
		}
		high, err := strconv.Atoi(strings.TrimSpace(highText))
		if err != nil {
			return 0, 0, false, fmt.Errorf("年份范围结束值无效")
		}
		if low > high {
			return 0, 0, false, fmt.Errorf("年份范围起始值不能大于结束值")
		}
		return low, high, true, nil
	}
	value, err := strconv.Atoi(token)
	if err != nil {
		return 0, 0, false, fmt.Errorf("年份必须为数字或数字范围")
	}
	return value, value, false, nil
}

// parseYearValues 校验 year 条件取值，供归一化阶段复用。
func parseYearValues(raw string) ([]string, error) {
	tokens := splitRuleValues(raw)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("年份不能为空")
	}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "-") && len(token) > 1 {
			token = strings.TrimSpace(token[1:])
		}
		if _, _, _, err := parseYearToken(token); err != nil {
			return nil, err
		}
	}
	return tokens, nil
}

// splitRuleValues 按逗号切分值（中英文逗号都接受），并去掉空项。
func splitRuleValues(raw string) []string {
	raw = strings.ReplaceAll(raw, "，", ",")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}

// hasCustomRules 判断当前是否存在启用中的自定义规则。
// 无 DB 句柄（例如旧版部署或单元测试）时返回 false，从而完全回落既有模板逻辑。
func (s *Service) hasCustomRules(ctx context.Context) bool {
	db := s.dbOrNil()
	if db == nil {
		return false
	}
	var count int64
	if err := db.WithContext(ctx).Model(&ClassifyRule{}).
		Where("enabled = ?", true).Count(&count).Error; err != nil {
		// 表尚未建立等情况：视为没有自定义规则，不影响既有模板。
		return false
	}
	return count > 0
}

// matchCustomRules 按 Position 升序评估自定义规则，返回首个命中的目标目录。
func (s *Service) matchCustomRules(ctx context.Context, mediaType string, state *evaluationState) ([]string, map[string]any, error) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		values, err := s.ruleFieldValues(ctx, state, rule)
		if err != nil {
			return nil, nil, err
		}
		evaluation := matchRule(rule, mediaType, values)
		if !evaluation.Matched {
			continue
		}
		segments := strings.Split(rule.TargetPath, "/")
		evidence := map[string]any{
			"rule_id":       rule.ID,
			"target_path":   rule.TargetPath,
			"conditions":    evaluation.Evidence,
			"match_policy":  "first_match_wins",
			"detail_loaded": state.detailLoaded,
			"soft_matched":  evaluation.SoftMatched,
		}
		return segments, evidence, nil
	}
	return nil, nil, nil
}

// ruleFieldValues 收集一条规则涉及的全部字段取值。
// 与模板引擎共用 valuesForField，因此复用同一套 TMDB detail 缓存与降级路径。
func (s *Service) ruleFieldValues(ctx context.Context, state *evaluationState, rule ClassifyRule) (map[string][]string, error) {
	values := make(map[string][]string, len(rule.ConditionList))
	for _, condition := range rule.ConditionList {
		values[condition.Key] = s.valuesForField(ctx, state, condition.Key)
	}
	return values, nil
}
