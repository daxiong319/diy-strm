package classifyorganize

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// 目录整理分类：YAML 规则文件导入 / 导出
//
// 仓库未引入任何 YAML 依赖（`grep -n yaml go.mod` 无输出），且硬约束禁止新增
// 第三方依赖，因此这里手写一个**只针对 C 佬二级分类规则文件子集**的解析器。
//
// 该子集非常窄，因此自写是可控的：
//   movie:            # 顶层键，媒体类型
//     A/B/C:          # 二级键，目标目录（可含 /），缩进 2 空格
//       key: "value"  # 三级键，条件；值可加引号也可不加
//       ?key: "value" # `?` 前缀 = 可选（软）条件
//   # 注释行
//
// 明确不支持：锚点/别名、多行字面量、流式集合、多文档、非字符串标量类型。
// 遇到这类语法会返回带行号的错误，而不是静默产出错误规则。
// ---------------------------------------------------------------------------

// yamlImportResult 导入结果：解析出的规则 + 非致命提示。
type yamlImportResult struct {
	Rules []ClassifyRule
	// Warnings 记录被跳过/降级的内容（例如参考文件末尾被截断、无条件的目录）。
	Warnings []string
	// Series 是从 `series:` 段解析出的系列目录规则（C-8）。
	//
	// 与 Rules 分开而不是塞进 Rules：系列规则不是「影片命中它就放这里」的判定规则，
	// 而是「命中后往目录末尾再挂一段」。两者的执行时机不同，塞进同一个列表的话
	// 导入器就得在两次遍历里区分它们，而区分的依据只是「有没有 target_path」。
	Series []SeriesRule
}

// importRulesFromYAML 解析规则文件文本为规则列表（保持文件顺序即为优先级）。
func importRulesFromYAML(text string) (yamlImportResult, error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	if text == "" {
		return yamlImportResult{}, domain.Errorf(domain.CodeValidation, "规则内容为空")
	}
	result := yamlImportResult{Rules: make([]ClassifyRule, 0, 64)}

	mediaType := ""
	targetPath := ""
	var conditions []RuleCondition
	seenCondition := map[string]bool{}
	// pendingTarget 记录"已读到目录名但还没读到任何条件"的目录，
	// 用于在下一个目录/文件结束处决定是落地还是丢弃。
	pendingTarget := ""
	stateLine := 0
	lineNo := 0
	seriesMode := false
	seriesState := seriesYAMLState{}

	flush := func(endLine int) {
		if pendingTarget == "" {
			return
		}
		if len(conditions) == 0 {
			// 参考文件尾部存在被截断的目录（`剧集/欧美剧集/1994年后欧美剧集:` 后
			// 直接跟注释块）。无条件的目录若导入会成为"永远命中"的兜底规则，
			// 反而破坏文件原本的优先级，因此跳过并给出提示。
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("第 %d 行目录“%s”没有任何条件，已跳过", stateLine, pendingTarget))
			pendingTarget = ""
			return
		}
		rule, err := normalizeRule(ClassifyRule{
			MediaType:     mediaType,
			TargetPath:    pendingTarget,
			ConditionList: conditions,
			Enabled:       true,
		})
		if err == nil {
			result.Rules = append(result.Rules, rule)
		} else {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("第 %d 行目录“%s”无效（%v），已跳过", stateLine, pendingTarget, err))
		}
		pendingTarget = ""
		_ = endLine
	}

	for _, raw := range strings.Split(text, "\n") {
		lineNo++
		line := stripYAMLComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := countIndent(line)
		trimmed := strings.TrimSpace(line)
		// 顶层键（无缩进）：movie: / tv:
		if indent == 0 {
			if !strings.HasSuffix(trimmed, ":") {
				return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
					"第 %d 行格式无效：%s", lineNo, trimmed)
			}
			flush(lineNo)
			key := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
			switch key {
			case "movie", "tv":
				mediaType = key
				seriesMode = false
			case seriesYAMLRoot:
				// series 段用一套独立的缩进约定：目录名在缩进 2，字段在缩进 4。
				// 与 Rules 的差别是它没有条件列表，目录名本身就是要产出的那一段。
				mediaType = ""
				seriesMode = true
			default:
				return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
					"第 %d 行顶层键只支持 movie / tv / %s，实际为“%s”", lineNo, seriesYAMLRoot, key)
			}
			targetPath = ""
			continue
		}
		if seriesMode {
			if err := parseSeriesYAMLLine(&result, trimmed, indent, lineNo, &seriesState); err != nil {
				return yamlImportResult{}, err
			}
			continue
		}
		if mediaType == "" {
			return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
				"第 %d 行出现在 movie/tv 之前", lineNo)
		}
		// 三级（条件）：缩进 >= 4 且含冒号。
		if indent >= 4 {
			if targetPath == "" {
				return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
					"第 %d 行条件缺少所属目录", lineNo)
			}
			key, value, err := splitYAMLPair(trimmed)
			if err != nil {
				return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
					"第 %d 行格式无效：%v", lineNo, err)
			}
			optional := false
			if strings.HasPrefix(key, "?") {
				optional = true
				key = strings.TrimSpace(strings.TrimPrefix(key, "?"))
			}
			condition, err := normalizeRuleCondition(RuleCondition{
				Key: key, Values: value, Optional: optional,
			})
			if err != nil {
				// 未知条件键不应让整份文件导入失败：跳过并提示。
				result.Warnings = append(result.Warnings, fmt.Sprintf("第 %d 行已跳过：%v", lineNo, err))
				continue
			}
			if seenCondition[condition.Key] {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("第 %d 行条件“%s”重复，已采用其一", lineNo, condition.Key))
				continue
			}
			seenCondition[condition.Key] = true
			conditions = append(conditions, condition)
			continue
		}
		// 二级（目标目录）：缩进 2，值形如 A/B/C:
		if !strings.HasSuffix(trimmed, ":") {
			return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
				"第 %d 行目录名必须以冒号结尾：%s", lineNo, trimmed)
		}
		flush(lineNo)
		// 目录名可能被引号包裹（含 ":" 等特殊字符时导出器会加引号），
		// 这里必须剥掉一层成对引号，否则引号会被当成目录名的一部分。
		name := unquoteYAMLKey(strings.TrimSpace(strings.TrimSuffix(trimmed, ":")))
		if name == "" {
			return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
				"第 %d 行目录名为空", lineNo)
		}
		targetPath = strings.Trim(name, "/")
		pendingTarget = targetPath
		conditions = nil
		seenCondition = map[string]bool{}
		stateLine = lineNo
	}
	flush(lineNo)
	if err := flushSeriesRule(&result, &seriesState, lineNo); err != nil {
		return yamlImportResult{}, err
	}

	// 同一媒体类型下目标目录重复：保留先出现者（先出现优先级更高）。
	result.Rules = dedupeRulesByTargetPath(result.Rules, &result.Warnings)
	if len(result.Rules) == 0 && len(result.Series) == 0 {
		return yamlImportResult{}, domain.Errorf(domain.CodeValidation,
			"未从文件解析出任何有效规则%s", warningSuffix(result.Warnings))
	}
	// 目标目录重复等多条同类提示只需展示一次。
	result.Warnings = uniqueStrings(result.Warnings)
	return result, nil
}

