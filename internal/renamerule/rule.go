// Package renamerule 批量重命名规则引擎。
// 移植自 123 云盘批量重命名油猴脚本（123-batch-rename.user.js）的 RenameEngine：
// 支持 12 种规则（查找替换 / 添加文件夹名 / 正则重命名 / 名称模板 / 添加序号 /
// 添加分隔符 / 添加字符 / 删除字符 / 移动字符 / 大小写转换 / 清理空格 / 全角半角转换）、
// 保留扩展名、规则与目标校验（重名 / 交换冲突 / 超长等）。
package renamerule

import (
	"fmt"
	"regexp"
	"strings"
)

// RuleType 规则类型常量。
const (
	TypeReplace    = "replace"    // 查找替换
	TypeFolder     = "folder"     // 添加文件夹名
	TypeRegex      = "regex"      // 基于正则重命名
	TypeSetName    = "setname"    // 名称模板
	TypeNumber     = "number"     // 修改名称/添加序号
	TypeSeparator  = "separator"  // 添加分隔符
	TypeAdd        = "add"        // 添加字符
	TypeDelete     = "delete"     // 删除字符
	TypeMove       = "move"       // 移动字符
	TypeCase       = "case"       // 大小写字母转换
	TypeSpace      = "space"      // 清理空格
	TypeWidth      = "width"      // 全角半角转换
)

// Rule 一条重命名规则。字段与脚本 Rule 对齐，未使用的字段忽略。
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

// RuleTypeLabel 规则类型中文名称。
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

// RuleTypes 全部规则类型（有序）。
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

// typeLabel 返回规则类型中文名，未知类型返回泛指。
func typeLabel(ruleType string) string {
	if label, ok := ruleTypeLabels[ruleType]; ok {
		return label
	}
	return "批量重命名"
}

// Defaults 返回某规则类型的默认配置。
func Defaults(ruleType string) Rule {
	rule := Rule{
		Type:     ruleType,
		Position: "",
		Separator: "",
		Start:    "",
		Digits:   "",
		Text:     "",
		Index:    "",
		Mode:     "",
		Length:   "",
		To:       "",
	}
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

// Target 待重命名目标。
type Target struct {
	ID       string `json:"file_id"`
	Name     string `json:"name"`
	NewName  string `json:"new_name"`
	Type     int    `json:"type"` // 0=文件 1=目录
	ParentID string `json:"parent_id"`
}

// PreviewRow 预览结果行。
type PreviewRow struct {
	Target  Target `json:"target"`
	Changed bool   `json:"changed"`
}

// Preview 批量应用规则生成预览。
func Preview(targets []Target, rules []Rule, keepExt bool, folderName string) []PreviewRow {
	rows := make([]PreviewRow, 0, len(targets))
	for i, target := range targets {
		newName := strings.TrimSpace(Apply(target.Name, i, rules, keepExt, folderName, target.Type == 1))
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

// splitName 按扩展名拆分，无扩展名或 keepExt=false 时原样返回。
func splitName(name string, keepExt bool) (string, string) {
	if !keepExt {
		return name, ""
	}
	index := strings.LastIndex(name, ".")
	if index <= 0 || index == len(name)-1 {
		return name, ""
	}
	return name[:index], name[index:]
}

// validateRule 单条规则校验，返回错误信息（可能有多条）。
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
		if _, ok := safeInteger(rule.Start, 0); !ok {
			errors = append(errors, label+" 的起始编号无效")
		}
		if digits, ok := safeInteger(rule.Digits, 0); !ok || digits < 1 {
			errors = append(errors, label+" 的位数必须大于 0")
		}
		if rule.Type == TypeSetName && strings.TrimSpace(rule.Pattern) == "" {
			errors = append(errors, label+" 的名称模板不能为空")
		}
	case TypeDelete:
		if rule.Mode == "range" {
			start, ok := safeInteger(rule.Start, 0)
			if !ok || start < 1 {
				errors = append(errors, label+" 的起始位置必须从 1 开始")
			}
		}
	case TypeMove:
		start, ok := safeInteger(rule.Start, 0)
		if !ok || start < 1 {
			errors = append(errors, label+" 的起始位置必须从 1 开始")
		}
		length, ok := safeInteger(rule.Length, 0)
		if !ok || length < 1 {
			errors = append(errors, label+" 的长度必须大于 0")
		}
		to, ok := safeInteger(rule.To, 0)
		if !ok || to < 1 {
			errors = append(errors, label+" 的移动到位置必须从 1 开始")
		}
	}
	return errors
}

// ValidateRules 校验全部规则，返回去重后的错误列表。
func ValidateRules(rules []Rule) []string {
	var errors []string
	for i, rule := range rules {
		errors = append(errors, validateRule(rule, i)...)
	}
	return dedupe(errors)
}

// ValidateTargets 校验预览后的目标：空名、非法字符、超长、同目录重名与交换冲突、与已有文件冲突。
// existingNamesByParent：parent_id -> 该目录下除本次目标外的已有名字（大小写不敏感集合）。
func ValidateTargets(targets []Target, existingNamesByParent map[string][]string) []string {
	var errors []string

	for _, target := range targets {
		name := strings.TrimSpace(target.NewName)
		if name == "" {
			errors = append(errors, "存在空文件名")
		}
		if strings.ContainsAny(name, "/\\") {
			errors = append(errors, "文件名不能包含 / 或 \\")
		}
		if name == "." || name == ".." {
			errors = append(errors, "文件名不能为 . 或 ..")
		}
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

// characters 按 Unicode 码点拆分字符串。
func characters(value string) []rune {
	return []rune(value)
}

// safeInteger 解析十进制整数；missing 为 true 时返回 fallback（字段为空的情况）。
func safeInteger(value string, fallback int64) (int64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return fallback, false
	}
	var n int64
	var err error
	n, err = parseInt64(text)
	if err != nil {
		return 0, false
	}
	return n, true
}

// sequence 生成序号文本（起始+index，不足位数补零）。
func sequence(index int, rule Rule) string {
	start := int64(1)
	if v, ok := safeInteger(rule.Start, 0); ok {
		start = v
	}
	digits := int64(2)
	if v, ok := safeInteger(rule.Digits, 0); ok && v > 0 {
		digits = v
	}
	numberText := fmt.Sprintf("%d", start+int64(index))
	for int64(len(numberText)) < digits {
		numberText = "0" + numberText
	}
	return numberText
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
				if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' {
					return -1
				}
				return r
			}, base)
		case "collapse":
			re := regexp.MustCompile(`\s+`)
			return re.ReplaceAllString(base, " ")
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

