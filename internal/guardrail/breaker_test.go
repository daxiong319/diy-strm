package guardrail

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock 是可手动推进的时钟。**不要真 sleep** —— 真 sleep 会让这组用例
// 在 CI 上慢到没人愿意等，而且失败时的现场也不好读。
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestBreaker(t *testing.T, store StateStore, guard Guardrail) (*Breaker, *fakeClock) {
	t.Helper()
	b := NewBreaker(store, guard)
	clk := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b.SetClock(clk.Now)
	return b, clk
}

func TestPauseSecondsAreCappedAtTheDocumentedCeiling(t *testing.T) {
	// 86400 秒（24 小时）是文档确认的上限，配置里填再大也必须被截回去。
	if MaxPauseSecondsCap != 86400 {
		t.Fatalf("调用暂停上限 = %d，文档确认的是 86400", MaxPauseSecondsCap)
	}
	if MaxPauseMinutesCap != 1440 {
		t.Fatalf("整理暂停上限 = %d，文档确认的是 1440", MaxPauseMinutesCap)
	}
	g := Guardrail{MaxCallsPerWindow: 1, MaxPauseSeconds: 999999}.normalized()
	if g.MaxPauseSeconds != MaxPauseSecondsCap {
		t.Errorf("MaxPauseSeconds = %d，期望被截到 %d", g.MaxPauseSeconds, MaxPauseSecondsCap)
	}
	g2 := Guardrail{MaxWorkMinutes: 1, MaxPauseMinutes: 999999}.normalized()
	if g2.MaxPauseMinutes != MaxPauseMinutesCap {
		t.Errorf("MaxPauseMinutes = %d，期望被截到 %d", g2.MaxPauseMinutes, MaxPauseMinutesCap)
	}
}

func TestZeroLimitsMeanUnlimited(t *testing.T) {
	b, _ := newTestBreaker(t, nil, Guardrail{})
	for i := 0; i < 50; i++ {
		b.RecordCall(context.Background())
	}
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("全零阈值时不该暂停，却返回 %v", err)
	}
	if got := b.Status(context.Background()); got.CallsWindow != 50 {
		t.Errorf("计数 = %d，期望 50", got.CallsWindow)
	}
	// BeginRun 返回 bool（能不能开始），不是 error。顺手断言一下：
	// 全零阈值时它必须返回 true，否则「不限」会被误读成「一直跳过」。
	if !b.BeginRun(context.Background()) {
		t.Fatal("全零阈值时 BeginRun 应返回 true")
	}
}

func TestCallLimitTripsPauseAndSkipsTheRound(t *testing.T) {
	b, clk := newTestBreaker(t, NewMemoryStore(), Guardrail{MaxCallsPerWindow: 3, CallWindowSeconds: 3600, MaxPauseSeconds: 600})

	for i := 0; i < 2; i++ {
		b.RecordCall(context.Background())
	}
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("未到上限不该暂停：%v", err)
	}
	b.RecordCall(context.Background())
	if err := b.Allow(context.Background()); err == nil {
		t.Fatal("到上限后应暂停")
	} else if !errors.Is(err, ErrPaused) {
		t.Fatalf("错误 = %v，期望是 ErrPaused", err)
	}

	// 暂停期内即使继续记调用也仍然暂停。
	clk.advance(10 * time.Second)
	b.RecordCall(context.Background())
	if err := b.Allow(context.Background()); err == nil {
		t.Fatal("暂停期内必须继续暂停")
	}

	// 到期后恢复。
	clk.advance(10 * time.Minute)
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("暂停到期后应恢复：%v", err)
	}
}

func TestSkippedRoundDoesNotExtendThePause(t *testing.T) {
	// 「暂停期内跳过」如果顺手把整理起点重置成现在，等于每次触发都替用户
	// 续命一次暂停期，熔断就永远不结束。
	b, clk := newTestBreaker(t, NewMemoryStore(), Guardrail{MaxCallsPerWindow: 1, MaxPauseSeconds: 3600})
	b.RecordCall(context.Background())
	b.TripCalls(context.Background(), true)

	if b.BeginRun(context.Background()) {
		t.Fatal("暂停期内 BeginRun 应返回 false")
	}
	for i := 0; i < 10; i++ {
		b.BeginRun(context.Background())
		clk.advance(60 * time.Second)
	}
	// 十次「触发 + 跳过」共 10 分钟，暂停仍是原来的一小时。
	st := b.Status(context.Background())
	clk.advance(50 * time.Minute)
	if b.Status(context.Background()).Paused {
		t.Fatal("原始暂停时长应仍然生效，不该被反复触发续期")
	}
	if st.Paused != true {
		t.Fatalf("暂停状态 = %v", st.Paused)
	}
}

func TestPausedStateSurvivesRestart(t *testing.T) {
	// 「重启服务」是最省事的绕过风控的办法，所以熔断必须落库。
	kv := &fakeKV{}
	store := &JSONStore{KV: kv}
	guard := Guardrail{MaxCallsPerWindow: 2, MaxPauseSeconds: 7200}

	first, clk := newTestBreaker(t, store, guard)
	first.Trip(context.Background(), "手动熔断", 2*time.Hour)
	if !first.Status(context.Background()).Paused {
		t.Fatal("熔断后应处于暂停")
	}

	// 模拟重启：全新熔断器，同一个 KV。
	second, _ := newTestBreaker(t, &JSONStore{KV: kv}, guard)
	second.now = clk.Now
	if !second.Status(context.Background()).Paused {
		t.Fatal("重启后熔断状态必须仍在")
	}
	if err := second.Allow(context.Background()); err == nil {
		t.Fatal("重启后仍在暂停期，Allow 必须拒绝")
	}
}

