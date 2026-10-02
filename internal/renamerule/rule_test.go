package renamerule

import (
	"strings"
	"testing"
)

// TestApplyKeepExtension 扩展名拆分与保留。
func TestApplyKeepExtension(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		keepExt  bool
		isFolder bool
		rule     Rule
		want     string
	}{
		{
			name:     "保留扩展名",
			fileName: "a.mkv",
			keepExt:  true,
			rule:     Rule{Type: TypeAdd, Position: "start", Text: "剧-"},
			want:     "剧-a.mkv",
		},
		{
			name:     "不保留扩展名时扩展名参与规则",
			fileName: "a.mkv",
			keepExt:  false,
			rule:     Rule{Type: TypeAdd, Position: "start", Text: "剧-"},
			want:     "剧-a.mkv",
		},
		{
			name:     "目录不拆扩展名",
			fileName: "剧集.S01.mkv",
			keepExt:  true,
			isFolder: true,
			rule:     Rule{Type: TypeAdd, Position: "start", Text: "X"},
			want:     "X剧集.S01.mkv",
		},
		{
			name:     "无扩展名",
			fileName: "README",
			keepExt:  true,
			rule:     Rule{Type: TypeAdd, Position: "end", Text: "!"},
			want:     "README!",
		},
		{
			name:     "隐藏文件整体视为名称",
			fileName: ".旧",
			keepExt:  true,
			rule:     Rule{Type: TypeReplace, Find: "旧", Replace: "新", CaseSensitive: true},
			want:     ".新",
		},
		{
			name:     "结尾的点不视为扩展名",
			fileName: "abc.",
			keepExt:  true,
			rule:     Rule{Type: TypeMove, Start: "1", Length: "1", To: "2"},
			want:     "bac.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.fileName, 0, []Rule{tc.rule}, tc.keepExt, "", tc.isFolder)
			if got != tc.want {
				t.Fatalf("Apply(%q) = %q, 期望 %q", tc.fileName, got, tc.want)
			}
		})
	}
}

