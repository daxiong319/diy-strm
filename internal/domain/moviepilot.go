package domain

import (
	"context"
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
	ID                    int64
	Enabled               bool
	BaseUrl               string
	ApiToken              string
	DownloadRoot          string
	LocalViewRoot         string
	UploadAccountId       int64
	UploadRoot            string
	UploadRootId          string
	StrmLocalDir          string
	PollInterval          int // 轮询间隔（分钟），默认 5
	NotifyEnabled         bool
	CategoryConfig        string
	PromotionOrder        string // 逗号分隔，默认 free,2xfree,normal,half,2xhalf
	PromotionPatienceHours int   // 每层耐心小时数，默认 12
	SeedRetentionHours    int    // 做种保留小时数，0 表示不做种删除
	QbittorrentURL        string
	QbittorrentUser       string
	QbittorrentPass       string
	CreatedAt             time.Time
	UpdatedAt             time.Time
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
	ID              int64
	TorrentHash     string
	Title           string
	MediaType       string // movie/tv
	TmdbId          int64
	Season          string
	LocalPath       string
	RemotePath      string
	Status          string
	TotalFiles      int
	UploadedFiles   int
	TotalBytes      int64
	UploadedBytes   int64
	Error           string
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
}
