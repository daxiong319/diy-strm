package executor_test

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/executor"
	"litepan/internal/mediaorganize/moplan"
)

// sizedExecFS 给 Info 真实体积，用来驱动小文件判定。
type sizedExecFS struct {
	mockExecFS
	sizes map[string]int64
	// moved 记录 (源目录, 目标目录, 文件ID) 三元组，用来断言「搬去哪了」。
	moved []string
}

func (m *sizedExecFS) Info(_ context.Context, _ int64, fileID string) (*domain.FileItem, error) {
	for _, items := range m.dirs {
		for i := range items {
			if items[i].ID == fileID {
				cp := items[i]
				cp.Size = m.sizes[fileID]
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (m *sizedExecFS) MoveFiles(ctx context.Context, accountID int64, fileIDs []string, targetParentID, sourceParentID string) error {
	for _, id := range fileIDs {
		m.moved = append(m.moved, sourceParentID+"->"+targetParentID+":"+id)
	}
	return m.mockExecFS.MoveFiles(ctx, accountID, fileIDs, targetParentID, sourceParentID)
}

func quarantinePlan() *moplan.Plan {
	return &moplan.Plan{
		TaskID:       "q1",
		TargetRootID: "root",
		Actions: []moplan.PlanAction{
			{
				ID: "a1", Kind: moplan.ActionKindRelocate,
				SourceID: "small", SourceName: "tiny.mkv",
				SourceParentID: "root", TargetParentID: "root", TargetName: "tiny.mkv",
			},
			{
				ID: "a2", Kind: moplan.ActionKindRelocate,
				SourceID: "big", SourceName: "movie.mkv",
				SourceParentID: "root", TargetParentID: "root", TargetName: "movie.mkv",
			},
		},
	}
}

func newSizedFS() *sizedExecFS {
	return &sizedExecFS{
		mockExecFS: mockExecFS{dirs: map[string][]domain.FileItem{
			"root": {
				{ID: "small", Name: "tiny.mkv", Size: 1024},
				{ID: "big", Name: "movie.mkv", Size: 5 << 30},
			},
		}},
		sizes: map[string]int64{"small": 1024, "big": 5 << 30},
	}
}

// 验收 ⑤：move 模式 + 已确认 → 小文件被移进隔离目录而不是删除，
// 且不进入 relocate 流程。
func TestQuarantineMovesSmallFileToQuarantineDir(t *testing.T) {
	fs := newSizedFS()
	plan := quarantinePlan()
	ex := executor.New(context.Background(), fs, plan, 1, false, nil, nil)
	ex.SetMoveMode(true)
	ex.SetQuarantine(executor.QuarantinePolicy{Enabled: true, MinBytes: 10 << 20, DirName: "_隔离"})
	if _, err := ex.Apply(); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if len(fs.moved) != 1 || !strings.HasPrefix(fs.moved[0], "root->root/_隔离:") {
		t.Fatalf("小文件应被移进隔离目录，实际移动记录: %#v", fs.moved)
	}
	if !strings.Contains(plan.Actions[0].Error, "隔离目录") {
		t.Fatalf("动作备注未说明隔离去向: %q", plan.Actions[0].Error)
	}
	// 隔离目录里必须还留着这个文件 —— 「可恢复」就是这里成立的。
	var found bool
	for _, item := range fs.dirs["root/_隔离"] {
		if item.ID == "small" {
			found = true
		}
	}
	if !found {
		t.Fatal("小文件没有留在隔离目录里，无法恢复")
	}
	// 大文件不受影响：它的备注里不该出现隔离相关字样。
	// 这里不断言 Status —— 同名文件本来就可能被标记为「已是目标名」，
	// 把无关的跳过原因算成失败会让这条用例在别处重构时假红。
	if strings.Contains(plan.Actions[1].Error, "隔离") || strings.Contains(plan.Actions[1].Error, "最小体积") {
		t.Fatalf("大文件被误隔离: %q", plan.Actions[1].Error)
	}
}

// 未勾知情确认 → 只报告，一个字节都不动。
func TestQuarantineReportOnlyDoesNotMove(t *testing.T) {
	fs := newSizedFS()
	plan := quarantinePlan()
	ex := executor.New(context.Background(), fs, plan, 1, false, nil, nil)
	ex.SetMoveMode(true)
	ex.SetQuarantine(executor.QuarantinePolicy{
		Enabled: true, ReportOnly: true, MinBytes: 10 << 20, DirName: "_隔离",
	})
	if _, err := ex.Apply(); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if len(fs.moved) != 0 {
		t.Fatalf("未确认时不应移动任何文件: %#v", fs.moved)
	}
	if _, ok := fs.dirs["root/_隔离"]; ok {
		t.Fatal("未确认时不该创建隔离目录")
	}
	if !strings.Contains(plan.Actions[0].Error, "未确认隔离") {
		t.Fatalf("动作备注未说明只报告: %q", plan.Actions[0].Error)
	}
	// 只报告的项仍要被记进诊断，否则用户看不到「会命中什么」。
	if _, ok := plan.Diagnostics["small_files"]; !ok {
		t.Fatalf("诊断里缺少小文件清单: %#v", plan.Diagnostics)
	}
}

// rename 模式不隔离：文件本来就不该被搬走。
func TestQuarantineDisabledInRenameMode(t *testing.T) {
	fs := newSizedFS()
	plan := quarantinePlan()
	ex := executor.New(context.Background(), fs, plan, 1, false, nil, nil)
	ex.SetMoveMode(false)
	ex.SetQuarantine(executor.QuarantinePolicy{Enabled: true, MinBytes: 10 << 20, DirName: "_隔离"})
	if _, err := ex.Apply(); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	for _, m := range fs.moved {
		if strings.Contains(m, "_隔离") {
			t.Fatalf("rename 模式不应隔离: %#v", fs.moved)
		}
	}
}

// 阈值为 0（默认关闭）时通道完全静默。
func TestQuarantineOffWhenThresholdZero(t *testing.T) {
	fs := newSizedFS()
	plan := quarantinePlan()
	ex := executor.New(context.Background(), fs, plan, 1, false, nil, nil)
	ex.SetMoveMode(true)
	if _, err := ex.Apply(); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if _, ok := fs.dirs["root/_隔离"]; ok {
		t.Fatal("阈值为 0 时不该出现隔离目录")
	}
}