// replaceText 查找替换。
func replaceText(base string, rule Rule) string {
	if rule.Find == "" {
		return base
	}
	if !rule.CaseSensitive {
		pattern := regexp.QuoteMeta(rule.Find)
		re, err := regexp.Compile("(?i:" + pattern + ")")
		if err != nil {
			return base
		}
		replacement := rule.Replace
		if rule.FirstOnly {
			loc := re.FindStringIndex(base)
			if loc == nil {
				return base
			}
			return base[:loc[0]] + replacement + base[loc[1]:]
		}
		return re.ReplaceAllString(base, replacement)
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

// insert 在指定位置插入文本。
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
	chars := characters(base)
	idx := int64(1)
	if v, ok := safeInteger(rawIndex, 0); ok {
		idx = v
	}
	at := clamp(int(idx-1), 0, len(chars))
	if at > len(chars) {
		at = len(chars)
	}
	return string(chars[:at]) + text + string(chars[at:])
}

// deleteRange 删除指定位置区间的字符。
func deleteRange(base, rawStart, rawLength string) string {
	chars := characters(base)
	start := int64(1)
	if v, ok := safeInteger(rawStart, 0); ok {
		start = v
	}
	length := int64(1)
	if v, ok := safeInteger(rawLength, 0); ok {
		length = v
	}
	at := int(start - 1)
	if at < 0 {
		at = 0
	}
	if at > len(chars) {
		at = len(chars)
	}
	end := at + int(length)
	if end > len(chars) {
		end = len(chars)
	}
	return string(chars[:at]) + string(chars[end:])
}

// moveRange 移动指定位置区间的字符到新位置。
func moveRange(base, rawStart, rawLength, rawTo string) string {
	chars := characters(base)
	start := int64(1)
	if v, ok := safeInteger(rawStart, 0); ok {
		start = v
	}
	length := int64(1)
	if v, ok := safeInteger(rawLength, 0); ok {
		length = v
	}
	to := int64(1)
	if v, ok := safeInteger(rawTo, 0); ok {
		to = v
	}
	at := int(start - 1)
	if at < 0 {
		at = 0
	}
	if at >= len(chars) || length <= 0 {
		return base
	}
	end := at + int(length)
	if end > len(chars) {
		end = len(chars)
	}
	cut := chars[at:end]
	rest := append(append([]rune{}, chars[:at]...), chars[end:]...)
	dest := int(to - 1)
	if dest < 0 {
		dest = 0
	}
	if dest > len(rest) {
		dest = len(rest)
	}
	return string(rest[:dest]) + string(cut) + string(rest[dest:])
}

// titleCase 单词首字母大写（空格/点/横线/下划线后视为新词）。
func titleCase(base string) string {
	var builder strings.Builder
	upper := true
	for _, char := range characters(base) {
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
	for _, char := range characters(base) {
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
	for _, char := range characters(base) {
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

// dedupe 去重但保持顺序。
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

// parseInt64 解析 int64。
func parseInt64(text string) (int64, error) {
	var n int64
	negative := false
	index := 0
	if index < len(text) && (text[index] == '-' || text[index] == '+') {
		negative = text[index] == '-'
		index++
	}
	if index >= len(text) {
		return 0, fmt.Errorf("无效整数")
	}
	for ; index < len(text); index++ {
		ch := text[index]
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("无效整数")
		}
		n = n*10 + int64(ch-'0')
	}
	if negative {
		n = -n
	}
	return n, nil
}