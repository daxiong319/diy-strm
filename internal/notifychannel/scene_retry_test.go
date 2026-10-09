package notifychannel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/eventbus"
)

// 本文件覆盖 T12 的两个行为闸门：
//   ①场景过滤 matchesSceneFilter —— 关闭某场景后对应通知不发、其它不受影响；
//   ②补发入队 maybeEnqueueRetry —— 只有 webhook 进队列，非 webhook 失败就是失败。
//
// 两者都用「真装配 + 异步轮询」而不是直接调私有函数：闸门在
// onNotificationCreated 里面，只有真的发一次事件才能证明它被调用了。

// recordingChannelRepo 是 domain.NotifyChannelRepository 的最小实现。
type recordingChannelRepo struct {
	mu       sync.Mutex
	channels []*domain.NotifyChannel
}

func (r *recordingChannelRepo) Create(context.Context, *domain.NotifyChannel) (int64, error) {
	return 0, nil
}
func (r *recordingChannelRepo) Update(context.Context, *domain.NotifyChannel) error { return nil }
func (r *recordingChannelRepo) Delete(context.Context, int64) error                 { return nil }
func (r *recordingChannelRepo) Get(context.Context, int64) (*domain.NotifyChannel, error) {
	return nil, nil
}
func (r *recordingChannelRepo) List(context.Context) ([]*domain.NotifyChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*domain.NotifyChannel(nil), r.channels...), nil
}
func (r *recordingChannelRepo) ListEnabled(ctx context.Context) ([]*domain.NotifyChannel, error) {
	return r.List(ctx)
}

// memoryRetryRepo 是 domain.NotifyRetryRepository 的内存替身。
//
// 刻意**自己实现退避判定而不是复用 store 的 SQL**：本文件要证明的是
// 「失败次数与 next_retry_at 的关系对不对」，而 store 那侧的行为由
// internal/store/migration_0047_test.go 走真 SQL 覆盖。两边各管一层，
// 任何一层改坏都会在某一侧红。
type memoryRetryRepo struct {
	mu      sync.Mutex
	entries map[int64]domain.NotifyRetryEntry
	nextID  int64
	dueErr  error
}

func newMemoryRetryRepo() *memoryRetryRepo {
	return &memoryRetryRepo{entries: map[int64]domain.NotifyRetryEntry{}, nextID: 100}
}

func (r *memoryRetryRepo) Enqueue(_ context.Context, e domain.NotifyRetryEntry) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.Status == "" {
		e.Status = domain.NotifyRetryStatusPending
	}
	id := r.nextID
	r.nextID++
	e.ID = id
	r.entries[id] = e
	return id, nil
}

func (r *memoryRetryRepo) Due(_ context.Context, now time.Time, limit int) ([]domain.NotifyRetryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dueErr != nil {
		return nil, r.dueErr
	}
	var out []domain.NotifyRetryEntry
	for _, e := range r.entries {
		if e.Status == domain.NotifyRetryStatusPending && !e.NextRetryAt.After(now) {
			out = append(out, e)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *memoryRetryRepo) UpdateResult(_ context.Context, id int64, attempts int, next time.Time, lastErr string, status domain.NotifyRetryStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return errors.New("not found")
	}
	e.Attempts, e.NextRetryAt, e.LastError, e.Status = attempts, next, lastErr, status
	r.entries[id] = e
	return nil
}

func (r *memoryRetryRepo) List(context.Context, domain.NotifyRetryQuery) ([]domain.NotifyRetryEntry, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.NotifyRetryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out, int64(len(out)), nil
}

func (r *memoryRetryRepo) Get(_ context.Context, id int64) (domain.NotifyRetryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return domain.NotifyRetryEntry{}, errors.New("not found")
	}
	return e, nil
}

func (r *memoryRetryRepo) Redrive(_ context.Context, id int64, now time.Time) error {
	// 与 store 实现同口径：只有 failed 能重投。允许改 pending 会让正在
	// 退避中的记录 attempts 归零、等于免费插队。
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return errors.New("not found")
	}
	if e.Status != domain.NotifyRetryStatusFailed {
		return errors.New("补发记录当前不是失败状态，无需重投")
	}
	e.Attempts, e.NextRetryAt, e.LastError, e.Status = 0, now, "", domain.NotifyRetryStatusPending
	r.entries[id] = e
	return nil
}

