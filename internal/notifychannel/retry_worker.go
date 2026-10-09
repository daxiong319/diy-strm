package notifychannel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"litepan/internal/domain"
)

// 补发（outbox）退避表：第 n 次失败后等多久再发。
//
// 取自 参考实现 文档的 1m/5m/30m/2h/12h 五档，最后一档即用尽 ——
// 第 6 次失败落 failed 等人工介入。
//
// 刻意**不做指数退避 × 随机抖动**：文档给的是固定五档，实现成别的形状
// 就对不上了；而且这里每轮最多 5 次重试，抖动带来的削峰收益远小于
// 「实现结果与文档不一致」带来的排查成本。
var notifyRetryBackoff = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	12 * time.Hour,
}

// notifyRetryMaxAttempts 等于退避表长度，即用尽前的总尝试次数。
// 刻意做成 var 而非 const：len() 不是常量表达式，且与退避表绑在一起
// 改表时不会漏改这个数。
var notifyRetryMaxAttempts = len(notifyRetryBackoff)

// notifyRetryPollInterval worker 轮询间隔。
//
// 退避最短 1 分钟，所以 30s 一轮足够：最坏情况下多等 30 秒，
// 换来的是空转频率降到 1/60。
const notifyRetryPollInterval = 30 * time.Second

// notifyRetryBatch 单轮最多处理多少条。刻意小于单轮间隔内的产生量上限，
// 避免一个坏地址堆积时一轮发太久、拖住后面的记录。
const notifyRetryBatch = 20

// NotifyRetryMaxAttempts 导出最大尝试次数，供管理台显示「第 3 / 5 次」。
func NotifyRetryMaxAttempts() int { return notifyRetryMaxAttempts }

// backoffFor 返回第 attempts 次失败后应等待的时长。
//
// attempts 从 1 起（已经失败过一次）。超过退避表长度返回 false = 用尽。
func backoffFor(attempts int) (time.Duration, bool) {
	if attempts < 1 || attempts > len(notifyRetryBackoff) {
		return 0, false
	}
	return notifyRetryBackoff[attempts-1], true
}

