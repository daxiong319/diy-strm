package inspection

import (
	"context"
	"strings"
	"testing"
	"time"

	"litepan/internal/domain"
)

// memRepo 是巡检快照的内存仓储。
//
// 快照的语义（一次性消费、过期作废）是这套东西的安全边界：
// 修复只认「扫描那一刻报出来的那些条」，所以这里必须真的把 consumed_at
// 和 TTL 模拟出来，不能拿个 map 一存了事 —— 那样测的就不是生产行为。
type memRepo struct {
	rows map[string]memSnapshot
}

type memSnapshot struct {
	payload   string
	ids       string
	scannedAt time.Time
	consumed  bool
}

func newMemRepo() *memRepo { return &memRepo{rows: map[string]memSnapshot{}} }

func (m *memRepo) SaveInspectionSnapshot(_ context.Context, key, ids, payload string, scannedAt time.Time) error {
	m.rows[key] = memSnapshot{payload: payload, ids: ids, scannedAt: scannedAt}
	return nil
}

func (m *memRepo) GetInspectionSnapshot(_ context.Context, key string) (string, []string, bool, error) {
	row, ok := m.rows[key]
	if !ok {
		return "", nil, true, nil
	}
	return row.payload, splitIDs(row.ids), row.consumed, nil
}

func (m *memRepo) ConsumeInspectionSnapshot(_ context.Context, key string) error {
	row, ok := m.rows[key]
	if !ok || row.consumed {
		return domain.Errorf(domain.CodeValidation, "该巡检快照已被执行，请重新扫描后再试")
	}
	row.consumed = true
	m.rows[key] = row
	return nil
}

func (m *memRepo) DeleteOldInspectionSnapshots(_ context.Context, before time.Time) (int64, error) {
	var n int64
	for key, row := range m.rows {
		if row.scannedAt.Before(before) {
			delete(m.rows, key)
			n++
		}
	}
	return n, nil
}

func splitIDs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// fakeChecker 造一个受控的检查器。
type fakeChecker struct {
	key   string
	label string
	err   error
	got   []Finding
	calls int
}

func (c *fakeChecker) Key() string   { return c.key }
func (c *fakeChecker) Label() string { return c.label }

func (c *fakeChecker) Scan(context.Context) ([]Finding, error) {
	c.calls++
	return c.got, c.err
}

func newTestService(t *testing.T, checkers ...Checker) (*Service, *memRepo, *time.Time) {
	t.Helper()
	reg := NewRegistry()
	for _, c := range checkers {
		reg.Register(c)
	}
	repo := newMemRepo()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	return NewService(ServiceOptions{Repo: repo, Registry: reg, Now: clock}), repo, &now
}

func finding(checker, kind, target string) Finding {
	return Finding{CheckerKey: checker, Kind: kind, Target: target}
}

func TestScanAllKeepsGoingWhenOneCheckerFails(t *testing.T) {
	bad := &fakeChecker{key: KeyEmptyDir, label: "目录树清理", err: domain.Errorf(domain.CodeDriverError, "网盘不可用")}
	good := &fakeChecker{key: KeyDuplicate, label: "重复排查", got: []Finding{finding(KeyDuplicate, "duplicate_candidate", "a")}}
	svc, _, _ := newTestService(t, bad, good)

	report, err := svc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("一个检查器出错不该让整轮失败：%v", err)
	}
	if report.Total != 1 {
		t.Fatalf("Total = %d，期望 1 —— 另一个检查器的结论必须仍然可见", report.Total)
	}
	var failed, ok int
	for _, info := range report.Checkers {
		if info.Error != "" {
			failed++
			if info.Findings != 0 {
				t.Errorf("%s 出错了却带着 %d 条发现", info.Key, info.Findings)
			}
		} else {
			ok++
		}
	}
	if failed != 1 || ok != 1 {
		t.Fatalf("CheckerInfo 里失败 %d 个、成功 %d 个，期望各 1 个", failed, ok)
	}
}

func TestRepairRequiresPreviewedSnapshot(t *testing.T) {
	c := &fakeChecker{key: KeyOrphanDir, label: "孤儿目录清理", got: []Finding{
		{
			CheckerKey: KeyOrphanDir, Kind: "orphan_dir", Target: "/media/残骸",
			Repair: RepairAction{
				Kind: RepairDeleteDir, Label: "删除这个孤儿目录",
				Params: map[string]string{"account_id": "0", "dir_id": "d1"},
				Preview: "删除目录 /media/残骸。", Reversible: false,
			},
		},
	}}
	svc, _, _ := newTestService(t, c)

	// 没有快照直接给 ID：必须拒绝。
	// 这一条就是「修复前必须预览」的落点 —— 修复动作只能从快照里取，
	// 客户端自带的路径与文件名一概不认。
	if _, err := svc.Repair(context.Background(), "snap-forged", []string{"orphan_dir|orphan_dir|/media/残骸"}); err == nil {
		t.Fatal("伪造的 snapshot_id 也被执行了 —— 等于任何客户端都能删目录")
	}

	report, err := svc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll 失败：%v", err)
	}
	var executed []string
	reg := svc.reg
	reg.RegisterRepairer(RepairDeleteDir, funcRepairer(func(_ context.Context, a RepairAction) (string, error) {
		executed = append(executed, a.Params["dir_id"])
		return "已删除", nil
	}))

	results, err := svc.Repair(context.Background(), report.SnapshotID, []string{"orphan_dir|orphan_dir|/media/残骸"})
	if err != nil {
		t.Fatalf("Repair 失败：%v", err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("结果 = %+v，期望一条成功", results)
	}
	if len(executed) != 1 || executed[0] != "d1" {
		t.Fatalf("修复器收到 %v，期望只收到 dir_id=d1", executed)
	}
}

