package notifychannel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
)

// 补发退避 worker 的用例。
//
// ⚠️ 本文件**不允许出现真 sleep**：真等一次 12 小时退避不可能，真等 1 分钟
// 也会把测试拖成慢套件。所以 worker 的时钟是注入的（RetryWorker.SetClock），
// 测试自己推进时间；只有断言「后台 goroutine 真的会自己跑起来」那条用例
// 才用几十毫秒的真实 tick。

// fakeClock 可手动推进的假时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestRetryBackoffTableIsExactlyTheDocumentedOne 退避表钉死成文档的五档。
//
// 刻意不做「指数退避 + 抖动」：文档给的是固定档位，实现成别的形状就对不上；
// 而且这里最多重试 5 次，削峰收益远小于「与文档不一致」的排查成本。
func TestRetryBackoffTableIsExactlyTheDocumentedOne(t *testing.T) {
	want := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		12 * time.Hour,
	}
	if len(notifyRetryBackoff) != len(want) {
		t.Fatalf("退避档位数 = %d，期望 %d", len(notifyRetryBackoff), len(want))
	}
	for i, d := range want {
		if notifyRetryBackoff[i] != d {
			t.Errorf("退避第 %d 档 = %v，期望 %v", i+1, notifyRetryBackoff[i], d)
		}
	}
	if notifyRetryMaxAttempts != len(want) {
		t.Errorf("notifyRetryMaxAttempts = %d，期望 %d（用尽前的总尝试次数）", notifyRetryMaxAttempts, len(want))
	}
	for attempts := 1; attempts <= len(want); attempts++ {
		d, ok := backoffFor(attempts)
		if !ok {
			t.Fatalf("backoffFor(%d) 报告无退避档位", attempts)
		}
		if d != want[attempts-1] {
			t.Errorf("backoffFor(%d) = %v，期望 %v", attempts, d, want[attempts-1])
		}
	}
	// 0 次失败不该有退避（还没发过），超出档位数=用尽。
	if _, ok := backoffFor(0); ok {
		t.Error("backoffFor(0) 返回了退避时长，attempts 应从 1 起")
	}
	if _, ok := backoffFor(len(want) + 1); ok {
		t.Errorf("backoffFor(%d) 仍返回退避时长，应报告用尽", len(want)+1)
	}
}

// retryWorkerFixture 装一套「假时钟 + 内存队列 + 可控投递」的 worker。
type retryWorkerFixture struct {
	repo   *memoryRetryRepo
	clock  *fakeClock
	worker *RetryWorker

	mu       sync.Mutex
	sends    []string // 每次 send 收到的配置 JSON，按顺序
	failNext int
	failWith error
}

func newRetryWorkerFixture(t *testing.T, sendErr error) *retryWorkerFixture {
	t.Helper()
	f := &retryWorkerFixture{
		repo:     newMemoryRetryRepo(),
		clock:    newFakeClock(),
		failWith: sendErr,
	}
	f.worker = NewRetryWorker(f.repo, nil, func(_ context.Context, channelType, channelConfig string, _ Message) error {
		f.mu.Lock()
		f.sends = append(f.sends, channelType+"|"+channelConfig)
		n := f.failNext
		if n > 0 {
			f.failNext--
		}
		err := f.failWith
		f.mu.Unlock()
		if n > 0 {
			return err
		}
		return nil
	})
	f.worker.SetClock(f.clock.now, 0) // tick 只影响后台循环，单轮测试不用
	return f
}

func (f *retryWorkerFixture) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *retryWorkerFixture) setFailures(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext, f.failWith = n, err
}

// entry 返回当前队列里那一条（要求只有一条）。
func (f *retryWorkerFixture) entry(t *testing.T) domain.NotifyRetryEntry {
	t.Helper()
	snap := f.repo.snapshot()
	if len(snap) != 1 {
		t.Fatalf("队列里 %d 条，期望 1 条", len(snap))
	}
	return snap[0]
}

