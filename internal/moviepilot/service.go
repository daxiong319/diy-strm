// Package moviepilot 提供 MoviePilot 对接：订阅管理、下载完成检测、订阅下载的本地整理与网盘上传。
package moviepilot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/tmdb"
)

// DefaultPollMinutes 默认轮询间隔（分钟）。
const DefaultPollMinutes = 5

// emptySourceWaitLimit 源目录持续无文件的最长等待时间：
// 超过后判定为终态失败（MoviePilot 的完成信号可能早于文件真正落盘，短期为空不代表出错）。
const emptySourceWaitLimit = 48 * time.Hour

// Options 构造服务所需依赖。
type Options struct {
	// Repo MoviePilot 仓储（必需）；为 nil 时 New 返回 nil，调用方可跳过装配。
	Repo domain.MoviePilotRepository
	// Uploads 上传任务创建能力（由 upload.Manager 实现）。
	Uploads UploadEnqueuer
	// Folders 网盘目录创建能力（由 file.Service 实现）。
	Folders FolderCreator
	// Accounts 账号查询能力（由 account.Service 实现），用于解析上传账号名称与驱动类型。
	Accounts AccountLookup
	// Tmdb TMDB 客户端，用于识别校验与正式命名。
	Tmdb *tmdb.Client
	// Notifier 通知发送能力（可选）。
	Notifier Notifier
	// Strm 触发 STRM 生成的能力（可选）。
	Strm StrmTrigger
	// Now 便于测试注入时钟，nil 时使用 time.Now。
	Now func() time.Time
	// Log 日志器，nil 时使用 slog.Default()。
	Log *slog.Logger
}

// AccountLookup 解析上传账号的名称与驱动类型。
type AccountLookup interface {
	LookupUploadAccount(ctx context.Context, accountID int64) (name, driverType string, err error)
}

// StrmTrigger 触发某个网盘目录的 STRM 生成（由 strm 子系统实现或包装）。
type StrmTrigger interface {
	TriggerStrmForDir(ctx context.Context, accountID int64, sourcePath, strmLocalDir string) error
}

// Service MoviePilot 订阅下载的整理与上传服务。
type Service struct {
	repo     domain.MoviePilotRepository
	uploads  UploadEnqueuer
	folders  FolderCreator
	accounts AccountLookup
	tmdb     *tmdb.Client
	notifier Notifier
	strm     StrmTrigger
	now      func() time.Time
	log      *slog.Logger

	mu      sync.Mutex
	started bool
	stopCh  chan struct{}
	doneCh  chan struct{}
	wakeCh  chan struct{}

	// 上传任务的状态查询能力（由 upload.Manager 可选注入，用于批次收敛等待）。
	uploadTasks UploadTaskLookup

	// uploadQueue 串行上传队列；queued 为在队任务集合（幂等去重）；
	// processingID 记录当前正在执行的任务，用于区分「上传中」与「进程重启遗留状态」。
	uploadQueue  chan int64
	queued       map[int64]struct{}
	processingID int64

	// lastHistoryID 下载历史扫描游标；historyAttempts 记录各 hash 的失败重试时间。
	lastHistoryID   int64
	historyAttempts map[string]time.Time
}

// UploadTaskLookup 查询某个 MoviePilot 任务下所有文件上传任务的收敛情况。
type UploadTaskLookup interface {
	// BatchProgress 返回 (是否全部终态, 失败数, 已上传数, 总数)。
	BatchProgress(taskID int64) (finished bool, failed int, uploaded int, total int)
}

// Notifier 发送通知的能力（可选装配）。
type Notifier interface {
	NotifyMediaAdded(ctx context.Context, title, content string) error
}

// New 构造 MoviePilot 服务。repo 为 nil 时返回 nil，便于调用方在未装配时跳过。
func New(opts Options) *Service {
	if opts.Repo == nil {
		return nil
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo:            opts.Repo,
		uploads:         opts.Uploads,
		folders:         opts.Folders,
		accounts:        opts.Accounts,
		tmdb:            opts.Tmdb,
		notifier:        opts.Notifier,
		strm:            opts.Strm,
		now:             now,
		log:             log,
		uploadQueue:     make(chan int64, 32),
		queued:          make(map[int64]struct{}),
		historyAttempts: make(map[string]time.Time),
	}
}