// TestApplyReplace 查找替换规则。
func TestApplyReplace(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		want string
	}{
		{
			name: "全部替换",
			rule: Rule{Type: TypeReplace, Find: "a", Replace: "b", CaseSensitive: true},
			want: "bbbc.mkv",
		},
		{
			name: "仅替换首个",
			rule: Rule{Type: TypeReplace, Find: "a", Replace: "b", CaseSensitive: true, FirstOnly: true},
			want: "babc.mkv",
		},
		{
			name: "大小写不敏感",
			rule: Rule{Type: TypeReplace, Find: "A", Replace: "x"},
			want: "xxbc.mkv",
		},
		{
			name: "替换为空即删除",
			rule: Rule{Type: TypeReplace, Find: "ab", Replace: "", CaseSensitive: true},
			want: "ac.mkv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply("aabc.mkv", 0, []Rule{tc.rule}, true, "", false)
			if got != tc.want {
				t.Fatalf("Apply = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestApplyNumber 修改名称/添加序号规则。
func TestApplyNumber(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		want string
	}{
		{
			name: "前缀补零",
			rule: Rule{Type: TypeNumber, Position: "prefix", Prefix: "EP-", Start: "1", Digits: "3"},
			want: "EP-002a.mkv",
		},
		{
			name: "后缀按序号递增",
			rule: Rule{Type: TypeNumber, Position: "suffix", Prefix: "EP-", Start: "1", Digits: "3"},
			want: "aEP-002.mkv",
		},
		{
			name: "整体替换为序号",
			rule: Rule{Type: TypeNumber, Position: "replace", Start: "10", Digits: "2"},
			want: "11.mkv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply("a.mkv", 1, []Rule{tc.rule}, true, "", false)
			if got != tc.want {
				t.Fatalf("Apply = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestApplySetName 名称模板规则。
func TestApplySetName(t *testing.T) {
	rule := Rule{Type: TypeSetName, Pattern: "{name} {n}", Start: "5", Digits: "2"}
	got := Apply("剧.mkv", 0, []Rule{rule}, true, "", false)
	if got != "剧 05.mkv" {
		t.Fatalf("Apply = %q, 期望 %q", got, "剧 05.mkv")
	}
}

// TestApplyInsertDeleteMove 添加字符 / 删除字符 / 移动字符。
func TestApplyInsertDeleteMove(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		rule     Rule
		want     string
	}{
		{
			name:     "末尾添加分隔符",
			fileName: "abc",
			rule:     Rule{Type: TypeSeparator, Position: "end", Text: "+"},
			want:     "abc+",
		},
		{
			name:     "指定位置插入",
			fileName: "abc",
			rule:     Rule{Type: TypeAdd, Position: "index", Text: "X", Index: "2"},
			want:     "aXbc",
		},
		{
			name:     "按范围删除",
			fileName: "abcd",
			rule:     Rule{Type: TypeDelete, Mode: "range", Start: "2", Length: "2"},
			want:     "ad",
		},
		{
			name:     "按文本删除",
			fileName: "abab",
			rule:     Rule{Type: TypeDelete, Mode: "text", Text: "ab"},
			want:     "",
		},
		{
			name:     "移动字符",
			fileName: "abcd",
			rule:     Rule{Type: TypeMove, Start: "2", Length: "2", To: "1"},
			want:     "bcad",
		},
		{
			name:     "越界移动原样返回",
			fileName: "ab",
			rule:     Rule{Type: TypeMove, Start: "9", Length: "1", To: "1"},
			want:     "ab",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.fileName, 0, []Rule{tc.rule}, false, "", false)
			if got != tc.want {
				t.Fatalf("Apply = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestApplyCaseSpaceWidth 大小写 / 空格 / 全角半角。
func TestApplyCaseSpaceWidth(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		rule     Rule
		want     string
	}{
		{"转大写", "abc", Rule{Type: TypeCase, Mode: "upper"}, "ABC"},
		{"转小写", "ABC", Rule{Type: TypeCase, Mode: "lower"}, "abc"},
		{"标题大小写", "hello world", Rule{Type: TypeCase, Mode: "title"}, "Hello World"},
		{"合并多余空格", "a  b", Rule{Type: TypeSpace, Mode: "collapse"}, "a b"},
		{"删除全部空格", "a b", Rule{Type: TypeSpace, Mode: "all"}, "ab"},
		{"去除首尾空格", "  a  ", Rule{Type: TypeSpace, Mode: "trim"}, "a"},
		{"全角转半角", "ＡＢＣ", Rule{Type: TypeWidth, Mode: "half"}, "ABC"},
		{"半角转全角", "ABC", Rule{Type: TypeWidth, Mode: "full"}, "ＡＢＣ"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.fileName, 0, []Rule{tc.rule}, false, "", false)
			if got != tc.want {
				t.Fatalf("Apply = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestApplyFolderAndRegex 添加文件夹名与正则重命名。
func TestApplyFolderAndRegex(t *testing.T) {
	tests := []struct {
		name       string
		fileName   string
		rule       Rule
		folderName string
		want       string
	}{
		{
			name:     "文件夹名作为后缀",
			fileName: "a.mkv",
			rule:     Rule{Type: TypeFolder, Position: "suffix", Separator: "-", FolderName: "剧集"},
			want:     "a-剧集.mkv",
		},
		{
			name:       "文件夹名为空时回退到当前目录名",
			fileName:   "a.mkv",
			rule:       Rule{Type: TypeFolder, Position: "prefix", Separator: "-"},
			folderName: "当前目录",
			want:       "当前目录-a.mkv",
		},
		{
			name:     "正则重命名",
			fileName: "abc.12.mkv",
			rule:     Rule{Type: TypeRegex, Pattern: `^(.+?)\.(\d+)$`, Replace: "$2-$1"},
			want:     "12-abc.mkv",
		},
		{
			name:     "无效正则原样返回",
			fileName: "abc.mkv",
			rule:     Rule{Type: TypeRegex, Pattern: "(", Replace: "x"},
			want:     "abc.mkv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.fileName, 0, []Rule{tc.rule}, true, tc.folderName, false)
			if got != tc.want {
				t.Fatalf("Apply = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestUnicodeHandling 多字节字符按 rune 处理。
func TestUnicodeHandling(t *testing.T) {
	rule := Rule{Type: TypeMove, Start: "1", Length: "1", To: "3"}
	got := Apply("😀ab.mkv", 0, []Rule{rule}, true, "", false)
	if got != "ab😀.mkv" {
		t.Fatalf("Apply = %q, 期望 %q", got, "ab😀.mkv")
	}
}

// TestValidateRules 规则校验。
func TestValidateRules(t *testing.T) {
	tests := []struct {
		name      string
		rules     []Rule
		wantIssue string
	}{
		{
			name:      "无效正则",
			rules:     []Rule{{Type: TypeRegex, Pattern: "("}},
			wantIssue: "正则表达式无效",
		},
		{
			name:      "起始编号无效",
			rules:     []Rule{{Type: TypeNumber, Start: "x", Digits: "0"}},
			wantIssue: "起始编号无效",
		},
		{
			name:      "位数为零",
			rules:     []Rule{{Type: TypeNumber, Start: "1", Digits: "0"}},
			wantIssue: "位数必须大于 0",
		},
		{
			name:      "名称模板为空",
			rules:     []Rule{{Type: TypeSetName, Pattern: "  ", Start: "1", Digits: "2"}},
			wantIssue: "名称模板不能为空",
		},
		{
			name:      "移动规则起始位置非法",
			rules:     []Rule{{Type: TypeMove, Start: "0", Length: "1", To: "1"}},
			wantIssue: "起始位置必须从 1 开始",
		},
		{
			name:  "合规规则不报错",
			rules: []Rule{{Type: TypeReplace, Find: "a", Replace: "b"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateRules(tc.rules)
			if tc.wantIssue == "" {
				if len(issues) != 0 {
					t.Fatalf("期望无错误，实际 %v", issues)
				}
				return
			}
			if len(issues) == 0 {
				t.Fatalf("期望包含 %q 的错误，实际无错误", tc.wantIssue)
			}
			joined := strings.Join(issues, " | ")
			if !strings.Contains(joined, tc.wantIssue) {
				t.Fatalf("错误 %q 未包含 %q", joined, tc.wantIssue)
			}
		})
	}
}

// TestValidateTargets 目标校验。
func TestValidateTargets(t *testing.T) {
	tests := []struct {
		name      string
		targets   []Target
		existing  map[string][]string
		wantIssue string
	}{
		{
			name: "交换冲突",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "b.mkv", ParentID: "0"},
				{ID: "2", Name: "b.mkv", NewName: "a.mkv", ParentID: "0"},
			},
			wantIssue: "交换冲突",
		},
		{
			name: "同一目录已存在",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "c.mkv", ParentID: "0"},
			},
			existing:  map[string][]string{"0": {"c.mkv"}},
			wantIssue: "同一目录已存在",
		},
		{
			name: "自身未变化不报错",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "a.mkv", ParentID: "0"},
			},
			existing: map[string][]string{"0": {"a.mkv"}},
		},
		{
			name: "重名",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "x.mkv", ParentID: "0"},
				{ID: "2", Name: "b.mkv", NewName: "x.mkv", ParentID: "0"},
			},
			wantIssue: "重复的新文件名",
		},
		{
			name: "超长文件名",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: strings.Repeat("长", 256), ParentID: "0"},
			},
			wantIssue: "255 个字符",
		},
		{
			name: "非法字符",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "a/b.mkv", ParentID: "0"},
			},
			wantIssue: "不能包含",
		},
		{
			name: "空文件名",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "   ", ParentID: "0"},
			},
			wantIssue: "空文件名",
		},
		{
			name: "不同目录互不干扰",
			targets: []Target{
				{ID: "1", Name: "a.mkv", NewName: "x.mkv", ParentID: "0"},
				{ID: "2", Name: "b.mkv", NewName: "x.mkv", ParentID: "9"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateTargets(tc.targets, tc.existing)
			if tc.wantIssue == "" {
				if len(issues) != 0 {
					t.Fatalf("期望无错误，实际 %v", issues)
				}
				return
			}
			if len(issues) == 0 {
				t.Fatalf("期望包含 %q 的错误，实际无错误", tc.wantIssue)
			}
			joined := strings.Join(issues, " | ")
			if !strings.Contains(joined, tc.wantIssue) {
				t.Fatalf("错误 %q 未包含 %q", joined, tc.wantIssue)
			}
		})
	}
}

// TestPreview 预览：序号按行递增，且保留原始目标字段。
func TestPreview(t *testing.T) {
	targets := []Target{
		{ID: "1", Name: "第一集.mkv", Type: 0, ParentID: "0"},
		{ID: "2", Name: "第二集.mkv", Type: 0, ParentID: "0"},
	}
	rules := []Rule{{Type: TypeNumber, Position: "prefix", Prefix: "", Start: "1", Digits: "2"}}

	rows := Preview(targets, rules, true, "")
	if len(rows) != 2 {
		t.Fatalf("期望 2 行，实际 %d", len(rows))
	}
	if rows[0].Target.NewName != "01第一集.mkv" {
		t.Fatalf("第 1 行 = %q", rows[0].Target.NewName)
	}
	if rows[1].Target.NewName != "02第二集.mkv" {
		t.Fatalf("第 2 行 = %q", rows[1].Target.NewName)
	}
	if !rows[0].Changed || !rows[1].Changed {
		t.Fatalf("期望两行都发生变化")
	}
	if rows[0].Target.ID != "1" || rows[0].Target.Name != "第一集.mkv" || rows[0].Target.ParentID != "0" {
		t.Fatalf("原始字段未保留：%+v", rows[0].Target)
	}
}

// TestPreviewUnchanged 未命中的规则不产生变更标记。
func TestPreviewUnchanged(t *testing.T) {
	targets := []Target{{ID: "1", Name: "a.mkv"}}
	rows := Preview(targets, []Rule{{Type: TypeReplace, Find: "zzz", Replace: "y", CaseSensitive: true}}, true, "")
	if len(rows) != 1 || rows[0].Changed {
		t.Fatalf("期望无变更，实际 %+v", rows)
	}
}

// TestDefaults 默认值与前端下拉保持一致。
func TestDefaults(t *testing.T) {
	tests := []struct {
		ruleType string
		check    func(Rule) bool
	}{
		{TypeFolder, func(r Rule) bool { return r.Position == "prefix" && r.Separator == "-" }},
		{TypeSetName, func(r Rule) bool { return r.Pattern == "{name}" && r.Start == "1" && r.Digits == "2" }},
		{TypeNumber, func(r Rule) bool { return r.Position == "replace" && r.Start == "1" && r.Digits == "2" }},
		{TypeSeparator, func(r Rule) bool { return r.Position == "end" && r.Index == "1" }},
		{TypeDelete, func(r Rule) bool { return r.Mode == "text" && r.Start == "1" && r.Length == "1" }},
		{TypeMove, func(r Rule) bool { return r.Start == "1" && r.Length == "1" && r.To == "1" }},
		{TypeCase, func(r Rule) bool { return r.Mode == "upper" }},
		{TypeSpace, func(r Rule) bool { return r.Mode == "trim" }},
		{TypeWidth, func(r Rule) bool { return r.Mode == "half" }},
	}

	for _, tc := range tests {
		t.Run(tc.ruleType, func(t *testing.T) {
			rule := Defaults(tc.ruleType)
			if rule.Type != tc.ruleType {
				t.Fatalf("类型 = %q, 期望 %q", rule.Type, tc.ruleType)
			}
			if !tc.check(rule) {
				t.Fatalf("默认值不符合预期：%+v", rule)
			}
		})
	}
}

// TestIsValidType 类型白名单。
func TestIsValidType(t *testing.T) {
	if !IsValidType(TypeReplace) {
		t.Fatal("replace 应为合法类型")
	}
	if IsValidType("unknown") {
		t.Fatal("unknown 不应为合法类型")
	}
	if len(RuleTypes) != 12 {
		t.Fatalf("规则类型数量 = %d, 期望 12", len(RuleTypes))
	}
	if TypeLabel("unknown") != "批量重命名" {
		t.Fatalf("未知类型标签 = %q", TypeLabel("unknown"))
	}
}