// seedDue 直接往内存队列塞一条「已到点」的记录。
func (f *retryWorkerFixture) seedDue(t *testing.T, e domain.NotifyRetryEntry) int64 {
	t.Helper()
	if e.NextRetryAt.IsZero() {
		e.NextRetryAt = f.clock.now()
	}
	id, err := f.repo.Enqueue(context.Background(), e)
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	return id
}

// TestRetryWorkerRetriesUntilSuccess 验收 ③：退避重试直到送达。
//
// 全程零 sleep：每一轮手动把假时钟推进到下一个 next_retry_at。
func TestRetryWorkerRetriesUntilSuccess(t *testing.T) {
	f := newRetryWorkerFixture(t, errors.New("webhook HTTP 503"))
	f.setFailures(2, errors.New("webhook HTTP 503"))
	id := f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType:   ChannelWebhook,
		ChannelName:   "运维告警",
		TargetURL:     "https://hook.test/notify",
		ChannelConfig: `{"method":"POST","url":"https://hook.test/notify"}`,
		Title:         "STRM 扫描部分失败",
		Content:       "3 个任务失败",
		Tone:          "warn",
		NextRetryAt:   f.clock.now(),
	})
	ctx := context.Background()

	// 第 1 轮：失败 → 推后 1 分钟
	f.worker.RunOnce(ctx)
	e := f.entry(t)
	if e.Attempts != 1 || e.Status != domain.NotifyRetryStatusPending {
		t.Fatalf("第 1 轮后 = attempts %d / %s，期望 1 / pending", e.Attempts, e.Status)
	}
	if got := e.NextRetryAt.Sub(f.clock.now()); got != time.Minute {
		t.Fatalf("第 1 轮后的重试间隔 = %v，期望 1m", got)
	}

	// 还没到点时不该被碰（否则退避形同虚设）
	f.worker.RunOnce(ctx)
	if f.sendCount() != 1 {
		t.Fatalf("未到点却重发了 %d 次，退避没生效", f.sendCount())
	}

	// 推进到第一档退避之后：第 2 轮 → 推后 5 分钟
	f.clock.advance(time.Minute)
	f.worker.RunOnce(ctx)
	e = f.entry(t)
	if e.Attempts != 2 || e.Status != domain.NotifyRetryStatusPending {
		t.Fatalf("第 2 轮后 = attempts %d / %s，期望 2 / pending", e.Attempts, e.Status)
	}
	if got := e.NextRetryAt.Sub(f.clock.now()); got != 5*time.Minute {
		t.Fatalf("第 2 轮后的重试间隔 = %v，期望 5m", got)
	}

	// 第 3 轮成功 → sent 终态
	f.clock.advance(5 * time.Minute)
	f.worker.RunOnce(ctx)
	e = f.entry(t)
	if e.Attempts != 3 || e.Status != domain.NotifyRetryStatusSent {
		t.Fatalf("第 3 轮后 = attempts %d / %s，期望 3 / sent", e.Attempts, e.Status)
	}
	if !e.NextRetryAt.IsZero() {
		t.Errorf("成功后仍留了下次重试时刻 %v，应清空", e.NextRetryAt)
	}
	if e.LastError != "" {
		t.Errorf("成功后仍留着失败原因 %q，应清空", e.LastError)
	}

	// sent 是终态：再跑多少轮都不该再发
	for range 3 {
		f.clock.advance(12 * time.Hour)
		f.worker.RunOnce(ctx)
	}
	if f.sendCount() != 3 {
		t.Fatalf("已送达的记录又被发了 %d 次，总发送 %d 次", f.sendCount()-3, f.sendCount())
	}
	_ = id
}

