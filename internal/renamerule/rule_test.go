package renamerule

import (
	"strings"
	"testing"
)

// mustApply 便捷断言。
func mustApply(t *testing.T, name string, index int, rules []Rule, keepExt bool, folderName string, isFolder bool) string {
	t.Helper()
	return Apply(name, index, rules, keepExt, folderName, isFolder)
}

func TestApplyKeepExtension(t *testing.T) {
	rules := []Rule{{
		Type:  TypeReplace,
		Find:  "旧",
		Replace: "新",
	}}
	if got := mustApply(t, "旧名.mkv", 0, rules, true, "", false); got != "新名.mkv" {
		t.Fatalf("保留扩展名失败：%s", got)
	}
	// 不保留扩展名：扩展名作为普通字符参与规则
	extRule := []Rule{{Type: TypeReplace, Find: ".mkv", Replace: ""}}
	if got := mustApply(t, "旧名.mkv", 0, extRule, false, "", false); got != "旧名" {
		t.Fatalf("不保留扩展名失败：%s", got)
	}
	// 目录名不保留扩展名
	if got := mustApply(t, "旧名.mkv", 0, rules, true, "", true); got != "新名.mkv" {
		t.Fatalf("目录未保留扩展名处理失败：%s", got)
	}
	// 隐藏文件 / 无扩展名
	if got := mustApply(t, "旧名", 0, rules, true, "", false); got != "新名" {
		t.Fatalf("无扩展名失败：%s", got)
	}
	if got := mustApply(t, ".旧", 0, rules, true, "", false); got != ".新" {
		t.Fatalf("隐藏文件失败：%s", got)
	}
}

func TestApplyReplace(t *testing.T) {
	base := []Rule{{Type: TypeReplace, Find: "abc", Replace: "X"}}
	if got := mustApply(t, "abcabc.mkv", 0, base, true, "", false); got != "XX.mkv" {
		t.Fatalf("全部替换失败：%s", got)
	}
	first := []Rule{{Type: TypeReplace, Find: "abc", Replace: "X", FirstOnly: true}}
	if got := mustApply(t, "abcabc.mkv", 0, first, true, "", false); got != "Xabc.mkv" {
		t.Fatalf("仅替换第一处失败：%s", got)
	}
	caseIns := []Rule{{Type: TypeReplace, Find: "ABC", Replace: "X"}}
	if got := mustApply(t, "abc.mkv", 0, caseIns, true, "", false); got != "X.mkv" {
		t.Fatalf("大小写不敏感替换失败：%s", got)
	}
	del := []Rule{{Type: TypeReplace, Find: "-", Replace: ""}}
	if got := mustApply(t, "ab-c.mkv", 0, del, true, "", false); got != "abc.mkv" {
		t.Fatalf("替换为空失败：%s", got)
	}
}

func TestApplyNumber(t *testing.T) {
	rules := []Rule{{Type: TypeNumber, Position: "prefix", Start: "1", Digits: "3", Prefix: "EP-"}}
	if got := mustApply(t, "a.mkv", 0, rules, true, "", false); got != "EP-001a.mkv" {
		t.Fatalf("前缀序号失败：%s", got)
	}
	rules[0].Position = "suffix"
	if got := mustApply(t, "a.mkv", 1, rules, true, "", false); got != "aEP-002.mkv" {
		t.Fatalf("后缀序号失败：%s", got)
	}
}

func TestApplySetName(t *testing.T) {
	rules := []Rule{{Type: TypeSetName, Pattern: "{name} {n}", Start: "5", Digits: "2"}}
	if got := mustApply(t, "剧", 0, rules, true, "", false); got != "剧 05" {
		t.Fatalf("名称模板失败：%s", got)
	}
	rules[0].Pattern = "{n}（{.none}）{name}"
}

func TestApplyInsertDeleteMove(t *testing.T) {
	insertEnd := []Rule{{Type: TypeAdd, Position: "end", Text: "+"}}
	if got := mustApply(t, "ab.mkv", 0, insertEnd, true, "", false); got != "ab+.mkv" {
		t.Fatalf("尾部插入失败：%s", got)
	}
	insertPos := []Rule{{Type: TypeAdd, Position: "index", Text: "X", Index: "2"}}
	if got := mustApply(t, "abc.mkv", 0, insertPos, true, "", false); got != "aXbc.mkv" {
		t.Fatalf("指定位置插入失败：%s", got)
	}
	deleteRange := []Rule{{Type: TypeDelete, Mode: "range", Start: "2", Length: "2"}}
	if got := mustApply(t, "abcd.mkv", 0, deleteRange, true, "", false); got != "ad.mkv" {
		t.Fatalf("区间删除失败：%s", got)
	}
	deleteText := []Rule{{Type: TypeDelete, Text: "ab"}}
	if got := mustApply(t, "abab.mkv", 0, deleteText, true, "", false); got != ".mkv" {
		t.Fatalf("字符删除失败：%s", got)
	}
	move := []Rule{{Type: TypeMove, Start: "2", Length: "2", To: "1"}}
	if got := mustApply(t, "abcd.mkv", 0, move, true, "", false); got != "bcad.mkv" {
		t.Fatalf("区间移动失败：%s", got)
	}
}

