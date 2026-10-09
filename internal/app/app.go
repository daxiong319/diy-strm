package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"litepan/internal/auth"
	"litepan/internal/automation"
	"litepan/internal/backuprestore"
	"litepan/internal/cache"
	"litepan/internal/cacheretention"
	"litepan/internal/config"
	"litepan/internal/driver"
	"litepan/internal/embyindex"
	"litepan/internal/embyproxy"
	"litepan/internal/embyrefresh"
	"litepan/internal/embywebhook"
	"litepan/internal/eventbus"
	"litepan/internal/file"
	"litepan/internal/fnosproxy"
	"litepan/internal/fusemount"
	"litepan/internal/logx"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediarequest"
	"litepan/internal/moviepilot"
	"litepan/internal/notifychannel"
	"litepan/internal/offlinedownload"
	"litepan/internal/playback"
	"litepan/internal/playmonitor"
	"litepan/internal/settings"
	"litepan/internal/store"
	"litepan/internal/strm"
	"litepan/internal/upload"
)

// ErrRestartRequested 表示管理员已从页面发起优雅重启。
var ErrRestartRequested = errors.New("restart requested")

// App 按依赖顺序构造与关闭各子系统。
type App struct {
	cfg              config.Config
	logs             *logx.Manager
	log              *slog.Logger
	db               *store.DB
	store            *store.Store
	settings         *settings.Service
	bus              *eventbus.Bus
	cache            *cache.Service
	drivers          *driver.Manager
	auth             *auth.Service
	sched            *auth.Scheduler
	files            *file.Service
	uploads          *upload.Manager
	offlineDownloads *offlinedownload.Service
	playback         *playback.Service
	playMonitor      *playmonitor.Service
	strm             *strm.Service
	mediaOrganize    *mediaorganize.Service
	automation       *automation.Service
	fuse             *fusemount.Service
	cacheRetention   *cacheretention.Service
	embyProxy        *embyproxy.Service
	embyRefresh      *embyrefresh.Service
	embyIndex        *embyindex.Service
	moviePilot       *moviepilot.Service
	embyWebhook      *embywebhook.Service
	fnosProxy        *fnosproxy.Service
	httpSrv          *http.Server
	// notifyRetry 通知补发 worker（webhook 投递失败的退避重发）。
	notifyRetry     *notifychannel.RetryWorker
	requestCenter   *requestCenterBundle
	requestListener *mediarequest.Listener
	httpBaseCtx     context.Context
	httpBaseCancel  context.CancelFunc
	restartCh       <-chan struct{}
}

// Options 是构造 App 所需的外部依赖。
type Options struct {
	Config config.Config
	Logs   *logx.Manager
}

// New 按依赖顺序装配 App：目录 → DB(+迁移) → 仓储 → 驱动管理器 → 文件服务 → HTTP。
func New(ctx context.Context, opts Options) (*App, error) {
	logs := opts.Logs
	if logs == nil {
		logs = logx.NewDiscard()
	}
	log := logs.Root()
	cfg := opts.Config

	if err := prepareDataDirs(cfg); err != nil {
		return nil, err
	}
	if status, restoreErr := backuprestore.ApplyPending(ctx, backuprestore.ApplyOptions{
		DataDir: cfg.DataDir,
		DBPath:  cfg.DBPath,
		Log:     logs.For(logx.ModuleSystem),
	}); restoreErr != nil {
		return nil, fmt.Errorf("apply pending restore: %w", restoreErr)
	} else if status.State == backuprestore.StateRestoreSuccess || status.State == backuprestore.StateRestoreRollback {
		log.Info("备份恢复启动阶段已完成", "state", status.State, "backup_id", status.BackupID)
	}

	stBundle, err := openStore(ctx, cfg, logs)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = stBundle.db.Close()
		}
	}()

	core, err := wireCore(ctx, cfg, logs, stBundle)
	if err != nil {
		return nil, err
	}
	svc := wireServices(cfg, logs, stBundle, core)

	restartCh := make(chan struct{})
	var restartOnce sync.Once
	requestRestart := func() { restartOnce.Do(func() { close(restartCh) }) }
	reqCenter := wireRequestCenter(stBundle, logs)
	libShare := wireLibraryShare(stBundle, logs)
	httpSrv, reqListener, notifyRetry, err := wireHTTPServer(cfg, logs, stBundle, core, svc, reqCenter, libShare, requestRestart)
	if err != nil {
		return nil, err
	}
	httpBaseCtx, httpBaseCancel := context.WithCancel(context.Background())
	httpSrv.BaseContext = func(_ net.Listener) context.Context { return httpBaseCtx }

	log.Info("应用初始化完成", "db", cfg.DBPath, "log_level", logs.Level())
	return &App{
		cfg:              cfg,
		logs:             logs,
		log:              log,
		db:               stBundle.db,
		store:            stBundle.store,
		settings:         stBundle.settings,
		bus:              core.bus,
		cache:            core.cache,
		drivers:          core.drivers,
		auth:             core.auth,
		sched:            core.sched,
		files:            svc.files,
		uploads:          svc.uploads,
		offlineDownloads: svc.offlineDownloads,
		playback:         svc.playback,
		playMonitor:      svc.playMonitor,
		strm:             svc.strm,
		mediaOrganize:    svc.mediaOrganize,
		automation:       svc.automation,
		fuse:             svc.fuse,
		cacheRetention:   svc.cacheRetention,
		embyProxy:        svc.embyProxy,
		embyRefresh:      svc.embyRefresh,
		embyIndex:        svc.embyIndex,
		moviePilot:       svc.moviePilot,
		embyWebhook:      svc.embyWebhook,
		fnosProxy:        svc.fnosProxy,
		httpSrv:          httpSrv,
		notifyRetry:      notifyRetry,
		requestCenter:    reqCenter,
		requestListener:  reqListener,
		httpBaseCtx:      httpBaseCtx,
		httpBaseCancel:   httpBaseCancel,
		restartCh:        restartCh,
	}, nil
}

