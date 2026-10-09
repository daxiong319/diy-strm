package classifyorganize

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"litepan/internal/domain"
)

// ---------------------------------------------------------------------------
// 系列目录规则的 YAML 导入/导出（C-8）
//
// 语法（顶层第三块，与 movie / tv 平级）：
//
//	series:
//	  流浪地球:
//	    series_keywords: "流浪地球,The Wandering Earth"
//	  星球大战:
//	    dir_name: "星球大战系列"
//	    media_type: movie
//	    position: 10
//
// 缩进约定沿用既有规则文件的习惯：顶层键 0，目录名 2，字段 4。
// 这不是最优雅的 YAML，但它和 Rules 段长得一样，用户在同一个文件里
// 改两块内容时不用换脑子。
//
// 为什么系列规则要和 Rules 一起进同一个文件而不是分两个文件：
// 两者必须同时迁移才正确 —— 只导入了 Rules 会让用户的分类目录里
// 突然多出一堆「流浪地球系列」目录（因为 Config.Series 还在），或者反过来。
// 拆成两个文件，用户就会只备份其中一个，然后某天升级后目录结构整个变了。
// ---------------------------------------------------------------------------

const seriesYAMLRoot = "series"

// seriesYAMLState 记录 series 段当前读到哪一条规则。
type seriesYAMLState struct {
	pending string // 已读到目录名但还没读到任何字段的行号对应的名字
	rule    SeriesRule
	seen    map[string]bool
}

// parseSeriesYAMLLine 处理 series 段内的一行。
func parseSeriesYAMLLine(result *yamlImportResult, trimmed string, indent, lineNo int, state *seriesYAMLState) error {
	switch {
	case indent == 2:
		// 目录名行：形如 `流浪地球:`。注意这里不消费条件，series 段的
		// 缩进 4 是字段而不是条件。
		if !strings.HasSuffix(trimmed, ":") {
			return domain.Errorf(domain.CodeValidation,
				"第 %d 行 series 目录名必须以冒号结尾：%s", lineNo, trimmed)
		}
		if err := flushSeriesRule(result, state, lineNo); err != nil {
			return err
		}
		name := unquoteYAMLKey(strings.TrimSpace(strings.TrimSuffix(trimmed, ":")))
		if name == "" {
			return domain.Errorf(domain.CodeValidation, "第 %d 行 series 目录名为空", lineNo)
		}
		state.pending = name
		state.rule = SeriesRule{Name: name}
		state.seen = map[string]bool{}
		return nil
	case indent >= 4:
		if state.pending == "" {
			return domain.Errorf(domain.CodeValidation,
				"第 %d 行 series 字段缺少所属目录", lineNo)
		}
		key, value, err := splitYAMLPair(trimmed)
		if err != nil {
			return domain.Errorf(domain.CodeValidation, "第 %d 行格式无效：%v", lineNo, err)
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "?"))
		if state.seen[key] {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("第 %d 行 series 字段“%s”重复，已采用其一", lineNo, key))
			return nil
		}
		state.seen[key] = true
		return applySeriesYAMLField(result, state, key, value, lineNo)
	default:
		return domain.Errorf(domain.CodeValidation,
			"第 %d 行 series 段缩进无效：%s", lineNo, trimmed)
	}
}

func applySeriesYAMLField(result *yamlImportResult, state *seriesYAMLState, key, value string, lineNo int) error {
	value = strings.TrimSpace(value)
	switch key {
	case fieldSeries:
		state.rule.SeriesKeywords = splitYAMLList(value)
	case fieldKeywords:
		state.rule.Keywords = splitYAMLList(value)
	case seriesFieldDirName:
		state.rule.DirName = unquoteYAMLKey(value)
	case seriesFieldMediaType:
		state.rule.MediaType = strings.ToLower(value)
	case seriesFieldPosition:
		pos, err := strconv.Atoi(value)
		if err != nil {
			// 位置写错了就丢给用户看，而不是悄悄当 0 —— 当 0 会把这条规则
			// 排到最前面，用户的排序意图整个反转，而且界面上看不出来。
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("第 %d 行 series 字段 position 不是整数，已忽略：%s", lineNo, value))
			return nil
		}
		state.rule.Position = pos
	case seriesFieldRemark:
		state.rule.Remark = value
	default:
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("第 %d 行 series 未知字段“%s”，已跳过", lineNo, key))
	}
	return nil
}

// flushSeriesRule 把待定的系列规则落进结果。
func flushSeriesRule(result *yamlImportResult, state *seriesYAMLState, lineNo int) error {
	if state.pending == "" {
		return nil
	}
	if len(state.rule.SeriesKeywords) == 0 && len(state.rule.Keywords) == 0 {
		// 与 Rules 段同样跳过无条件条目：没有关键词的系列规则对任何影片都成立，
		// 等于把全部分类结果塞进同一个目录。
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("第 %d 行 series 目录“%s”没有任何关键词，已跳过", lineNo, state.pending))
		state.pending = ""
		return nil
	}
	rule := state.rule
	rule.Name = state.pending
	result.Series = append(result.Series, rule)
	state.pending = ""
	return nil
}

const (
	seriesFieldDirName   = "dir_name"
	seriesFieldMediaType = "media_type"
	seriesFieldPosition  = "position"
	seriesFieldRemark    = "remark"
)

// splitYAMLList 把 `流浪地球,The Wandering Earth` 切成 []string。
// 与 SeriesKeywords 的存储口径一致（逗号或分号分隔），所以往返不会变形。
func splitYAMLList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})
	out := make([]string, 0, len(fields))
	for _, item := range fields {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// joinYAMLList 是 splitYAMLList 的逆操作。
func joinYAMLList(items []string) string {
	return strings.Join(items, ",")
}

// exportSeriesToYAML 导出系列规则段。没有系列规则时返回空串，
// 免得导出的文件里躺着一个空的 `series:` 段让人以为配了东西。
func exportSeriesToYAML(rules []SeriesRule) string {
	if len(rules) == 0 {
		return ""
	}
	sorted := append([]SeriesRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	var builder strings.Builder
	builder.WriteString(seriesYAMLRoot)
	builder.WriteString(":\n")
	for _, rule := range sorted {
		builder.WriteString("  ")
		builder.WriteString(quoteYAMLKey(rule.Name))
		builder.WriteString(":\n")
		writeSeriesField(&builder, seriesFieldDirName, rule.DirName)
		writeSeriesField(&builder, seriesFieldMediaType, rule.MediaType)
		writeSeriesField(&builder, fieldSeries, joinYAMLList(rule.SeriesKeywords))
		writeSeriesField(&builder, fieldKeywords, joinYAMLList(rule.Keywords))
		writeSeriesField(&builder, seriesFieldPosition, strconv.Itoa(rule.Position))
		writeSeriesField(&builder, seriesFieldRemark, rule.Remark)
	}
	return builder.String()
}

func writeSeriesField(builder *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" || value == "0" {
		return
	}
	builder.WriteString("    ")
	builder.WriteString(key)
	builder.WriteString(": \"")
	builder.WriteString(escapeYAMLDouble(value))
	builder.WriteString("\"\n")
}
