package api

import (
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"litepan/internal/account"
	"litepan/internal/accountprofile"
	"litepan/internal/adminauth"
	"litepan/internal/aiorganize"
	"litepan/internal/announcement"
	"litepan/internal/apikey"
	"litepan/internal/auth"
	"litepan/internal/automation"
	"litepan/internal/backuprestore"
	"litepan/internal/cache"
	"litepan/internal/cacheretention"
	"litepan/internal/cas"
	"litepan/internal/classifyorganize"
	"litepan/internal/coverextract"
	"litepan/internal/crosstransfer"
	"litepan/internal/domain"
	"litepan/internal/embyproxy"
	"litepan/internal/embywebhook"
	"litepan/internal/favorites"
	"litepan/internal/file"
	"litepan/internal/fnosproxy"
	"litepan/internal/fusemount"
	"litepan/internal/logx"
	"litepan/internal/mcp"
	"litepan/internal/mediaorganize"
	"litepan/internal/moviepilot"
	"litepan/internal/notification"
	"litepan/internal/notifychannel"
	"litepan/internal/offlinedownload"
	"litepan/internal/playback"
	"litepan/internal/quarktv"
	"litepan/internal/settings"
	"litepan/internal/share/dav"
	"litepan/internal/spacecleanup"
	"litepan/internal/strm"
	"litepan/internal/strmscrape"
	"litepan/internal/subtitle"
	"litepan/internal/upload"
)

//go:embed web
var webFS embed.FS

// Deps 是 API 层所需依赖（只依赖接口/服务，不感知具体存储实现）。
type Deps struct {
	Logs             *logx.Manager
	AccountSvc       *account.Service
	AccountProfile   *accountprofile.Service
	Accounts         domain.AccountRepository
	Configs          domain.ConfigRepository
	Settings         *settings.Service
	Cache            *cache.Service
	ListHitTracker   *cache.HitTracker
	Files            *file.Service
	Favorites        *favorites.Service
	Uploads          *upload.Manager
	OfflineDownloads *offlinedownload.Service
	Playback         *playback.Service
	Strm             *strm.Service
	CacheRetention   *cacheretention.Service
	MediaOrganize    *mediaorganize.Service
	MoviePilot       *moviepilot.Service
	AIOrganize       *aiorganize.Service
	ClassifyOrganize *classifyorganize.Service
	StrmScrape       *strmscrape.Service
	Automation       *automation.Service
	Fuse             *fusemount.Service
	CrossTransfer    *crosstransfer.Service
	EmbyProxy        *embyproxy.Service
	EmbyWebhook      *embywebhook.Service
	FnosProxy        *fnosproxy.Service
	QuarkTV          *quarktv.Service
	ApiKeys          *apikey.Service
	Auth             *auth.Service
	AuthSched        *auth.Scheduler
	AdminAuth        *adminauth.Service
	Notifications    *notification.Service
	Announcement     *announcement.Service
	BackupRestore    *backuprestore.Service
	SpaceCleanup     *spacecleanup.Service
	CoverExtract     *coverextract.Service
	NotifyChannels   *notifychannel.Service
	CASRunner        *cas.Runner
	// PlaybackRecords 播放记录仓储：面板只读与清理用它，写入由 playbackrecord.Service 负责。
	PlaybackRecords domain.PlaybackRecordRepository
	// Renames 批量重命名历史与常用组合仓储：文件操作类接口直接用仓储，与 PlaybackRecords 同风格。
	Renames domain.RenameRepository
	// MCPChat MCP 助理对话历史仓储。为 nil 时对话历史不落库（只做无状态单轮问答），
	// 而不是让整个 MCP 功能不可用。
	MCPChat *mcp.ChatStore
	// SubtitleTasks 字幕任务历史仓储。为 nil 时字幕检索/下载/校正照常工作，
	// 只是不落任务历史。
	SubtitleTasks *subtitle.TaskStore
	// SubtitleService 字幕服务实例（Muvyo 移植③）。
	//
	// 由装配层构造并注入，使「整理流程自动下载字幕」与「管理页手动操作」
	// 共用同一个实例与同一份配置快照；为 nil 时路由层按需自行惰性构造，
	// 保证单独测试 Handler 时不依赖装配层。
	SubtitleService *subtitle.Service
	DataDir         string
	StrmDir         string
	// MediaRoots 本地媒体根目录白名单，供 POST /admin/cas/generate-local 校验本地路径。
	// 为空切片表示该功能未启用。
	MediaRoots        []string
	OnSettingsUpdated func(map[string]string)
}