// TestRetryWorkerMarksFailedAfterExhaustingBackoff 验收 ③后半：五档用尽 → failed。
func TestRetryWorkerMarksFailedAfterExhaustingBackoff(t *testing.T) {
	f := newRetryWorkerFixture(t, errors.New("webhook HTTP 503"))
	f.setFailures(99, errors.New("webhook HTTP 503"))
	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "运维告警",
		TargetURL: "https://hook.test/notify", ChannelConfig: "{}",
		Title: "t", Content: "m", Tone: "warn",
	})
	ctx := context.Background()

	// 逐档推进：1m → 5m → 30m → 2h → 12h，第 6 次失败才判死。
	for i := 1; i <= len(notifyRetryBackoff); i++ {
		f.worker.RunOnce(ctx)
		e := f.entry(t)
		if e.Status != domain.NotifyRetryStatusPending {
			t.Fatalf("第 %d 次失败后状态 = %s，期望仍 pending（还有退避档位没用）", i, e.Status)
		}
		if e.Attempts != i {
			t.Fatalf("第 %d 次失败后 attempts = %d", i, e.Attempts)
		}
		if got := e.NextRetryAt.Sub(f.clock.now()); got != notifyRetryBackoff[i-1] {
			t.Fatalf("第 %d 次失败后的间隔 = %v，期望 %v", i, got, notifyRetryBackoff[i-1])
		}
		f.clock.advance(notifyRetryBackoff[i-1])
	}

	f.worker.RunOnce(ctx) // 第 6 次：档位用尽
	e := f.entry(t)
	if e.Status != domain.NotifyRetryStatusFailed {
		t.Fatalf("档位用尽后状态 = %s，期望 failed（管理台要能看到并重投）", e.Status)
	}
	if e.Attempts != len(notifyRetryBackoff)+1 {
		t.Errorf("failed 时 attempts = %d，期望 %d", e.Attempts, len(notifyRetryBackoff)+1)
	}
	if e.LastError == "" {
		t.Error("failed 记录没留失败原因")
	}
	// failed 是终态：不再被捞起
	before := f.sendCount()
	f.clock.advance(24 * time.Hour)
	f.worker.RunOnce(ctx)
	if f.sendCount() != before {
		t.Fatalf("failed 记录又被重发了 %d 次", f.sendCount()-before)
	}
}

// TestRetryWorkerFailsImmediatelyOnNonRetryableError 4xx 不进退避循环。
func TestRetryWorkerFailsImmediatelyOnNonRetryableError(t *testing.T) {
	f := newRetryWorkerFixture(t, nil)
	f.setFailures(99, httpFailure(ChannelWebhook, 404, []byte("no such hook"), nil))
	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "地址写错了",
		TargetURL: "https://hook.test/nope", ChannelConfig: "{}",
		Title: "t", Content: "m",
	})
	f.worker.RunOnce(context.Background())

	e := f.entry(t)
	if e.Status != domain.NotifyRetryStatusFailed {
		t.Fatalf("404 失败后状态 = %s，期望直接 failed（重发不会成功）", e.Status)
	}
	if e.Attempts != 1 {
		t.Errorf("404 失败后 attempts = %d，期望 1（不该在退避里空转五档）", e.Attempts)
	}
	if !e.NextRetryAt.IsZero() {
		t.Errorf("failed 仍留着下次重试时刻 %v", e.NextRetryAt)
	}
	// 失败原因里必须能看到对方返回的内容，否则用户无从下手。
	if e.LastError == "" {
		t.Fatal("failed 没留失败原因")
	}
}

// TestRetryWorkerUsesConfigSnapshot 重发用的是发送当时的配置快照，
// 不是用户改过之后的最新配置。
//
// 理由：用户改了 webhook 地址之后，旧失败记录如果按新配置重发，等于把
// 一次失败的历史转嫁到一个用户可能压根不认识的接收端上。
func TestRetryWorkerUsesConfigSnapshot(t *testing.T) {
	f := newRetryWorkerFixture(t, nil)
	f.setFailures(1, errors.New("dial tcp: connection refused"))
	snapshot := `{"method":"POST","url":"https://old.test/hook"}`
	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "改过地址的渠道",
		TargetURL: "https://old.test/hook", ChannelConfig: snapshot,
		Title: "t", Content: "m",
	})
	f.worker.RunOnce(context.Background())

	f.mu.Lock()
	got := f.sends
	f.mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("发送 %d 次，期望 1 次", len(got))
	}
	if got[0] != ChannelWebhook+"|"+snapshot {
		t.Fatalf("重发用的配置 = %q，期望原快照 %q", got[0], ChannelWebhook+"|"+snapshot)
	}
}