// SetUploadTaskLookup 注入上传任务状态查询能力（用于等待上传批次收敛）。
func (s *Service) SetUploadTaskLookup(lookup UploadTaskLookup) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.uploadTasks = lookup
	s.mu.Unlock()
}

// Start 启动后台轮询。未启用配置时仍会启动循环，每轮重新读取配置以支持运行期开启。
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
	stopCh, doneCh := s.stopCh, s.doneCh
	s.mu.Unlock()

	// 启动恢复：把上次进程遗留的 pending/uploading 任务重新排队
	go s.recoverPendingTasks(ctx)

	// 串行上传 worker
	go s.uploadWorker(ctx, stopCh)

	go func() {
		defer close(doneCh)
		s.run(ctx, stopCh)
	}()
}

// Stop 停止后台轮询，等待当前轮结束或 ctx 超时。
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
	stopCh, doneCh := s.stopCh, s.doneCh
	s.mu.Unlock()

	close(stopCh)
	select {
	case <-doneCh:
	case <-ctx.Done():
		s.log.Warn("MoviePilot 服务停止超时，仍有轮询任务在运行")
	}
}

// wake 立即唤醒轮询（非阻塞）。
func (s *Service) wake() {
	if s == nil {
		return
	}
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

// run 轮询主循环：每 tick 重载配置，仅在启用且配置完整时执行真实工作。
func (s *Service) run(ctx context.Context, stopCh <-chan struct{}) {
	interval := s.pollInterval(ctx, nil)
	s.log.Info("MoviePilot 订阅下载检测已启动", "间隔", interval)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-s.wakeChan():
		}
		if ctx.Err() != nil {
			return
		}
		cfg, err := s.repo.LoadConfig(ctx)
		if err != nil {
			s.log.Error("MoviePilot 读取配置失败", "err", err)
		} else {
			interval = s.pollInterval(ctx, cfg)
			if !cfg.Enabled || strings.TrimSpace(cfg.BaseUrl) == "" || strings.TrimSpace(cfg.ApiToken) == "" {
				// 未启用或配置不完整：静默跳过本轮
			} else {
				s.runOnce(ctx, cfg)
			}
		}
		timer.Reset(interval)
	}
}

// wakeChan 返回唤醒通道（未启动时返回 nil，select 会忽略该分支）。
func (s *Service) wakeChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wakeCh
}

// pollInterval 计算轮询间隔（默认 5 分钟）。
func (s *Service) pollInterval(_ context.Context, cfg *domain.MoviePilotConfig) time.Duration {
	minutes := DefaultPollMinutes
	if cfg != nil && cfg.PollInterval > 0 {
		minutes = cfg.PollInterval
	}
	return time.Duration(minutes) * time.Minute
}

// runOnce 执行一轮完整检测：完成下载 → 促销阶梯 → 删种。
func (s *Service) runOnce(ctx context.Context, cfg *domain.MoviePilotConfig) {
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	if err := s.checkCompletedDownloads(ctx, cfg, client); err != nil {
		s.log.Warn("MoviePilot 下载完成检测失败", "err", err)
	}
	if err := s.applyPromotionLadder(ctx, cfg, client); err != nil {
		s.log.Warn("MoviePilot 促销阶梯处理失败", "err", err)
	}
	if err := s.autoDeleteSeeds(ctx, cfg, client); err != nil {
		s.log.Warn("MoviePilot 自动删种失败", "err", err)
	}
}

// LoadConfig 读取配置（供 API 层复用）。
func (s *Service) LoadConfig(ctx context.Context) (*domain.MoviePilotConfig, error) {
	if s == nil || s.repo == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	return s.repo.LoadConfig(ctx)
}

// SaveConfig 保存配置并唤醒轮询（使新的间隔与开关立即生效）。
func (s *Service) SaveConfig(ctx context.Context, cfg *domain.MoviePilotConfig) error {
	if s == nil || s.repo == nil {
		return domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	if cfg == nil {
		return domain.Errorf(domain.CodeValidation, "配置不能为空")
	}
	cfg.PromotionOrder = NormalizePromotionOrder(cfg.PromotionOrder)
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollMinutes
	}
	cfg.BaseUrl = strings.TrimRight(strings.TrimSpace(cfg.BaseUrl), "/")
	cfg.ApiToken = strings.TrimSpace(cfg.ApiToken)
	if err := s.repo.SaveConfig(ctx, cfg); err != nil {
		return err
	}
	s.wake()
	return nil
}