// Handler 持有处理请求所需的依赖。
type Handler struct {
	bootID               string
	logs                 *logx.Manager
	log                  *slog.Logger
	accountSvc           *account.Service
	accountProfile       *accountprofile.Service
	settings             *settings.Service
	cache                *cache.Service
	listHits             *cache.HitTracker
	files                *file.Service
	favorites            *favorites.Service
	uploads              *upload.Manager
	offlineDownloads     *offlinedownload.Service
	playback             *playback.Service
	strm                 *strm.Service
	cacheRetention       *cacheretention.Service
	mediaOrganize        *mediaorganize.Service
	moviePilot           *moviepilot.Service
	aiOrganize           *aiorganize.Service
	classifyOrganize     *classifyorganize.Service
	strmScrape           *strmscrape.Service
	automation           *automation.Service
	fuse                 *fusemount.Service
	crossTransfer        *crosstransfer.Service
	embyProxy            *embyproxy.Service
	embyWebhookSvc       *embywebhook.Service
	fnosProxy            *fnosproxy.Service
	quarktv              *quarktv.Service
	apiKeys              *apikey.Service
	auth                 *auth.Service
	authSched            *auth.Scheduler
	adminAuth            *adminauth.Service
	notifications        *notification.Service
	announcement         *announcement.Service
	backupRestore        *backuprestore.Service
	spaceCleanup         *spacecleanup.Service
	coverExtract         *coverextract.Service
	notifyChannels       *notifychannel.Service
	casRunner            *cas.Runner
	storePlaybackRecords domain.PlaybackRecordRepository
	renames              domain.RenameRepository
	mcpChat              *mcp.ChatStore
	subtitleTasks        *subtitle.TaskStore
	// subtitleSvc 装配层注入的字幕服务；为 nil 时 SubtitleService() 自行惰性构造。
	subtitleSvc       *subtitle.Service
	dataDir           string
	strmDir           string
	mediaRoots        []string
	onSettingsUpdated func(map[string]string)

	devMu       sync.Mutex
	devUnlocked bool
	slowLogs    slowRequestLogs
}

