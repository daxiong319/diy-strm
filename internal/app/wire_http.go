package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"litepan/internal/adminauth"
	"litepan/internal/announcement"
	"litepan/internal/api"
	"litepan/internal/apikey"
	"litepan/internal/backuprestore"
	"litepan/internal/buildinfo"
	"litepan/internal/cache"
	"litepan/internal/cas"
	caslitepan "litepan/internal/cas/litepan"
	casintake "litepan/internal/casintake"
	"litepan/internal/classifyorganize"
	"litepan/internal/config"
	"litepan/internal/coverextract"
	"litepan/internal/discover/ddb"
	"litepan/internal/discover/discovery"
	"litepan/internal/discover/dmodels"
	"litepan/internal/discover/dutil"
	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/eventbus"
	"litepan/internal/logx"
	"litepan/internal/mcp"
	"litepan/internal/notification"
	"litepan/internal/notifychannel"
	"litepan/internal/offlinedownload"
	"litepan/internal/settings"
	"litepan/internal/spacecleanup"
	"litepan/internal/strm"
	"litepan/internal/subtitle"
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

	// CAS 链接联动：CAS 化删除源文件后，把指向该文件的 .strm 改写成 CAS 播放直链，
	// 打通"播放 → 读 cas strm → 秒传恢复 → 302"。
	if strings.TrimSpace(cfg.StrmDir) != "" {
		cas.BindStrmLinker(func(sourceFileID string, recordID uint, fileName string) (int, int, error) {
			return strm.RewriteCASStrmReferences(cfg.StrmDir, sourceFileID, recordID, fileName)
		})
	}

	// CAS 自动转存：通知渠道收到用户发来的 .cas 文件后，保存到前端配置的网盘目录。
	cas.BindAutoSaveAccounts(st.store.Accounts.List)
	cas.BindAutoSaveNotifier(notifySvc.Notify)
	// 转存成功后广播事件，自动化引擎据此触发「整理 → STRM → 元数据 → 扫库 → 入库通知」。
	cas.BindAutoSaveEventPublisher(func(ctx context.Context, event eventbus.CasAutoSaved) {
		if core.bus != nil {
			core.bus.Publish(ctx, event)
		}
	})
	cas.BindAutoSaveSaver(func(ctx context.Context, accountID int64, saveDir string, file cas.AutoSaveSourceFile) (string, error) {
		rootID := casAccountRootID(ctx, st.store.Accounts, accountID)
		folderID, err := svc.uploads.EnsureTargetDir(ctx, accountID, rootID, saveDir)
		if err != nil {
			return "", err
		}
		localPath, err := writeAutoSaveTemp(cfg.DataDir, file)
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(localPath) }()
		res, err := svc.files.UploadLocal(ctx, accountID, driver.LocalUploadRequest{
			LocalPath:      localPath,
			FileName:       file.FileName,
			ParentID:       folderID,
			ConflictPolicy: "overwrite",
		})
		if err != nil {
			return "", err
		}
		if res == nil {
			return "", nil
		}
		return res.FileID, nil
	})

	// CAS 接收：Telegram 渠道收到用户发来的 .cas 文件后自动转存（手写长轮询，无第三方依赖）。
	casintake.Start(context.Background(), st.store.NotifyChannels, logs.For(logx.ModuleSystem))

	// 影视发现板块：初始化 GORM 数据层（复用主库），桥接 TMDB 配置，建表并启动后台 Worker。
	if err := discoverInit(cfg, st, core, svc, casRunner, notifySvc.Notify, logs); err != nil {
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
		// 入库通知动作复用通知中心；dispatcher 已订阅 NotificationCreated，
		// 因此这一步就把 Telegram/Bark 外部渠道一并接上了。
		svc.automation.SetNotifier(notifySvc.Notify)
	}
	if svc.embyWebhook != nil {
		// embywebhook.Notifier 是 5 参数版本，notification.Service.Notify 多出
		// accountID/refID 两个入参，这里用适配器补齐（站内广播，不绑定账号）。
		svc.embyWebhook.SetNotifier(embyWebhookNotifier{notify: notifySvc.Notify})
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
		Logs:             logs,
		AccountSvc:       svc.account,
		AccountProfile:   svc.accountProfile,
		Accounts:         st.store.Accounts,
		Configs:          st.store.Configs,
		Settings:         st.settings,
		Cache:            core.cache,
		ListHitTracker:   core.listHits,
		Files:            svc.files,
		Favorites:        svc.favorites,
		Uploads:          svc.uploads,
		CASRunner:        casRunner,
		OfflineDownloads: svc.offlineDownloads,
		Playback:         svc.playback,
		Strm:             svc.strm,
		CacheRetention:   svc.cacheRetention,
		MediaOrganize:    svc.mediaOrganize,
		MoviePilot:       svc.moviePilot,
		AIOrganize:       svc.aiOrganize,
		ClassifyOrganize: svc.classifyOrganize,
		StrmScrape:       svc.strmScrape,
		Automation:       svc.automation,
		Fuse:             svc.fuse,
		CrossTransfer:    svc.crossTransfer,
		EmbyProxy:        svc.embyProxy,
		EmbyWebhook:      svc.embyWebhook,
		FnosProxy:        svc.fnosProxy,
		QuarkTV:          svc.quarktv,
		ApiKeys:          apiKeySvc,
		Auth:             core.auth,
		AuthSched:        core.sched,
		AdminAuth:        adminauth.New(st.store.Configs, core.secret, logs.For(logx.ModuleAPI)),
		Notifications:    notifySvc,
		Announcement:     announcement.New(announcement.DefaultURL),
		BackupRestore:    backupRestoreSvc,
		SpaceCleanup:     spaceCleanupSvc,
		CoverExtract:     coverExtractSvc,
		NotifyChannels:   notifyChannelSvc,
		PlaybackRecords:  st.store.PlaybackRecords,
		Renames:          st.store.Renames,
		MCPChat:          mcp.NewChatStore(st.store.DB.WriteHandle()),
		SubtitleTasks:    subtitle.NewTaskStore(st.store.DB.WriteHandle(), st.store.DB.ReadHandle()),
		// 复用装配层构造的字幕服务单例：整理流程自动下载字幕与管理页手动操作
		// 必须看到同一份配置快照，否则管理页改了设置、整理流程仍按旧配置跑。
		SubtitleService:   svc.subtitleSvc,
		DataDir:           cfg.DataDir,
		StrmDir:           cfg.StrmDir,
		MediaRoots:        cfg.MediaRoots(),
		OnSettingsUpdated: cacheSettingsHook(core.cache, st.settings, cfg.DataDir),
	})

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}, nil
}

