package guardrail

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// StateStore 是熔断状态的持久化接口。生产实现落数据库，重启后仍然生效。
//
// 为什么要持久化：熔断的意义是「别把账号用废」，而绕过风控最省事的办法
// 就是重启服务。只存在内存里的熔断等于没有熔断。
type StateStore interface {
	LoadState(ctx context.Context, accountID int64) (State, error)
	SaveState(ctx context.Context, accountID int64, s State) error
}

// MemoryStore 是只在内存里的实现，仅供测试与「明确不需要持久化」的场合。
type MemoryStore struct {
	mu     sync.Mutex
	states map[int64]State
}

// NewMemoryStore 返回一个空的内存实现。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[int64]State)}
}

func (m *MemoryStore) LoadState(_ context.Context, accountID int64) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[accountID], nil
}

func (m *MemoryStore) SaveState(_ context.Context, accountID int64, s State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[accountID] = s
	return nil
}

// JSONStore 把状态编码成 JSON 存进一个 KV 接口。
//
// 复用 store.Configs 而不是新建迁移表，理由：熔断状态是「每个账号一行的小 JSON」，
// 与已有的 account_auth_states 形态完全一致；而新建表要多一个迁移、多一个仓库文件、
// 多一处备份白名单判断，收益只有「不用 encode/decode」这一点。
type JSONStore struct {
	KV KVStore
	// Now 可注入，测试里换成固定时钟。
	Now func() time.Time
}

// KVStore 是 JSONStore 需要的最小 KV 接口。
type KVStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

// keyForState 返回熔断状态在 KV 里的键。带前缀是为了让备份/迁移工具一眼认出
// 哪些键是运行时状态而不是用户配置。
func keyForState(accountID int64) string {
	return "guardrail_circuit_account_" + itoa(accountID)
}

func (s *JSONStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// LoadState 读回状态。坏数据按「没有熔断」处理并顺手清掉：
// 一条解析不出来的熔断记录不该让目录监控永久停摆，也不该被反复读到。
func (s *JSONStore) LoadState(ctx context.Context, accountID int64) (State, error) {
	raw, ok, err := s.KV.Get(ctx, keyForState(accountID))
	if err != nil {
		return State{}, err
	}
	if !ok || raw == "" {
		return State{}, nil
	}
	var st State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		_ = s.KV.Set(ctx, keyForState(accountID), "")
		return State{}, nil
	}
	return st, nil
}

func (s *JSONStore) SaveState(ctx context.Context, accountID int64, st State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.KV.Set(ctx, keyForState(accountID), string(raw))
}

// ErrPaused 表示当前处于熔断暂停期，调用方应当跳过本轮且**不要补跑**。
var ErrPaused = errors.New("guardrail paused")

// breakerScopeID 是熔断状态在 KV 里的固定作用域。
//
// 为什么是全局一个熔断而不是按账号：风控的目的是保护**账号不被封**，
// 而熔断是「整体降低打网盘的频率」。按账号拆开的话，多账号部署只要有一个
// 账号触顶就暂停全部，等于把风控变成总闸；不拆又意味着最活跃的账号会把
// 其它账号一起拖停。选全局是刻意取「宁可停得保守」——
// 少扫一轮的代价远小于账号被封。
const breakerScopeID = 0

// Decision 是一次熔断检查的结论。
type Decision struct {
	// Paused 为 true 时本轮必须跳过。
	Paused bool
	// Reason 是给用户看的暂停原因；Paused 为 false 时是空串。
	Reason string
	// ResumeAt 是预计恢复时间；Paused 为 false 时是零值。
	ResumeAt time.Time
	// Triggered 说明这次暂停是本次检查新触发的（区别于「本来就在暂停期」）。
	Triggered bool
}