// dedupeRulesByTargetPath 按「媒体类型 + 目标目录」去重，同一目录保留先出现者。
// 注意：必须把去重后的切片**返回**给调用方。早先的实现在原切片上 copy 却不截断，
// 调用方拿到的仍是原长度，重复项依然可见（导入结果与警告自相矛盾）。
func dedupeRulesByTargetPath(rules []ClassifyRule, warnings *[]string) []ClassifyRule {
	seen := make(map[string]int, len(rules))
	out := make([]ClassifyRule, 0, len(rules))
	for _, rule := range rules {
		key := rule.MediaType + "\x00" + strings.ToLower(rule.TargetPath)
		if index, dup := seen[key]; dup {
			*warnings = append(*warnings, fmt.Sprintf("目录“%s”重复，已保留第 %d 条", rule.TargetPath, index+1))
			continue
		}
		seen[key] = len(out) + 1
		out = append(out, rule)
	}
	return out
}

func warningSuffix(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return "（" + strings.Join(uniqueStrings(warnings), "；") + "）"
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// stripYAMLComment 去掉行尾注释，但保留引号内的 #。
func stripYAMLComment(line string) string {
	quote := rune(0)
	for index, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			// YAML 注释要求 # 前是空白或行首，避免切断 A#B 这类目录名。
			if index == 0 || line[index-1] == ' ' || line[index-1] == '\t' {
				return line[:index]
			}
		}
	}
	return line
}

func countIndent(line string) int {
	count := 0
	for _, r := range line {
		switch r {
		case ' ':
			count++
		case '\t':
			count += 2
		default:
			return count
		}
	}
	return count
}