// RetryWorker 后台补发 worker：把到点未送达的 webhook 通知重发出去。
//
// ⚠️ 为什么必须是独立 goroutine，不能 inline 在 onNotificationCreated 里：
// 事件总线只有一个消费者 goroutine（eventbus/bus.go 的 run），而它的队列
// 缓冲只有 256 且 Publish 在队满时**阻塞调用方且不看 ctx**。在那里做
// HTTP 重试会卡住后续所有事件的所有订阅者，队列一满整个通知系统停止呼吸。
type RetryWorker struct {
	repo domain.NotifyRetryRepository
	log  *slog.Logger

	// send 重发一条，用与首次投递完全一致的 cfg（快照）。
	send func(ctx context.Context, channelType, channelConfig string, msg Message) error

	// now 便于测试注入假时钟。nil 时用 time.Now。
	now func() time.Time
	// tick 便于测试缩短轮询间隔。nil 时用 notifyRetryPollInterval。
	tick time.Duration

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewRetryWorker 构造补发 worker。send 为 nil 时 worker 直接不启动
// （没有投递通道的 worker 只会把记录反复标失败）。
func NewRetryWorker(repo domain.NotifyRetryRepository, log *slog.Logger, send func(ctx context.Context, channelType, channelConfig string, msg Message) error) *RetryWorker {
	if log == nil {
		log = slog.Default()
	}
	w := &RetryWorker{repo: repo, log: log, send: send, now: time.Now, tick: notifyRetryPollInterval}
	return w
}

// SetClock 注入假时钟与轮询间隔（仅测试用）。
func (w *RetryWorker) SetClock(now func() time.Time, tick time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if now != nil {
		w.now = now
	}
	if tick > 0 {
		w.tick = tick
	}
}

// Start 启动后台循环。重复调用只生效一次（与 StartChannelWatcher 的 sync.Once 同风格）。
func (w *RetryWorker) Start(ctx context.Context) {
	if w == nil || w.repo == nil || w.send == nil {
		return
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go func() {
		defer close(done)
		w.loop(runCtx)
	}()
	w.log.Info("通知补发 worker 已启动", "interval", w.tick, "max_attempts", notifyRetryMaxAttempts)
}

// Stop 停止后台循环并等待其退出。
func (w *RetryWorker) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	w.mu.Lock()
	w.started, w.cancel, w.done = false, nil, nil
	w.mu.Unlock()
}

func (w *RetryWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce 跑一轮补发。导出是因为管理台的「立即重试」按钮要能手动触发一轮，
// 以及测试要能驱动它而不必启动 goroutine。
func (w *RetryWorker) RunOnce(ctx context.Context) {
	if w == nil || w.repo == nil || w.send == nil {
		return
	}
	due, err := w.repo.Due(ctx, w.now(), notifyRetryBatch)
	if err != nil {
		w.log.Warn("查询待补发通知失败", "err", err)
		return
	}
	for _, e := range due {
		w.retryOne(ctx, e)
	}
}

// retryOne 重发一条并推进状态。
//
// 状态机只有四步：pending →（到点重发成功）→ sent；pending →（失败且退避
// 未用尽）→ pending（next_retry_at 推后）；pending →（失败且用尽 / 失败且
// 判定不可重试）→ failed。sent 与 failed 都是终态，只有 failed 能被管理台
// 手动重投。
func (w *RetryWorker) retryOne(ctx context.Context, e domain.NotifyRetryEntry) {
	// 场景字段从记录快照里的 EventScene 取，而不是重新查 category：
	// 补发可能发生在几小时后，那时 catalog 未必没变，而用户看到的
	// 必须与首次投递时渲染出来的完全一致。
	sceneLabel := ""
	if label, ok := domain.SceneLabel(e.EventScene); ok {
		sceneLabel = label
	}
	msg := Message{
		Title:      e.Title,
		Content:    e.Content,
		Tone:       e.Tone,
		Scene:      string(e.EventScene),
		SceneLabel: sceneLabel,
	}
	// 单条超时：与首次投递同口径（dispatcher 里是 15s）。
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := w.send(sendCtx, e.ChannelType, e.ChannelConfig, msg)
	cancel()

	attempts := e.Attempts + 1
	if err == nil {
		if uerr := w.repo.UpdateResult(ctx, e.ID, attempts, time.Time{}, "",
			domain.NotifyRetryStatusSent); uerr != nil {
			w.log.Warn("标记补发成功失败", "id", e.ID, "err", uerr)
		}
		w.log.Info("通知补发成功", "id", e.ID, "channel", e.ChannelName, "attempts", attempts)
		return
	}

	// 不可重试的失败（4xx、配置缺失、未知渠道）当场判死：
	// 继续退避只是让用户多等五档（约 15 小时）才看到同一个必然失败的请求。
	if !RetryableError(err) {
		w.markFailed(ctx, e, attempts, err)
		return
	}

	delay, hasMore := backoffFor(attempts)
	if !hasMore {
		w.markFailed(ctx, e, attempts, err)
		return
	}

	next := w.now().Add(delay)
	if uerr := w.repo.UpdateResult(ctx, e.ID, attempts, next, err.Error(),
		domain.NotifyRetryStatusPending); uerr != nil {
		w.log.Warn("记录补发失败失败", "id", e.ID, "err", uerr)
	}
	w.log.Info("通知补发失败，等待下次重试", "id", e.ID, "channel", e.ChannelName,
		"attempts", attempts, "next_retry_in", delay, "err", err)
}

func (w *RetryWorker) markFailed(ctx context.Context, e domain.NotifyRetryEntry, attempts int, err error) {
	if uerr := w.repo.UpdateResult(ctx, e.ID, attempts, time.Time{}, err.Error(),
		domain.NotifyRetryStatusFailed); uerr != nil {
		w.log.Warn("标记补发失败失败", "id", e.ID, "err", uerr)
	}
	w.log.Warn("通知补发放弃，转人工处理", "id", e.ID, "channel", e.ChannelName,
		"attempts", attempts, "retryable", RetryableError(err), "err", err)
}
