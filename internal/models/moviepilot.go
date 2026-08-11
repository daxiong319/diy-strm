package models

import (
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
)

// MoviePilotConfig MoviePilot 对接配置（单行表）
type MoviePilotConfig struct {
	ID             uint     `json:"id" gorm:"primaryKey"`
	Enabled        bool     `json:"enabled" gorm:"default:false"`    // 是否启用订阅自动下载检测
	BaseUrl        string   `json:"base_url"`                        // MoviePilot 地址，如 http://127.0.0.1:3000
	ApiToken       string   `json:"api_token"`                       // MoviePilot API Token（settings.API_TOKEN）
	DownloadRoot   string   `json:"download_root"`                   // 下载器保存根目录（MoviePilot 侧路径）
	LocalViewRoot  string   `json:"local_view_root"`                 // 下载目录在本容器/进程中的路径（用于前缀映射）
	UploadAccountId uint    `json:"upload_account_id"`               // 目标网盘账号 ID（0=禁用上传）
	UploadRoot     string   `json:"upload_root"`                     // 目标网盘上传根目录（路径）
	UploadRootId   string   `json:"upload_root_id"`                  // 目标网盘上传根目录 ID
	StrmLocalDir   string   `json:"strm_local_dir"`                  // STRM 文件本地输出目录
	PollInterval   int      `json:"poll_interval" gorm:"default:5"`  // 轮询间隔（分钟）
	NotifyEnabled  bool     `json:"notify_enabled" gorm:"default:true"` // 完成后是否发送通知
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (*MoviePilotConfig) TableName() string { return "movie_pilot_configs" }

var MoviePilotConfigGlobal = &MoviePilotConfig{}

// LoadMoviePilotConfig 加载配置（首行），不存在则创建默认行
func LoadMoviePilotConfig() *MoviePilotConfig {
	if err := db.Db.Take(MoviePilotConfigGlobal).Error; err != nil {
		cfg := &MoviePilotConfig{PollInterval: 5, NotifyEnabled: true}
		if err := db.Db.Create(cfg).Error; err != nil {
			helpers.AppLogger.Errorf("创建 MoviePilot 配置失败：%v", err)
			return MoviePilotConfigGlobal
		}
		MoviePilotConfigGlobal = cfg
	}
	return MoviePilotConfigGlobal
}

// UpdateMoviePilotConfig 更新配置并返回
func UpdateMoviePilotConfig(req *MoviePilotConfig) (*MoviePilotConfig, bool) {
	cfg := LoadMoviePilotConfig()
	cfg.Enabled = req.Enabled
	cfg.BaseUrl = req.BaseUrl
	cfg.ApiToken = req.ApiToken
	cfg.DownloadRoot = req.DownloadRoot
	cfg.LocalViewRoot = req.LocalViewRoot
	cfg.UploadAccountId = req.UploadAccountId
	cfg.UploadRoot = req.UploadRoot
	cfg.UploadRootId = req.UploadRootId
	cfg.StrmLocalDir = req.StrmLocalDir
	if req.PollInterval > 0 {
		cfg.PollInterval = req.PollInterval
	}
	cfg.NotifyEnabled = req.NotifyEnabled
	if err := db.Db.Model(cfg).Where("id = ?", cfg.ID).Save(cfg).Error; err != nil {
		helpers.AppLogger.Errorf("更新 MoviePilot 配置失败：%v", err)
		return nil, false
	}
	return cfg, true
}

// MoviePilotUploadStatus 上传任务状态
type MoviePilotUploadStatus string

const (
	MoviePilotUploadPending   MoviePilotUploadStatus = "pending"   // 等待上传
	MoviePilotUploadUploading MoviePilotUploadStatus = "uploading" // 上传中
	MoviePilotUploadUploaded  MoviePilotUploadStatus = "uploaded"  // 已完成
	MoviePilotUploadFailed    MoviePilotUploadStatus = "failed"    // 失败
	MoviePilotUploadCanceled  MoviePilotUploadStatus = "canceled"  // 已取消
)

// MoviePilotUploadTask MoviePilot 下载完成后的 139 上传任务
type MoviePilotUploadTask struct {
	ID            uint                  `json:"id" gorm:"primaryKey"`
	TorrentHash   string                `json:"torrent_hash" gorm:"index"` // 下载任务哈希，用于去重
	Title         string                `json:"title"`                     // 种子标题
	MediaType     string                `json:"media_type"`                // movie/tv
	TmdbId        int64                 `json:"tmdb_id"`
	Season        string                `json:"season"`
	LocalPath     string                `json:"local_path"`  // 上传源目录（容器内路径）
	RemotePath    string                `json:"remote_path"` // 139 目标目录
	Status        MoviePilotUploadStatus `json:"status" gorm:"index"`
	TotalFiles    int                   `json:"total_files"`    // 总文件数
	UploadedFiles int                   `json:"uploaded_files"` // 已上传文件数
	TotalBytes    int64                 `json:"total_bytes"`
	UploadedBytes int64                 `json:"uploaded_bytes"`
	Error         string                `json:"error" gorm:"type:text"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

func (*MoviePilotUploadTask) TableName() string { return "movie_pilot_upload_tasks" }

// FindMoviePilotUploadTask 按哈希查询已存在的上传任务
func FindMoviePilotUploadTask(hash string) *MoviePilotUploadTask {
	var task MoviePilotUploadTask
	if err := db.Db.Where("torrent_hash = ?", hash).First(&task).Error; err != nil {
		return nil
	}
	return &task
}

// CreateMoviePilotUploadTask 创建上传任务
func CreateMoviePilotUploadTask(task *MoviePilotUploadTask) error {
	return db.Db.Create(task).Error
}

// UpdateMoviePilotUploadTask 更新任务进度
func UpdateMoviePilotUploadTask(task *MoviePilotUploadTask) error {
	return db.Db.Model(task).Where("id = ?", task.ID).Save(task).Error
}

// GetMoviePilotUploadTask 查询单个任务
func GetMoviePilotUploadTask(id uint) *MoviePilotUploadTask {
	var task MoviePilotUploadTask
	if err := db.Db.First(&task, id).Error; err != nil {
		return nil
	}
	return &task
}

// ListMoviePilotUploadTasks 分页查询上传任务
func ListMoviePilotUploadTasks(page, pageSize int, status string) ([]MoviePilotUploadTask, int64) {
	var tasks []MoviePilotUploadTask
	var total int64
	q := db.Db.Model(&MoviePilotUploadTask{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Count(&total)
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks)
	return tasks, total
}