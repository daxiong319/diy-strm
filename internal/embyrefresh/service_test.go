package embyrefresh

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// testClock 是可手动推进的测试时钟。
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func newTestClock() *testClock {
	return &testClock{at: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// fakeRefresher 记录刷新调用，并按规则返回错误。
type fakeRefresher struct {
	mu       sync.Mutex
	calls    []RefreshRequest
	itemErr  map[string]error
	libErr   map[string]error
	itemCall int
	libCall  int
}

func newFakeRefresher() *fakeRefresher {
	return &fakeRefresher{itemErr: map[string]error{}, libErr: map[string]error{}}
}

func (f *fakeRefresher) RefreshLibrary(_ context.Context, req RefreshRequest) (RefreshResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if req.Mode == TargetTypeItem {
		f.itemCall++
		if err := f.itemErr[req.ItemID]; err != nil {
			return RefreshResult{}, err
		}
	} else {
		f.libCall++
		if err := f.libErr[req.LibraryID]; err != nil {
			return RefreshResult{}, err
		}
	}
	return RefreshResult{Mode: req.Mode, LibraryID: req.LibraryID, ItemID: req.ItemID}, nil
}

func (f *fakeRefresher) snapshot() []RefreshRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RefreshRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

func newTestService(t *testing.T, refresher Refresher, clock *testClock, threshold int) (*Service, domain.EmbyRefreshTaskRepository) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repos := store.New(db)
	svc := New(Options{
		Tasks:                repos.EmbyRefreshTasks,
		Refresher:            refresher,
		AggregationThreshold: threshold,
		Now:                  clock.Now,
		Log:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if svc == nil {
		t.Fatal("New 返回 nil")
	}
	return svc, repos.EmbyRefreshTasks
}

func TestNewReturnsNilWithoutTasks(t *testing.T) {
	if svc := New(Options{}); svc != nil {
		t.Fatal("缺少任务仓库时应返回 nil")
	}
}

func TestRequestRefreshDebouncesAndUnionsItemIDs(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	svc, repo := newTestService(t, newFakeRefresher(), clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{TargetType: TargetTypeItem, ItemID: "item-1"}); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}
	first, ok, err := repo.GetByKey(ctx, ItemTaskKey("item-1"))
	if err != nil || !ok {
		t.Fatalf("读取任务失败: ok=%v err=%v", ok, err)
	}
	if got := first.RefreshAfter; got != clock.Now().Add(DefaultDebounce).Unix() {
		t.Fatalf("首次防抖时间=%d，期望 %d", got, clock.Now().Add(DefaultDebounce).Unix())
	}
	if got := first.DeadlineAt; got != clock.Now().Add(DefaultMaxWait).Unix() {
		t.Fatalf("截止时间=%d，期望 %d", got, clock.Now().Add(DefaultMaxWait).Unix())
	}

	// 同一目标在防抖窗口内再次到达：窗口顺延，且不新增任务行。
	clock.Advance(5 * time.Second)
	if err := svc.RequestRefresh(ctx, RequestRefreshParams{TargetType: TargetTypeItem, ItemID: "item-1"}); err != nil {
		t.Fatalf("二次登记失败: %v", err)
	}
	second, _, err := repo.GetByKey(ctx, ItemTaskKey("item-1"))
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同一 task_key 应复用任务行: first=%d second=%d", first.ID, second.ID)
	}
	if second.RefreshAfter <= first.RefreshAfter {
		t.Fatalf("防抖窗口未顺延: first=%d second=%d", first.RefreshAfter, second.RefreshAfter)
	}
	if want := clock.Now().Add(DefaultDebounce).Unix(); second.RefreshAfter != want {
		t.Fatalf("顺延后防抖时间=%d，期望 %d", second.RefreshAfter, want)
	}
	// 截止时间不随事件顺延，保证任务不会无限延后。
	if second.DeadlineAt != first.DeadlineAt {
		t.Fatalf("截止时间被意外顺延: first=%d second=%d", first.DeadlineAt, second.DeadlineAt)
	}
	if second.Status != StatusPending {
		t.Fatalf("重复事件后状态=%q，期望 pending", second.Status)
	}

	// 库级任务的条目集合按并集合并。
	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeLibrary, LibraryID: "lib-1", LibraryName: "电影",
	}); err != nil {
		t.Fatalf("登记库级任务失败: %v", err)
	}
	lib, ok, err := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if err != nil || !ok {
		t.Fatalf("读取库级任务失败: ok=%v err=%v", ok, err)
	}
	if lib.TargetType != TargetTypeLibrary || lib.LibraryName != "电影" {
		t.Fatalf("库级任务异常: %+v", lib)
	}
}