// NewRouter 装配并返回 HTTP 路由（含内嵌管理页面）。
func NewRouter(d Deps) http.Handler {
	apiLog := slog.Default()
	if d.Logs != nil {
		apiLog = d.Logs.For(logx.ModuleAPI)
	}
	h := &Handler{
		bootID:               uuid.NewString(),
		logs:                 d.Logs,
		log:                  apiLog,
		accountSvc:           d.AccountSvc,
		accountProfile:       d.AccountProfile,
		settings:             d.Settings,
		cache:                d.Cache,
		listHits:             d.ListHitTracker,
		files:                d.Files,
		favorites:            d.Favorites,
		uploads:              d.Uploads,
		offlineDownloads:     d.OfflineDownloads,
		playback:             d.Playback,
		strm:                 d.Strm,
		cacheRetention:       d.CacheRetention,
		mediaOrganize:        d.MediaOrganize,
		moviePilot:           d.MoviePilot,
		aiOrganize:           d.AIOrganize,
		classifyOrganize:     d.ClassifyOrganize,
		strmScrape:           d.StrmScrape,
		automation:           d.Automation,
		fuse:                 d.Fuse,
		crossTransfer:        d.CrossTransfer,
		embyProxy:            d.EmbyProxy,
		embyWebhookSvc:       d.EmbyWebhook,
		fnosProxy:            d.FnosProxy,
		quarktv:              d.QuarkTV,
		apiKeys:              d.ApiKeys,
		auth:                 d.Auth,
		authSched:            d.AuthSched,
		adminAuth:            d.AdminAuth,
		notifications:        d.Notifications,
		announcement:         d.Announcement,
		backupRestore:        d.BackupRestore,
		spaceCleanup:         d.SpaceCleanup,
		coverExtract:         d.CoverExtract,
		notifyChannels:       d.NotifyChannels,
		casRunner:            d.CASRunner,
		storePlaybackRecords: d.PlaybackRecords,
		renames:              d.Renames,
		mcpChat:              d.MCPChat,
		subtitleTasks:        d.SubtitleTasks,
		subtitleSvc:          d.SubtitleService,
		dataDir:              d.DataDir,
		strmDir:              d.StrmDir,
		mediaRoots:           d.MediaRoots,
		onSettingsUpdated:    d.OnSettingsUpdated,
	}

	r := chi.NewRouter()
	r.Use(trackResponseCommit)
	r.Use(chimw.RequestID)
	r.Use(h.attachRequestLogger)
	r.Use(h.logSlowDashboardRequests)
	r.Use(chimw.Recoverer)

	// CAS 播放直链/代理入口（对齐老 diy-strm /cas/play/* 契约，免鉴权供 Emby/播放器直接访问）
	r.Get("/cas/play/{id}", h.casPlay)
	r.Head("/cas/play/{id}", h.casPlay)
	r.Get("/cas/play/{id}/{filename}", h.casPlay)
	r.Head("/cas/play/{id}/{filename}", h.casPlay)
	r.Get("/cas/play", h.casPlay)
	r.Head("/cas/play", h.casPlay)
	r.Get("/cas/play/*", h.casPlay)
	r.Head("/cas/play/*", h.casPlay)

	r.Route("/api", func(r chi.Router) {
		r.Get("/internal/cover-source/{token}", h.coverExtractSource)
		r.Get("/health", h.health)
		r.Get("/auth/status", h.authStatus)
		r.Post("/auth/login", h.authLogin)
		r.Post("/auth/logout", h.authLogout)
		r.Post("/auth/reset-password", h.authResetPassword)
		r.Get("/strm/play/{account_id}/{file_key}/t/{token}/n/{filename}", h.strmPlay)
		r.Head("/strm/play/{account_id}/{file_key}/t/{token}/n/{filename}", h.strmPlay)
		r.Get("/strm/play/{account_id}/{file_key}/t/{token}/n/{filename}/s/{signature}", h.strmPlay)
		// 观影（guanying）内嵌代理：播放页/详情页/海报/站内接口（免登录渲染，供内嵌 iframe 用）
		r.Get("/guanying/play/{line}/{episode}", h.guanyingPlayPage)
		r.Get("/guanying/detail/{dir}/{id}", h.guanyingDetailPage)
		r.Get("/guanying/img/{dir}/{id}/{size}", h.guanyingImage)
		r.Get("/guanying/res/*", h.guanyingResProxy)

		// CAS 播放直链/代理入口（/api/cas/play 别名）
		r.Get("/cas/play/{id}", h.casPlay)
		r.Head("/cas/play/{id}", h.casPlay)
		r.Get("/cas/play/{id}/{filename}", h.casPlay)
		r.Head("/cas/play/{id}/{filename}", h.casPlay)
		r.Get("/cas/play", h.casPlay)
		r.Head("/cas/play", h.casPlay)
		r.Get("/cas/play/*", h.casPlay)
		r.Head("/cas/play/*", h.casPlay)
		r.Head("/strm/play/{account_id}/{file_key}/t/{token}/n/{filename}/s/{signature}", h.strmPlay)
		r.Get("/strm/path/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}", h.strmPathPlay)
		r.Head("/strm/path/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}", h.strmPathPlay)
		r.Get("/strm/path/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}/s/{signature}", h.strmPathPlay)
		r.Head("/strm/path/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}/s/{signature}", h.strmPathPlay)
		r.Route("/public", func(r chi.Router) {
			r.Use(h.requirePublicOrAdmin)
			r.Get("/accounts", h.publicAccounts)
			r.Get("/system-config", h.publicSystemConfig)
			r.Get("/cache/hit-rate", h.publicCacheHitRate)
		})
		// MCP 外部客户端入口：必须在 requireAdmin 组之外。
		// 它用独立 API Key 鉴权（见 mcp.go 的 requireMcpAPIKey），
		// 若挂在 requireAdmin 组内，无浏览器的 MCP 客户端会先被会话中间件拦掉。
		h.RegisterMcpPublicRoutes(r)
		r.Route("/open", func(r chi.Router) {
			r.Post("/automation/events", h.automationWebhook)
			// Emby/Jellyfin Webhook 回调：免会话鉴权，由处理器内的 API Key 开关保护。
			r.Post("/emby/webhook", h.embyWebhook)
		})
		r.Group(func(r chi.Router) {
			r.Use(h.requireAdmin)
			// MCP 站内设置与助理对话端点：走管理员会话鉴权。
			h.RegisterMcpAdminRoutes(r)
			// 字幕智能处理端点：本函数内部自带 requireAdmin。
			h.RegisterSubtitleRoutes(r)
			r.Route("/cross-transfer", func(r chi.Router) {
				r.Get("/routes", h.crossTransferRoutes)
				r.Post("/scan", h.crossTransferScan)
				r.Post("/scan/stream", h.crossTransferScanStream)
				r.Post("/probe", h.crossTransferProbe)
				r.Post("/execute", h.crossTransferExecute)
				r.Post("/plain-enqueue", h.crossTransferPlainEnqueue)
				r.Post("/plain-enqueue/stream", h.crossTransferPlainEnqueueStream)
			})
			r.Get("/logs", h.listLogs)
			r.Get("/logs/stats", h.logStats)
			r.Post("/logs/ack-errors", h.ackRecentErrors)
			r.Post("/logs/cleanup", h.cleanupLogs)
			r.Post("/logs/cleanup/keep-today", h.cleanupLogsKeepToday)
			r.Post("/logs/cleanup/all", h.cleanupLogsAll)
			r.Route("/admin", func(r chi.Router) {
				r.Get("/system-config", h.adminSystemConfig)
				r.Route("/backups", func(r chi.Router) {
					r.Get("/", h.listBackups)
					r.Post("/", h.createBackup)
					r.Post("/import", h.importBackup)
					r.Get("/status", h.backupRestoreStatus)
					r.Post("/status/ack", h.acknowledgeRestoreStatus)
					r.Delete("/pending", h.cancelPendingRestore)
					r.Post("/restart", h.restartForRestore)
					r.Get("/{id}/download", h.downloadBackup)
					r.Post("/{id}/restore", h.prepareBackupRestore)
					r.Delete("/{id}", h.deleteBackup)
				})
				r.Post("/update-credentials", h.adminUpdateCredentials)
				r.Post("/webdav-config", h.adminWebDAVConfig)
				r.Get("/emby/configs", h.listEmbyConfigs)
				r.Put("/emby/configs", h.replaceEmbyConfigs)
				r.Post("/emby/test", h.testEmbyConfig)
				r.Get("/emby/libraries", h.listEmbyLibraries)
				r.Post("/emby/refresh", h.refreshEmbyLibrary)
				r.Get("/fnos/config", h.getFnosConfig)
				r.Put("/fnos/config", h.updateFnosConfig)
				r.Post("/fnos/test", h.testFnosConfig)
				r.Put("/fnos/management", h.updateFnosManagement)
				r.Post("/fnos/management/test", h.testFnosManagement)
				r.Get("/fnos/libraries", h.listFnosLibraries)
				r.Get("/local-fs/browse", h.browseLocalFS)
				r.Get("/drivers", h.listDrivers)
				r.Get("/dev/state", h.getDevState)
				r.Post("/dev/unlock", h.unlockDevMode)
				r.Get("/accounts", h.listAccounts)
				r.Get("/overview", h.dashboardOverview)
				r.Post("/accounts", h.createAccount)
				r.Get("/accounts/{id}", h.getAccount)
				r.Put("/accounts/{id}", h.updateAccount)
				r.Delete("/accounts/{id}", h.deleteAccount)
				r.Post("/accounts/{id}/toggle", h.toggleAccount)
				r.Post("/accounts/{id}/set-default", h.setDefaultAccount)
				r.Post("/accounts/{id}/refresh-auth", h.refreshAccountAuth)
				r.Post("/accounts/{id}/refresh-profile", h.refreshAccountProfile)
				r.Get("/settings", h.getSettings)
				r.Put("/settings", h.updateSettings)
				r.Route("/api-keys", func(r chi.Router) {
					r.Get("/", h.listApiKeys)
					r.Post("/", h.createApiKey)
					r.Post("/strm/rotate", h.rotateStrmKey)
					r.Put("/{id}", h.updateApiKey)
					r.Post("/{id}/toggle", h.toggleApiKey)
					r.Delete("/{id}", h.deleteApiKey)
				})
				r.Get("/cache/stats", h.cacheStats)
				r.Get("/cache/stats/{id}", h.accountCacheStats)
				r.Post("/clear-cache", h.clearCache)
				r.Route("/cache-retention", func(r chi.Router) {
					r.Get("/configs", h.listRetentionTasks)
					r.Get("/stats", h.getRetentionStats)
					r.Get("/defaults", h.retentionDefaults)
					r.Get("/startup", h.retentionStartupRemaining)
					r.Post("/configs", h.createRetentionTask)
					r.Put("/configs/{id}", h.updateRetentionTask)
					r.Delete("/configs/{id}", h.deleteRetentionTask)
					r.Post("/configs/{id}/toggle", h.toggleRetentionTask)
					r.Post("/configs/{id}/refresh", h.refreshRetentionTask)
					r.Post("/configs/{id}/force-stop", h.forceStopRetentionTask)
					r.Post("/configs/{id}/ack-scope-warn", h.ackRetentionScopeWarn)
				})
				r.Get("/notifications", h.listNotifications)
				r.Get("/notifications/unread-count", h.notificationUnreadCount)
				r.Get("/notifications/stream", h.streamNotificationUnread)
				r.Post("/notifications/read-all", h.markAllNotificationsRead)
				r.Delete("/notifications", h.deleteAllNotifications)
				r.Post("/notifications/{id}/read", h.markNotificationRead)
				r.Delete("/notifications/{id}", h.deleteNotification)
				r.Route("/notify-channels", func(r chi.Router) {
					r.Get("/meta", h.notifyChannelMeta)
					r.Get("/", h.listNotifyChannels)
					r.Post("/", h.createNotifyChannel)
					r.Post("/test", h.testNotifyChannel)
					r.Put("/{id}", h.updateNotifyChannel)
					r.Delete("/{id}", h.deleteNotifyChannel)
				})
				r.Route("/discovery", func(r chi.Router) {
					r.Get("/meta", h.discoverMeta)
					r.Get("/cover", h.discoveryCoverProxy)
					r.Get("/explore", h.discoverExplore)
					r.Get("/explore/douban", h.discoverExploreDouban)
					r.Get("/explore/douban/catalog", h.discoverDoubanCatalog)
					r.Get("/anime/catalog", h.discoverAnimeCatalog)
					r.Get("/rankings", h.discoverRankings)
					r.Get("/rankings/maoyan", h.discoverMaoyanRankings)
					r.Get("/calendar", h.discoverCalendar)
					r.Get("/anime/calendar", h.discoverAnimeCalendar)
					r.Get("/anime/search", h.discoverAnimeSearch)
					r.Post("/anime/match", h.discoverAnimeMatch)
					r.Get("/actors", h.discoverActors)
					r.Get("/actors/{id}/works", h.discoverActorWorks)
					r.Get("/search", h.discoverSearch)
					r.Get("/details/{source}/{type}/{id}", h.discoverDetails)
					r.Get("/favorites", h.discoverFavorites)
					r.Post("/favorites", h.discoverFavoriteAdd)
					r.Post("/favorites/check", h.discoverFavoriteCheck)
					r.Delete("/favorites/{id}", h.discoverFavoriteDelete)
					r.Get("/settings", h.discoverSettingsGet)
					r.Put("/settings", h.discoverSettingsUpdate)
					r.Route("/guanying", func(r chi.Router) {
						r.Get("/session", h.guanyingSession)
						r.Post("/login", h.guanyingLogin)
						r.Post("/captcha", h.guanyingCaptcha)
						r.Post("/captcha/verify", h.guanyingCaptchaVerify)
						r.Post("/relogin", h.guanyingRelogin)
						r.Post("/test", h.guanyingTest)
						r.Delete("/session", h.guanyingClearSession)
						r.Get("/catalog", h.guanyingCatalog)
					})
					r.Route("/resources", func(r chi.Router) {
						r.Post("/search", h.searchMediaResources)
						r.Post("/copy-link", h.copyRe0ResourceLink)
						r.Post("/transfer", h.transferMediaResource)
						r.Post("/offline", h.offlineMediaResource)
					})
					r.Route("/subscriptions", func(r chi.Router) {
						r.Get("/", h.subscriptionList)
						r.Post("/", h.subscriptionSave)
						r.Get("/by-key", h.subscriptionByKey)
						r.Post("/preview", h.subscriptionPreviewMatch)
						r.Post("/run-due", h.subscriptionRunDue)
						r.Get("/{id}", h.subscriptionGet)
						r.Delete("/{id}", h.subscriptionDelete)
						r.Post("/{id}/toggle", h.subscriptionToggle)
						r.Post("/{id}/run", h.subscriptionRun)
						r.Get("/{id}/runs", h.subscriptionRuns)
						r.Get("/{id}/events", h.subscriptionEvents)
						r.Get("/{id}/items", h.subscriptionItems)
					})
					r.Route("/channels", func(r chi.Router) {
						r.Get("/", h.channelList)
						r.Post("/", h.channelSave)
						r.Get("/preview", h.channelPreview)
						r.Post("/run-now", h.channelRunNow)
						r.Delete("/{id}", h.channelDelete)
						r.Post("/{id}/toggle", h.channelToggle)
						r.Post("/{id}/reset-cursor", h.channelResetCursor)
					})
					r.Route("/monitor-records", func(r chi.Router) {
						r.Get("/", h.monitorRecordList)
						r.Get("/sources", h.monitorRecordSources)
						r.Post("/delete", h.monitorRecordDelete)
						r.Post("/clear", h.monitorRecordClear)
					})
					r.Route("/emby-missing", func(r chi.Router) {
						r.Get("/status", h.embyMissingStatus)
						r.Get("/config", h.embyMissingConfigGet)
						r.Put("/config", h.embyMissingConfigSave)
						r.Post("/config/test", h.embyMissingConfigTest)
						r.Get("/libraries", h.embyMissingLibraries)
						r.Post("/scan", h.embyMissingScanStart)
						r.Get("/scans", h.embyMissingScans)
						r.Get("/scans/{id}/results", h.embyMissingResults)
						r.Get("/scans/{id}/events", h.embyMissingEvents)
						r.Post("/subscriptions", h.embyMissingSubscribe)
					})
				})
				r.Route("/cas", func(r chi.Router) {
					r.Get("/records", h.casListRecords)
					r.Get("/records/{id}", h.casGetRecord)
					r.Delete("/records/{id}", h.casDeleteRecord)
					r.Get("/records/{id}/export", h.casExportRecord)
					r.Post("/records/{id}/restore", h.casRestoreRecord)
					r.Post("/records/{id}/play-url", h.casPlayURL)
					r.Post("/restore", h.casRestoreFromText)
					r.Get("/config", h.casGetConfig)
					r.Put("/config", h.casSaveConfig)
					r.Post("/run-once", h.casRunOnce)
					r.Post("/generate", h.casGenerateForFile)
					r.Post("/generate-local", h.casGenerateLocal)
				})
				r.Get("/announcement", h.getAnnouncement)
				r.Post("/announcement/read", h.markAnnouncementRead)
				r.Route("/strm", func(r chi.Router) {
					r.Get("/startup", h.strmStartupRemaining)
					r.Get("/tasks", h.listStrmTasks)
					r.Post("/tasks", h.createStrmTask)
					r.Put("/tasks/{id}", h.updateStrmTask)
					r.Delete("/tasks/{id}", h.deleteStrmTask)
					r.Post("/tasks/{id}/toggle", h.toggleStrmTask)
					r.Post("/tasks/{id}/run", h.runStrmTaskNow)
					r.Post("/tasks/{id}/force-stop", h.forceStopStrmTask)
					r.Get("/tasks/{id}/branches", h.listStrmBranches)
					r.Post("/tasks/{id}/branches", h.createStrmBranch)
					r.Put("/tasks/{id}/branches/{branch_id}", h.updateStrmBranch)
					r.Delete("/tasks/{id}/branches/{branch_id}", h.deleteStrmBranch)
					r.Get("/settings", h.getStrmSettings)
					r.Put("/settings", h.updateStrmSettings)
					r.Post("/replace-base-url", h.replaceStrmBaseURL)
					r.Post("/tasks/precheck-account-repair", h.precheckStrmAccountRepair)
					r.Post("/tasks/repair-account-references", h.repairStrmAccountReferences)
					r.Post("/generate-current-directory", h.generateCurrentDirectoryStrm)
					r.Post("/directory-status", h.checkStrmDirectoryStatus)
				})
				r.Route("/tools/115-strm", func(r chi.Router) {
					r.Get("/status", h.get115StrmToolStatus)
					r.Post("/enabled", h.set115StrmToolEnabled)
					r.Post("/cache/clear", h.clear115StrmDirCache)
				})
				r.Route("/tools/local-upload", func(r chi.Router) {
					r.Get("/config", h.getLocalUploadConfig)
					r.Put("/config", h.updateLocalUploadConfig)
					r.Post("/browse", h.browseLocalUpload)
					r.Post("/upload", h.createLocalUploadTasks)
				})
				r.Route("/tools/ai-organize", func(r chi.Router) {
					r.Get("/config", h.getAIOrganizeConfig)
					r.Put("/config", h.updateAIOrganizeConfig)
					r.Post("/test", h.testAIOrganizeConfig)
				})
				r.Route("/tools/classification", func(r chi.Router) {
					r.Get("/config", h.getClassificationConfig)
					r.Put("/config", h.updateClassificationConfig)
					r.Post("/tmdb-detail", h.lookupClassificationTMDBDetail)
					// 用户自定义分类规则（含 YAML 导入/导出）
					r.Get("/rules", h.listClassificationRules)
					r.Post("/rules", h.createClassificationRule)
					r.Put("/rules/{id}", h.updateClassificationRule)
					r.Delete("/rules/{id}", h.deleteClassificationRule)
					r.Post("/rules/reorder", h.reorderClassificationRules)
					r.Post("/rules/import", h.importClassificationRules)
					r.Get("/rules/export", h.exportClassificationRules)
				})
				r.Route("/tools/quarktv", func(r chi.Router) {
					r.Get("/status", h.getQuarkTVStatus)
					r.Post("/enabled", h.setQuarkTVEnabled)
					r.Get("/accounts", h.listQuarkTVAccounts)
					r.Post("/bind/start", h.startQuarkTVBind)
					r.Post("/bind/poll", h.pollQuarkTVBind)
					r.Put("/binding/settings", h.updateQuarkTVBindingSettings)
					r.Delete("/bind", h.unbindQuarkTV)
				})
				r.Route("/tools/cleanup", func(r chi.Router) {
					r.Post("/scan", h.scanSpaceCleanup)
					r.Post("/execute", h.executeSpaceCleanup)
					r.Get("/report", h.latestSpaceCleanupReport)
				})
				r.Route("/tools/cover-extract", func(r chi.Router) {
					r.Put("/enabled", h.updateCoverExtractEnabled)
					r.Get("/files", h.listCoverExtractFiles)
					r.Post("/files", h.addCoverExtractFile)
					r.Delete("/files", h.clearCoverExtractFiles)
					r.Delete("/files/{id}", h.removeCoverExtractFile)
					r.Delete("/files/{id}/frames/{frameID}", h.removeCoverFrame)
					r.Put("/files/{id}/target", h.updateCoverExtractTarget)
					r.Post("/extract", h.extractCoverFrames)
					r.Get("/images/{id}", h.coverExtractImage)
					r.Post("/save", h.saveCoverFrame)
					r.Post("/save-composed", h.saveComposedCover)
					r.Get("/runtime", h.coverExtractRuntime)
					r.Post("/runtime/download", h.downloadCoverExtractRuntime)
					r.Get("/style", h.getCoverStyle)
					r.Put("/style", h.putCoverStyle)
				})
				r.Route("/moviepilot", func(r chi.Router) {
					r.Get("/setting", h.getMoviePilotConfig)
					r.Put("/setting", h.updateMoviePilotConfig)
					r.Post("/setting/test", h.testMoviePilotConnection)
					r.Get("/version", h.getMoviePilotVersion)
					r.Get("/subscribes", h.listMoviePilotSubscribes)
					r.Post("/subscribes", h.createMoviePilotSubscribe)
					r.Post("/subscribes/{id}/search", h.searchMoviePilotSubscribe)
					r.Delete("/subscribes/{id}", h.deleteMoviePilotSubscribe)
					r.Put("/subscribes/{id}/status", h.updateMoviePilotSubscribeStatus)
					r.Get("/downloads", h.listMoviePilotDownloads)
					r.Get("/upload-tasks", h.listMoviePilotUploadTasks)
					r.Post("/upload-tasks/{id}/retry", h.retryMoviePilotUploadTask)
					r.Post("/upload-tasks/{id}/cancel", h.cancelMoviePilotUploadTask)
					r.Get("/failed-files", h.listMoviePilotFailedFiles)
					r.Post("/failed-files/{id}/identify", h.identifyMoviePilotFailedFile)
					r.Post("/failed-files/{id}/resolve", h.resolveMoviePilotFailedFile)
					r.Post("/failed-files/{id}/skip", h.skipMoviePilotFailedFile)
					r.Get("/organize-history", h.listMoviePilotOrganizeHistory)
					r.Get("/fallbacks", h.listMoviePilotFallbacks)
					r.Get("/fallbacks/summary", h.getMoviePilotFallbackSummary)
				})
				r.Route("/media-organize", func(r chi.Router) {
					r.Get("/tasks", h.listMediaOrganizeTasks)
					r.Post("/tasks", h.createMediaOrganizeTask)
					r.Put("/tasks/{id}", h.updateMediaOrganizeTask)
					r.Delete("/tasks/{id}", h.deleteMediaOrganizeTask)
					r.Post("/tasks/{id}/plan", h.planMediaOrganizeTask)
					r.Get("/tasks/{id}/plan", h.getMediaOrganizePlan)
					r.Delete("/tasks/{id}/plan", h.deleteMediaOrganizePlan)
					r.Put("/tasks/{id}/plan/actions/{action_id}", h.updateMediaOrganizePlanAction)
					r.Delete("/tasks/{id}/plan/actions/{action_id}", h.deleteMediaOrganizePlanAction)
					r.Post("/tasks/{id}/plan/actions/batch-delete", h.batchDeleteMediaOrganizePlanActions)
					r.Post("/tasks/{id}/apply", h.applyMediaOrganizeTask)
					r.Post("/tasks/{id}/run", h.runMediaOrganizeTask)
					r.Post("/tasks/{id}/stop", h.stopMediaOrganizeTask)
					r.Get("/tasks/{id}/logs", h.getMediaOrganizeLogs)
					r.Get("/tasks/{id}/progress", h.getMediaOrganizeProgress)
					r.Get("/settings", h.getMediaOrganizeSettings)
					r.Put("/settings", h.updateMediaOrganizeSettings)
					r.Get("/guess-file", h.guessMediaOrganizeFile)
					r.Post("/test-tmdb", h.testMediaOrganizeTMDB)
					r.Get("/search-tmdb", h.searchMediaOrganizeTMDB)
					r.Post("/tasks/{id}/bindings", h.setMediaOrganizeBinding)
				})
				r.Route("/strm-scrape", func(r chi.Router) {
					r.Get("/settings", h.getStrmScrapeSettings)
					r.Put("/settings", h.updateStrmScrapeSettings)
					r.Get("/scope", h.getStrmScrapeScope)
					r.Put("/scope", h.updateStrmScrapeScope)
					r.Get("/scope/directories", h.listStrmScrapeScopeDirectories)
					r.Post("/run", h.runStrmScrape)
					r.Post("/stop", h.stopStrmScrape)
					r.Get("/progress", h.getStrmScrapeProgress)
					r.Get("/items", h.listStrmScrapeItems)
					r.Post("/refresh-index", h.refreshStrmScrapeIndex)
					r.Post("/rematch", h.rematchStrmScrapeItem)
					r.Post("/rescrape", h.rescrapeStrmScrapeItem)
					r.Post("/mark-normal", h.markStrmScrapeNormal)
					r.Get("/poster", h.getStrmScrapePoster)
				})
				r.Route("/automation", func(r chi.Router) {
					r.Get("/rules", h.listAutomationRules)
					r.Post("/rules", h.createAutomationRule)
					r.Put("/rules/{id}", h.updateAutomationRule)
					r.Delete("/rules/{id}", h.deleteAutomationRule)
					r.Post("/rules/{id}/toggle", h.toggleAutomationRule)
					r.Post("/rules/{id}/run", h.runAutomationRule)
					r.Post("/validate", h.validateAutomationRule)
					r.Get("/runs", h.listAutomationRuns)
					r.Post("/runs/clear", h.clearAutomationRuns)
					r.Get("/options", h.automationOptions)
				})
				// 播放记录面板（老版 /api/emby302/playback-records 的等价物）
				r.Route("/playback-records", func(r chi.Router) {
					r.Get("/", h.playbackRecords)
					r.Get("/stats", h.playbackRecordsStats)
					r.Delete("/{id}", h.playbackRecordDelete)
					r.Post("/clear", h.playbackRecordsClear)
				})
				r.Route("/fuse", func(r chi.Router) {
					r.Get("/status", h.fuseStatus)
					r.Put("/config", h.updateFuseConfig)
					r.Get("/read-cache", h.getFuseReadCache)
					r.Put("/read-cache", h.updateFuseReadCache)
					r.Post("/read-cache/clear", h.clearFuseReadCache)
					r.Get("/mounts", h.listFuseMounts)
					r.Post("/mounts", h.createFuseMount)
					r.Put("/mounts/{id}", h.updateFuseMount)
					r.Delete("/mounts/{id}", h.deleteFuseMount)
					r.Post("/mounts/{id}/mount", h.mountFuse)
					r.Post("/mounts/{id}/unmount", h.unmountFuse)
				})
			})
			r.Post("/oauth/start", h.startOAuth)
			r.Get("/oauth/status/{session_id}", h.oauthStatus)
			r.Post("/oauth/confirm-received/{session_id}", h.oauthConfirmReceived)
			r.Post("/qr/start", h.startQRLogin)
			r.Post("/qr/poll", h.pollQRLogin)
		})
		r.Route("/files", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(h.requirePublicOrAdmin)
				r.Get("/list", h.listFiles)
				r.Get("/info", h.fileInfo)
				r.Get("/download", h.downloadFile)
				r.Head("/download", h.downloadFile)
			})
			r.Group(func(r chi.Router) {
				r.Use(h.requireAdmin)
				r.Delete("/delete", h.deleteFiles)
				r.Post("/move", h.moveFiles)
				r.Post("/copy", h.copyFiles)
				r.Put("/rename", h.renameFile)
				r.Get("/favorites", h.getFavorites)
				r.Put("/favorites", h.saveFavorites)
				r.Post("/name-align/preview", h.previewNameAlign)
				r.Post("/name-align/apply", h.applyNameAlign)
				r.Post("/batch-rename/preview", h.previewBatchRename)
				r.Post("/batch-rename/apply", h.applyBatchRename)
				r.Get("/batch-rename/history", h.listBatchRenameHistory)
				r.Post("/batch-rename/rollback", h.rollbackBatchRename)
				r.Get("/batch-rename/presets", h.listBatchRenamePresets)
				r.Post("/batch-rename/presets", h.saveBatchRenamePreset)
				r.Delete("/batch-rename/presets", h.deleteBatchRenamePreset)
				r.Post("/create-folder", h.createFolder)
				r.Post("/upload-task", h.createUploadTask)
				r.Get("/upload/runtime", h.getUploadRuntime)
				r.Put("/upload/runtime", h.updateUploadRuntime)
				r.Get("/upload/tasks", h.listUploadTasks)
				r.Get("/upload/tasks/stream", h.streamUploadTasks)
				r.Get("/upload/tasks/{taskID}", h.getUploadTask)
				r.Post("/upload/tasks/{taskID}/pause", h.pauseUploadTask)
				r.Post("/upload/tasks/{taskID}/resume", h.resumeUploadTask)
				r.Delete("/upload/tasks/{taskID}", h.deleteUploadTask)
				r.Post("/upload/tasks/batch-pause", h.batchPauseUploadTasks)
				r.Post("/upload/tasks/batch-delete", h.batchDeleteUploadTasks)
				r.Route("/offline-download", func(r chi.Router) {
					r.Get("/capabilities", h.offlineDownloadCapabilities)
					r.Post("/urls", h.addOfflineURLs)
					r.Post("/torrent/prepare", h.prepareOfflineTorrent)
					r.Post("/torrent", h.addOfflineTorrent)
					r.Get("/tasks", h.listOfflineDownloadTasks)
					r.Post("/tasks/refresh", h.refreshOfflineDownloadTasks)
					r.Post("/tasks/batch-delete", h.batchDeleteOfflineDownloadTasks)
					r.Delete("/tasks/{taskID}", h.deleteOfflineDownloadTask)
				})
			})
		})
	})

	davLog := apiLog
	if d.Logs != nil {
		davLog = d.Logs.For(logx.ModuleWebDAV)
	}
	davSrv := dav.New(dav.Deps{
		Logs:         davLog,
		Files:        d.Files,
		Playback:     d.Playback,
		Accounts:     d.Accounts,
		Configs:      d.Configs,
		Cache:        d.Cache,
		Settings:     d.Settings,
		DataDir:      d.DataDir,
		TempRegistry: d.Uploads.TempRegistry(),
	})

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // 编译期内嵌，理论上不会失败
	}
	r.Handle("/*", spaHandler(sub))

	return davBypass(davSrv, r)
}

