package domain

import "time"

const NotificationCategoryCacheScopeWarn = "cache_scope_warn"
const NotificationCategoryStrmScanWarn = "strm_scan_warn"
const NotificationCategoryStrmScrapeWarn = "strm_scrape_warn"
const NotificationCategoryFuseMountWarn = "fuse_mount_warn"
const NotificationCategoryQuarkTVWarn = "quarktv_warn"

// Emby Webhook 相关通知分类。
const NotificationCategoryEmbyIngest = "emby_ingest"
const NotificationCategoryEmbyPlayback = "emby_playback"
const NotificationCategoryEmbyDeleted = "emby_deleted"

// 以下分类原先散落各处以裸字符串出现（T12 起补上常量，字面量不变）。
//
// 为什么不合并成一个大 const 块：这五个不是通知「领域」的分类，而是调用方
// （认证、自动化、CAS 转存、本地上传）的领域名，统一进来会让 notification.go
// 变成杂物袋。分开写并注明来源，方便以后追溯。
const (
	// 存储账号认证失效 / 恢复。见 internal/notification/service.go 的 auth 分支。
	NotificationCategoryAuth = "auth"
	// 事件总线兜底分类：非 Notify() 来源的事件在 category 为空时归到这里。
	NotificationCategorySystem = "system"
	// 自动化动作产出的通用通知（internal/automation/service_notify.go）。
	NotificationCategoryAutomation = "automation"
	// CAS 自动转存结果（internal/cas/autosave.go）。
	NotificationCategoryCas = "cas"
	// 服务器上传任务告警（internal/api/local_upload.go）。
	NotificationCategoryUpload = "upload"
)

type Notification struct {
	ID        int64
	Level     string
	Category  string
	Title     string
	Message   string
	AccountID int64
	RefID     int64
	IsRead    bool
	CreatedAt time.Time
}
