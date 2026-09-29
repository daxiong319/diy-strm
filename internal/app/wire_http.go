package app

import (
	"context"
	"net/http"
	"time"

	"litepan/internal/adminauth"
	"litepan/internal/announcement"
	"litepan/internal/api"
	"litepan/internal/apikey"
	"litepan/internal/backuprestore"
	"litepan/internal/buildinfo"
	"litepan/internal/cas"
	caslitepan "litepan/internal/cas/litepan"
	"litepan/internal/cache"
	"litepan/internal/config"
	"litepan/internal/coverextract"
	"litepan/internal/discover/ddb"
	"litepan/internal/discover/discovery"
	"litepan/internal/discover/dmodels"
	"litepan/internal/discover/dutil"
	"litepan/internal/logx"
	"litepan/internal/notification"
	"litepan/internal/notifychannel"
	"litepan/internal/settings"
	"litepan/internal/spacecleanup"
)

func wireHTTPServer(cfg config.Config, logs *logx.Manager, st *storeBundle, core *coreBundle, svc *servicesBundle, onRestart func()) (*http.Server, error) {
	notifySvc := notification.NewService(notification.Options{
		Repo:     st.store.Notifications,
		Accounts: st.store.Accounts,
		Log:      logs.For(logx.ModuleSystem),
	})
	notifySvc.Register(core.bus)

	// 外部通知渠道：dispatcher 订阅事件总线把通知投递到 telegram/bark 等启用渠道；
	// service 提供渠道 CRUD 与测试发送，供管理 API 使用。
	notifyDisp := notifychannel.NewDispatcher(st.store.NotifyChannels, logs.For(logx.ModuleSystem))
	notifyDisp.Register(core.bus)
	notifyChannelSvc := notifychannel.NewService(st.store.NotifyChannels, notifyDisp, logs.For(logx.ModuleAPI))
	notifyDisp.Refresh(context.Background())

	// CAS 运行器（扫描上传任务自动 CAS 化）：API 手动触发与定时调度共用同一实例。
	casRunner := cas.NewRunner(svc.uploads)
	casRunner.Log = logs.For(logx.ModuleSystem)

	// 影视发现板块：初始化 GORM 数据层（复用主库），桥接 TMDB 配置，建表并启动后台 Worker。
	if err := discoverInit(cfg, st, core, casRunner, logs); err != nil {
		return nil, err
	}

	apiKeySvc := apikey.New(apikey.Options{
		Repo:     st.store.ApiKeys,
		Settings: st.settings,
		Strm:     svc.strm,
		StrmDir:  cfg.StrmDir,
		Secret:   core.secret,
	})
	if svc.automation != nil {
		svc.automation.SetApiKeys(apiKeySvc)
	}
	backupRestoreSvc, err := backuprestore.New(backuprestore.Options{
		DataDir:   cfg.DataDir,
		DBPath:    cfg.DBPath,
		Version:   buildinfo.Version,
		DB:        st.db,
		Configs:   st.store.Configs,
		Secret:    core.secret,
		Log:       logs.For(logx.ModuleSystem),
		OnRestart: onRestart,
	})
	if err != nil {
		return nil, err
	}
	coverExtractSvc, err := coverextract.New(coverextract.Options{
		DataDir:    cfg.DataDir,
		ListenAddr: cfg.ListenAddr,
		Files:      svc.files,
		Playback:   svc.playback,
		Log:        logs.For(logx.ModuleSystem),
	})
	if err != nil {
		return nil, err
	}
	spaceCleanupSvc, err := spacecleanup.New(spacecleanup.Options{
		DataDir:           cfg.DataDir,
		StrmDir:           cfg.StrmDir,
		DBPath:            cfg.DBPath,
		StrmTasks:         st.store.StrmTasks,
		Cache:             core.cache,
		DB:                st.db,
		Logs:              logs,
		UploadActivePaths: svc.uploads.ActiveTempPaths,
		OfflineTempRoots:  svc.offlineDownloads.BuiltinTempRoots,
		OfflineActivePaths: func(ctx context.Context) []string {
			return svc.offlineDownloads.ActiveBuiltinTempPaths(ctx)
		},
		BackupTempScan: func(ctx context.Context, minAge time.Duration) ([]spacecleanup.ExternalTempEntry, error) {
			candidates, scanErr := backupRestoreSvc.OrphanTempCandidates(ctx, minAge)
			if scanErr != nil {
				return nil, scanErr
			}
			out := make([]spacecleanup.ExternalTempEntry, 0, len(candidates))
			for _, candidate := range candidates {
				out = append(out, spacecleanup.ExternalTempEntry{
					Path:       candidate.Path,
					SizeBytes:  candidate.SizeBytes,
					FileCount:  candidate.FileCount,
					DirCount:   candidate.DirCount,
					ModifiedAt: candidate.ModifiedAt,
				})
			}
			return out, nil
		},
		BackupTempClean: backupRestoreSvc.CleanupOrphanTempCandidates,
		FuseCacheStats: func(ctx context.Context) (spacecleanup.FuseStats, error) {
			if svc.fuseReadCache == nil {
				return spacecleanup.FuseStats{}, nil
			}
			stats, statsErr := svc.fuseReadCache.Stats(ctx)
			return spacecleanup.FuseStats{UsedBytes: stats.UsedBytes, Blocks: stats.BlockCount}, statsErr
		},
		ClearFuseCache: func(ctx context.Context) error {
			if svc.fuseReadCache == nil {
				return nil
			}
			return svc.fuseReadCache.ClearAll(ctx)
		},
		CoverExtractStats: func() (int, int, int64) {
			return coverExtractSvc.Stats()
		},
		ClearCoverExtract: func() (int, int, int64) {
			return coverExtractSvc.ClearWithStats()
		},
		AfterMetadataClear: func() {
			core.listHits.Reset()
			svc.playback.InvalidateAll()
			if st.settings.Bool(settings.KeyCachePersistenceEnabled) {
				_ = core.cache.SaveSnapshot(cacheDir(cfg.DataDir))
			} else {
				_ = core.cache.RemoveSnapshot(cacheDir(cfg.DataDir))
			}
		},
	})
	if err != nil {
		return nil, err
	}
	router := api.NewRouter(api.Deps{
		Logs:              logs,
		AccountSvc:        svc.account,
		AccountProfile:    svc.accountProfile,
		Accounts:          st.store.Accounts,
		Configs:           st.store.Configs,
		Settings:          st.settings,
		Cache:             core.cache,
		ListHitTracker:    core.listHits,
		Files:             svc.files,
		Favorites:         svc.favorites,
		Uploads:           svc.uploads,
		CASRunner:         casRunner,
		OfflineDownloads:  svc.offlineDownloads,
		Playback:          svc.playback,
		Strm:              svc.strm,
		CacheRetention:    svc.cacheRetention,
		MediaOrganize:     svc.mediaOrganize,
		AIOrganize:        svc.aiOrganize,
		ClassifyOrganize:  svc.classifyOrganize,
		StrmScrape:        svc.strmScrape,
		Automation:        svc.automation,
		Fuse:              svc.fuse,
		CrossTransfer:     svc.crossTransfer,
		EmbyProxy:         svc.embyProxy,
		FnosProxy:         svc.fnosProxy,
		QuarkTV:           svc.quarktv,
		ApiKeys:           apiKeySvc,
		Auth:              core.auth,
		AuthSched:         core.sched,
		AdminAuth:         adminauth.New(st.store.Configs, core.secret, logs.For(logx.ModuleAPI)),
		Notifications:     notifySvc,
		Announcement:      announcement.New(announcement.DefaultURL),
		BackupRestore:     backupRestoreSvc,
		SpaceCleanup:      spaceCleanupSvc,
		CoverExtract:      coverExtractSvc,
		NotifyChannels:    notifyChannelSvc,
		DataDir:           cfg.DataDir,
		StrmDir:           cfg.StrmDir,
		OnSettingsUpdated: cacheSettingsHook(core.cache, st.settings, cfg.DataDir),
	})

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, nil
}

