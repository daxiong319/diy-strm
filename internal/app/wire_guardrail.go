package app

import (
	"time"

	"litepan/internal/driver"
	"litepan/internal/guardrail"
	"litepan/internal/logx"
	"litepan/internal/settings"
)

// wireGuardrail 构造熔断器并接进驱动管理器。
//
// 这里有两处「不接就等于没做」的接线，少任何一处风控都只是假的安全感：
//
//  1. core.drivers.SetCallObserver(brk)：全部 9 个驱动的 API 调用最终都过
//     DelayController 的间隔门，装配时不注入观察者，调用次数就永远不会计数。
//  2. 状态挂在 st.store.Configs 而不是内存：重启必须是账号被封时的后门吗？不是 ——
//     它必须是绕过风控的后门，所以状态必须跨重启存活（验收项 3）。
//
// 熔断器按「每个 scope 一行」设计，本期只有一个 scope（scopeID 0）：风控针对的是
// 目录监控这个整体，而不是单个网盘账号 —— 一个账号被封时同链路上往往已经挨过一轮了。
func wireGuardrail(st *storeBundle, mgr *driver.Manager, logs *logx.Manager) *guardrail.Breaker {
	log := logs.For(logx.ModuleSystem)
	brk := guardrail.NewBreaker(
		&guardrail.JSONStore{KV: st.store.Configs, Now: time.Now},
		guardrailFromSettings(st.settings),
	)
	if mgr != nil {
		mgr.SetCallObserver(brk)
	}
	g := brk.Guard()
	log.Info("风控熔断已接线",
		"max_calls_per_window", g.MaxCallsPerWindow,
		"call_window_seconds", g.CallWindowSeconds,
		"call_pause_seconds", g.MaxPauseSeconds,
		"max_work_minutes", g.MaxWorkMinutes,
		"work_pause_minutes", g.MaxPauseMinutes,
		"min_media_size_bytes", g.MinMediaSizeBytes,
	)
	return brk
}

// guardrailFromSettings 把设置读成一份阈值快照。
//
// 六个键都允许 0（不限）。这一点必须在界面上同时写出来 ——「填 0 就是没有风控」
// 比「填 0 会怎样」更不容易让人误配成「打开但没生效」。
func guardrailFromSettings(set *settings.Service) guardrail.Guardrail {
	if set == nil {
		return guardrail.Guardrail{}
	}
	return guardrail.Guardrail{
		MaxCallsPerWindow: set.Int(settings.KeyMOScrapeMaxCallsPerWindow),
		CallWindowSeconds: set.Int(settings.KeyMOScrapeCallWindowSeconds),
		MaxPauseSeconds:   set.Int(settings.KeyMOScrapeCallPauseSeconds),
		MaxWorkMinutes:    set.Int(settings.KeyMOScrapeMaxWorkMinutes),
		MaxPauseMinutes:   set.Int(settings.KeyMOScrapeWorkPauseMinutes),
		MinMediaSizeBytes: int64(set.Int(settings.KeyMOMinMediaSizeBytes)),
	}
}

// 编译期断言：熔断器必须能被当作驱动层的调用观察者。
// 这一条不是形式主义 —— 观察者接口在 driver 包、实现在 guardrail 包，
// 少一个方法只会表现为「计数静默为 0」，而那正是熔断看起来在跑、实际没跑的样子。
var _ driver.CallObserver = (*guardrail.Breaker)(nil)

// 编译期断言：熔断状态的持久层必须满足 guardrail.StateStore，
// 且它承载的正是仓储里的通用配置表 —— 熔断状态因此不需要新表，
// 写进 store.Configs 就能活过重启（验收项 3），也不会漏进备份白名单之外的表。
var _ guardrail.StateStore = (*guardrail.JSONStore)(nil)
