package embyrefresh

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
)

// Refresher 是提交刷新所需的 Emby/Jellyfin 能力，由 embyproxy.Service 实现。
// 只声明本包真正用到的方法，避免与 embyproxy 形成强耦合。
type Refresher interface {
	RefreshLibrary(ctx context.Context, req RefreshRequest) (RefreshResult, error)
}

// RefreshRequest 与 embyproxy.RefreshRequest 字段对齐，用于避免跨包类型耦合。
type RefreshRequest struct {
	ConfigID  string
	Mode      string
	LibraryID string
	ItemID    string
}

// RefreshResult 是刷新结果的最小投影：只保留任务终态需要的信息。
type RefreshResult struct {
	Mode      string
	LibraryID string
	ItemID    string
}

// Libraries 提供条目所属媒体库的解析能力，由 embyproxy.Service 实现。
type Libraries interface {
	ListLibraries(ctx context.Context, configIDs ...string) ([]Library, error)
}

// Library 是媒体库的最小投影。
type Library struct {
	ID   string
	Name string
}

// Options 构造服务所需依赖。
type Options struct {
	Tasks     domain.EmbyRefreshTaskRepository
	Refresher Refresher
	Libraries Libraries
	// ConfigID 是默认使用的 Emby/Jellyfin 配置；为空时由 RefreshLibrary 内部回退到默认配置。
	ConfigID string
	// Debounce 覆盖默认防抖窗口，<=0 时使用 DefaultDebounce。
	Debounce time.Duration
	// MaxWait 覆盖默认最长等待时间，<=0 时使用 DefaultMaxWait。
	MaxWait time.Duration
	// ScanInterval 覆盖默认扫描周期，<=0 时使用 DefaultScanInterval。
	ScanInterval time.Duration
	// AggregationThreshold 覆盖条目合并阈值。0 表示使用默认阈值
	// ItemAggregationThreshold；负数表示关闭条目合并。
	AggregationThreshold int
	// Now 便于测试注入时钟，nil 时使用 time.Now。
	Now func() time.Time
	Log *slog.Logger
}

// Service 提供刷新意图登记与后台扫描执行。
type Service struct {
	tasks     domain.EmbyRefreshTaskRepository
	refresher Refresher
	libraries Libraries
	configID  string
	debounce  time.Duration
	maxWait   time.Duration
	interval  time.Duration
	threshold int
	now       func() time.Time
	log       *slog.Logger

	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	wakeCh  chan struct{}
}

// New 构造刷新任务服务。tasks 为 nil 时返回 nil，便于调用方在未装配时跳过。
func New(opts Options) *Service {
	if opts.Tasks == nil {
		return nil
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		tasks:     opts.Tasks,
		refresher: opts.Refresher,
		libraries: opts.Libraries,
		configID:  strings.TrimSpace(opts.ConfigID),
		debounce:  opts.Debounce,
		maxWait:   opts.MaxWait,
		interval:  opts.ScanInterval,
		threshold: opts.AggregationThreshold,
		now:       opts.Now,
		log:       log,
	}
	if s.debounce <= 0 {
		s.debounce = DefaultDebounce
	}
	if s.maxWait <= 0 {
		s.maxWait = DefaultMaxWait
	}
	if s.interval <= 0 {
		s.interval = DefaultScanInterval
	}
	switch {
	case opts.AggregationThreshold == 0:
		s.threshold = ItemAggregationThreshold
	case opts.AggregationThreshold < 0:
		s.threshold = 0
	default:
		s.threshold = opts.AggregationThreshold
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// RequestRefresh 登记一次刷新意图。同一 TaskKey 的重复事件会顺延防抖窗口并合并 item_ids。
//
// 该方法是幂等的：对同一目标重复调用不会产生多余任务行。
func (s *Service) RequestRefresh(ctx context.Context, req RequestRefreshParams) error {
	if s == nil {
		return nil
	}
	targetType := strings.TrimSpace(req.TargetType)
	if targetType == "" {
		targetType = TargetTypeLibrary
	}
	now := s.now()
	task := &domain.EmbyRefreshTask{
		TargetType:   targetType,
		Status:       domain.EmbyRefreshStatusPending,
		LastEventAt:  now.Unix(),
		RefreshAfter: now.Add(s.debounce).Unix(),
		DeadlineAt:   now.Add(s.maxWait).Unix(),
		LibraryID:    strings.TrimSpace(req.LibraryID),
		LibraryName:  strings.TrimSpace(req.LibraryName),
	}
	switch targetType {
	case TargetTypeItem:
		itemID := strings.TrimSpace(req.ItemID)
		if itemID == "" {
			return domain.Errorf(domain.CodeValidation, "刷新条目 ID 不能为空")
		}
		task.TaskKey = ItemTaskKey(itemID)
		task.ItemIDs = encodeItemIDs([]string{itemID})
		// TODO: item 任务最好记录所属媒体库，以便条目刷新失败时降级到库刷新。
		// 现版的 embyproxy 只有库级列举（ListLibraries），没有「按条目反查所属库」
		// 的接口，因此这里只能依赖调用方通过 LibraryID 显式传入；未传入时
		// executeItem 会跳过降级直接标记失败。
		_ = s.libraries
	case TargetTypeLibrary:
		if task.LibraryID == "" {
			return domain.Errorf(domain.CodeValidation, "刷新媒体库 ID 不能为空")
		}
		task.TaskKey = LibraryTaskKey(task.LibraryID)
		task.ItemIDs = "[]"
	default:
		return domain.Errorf(domain.CodeValidation, "刷新目标类型无效：%s", targetType)
	}
	if _, err := s.tasks.UpsertEvent(ctx, task); err != nil {
		return err
	}
	s.wake()
	return nil
}

// RequestRefreshParams 描述一次刷新意图。
type RequestRefreshParams struct {
	TargetType  string
	LibraryID   string
	LibraryName string
	ItemID      string
}

// Start 启动后台扫描器。
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.wakeCh = make(chan struct{}, 1)
	s.mu.Unlock()

	go s.run(ctx)
	// 启动时先清理一次已过期的任务，避免重启后堆积。
	s.cancelExpired(ctx)
}

// Stop 停止后台扫描器并等待其退出。
func (s *Service) Stop(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	close(s.stopCh)
	done := s.doneCh
	s.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// wake 通知扫描器立即检查一次（非阻塞）。
func (s *Service) wake() {
	s.mu.Lock()
	ch := s.wakeCh
	s.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.scanOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
		case <-s.wakeCh:
		}
	}
}

