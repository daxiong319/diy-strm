package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"litepan/internal/domain"
)

// TestRenameHistoryLifecycle 历史记录的写入、列出、读取、更新与删除。
func TestRenameHistoryLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rules := json.RawMessage(`[{"type":"replace","find":"a","replace":"b"}]`)
	targets := json.RawMessage(`[{"file_id":"1","name":"b.mkv","new_name":"a.mkv","parent_id":"0"}]`)

	history := &domain.RenameHistory{
		UserID:      0,
		Name:        "批量重命名",
		Rules:       rules,
		KeepExt:     true,
		Targets:     targets,
		ItemCount:   3,
		ChangeCount: 2,
	}
	if err := s.Renames.CreateHistory(ctx, history); err != nil {
		t.Fatalf("create history: %v", err)
	}
	if history.ID == 0 {
		t.Fatal("期望回填自增 ID")
	}

	items, err := s.Renames.ListHistories(ctx, 0, 80)
	if err != nil {
		t.Fatalf("list histories: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("历史条数 = %d, 期望 1", len(items))
	}
	got := items[0]
	if got.Name != "批量重命名" || got.ItemCount != 3 || got.ChangeCount != 2 || !got.KeepExt {
		t.Fatalf("字段往返失败：%+v", got)
	}
	if string(got.Rules) != string(rules) {
		t.Fatalf("规则往返失败：%s", got.Rules)
	}
	if string(got.Targets) != string(targets) {
		t.Fatalf("目标往返失败：%s", got.Targets)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("期望写入创建时间")
	}

	loaded, err := s.Renames.GetHistory(ctx, history.ID, 0)
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	if loaded.ID != history.ID {
		t.Fatalf("ID = %d, 期望 %d", loaded.ID, history.ID)
	}

	// 回滚后只剩余部分条目
	remaining := json.RawMessage(`[{"file_id":"2","name":"y.mkv","new_name":"x.mkv","parent_id":"0"}]`)
	if err := s.Renames.UpdateHistoryTargets(ctx, history.ID, remaining, 1); err != nil {
		t.Fatalf("update targets: %v", err)
	}
	updated, err := s.Renames.GetHistory(ctx, history.ID, 0)
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	if updated.ChangeCount != 1 || string(updated.Targets) != string(remaining) {
		t.Fatalf("更新未生效：%+v", updated)
	}

	// 空目标应落为 []，避免回滚时反序列化失败
	if err := s.Renames.UpdateHistoryTargets(ctx, history.ID, nil, 0); err != nil {
		t.Fatalf("update empty targets: %v", err)
	}
	emptied, err := s.Renames.GetHistory(ctx, history.ID, 0)
	if err != nil {
		t.Fatalf("reload emptied history: %v", err)
	}
	var decoded []any
	if err := json.Unmarshal(emptied.Targets, &decoded); err != nil {
		t.Fatalf("空目标不是合法 JSON：%s", emptied.Targets)
	}

	if err := s.Renames.DeleteHistory(ctx, history.ID, 0); err != nil {
		t.Fatalf("delete history: %v", err)
	}
	items, err = s.Renames.ListHistories(ctx, 0, 80)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后仍有 %d 条", len(items))
	}
}

// TestRenameHistoryGetMissing 不存在的历史返回 CodeNotFound。
func TestRenameHistoryGetMissing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.Renames.GetHistory(ctx, 999, 0)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	ae, ok := domain.AsAppError(err)
	if !ok || ae.Code != domain.CodeNotFound {
		t.Fatalf("错误码 = %v, 期望 CodeNotFound", err)
	}
}

// TestRenameHistoryListOrderAndLimit 倒序返回且 limit 归一化。
func TestRenameHistoryListOrderAndLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		if err := s.Renames.CreateHistory(ctx, &domain.RenameHistory{
			Name:    "批量重命名",
			Rules:   json.RawMessage(`[]`),
			Targets: json.RawMessage(`[]`),
			KeepExt: true,
		}); err != nil {
			t.Fatalf("create history %d: %v", i, err)
		}
	}
	items, err := s.Renames.ListHistories(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list histories: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("条数 = %d, 期望 3", len(items))
	}
	if items[0].ID < items[1].ID || items[1].ID < items[2].ID {
		t.Fatalf("期望按 id 倒序，实际 %d,%d,%d", items[0].ID, items[1].ID, items[2].ID)
	}
}