// splitYAMLPair 拆分 "key: value" 或 "key: \"value\""，value 允许为空（表示空值）。
func splitYAMLPair(line string) (string, string, error) {
	key, value, found := strings.Cut(line, ":")
	if !found {
		return "", "", fmt.Errorf("缺少冒号")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", fmt.Errorf("键名为空")
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, nil
}

// exportRulesToYAML 把规则导出为与参考文件同构的 YAML 文本。
func exportRulesToYAML(rules []ClassifyRule) string {
	return exportRulesToYAMLWithSeries(rules, nil)
}

// exportRulesToYAMLWithSeries 同时导出规则与系列目录规则（C-8）。
//
// 两者写在同一个文件里是刻意的：只导出一个会造成「用户的目录结构只迁移了一半」
// —— 只导 Rules 会让 Config.Series 里残留的系列目录继续生效，只导 Series 则相反。
func exportRulesToYAMLWithSeries(rules []ClassifyRule, series []SeriesRule) string {
	grouped := map[string][]ClassifyRule{}
	for _, rule := range rules {
		key := rule.MediaType
		if key == "" {
			// 未限定类型的规则同时归属 movie 与 tv，保证导出后语义不丢失。
			grouped["movie"] = append(grouped["movie"], rule)
			grouped["tv"] = append(grouped["tv"], rule)
			continue
		}
		grouped[key] = append(grouped[key], rule)
	}
	var builder strings.Builder
	builder.WriteString("# 目录整理分类规则（由 LitePan 导出）\n")
	builder.WriteString("# 规则按出现顺序评估，首个命中胜出。\n")
	builder.WriteString("# series 段是系列目录规则，命中后会在目录末尾再挂一段。\n")
	for _, mediaType := range []string{"movie", "tv"} {
		items := grouped[mediaType]
		if len(items) == 0 {
			continue
		}
		builder.WriteString(mediaType)
		builder.WriteString(":\n")
		sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })
		for _, rule := range items {
			if !rule.Enabled {
				builder.WriteString("  # 已停用\n")
			}
			builder.WriteString("  ")
			builder.WriteString(quoteYAMLKey(rule.TargetPath))
			builder.WriteString(":\n")
			if len(rule.ConditionList) == 0 {
				builder.WriteString("    # 无条件\n")
			}
			for _, condition := range rule.ConditionList {
				builder.WriteString("    ")
				if condition.Optional {
					builder.WriteString("?")
				}
				builder.WriteString(condition.Key)
				builder.WriteString(": \"")
				builder.WriteString(escapeYAMLDouble(condition.Values))
				builder.WriteString("\"\n")
			}
		}
	}
	builder.WriteString(exportSeriesToYAML(series))
	return builder.String()
}

// quoteYAMLKey 给含特殊字符的键加引号，保证往返可解析。
func quoteYAMLKey(key string) string {
	if key == "" {
		return `""`
	}
	if strings.ContainsAny(key, ":#\"'{}[]&*!|>%@`") ||
		strings.HasPrefix(key, "?") || strings.HasPrefix(key, "-") ||
		strings.TrimSpace(key) != key {
		return `"` + escapeYAMLDouble(key) + `"`
	}
	return key
}

// unquoteYAMLKey 剥掉一层成对的引号并还原转义，是 quoteYAMLKey 的逆操作。
// 仅在首尾引号成对时剥离，避免把目录名里合法的单个引号吃掉。
func unquoteYAMLKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return raw
	}
	first, last := raw[0], raw[len(raw)-1]
	if first != last || (first != '"' && first != '\'') {
		return raw
	}
	inner := raw[1 : len(raw)-1]
	if first == '\'' {
		// 单引号串里 '' 表示一个单引号。
		return strings.ReplaceAll(inner, "''", "'")
	}
	return unescapeYAMLValue(inner)
}

func escapeYAMLDouble(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// unescapeYAMLValue 还原导出时转义的字符，供往返测试与手工粘贴使用。
func unescapeYAMLValue(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var builder strings.Builder
	escaped := false
	for _, r := range value {
		if escaped {
			switch r {
			case 'n':
				builder.WriteRune('\n')
			case 'r':
				builder.WriteRune('\r')
			case 't':
				builder.WriteRune('\t')
			default:
				builder.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		builder.WriteRune(r)
	}
	if escaped {
		builder.WriteRune('\\')
	}
	return builder.String()
}

// validateYAMLText 供 API 预校验使用：解析但不落库。
func validateYAMLText(text string) (yamlImportResult, error) {
	if utf8.RuneCountInString(text) > 2*1024*1024 {
		return yamlImportResult{}, domain.Errorf(domain.CodeValidation, "规则文件不能超过 2MB")
	}
	return importRulesFromYAML(text)
}

// ImportRulesFromYAML 解析并整体替换当前规则；replace=false 时追加到末尾。
func (s *Service) ImportRulesFromYAML(ctx context.Context, text string, replace bool) ([]ClassifyRule, []string, error) {
	parsed, err := validateYAMLText(text)
	if err != nil {
		return nil, nil, err
	}
	if replace {
		rules, err := s.ReplaceRules(ctx, parsed.Rules)
		if err != nil {
			return nil, nil, err
		}
		// 系列规则与规则表一起替换：replace 的语义是「这份文件就是全部配置」，
		// 只换一半会让旧系列规则残留在配置里，而界面上已经看不到它们了。
		if err := s.replaceSeriesRules(ctx, parsed.Series); err != nil {
			return nil, nil, err
		}
		return rules, parsed.Warnings, nil
	}
	for _, rule := range parsed.Rules {
		if _, err := s.CreateRule(ctx, rule); err != nil {
			return nil, nil, err
		}
	}
	rules, err := s.ListRules(ctx)
	if err != nil {
		return nil, nil, err
	}
	return rules, parsed.Warnings, nil
}

// ExportRulesToYAML 导出当前规则为 YAML 文本。
func (s *Service) ExportRulesToYAML(ctx context.Context) (string, error) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return "", err
	}
	return exportRulesToYAMLWithSeries(rules, s.Config().Series), nil
}
