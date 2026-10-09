package app

import (
	"context"
	"log/slog"

	"litepan/internal/account"
	"litepan/internal/accountprofile"
	"litepan/internal/aiorganize"
	"litepan/internal/automation"
	"litepan/internal/cacheretention"
	"litepan/internal/classifyorganize"
	"litepan/internal/config"
	"litepan/internal/crosstransfer"
	"litepan/internal/domain"
	"litepan/internal/embyindex"
	"litepan/internal/embyproxy"
	"litepan/internal/embyrefresh"
	"litepan/internal/embywebhook"
	"litepan/internal/favorites"
	"litepan/internal/file"
	"litepan/internal/fnosproxy"
	"litepan/internal/fusemount"
	"litepan/internal/fusereadcache"
	"litepan/internal/logx"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediaorganize/tmdb"
	"litepan/internal/mediaupgrade"
	"litepan/internal/moviepilot"
	"litepan/internal/offlinedownload"
	"litepan/internal/playback"
	"litepan/internal/playbackrecord"
	"litepan/internal/playmonitor"
	"litepan/internal/quarktv"
	"litepan/internal/settings"
	"litepan/internal/strm"
	"litepan/internal/strmscrape"
	"litepan/internal/subtitle"
	"litepan/internal/upload"
)

type servicesBundle struct {
	files            *file.Service
	uploads          *upload.Manager
	offlineDownloads *offlinedownload.Service
	playback         *playback.Service
	playbackRecord   *playbackrecord.Service
	playMonitor      *playmonitor.Service
	account          *account.Service
	accountProfile   *accountprofile.Service
	strm             *strm.Service
	mediaOrganize    *mediaorganize.Service
	mediaUpgrade     *mediaupgrade.Service
	subtitleSvc      *subtitle.Service
	aiOrganize       *aiorganize.Service
	classifyOrganize *classifyorganize.Service
	strmScrape       *strmscrape.Service
	automation       *automation.Service
	fuse             *fusemount.Service
	fuseReadCache    *fusereadcache.Service
	cacheRetention   *cacheretention.Service
	crossTransfer    *crosstransfer.Service
	embyProxy        *embyproxy.Service
	embyRefresh      *embyrefresh.Service
	embyIndex        *embyindex.Service
	moviePilot       *moviepilot.Service
	embyWebhook      *embywebhook.Service
	fnosProxy        *fnosproxy.Service
	favorites        *favorites.Service
	quarktv          *quarktv.Service
}