// Run 启动 HTTP 服务并阻塞直到 ctx 取消或服务出错。
func (a *App) Run(ctx context.Context) error {
	if a.logs != nil && a.settings != nil {
		a.logs.StartAutoCleanup(ctx, a.settings.Int(settings.KeyLogRetentionDays))
	}
	if a.sched != nil && a.settings != nil {
		a.sched.InitActiveRefresh(ctx, a.settings.Bool(settings.KeyAuthActiveRefresh))
	}
	if a.strm != nil {
		a.strm.Start(ctx)
	}
	if a.cacheRetention != nil {
		a.cacheRetention.Start(ctx)
	}
	if a.automation != nil {
		a.automation.Start(ctx)
	}
	if a.fuse != nil {
		a.fuse.Start(ctx)
	}
	if a.uploads != nil {
		a.uploads.StartTempCleanup(ctx)
	}
	if a.offlineDownloads != nil {
		a.offlineDownloads.Start(ctx)
	}
	if a.embyProxy != nil {
		a.embyProxy.Start(ctx)
	}
	if a.embyRefresh != nil {
		a.embyRefresh.Start(ctx)
	}
	// Emby 本地索引的周期扫描：把 Emby 媒体库条目同步到本地索引，
	// 发现新增/变更后登记刷新意图。未配置 Emby 时循环内自行跳过，不会空转。
	if a.embyIndex != nil {
		a.embyIndex.Start(ctx)
	}
	if a.moviePilot != nil {
		a.moviePilot.Start(ctx)
	}
	// Emby Webhook 通知服务的剧集合并缓冲区需要常驻后台协程。
	if a.embyWebhook != nil {
		a.embyWebhook.Start(ctx)
	}
	if a.fnosProxy != nil {
		a.fnosProxy.Start(ctx)
	}
	// 播放监控采样循环：每 sampleSeconds 秒把「码率 × 间隔」累进当日桶。
	// 未启用（mo_play_monitor_enabled=false）时 Start 自行返回，列表恒空。
	if a.playMonitor != nil {
		a.playMonitor.Start(ctx)
	}
	// 通知补发 worker：轮询到点的 webhook 失败记录并重发。
	// 刻意独立 goroutine 而不是挂在事件总线的消费者上 ——
	// 总线只有一个消费者且队列满时 Publish 会阻塞调用方，
	// 在那里做 HTTP 重试会卡住后续所有事件的所有订阅者。
	if a.notifyRetry != nil {
		a.notifyRetry.Start(ctx)
	}
	// 求片站端口与后台对账。
	//
	// 监听挂 httpBaseCtx 而不是 ctx：它得跟 HTTP 服务同一个生命周期，
	// 否则 Shutdown 之后 7812 端口还会继续接请求 ——
	// 那种「关了应用还能求片」的僵尸状态比没开更难查。
	if a.requestListener != nil && a.httpBaseCtx != nil {
		go a.requestListener.Run(a.httpBaseCtx)
	}
	a.requestCenter.Start(ctx, a.logs)
	errCh := make(chan error, 1)
	go func() {
		a.log.Info("HTTP 服务已监听", "addr", a.cfg.ListenAddr)
		if err := a.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		a.log.Info("收到停止信号，准备关闭应用")
		return nil
	case <-a.restartCh:
		a.log.Warn("收到备份恢复重启请求，准备关闭应用")
		return ErrRestartRequested
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}
}