// chi 不认 WebDAV 方法，/dav 须在外层旁路
func davBypass(dav http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
			dav.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	index, _ := fs.ReadFile(fsys, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}
		if _, statErr := fs.Stat(fsys, upath); statErr == nil {
			setStaticCacheHeader(w, upath)
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, statErr := fs.Stat(fsys, upath+".gz"); statErr == nil {
			serveCompressedAsset(w, r, fsys, upath)
			return
		}
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	})
}

func serveCompressedAsset(w http.ResponseWriter, r *http.Request, fsys fs.FS, upath string) {
	compressed, err := fsys.Open(upath + ".gz")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer compressed.Close()
	info, err := compressed.Stat()
	if err != nil {
		http.Error(w, "静态资源读取失败", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", staticContentType(upath))
	w.Header().Set("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, compressed)
		}
		return
	}

	if r.Method == http.MethodHead {
		return
	}
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		http.Error(w, "静态资源解压失败", http.StatusInternalServerError)
		return
	}
	defer reader.Close()
	_, _ = io.Copy(w, reader)
}

func setStaticCacheHeader(w http.ResponseWriter, upath string) {
	if strings.HasPrefix(upath, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	// index.html 与其它入口文件：禁止任何缓存（含磁盘快照），
	// 确保发版后浏览器必然重新拉取入口，从而引用到新的 hash 资源。
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
}

func staticContentType(name string) string {
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		return contentType
	}
	switch path.Ext(name) {
	case ".mjs", ".js":
		return "text/javascript; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	default:
		return "application/octet-stream"
	}
}

func acceptsGzip(header string) bool {
	wildcard := false
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(value), ";")
		coding := strings.ToLower(strings.TrimSpace(parts[0]))
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, raw, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if !found || !strings.EqualFold(key, "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				quality = 0
			} else {
				quality = parsed
			}
		}
		if coding == "gzip" {
			return quality > 0
		}
		if coding == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}
