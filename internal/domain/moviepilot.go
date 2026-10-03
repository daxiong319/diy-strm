package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MoviePilot 上传任务状态。
const (
	MoviePilotUploadPending   = "pending"
	MoviePilotUploadUploading = "uploading"
	MoviePilotUploadUploaded  = "uploaded"
	MoviePilotUploadFailed    = "failed"
	MoviePilotUploadCanceled  = "canceled"
)

// MoviePilot 识别失败文件状态。
const (
	MoviePilotFailedPending  = "pending"
	MoviePilotFailedResolved = "resolved"
	MoviePilotFailedSkipped  = "skipped"
)

// MoviePilot 降级兜底（次数触发自动补找）状态。
const (
	MoviePilotFallbackPending   = "pending"   // 已触发，等待执行
	MoviePilotFallbackRunning   = "running"   // 正在转交 MoviePilot 处理
	MoviePilotFallbackSucceeded = "succeeded" // 已成功转交
	MoviePilotFallbackFailed    = "failed"    // 转交失败（不影响本地订阅继续运行）
)

// MoviePilot 降级动作（对齐参考实现 mp_fallback_*_action 选项）。
const (
	MoviePilotFallbackActionSubscribe             = "subscribe"               // 仅添加 MoviePilot 订阅
	MoviePilotFallbackActionDownload              = "download"                // 仅让 MP 搜索下载（默认）
	MoviePilotFallbackActionDownloadThenSubscribe = "download_then_subscribe" // 先搜索下载，无资源再订阅
)

// MoviePilot 兜底触发来源。
const (
	MoviePilotFallbackTriggerSearch       = "search"       // 搜索次数触发
	MoviePilotFallbackTriggerSubscription = "subscription" // 订阅轮次触发
)

// DefaultMoviePilotFallbackThreshold 兜底阈值默认值（累计无结果次数 / 连续无进展轮数）。
const DefaultMoviePilotFallbackThreshold = 3

// NormalizeMoviePilotFallbackAction 归一化降级动作，未知值回退默认 download。
func NormalizeMoviePilotFallbackAction(action string) string {
	switch strings.TrimSpace(action) {
	case MoviePilotFallbackActionSubscribe:
		return MoviePilotFallbackActionSubscribe
	case MoviePilotFallbackActionDownloadThenSubscribe:
		return MoviePilotFallbackActionDownloadThenSubscribe
	default:
		return MoviePilotFallbackActionDownload
	}
}

// MoviePilotFallbackMediaKey 构造「影片+季」计数唯一键（形如 tv:12345:2 / movie:9:0）。
//
// 计数与阈值判定都以它为粒度：同一影片的不同季互不干扰，不同影片的同季也互不干扰。
func MoviePilotFallbackMediaKey(mediaType string, tmdbID int64, season int) string {
	ty := strings.TrimSpace(mediaType)
	if ty == "" {
		ty = "unknown"
	}
	return fmt.Sprintf("%s:%d:%d", ty, tmdbID, season)
}

// NormalizeMoviePilotFallbackThreshold 阈值归一：<=0 回退默认 3。
func NormalizeMoviePilotFallbackThreshold(v int) int {
	if v <= 0 {
		return DefaultMoviePilotFallbackThreshold
	}
	return v
}