func wireServices(cfg config.Config, logs *logx.Manager, st *storeBundle, core *coreBundle) *servicesBundle {
	var startupGate <-chan struct{}
	if core != nil && core.sched != nil {
		startupGate = core.sched.StartupReady()
	}
	favoritesSvc := favorites.NewService(cfg.DBPath, logs.For(logx.ModuleSystem))
	fileSvc := file.NewService(core.exec, core.cache, st.store.Accounts, core.bus, st.settings, core.listHits)
	fileSvc.SetLogger(logs.For(logx.ModuleFileOp))
	playbackSvc := playback.NewService(core.exec, core.cache)
	playbackSvc.SetLogger(logs.For(logx.ModuleSystem))
	strmSvc, coord := wireSTRM(st, fileSvc, playbackSvc, core.bus, logs, cfg.DataDir, cfg.StrmDir, cfg.ListenAddr, core.secret)
	retentionSvc, retentionCoord := wireCacheRetention(st, fileSvc, core.cache, core.bus, logs)
	aiOrganizeSvc := aiorganize.New(st.settings)
	classifyOrganizeSvc := classifyorganize.New(st.settings)
	// 熔断打开时会打一行 Warn，解释「为什么这批文件全落在一级目录」。
	// 不接日志的话这条诊断信息就丢了 —— 用户只能看到分类结果突然变了。
	classifyOrganizeSvc.SetLogger(classifyOrganizeLogger{logs.For(logx.ModuleSystem)})
	// 字幕服务在装配层构造一次，同时供「整理流程自动下载字幕」与
	// API 管理端点使用，保证两边共享同一份配置快照与同一个任务仓储。
	subtitleSvc := subtitle.NewService(st.settings, subtitle.NewLogger(logs.For(logx.ModuleSystem)))
	subtitleSvc.SetTaskStore(subtitle.NewTaskStore(st.store.DB.WriteHandle(), st.store.DB.ReadHandle()))
	// Emby 反查闭包：embyProxySvc 在下面才构造（它依赖整理服务之外的一批依赖），
	// 所以这里先放一个间接层，等 Emby 服务建好后再把实现塞进去。
	// 用闭包而不是直接传值，是因为 planner 每次 Build 计划时才需要它，
	// 那时 Emby 服务早已就绪。
	var embyLocationLookupFn func(ctx context.Context, accountID int64, title string, year *int) string
	mediaOrganizeSvc := wireMediaOrganize(st, fileSvc, logs, cfg.DataDir, aiOrganizeSvc, classifyOrganizeSvc, subtitleSvc, func() func(context.Context, int64, string, *int) string {
		return embyLocationLookupFn
	})
	strmScrapeSvc := strmscrape.New(strmscrape.Options{
		Strm:     strmSvc,
		Settings: st.settings,
		Bus:      core.bus,
		DataDir:  cfg.DataDir,
		StrmDir:  cfg.StrmDir,
		Log:      logs.For(logx.ModuleSystem),
	})
	strmSvc.SetOrganizeBusyChecker(mediaOrganizeSvc)
	strmSvc.SetRetentionBusyChecker(retentionSvc)
	strmSvc.SetStartupGate(startupGate)
	retentionSvc.SetStrmBusyChecker(strmSvc)
	retentionSvc.SetOrganizeBusyChecker(mediaOrganizeSvc)
	retentionSvc.SetStartupGate(startupGate)
	fuseReadCache := wireFuseReadCacheOrNil(context.Background(), cfg, logs, st, core.bus)
	offlineDownloadSvc := offlinedownload.New(offlinedownload.Options{
		Exec:     core.exec,
		Accounts: st.store.Accounts,
		Repo:     st.store.OfflineDownloads,
		Folders:  fileSvc,
		Settings: st.settings,
		DataDir:  cfg.DataDir,
		Bus:      core.bus,
		Log:      logs.For(logx.ModuleFileOp),
	})
	fusemount.ApplyConfiguredMountRoot(context.Background(), st.store.Configs)
	fuseSvc := fusemount.New(fusemount.Options{
		Repo:      st.store.FuseMounts,
		Configs:   st.store.Configs,
		Accounts:  st.store.Accounts,
		Notify:    st.store.Notifications,
		Files:     fileSvc,
		Playback:  playbackSvc,
		ReadCache: fuseReadCache,
		Bus:       core.bus,
		Log:       logs.For(logx.ModuleSystem),
	})
	fuseSvc.SetStartupGate(startupGate)
	fuseSvc.Register(core.bus)
	_ = fuseSvc.PrepareMountRoot()
	lifecycle := &accountLifecycle{
		fuse:      fuseSvc,
		readCache: fuseReadCache,
		strm:      coord,
		strmSvc:   strmSvc,
		retention: retentionCoord,
		media:     mediaOrganizeSvc,
		favorites: favoritesSvc,
		offline:   offlineDownloadSvc,
	}
	accountSvc := account.NewService(account.Options{
		Accounts:      st.store.Accounts,
		AuthStates:    st.store.AuthStates,
		Drivers:       core.drivers,
		Auth:          core.auth,
		Playback:      playbackSvc,
		MetadataCache: core.cache,
		Lifecycle:     lifecycle,
		OAuthURL: func(context.Context) string {
			return domain.NormalizeOAuthServerURL(st.settings.String(settings.KeyOAuthServerURL))
		},
	})
	accountProfileSvc := accountprofile.New(core.exec)
	quarktvSvc := quarktv.New(quarktv.Options{
		Settings:       st.settings,
		Bindings:       st.store.QuarkTVBindings,
		Accounts:       st.store.Accounts,
		AccountProfile: accountProfileSvc,
		Bus:            core.bus,
		Log:            logs.For(logx.ModuleSystem),
	})
	playbackSvc.SetDownloadResolverHook(quarktvSvc.ResolveHook)
	lifecycle.quarktv = quarktvSvc
	// 播放记录（Emby 302 反代）：记录写入挂在取流入口上（与老版
	// recordStrmPlayback 挂 STRM 重定向同一位置），异步落库不影响重定向。
	// 仓储不可用时降级为不记录，播放链路照常工作。
	var playbackRecordSvc *playbackrecord.Service
	if st.store.PlaybackRecords != nil && st.store.DB != nil {
		playbackRecordSvc = playbackrecord.New(st.store.DB.WriteHandle(), st.store.DB.ReadHandle())
		playbackrecord.SetErrorHook(func(err error) {
			logs.For(logx.ModuleSystem).Warn("播放记录落库失败", "error", err)
		})
		logs.For(logx.ModuleSystem).Info("播放记录已接入")
	}
	if playbackRecordSvc != nil {
		// 账号仓储只为把账号 ID 翻译成网盘类型（provider），失败不影响记录。
		playbackSvc.SetRedirectObserver(
			newPlaybackRecordObserverWithAccounts(playbackRecordSvc, st.store.Accounts),
		)
	}
	// 播放监控（计费中 / CDN 直连 / 局域网 三态 + 观影报告）。
	//
	// 装配顺序有讲究：监控器必须在 automationSvc 之前构造，
	// 因为观影报告触发器要拿它当生成器（见下面的 SetPlayReportGenerator）。
	// 仓储不可用时降级为「不监控」—— 取流链路照常工作，
	// 只是列表恒空、流量恒 0；绝不让监控器把播放拖垮。
	var playMonitorSvc *playmonitor.Service
	if st.store.PlaybackRecords != nil && st.store.PlayTraffic != nil {
		playMonitorSvc = playmonitor.New(playmonitor.Options{
			Settings: st.settings,
			Records:  st.store.PlaybackRecords,
			Traffic:  st.store.PlayTraffic,
			Log:      logs.For(logx.ModuleSystem).Warn,
		})
		// 三态判定挂在取流入口上（internal/playback.Service.ServeHTTP），
		// 而不是各个 api handler —— internal/api/cas.go 的 casPlay 也走
		// 同一条 ServeHTTP，挂 handler 层会漏掉 CAS 取流。
		playbackSvc.SetStreamMonitor(playMonitorSvc)
		logs.For(logx.ModuleSystem).Info("播放监控已接入")
	}
	uploadSvc := upload.NewManager(upload.Options{
		Exec:        core.exec,
		Files:       fileSvc,
		Playback:    playbackSvc,
		Accounts:    accountSvc,
		Repo:        st.store.UploadTasks,
		Settings:    st.settings,
		Bus:         core.bus,
		DataDir:     cfg.DataDir,
		Log:         logs.For(logx.ModuleFileOp),
		StartupGate: startupGate,
	})
	lifecycle.uploads = uploadSvc
	offlineDownloadSvc.SetUploads(uploadSvc)
	fuseSvc.SetUploads(uploadSvc)
	crossTransferSvc := crosstransfer.New(crosstransfer.Options{
		Exec:    core.exec,
		Files:   fileSvc,
		Uploads: uploadSvc,
		Log:     logs.For(logx.ModuleAPI),
	})
	embyProxySvc := embyproxy.New(embyproxy.Options{
		Settings: st.settings,
		Playback: playbackSvc,
		Strm:     strmSvc,
		Log:      logs.For(logx.ModuleSystem),
		ResolvePath: func(ctx context.Context, accountID int64, rootID, relativePath string) (string, error) {
			item, err := fileSvc.ResolvePath(ctx, accountID, rootID, relativePath)
			if err != nil {
				return "", err
			}
			return item.ID, nil
		},
	})
	fnosProxySvc := fnosproxy.New(fnosproxy.Options{
		Settings:       st.settings,
		Playback:       playbackSvc,
		Strm:           strmSvc,
		StrmDir:        cfg.StrmDir,
		Log:            logs.For(logx.ModuleSystem),
		PortUsedByEmby: embyProxySvc.UsesPort,
		ResolvePath: func(ctx context.Context, accountID int64, rootID, relativePath string) (string, error) {
			item, err := fileSvc.ResolvePath(ctx, accountID, rootID, relativePath)
			if err != nil {
				return "", err
			}
			return item.ID, nil
		},
	})
	embyLocationLookupFn = wireEmbyLocationLookup(embyProxySvc)
	refreshAdapter := embyproxy.NewRefreshTaskAdapter(embyProxySvc)
	embyRefreshSvc := embyrefresh.New(embyrefresh.Options{
		Tasks:     st.store.EmbyRefreshTasks,
		Refresher: refreshAdapter,
		Libraries: refreshAdapter,
		Log:       logs.For(logx.ModuleSystem),
	})
	automationSvc := automation.New(automation.Options{
		Rules:      st.store.AutomationRules,
		Runs:       st.store.AutomationRuns,
		Strm:       strmSvc,
		StrmScrape: strmScrapeSvc,
		Organize:   mediaOrganizeSvc,
		Emby:       embyProxySvc,
		Fnos:       fnosProxySvc,
		Files:      fileSvc,
		Log:        logs.For(logx.ModuleSystem),
	})
	automationSvc.SetRefreshQueue(embyRefreshSvc)
	automationSvc.SetStartupGate(startupGate)
	automationSvc.Register(core.bus)
	// 观影报告触发器（play_report）：到点生成排行图，
	// 再由规则里的 notify 动作推出去 —— 生成与推送解耦，
	// 用户不配 notify 就只生成不推送，不会莫名收到消息。
	// 监控器没装配时**不注入**，让规则明确报「播放监控服务未就绪」，
	// 而不是静默成功（静默成功会让用户以为报告发出去了）。
	if playMonitorSvc != nil {
		automationSvc.SetPlayReportGenerator(playReportAdapter{monitor: playMonitorSvc})
	}
	// Emby 本地索引：扫描 Emby 媒体库条目并落到 emby_media_items，
	// 发现新增/变更条目后登记刷新意图，形成「索引 → 刷新队列」的自动链路。
	// 配置由 embyProxySvc.LiveConfigs() 投影（需明文 API Key，不能用脱敏的 Snapshots）。
	embyIndexSvc := embyindex.New(embyindex.Options{
		Index:    st.store.EmbyIndex,
		Files:    fileSvc,
		Strm:     strmSvc,
		Settings: st.settings,
		Log:      logs.For(logx.ModuleSystem),
		RefreshSink: embyindexRefreshSink{
			register: automationSvc.RegisterRefreshIntent,
		},
	})
	if embyIndexSvc != nil {
		embyIndexSvc.SetConfigLoader(embyIndexConfigLoader(embyProxySvc, st.settings))
	}
	// Emby Webhook 通知服务：Enabled 表示 Emby 已配置（地址与 API Key 齐备）；
	// 媒体/播放通知开关取自 settings 的 emby 分类，由 API 层在请求时实时读取。
	embyWebhookSvc := embywebhook.New(embywebhook.Config{
		Enabled:           st.settings.Bool(settings.KeyEmbyEnabled),
		MediaNotification: st.settings.Bool(settings.KeyEmbyNotifyEnabled),
		PlaybackOverview:  st.settings.Bool(settings.KeyEmbyPlaybackOverviewEnabled),
		PlaybackProgress:  st.settings.Bool(settings.KeyEmbyPlaybackNotifyEnabled),
		DeleteNetdisk:     st.settings.Bool(settings.KeyEmbyDeleteNetdiskEnabled),
	}, embyProxySvc, nil, logs.For(logx.ModuleSystem))
	strmSvc.SetAutomationManagedChecker(automationSvc.IsStrmTaskManaged)
	// MoviePilot 订阅下载整理上传服务：轮询 MoviePilot 的下载与下载历史，
	// 将完成的订阅下载在本地整理命名后交棒既有上传链路，再触发 STRM 同步。
	plannerSettings := mediaorganize.EnrichPlannerSettings(st.settings, nil)
	moviePilotSvc := moviepilot.New(moviepilot.Options{
		Repo:     st.store.MoviePilot,
		Uploads:  uploadSvc,
		Folders:  fileSvc,
		Accounts: accountSvc,
		Tmdb: tmdb.NewClient(tmdb.Options{
			APIKey:        mediaorganize.PlannerTMDBAPIKey(plannerSettings),
			Language:      mediaorganize.PlannerTMDBLanguage(plannerSettings),
			ProxyURL:      tmdb.BuildProxyURL(mediaorganize.TmdbProxyFromSettings(plannerSettings)),
			APIBaseHost:   mediaorganize.PlannerTMDBAPIHost(plannerSettings),
			ImageBaseHost: mediaorganize.PlannerTMDBImageHost(plannerSettings),
		}),
		Log: logs.For(logx.ModuleSystem),
	})
	if moviePilotSvc != nil {
		moviePilotSvc.SetUploadTaskLookup(newMoviePilotBatchAdapter(uploadSvc))
	} else {
		logs.For(logx.ModuleSystem).Warn("MoviePilot 存储未装配，服务未启动")
	}
	return &servicesBundle{
		files:            fileSvc,
		uploads:          uploadSvc,
		offlineDownloads: offlineDownloadSvc,
		playback:         playbackSvc,
		playbackRecord:   playbackRecordSvc,
		playMonitor:      playMonitorSvc,
		account:          accountSvc,
		accountProfile:   accountProfileSvc,
		strm:             strmSvc,
		mediaOrganize:    mediaOrganizeSvc,
		subtitleSvc:      subtitleSvc,
		aiOrganize:       aiOrganizeSvc,
		classifyOrganize: classifyOrganizeSvc,
		strmScrape:       strmScrapeSvc,
		automation:       automationSvc,
		fuse:             fuseSvc,
		fuseReadCache:    fuseReadCache,
		cacheRetention:   retentionSvc,
		crossTransfer:    crossTransferSvc,
		embyProxy:        embyProxySvc,
		embyRefresh:      embyRefreshSvc,
		embyIndex:        embyIndexSvc,
		moviePilot:       moviePilotSvc,
		embyWebhook:      embyWebhookSvc,
		fnosProxy:        fnosProxySvc,
		favorites:        favoritesSvc,
		quarktv:          quarktvSvc,
	}
}

// classifyOrganizeLogger 把 *slog.Logger 适配成 classifyorganize.Logger。
// 在装配层而不是包内定义，是为了 classifyorganize 不必 import slog。
type classifyOrganizeLogger struct{ l *slog.Logger }

func (a classifyOrganizeLogger) Warn(msg string, args ...any) {
	if a.l != nil {
		a.l.Warn(msg, args...)
	}
}

func (a classifyOrganizeLogger) Info(msg string, args ...any) {
	if a.l != nil {
		a.l.Info(msg, args...)
	}
}