// notifyFn 通知写入器（notification.Service.Notify 的方法值）。
// 单独抽成类型是为了让 discoverInit 不必依赖 notification 包，避免装配层耦合。
type notifyFn func(ctx context.Context, level, category, title, message string, accountID, refID int64)

// embyWebhookNotifier 把 6 参数的 notifyFn 适配成 embywebhook.Notifier（5 参数）。
// Emby 通知属于站内广播，不绑定具体账号与引用记录，因此 accountID/refID 传 0。
type embyWebhookNotifier struct {
	notify notifyFn
}

// Notify 实现 embywebhook.Notifier。
func (n embyWebhookNotifier) Notify(ctx context.Context, level, category, title, message string) {
	if n.notify == nil {
		return
	}
	n.notify(ctx, level, category, title, message, 0, 0)
}

// discoverInit 初始化影视发现板块：GORM 数据层（复用主库）→ 桥接 TMDB 配置 → 建表 → 启动后台 Worker。
// 失败仅记日志不阻断启动（发现板块为非核心增强功能）。
func discoverInit(cfg config.Config, st *storeBundle, core *coreBundle, svc *servicesBundle, casRunner *cas.Runner, notify notifyFn, logs *logx.Manager) error {
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
	// 目录整理自定义规则表（UI 自定义规则 + YAML 导入）。失败不阻断启动：
	// classifyorganize 在无表/无规则时会回落到既有模板逻辑。
	if err := classifyorganize.EnsureClassifySchema(); err != nil {
		log.Warn("目录整理规则建表失败", "err", err)
	}
	// CAS 秒传：建表 + 配置桥接到 discovery_settings + 网盘驱动适配（LitePan 驱动 → cas.RapidDriver）
	cas.EnsureTable()
	cas.BindConfigStore(discoveryCASConfigGet, discoveryCASConfigSet)
	casAdapter := &caslitepan.Adapter{Manager: core.drivers}
	cas.BindDriverResolver(func(accountID int64, sourceType string) cas.RapidDriver {
		return casAdapter.Resolve(context.Background(), accountID, sourceType)
	})
	casRunner.StartScheduler(context.Background())
	bindDiscoveryTransfer(cfg, st, core, svc, notify, log)
	discovery.StartDiscoveryWorkers()
	log.Info("发现板块已初始化")
	return nil
}

