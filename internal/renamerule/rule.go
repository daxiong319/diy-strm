// Package renamerule 批量重命名规则引擎。
// 移植自老版 internal/renamerule（其本身来自 123 云盘批量重命名油猴脚本的 RenameEngine）：
// 支持 12 种规则（查找替换 / 添加文件夹名 / 正则重命名 / 名称模板 / 添加序号 /
// 添加分隔符 / 添加字符 / 删除字符 / 移动字符 / 大小写转换 / 清理空格 / 全角半角转换）、
// 保留扩展名、规则与目标校验（重名 / 交换冲突 / 超长等）。
//
// 本包是纯函数式的：不触碰文件系统，也不依赖任何仓储，便于表驱动测试。
package renamerule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RuleType 规则类型常量。
const (
	TypeReplace   = "replace"   // 查找替换
	TypeFolder    = "folder"    // 添加文件夹名
	TypeRegex     = "regex"     // 基于正则重命名
	TypeSetName   = "setname"   // 名称模板
	TypeNumber    = "number"    // 修改名称/添加序号
	TypeSeparator = "separator" // 添加分隔符
	TypeAdd       = "add"       // 添加字符
	TypeDelete    = "delete"    // 删除字符
	TypeMove      = "move"      // 移动字符
	TypeCase      = "case"      // 大小写字母转换
	TypeSpace     = "space"     // 清理空格
	TypeWidth     = "width"     // 全角半角转换
)

// RuleTypes 全部规则类型（有序，供前端下拉与后端校验共用）。
var RuleTypes = []string{
	TypeReplace,
	TypeFolder,
	TypeRegex,
	TypeSetName,
	TypeNumber,
	TypeSeparator,
	TypeAdd,
	TypeDelete,
	TypeMove,
	TypeCase,
	TypeSpace,
	TypeWidth,
}

var ruleTypeLabels = map[string]string{
	TypeReplace:   "查找替换",
	TypeFolder:    "添加文件夹名",
	TypeRegex:     "基于正则重命名",
	TypeSetName:   "名称模板",
	TypeNumber:    "修改名称/添加序号",
	TypeSeparator: "添加分隔符",
	TypeAdd:       "添加字符",
	TypeDelete:    "删除字符",
	TypeMove:      "移动字符",
	TypeCase:      "大小写字母转换",
	TypeSpace:     "清理空格",
	TypeWidth:     "全角半角转换",
}

// Rule 一条重命名规则。字段与油猴脚本 Rule 对齐，未使用的字段忽略。
type Rule struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Find          string `json:"find"`
	Replace       string `json:"replace"`
	Pattern       string `json:"pattern"`
	Position      string `json:"position"`
	Separator     string `json:"separator"`
	FolderName    string `json:"folder_name"`
	Start         string `json:"start"`
	Digits        string `json:"digits"`
	Prefix        string `json:"prefix"`
	Suffix        string `json:"suffix"`
	Text          string `json:"text"`
	Index         string `json:"index"`
	Mode          string `json:"mode"`
	Length        string `json:"length"`
	To            string `json:"to"`
	CaseSensitive bool   `json:"case_sensitive"`
	FirstOnly     bool   `json:"first_only"`
}

// TypeLabel 返回规则类型中文名，未知类型返回泛指。
func TypeLabel(ruleType string) string {
	if label, ok := ruleTypeLabels[ruleType]; ok {
		return label
	}
	return "批量重命名"
}

// IsValidType 判断规则类型是否已支持。
func IsValidType(ruleType string) bool {
	_, ok := ruleTypeLabels[ruleType]
	return ok
}

// Defaults 返回某规则类型的默认配置。
func Defaults(ruleType string) Rule {
	rule := Rule{Type: ruleType}
	switch ruleType {
	case TypeFolder:
		rule.Position = "prefix"
		rule.Separator = "-"
	case TypeSetName:
		rule.Pattern = "{name}"
		rule.Start = "1"
		rule.Digits = "2"
	case TypeNumber:
		rule.Position = "replace"
		rule.Start = "1"
		rule.Digits = "2"
	case TypeSeparator, TypeAdd:
		rule.Position = "end"
		rule.Text = "-"
		rule.Index = "1"
	case TypeDelete:
		rule.Mode = "text"
		rule.Start = "1"
		rule.Length = "1"
	case TypeMove:
		rule.Start = "1"
		rule.Length = "1"
		rule.To = "1"
	case TypeCase:
		rule.Mode = "upper"
	case TypeSpace:
		rule.Mode = "trim"
	case TypeWidth:
		rule.Mode = "half"
	}
	return rule
}

