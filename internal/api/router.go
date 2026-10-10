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
	"litepan/internal/inspection"
	"litepan/internal/logx"
	"litepan/internal/mcp"
	"litepan/internal/medialibshare"
	"litepan/internal/mediaorganize"
	"litepan/internal/mediarequest"
	"litepan/internal/mediaupgrade"
	"litepan/internal/moviepilot"
	"litepan/internal/notification"
	"litepan/internal/notifychannel"
	"litepan/internal/offlinedownload"
	"litepan/internal/playback"
	"litepan/internal/playbackfallback"
	"litepan/internal/playmonitor"
	"litepan/internal/playpath"
	"litepan/internal/quarktv"
	"litepan/internal/rbac"
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
	Inspection       *inspection.Service
	CoverExtract     *coverextract.Service
	NotifyChannels   *notifychannel.Service
	// NotifyRetries 补发队列仓储：webhook 投递失败的记录与手动重投。
	NotifyRetries domain.NotifyRetryRepository
	// NotifyRetryRunner 补发 worker。API 手动重投时立刻跑一轮，
	// 否则用户点了按钮要等最多 30 秒才看到结果，会以为按钮坏了。
	NotifyRetryRunner *notifychannel.RetryWorker
	CASRunner         *cas.Runner
	// PlaybackRecords 播放记录仓储：面板只读与清理用它，写入由 playbackrecord.Service 负责。
	PlaybackRecords domain.PlaybackRecordRepository
	// PlayMonitor 播放监控服务：三态实时会话 + 观影报告生成。
	PlayMonitor *playmonitor.Service
	// PlayTraffic 日流量桶仓储：今日/本月/累计与排行。
	PlayTraffic domain.PlayTrafficRepository
	// Renames 批量重命名历史与常用组合仓储：文件操作类接口直接用仓储，与 PlaybackRecords 同风格。
	Renames domain.RenameRepository
	// MCPChat MCP 助理对话历史仓储。为 nil 时对话历史不落库（只做无状态单轮问答），
	// 而不是让整个 MCP 功能不可用。
	MCPChat *mcp.ChatStore
	// SubtitleTasks 字幕任务历史仓储。为 nil 时字幕检索/下载/校正照常工作，
	// 只是不落任务历史。
	SubtitleTasks *subtitle.TaskStore
	// SubtitleService 字幕服务实例（参考实现 移植③）。
	//
	// 由装配层构造并注入，使「整理流程自动下载字幕」与「管理页手动操作」
	// 共用同一个实例与同一份配置快照；为 nil 时路由层按需自行惰性构造，
	// 保证单独测试 Handler 时不依赖装配层。
	SubtitleService *subtitle.Service
	// PlayPath 播放路径映射（T17）。为 nil 时路径原样透传。
	PlayPath *playpath.Service
	// CrossAccount 跨账户播放转移（T17）。为 nil 时不发生任何切换。
	CrossAccount *playbackfallback.Service
	// MediaUpgrade 洗版服务（参考实现 移植⑦）。
	//
	// 为 nil 时 /media-upgrade 下所有接口返回「该操作不支持」，
	// 而不是让管理页出现一个点开就报错的入口。
	MediaUpgrade *mediaupgrade.Service
	// RBAC 用户与权限服务（参考实现 移植⑧）。
	//
	// 为 nil 或 mo_rbac_enabled=false 时所有 RequirePermission 中间件直接放行，
	// 与本功能上线前的行为逐字一致（验收①）。
	RBAC *rbac.Service
	// MediaRequest 求片中心服务（参考实现 移植⑨）。
	//
	// 为 nil 时求片中心整体不可用：管理台接口返回「该操作不支持」，
	// 求片站端口不起（见 mediarequest.Listener）。求片中心是可选项，
	// 不装配它不应该让管理台出现任何异常。
	MediaRequest *mediarequest.Service
	// RequestSigner 求片站会话签发器。
	//
	// 与 MediaRequest 分开传：签发器只需要 core secret，不需要数据库。
	// 分开之后单测可以只测签名而不必装配整个服务。
	RequestSigner *mediarequest.SessionSigner
	// RequestPortalFS 求片站构建产物（request.html + assets）。
	//
	// 为 nil 时求片站页面返回一段「前端未构建」的提示，
	// 而不是 500 —— 排查「端口通了但页面空白」时这条提示直接指明原因。
	RequestPortalFS fs.FS
	// RequestPortalHTML 求片站入口 HTML（RequestPortalFS 里的 request.html）。
	RequestPortalHTML []byte
	// LibraryShare 免登录分享页服务（参考实现 移植⑩）。
	//
	// 为 nil 时分享相关端点**一个都不注册**：访客路由 404、管理端 404。
	// 这里不注册「拒绝中间件」是有意的 —— 一个不存在的分享功能，
	// 在路由表里应该表现为根本没有这条路径，而不是有一个专门回 403 的路径。
	// 装配层不装它时管理台不出现任何异常入口（见 AdminView 的功能开发中白名单）。
	LibraryShare *medialibshare.Service
	DataDir      string
	StrmDir      string

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
	playPath             *playpath.Service
	crossAccount         *playbackfallback.Service
	strm                 *strm.Service
	cacheRetention       *cacheretention.Service
	mediaOrganize        *mediaorganize.Service
	mediaUpgrade         *mediaupgrade.Service
	rbac                 *rbac.Service
	mediaRequest         *mediarequest.Service
	requestSigner        *mediarequest.SessionSigner
	portalFS             fs.FS
	portalHTML           []byte
	libraryShare         *medialibshare.Service
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
	inspection           *inspection.Service
	coverExtract         *coverextract.Service
	notifyChannels       *notifychannel.Service
	notifyRetries        domain.NotifyRetryRepository
	notifyRetryRunner    *notifychannel.RetryWorker
	casRunner            *cas.Runner
	storePlaybackRecords domain.PlaybackRecordRepository
	playMonitor          *playmonitor.Service
	playTraffic          domain.PlayTrafficRepository
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