// Breaker 是挂在目录监控执行入口上的熔断器。
type Breaker struct {
	store StateStore
	guard Guardrail

	// now 可注入，测试里换成固定时钟；不要真 sleep。
	now func() time.Time

	mu sync.Mutex
	// inWindow 是本账号在当前窗口内累计的调用次数（内存计数，落库的是暂停状态）。
	inWindow int
	// windowStart 是当前窗口的起点。
	windowStart time.Time
	// workStart 是当前这一轮整理的起点；零值表示没有在整理。
	workStart time.Time
	// pausedUntil 是内存里的暂停到期时间，与落库状态合并使用。
	pausedUntil time.Time
	// pauseReason 是当前暂停的原因，跨重启后仍能显示给用户。
	pauseReason string
	// stateLoaded 标记持久状态是否已读入，避免每轮检查都打一次库。
	stateLoaded bool
}

// NewBreaker 组装一个熔断器。store 为 nil 时退化成纯内存模式，
// 但生产路径**必须**给一个持久的 store —— 见 StateStore 的注释。
func NewBreaker(store StateStore, guard Guardrail) *Breaker {
	return &Breaker{
		store:       store,
		guard:       guard.normalized(),
		now:         time.Now,
		windowStart: time.Time{},
	}
}

// SetClock 注入时钟，仅供测试。
func (b *Breaker) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	b.mu.Lock()
	b.now = now
	b.mu.Unlock()
}

// Guard 返回当前生效的阈值副本。
func (b *Breaker) Guard() Guardrail {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.guard
}

// SetGuard 热更新阈值。正在进行的暂停**不会被**缩短或延长 ——
// 改配置不应该成为绕过熔断的途径：暂停到期前改大阈值，熔断就白触发了。
func (b *Breaker) SetGuard(g Guardrail) {
	b.mu.Lock()
	b.guard = g.normalized()
	b.mu.Unlock()
}

// nowOrDefault 在**不持锁**的前提下取当前时间。持锁的代码路径必须直接调 b.now()，
// 绕这一层 —— 这里会再拿一次锁，在已持锁的路径上调用就是自死锁。
func (b *Breaker) nowOrDefault() time.Time {
	if b.now == nil {
		return time.Now()
	}
	return b.now()
}

// loadStateLocked 在熔断器尚未持有任何内存状态时，从持久层恢复暂停状态。
// 只在第一次检查时执行：之后状态都在内存里，反复读库没有意义。
func (b *Breaker) loadStateLocked(ctx context.Context) {
	if b.store == nil || b.stateLoaded {
		return
	}
	b.stateLoaded = true
	st, err := b.store.LoadState(ctx, breakerScopeID)
	if err != nil || st.PausedUntil.IsZero() {
		return
	}
	if b.nowOrDefault().Before(st.PausedUntil) {
		b.pausedUntil = st.PausedUntil
		b.pauseReason = st.Reason
	}
}

// Allow 是目录监控执行入口要调的第一个函数。
//
// 它做两件事：判断现在能不能跑这一轮，以及在跑之前把持久状态读进来。
// 返回 ErrPaused 时调用方**必须直接跳过**，不排队也不补跑 ——
// 补跑正是「连续调用」被触发的原因本身。
func (b *Breaker) Allow(ctx context.Context) error {
	d := b.Check(ctx)
	if !d.Paused {
		return nil
	}
	return &PauseError{Decision: d}
}

// PauseError 是 Allow 返回的错误，携带给人看的暂停原因与恢复时间。
type PauseError struct {
	Decision
}

func (e *PauseError) Error() string {
	if e.Reason == "" {
		return "风控熔断中，本轮已跳过"
	}
	return "风控熔断中，本轮已跳过：" + e.Reason
}

func (e *PauseError) Unwrap() error { return ErrPaused }

// Check 返回当前是否可以运行这一轮。
func (b *Breaker) Check(ctx context.Context) Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadStateLocked(ctx)
	now := b.now()
	if now.Before(b.pausedUntil) {
		return Decision{Paused: true, Reason: b.pauseReason, ResumeAt: b.pausedUntil}
	}
	return Decision{}
}