func TestExpiredPauseIsNotRestored(t *testing.T) {
	kv := &fakeKV{}
	store := &JSONStore{KV: kv}
	first, clk := newTestBreaker(t, store, Guardrail{MaxPauseSeconds: 600})
	first.Trip(context.Background(), "手动", time.Minute)

	clk.advance(2 * time.Minute)
	second, _ := newTestBreaker(t, store, Guardrail{MaxPauseSeconds: 600})
	second.now = clk.Now
	if second.Status(context.Background()).Paused {
		t.Fatal("暂停已过期，重启后不该恢复成暂停")
	}
}

func TestCorruptPersistedStateDoesNotWedgeTheMonitor(t *testing.T) {
	kv := &fakeKV{raw: map[string]string{keyForState(breakerScopeID): "{不是 json"}}
	b, _ := newTestBreaker(t, &JSONStore{KV: kv}, Guardrail{})
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("坏数据不该让目录监控停摆：%v", err)
	}
	if got := kv.get(keyForState(breakerScopeID)); got != "" {
		t.Errorf("坏数据应被清掉，实际留下 %q", got)
	}
}

func TestWorkDurationLimit(t *testing.T) {
	b, clk := newTestBreaker(t, NewMemoryStore(), Guardrail{MaxWorkMinutes: 10, MaxPauseMinutes: 120})
	if !b.BeginRun(context.Background()) {
		t.Fatal("正常状态下应允许开始")
	}
	clk.advance(5 * time.Minute)
	if d := b.FinishRun(context.Background()); d.Paused {
		t.Fatal("未到时长上限不该熔断")
	}
	if err := b.Allow(context.Background()); err != nil {
		t.Fatalf("未熔断时不该暂停：%v", err)
	}

	b.Clear(context.Background())
	if !b.BeginRun(context.Background()) {
		t.Fatal("解除后应能开始")
	}
	clk.advance(30 * time.Minute)
	d := b.FinishRun(context.Background())
	if !d.Paused {
		t.Fatal("超过时长上限应熔断")
	}
	if err := b.Allow(context.Background()); err == nil {
		t.Fatal("时长熔断后应暂停")
	}
}

func TestFinishRunWithoutBeginDoesNothing(t *testing.T) {
	b, _ := newTestBreaker(t, NewMemoryStore(), Guardrail{MaxWorkMinutes: 1})
	if d := b.FinishRun(context.Background()); d.Paused {
		t.Fatal("没有开始过就不该有结束，更不该熔断")
	}
}

func TestPersistFailureStillPauses(t *testing.T) {
	// 方向很重要：宁可多暂停一轮，也不能因为数据库抖了一下就把账号继续往网盘上打。
	b, _ := newTestBreaker(t, &failingStore{}, Guardrail{})
	d := b.Trip(context.Background(), "手动", time.Hour)
	if !d.Paused {
		t.Fatal("落库失败时熔断必须仍然生效")
	}
	if !b.Status(context.Background()).Paused {
		t.Fatal("落库失败后状态应仍是暂停")
	}
}

func TestSmallFileQuarantineRules(t *testing.T) {
	g := Guardrail{MinMediaSizeBytes: 10 * 1024 * 1024}
	if !g.ShouldQuarantineSmallFile(1024, true) {
		t.Error("move 模式下低于阈值的文件应被移走")
	}
	if g.ShouldQuarantineSmallFile(1024, false) {
		t.Error("copy 模式不应移走源文件")
	}
	if g.ShouldQuarantineSmallFile(20*1024*1024, true) {
		t.Error("高于阈值不应移走")
	}
	if (Guardrail{}).ShouldQuarantineSmallFile(1, true) {
		t.Error("阈值为 0 表示不启用，不该移走任何文件")
	}
	if (Guardrail{MinMediaSizeBytes: -1}).normalized().ShouldQuarantineSmallFile(1, true) {
		t.Error("负阈值应被归零，即不启用")
	}
}

func TestSetGuardDoesNotShortenAnActivePause(t *testing.T) {
	// 改配置不应该成为绕过熔断的途径：暂停到期前把阈值调大，熔断就白触发了。
	b, _ := newTestBreaker(t, NewMemoryStore(), Guardrail{MaxCallsPerWindow: 1, MaxPauseSeconds: 3600})
	b.Trip(context.Background(), "手动", 2*time.Hour)
	b.SetGuard(Guardrail{MaxCallsPerWindow: 100000, MaxPauseSeconds: 1})
	if !b.Status(context.Background()).Paused {
		t.Fatal("热更新阈值不应解除正在进行的暂停")
	}
}

// fakeKV 是内存 KV，用于验证持久化确实发生了，而不是被内存实现掩盖掉。
type fakeKV struct{ raw map[string]string }

func (f *fakeKV) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := f.raw[key]
	return v, ok, nil
}

func (f *fakeKV) Set(_ context.Context, key, value string) error {
	if f.raw == nil {
		f.raw = map[string]string{}
	}
	f.raw[key] = value
	return nil
}

func (f *fakeKV) get(key string) string { return f.raw[key] }

type failingStore struct{}

func (failingStore) LoadState(context.Context, int64) (State, error) { return State{}, nil }
func (failingStore) SaveState(context.Context, int64, State) error {
	return errors.New("数据库不可用")
}