// TestRepairConsumesSnapshotAfterExecution 钉住「一份快照只能用一次」。
//
// 不作废的后果：修完之后剩下的 Finding 可能已经不成立（父目录删了，
// 子目录那条也就没了），用户照着旧预览再点一次就是在删不存在的东西，
// 或者更糟 —— 目录已经被同名重建、内容换了一部片子。
func TestRepairConsumesSnapshotAfterExecution(t *testing.T) {
	c := &fakeChecker{key: KeyOrphanDir, label: "孤儿目录清理", got: []Finding{
		{CheckerKey: KeyOrphanDir, Kind: "orphan_dir", Target: "/media/a",
			Repair: RepairAction{Kind: RepairDeleteDir, Params: map[string]string{"dir_id": "d1"}, Preview: "删 a"}},
	}}
	svc, _, _ := newTestService(t, c)
	svc.reg.RegisterRepairer(RepairDeleteDir, funcRepairer(func(context.Context, RepairAction) (string, error) {
		return "已删除", nil
	}))

	report, err := svc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll 失败：%v", err)
	}
	id := "orphan_dir|orphan_dir|/media/a"
	if _, err := svc.Repair(context.Background(), report.SnapshotID, []string{id}); err != nil {
		t.Fatalf("第一次 Repair 失败：%v", err)
	}
	if _, err := svc.Repair(context.Background(), report.SnapshotID, []string{id}); err == nil {
		t.Fatal("同一份快照被执行了第二次 —— 用户会照着已经过期的预览重复操作")
	}
	if _, ok := svc.GetPreview(context.Background(), report.SnapshotID); ok {
		t.Fatal("已执行的快照还能取到预览 —— 页面会继续显示可以点的那批行")
	}
}

func TestRepairLeavesSnapshotAliveWhenNothingExecuted(t *testing.T) {
	// 只报告项（重复排查、查漏补缺）不该消耗快照：它们没有改动任何东西，
	// 用户看完预览可能还想留着这份快照去别的界面比对。
	c := &fakeChecker{key: KeyDuplicate, label: "重复排查", got: []Finding{
		{CheckerKey: KeyDuplicate, Kind: "duplicate_candidate", Target: "/media/700M",
			Repair: RepairAction{Kind: RepairNone, Label: "仅报告"}},
	}}
	svc, _, _ := newTestService(t, c)
	report, err := svc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll 失败：%v", err)
	}
	results, err := svc.Repair(context.Background(), report.SnapshotID, []string{"duplicate|duplicate_candidate|/media/700M"})
	if err != nil {
		t.Fatalf("Repair 失败：%v", err)
	}
	if len(results) != 1 || !results[0].OK || !strings.Contains(results[0].Message, "仅报告") {
		t.Fatalf("结果 = %+v，期望一条「仅报告项，无需执行」", results)
	}
	if _, ok := svc.GetPreview(context.Background(), report.SnapshotID); !ok {
		t.Fatal("只看不改却把快照作废了")
	}
}

func TestRepairRejectsIDFromAnotherScan(t *testing.T) {
	c := &fakeChecker{key: KeyEmptyDir, label: "目录树清理", got: []Finding{
		{CheckerKey: KeyEmptyDir, Kind: "empty_dir", Target: "/media/x",
			Repair: RepairAction{Kind: RepairDeleteDir, Params: map[string]string{"dir_id": "d1"}, Preview: "删 x"}},
	}}
	svc, _, _ := newTestService(t, c)
	svc.reg.RegisterRepairer(RepairDeleteDir, funcRepairer(func(context.Context, RepairAction) (string, error) {
		t.Error("修复器被调用了 —— 陌生 ID 不该触发任何动作")
		return "", nil
	}))
	report, err := svc.ScanAll(context.Background())
	if err != nil {
		t.Fatalf("ScanAll 失败：%v", err)
	}
	results, err := svc.Repair(context.Background(), report.SnapshotID, []string{"empty_dir|empty_dir|/media/不存在"})
	if err != nil {
		t.Fatalf("Repair 失败：%v", err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("陌生 ID 的结果 = %+v，期望一条失败并说明原因", results)
	}
}

func TestGetPreviewRejectsUnknownSnapshot(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, ok := svc.GetPreview(context.Background(), "snap-nonexistent"); ok {
		t.Fatal("不存在的快照返回了 ok —— 页面会把它当成「没有问题」")
	}
}

// funcRepairer 把一个函数包装成 Repairer，避免每个用例都写一个类型。
func funcRepairer(fn func(context.Context, RepairAction) (string, error)) Repairer {
	return repairerFunc(fn)
}

type repairerFunc func(context.Context, RepairAction) (string, error)

func (f repairerFunc) Repair(ctx context.Context, a RepairAction) (string, error) { return f(ctx, a) }