// MoviePilotFallback 降级兜底状态（按「影片+季+触发来源」聚合计数，持久化供前端观测）。
type MoviePilotFallback struct {
	ID                int64
	MediaKey          string // 影片+季唯一键
	Trigger           string // 触发来源：search/subscription
	MediaType         string // movie/tv
	TmdbId            int64
	Title             string
	Season            int
	SearchCount       int // 累计无结果搜索次数
	SubscriptionCount int // 连续无进展订阅轮数
	Progress          int
	Status            string
	Action            string
	DownloadEpisodes  string // 已提交下载的集数键（逗号分隔）
	ExternalID        string // MP 侧订阅 ID / 种子 hash
	Message           string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// MoviePilot 促销状态（与 MoviePilot 侧一致）。
var PromotionStates = []string{"free", "2xfree", "normal", "half", "2xhalf"}

// PromotionStateNames 促销状态中文名（前端展示用）。
var PromotionStateNames = map[string]string{
	"free":   "免费",
	"2xfree": "2X免费",
	"normal": "普通",
	"half":   "50%",
	"2xhalf": "2X 50%",
}

// DefaultPromotionOrder 默认促销阶梯顺序（越高优先越靠前）。
const DefaultPromotionOrder = "free,2xfree,normal,half,2xhalf"

// MoviePilotConfig 全局单行配置，对应 movie_pilot_configs 表。
type MoviePilotConfig struct {
	ID                     int64
	Enabled                bool
	BaseUrl                string
	ApiToken               string
	DownloadRoot           string
	LocalViewRoot          string
	UploadAccountId        int64
	UploadRoot             string
	UploadRootId           string
	StrmLocalDir           string
	PollInterval           int // 轮询间隔（分钟），默认 5
	NotifyEnabled          bool
	CategoryConfig         string
	PromotionOrder         string // 逗号分隔，默认 free,2xfree,normal,half,2xhalf
	PromotionPatienceHours int    // 每层耐心小时数，默认 12
	SeedRetentionHours     int    // 做种保留小时数，0 表示不做种删除
	QbittorrentURL         string
	QbittorrentUser        string
	QbittorrentPass        string
	// —— 降级兜底（次数触发自动补找）配置，语义见本文件 MoviePilotFallback 注释 ——
	// MpFallbackEnabled 次数触发自动补找总开关；只控制搜索次数与订阅轮次规则，
	// 不影响独立的定时搜索下载；兜底调用失败时本地订阅继续运行。
	MpFallbackEnabled bool
	// MpFallbackSearchEnabled 资源搜索无结果时启用（只对已选定影片和季号生效，报错和取消不计数）。
	MpFallbackSearchEnabled bool
	// MpFallbackSearchThreshold 同一影片或同一季累计无结果次数达到该值后触发，默认 3。
	MpFallbackSearchThreshold int
	// MpFallbackSearchAction 搜索达到次数后的动作：subscribe/download/download_then_subscribe（默认 download）。
	MpFallbackSearchAction string
	// MpFallbackSubscriptionEnabled 本地订阅无进展时启用（仅统计正常完成的轮次，有进展重置，有待入库任务时不触发）。
	MpFallbackSubscriptionEnabled bool
	// MpFallbackSubscriptionThreshold 同一影片或同一季连续无进展轮数达到该值后触发，默认 3。
	MpFallbackSubscriptionThreshold int
	// MpFallbackSubscriptionAction 订阅达到轮数后的动作：subscribe/download/download_then_subscribe（默认 download）。
	MpFallbackSubscriptionAction string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// MoviePilotPromotionLadder 订阅的促销阶梯当前层。
// Tier 0 为最高优先层，数值越大越放宽。
type MoviePilotPromotionLadder struct {
	SubscribeID   int64
	Tier          int
	TierStartedAt int64 // unix 秒
	UpdatedAt     time.Time
}

// MoviePilotUploadTask 一次下载完成后的上传任务。
type MoviePilotUploadTask struct {
	ID            int64
	TorrentHash   string
	Title         string
	MediaType     string // movie/tv
	TmdbId        int64
	Season        string
	LocalPath     string
	RemotePath    string
	Status        string
	TotalFiles    int
	UploadedFiles int
	TotalBytes    int64
	UploadedBytes int64
	Error         string
	// EmptySourceSince 记录源目录首次为空的时间。
	// MoviePilot 的下载完成信号可能早于文件真正落盘，
	// 空目录只代表文件尚未就绪，需要轮询等待而非立即失败。
	EmptySourceSince *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// MoviePilotFailedFile 识别失败/整理失败的网盘文件，供手动确认整理。
type MoviePilotFailedFile struct {
	ID        int64
	TaskID    int64
	FileName  string
	ParentID  string // 源目录 ID（网盘语义）
	RootPath  string
	AccountID int64
	Status    string
	MediaType string
	Title     string
	TmdbId    int64
	Year      int
	Season    int
	Reason    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MoviePilotOrganizeHistory 整理历史记录。
type MoviePilotOrganizeHistory struct {
	ID         int64
	AccountID  int64
	TaskID     int64
	FileName   string
	SourcePath string
	TargetPath string
	MediaType  string
	Title      string
	Year       int
	SeasonNum  int
	EpisodeNum int
	TmdbId     int64
	Status     string
	Message    string
	CreatedAt  time.Time
}

// MoviePilotRepository MoviePilot 集成的持久化接口。
type MoviePilotRepository interface {
	// 配置：单行全局配置，首次读取时创建默认行。
	LoadConfig(ctx context.Context) (*MoviePilotConfig, error)
	SaveConfig(ctx context.Context, cfg *MoviePilotConfig) error

	// 促销阶梯
	GetPromotionLadder(ctx context.Context, subscribeID int64) (*MoviePilotPromotionLadder, error)
	SavePromotionLadder(ctx context.Context, l *MoviePilotPromotionLadder) error
	DeletePromotionLadder(ctx context.Context, subscribeID int64) error
	ListPromotionLadders(ctx context.Context) ([]MoviePilotPromotionLadder, error)

	// 上传任务
	CreateUploadTask(ctx context.Context, t *MoviePilotUploadTask) (int64, error)
	UpdateUploadTask(ctx context.Context, t *MoviePilotUploadTask) error
	GetUploadTask(ctx context.Context, id int64) (*MoviePilotUploadTask, error)
	FindUploadTaskByHash(ctx context.Context, hash string) (*MoviePilotUploadTask, error)
	FindUploadTaskByLocalPath(ctx context.Context, localPath, excludeHash string) (*MoviePilotUploadTask, error)
	ListUploadTasks(ctx context.Context, page, pageSize int, status string) ([]MoviePilotUploadTask, int64, error)
	ListUploadTasksByStatus(ctx context.Context, statuses ...string) ([]MoviePilotUploadTask, error)

	// 识别失败文件
	CreateFailedFile(ctx context.Context, f *MoviePilotFailedFile) (int64, error)
	UpdateFailedFile(ctx context.Context, f *MoviePilotFailedFile) error
	GetFailedFile(ctx context.Context, id int64) (*MoviePilotFailedFile, error)
	FindPendingFailedFile(ctx context.Context, taskID int64, fileName string) (*MoviePilotFailedFile, error)
	ListFailedFiles(ctx context.Context, page, pageSize int, status string) ([]MoviePilotFailedFile, int64, error)

	// 整理历史
	AddOrganizeHistory(ctx context.Context, h *MoviePilotOrganizeHistory) error
	ListOrganizeHistory(ctx context.Context, limit int) ([]MoviePilotOrganizeHistory, error)

	// 降级兜底：按「影片+季+触发来源」聚合的计数记录。
	GetFallback(ctx context.Context, mediaKey, trigger string) (*MoviePilotFallback, error)
	SaveFallback(ctx context.Context, rec *MoviePilotFallback) error
	ListFallbacks(ctx context.Context, page, pageSize int, status string) ([]MoviePilotFallback, int64, error)
	CountActiveFallbacks(ctx context.Context) (int64, error)
	// HasPendingTransferTasks 是否存在「待入库」任务：
	// 已提交上传但尚未完成的 MoviePilot 上传任务，或尚未结束的本地上传任务。
	HasPendingTransferTasks(ctx context.Context) (bool, error)
}