// newHandler 由 Deps 装配 Handler。
//
// 单独抽出来是因为现在有**两棵**互不相干的路由树共用同一份 Deps：
// 管理台（NewRouter）和求片站（NewRequestPortalRouter）。
// 求片站必须能从同一份依赖里拿到 mediaRequest / requestSigner / adminAuth / rbac ——
// 它要自己算 Principal、自己校验凭据；
// 如果另起一个 Handler 手工挑字段，两边就会各有一份「我以为装上了」的清单，
// 而那正是这个仓库最常见的那类假接线。
func newHandler(d Deps) *Handler {
	apiLog := slog.Default()
	if d.Logs != nil {
		apiLog = d.Logs.For(logx.ModuleAPI)
	}
	// 求片站前端产物默认从内嵌 embed 里取，装配层显式注入时才让注入值生效。
	//
	// 默认值放在这里而不是塞进 Deps 由装配层填，是为了少两处「必须记得赋值」的义务：
	// 这两个字段漏赋值的后果不是编译错误，而是求片站打开是一片空白页提示，
	// 属于那种只有真机点开才会发现的坑。
	if d.RequestPortalFS == nil && d.RequestPortalHTML == nil {
		d.RequestPortalFS, d.RequestPortalHTML = LoadPortalFS(EmbeddedWebFS())
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
		playPath:             d.PlayPath,
		crossAccount:         d.CrossAccount,
		strm:                 d.Strm,
		cacheRetention:       d.CacheRetention,
		mediaOrganize:        d.MediaOrganize,
		mediaUpgrade:         d.MediaUpgrade,
		rbac:                 d.RBAC,
		mediaRequest:         d.MediaRequest,
		requestSigner:        d.RequestSigner,
		portalFS:             d.RequestPortalFS,
		portalHTML:           d.RequestPortalHTML,
		libraryShare:         d.LibraryShare,
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
		inspection:           d.Inspection,
		coverExtract:         d.CoverExtract,
		notifyChannels:       d.NotifyChannels,
		notifyRetries:        d.NotifyRetries,
		notifyRetryRunner:    d.NotifyRetryRunner,
		casRunner:            d.CASRunner,
		storePlaybackRecords: d.PlaybackRecords,
		playMonitor:          d.PlayMonitor,
		playTraffic:          d.PlayTraffic,
		renames:              d.Renames,
		mcpChat:              d.MCPChat,
		subtitleTasks:        d.SubtitleTasks,
		subtitleSvc:          d.SubtitleService,
		dataDir:              d.DataDir,
		strmDir:              d.StrmDir,
		mediaRoots:           d.MediaRoots,
		onSettingsUpdated:    d.OnSettingsUpdated,
	}
	return h
}