// discoverInit 初始化影视发现板块：GORM 数据层（复用主库）→ 桥接 TMDB 配置 → 建表 → 启动后台 Worker。
// 失败仅记日志不阻断启动（发现板块为非核心增强功能）。
func discoverInit(cfg config.Config, st *storeBundle, core *coreBundle, casRunner *cas.Runner, logs *logx.Manager) error {
	log := logs.For(logx.ModuleSystem)
	if err := ddb.Init(cfg.DBPath, log); err != nil {
		log.Warn("发现板块数据库初始化失败", "err", err)
		return err
	}
	dmodels.BindSettings(st.settings)
	// 观影等模块的本地敏感数据加解密密钥存于配置目录
	dutil.ConfigDir = cfg.DataDir
	if err := discovery.EnsureDiscoverySchema(); err != nil {
		log.Warn("发现板块建表失败", "err", err)
		return err
	}
	// CAS 秒传：建表 + 配置桥接到 discovery_settings + 网盘驱动适配（LitePan 驱动 → cas.RapidDriver）
	cas.EnsureTable()
	cas.BindConfigStore(discoveryCASConfigGet, discoveryCASConfigSet)
	casAdapter := &caslitepan.Adapter{Manager: core.drivers}
	cas.BindDriverResolver(func(accountID int64, sourceType string) cas.RapidDriver {
		return casAdapter.Resolve(context.Background(), accountID, sourceType)
	})
	casRunner.StartScheduler(context.Background())
	discovery.StartDiscoveryWorkers()
	log.Info("发现板块已初始化")
	return nil
}

// discoveryCASConfigGet/Set 读写 discovery_settings 表（CAS 配置存 cas_engine_config 键）。
func discoveryCASConfigGet(key string) (string, bool) {
	var row struct {
		Value string
	}
	err := ddb.Db.Table("discovery_settings").Select("value").Where("`key` = ?", key).Take(&row).Error
	if err != nil || row.Value == "" {
		return "", false
	}
	return row.Value, true
}

func discoveryCASConfigSet(key, value string) {
	_ = ddb.Db.Exec(
		"INSERT INTO discovery_settings(`key`, value, updated_at) VALUES(?, ?, ?) ON CONFLICT(`key`) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at",
		key, value, time.Now()).Error
}

func cacheSettingsHook(cacheSvc *cache.Service, settingsSvc *settings.Service, dataDir string) func(map[string]string) {
	return func(changed map[string]string) {
		if !settingsTouchesCache(changed) {
			return
		}
		applyCacheRuntime(cacheSvc, settingsSvc, dataDir)
	}
}