func (r *memoryRetryRepo) Clear(_ context.Context, status domain.NotifyRetryStatus) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for id, e := range r.entries {
		if status == "" || e.Status == status {
			delete(r.entries, id)
			n++
		}
	}
	return n, nil
}

func (r *memoryRetryRepo) Counts(context.Context) (map[domain.NotifyRetryStatus]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[domain.NotifyRetryStatus]int64{}
	for _, e := range r.entries {
		out[e.Status]++
	}
	return out, nil
}

func (r *memoryRetryRepo) snapshot() []domain.NotifyRetryEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.NotifyRetryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// ── 场景过滤 ────────────────────────────────────────────────────────────────

// TestMatchesSceneFilter 覆盖验收 ①②的核心判定。
func TestMatchesSceneFilter(t *testing.T) {
	cases := []struct {
		name     string
		scenes   string
		category string
		want     bool
	}{
		// 默认全开：存量渠道没有 scenes 键，必须照旧全部送达。
		{"空白名单放行（存量渠道）", "", domain.NotificationCategoryStrmScanWarn, true},
		{"空白字符放行", "   ", domain.NotificationCategoryStrmScanWarn, true},

		// 命中场景。
		{"strm 命中 strm 场景", "strm", domain.NotificationCategoryStrmScanWarn, true},
		{"多值命中", "upload,strm,backup", domain.NotificationCategoryStrmScrapeWarn, true},
		{"带空格命中", "upload, strm ", domain.NotificationCategoryStrmScanWarn, true},

		// 未命中该场景。
		{"strm 不受 upload 开关影响", "upload", domain.NotificationCategoryStrmScanWarn, false},
		{"cas 不受 upload 开关影响", "upload", domain.NotificationCategoryCas, false},
		{"upload 命中", "upload", domain.NotificationCategoryUpload, true},

		// ⚠️ 未归类分类恒放行：关掉所有场景也不能静音账号认证失效。
		{"未归类：关掉全部场景仍放行", "", domain.NotificationCategoryAuth, true},
		{"未归类：选了别的场景仍放行", "upload,strm", domain.NotificationCategoryAuth, true},
		{"未归类：emby 播放仍放行", "backup", domain.NotificationCategoryEmbyPlayback, true},
		{"未归类：网盘挂载告警仍放行", "sync", domain.NotificationCategoryFuseMountWarn, true},
		{"未归类：自动化通知仍放行", "download", domain.NotificationCategoryAutomation, true},
	}
	for _, c := range cases {
		if got := matchesSceneFilter(c.scenes, c.category); got != c.want {
			t.Errorf("%s: matchesSceneFilter(%q,%q)=%v want %v", c.name, c.scenes, c.category, got, c.want)
		}
	}
}

// TestSceneSubscriptionRoundTrip scenes 键的编码/解析往返。
func TestSceneSubscriptionRoundTrip(t *testing.T) {
	want := []domain.NotificationScene{domain.SceneUpload, domain.SceneStrm}
	raw := domain.EncodeSceneSubscriptions(want)
	got, any := domain.ParseSceneSubscriptions(raw)
	if !any {
		t.Fatalf("解析 %q 失败", raw)
	}
	if len(got) != len(want) {
		t.Fatalf("往返长度不符: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("往返第 %d 项不符: got %v want %v", i, got, want[i])
		}
	}
	// 空集合 = 全订阅（而不是"订阅不到任何场景"）。
	if got, any := domain.ParseSceneSubscriptions(domain.EncodeSceneSubscriptions(nil)); any {
		t.Fatalf("空集合编码后被解析成 %v，应为空并被视为全订阅", got)
	}
	// 未知场景 ID 被丢弃而不是保留成脏值。
	got, any = domain.ParseSceneSubscriptions("upload,not_a_scene,strm")
	if !any || len(got) != 2 {
		t.Fatalf("未知场景未正确丢弃: got %v any=%v", got, any)
	}
}