func TestApplyCaseSpaceWidth(t *testing.T) {
	upper := []Rule{{Type: TypeCase, Mode: "upper"}}
	if got := mustApply(t, "abc.mkv", 0, upper, true, "", false); got != "ABC.mkv" {
		t.Fatalf("大写失败：%s", got)
	}
	title := []Rule{{Type: TypeCase, Mode: "title"}}
	if got := mustApply(t, "hello world.mkv", 0, title, true, "", false); got != "Hello World.mkv" {
		t.Fatalf("标题化失败：%s", got)
	}
	collapse := []Rule{{Type: TypeSpace, Mode: "collapse"}}
	if got := mustApply(t, "a  b.mkv", 0, collapse, true, "", false); got != "a b.mkv" {
		t.Fatalf("合并空格失败：%s", got)
	}
	all := []Rule{{Type: TypeSpace, Mode: "all"}}
	if got := mustApply(t, "a b.mkv", 0, all, true, "", false); got != "ab.mkv" {
		t.Fatalf("删除空格失败：%s", got)
	}
	half := []Rule{{Type: TypeWidth, Mode: "half"}}
	if got := mustApply(t, "ＡＢＣ.mkv", 0, half, true, "", false); got != "ABC.mkv" {
		t.Fatalf("转半角失败：%s", got)
	}
	full := []Rule{{Type: TypeWidth, Mode: "full"}}
	if got := mustApply(t, "ABC.mkv", 0, full, true, "", false); got != "ＡＢＣ.mkv" {
		t.Fatalf("转全角失败：%s", got)
	}
}

func TestApplyFolderAndRegex(t *testing.T) {
	folder := []Rule{{Type: TypeFolder, Position: "suffix", Separator: "-", FolderName: "剧集"}}
	if got := mustApply(t, "a.mkv", 0, folder, true, "", false); got != "a-剧集.mkv" {
		t.Fatalf("文件夹名后缀失败：%s", got)
	}
	folder[0].Position = "prefix"
	folder[0].FolderName = ""
	if got := mustApply(t, "a.mkv", 0, folder, true, "当前目录", false); got != "当前目录-a.mkv" {
		t.Fatalf("文件夹名 fallback 失败：%s", got)
	}
	regex := []Rule{{Type: TypeRegex, Pattern: `^(.+?)\.(\d+)$`, Replace: "$2-$1"}}
	if got := mustApply(t, "abc.12.mkv", 0, regex, true, "", false); got != "12-abc.mkv" {
		t.Fatalf("正则重命名失败：%s", got)
	}
}

func TestUnicodeHandling(t *testing.T) {
	rules := []Rule{{Type: TypeMove, Start: "1", Length: "1", To: "3"}}
	if got := mustApply(t, "😀ab.mkv", 0, rules, true, "", false); got != "ab😀.mkv" {
		t.Fatalf("Unicode 移动失败：%s", got)
	}
}

func TestValidateRules(t *testing.T) {
	if errs := ValidateRules([]Rule{{Type: TypeRegex, Pattern: "("}}); len(errs) == 0 {
		t.Fatal("无效正则未报错")
	}
	if errs := ValidateRules([]Rule{{Type: TypeNumber, Start: "x", Digits: "0"}}); len(errs) == 0 {
		t.Fatal("无效序号未报错")
	}
	if errs := ValidateRules([]Rule{{Type: TypeSetName, Pattern: ""}}); len(errs) == 0 {
		t.Fatal("空模板未报错")
	}
	if errs := ValidateRules([]Rule{{Type: TypeReplace, Find: "a"}}); len(errs) != 0 {
		t.Fatalf("合法规则误报错：%v", errs)
	}
}

func TestValidateTargets(t *testing.T) {
	targets := []Target{
		{ID: "1", Name: "a.mkv", NewName: "b.mkv", ParentID: "0"},
		{ID: "2", Name: "b.mkv", NewName: "a.mkv", ParentID: "0"},
	}
	if errs := ValidateTargets(targets, nil); len(errs) == 0 {
		t.Fatal("交换冲突未报错")
	}
	targets = []Target{
		{ID: "1", Name: "a.mkv", NewName: "c.mkv", ParentID: "0"},
	}
	if errs := ValidateTargets(targets, map[string][]string{"0": {"c.mkv"}}); len(errs) == 0 {
		t.Fatal("同目录已存在未报错")
	}
	targets = []Target{
		{ID: "1", Name: "a.mkv", NewName: "a.mkv", ParentID: "0"},
	}
	if errs := ValidateTargets(targets, map[string][]string{"0": {"a.mkv"}}); len(errs) != 0 {
		t.Fatalf("自身未变化误报错：%v", errs)
	}
	targets = []Target{
		{ID: "1", Name: "a.mkv", NewName: strings.Repeat("长", 256) + ".mkv", ParentID: "0"},
	}
	if errs := ValidateTargets(targets, nil); len(errs) == 0 {
		t.Fatal("超长文件名未报错")
	}
	targets = []Target{
		{ID: "1", Name: "a.mkv", NewName: "b/c.mkv", ParentID: "0"},
	}
	if errs := ValidateTargets(targets, nil); len(errs) == 0 {
		t.Fatal("非法字符未报错")
	}
}

func TestPreview(t *testing.T) {
	targets := []Target{
		{ID: "1", Name: "第一集.mkv"},
		{ID: "2", Name: "第二集.mkv"},
	}
	rules := []Rule{{Type: TypeNumber, Position: "prefix", Start: "1", Digits: "2"}}
	rows := Preview(targets, rules, true, "")
	if len(rows) != 2 {
		t.Fatalf("预览行数错误")
	}
	if !rows[0].Changed || rows[0].Target.NewName != "01第一集.mkv" {
		t.Fatalf("预览结果错误：%+v", rows[0])
	}
	if !rows[1].Changed || rows[1].Target.NewName != "02第二集.mkv" {
		t.Fatalf("预览结果错误：%+v", rows[1])
	}
	if rows[0].Target.ID != "1" || rows[0].Target.Name != "第一集.mkv" {
		t.Fatalf("预览目标字段丢失")
	}
}