// Target 待重命名目标。Type 为 0 表示文件、1 表示目录。
type Target struct {
	ID       string `json:"file_id"`
	Name     string `json:"name"`
	NewName  string `json:"new_name"`
	Type     int    `json:"type"`
	ParentID string `json:"parent_id"`
}

// IsDir 判断目标是否为目录。
func (t Target) IsDir() bool { return t.Type == 1 }

// PreviewRow 预览结果行。
type PreviewRow struct {
	Target  Target `json:"target"`
	Changed bool   `json:"changed"`
}

// Preview 批量应用规则生成预览，不修改任何真实文件。
func Preview(targets []Target, rules []Rule, keepExt bool, folderName string) []PreviewRow {
	rows := make([]PreviewRow, 0, len(targets))
	for i, target := range targets {
		newName := strings.TrimSpace(Apply(target.Name, i, rules, keepExt, folderName, target.IsDir()))
		rows = append(rows, PreviewRow{
			Target: Target{
				ID:       target.ID,
				Name:     target.Name,
				NewName:  newName,
				Type:     target.Type,
				ParentID: target.ParentID,
			},
			Changed: newName != target.Name,
		})
	}
	return rows
}

// Apply 对单个名称依次应用全部规则。
func Apply(name string, index int, rules []Rule, keepExt bool, folderName string, isFolder bool) string {
	value, extension := splitName(name, keepExt && !isFolder)
	for _, rule := range rules {
		value = applyRule(value, index, rule, folderName)
	}
	return value + extension
}

// splitName 按扩展名拆分；无扩展名、隐藏文件或 keepExt=false 时扩展名为空串。
func splitName(name string, keepExt bool) (string, string) {
	if !keepExt {
		return name, ""
	}
	index := strings.LastIndex(name, ".")
	// index<=0 覆盖 ".gitignore" 这类隐藏文件；index==len-1 覆盖结尾的点。
	if index <= 0 || index == len(name)-1 {
		return name, ""
	}
	return name[:index], name[index:]
}

// ValidateRules 校验全部规则，返回去重后的错误列表。
func ValidateRules(rules []Rule) []string {
	var errors []string
	for i, rule := range rules {
		errors = append(errors, validateRule(rule, i)...)
	}
	return dedupe(errors)
}

// validateRule 单条规则校验。
func validateRule(rule Rule, index int) []string {
	var errors []string
	label := fmt.Sprintf("第 %d 条规则", index+1)
	switch rule.Type {
	case TypeRegex:
		if rule.Pattern != "" {
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				errors = append(errors, label+" 的正则表达式无效")
			}
		}
	case TypeNumber, TypeSetName:
		if _, ok := parseOptionalInt(rule.Start); !ok {
			errors = append(errors, label+" 的起始编号无效")
		}
		if digits, ok := parseOptionalInt(rule.Digits); !ok || digits < 1 {
			errors = append(errors, label+" 的位数必须大于 0")
		}
		if rule.Type == TypeSetName && strings.TrimSpace(rule.Pattern) == "" {
			errors = append(errors, label+" 的名称模板不能为空")
		}
	case TypeDelete:
		if rule.Mode == "range" {
			if start, ok := parseOptionalInt(rule.Start); !ok || start < 1 {
				errors = append(errors, label+" 的起始位置必须从 1 开始")
			}
		}
	case TypeMove:
		if start, ok := parseOptionalInt(rule.Start); !ok || start < 1 {
			errors = append(errors, label+" 的起始位置必须从 1 开始")
		}
		if length, ok := parseOptionalInt(rule.Length); !ok || length < 1 {
			errors = append(errors, label+" 的长度必须大于 0")
		}
		if to, ok := parseOptionalInt(rule.To); !ok || to < 1 {
			errors = append(errors, label+" 的移动到位置必须从 1 开始")
		}
	}
	return errors
}