// bindDiscoveryTransfer 注入资源转存执行器：
//   - TransferShareFn：TG 频道 / 资源搜索结果里的网盘分享链接 → 目标网盘目录（走 driver.ShareLinkSaver）。
//   - OfflineLinkFn：磁力/ed2k → 内置离线下载（走 offlinedownload.Service）。
func bindDiscoveryTransfer(cfg config.Config, st *storeBundle, core *coreBundle, svc *servicesBundle, notify notifyFn, log *slog.Logger) {
	discovery.TransferShareFn = func(ctx context.Context, text, pwd, sourceType, targetDir string) (string, int, error) {
		creds := cas.NormalizeDriveType(sourceType)
		accounts, err := st.store.Accounts.List(ctx)
		if err != nil {
			return "", 0, err
		}
		var lastErr error
		for _, acc := range accounts {
			if acc == nil || !acc.IsActive {
				continue
			}
			if err := ctx.Err(); err != nil {
				return "", 0, err
			}
			if creds != "" && cas.NormalizeDriveType(acc.DriverType) != creds {
				continue
			}
			drv, err := core.drivers.Get(ctx, acc.ID)
			if err != nil {
				lastErr = err
				continue
			}
			saver, ok := drv.(driver.ShareLinkSaver)
			if !ok {
				continue
			}
			targetParentID := ""
			if targetDir != "" && targetDir != "/" {
				rootID := casAccountRootID(ctx, st.store.Accounts, acc.ID)
				item, err := svc.files.ResolvePath(ctx, acc.ID, rootID, targetDir)
				if err != nil {
					lastErr = fmt.Errorf("目标目录不存在 %q：%w", targetDir, err)
					continue
				}
				if item == nil || !item.IsDir {
					lastErr = fmt.Errorf("目标目录不存在 %q", targetDir)
					continue
				}
				targetParentID = item.ID
			}
			res, err := saver.SaveShareLink(ctx, driver.ShareLinkSaveRequest{
				ShareURL:       text,
				SharePwd:       pwd,
				TargetParentID: targetParentID,
			})
			if err != nil {
				lastErr = err
				continue
			}
			if res == nil {
				return "", 0, nil
			}
			return res.Title, res.Total, nil
		}
		if lastErr != nil {
			return "", 0, lastErr
		}
		return "", 0, fmt.Errorf("没有支持分享转存的%s账号，请先在账号管理中添加", cas.NormalizeDriveType(sourceType))
	}

	discovery.OfflineLinkFn = func(ctx context.Context, link, savePath string) error {
		accountID := discoveryOfflineAccountID(ctx, st)
		if accountID == 0 {
			return fmt.Errorf("未找到可用于离线下载的网盘账号")
		}
		_, err := svc.offlineDownloads.AddURLs(ctx, offlinedownload.AddURLParams{
			AccountID: accountID,
			URLs:      []string{link},
			FileName:  "",
		})
		return err
	}

	// ---- TG 频道订阅 watcher 的注入点 ----
	// 这三项在 discovery 包内无法直接实现（通知服务会成环、驱动能力在 driver 层），
	// 未绑定时 watcher 只是静默跳过，功能看着「没反应」且不报错——所以必须在这里接上。

	// 聚合帖防误转：转存前只读探测分享顶级名称，与订阅关键词复核。
	// 失败/未绑定时 watcher 一律放行（宁放勿拦），因此这里的错误不影响主流程。
	discovery.ShareTitleProbeFn = func(ctx context.Context, shareURL, pwd string) (string, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for _, acc := range discoveryActiveAccounts(ctx, st) {
			if cas.NormalizeDriveType(acc.DriverType) != cas.NormalizeDriveType("123_open") {
				continue
			}
			drv, err := core.drivers.Get(ctx, acc.ID)
			if err != nil {
				continue
			}
			prober, ok := drv.(driver.ShareDirProbe)
			if !ok {
				continue
			}
			name, err := prober.ProbeShareTitle(probeCtx, driver.ShareLinkSaveRequest{
				ShareURL: shareURL,
				SharePwd: pwd,
			})
			if err != nil {
				return "", err
			}
			return name, nil
		}
		return "", nil
	}

	discovery.TransferSuccessNotifyFn = func(subscriptionID uint, title, shareTitle, provider string) {
		if notify == nil {
			return
		}
		// 分享标题为空时退回订阅标题，避免出现「已转存「」」这种空引号文案。
		shown := shareTitle
		if strings.TrimSpace(shown) == "" {
			shown = title
		}
		msg := fmt.Sprintf("订阅「%s」已转存「%s」到 %s 保存目录", title, shown, providerDisplayName(provider))
		notify(context.Background(), "info", "discovery", "TG 频道订阅转存成功", msg, 0, int64(subscriptionID))
	}

	discovery.TransferFailedNotifyFn = func(subscriptionID uint, title, shareURL, provider, reason string) {
		if notify == nil {
			return
		}
		msg := fmt.Sprintf("订阅「%s」转存 %s 失败：%s", title, providerDisplayName(provider), reason)
		notify(context.Background(), "error", "discovery", "TG 频道订阅转存失败", msg, 0, int64(subscriptionID))
	}

	_ = cfg
	log.Debug("发现板块转存执行器已注入")
}