func TestRequestRefreshValidatesTarget(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	svc, _ := newTestService(t, newFakeRefresher(), clock, 10)

	cases := []struct {
		name string
		req  RequestRefreshParams
	}{
		{"条目 ID 为空", RequestRefreshParams{TargetType: TargetTypeItem}},
		{"媒体库 ID 为空", RequestRefreshParams{TargetType: TargetTypeLibrary}},
		{"目标类型无效", RequestRefreshParams{TargetType: "unknown", LibraryID: "lib-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.RequestRefresh(ctx, tc.req); err == nil {
				t.Fatal("应返回校验错误")
			}
		})
	}
}

func TestRequestRefreshIsIdempotentPerTaskKey(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	svc, repo := newTestService(t, newFakeRefresher(), clock, 10)

	for i := 0; i < 5; i++ {
		if err := svc.RequestRefresh(ctx, RequestRefreshParams{
			TargetType: TargetTypeLibrary, LibraryID: "lib-1", LibraryName: "电影",
		}); err != nil {
			t.Fatalf("第 %d 次登记失败: %v", i, err)
		}
		clock.Advance(time.Second)
	}
	tasks, err := repo.ListReady(ctx, clock.Now().Add(time.Hour).Unix(), 100)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("同一 task_key 应只保留一行，实际 %d 行", len(tasks))
	}
}