// scanOnce 执行一轮扫描：取消过期任务 → 合并条目 → 认领并执行就绪任务。
func (s *Service) scanOnce(ctx context.Context) {
	s.cancelExpired(ctx)
	s.aggregateItems(ctx)
	now := s.now().Unix()
	tasks, err := s.tasks.ListReady(ctx, now, 50)
	if err != nil {
		s.log.Warn("读取 Emby 刷新任务失败", "error", err)
		return
	}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		claimed, claimErr := s.tasks.Claim(ctx, task.ID)
		if claimErr != nil {
			s.log.Warn("认领 Emby 刷新任务失败", "error", claimErr, "task_key", task.TaskKey)
			continue
		}
		// CAS 失败说明已被其它执行者认领，跳过。
		if !claimed {
			continue
		}
		s.execute(ctx, task)
	}
}

// cancelExpired 把超过截止时间的 pending 任务标记为 cancelled。
func (s *Service) cancelExpired(ctx context.Context) {
	n, err := s.tasks.CancelExpired(ctx, s.now().Unix())
	if err != nil {
		s.log.Warn("取消过期 Emby 刷新任务失败", "error", err)
		return
	}
	if n > 0 {
		s.log.Info("已放弃超时的 Emby 刷新任务", "count", n)
	}
}

// aggregateItems 把同一媒体库下达到阈值的 pending item 任务合并为一个库级任务。
func (s *Service) aggregateItems(ctx context.Context) {
	if s.threshold <= 0 {
		return
	}
	now := s.now()
	items, err := s.tasks.ListReadyItems(ctx, now.Unix(), 200)
	if err != nil {
		s.log.Warn("读取待合并 Emby 刷新条目失败", "error", err)
		return
	}
	byLibrary := make(map[string][]*domain.EmbyRefreshTask)
	for _, item := range items {
		libID := strings.TrimSpace(item.LibraryID)
		if libID == "" {
			// 没有归属媒体库的条目无法合并，仍按条目各自刷新。
			continue
		}
		byLibrary[libID] = append(byLibrary[libID], item)
	}
	for libID, group := range byLibrary {
		if len(group) < s.threshold {
			continue
		}
		s.absorb(ctx, libID, group, now)
	}
}