// ── 补发入队（走真实 onNotificationCreated） ─────────────────────────────────

// testBus 装一条真事件总线与 dispatcher，返回事件投递的辅助函数。
func testBus(t *testing.T, channels []*domain.NotifyChannel, queue domain.NotifyRetryRepository) func(e eventbus.NotificationCreated) {
	t.Helper()
	bus := eventbus.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	d := NewDispatcher(&recordingChannelRepo{channels: channels}, nil)
	if queue != nil {
		d.SetRetryQueue(queue)
	}
	d.Register(bus)
	d.Refresh(context.Background())
	return func(e eventbus.NotificationCreated) { bus.Publish(context.Background(), e) }
}

func mustEventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// stubSender 把全局 Registry 里的某渠道换成可控替身，返回调用计数与还原函数。
//
// ⚠️ 必须**写回 map**：`Registry` 是 `map[string]struct{Meta; Send}`，
// 取出来的 `ch` 是一份结构体副本，`ch.Send = f` 只改副本，
// Registry 里的真 sender 纹丝不动。踩过这个坑：替身没生效，测试靠
// 真发一次 DNS 解析失败「假绿」通过 —— 断言的是失败原因，不是被测行为。
func stubSender(t *testing.T, channelID string, err error) (calls func() int, restore func()) {
	t.Helper()
	entry, ok := Registry[channelID]
	if !ok {
		t.Fatalf("渠道 %s 不在 Registry 里", channelID)
	}
	old := entry.Send
	var n int
	entry.Send = func(context.Context, map[string]string, Message) error {
		n++
		return err
	}
	Registry[channelID] = entry
	return func() int { return n }, func() {
		Registry[channelID] = struct {
			Meta ChannelMeta
			Send Sender
		}{Meta: entry.Meta, Send: old}
	}
}

// TestWebhookFailureEnqueuesRetry 验收 ③的前半：webhook 失败必须入队。
func TestWebhookFailureEnqueuesRetry(t *testing.T) {
	_, restore := stubSender(t, ChannelWebhook, errors.New("webhook HTTP 500"))
	defer restore()

	queue := newMemoryRetryRepo()
	ch := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "运维告警", Enabled: true,
		Config: `{"url":"https://hook.test/notify","method":"POST"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{ch}, queue)

	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryStrmScanWarn,
		Title: "STRM 扫描部分失败", Message: "3 个任务失败",
	})

	mustEventually(t, func() bool { return len(queue.snapshot()) == 1 },
		"webhook 发送失败后没有进入补发队列")

	e := queue.snapshot()[0]
	if e.ChannelType != ChannelWebhook {
		t.Fatalf("入队渠道类型 = %q，期望 webhook", e.ChannelType)
	}
	if e.TargetURL != "https://hook.test/notify" {
		t.Fatalf("入队 URL = %q，期望发送当时的地址快照", e.TargetURL)
	}
	if e.EventScene != domain.SceneStrm {
		t.Fatalf("入队场景 = %q，期望 strm（strm_scan_warn 归入 strm 场景）", e.EventScene)
	}
	if e.Attempts != 0 || e.Status != domain.NotifyRetryStatusPending {
		t.Fatalf("入队初始状态 = attempts=%d status=%q，期望 0/pending", e.Attempts, e.Status)
	}
	if e.LastError == "" {
		t.Fatal("入队没记下失败原因，补发队列就回答不了「为什么失败」")
	}
	// 首次重试时刻必须等于入队时刻 + 第一档退避。
	wait := e.NextRetryAt.Sub(time.Now())
	if wait < 30*time.Second || wait > 90*time.Second {
		t.Fatalf("首次重试间隔 = %v，期望约 1 分钟（第一档退避）", wait)
	}
}

// TestNonWebhookFailureDoesNotEnqueue 验收 ⑤：非 webhook 失败不进队列。
func TestNonWebhookFailureDoesNotEnqueue(t *testing.T) {
	calls, restore := stubSender(t, "telegram", errors.New("telegram 400 bad token"))
	defer restore()

	queue := newMemoryRetryRepo()
	ch := &domain.NotifyChannel{
		ID: 1, Type: "telegram", Name: "我的 Telegram", Enabled: true,
		Config: `{"bot_token":"x","chat_id":"1"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{ch}, queue)

	publish(eventbus.NotificationCreated{
		Level: "success", Category: domain.NotificationCategoryCas,
		Title: "CAS 自动转存成功", Message: "ok",
	})

	// 事件是异步的：先等到「确实尝试过发送并失败」这个信号，再断言队列为空。
	// 直接断言队列为空会假绿 —— 事件还没跑到，队列当然空。
	mustEventually(t, func() bool { return calls() == 1 },
		"telegram 渠道压根没尝试发送（测试会在没走到被测逻辑的情况下通过）")
	time.Sleep(50 * time.Millisecond)
	if entries := queue.snapshot(); len(entries) != 0 {
		t.Fatalf("telegram 失败后进了补发队列（%d 条）—— 只有 webhook 渠道才该补发", len(entries))
	}
}