// NewRouter 装配并返回管理台路由树（含内嵌管理页面）。
func NewRouter(d Deps) http.Handler {
	apiLog := slog.Default()
	if d.Logs != nil {
		apiLog = d.Logs.For(logx.ModuleAPI)
	}
	h := newHandler(d)

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

	// 免登录分享页访客侧（参考实现 移植⑩）：换令牌 / 上报事件 / 取流。
	//
	// 刻意挂在 /api 之外、也不在下面的 requireAdmin 组里 ——
	// 访客没有账号会话，唯一的凭证是 X-Share-Token 头。
	// 这组路由由 medialibshare.Service 为 nil 时整体不注册（⇒ 全 404）。
	//
	// ⚠️ GET /share/{code} 必须注册在文件末尾 r.Handle("/*", spaHandler(sub))
	// **之前**：chi 会优先匹配更具体的 pattern，但写在兜底之前
	// 一眼就能看出「分享页是有意抢在 SPA 兜底前面的」，
	// 将来有人把注册顺序调换过去时 review 一眼能看出问题。
	h.RegisterLibraryShareGuestRoutes(r)

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
		r.Get("/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}", h.strmRedirectPlay)
		r.Head("/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}", h.strmRedirectPlay)
		r.Get("/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}/s/{signature}", h.strmRedirectPlay)
		r.Head("/strm/redirect/{account_id}/{file_key}/t/{token}/n/{filename}/s/{signature}", h.strmRedirectPlay)
		r.Get("/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}", h.strmPathRedirectPlay)
		r.Head("/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}", h.strmPathRedirectPlay)
		r.Get("/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}/s/{signature}", h.strmPathRedirectPlay)
		r.Head("/strm/path-redirect/{account_id}/{root_key}/{path_key}/t/{token}/n/{filename}/s/{signature}", h.strmPathRedirectPlay)
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
			// 用户与权限（RBAC，参考实现 移植⑧）：注册在 requireAdmin 组内，
			// 所以它自己只管权限、不重复管会话。
			h.RegisterRBACRoutes(r)
			// 求片中心管理台（参考实现 移植⑨）：审核、规则、统计。
			// 与 RBAC 同理，函数内部只管权限、不重复管会话。
			h.RegisterRequestCenterRoutes(r)
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
				r.Get("/overview", h.dashboardOverview)
				// 账号与系统设置都在这组里：任一单独授权都能用，
				// 但改密码、删账号这类改的是全局凭据，所以与系统设置同级。
				r.Group(func(r chi.Router) {
					r.Use(h.requirePermission(rbac.PermAccountManage))
					r.Get("/accounts", h.listAccounts)
					r.Post("/accounts", h.createAccount)
					r.Get("/accounts/{id}", h.getAccount)
					r.Put("/accounts/{id}", h.updateAccount)
					r.Delete("/accounts/{id}", h.deleteAccount)
					r.Post("/accounts/{id}/toggle", h.toggleAccount)
					r.Post("/accounts/{id}/set-default", h.setDefaultAccount)
					r.Post("/accounts/{id}/refresh-auth", h.refreshAccountAuth)
					r.Post("/accounts/{id}/refresh-profile", h.refreshAccountProfile)
				})
				// 系统设置：mo_* 全部改在这里，所以 system.manage 是一把万能钥匙，
				// 也是洗版开关与 RBAC 开关自己所在的地方。
				r.Group(func(r chi.Router) {
					r.Use(h.requirePermission(rbac.PermSystemManage))
					r.Get("/settings", h.getSettings)
					r.Put("/settings", h.updateSettings)
					// 设置项搜索索引（⌘G）。刻意挂在 system.manage 组内：
					// 它虽然不含任何配置值，但条目本身就是一张功能清单，
					// 对谁开放等同于对谁开放「本系统有哪些功能」。
					r.Get("/settings/index", h.getSettingsIndex)
				})
				r.Route("/api-keys", func(r chi.Router) {
					// API Key 是绕过会话的长期凭据，权限与系统设置同级。
					r.Use(h.requirePermission(rbac.PermSystemManage))
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
				r.Route("/notify-scenes", func(r chi.Router) {
					r.Get("/", h.notifySceneList)
				})
				r.Route("/notify-retries", func(r chi.Router) {
					r.Get("/", h.notifyRetryList)
					r.Post("/clear", h.notifyRetryClear)
					r.Post("/{id}/redrive", h.notifyRetryRedrive)
					r.Get("/{id}", h.notifyRetryDetail)
				})
				r.Route("/dir-refs", func(r chi.Router) {
					// 目录配置防呆提示（T32）。只读接口，但要读洗版规则和
					// 自动化规则 —— 这两处的配置管理权限不同，所以放在
					// /admin 平级而不是塞进 media-upgrade/automation 的子树。
					r.Get("/", h.resolveDirRef)
				})
				r.Route("/discovery", func(r chi.Router) {
					r.Use(h.requirePermission(rbac.PermDiscoverView))
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
						// 这条路也会花配额，绕过上面的 /offline-download 组，
						// 所以必须单独再圈一次，否则从求片页就能绕过去下东西。
						r.Group(func(r chi.Router) {
							r.Use(h.requirePermission(rbac.PermOfflineDownloadRun))
							r.Post("/offline", h.offlineMediaResource)
						})
					})
					r.Route("/subscriptions", func(r chi.Router) {
						// 新增订阅要花钱，所以是超管专属。判定只读 rbac 里的
						// superOnlyPermissions，这两项永远不下放 ——
						// permission.manage 拿到手也不能勾。
						r.Group(func(r chi.Router) {
							r.Use(h.requirePermission(rbac.PermSubscriptionCreate))
							r.Post("/", h.subscriptionSave)
						})
						r.Get("/", h.subscriptionList)
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
					r.Use(h.requirePermission(rbac.PermCASManage))
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
					// T17 播放路径映射：规则读写与「测试路径」。
					r.Get("/path-mapping", h.strmPathMappingRules)
					r.Put("/path-mapping", h.strmPathMappingSave)
					r.Post("/path-mapping/test", h.strmPathMappingTest)
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
					// C-8 只读出口：给洗版筛选、清理保护等跨模块消费者
					// 一个"当前配了哪些分类目录"的查询口。它挂在 tools 下
					// 而不是分类规则编辑口旁边，是为了让消费者不必先知道
					// 分类模板存在哪里——它们只关心清单。
					r.Get("/categories", h.listClassificationCategories)
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
				// 巡检与空间清理分成两条路由：两者都扫描网盘目录树，
				// 但语义完全不同 —— 巡检只报告与执行有预览的修复，
				// 清理是另一套参数与报告模型。共用一个前缀会让前端
				// 出现「scan 到底打哪边」的歧义。
				r.Route("/tools/inspection", func(r chi.Router) {
					r.Get("/checkers", h.inspectionCheckers)
					r.Post("/scan", h.inspectionScan)
					r.Get("/preview", h.inspectionPreview)
					r.Post("/repair", h.inspectionRepair)
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
				r.Route("/media-upgrade", func(r chi.Router) {
					// 洗版会删用户文件，权限与它自己那个开关分开：
					// 开关决定「这个功能开不开启」，权限决定「这个人能不能用」。
					r.Use(h.requirePermission(rbac.PermMediaUpgradeManage))
					// 扫描（只判定，不动文件）
					r.Get("/scans", h.listMediaUpgradeScans)
					r.Post("/scans", h.createMediaUpgradeScan)
					r.Get("/scans/{id}", h.getMediaUpgradeScan)
					r.Post("/scans/{id}/execute", h.executeMediaUpgradeScan)
					// 判定记录
					r.Get("/records", h.listMediaUpgradeRecords)
					r.Get("/records/{id}", h.getMediaUpgradeRecord)
					// 规则
					r.Get("/rules", h.listMediaUpgradeRules)
					r.Post("/rules", h.createMediaUpgradeRule)
					r.Put("/rules/{id}", h.updateMediaUpgradeRule)
					r.Delete("/rules/{id}", h.deleteMediaUpgradeRule)
					// 规则试算：纯函数、只读、无副作用。
					r.Post("/rule-trial", h.mediaUpgradeRuleTrial)
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
					// C-7 分类预览：只算不写。挂在 media-organize 下而不是
					// tools/classification 下，是因为它回答的是「这个文件会被
					// 放到哪个目录」这个整理计划的问题，而它是媒体整理页上的一行提示。
					// 分类规则的增删改仍然留在 tools/classification。
					r.Post("/classification/preview", h.previewClassification)
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
				// 播放监控 + 观影报告（计费中 / CDN 直连 / 局域网 三态）。
				//
				// 挂在 /admin 子树**里面**，真实路径是 /api/admin/play-monitor/*，
				// 与前端 web/src/api/playMonitor.ts 一致。理由同上：
				// library-share 挂到 /admin 之外时，前端按 /admin/ 写，
				// 五个操作全部 404。不在前面再挂一份重复路由 ——
				// 两套路径都能访问同一功能，日后改一处漏一处。
				r.Route("/play-monitor", func(r chi.Router) {
					r.Get("/sessions", h.playMonitorSessions)
					r.Get("/traffic", h.playMonitorTraffic)
					r.Post("/traffic/clear", h.playMonitorTrafficClear)
					r.Get("/report", h.playReport)
					r.Get("/report/chart", h.playReportChart)
					r.Get("/options", h.playMonitorOptions)
				})
				// RSS 订阅源（T16）。真实路径 /api/admin/rss-*。
				//
				// 挂在 /admin 子树**里面**（理由同上：library-share 挂到 /admin
				// 之外时前端按 /admin/ 写，五个操作全部 404）。因为前缀是
				// /rss-sources 而不是 /discovery/rss-sources，所以权限闸要自己
				// 包一层，复用发现板块的 PermDiscoverView —— RSS 就是发现板块的
				// 第三个入口，不给它单开一个权限位。
				//
				// ⚠️ /preview、/sync 这类字面量路径必须排在 /{id} 之前：
				// chi 逐段匹配，/{id} 会把 "preview" 当成 id 吃掉。
				r.Group(func(r chi.Router) {
					r.Use(h.requirePermission(rbac.PermDiscoverView))
					r.Route("/rss-sources", func(r chi.Router) {
						r.Get("/", h.rssSources)
						r.Post("/", h.rssSourceCreate)
						r.Get("/{id}", h.rssSourceDetail)
						r.Put("/{id}", h.rssSourceUpdate)
						r.Delete("/{id}", h.rssSourceDelete)
						r.Post("/{id}/sync", h.rssSourceSync)
					})
					r.Route("/rss-history", func(r chi.Router) {
						r.Get("/", h.rssHistory)
						r.Delete("/{id}", h.rssHistoryDelete)
					})
					r.Post("/rss-preview", h.rssPreview)
					r.Post("/rss-sync", h.rssSyncAll)
					r.Get("/rss-options", h.rssOptions)
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
				// 免登录分享页管理端（参考实现 移植⑩）：创建/列表/改期/撤销/统计。
				//
				// 挂在 /admin 子树**里面**，真实路径是 /api/admin/library-shares/*，
				// 与 T10 任务书 §2 和前端 web/src/api/libraryShare.ts 一致。
				//
				// 它曾经挂在上面那个 requireAdmin 组里、也就是 /admin 之外，
				// 于是真实路径变成 /api/library-shares/*，而前端按 /admin/ 写 ——
				// 分享管理页五个操作全部 404。T29 的前后端路径一致性守卫查出来的，
				// 当时按 T29 的「只报告不修」约定记在白名单里。
				//
				// 不在前端去掉 /admin 来迁就错误的注册位置，也不在这儿再挂一份
				// /library-shares 重复路由：两套路径都能访问同一功能，
				// 日后改一处漏一处，是新的假接线来源。
				h.RegisterLibraryShareRoutes(r)
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
				// 这一组全是「改动文件」的动作：删、改名、移动、建目录。
				// 只圈写操作、不圈上面那组只读接口（浏览、下载），
				// 这样「只能看不能动」的账号还能当只读运维用。
				r.Use(h.requireAdmin)
				r.Use(h.requirePermission(rbac.PermFileManage))
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
				// 离线下载要花配额，同样超管专属（理由见上面 subscriptionSave）。
				r.Group(func(r chi.Router) {
					r.Use(h.requirePermission(rbac.PermOfflineDownloadRun))
					r.Post("/upload-task", h.createUploadTask)
				})
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
					// 只圈住「真正花配额」的三个端点，任务列表仍然可读 ——
					// 一个只能看不能下的运维角色需要知道任务跑成什么样。
					r.Group(func(r chi.Router) {
						r.Use(h.requirePermission(rbac.PermOfflineDownloadRun))
						r.Post("/urls", h.addOfflineURLs)
						r.Post("/torrent/prepare", h.prepareOfflineTorrent)
						r.Post("/torrent", h.addOfflineTorrent)
					})
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
	// 访客分享页 HTML（/share/{code}）：与上面 RegisterLibraryShareGuestRoutes
	// 成对，两处都在这一行之前 —— 见那里的注释。
	h.RegisterLibrarySharePageRoutes(r)
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