// TestRetryWorkerDoesNotTouchEntriesNotDue 未到点的记录一律不动。
func TestRetryWorkerDoesNotTouchEntriesNotDue(t *testing.T) {
	f := newRetryWorkerFixture(t, nil)
	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "还在退避",
		TargetURL: "https://hook.test/notify", ChannelConfig: "{}",
		Title: "t", Content: "m",
		NextRetryAt: f.clock.now().Add(30 * time.Minute),
	})
	f.worker.RunOnce(context.Background())
	if f.sendCount() != 0 {
		t.Fatalf("未到点的记录被发了 %d 次", f.sendCount())
	}
	e := f.entry(t)
	if e.Attempts != 0 || e.Status != domain.NotifyRetryStatusPending {
		t.Fatalf("未到点的记录被改了：attempts=%d status=%s", e.Attempts, e.Status)
	}
}

// TestRedriveOnlyWorksOnFailed 手动重投只对 failed 生效。
//
// 允许改 pending 的话，重投会把 attempts 归零，等于给正在退避的记录
// 免费插队、把「服务端挂了两小时」这种连续失败打成 12 小时档重来。
func TestRedriveOnlyWorksOnFailed(t *testing.T) {
	f := newRetryWorkerFixture(t, nil)
	ctx := context.Background()

	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "还在退避",
		ChannelConfig: "{}", Title: "t",
		NextRetryAt: f.clock.now().Add(5 * time.Minute),
	})
	pending := f.entry(t)
	if err := f.repo.Redrive(ctx, pending.ID, f.clock.now()); err == nil {
		t.Fatal("对 pending 记录重投没有报错 —— 会让退避中的记录免费插队")
	}

	// failed 才能重投：归零 attempts、清除失败原因、立刻到点。
	failedID, err := f.repo.Enqueue(ctx, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "已失败",
		ChannelConfig: "{}", Title: "t", Attempts: 5,
		Status: domain.NotifyRetryStatusFailed, LastError: "webhook HTTP 500",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if err := f.repo.Redrive(ctx, failedID, f.clock.now()); err != nil {
		t.Fatalf("重投 failed 记录失败: %v", err)
	}
	got, err := f.repo.Get(ctx, failedID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Status != domain.NotifyRetryStatusPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("重投后 = %s / attempts %d / err %q，期望 pending / 0 / 空",
			got.Status, got.Attempts, got.LastError)
	}
	// 重投后应立刻被下一轮补发捞起。
	f.clock.advance(time.Second)
	f.worker.RunOnce(ctx)
	if f.sendCount() != 1 {
		t.Fatalf("重投后补发没有发生，发送 %d 次", f.sendCount())
	}
}

// TestRetryWorkerStartRunsInBackground 后台循环真的会自己跑。
//
// 这条是唯一用真实 tick 的用例：前面所有用例都靠 RunOnce 驱动，
// 只能证明「一轮跑对了」，证明不了「没人在时它也会跑」。
func TestRetryWorkerStartRunsInBackground(t *testing.T) {
	f := newRetryWorkerFixture(t, nil)
	f.worker.SetClock(f.clock.now, 10*time.Millisecond)
	f.seedDue(t, domain.NotifyRetryEntry{
		ChannelType: ChannelWebhook, ChannelName: "后台补发",
		ChannelConfig: "{}", Title: "t", Content: "m",
		NextRetryAt: f.clock.now(),
	})
	f.worker.Start(context.Background())
	defer f.worker.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && f.sendCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if f.sendCount() == 0 {
		t.Fatal("worker 启动了但从没补发过 —— 后台循环没跑")
	}
	// Stop 之后不该再有新的发送。
	f.worker.Stop()
	before := f.sendCount()
	time.Sleep(80 * time.Millisecond)
	if f.sendCount() != before {
		t.Fatalf("Stop 之后仍在发送（+%d 次）", f.sendCount()-before)
	}
}

// TestRetryWorkerNilSendDoesNotStart 没有投递通道的 worker 不该启动。
func TestRetryWorkerNilSendDoesNotStart(t *testing.T) {
	w := NewRetryWorker(newMemoryRetryRepo(), nil, nil)
	w.Start(context.Background()) // 不能 panic
	w.Stop()
	w.RunOnce(context.Background())
}