// TestDisabledSceneBlocksWebhookAndRetry 验收 ②：关闭某场景后通知不发，
// 也就不会进入补发队列（没发过的东西没什么可补发的）。
func TestDisabledSceneBlocksWebhookAndRetry(t *testing.T) {
	calls, restore := stubSender(t, ChannelWebhook, errors.New("webhook HTTP 500"))
	defer restore()

	queue := newMemoryRetryRepo()
	c := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "只订 upload", Enabled: true,
		// scenes=upload：strm 场景被关掉。
		Config: `{"url":"https://hook.test/notify","method":"POST","scenes":"upload"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{c}, queue)

	// strm 场景的通知：被拦下。
	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryStrmScanWarn,
		Title: "STRM 扫描部分失败", Message: "x",
	})
	time.Sleep(100 * time.Millisecond)
	if calls() != 0 {
		t.Fatalf("strm 场景关闭后仍发送了 %d 次", calls())
	}
	if entries := queue.snapshot(); len(entries) != 0 {
		t.Fatalf("strm 场景关闭后仍进了补发队列 %d 条", len(entries))
	}

	// 同一渠道订阅的 upload 场景：不受影响，照发照补发。
	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryUpload,
		Title: "上传任务未全部创建", Message: "y",
	})
	mustEventually(t, func() bool { return len(queue.snapshot()) == 1 },
		"upload 场景的通知没有进入补发队列（场景过滤误伤了已订阅场景）")
	if calls() == 0 {
		t.Fatal("upload 场景的通知压根没尝试发送")
	}
}

// TestUnmappedCategoryAlwaysDelivered 关掉全部场景，账号认证失效仍要送达。
func TestUnmappedCategoryAlwaysDelivered(t *testing.T) {
	sent, restore := stubSender(t, ChannelWebhook, nil)
	defer restore()

	c := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "只订 upload", Enabled: true,
		Config: `{"url":"https://hook.test/notify","method":"POST","scenes":"upload"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{c}, nil)
	publish(eventbus.NotificationCreated{
		Level: "error", Category: domain.NotificationCategoryAuth,
		Title: "账号认证失效", Message: "z",
	})
	mustEventually(t, func() bool { return sent() == 1 },
		"账号认证失效这条告警被场景开关静音了 —— 未归类分类必须恒送达")
}

// TestNoRetryQueueConfiguredIsHarmless 没有注入队列时 webhook 失败不得 panic，
// 也只记日志。
func TestNoRetryQueueConfiguredIsHarmless(t *testing.T) {
	calls, restore := stubSender(t, ChannelWebhook, errors.New("webhook HTTP 500"))
	defer restore()

	c := &domain.NotifyChannel{
		ID: 1, Type: ChannelWebhook, Name: "无队列", Enabled: true,
		Config: `{"url":"https://hook.test/notify","method":"POST"}`,
	}
	publish := testBus(t, []*domain.NotifyChannel{c}, nil)
	publish(eventbus.NotificationCreated{
		Level: "warning", Category: domain.NotificationCategoryStrmScanWarn,
		Title: "t", Message: "m",
	})
	mustEventually(t, func() bool { return calls() == 1 },
		"没有注入补发队列时连发送都没走到")
}
