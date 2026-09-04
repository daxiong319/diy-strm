package models

import (
	"strings"
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
	CategoryConfig string   `json:"category_config"`                 // 分类策略配置（MoviePilot category.yaml 风格，空=默认）
	// PromotionOrder 促销优先阶梯（逗号分隔，从高到低）：free=免费 2xfree=2X免费 normal=普通 half=50% 2xhalf=2X 50%。
	// 空=默认 free,2xfree,normal,half,2xhalf。订阅促销监督按此顺序逐层回退：
	// 始终优先下载最高可用促销层，该层持续无新下载则放宽到下一层，一旦下载立即回到最高层
	PromotionOrder string `json:"promotion_order" gorm:"default:free,2xfree,normal,half,2xhalf"`
	// PromotionPatienceHours 促销层耐心期（小时）：当前层持续该时长无新下载才放宽到下一层，默认 12
	PromotionPatienceHours int `json:"promotion_patience_hours" gorm:"default:12"`
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
	cfg.CategoryConfig = req.CategoryConfig
	cfg.PromotionOrder = NormalizePromotionOrder(req.PromotionOrder)
	if req.PromotionPatienceHours > 0 {
		cfg.PromotionPatienceHours = req.PromotionPatienceHours
	}
	if err := db.Db.Model(cfg).Where("id = ?", cfg.ID).Save(cfg).Error; err != nil {
		helpers.AppLogger.Errorf("更新 MoviePilot 配置失败：%v", err)
		return nil, false
	}
	return cfg, true
}

// 促销优选合法取值（顺序即默认优先级）
var PromotionStates = []string{"free", "2xfree", "normal", "half", "2xhalf"}

// PromotionStateNames 促销状态展示名
var PromotionStateNames = map[string]string{
	"free": "免费", "2xfree": "2X免费", "normal": "普通", "half": "50%", "2xhalf": "2X 50%",
}

// NormalizePromotionOrder 归一化促销优先级串：去空格、去未知值、去重；空串返回默认顺序
func NormalizePromotionOrder(order string) string {
	if strings.TrimSpace(order) == "" {
		return strings.Join(PromotionStates, ",")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(PromotionStates))
	for _, part := range strings.Split(order, ",") {
		p := strings.TrimSpace(strings.ToLower(part))
		if p == "" || seen[p] {
			continue
		}
		for _, valid := range PromotionStates {
			if p == valid {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return strings.Join(PromotionStates, ",")
	}
	return strings.Join(out, ",")
}

// PromotionOrderList 把配置串解析为状态切片
func PromotionOrderList(order string) []string {
	normalized := NormalizePromotionOrder(order)
	return strings.Split(normalized, ",")
}

// MoviePilotPromotionLadder 订阅促销优先阶梯状态（diy-strm 促销监督的游标）
type MoviePilotPromotionLadder struct {
	SubscribeID   uint  `json:"subscribe_id" gorm:"primaryKey"` // MP 订阅 ID
	Tier          int   `json:"tier"`                           // 当前允许到的层（0=最高优先层）
	TierStartedAt int64 `json:"tier_started_at"`                // 当前层开始时间（unix 秒）
	UpdatedAt     time.Time `json:"updated_at"`
}

func (*MoviePilotPromotionLadder) TableName() string { return "movie_pilot_promotion_ladders" }

// GetMoviePilotPromotionLadder 读取订阅的促销阶梯状态
func GetMoviePilotPromotionLadder(subscribeID uint) *MoviePilotPromotionLadder {
	var l MoviePilotPromotionLadder
	if err := db.Db.First(&l, "subscribe_id = ?", subscribeID).Error; err != nil {
		return nil
	}
	return &l
}

// SaveMoviePilotPromotionLadder 保存订阅的促销阶梯状态（upsert）
func SaveMoviePilotPromotionLadder(l *MoviePilotPromotionLadder) error {
	l.UpdatedAt = time.Now()
	if err := db.Db.Save(l).Error; err != nil {
		return err
	}
	return nil
}

// DeleteMoviePilotPromotionLadder 删除订阅的促销阶梯状态
func DeleteMoviePilotPromotionLadder(subscribeID uint) {
	_ = db.Db.Delete(&MoviePilotPromotionLadder{}, "subscribe_id = ?", subscribeID).Error
}

// ListMoviePilotPromotionLadders 全量促销阶梯状态
func ListMoviePilotPromotionLadders() []MoviePilotPromotionLadder {
	var out []MoviePilotPromotionLadder
	_ = db.Db.Find(&out).Error
	return out
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

// MoviePilotFailedStatus 识别失败文件处理状态
type MoviePilotFailedStatus string

const (
	MoviePilotFailedPending  MoviePilotFailedStatus = "pending"  // 待处理
	MoviePilotFailedResolved MoviePilotFailedStatus = "resolved" // 已确认并整理完成
	MoviePilotFailedSkipped  MoviePilotFailedStatus = "skipped"  // 已跳过
)

// MoviePilotFailedFile MoviePilot 上传整理时无法识别的文件（识别失败独立菜单数据）
type MoviePilotFailedFile struct {
	BaseModel
	TaskID    uint   `json:"task_id" gorm:"index"`              // 关联上传任务 ID
	FileName  string `json:"file_name"`                         // 网盘文件名
	ParentID  string `json:"parent_id"`                         // 文件所在源目录 ID（网盘语义）
	RootPath  string `json:"root_path"`                         // 文件所在源目录路径（整理根目录）
	AccountID uint   `json:"account_id"`                        // 网盘账号 ID
	Status    string `json:"status" gorm:"index;default:pending"` // 处理状态：pending/resolved/skipped
	MediaType string `json:"media_type"`                        // 确认后的媒体类型：movie/tv
	Title     string `json:"title"`                             // 确认后的标题
	TmdbId    int64  `json:"tmdb_id"`                           // 确认后的 TMDB ID
	Year      int    `json:"year"`                              // 确认后的年份
	Season    int    `json:"season"`                            // 确认后的季号（剧集）
	Reason    string `json:"reason"`                            // 失败原因
}

func (*MoviePilotFailedFile) TableName() string { return "movie_pilot_failed_files" }

// CreateMoviePilotFailedFile 创建识别失败记录
func CreateMoviePilotFailedFile(f *MoviePilotFailedFile) error {
	return db.Db.Create(f).Error
}

// UpdateMoviePilotFailedFile 更新识别失败记录
func UpdateMoviePilotFailedFile(f *MoviePilotFailedFile) error {
	return db.Db.Model(f).Where("id = ?", f.ID).Save(f).Error
}

// GetMoviePilotFailedFile 查询单条识别失败记录
func GetMoviePilotFailedFile(id uint) *MoviePilotFailedFile {
	var f MoviePilotFailedFile
	if err := db.Db.First(&f, id).Error; err != nil {
		return nil
	}
	return &f
}

// ListMoviePilotFailedFiles 分页查询识别失败记录
func ListMoviePilotFailedFiles(page, pageSize int, status string) ([]MoviePilotFailedFile, int64) {
	var files []MoviePilotFailedFile
	var total int64
	q := db.Db.Model(&MoviePilotFailedFile{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Count(&total)
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&files)
	return files, total
}