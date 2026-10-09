package driver

import (
	"context"
	"sync"
	"time"
)

// CallObserver 观察每一次网盘 API 调用，用于风控熔断计数。
//
// 为什么挂在间隔门上而不是散落在各个 driver 里：9 个驱动各自调
// driver.WaitRequestInterval，而它最终都收敛到 DelayController.Gate 返回的这一个门。
// 收在这里，一处就能覆盖全部驱动；放进任一驱动的 transport 就必然漏。
type CallObserver interface {
	// RecordAPICall 记一次调用。它必须是**非阻塞**的：它在请求路径上，
	// 任何阻塞都会变成全局串行，把驱动拖垮。
	RecordAPICall(accountID int64)
}

// DelayController 按账号维度串行化 API 请求间隔，同一 accountID 的请求不会短于 interval 连续发出。
type DelayController struct {
	mu       sync.Mutex
	accounts map[int64]*accountDelay

	observerMu sync.RWMutex
	observer   CallObserver
}

type accountDelay struct {
	mu   sync.Mutex
	last time.Time
}

func NewDelayController() *DelayController {
	return &DelayController{accounts: make(map[int64]*accountDelay)}
}

// RequestIntervalGate 是注入到驱动实例的账号级间隔门，驱动在发起平台 API 前调用 Wait。
type RequestIntervalGate interface {
	Wait(ctx context.Context, interval time.Duration) error
}

type accountGate struct {
	dc        *DelayController
	accountID int64
}

func (g accountGate) Wait(ctx context.Context, interval time.Duration) error {
	// 计数发生在**等待之前**：interval<=0 时这一行也不该被跳过，
	// 否则用户把请求间隔配成 0（不限速）就会让熔断计数彻底失效 ——
	// 而不限速恰恰是最容易触发风控的配置。
	g.dc.recordCall(g.accountID)
	return g.dc.wait(ctx, g.accountID, interval)
}

// SetCallObserver 注入调用观察者（生产装配层传熔断器）。nil 表示不计数。
func (dc *DelayController) SetCallObserver(obs CallObserver) {
	dc.observerMu.Lock()
	dc.observer = obs
	dc.observerMu.Unlock()
}

func (dc *DelayController) recordCall(accountID int64) {
	dc.observerMu.RLock()
	obs := dc.observer
	dc.observerMu.RUnlock()
	if obs == nil {
		return
	}
	obs.RecordAPICall(accountID)
}

// Gate 返回绑定到指定账号的间隔门，供 Manager 注入驱动。
func (dc *DelayController) Gate(accountID int64) RequestIntervalGate {
	return accountGate{dc: dc, accountID: accountID}
}

// DropAccount 在账号删除时清理间隔状态，避免 map 无界增长。
func (dc *DelayController) DropAccount(accountID int64) {
	dc.mu.Lock()
	delete(dc.accounts, accountID)
	dc.mu.Unlock()
}

func (dc *DelayController) account(accountID int64) *accountDelay {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if ad, ok := dc.accounts[accountID]; ok {
		return ad
	}
	ad := &accountDelay{}
	dc.accounts[accountID] = ad
	return ad
}

func (dc *DelayController) wait(ctx context.Context, accountID int64, interval time.Duration) error {
	if interval <= 0 {
		return nil
	}

	ad := dc.account(accountID)
	ad.mu.Lock()
	defer ad.mu.Unlock()

	if !ad.last.IsZero() {
		if wait := interval - time.Since(ad.last); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}
	ad.last = time.Now()
	return nil
}

type extraDelayKey struct{}

// WithExtraAPIDelay 在 ctx 上叠加任务级额外 API 间隔（毫秒），与驱动自身 operation_delay 相加后统一节流。
func WithExtraAPIDelay(ctx context.Context, ms int) context.Context {
	if ms <= 0 {
		return ctx
	}
	return context.WithValue(ctx, extraDelayKey{}, ms)
}

// ExtraAPIDelayMS 读取 ctx 中的任务级额外间隔。
func ExtraAPIDelayMS(ctx context.Context) int {
	v, _ := ctx.Value(extraDelayKey{}).(int)
	return v
}

// WaitRequestInterval 在账号级间隔门上等待 baseMS + 任务额外间隔。
func WaitRequestInterval(ctx context.Context, gate RequestIntervalGate, baseMS int) error {
	if gate == nil {
		return nil
	}
	ms := baseMS + ExtraAPIDelayMS(ctx)
	if ms <= 0 {
		return nil
	}
	return gate.Wait(ctx, time.Duration(ms)*time.Millisecond)
}