// TestRenamePresetLifecycle 常用组合的保存、同名覆盖、使用次数与删除。
func TestRenamePresetLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rules := json.RawMessage(`[{"type":"number","position":"prefix"}]`)
	preset := &domain.RenamePreset{UserID: 0, Name: "加序号", Rules: rules, KeepExt: true}
	if err := s.Renames.CreatePreset(ctx, preset); err != nil {
		t.Fatalf("create preset: %v", err)
	}
	if preset.ID == 0 {
		t.Fatal("期望回填自增 ID")
	}

	// 同名覆盖：ID 不变，规则更新
	nextRules := json.RawMessage(`[{"type":"case","mode":"upper"}]`)
	overwritten := &domain.RenamePreset{UserID: 0, Name: "加序号", Rules: nextRules, KeepExt: false}
	if err := s.Renames.CreatePreset(ctx, overwritten); err != nil {
		t.Fatalf("overwrite preset: %v", err)
	}
	if overwritten.ID != preset.ID {
		t.Fatalf("同名覆盖后 ID = %d, 期望 %d", overwritten.ID, preset.ID)
	}

	items, err := s.Renames.ListPresets(ctx, 0)
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("组合条数 = %d, 期望 1（同名应覆盖而非新增）", len(items))
	}
	if string(items[0].Rules) != string(nextRules) || items[0].KeepExt {
		t.Fatalf("覆盖未生效：%+v", items[0])
	}
	if items[0].UseCount != 0 {
		t.Fatalf("UseCount = %d, 期望保留 0", items[0].UseCount)
	}

	// 使用次数累加只命中规则与扩展名都相同的行
	if err := s.Renames.IncrementPresetUse(ctx, 0, nextRules, false); err != nil {
		t.Fatalf("increment use: %v", err)
	}
	if err := s.Renames.IncrementPresetUse(ctx, 0, nextRules, true); err != nil {
		t.Fatalf("increment use mismatched keep_ext: %v", err)
	}
	items, err = s.Renames.ListPresets(ctx, 0)
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if items[0].UseCount != 1 {
		t.Fatalf("UseCount = %d, 期望 1", items[0].UseCount)
	}

	if err := s.Renames.DeletePreset(ctx, preset.ID, 0); err != nil {
		t.Fatalf("delete preset: %v", err)
	}
	items, err = s.Renames.ListPresets(ctx, 0)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后仍有 %d 条", len(items))
	}
}

// TestRenamePresetDeleteMissing 删除不存在的组合返回 CodeNotFound。
func TestRenamePresetDeleteMissing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.Renames.DeletePreset(ctx, 42, 0)
	if err == nil {
		t.Fatal("期望返回错误")
	}
	ae, ok := domain.AsAppError(err)
	if !ok || ae.Code != domain.CodeNotFound {
		t.Fatalf("错误码 = %v, 期望 CodeNotFound", err)
	}
}

// TestRenameUserScoped 不同 user_id 之间互不可见（当前恒为 0，保留隔离语义）。
func TestRenameUserScoped(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Renames.CreateHistory(ctx, &domain.RenameHistory{
		UserID: 7, Name: "他人历史", Rules: json.RawMessage(`[]`),
		Targets: json.RawMessage(`[]`), KeepExt: true,
	}); err != nil {
		t.Fatalf("create history: %v", err)
	}
	items, err := s.Renames.ListHistories(ctx, 0, 80)
	if err != nil {
		t.Fatalf("list histories: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("user 0 不应看到 user 7 的历史，实际 %d 条", len(items))
	}
	if _, err := s.Renames.GetHistory(ctx, 1, 0); err == nil {
		t.Fatal("跨用户读取应失败")
	}
}