const (
	shutdownHTTPBudget    = 8 * time.Second
	shutdownFuseBudget    = 12 * time.Second
	shutdownBusBudget     = 3 * time.Second
	shutdownOfflineBudget = 20 * time.Second
	shutdownUploadBudget  = 20 * time.Second
	shutdownRefreshBudget = 5 * time.Second
)

// Shutdown 按依赖反序优雅关闭：先停 HTTP，再卸载 FUSE，最后关 DB。
func (a *App) Shutdown(ctx context.Context) error {
	a.log.Info("正在优雅关闭各组件")
	if a.sched != nil {
		a.sched.Stop()
	}
	if a.httpBaseCancel != nil {
		a.httpBaseCancel()
	}
	if a.embyRefresh != nil {
		refreshCtx, cancelRefresh := context.WithTimeout(ctx, shutdownRefreshBudget)
		a.embyRefresh.Stop(refreshCtx)
		cancelRefresh()
	}
	if a.embyIndex != nil {
		// Stop 只取消在途扫描、不等它扫完：未落库的部分下一轮会重新扫到。
		indexCtx, cancelIndex := context.WithTimeout(ctx, shutdownRefreshBudget)
		a.embyIndex.Stop(indexCtx)
		cancelIndex()
	}
	if a.moviePilot != nil {
		mpCtx, cancelMP := context.WithTimeout(ctx, shutdownRefreshBudget)
		a.moviePilot.Stop(mpCtx)
		cancelMP()
	}
	if a.embyWebhook != nil {
		// Stop 只等合并缓冲区收尾；缓冲区自身在 stopCh 关闭后立即返回。
		a.embyWebhook.Stop()
	}
	if a.embyProxy != nil {
		a.embyProxy.Shutdown(ctx)
	}
	if a.fnosProxy != nil {
		a.fnosProxy.Shutdown(ctx)
	}
	// 播放监控：关掉后台采样循环。
	// 刻意**不**在这里强行关掉所有活跃会话 —— 关会话会补一条停播记录，
	// 而正常关闭时用户未必真停播了，那条记录是假的。
	// 在途会话的心跳本来就会随 ctx 取消而停止，下一次启动自然过期。
	if a.playMonitor != nil {
		a.playMonitor.Stop()
	}
	// 通知补发 worker：在 HTTP 服务关停之前先停，让在途重发走完，
	// 不留下「一条记录卡在 sending」的状态。
	if a.notifyRetry != nil {
		a.notifyRetry.Stop()
	}

	httpCtx, cancelHTTP := context.WithTimeout(ctx, shutdownHTTPBudget)
	err := a.httpSrv.Shutdown(httpCtx)
	cancelHTTP()
	if err != nil {
		a.log.Warn("HTTP 服务关闭异常", "err", err)
		if cerr := a.httpSrv.Close(); cerr != nil && !errors.Is(cerr, http.ErrServerClosed) {
			a.log.Warn("HTTP 服务强制关闭异常", "err", cerr)
		}
	}

	if a.offlineDownloads != nil {
		offlineCtx, cancelOffline := context.WithTimeout(ctx, shutdownOfflineBudget)
		if err := a.offlineDownloads.Stop(offlineCtx); err != nil {
			a.log.Warn("内置离线下载停止异常", "err", err)
		}
		cancelOffline()
	}
	if a.fuse != nil {
		fuseCtx, cancelFuse := context.WithTimeout(ctx, shutdownFuseBudget)
		a.fuse.Stop(fuseCtx)
		cancelFuse()
	}
	if a.uploads != nil {
		uploadCtx, cancelUpload := context.WithTimeout(ctx, shutdownUploadBudget)
		if err := a.uploads.Stop(uploadCtx); err != nil {
			a.log.Warn("上传任务停止异常", "err", err)
		}
		cancelUpload()
		a.uploads.FlushPendingResume()
	}

	busCtx, cancelBus := context.WithTimeout(ctx, shutdownBusBudget)
	err = a.bus.Close(busCtx)
	cancelBus()
	if err != nil {
		a.log.Warn("事件总线关闭异常", "err", err)
	}
	snapshotCacheOnShutdown(a.cache, a.settings, a.cfg.DataDir)
	a.cache.Close()
	a.drivers.Close(ctx)
	if err := a.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	if err := a.logs.Close(ctx); err != nil {
		a.log.Warn("日志刷新异常", "err", err)
	}
	return nil
}
