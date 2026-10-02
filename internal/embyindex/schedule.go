package embyindex

import (
	"context"
	"time"

	"litepan/internal/domain"
	"litepan/pkg/safego"
)

// defaultScanInterval 是周期扫描的兜底间隔。
//
// 取 5 分钟：Emby 媒体库的条目变化通常来自 CAS 转存后的 STRM 落盘，
// 分钟级延迟足够；比老版 60 秒的 ticker 更保守，避免无意义地反复拉取
// 大型媒体库（老版每次 tick 都要判断一次就绪条件，现版直接整轮扫描）。
const defaultScanInterval = 5 * time.Minute

// ScanOnce 立刻执行一轮扫描（增量优先）。
//
// 这是给触发器与测试用的同步入口：它不启动循环，只跑一轮并返回结果。
// 返回 ok=false 表示当前不具备扫描条件（未启用、无可用配置、已有扫描在跑）。
//
// 增量与全量的选择：库里从未同步过（无游标）时走全量，否则走增量。
// 这与老版「首次全量、之后增量」的行为一致。
func (s *Service) ScanOnce(ctx context.Context) (SyncResult, bool) {
	if s == nil || s.index == nil {
		return SyncResult{}, false
	}
	if !s.SyncEnabled() {
		return SyncResult{}, false
	}
	cfg, ok := s.currentConfig(ctx)
	if !ok {
		return SyncResult{}, false
	}

	// 已有扫描在跑时直接放弃本轮，而不是排队：
	// 周期扫描本身是幂等的，下一轮再扫即可，排队只会堆积。
	if !s.syncRunning.CompareAndSwap(false, true) {
		s.log.Debug("已有 Emby 条目同步正在运行，跳过本轮周期扫描")
		return SyncResult{}, false
	}
	s.syncRunning.Store(false)

	cursor, err := s.index.GetSyncCursor(ctx, cfg.ConfigID)
	if err != nil {
		s.log.Warn("读取 Emby 增量同步游标失败，本轮改为全量扫描", "config_id", cfg.ConfigID, "err", err)
		cursor = domain.EmbySyncCursor{}
	}
	if cursor.LastSavedCursorAt <= 0 {
		result, err := s.PerformEmbySync(ctx, cfg)
		if err != nil {
			s.log.Warn("Emby 条目全量同步失败", "config_id", cfg.ConfigID, "err", err)
			return result, false
		}
		return result, true
	}
	result, err := s.PerformEmbyIncrementalSync(ctx, cfg)
	if err != nil {
		s.log.Warn("Emby 条目增量同步失败", "config_id", cfg.ConfigID, "err", err)
		return result, false
	}
	return result, true
}

// RequestScan 请求立刻扫一轮（异步、信号可合并）。
//
// 典型调用方是 Emby webhook：收到入库事件后除了登记刷新意图，
// 还可以让索引尽快跟上，避免「Emby 已有条目但本地索引还没有」的窗口。
// 循环未启动时该请求会被丢弃，不会阻塞调用方。
func (s *Service) RequestScan() {
	if s == nil || s.scanRequest == nil {
		return
	}
	select {
	case s.scanRequest <- struct{}{}:
	default:
		// 容量 1 且已有待处理信号，说明马上就会扫一轮，无需重复投递。
	}
}

// Start 启动周期扫描循环。重复调用是安全的空操作。
//
// 与 embyrefresh / embywebhook 的 Start 约定一致：由 App.Run 调用，
// ctx 结束时循环退出；nil 服务直接返回，不影响调用方。
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if !s.loopRunning.CompareAndSwap(false, true) {
		return
	}
	go s.schedulerLoop(ctx)
}

// Stop 停止周期扫描并取消在途扫描。重复调用是安全的。
//
// 只取消扫描，不等待：扫描本身对 ctx 取消敏感（performSync 每轮循环检查 ctx.Err），
// 未落库的部分会在下一轮重新扫到，因此无需阻塞关闭流程。
func (s *Service) Stop(ctx context.Context) {
	if s == nil {
		return
	}
	s.scanMu.Lock()
	cancel := s.scanCancel
	s.scanCancel = nil
	s.scanMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.loopRunning.Store(false)
}

// schedulerLoop 是周期扫描主循环。
func (s *Service) schedulerLoop(ctx context.Context) {
	defer s.loopRunning.Store(false)

	interval := s.scanInterval
	if interval <= 0 {
		interval = defaultScanInterval
	}

	// 兜住单轮崩溃：一轮出错只跳过这一轮，不能让整个服务下线。
	runOnce := func() {
		safego.Guard(s.log, "embyindex.schedule", func() { s.scanRound(ctx) })
	}

	// 启动后先扫一轮，让重启后能尽快把索引补齐。
	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		case <-s.scanRequest:
			runOnce()
		}
	}
}

// scanRound 执行一轮带可取消上下文的扫描。
//
// 单独包一层是为了把「本轮扫描的 cancel」登记到 Service 上，
// 这样 Stop 能中断正在进行的一轮，而不必等待扫描自然结束。
func (s *Service) scanRound(ctx context.Context) {
	if !s.SyncEnabled() {
		return
	}
	scanCtx, cancel := context.WithCancel(ctx)
	s.scanMu.Lock()
	s.scanCancel = cancel
	s.scanMu.Unlock()
	defer func() {
		cancel()
		s.scanMu.Lock()
		s.scanCancel = nil
		s.scanMu.Unlock()
	}()

	if _, ok := s.ScanOnce(scanCtx); !ok {
		s.log.Debug("本轮 Emby 条目扫描未执行")
	}
}
