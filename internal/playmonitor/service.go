package playmonitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"litepan/internal/domain"
)

// Settings 播放监控读配置的口子。
//
// 签名与 internal/rbac/service.go:23 的 Settings 一致（String/Bool/Int，无 fallback）：
// 真正的 settings.Service 在键缺失时会回落到 registry 登记的默认值，
// 再传一个 fallback 就是第二个真相来源。
type Settings interface {
	String(key string) string
	Bool(key string) bool
	Int(key string) int
}

// Options 监控器构造参数。
type Options struct {
	Settings Settings
	// Records 播放记录仓储（写入起播/停播事件、读观影报告数据）。
	Records domain.PlaybackRecordRepository
	// Traffic 日流量桶仓储。
	Traffic domain.PlayTrafficRepository
	// Users 用户名解析（play_traffic_daily 的 user_name 列）。
	// 可为 nil —— 解析不到就用空名，排行里显示"匿名"。
	Users UserResolver
	// Log 监控器内部的告警出口，可为 nil。
	Log func(msg string, kv ...any)
}

// UserResolver 从 RBAC 用户 ID 解析显示名。
type UserResolver interface {
	DisplayName(userID int64) (string, bool)
}

// Service 播放监控器。
//
// 它做四件事：
//
//  1. 三态判定（classifyState）：外网+流代理=计费中，外网+302=CDN 直连，内网=局域网
//  2. 实时会话表：起播建 session，心跳丢失 idleSeconds 关 session（从列表消失）
//  3. 流量估算：每 sampleSeconds 秒把「码率 × 采样间隔」累进当日桶
//  4. 消噪：同设备同片暂停只转交一次停止事件，真正续播约 resumeSeconds 后才回列表
type Service struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*sessionEntry

	// stoppedKey 记「已经转交过停止事件」的 (设备,片名) 键及其停止时刻。
	// 用于暂停消噪：暂停后再收一点数据不重新转交停止事件。
	stopped map[string]time.Time

	stopCh chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

// sessionEntry 内存里的一个活跃会话。
type sessionEntry struct {
	domain.PlaySession
	// stoppedReported 是否已就本会话转交过停止事件（暂停消噪用）。
	stoppedReported bool
	// startMono 起播时刻的单调钟读数，不受系统时间跳变影响。
	startMono time.Time
}

// New 构造播放监控器。
func New(opts Options) *Service {
	return &Service{
		opts:     opts,
		sessions: make(map[string]*sessionEntry, 32),
		stopped:  make(map[string]time.Time, 64),
		stopCh:   make(chan struct{}),
	}
}

// SetUserResolver 注入用户名解析。
//
// 用 setter 而非 Options 字段：RBAC 服务在 wire_http.go 里构造，
// 晚于 wireServices（监控器在那儿创建），注入顺序不由构造函数决定。
//
// 不注入不是错误，只是排行榜里的用户名解析不出来 —— 退到
// "Emby:xxx" / "用户#12" 兜底，统计数字本身不受影响。
func (s *Service) SetUserResolver(u UserResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.opts.Users = u
	s.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// 生命周期
// ─────────────────────────────────────────────────────────────────────────────

// userResolverLocked 取用户名解析器（调用方须持锁）。
func (s *Service) userResolverLocked() UserResolver { return s.opts.Users }

// userResolver 取用户名解析器。
//
// SetUserResolver 会改这个字段（RBAC 晚于监控器构造），所以
// 读它必须走锁 —— 直接读 s.opts.Users 会在「装配完成的瞬间」
// 与并发的报告生成之间撞上 data race。
func (s *Service) userResolver() UserResolver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Users
}

// Start 启动后台采样循环。插件未启用时不启动 —— 监控默认关闭。
func (s *Service) Start(ctx context.Context) {
	if s.opts.Settings == nil || !s.opts.Settings.Bool(KeyMonitorEnabled) {
		s.log("播放监控未启用，后台采样不启动")
		return
	}
	// 「统计从启用起累计、不补算」的下界必须在**第一次启用**那一刻落下来。
	// 之前没写过标记行的话，报告就没有下界，会把历史 Emby 播放记录
	// （0028 时代留下的）算进报告 —— 那正是"补算"，口径就废了。
	// 落库失败只告警：起播监控本身不依赖这个标记，少一天下界
	// 比整个播放监控起不来好；下次 Start 会再试一次。
	if s.opts.Traffic != nil {
		if err := s.opts.Traffic.EnsureEnabledSince(ctx); err != nil {
			s.logf("写入播放监控启用时刻失败：%v", err)
		}
	}
	sample := s.sampleSeconds()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Duration(sample) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.sample(ctx)
			}
		}
	}()
	s.logf("播放监控采样已启动（每 %d 秒累计一次）", sample)
}

// Stop 停止后台采样循环（幂等）。
func (s *Service) Stop() {
	s.once.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *Service) sampleSeconds() int {
	if v := s.opts.Settings.Int(KeyMonitorSampleSeconds); v >= 1 {
		return v
	}
	return DefaultSampleSeconds
}

func (s *Service) idleSeconds() int {
	if v := s.opts.Settings.Int(KeyMonitorIdleSeconds); v >= 1 {
		return v
	}
	return DefaultIdleSeconds
}

func (s *Service) log(msg string) {
	if s.opts.Log != nil {
		s.opts.Log(msg)
	}
}

func (s *Service) logf(format string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log(fmt.Sprintf(format, args...))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 配置键
// ─────────────────────────────────────────────────────────────────────────────

const (
	// KeyMonitorEnabled mo_play_monitor_enabled：播放监控总开关，默认关。
	KeyMonitorEnabled = "mo_play_monitor_enabled"
	// KeyMonitorIdleSeconds mo_play_monitor_idle_seconds：心跳丢失多少秒后
	// 会话从实时列表消失。默认 60（口径：停播约一分钟消失）。
	KeyMonitorIdleSeconds = "mo_play_monitor_idle_seconds"
	// KeyMonitorSampleSeconds mo_play_monitor_sample_seconds：流量累计间隔秒数。
	KeyMonitorSampleSeconds = "mo_play_monitor_sample_seconds"
	// KeyReportEnabled mo_play_report_enabled：观影报告总开关。
	KeyReportEnabled = "mo_play_report_enabled"
	// KeyReportMinSeconds mo_play_report_min_seconds：忽略播放时长低于 N 秒的记录。
	KeyReportMinSeconds = "mo_play_report_min_seconds"
	// KeyReportGapMinutes mo_play_report_gap_minutes：中断超过多少分钟算新的一次播放。
	KeyReportGapMinutes = "mo_play_report_gap_minutes"
)

const (
	// DefaultIdleSeconds 心跳丢失默认 60 秒。
	DefaultIdleSeconds = 60
	// DefaultSampleSeconds 流量累计默认 5 秒（口径：约每 5 秒累计一次）。
	DefaultSampleSeconds = 5
	// DefaultReportGapMinutes 播放次数的「中断超多久算新一次」默认 30 分钟。
	DefaultReportGapMinutes = 30
)