func TestScanOnceCancelsExpiredTask(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	svc, repo := newTestService(t, refresher, clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeLibrary, LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	// 超过最长等待时间仍未执行（模拟扫描器长时间不可用）。
	clock.Advance(DefaultMaxWait + time.Minute)
	svc.scanOnce(ctx)

	task, _, err := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != StatusCancelled {
		t.Fatalf("超时任务状态=%q，期望 cancelled", task.Status)
	}
	if task.Error == "" {
		t.Fatal("超时任务应记录原因")
	}
	if got := refresher.snapshot(); len(got) != 0 {
		t.Fatalf("超时任务不应触发刷新: %+v", got)
	}
}

func TestScanOnceSkipsTaskWithinDebounce(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	svc, repo := newTestService(t, refresher, clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeLibrary, LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	clock.Advance(DefaultDebounce - time.Second)
	svc.scanOnce(ctx)
	if got := refresher.snapshot(); len(got) != 0 {
		t.Fatalf("防抖窗口内不应刷新: %+v", got)
	}
	task, _, _ := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if task.Status != StatusPending {
		t.Fatalf("防抖窗口内状态=%q，期望 pending", task.Status)
	}

	clock.Advance(2 * time.Second)
	svc.scanOnce(ctx)
	calls := refresher.snapshot()
	if len(calls) != 1 || calls[0].Mode != TargetTypeLibrary || calls[0].LibraryID != "lib-1" {
		t.Fatalf("防抖结束后应执行一次库刷新: %+v", calls)
	}
	task, _, _ = repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if task.Status != StatusCompleted {
		t.Fatalf("执行后状态=%q，期望 completed", task.Status)
	}
}

func TestScanOnceClaimsTaskOnlyOnce(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	svc, repo := newTestService(t, newFakeRefresher(), clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeLibrary, LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	clock.Advance(DefaultDebounce + time.Second)
	task, _, err := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// 第一个执行者认领成功。
	claimed, err := repo.Claim(ctx, task.ID)
	if err != nil || !claimed {
		t.Fatalf("首次认领应当成功: claimed=%v err=%v", claimed, err)
	}
	// 第二个执行者认领同一个任务必须失败（CAS 条件不满足）。
	second, err := repo.Claim(ctx, task.ID)
	if err != nil {
		t.Fatalf("二次认领返回错误: %v", err)
	}
	if second {
		t.Fatal("二次认领不应成功")
	}
	// 认领后任务不再是 pending，不再是就绪任务。
	ready, err := repo.ListReady(ctx, clock.Now().Add(time.Hour).Unix(), 10)
	if err != nil {
		t.Fatalf("读取就绪任务失败: %v", err)
	}
	for _, r := range ready {
		if r.ID == task.ID {
			t.Fatalf("已认领任务不应出现在就绪列表: %+v", r)
		}
	}
}

func TestScanOnceAggregatesItemsAtThreshold(t *testing.T) {
	cases := []struct {
		name          string
		itemCount     int
		wantAbsorbed  bool
		wantLibraryHi bool
	}{
		{"达到阈值 10 个条目被合并", 10, true, true},
		{"未达阈值 9 个条目不合并", 9, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newTestClock()
			refresher := newFakeRefresher()
			svc, repo := newTestService(t, refresher, clock, 10)

			for i := 0; i < tc.itemCount; i++ {
				itemID := "item-" + string(rune('a'+i))
				if err := svc.RequestRefresh(ctx, RequestRefreshParams{
					TargetType: TargetTypeItem, ItemID: itemID,
					LibraryID: "lib-1", LibraryName: "电影",
				}); err != nil {
					t.Fatalf("登记条目 %s 失败: %v", itemID, err)
				}
			}
			clock.Advance(DefaultDebounce + time.Second)
			svc.aggregateItems(ctx)

			items, err := repo.ListReadyItems(ctx, clock.Now().Unix(), 200)
			if err != nil {
				t.Fatalf("读取条目任务失败: %v", err)
			}
			lib, found, err := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
			if err != nil {
				t.Fatalf("读取库级任务失败: %v", err)
			}
			if !tc.wantAbsorbed {
				if found && lib.ItemIDs != "[]" && lib.ItemIDs != "" {
					t.Fatalf("未达阈值不应产生合并的库级任务: %+v", lib)
				}
				if len(items) != tc.itemCount {
					t.Fatalf("未达阈值时条目任务应保持 %d 个，实际 %d", tc.itemCount, len(items))
				}
				return
			}
			if !found {
				t.Fatal("达到阈值应创建库级任务")
			}
			if lib.LibraryName != "电影" || lib.TargetType != TargetTypeLibrary {
				t.Fatalf("合并后的库级任务异常: %+v", lib)
			}
			if got := len(decodeItemIDs(lib.ItemIDs)); got != tc.itemCount {
				t.Fatalf("库级任务应吸收 %d 个条目，实际 %d", tc.itemCount, got)
			}
			if len(items) != 0 {
				t.Fatalf("被吸收的条目任务不应仍为 pending，实际 %d 个", len(items))
			}
			// 被吸收的条目行必须标记为 cancelled。
			for i := 0; i < tc.itemCount; i++ {
				itemID := "item-" + string(rune('a'+i))
				task, ok, err := repo.GetByKey(ctx, ItemTaskKey(itemID))
				if err != nil || !ok {
					t.Fatalf("读取条目任务 %s 失败: ok=%v err=%v", itemID, ok, err)
				}
				if task.Status != StatusCancelled {
					t.Fatalf("条目任务 %s 状态=%q，期望 cancelled", itemID, task.Status)
				}
			}
		})
	}
}

func TestScanOnceAggregationDisabled(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	svc, repo := newTestService(t, newFakeRefresher(), clock, -1)

	for i := 0; i < 20; i++ {
		if err := svc.RequestRefresh(ctx, RequestRefreshParams{
			TargetType: TargetTypeItem, ItemID: "item-" + string(rune('a'+i)),
			LibraryID: "lib-1",
		}); err != nil {
			t.Fatalf("登记失败: %v", err)
		}
	}
	clock.Advance(DefaultDebounce + time.Second)
	svc.scanOnce(ctx)

	if _, found, _ := repo.GetByKey(ctx, LibraryTaskKey("lib-1")); found {
		t.Fatal("关闭合并后不应创建库级任务")
	}
}

func TestExecuteItemFallsBackToLibraryThenFails(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	refresher.itemErr["item-1"] = errors.New("条目刷新被打回")
	refresher.libErr["lib-1"] = errors.New("媒体库刷新被打回")
	svc, repo := newTestService(t, refresher, clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeItem, ItemID: "item-1", LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	clock.Advance(DefaultDebounce + time.Second)
	svc.scanOnce(ctx)

	task, _, err := repo.GetByKey(ctx, ItemTaskKey("item-1"))
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != StatusFailed {
		t.Fatalf("条目与库刷新均失败时状态=%q，期望 failed", task.Status)
	}
	if !strings.Contains(task.Error, "条目刷新被打回") {
		t.Fatalf("失败原因未记录: %q", task.Error)
	}
	calls := refresher.snapshot()
	if len(calls) != 2 {
		t.Fatalf("应先尝试条目再降级库刷新，实际调用 %+v", calls)
	}
	if calls[0].Mode != TargetTypeItem || calls[0].ItemID != "item-1" {
		t.Fatalf("首次调用应为条目刷新: %+v", calls[0])
	}
	if calls[1].Mode != TargetTypeLibrary || calls[1].LibraryID != "lib-1" {
		t.Fatalf("降级调用应为库刷新: %+v", calls[1])
	}
}

func TestExecuteItemFallbackSucceeds(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	refresher.itemErr["item-1"] = errors.New("条目刷新被打回")
	svc, repo := newTestService(t, refresher, clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeItem, ItemID: "item-1", LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	clock.Advance(DefaultDebounce + time.Second)
	svc.scanOnce(ctx)

	task, _, err := repo.GetByKey(ctx, ItemTaskKey("item-1"))
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if task.Status != StatusCompleted {
		t.Fatalf("降级成功后状态=%q，期望 completed", task.Status)
	}
	if !strings.Contains(task.Error, "降级") {
		t.Fatalf("降级说明未记录: %q", task.Error)
	}
}

func TestExecuteItemWithoutLibraryMarksFailed(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	refresher.itemErr["item-1"] = errors.New("条目刷新被打回")
	svc, repo := newTestService(t, refresher, clock, 10)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeItem, ItemID: "item-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	clock.Advance(DefaultDebounce + time.Second)
	svc.scanOnce(ctx)

	task, _, _ := repo.GetByKey(ctx, ItemTaskKey("item-1"))
	if task.Status != StatusFailed {
		t.Fatalf("无库可降级时应标记失败，实际 %q", task.Status)
	}
	if len(refresher.snapshot()) != 1 {
		t.Fatal("无归属库时不应尝试降级刷新")
	}
}

func TestStartStopScanner(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock()
	refresher := newFakeRefresher()
	// 用一个很短的扫描周期，并让防抖窗口立刻到期，验证后台循环确实在跑。
	svc, repo := newTestService(t, refresher, clock, 10)
	svc.debounce = 0
	svc.interval = 20 * time.Millisecond

	svc.Start(ctx)
	// 重复启动应当是安全的。
	svc.Start(ctx)

	if err := svc.RequestRefresh(ctx, RequestRefreshParams{
		TargetType: TargetTypeLibrary, LibraryID: "lib-1",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	// wake 会立即触发一轮扫描；等待任务进入终态。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _, err := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
		if err == nil && task != nil && task.Status == StatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _, _ := repo.GetByKey(ctx, LibraryTaskKey("lib-1"))
	if task.Status != StatusCompleted {
		t.Fatalf("后台扫描未执行任务，状态=%q", task.Status)
	}
	svc.Stop(ctx)
	// 重复停止应当是安全的。
	svc.Stop(ctx)
}