// absorb 建立或复用库级任务并把条目并入库级任务。
func (s *Service) absorb(ctx context.Context, libraryID string, items []*domain.EmbyRefreshTask, now time.Time) {
	libraryTask := &domain.EmbyRefreshTask{
		TaskKey:     LibraryTaskKey(libraryID),
		LibraryID:   libraryID,
		TargetType:  TargetTypeLibrary,
		ItemIDs:     "[]",
		Status:      domain.EmbyRefreshStatusPending,
		LastEventAt: now.Unix(),
		// 库级刷新合并后立即到期，避免再等一个防抖窗口。
		RefreshAfter: now.Unix(),
		DeadlineAt:   now.Add(s.maxWait).Unix(),
	}
	for _, item := range items {
		if item.LibraryName != "" {
			libraryTask.LibraryName = item.LibraryName
			break
		}
	}
	saved, err := s.tasks.UpsertEvent(ctx, libraryTask)
	if err != nil {
		s.log.Warn("创建合并后的 Emby 媒体库刷新任务失败", "error", err, "library_id", libraryID)
		return
	}
	absorbed, err := s.tasks.AbsorbItemsIntoLibrary(ctx, saved, items, now.Unix())
	if err != nil {
		s.log.Warn("合并 Emby 条目刷新任务失败", "error", err, "library_id", libraryID)
		return
	}
	if absorbed > 0 {
		s.log.Info("已将 Emby 条目刷新任务合并为媒体库刷新", "library_id", libraryID, "count", absorbed)
	}
}

// execute 执行一个已认领的任务，并把结果写回终态。
func (s *Service) execute(ctx context.Context, task *domain.EmbyRefreshTask) {
	if s.refresher == nil {
		s.finish(ctx, task, domain.EmbyRefreshStatusFailed, "Emby/Jellyfin 服务未就绪")
		return
	}
	switch task.TargetType {
	case TargetTypeItem:
		s.executeItem(ctx, task)
	case TargetTypeLibrary:
		s.executeLibrary(ctx, task)
	default:
		s.finish(ctx, task, domain.EmbyRefreshStatusFailed, "刷新目标类型无效："+task.TargetType)
	}
}

// executeItem 逐个刷新条目；单条目失败时降级为所在媒体库刷新。
// 所有条目都失败且降级也失败时，任务整体标记为 failed。
func (s *Service) executeItem(ctx context.Context, task *domain.EmbyRefreshTask) {
	ids := decodeItemIDs(task.ItemIDs)
	if len(ids) == 0 {
		s.finish(ctx, task, domain.EmbyRefreshStatusCancelled, "刷新条目为空")
		return
	}
	var failures []string
	for _, itemID := range ids {
		if _, err := s.refresher.RefreshLibrary(ctx, RefreshRequest{
			ConfigID: s.configID,
			Mode:     TargetTypeItem,
			ItemID:   itemID,
		}); err != nil {
			failures = append(failures, itemID+": "+err.Error())
		}
	}
	if len(failures) == 0 {
		s.finish(ctx, task, domain.EmbyRefreshStatusCompleted, "")
		return
	}
	// 有失败条目时降级为媒体库刷新兜底。
	libraryID := strings.TrimSpace(task.LibraryID)
	if libraryID != "" {
		if _, err := s.refresher.RefreshLibrary(ctx, RefreshRequest{
			ConfigID:  s.configID,
			Mode:      TargetTypeLibrary,
			LibraryID: libraryID,
		}); err == nil {
			s.finish(ctx, task, domain.EmbyRefreshStatusCompleted,
				"条目刷新失败，已降级为媒体库刷新："+strings.Join(failures, "；"))
			return
		}
	}
	s.finish(ctx, task, domain.EmbyRefreshStatusFailed, strings.Join(failures, "；"))
}

// executeLibrary 刷新媒体库；若任务同时携带条目，则先逐条刷新再刷库。
func (s *Service) executeLibrary(ctx context.Context, task *domain.EmbyRefreshTask) {
	libraryID := strings.TrimSpace(task.LibraryID)
	if libraryID == "" {
		s.finish(ctx, task, domain.EmbyRefreshStatusCancelled, "刷新媒体库为空")
		return
	}
	if _, err := s.refresher.RefreshLibrary(ctx, RefreshRequest{
		ConfigID:  s.configID,
		Mode:      TargetTypeLibrary,
		LibraryID: libraryID,
	}); err != nil {
		s.finish(ctx, task, domain.EmbyRefreshStatusFailed, err.Error())
		return
	}
	s.finish(ctx, task, domain.EmbyRefreshStatusCompleted, "")
}

// finish 把任务写入终态。执行失败以外的原因（取消）也走这里。
func (s *Service) finish(ctx context.Context, task *domain.EmbyRefreshTask, status, message string) {
	if err := s.tasks.Complete(ctx, task.ID, status, message, s.now().Unix()); err != nil {
		s.log.Warn("写入 Emby 刷新任务结果失败", "error", err, "task_key", task.TaskKey)
		return
	}
	switch status {
	case domain.EmbyRefreshStatusFailed:
		s.log.Warn("Emby 刷新任务失败", "task_key", task.TaskKey, "error", message)
	case domain.EmbyRefreshStatusCancelled:
		s.log.Info("Emby 刷新任务已取消", "task_key", task.TaskKey, "reason", message)
	default:
		s.log.Info("Emby 刷新任务已完成", "task_key", task.TaskKey)
	}
}