// RecordCall 记一次网盘接口调用，统计窗口内的累计次数；触顶时当场熔断。
//
// 为什么是「记一次调用就可能熔断」而不是「一轮结束时才看」：调用次数是
// 按窗口累计的，一轮扫描本身就能跑几十分钟，等到轮次结束再熔断等于让
// 最后一轮照旧打完网盘。
//
// 为什么熔断不影响**这一轮已经在做的事**：熔断只挡住下一轮的入口
// （Check/Allow），不中断进行中的请求。这样用户看到的是「这一轮跑完，
// 然后暂停一小时」，而不是文件搬到一半被冻住。
func (b *Breaker) RecordCall(ctx context.Context) Decision {
	b.mu.Lock()
	now := b.now()
	b.rollWindowLocked(now)
	b.inWindow++
	reached := b.guard.CallLimitReached(b.inWindow)
	calls := b.inWindow
	b.mu.Unlock()
	if !reached {
		return Decision{}
	}
	return b.trip(ctx, b.guard.callTripReason(calls), b.guard.CallPauseDuration())
}

func (g Guardrail) callTripReason(calls int) string {
	return "统计窗口内的网盘接口调用次数已达上限（" + itoa(int64(calls)) + " 次）"
}

// rollWindowLocked 在窗口过期时把计数清零。
func (b *Breaker) rollWindowLocked(now time.Time) {
	if b.windowStart.IsZero() {
		b.windowStart = now
		return
	}
	if now.Sub(b.windowStart) < b.guard.CallWindow() {
		return
	}
	b.windowStart = now
	b.inWindow = 0
}

// CallsInWindow 返回当前窗口内已记的调用次数，供界面展示。
func (b *Breaker) CallsInWindow() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollWindowLocked(b.now())
	return b.inWindow
}

// BeginRun 标记一轮整理开始，用于统计连续整理时长。
//
// 已在暂停期时返回 false 且**不**覆盖 workStart —— 否则一次被跳过的触发
// 会把起点重置成现在，等于每次触发都替用户「续命」一次暂停期。
func (b *Breaker) BeginRun(ctx context.Context) bool {
	d := b.Check(ctx)
	if d.Paused {
		return false
	}
	b.mu.Lock()
	b.workStart = b.now()
	b.mu.Unlock()
	return true
}

// FinishRun 结束一轮整理，并按连续时长决定是否熔断。
func (b *Breaker) FinishRun(ctx context.Context) Decision {
	b.mu.Lock()
	started := b.workStart
	b.workStart = time.Time{}
	now := b.now()
	b.mu.Unlock()
	if started.IsZero() {
		return Decision{}
	}
	if !b.guard.WorkLimitReached(now.Sub(started)) {
		return Decision{}
	}
	return b.trip(ctx, b.guard.workTripReason(now.Sub(started)), b.guard.WorkPauseDuration())
}

func (g Guardrail) workTripReason(elapsed time.Duration) string {
	return "连续整理时长已超过上限（" + itoa(int64(elapsed.Minutes())) + " 分钟）"
}

// TripCalls 是给「调用次数在别处累计」的场景准备的入口：reached 为真时熔断。
// RecordCall 自己就会熔断，这个方法留给还没接 RecordCall 的老调用点。
func (b *Breaker) TripCalls(ctx context.Context, reached bool) Decision {
	if !reached {
		return Decision{}
	}
	return b.trip(ctx, b.guard.callTripReason(b.CallsInWindow()), b.guard.CallPauseDuration())
}

// Trip 是手动熔断入口（管理台上的「立即暂停」按钮）。
func (b *Breaker) Trip(ctx context.Context, reason string, pause time.Duration) Decision {
	if pause <= 0 {
		pause = b.guard.CallPauseDuration()
	}
	return b.trip(ctx, reason, pause)
}

// trip 执行熔断并把状态落库。
//
// 暂停时长在这里被截到文档上限：调用方可能传进来一个更大的值（未来的设置项、
// 手写 API 调用），而「最长 24 小时」是一条不能被绕过的约束。
func (b *Breaker) trip(ctx context.Context, reason string, pause time.Duration) Decision {
	b.mu.Lock()
	now := b.now()
	until := now.Add(pause)
	if max := time.Duration(MaxPauseSecondsCap) * time.Second; until.Sub(now) > max {
		until = now.Add(max)
	}
	b.pausedUntil = until
	b.pauseReason = reason
	b.inWindow = 0
	b.windowStart = now
	b.mu.Unlock()

	st := State{PausedUntil: until, Reason: reason, TriggeredAt: now}
	if b.store != nil {
		if err := b.store.SaveState(ctx, breakerScopeID, st); err != nil {
			// 落库失败**不**撤销熔断。方向很重要：宁可多暂停一轮，
			// 也不能因为数据库抖了一下就把账号继续往网盘上打。
			st.PersistFailed = true
		}
	}
	return Decision{Paused: true, Reason: reason, ResumeAt: until, Triggered: true}
}