// discoveryActiveAccounts 当前启用账号（探测/转存共用，失败返回 nil 由调用方降级）。
func discoveryActiveAccounts(ctx context.Context, st *storeBundle) []*domain.Account {
	accounts, err := st.store.Accounts.List(ctx)
	if err != nil {
		return nil
	}
	out := make([]*domain.Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc != nil && acc.IsActive {
			out = append(out, acc)
		}
	}
	return out
}

// providerDisplayName 网盘编码转中文展示名（通知文案用）。
// 统一走 cas.DriveDisplayName，避免各处各写一份映射而互相矛盾
// （此处曾把 cloud139 也写成「天翼云盘」，但 139 是移动、189 才是天翼）。
func providerDisplayName(provider string) string {
	return cas.DriveDisplayName(provider)
}

// discoveryOfflineAccountID 选择内置离线下载可用账号（优先 123/115/夸克等原生支持离线的驱动）。
func discoveryOfflineAccountID(ctx context.Context, st *storeBundle) int64 {
	accounts, err := st.store.Accounts.List(ctx)
	if err != nil {
		return 0
	}
	preferred := []string{"123_open", "115_open", "quark"}
	for _, want := range preferred {
		for _, acc := range accounts {
			if acc == nil || !acc.IsActive {
				continue
			}
			if cas.NormalizeDriveType(acc.DriverType) == cas.NormalizeDriveType(want) {
				return acc.ID
			}
		}
	}
	for _, acc := range accounts {
		if acc != nil && acc.IsActive {
			return acc.ID
		}
	}
	return 0
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

// casAccountRootID 读取账号配置里的根目录 ID（缺省为空，由驱动自行归一为各自根）。
func casAccountRootID(ctx context.Context, repo domain.AccountRepository, accountID int64) string {
	acc, err := repo.Get(ctx, accountID)
	if err != nil || acc == nil || strings.TrimSpace(acc.Config) == "" {
		return ""
	}
	var cfg struct {
		RootFolderID string `json:"root_folder_id"`
	}
	if err := json.Unmarshal([]byte(acc.Config), &cfg); err != nil {
		return ""
	}
	root := strings.TrimSpace(cfg.RootFolderID)
	if root == "0" || root == "/" || strings.EqualFold(root, "root") {
		return ""
	}
	return root
}

// writeAutoSaveTemp 把收到的 .cas 内容落到本地临时文件，供驱动上传。
func writeAutoSaveTemp(dataDir string, file cas.AutoSaveSourceFile) (string, error) {
	dir := filepath.Join(dataDir, "cas_autosave")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "*.cas")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_, werr := f.WriteString(file.Content)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return "", werr
	}
	if cerr != nil {
		_ = os.Remove(path)
		return "", cerr
	}
	return path, nil
}
