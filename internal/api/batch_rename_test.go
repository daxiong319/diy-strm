package api

import (
	"encoding/json"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/renamerule"
)

// TestBatchRenameKeepExtDefault 未传 keep_ext 时默认保留扩展名。
func TestBatchRenameKeepExtDefault(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name  string
		value *bool
		want  bool
	}{
		{"缺省默认为 true", nil, true},
		{"显式 true", &yes, true},
		{"显式 false", &no, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := batchRenameKeepExt(tc.value); got != tc.want {
				t.Fatalf("batchRenameKeepExt = %v, 期望 %v", got, tc.want)
			}
		})
	}
}

// TestValidateBatchRenameRules 规则数量与类型校验。
func TestValidateBatchRenameRules(t *testing.T) {
	tooMany := make([]renamerule.Rule, batchRenameMaxRules+1)
	for i := range tooMany {
		tooMany[i] = renamerule.Rule{Type: renamerule.TypeReplace}
	}
	cases := []struct {
		name      string
		rules     []renamerule.Rule
		wantIssue string
	}{
		{"空规则", nil, "rules 不能为空"},
		{"超出上限", tooMany, "单次最多支持"},
		{"未知类型", []renamerule.Rule{{Type: "nope"}}, "规则类型无效"},
		{"合法规则", []renamerule.Rule{{Type: renamerule.TypeReplace, Find: "a", Replace: "b"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBatchRenameRules(tc.rules)
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("期望无错误，实际 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIssue) {
				t.Fatalf("错误 = %v, 期望包含 %q", err, tc.wantIssue)
			}
		})
	}
}

// TestValidateBatchRenameItems 条目数量与必填字段校验。
func TestValidateBatchRenameItems(t *testing.T) {
	tooMany := make([]batchRenameItem, batchRenameMaxItems+1)
	for i := range tooMany {
		tooMany[i] = batchRenameItem{FileID: "1", Name: "a.mkv"}
	}
	cases := []struct {
		name      string
		items     []batchRenameItem
		wantIssue string
	}{
		{"空列表", nil, "items 不能为空"},
		{"超出上限", tooMany, "单次最多支持"},
		{"缺 file_id", []batchRenameItem{{Name: "a.mkv"}}, "file_id 不能为空"},
		{"缺 name", []batchRenameItem{{FileID: "1"}}, "name 不能为空"},
		{"合法条目", []batchRenameItem{{FileID: "1", Name: "a.mkv"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBatchRenameItems(tc.items)
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("期望无错误，实际 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIssue) {
				t.Fatalf("错误 = %v, 期望包含 %q", err, tc.wantIssue)
			}
		})
	}
}

// TestValidateBatchRenameName 新文件名合法性。
func TestValidateBatchRenameName(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		wantIssue string
	}{
		{"空名", "   ", "新文件名不能为空"},
		{"含斜杠", "a/b.mkv", "不能包含"},
		{"含反斜杠", `a\b.mkv`, "不能包含"},
		{"点", ".", "不能为 . 或 .."},
		{"双点", "..", "不能为 . 或 .."},
		{"合法", "剧集 01.mkv", ""},
		{"中文合法", "第一集.mkv", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBatchRenameName(tc.value)
			if tc.wantIssue == "" {
				if err != nil {
					t.Fatalf("期望无错误，实际 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIssue) {
				t.Fatalf("错误 = %v, 期望包含 %q", err, tc.wantIssue)
			}
		})
	}
}

// TestBatchRenameNormalizeParentID 空目录归一到根目录。
func TestBatchRenameNormalizeParentID(t *testing.T) {
	if got := batchRenameNormalizeParentID(""); got != "0" {
		t.Fatalf("空 parent = %q, 期望 \"0\"", got)
	}
	if got := batchRenameNormalizeParentID("abc"); got != "abc" {
		t.Fatalf("parent = %q, 期望 \"abc\"", got)
	}
}

// TestBatchRenameExistingNames 已有文件名包装。
func TestBatchRenameExistingNames(t *testing.T) {
	if got := batchRenameExistingNames(nil, "0"); got != nil {
		t.Fatalf("空列表应返回 nil，实际 %v", got)
	}
	got := batchRenameExistingNames([]string{"a.mkv", "b.mkv"}, "0")
	if len(got["0"]) != 2 || got["0"][0] != "a.mkv" {
		t.Fatalf("包装结果异常：%v", got)
	}
}

// TestBatchRenameStringSliceNilSafe errors 字段必须是数组而非 null。
func TestBatchRenameStringSliceNilSafe(t *testing.T) {
	if got := batchRenameStringSlice(nil); got == nil {
		t.Fatal("nil 应转换为空数组")
	}
	raw, err := json.Marshal(batchRenameStringSlice(nil))
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("序列化 = %s, 期望 []", raw)
	}
}

// TestBatchRenameRawJSON 损坏的 JSON 回退为空数组。
func TestBatchRenameRawJSON(t *testing.T) {
	if got := string(batchRenameRawJSON(nil)); got != "[]" {
		t.Fatalf("nil = %s, 期望 []", got)
	}
	if got := string(batchRenameRawJSON(json.RawMessage("{bad"))); got != "[]" {
		t.Fatalf("损坏 JSON = %s, 期望 []", got)
	}
	if got := string(batchRenameRawJSON(json.RawMessage(`[{"type":"replace"}]`))); got != `[{"type":"replace"}]` {
		t.Fatalf("合法 JSON 被改写：%s", got)
	}
}

// TestBatchRenameTargets 请求条目到规则引擎目标的映射。
func TestBatchRenameTargets(t *testing.T) {
	got := batchRenameTargets([]batchRenameItem{
		{FileID: "1", Name: "a.mkv", Type: 0, ParentID: "p"},
		{FileID: "2", Name: "目录", Type: 1, ParentID: "p"},
	})
	if len(got) != 2 {
		t.Fatalf("目标数 = %d, 期望 2", len(got))
	}
	if got[0].ID != "1" || got[0].Name != "a.mkv" || got[0].ParentID != "p" || got[0].IsDir() {
		t.Fatalf("文件目标映射异常：%+v", got[0])
	}
	if !got[1].IsDir() {
		t.Fatalf("目录目标 Type 未映射：%+v", got[1])
	}
}

// TestBatchRenamePreviewFlow 覆盖预览链路的核心语义：
// 只有产生变更的条目参与冲突校验，未命中规则的条目不应误报重名。
func TestBatchRenamePreviewFlow(t *testing.T) {
	rules := []renamerule.Rule{{Type: renamerule.TypeNumber, Position: "prefix", Start: "1", Digits: "2"}}
	items := []batchRenameItem{
		{FileID: "1", Name: "第一集.mkv", ParentID: "0"},
		{FileID: "2", Name: "第二集.mkv", ParentID: "0"},
	}
	rows := renamerule.Preview(batchRenameTargets(items), rules, true, "")

	changed := make([]renamerule.Target, 0, len(rows))
	for _, row := range rows {
		if row.Changed {
			changed = append(changed, row.Target)
		}
	}
	if len(changed) != 2 {
		t.Fatalf("变更数 = %d, 期望 2", len(changed))
	}
	if changed[0].NewName != "01第一集.mkv" || changed[1].NewName != "02第二集.mkv" {
		t.Fatalf("预览结果异常：%s / %s", changed[0].NewName, changed[1].NewName)
	}
	if issues := renamerule.ValidateTargets(changed, nil); len(issues) != 0 {
		t.Fatalf("不应有冲突，实际 %v", issues)
	}

	// 已有同名文件时必须报冲突
	if issues := renamerule.ValidateTargets(changed, batchRenameExistingNames([]string{"01第一集.mkv"}, "0")); len(issues) == 0 {
		t.Fatal("期望报出同目录已存在冲突")
	}
}

// TestBatchRenameApplyValidationRejectsBadName 应用请求中的非法新文件名应被拒绝。
func TestBatchRenameApplyValidationRejectsBadName(t *testing.T) {
	item := batchRenameApplyItem{FileID: "1", Name: "a.mkv", NewName: "bad/name.mkv"}
	if err := validateBatchRenameName(item.NewName); err == nil {
		t.Fatal("含斜杠的新文件名应被拒绝")
	}
	ae, ok := domain.AsAppError(validateBatchRenameName("bad/name.mkv"))
	if !ok || ae.Code != domain.CodeValidation {
		t.Fatalf("错误码异常：%v", ae)
	}
}