// Status 是给界面看的熔断状态快照。
type Status struct {
	Paused      bool      `json:"paused"`
	Reason      string    `json:"reason"`
	ResumeAt    time.Time `json:"resume_at"`
	CallsWindow int       `json:"calls_in_window"`
	CallsLimit  int       `json:"calls_limit"`
}

// Status 返回当前状态快照。
func (b *Breaker) Status(ctx context.Context) Status {
	d := b.Check(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	return Status{
		Paused:      d.Paused,
		Reason:      d.Reason,
		ResumeAt:    d.ResumeAt,
		CallsWindow: b.inWindow,
		CallsLimit:  b.guard.MaxCallsPerWindow,
	}
}

// Clear 手动解除熔断（管理台上的「立即恢复」）。
func (b *Breaker) Clear(ctx context.Context) {
	b.mu.Lock()
	b.pausedUntil = time.Time{}
	b.pauseReason = ""
	b.inWindow = 0
	b.windowStart = b.now()
	b.mu.Unlock()
	if b.store != nil {
		_ = b.store.SaveState(ctx, breakerScopeID, State{})
	}
}

// RecordAPICall 实现 driver.CallObserver：每记一次就可能熔断。
//
// 它在**网盘请求路径**上被调用（driver.DelayController 的间隔门里），所以必须
// 非阻塞：熔断要落库，落库是磁盘 IO，绝不能挂在每一次 API 请求前面等它。
// 这里的做法是「内存里立刻置为暂停（这一轮之后的入口马上就被挡住），
// 落库丢给一个后台 goroutine」。
//
// 这也是为什么内存里的暂停状态必须先于持久化生效：落库失败时
// 我们照样已经停下来了，只是重启后可能不记得（见 State.PersistFailed）。
func (b *Breaker) RecordAPICall(accountID int64) {
	d := b.recordCall()
	if !d.Paused {
		return
	}
	pause := b.pauseSnapshot()
	b.persistAsync(pause)
}

// persistAsync 在后台落库。理由同 RecordAPICall 的注释：不能挡住请求路径。
// goroutine 不做优雅退出等待：进程退出时丢一次落库是可以接受的结果，
// 因为内存里的暂停已经把这一轮挡住了，落库只是为了让重启后还记得。
// 因为内存里的暂停已经把这一轮挡住了。
func (b *Breaker) persistAsync(s State) {
	if b.store == nil {
		return
	}
	go func() {
		if err := b.store.SaveState(context.Background(), breakerScopeID, s); err != nil {
			slog.Warn("风控熔断状态落库失败，本次暂停在重启后可能失效", "err", err)
		}
	}()
}

// pauseSnapshot 返回当前内存里的暂停状态，供异步落库使用。
func (b *Breaker) pauseSnapshot() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pausedUntil.IsZero() {
		return State{}
	}
	return State{PausedUntil: b.pausedUntil, Reason: b.pauseReason}
}

// recordCall 是 RecordCall 的非阻塞内核：只做内存计数与置暂停，不落库。
func (b *Breaker) recordCall() Decision {
	b.mu.Lock()
	now := b.now()
	b.rollWindowLocked(now)
	b.inWindow++
	reached := b.guard.CallLimitReached(b.inWindow)
	calls := b.inWindow
	if reached {
		b.pausedUntil = now.Add(b.guard.CallPauseDuration())
		b.pauseReason = b.guard.callTripReason(calls)
		b.inWindow = 0
		b.windowStart = now
	}
	d := Decision{Paused: reached, Reason: b.pauseReason, ResumeAt: b.pausedUntil, Triggered: reached}
	b.mu.Unlock()
	return d
}