// ValidateTargets 校验预览后的目标：空名、非法字符、超长、同目录重名与交换冲突、
// 与已有文件冲突。existingNamesByParent 为 parent_id -> 该目录下除本次目标外
// 的已有名字（大小写不敏感）。
func ValidateTargets(targets []Target, existingNamesByParent map[string][]string) []string {
	var errors []string

	for _, target := range targets {
		name := strings.TrimSpace(target.NewName)
		if name == "" {
			errors = append(errors, "存在空文件名")
		}
		if strings.ContainsAny(name, `/\`) {
			errors = append(errors, `文件名不能包含 / 或 \`)
		}
		if name == "." || name == ".." {
			errors = append(errors, "文件名不能为 . 或 ..")
		}
		// 按字符数而非字节数计算，避免中文名被误判超长。
		if len([]rune(name)) > 255 {
			errors = append(errors, "文件名不能超过 255 个字符")
		}
	}

	byParent := map[string][]Target{}
	for _, target := range targets {
		parentID := target.ParentID
		if parentID == "" {
			parentID = "0"
		}
		byParent[parentID] = append(byParent[parentID], target)
	}

	for parentID, group := range byParent {
		newNames := map[string]int{}
		oldNames := map[string]bool{}
		for _, target := range group {
			oldNames[strings.ToLower(strings.TrimSpace(target.Name))] = true
		}
		existing := map[string]bool{}
		for _, name := range existingNamesByParent[parentID] {
			existing[strings.ToLower(strings.TrimSpace(name))] = true
		}
		for _, target := range group {
			newName := strings.TrimSpace(target.NewName)
			normalized := strings.ToLower(newName)
			newNames[normalized]++
			if newNames[normalized] > 1 {
				errors = append(errors, fmt.Sprintf("存在重复的新文件名：%s", newName))
			}
			oldLower := strings.ToLower(strings.TrimSpace(target.Name))
			// 目标名恰好是本次另一条目的旧名 -> 交换冲突（网盘逐条改名会互相覆盖）。
			if newName != "" && oldLower != normalized && oldNames[normalized] {
				errors = append(errors, fmt.Sprintf("存在文件名交换冲突：%s -> %s", target.Name, newName))
			}
			if newName != oldLower && existing[normalized] {
				errors = append(errors, fmt.Sprintf("同一目录已存在：%s", newName))
			}
		}
	}

	return dedupe(errors)
}

// applyRule 单条规则应用。
func applyRule(base string, index int, rule Rule, folderName string) string {
	switch rule.Type {
	case TypeReplace:
		return replaceText(base, rule)
	case TypeFolder:
		folder := strings.TrimSpace(rule.FolderName)
		if folder == "" {
			folder = strings.TrimSpace(folderName)
		}
		if folder == "" {
			return base
		}
		if rule.Position == "suffix" {
			return base + rule.Separator + folder
		}
		return folder + rule.Separator + base
	case TypeRegex:
		if rule.Pattern == "" {
			return base
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return base
		}
		return re.ReplaceAllString(base, rule.Replace)
	case TypeSetName:
		return template(rule.Pattern, base, index, rule)
	case TypeNumber:
		numberText := rule.Prefix + sequence(index, rule) + rule.Suffix
		switch rule.Position {
		case "prefix":
			return numberText + base
		case "suffix":
			return base + numberText
		}
		if strings.TrimSpace(numberText) != "" {
			return numberText
		}
		return base
	case TypeSeparator, TypeAdd:
		position := rule.Position
		if position == "" {
			position = "end"
		}
		return insert(base, rule.Text, position, rule.Index)
	case TypeDelete:
		if rule.Mode == "range" {
			return deleteRange(base, rule.Start, rule.Length)
		}
		if rule.Text != "" {
			return strings.ReplaceAll(base, rule.Text, "")
		}
		return base
	case TypeMove:
		return moveRange(base, rule.Start, rule.Length, rule.To)
	case TypeCase:
		switch rule.Mode {
		case "lower":
			return strings.ToLower(base)
		case "title":
			return titleCase(base)
		}
		return strings.ToUpper(base)
	case TypeSpace:
		switch rule.Mode {
		case "all":
			return strings.Map(func(r rune) rune {
				switch r {
				case ' ', '\t', '\n', '\r', '\f', '\v':
					return -1
				}
				return r
			}, base)
		case "collapse":
			return collapseSpaceRe.ReplaceAllString(base, " ")
		}
		return strings.TrimSpace(base)
	case TypeWidth:
		if rule.Mode == "full" {
			return toFullWidth(base)
		}
		return toHalfWidth(base)
	default:
		return base
	}
}

var collapseSpaceRe = regexp.MustCompile(`\s+`)

// replaceText 查找替换。大小写不敏感时用 (?i:) 包装并用 QuoteMeta 转义。
func replaceText(base string, rule Rule) string {
	if rule.Find == "" {
		return base
	}
	if !rule.CaseSensitive {
		re, err := regexp.Compile("(?i:" + regexp.QuoteMeta(rule.Find) + ")")
		if err != nil {
			return base
		}
		if rule.FirstOnly {
			loc := re.FindStringIndex(base)
			if loc == nil {
				return base
			}
			return base[:loc[0]] + rule.Replace + base[loc[1]:]
		}
		return re.ReplaceAllString(base, rule.Replace)
	}
	if rule.FirstOnly {
		index := strings.Index(base, rule.Find)
		if index < 0 {
			return base
		}
		return base[:index] + rule.Replace + base[index+len(rule.Find):]
	}
	return strings.ReplaceAll(base, rule.Find, rule.Replace)
}

// template 名称模板：{name} 原名称、{n} 序号。
func template(pattern, base string, index int, rule Rule) string {
	return strings.ReplaceAll(strings.ReplaceAll(pattern, "{name}", base), "{n}", sequence(index, rule))
}

// insert 在指定位置插入文本。位置为 1 基的字符下标。
func insert(base, text, position, rawIndex string) string {
	if text == "" {
		return base
	}
	if position == "start" {
		return text + base
	}
	if position == "end" {
		return base + text
	}
	chars := []rune(base)
	idx := int64(1)
	if v, ok := parseOptionalInt(rawIndex); ok {
		idx = v
	}
	at := clamp(int(idx-1), 0, len(chars))
	return string(chars[:at]) + text + string(chars[at:])
}

// deleteRange 删除从 start 开始、长度为 length 的字符区间。
func deleteRange(base, rawStart, rawLength string) string {
	chars := []rune(base)
	at, end := charRange(len(chars), rawStart, rawLength)
	return string(chars[:at]) + string(chars[end:])
}

// moveRange 把从 start 开始、长度为 length 的字符区间移动到 to 位置。
func moveRange(base, rawStart, rawLength, rawTo string) string {
	chars := []rune(base)
	at, end := charRange(len(chars), rawStart, rawLength)
	if at >= len(chars) || end <= at {
		return base
	}
	cut := chars[at:end]
	rest := append(append([]rune{}, chars[:at]...), chars[end:]...)
	to := int64(1)
	if v, ok := parseOptionalInt(rawTo); ok {
		to = v
	}
	dest := clamp(int(to-1), 0, len(rest))
	return string(rest[:dest]) + string(cut) + string(rest[dest:])
}

// charRange 把 1 基的 start/length 归一为 [at, end) 边界，越界自动收敛。
func charRange(size int, rawStart, rawLength string) (int, int) {
	start := int64(1)
	if v, ok := parseOptionalInt(rawStart); ok {
		start = v
	}
	length := int64(1)
	if v, ok := parseOptionalInt(rawLength); ok {
		length = v
	}
	at := clamp(int(start-1), 0, size)
	end := at + int(length)
	if end > size {
		end = size
	}
	if end < at {
		end = at
	}
	return at, end
}

// titleCase 单词首字母大写（空格/点/横线/下划线后视为新词）。
func titleCase(base string) string {
	var builder strings.Builder
	upper := true
	for _, char := range base {
		isLetter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		if upper && isLetter {
			builder.WriteString(strings.ToUpper(string(char)))
		} else {
			builder.WriteRune(char)
		}
		if isLetter {
			upper = false
		} else if char == ' ' || char == '.' || char == '-' || char == '_' {
			upper = true
		}
	}
	return builder.String()
}

// toHalfWidth 全角转半角。
func toHalfWidth(base string) string {
	var builder strings.Builder
	for _, char := range base {
		switch {
		case char == 0x3000:
			builder.WriteRune(' ')
		case char >= 0xff01 && char <= 0xff5e:
			builder.WriteRune(char - 0xfee0)
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// toFullWidth 半角转全角。
func toFullWidth(base string) string {
	var builder strings.Builder
	for _, char := range base {
		switch {
		case char == 0x20:
			builder.WriteRune(0x3000)
		case char >= 0x21 && char <= 0x7e:
			builder.WriteRune(char + 0xfee0)
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// sequence 生成序号文本（起始+index，不足位数补零）。
func sequence(index int, rule Rule) string {
	start := int64(1)
	if v, ok := parseOptionalInt(rule.Start); ok {
		start = v
	}
	digits := int64(2)
	if v, ok := parseOptionalInt(rule.Digits); ok && v > 0 {
		digits = v
	}
	numberText := strconv.FormatInt(start+int64(index), 10)
	for int64(len(numberText)) < digits {
		numberText = "0" + numberText
	}
	return numberText
}

// parseOptionalInt 解析十进制整数；空串返回 (0,false)，与老版 safeInteger 语义一致。
func parseOptionalInt(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// clamp 限制值范围。
func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// dedupe 去重但保持顺序，并丢弃空串。
func dedupe(items []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}
