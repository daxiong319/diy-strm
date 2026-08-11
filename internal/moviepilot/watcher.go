package moviepilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notification"
	"diy-strm/internal/notificationmanager"
	"diy-strm/internal/synccron"
)

// 全局上传串行队列
var uploadQueue = make(chan *models.MoviePilotUploadTask, 32)
var watcherRunning atomicBool

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) Set(v bool) {
	b.mu.Lock()
	b.v = v
	b.mu.Unlock()
}

func (b *atomicBool) Get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}

// StartMoviePilotWatcher 启动后台轮询：常量运行，按配置周期检测 MP 下载完成并处理
func StartMoviePilotWatcher() {
	if watcherRunning.Get() {
		return
	}
	watcherRunning.Set(true)

	// 上传 worker：串行处理上传任务
	go func() {
		for task := range uploadQueue {
			runUploadTask(task)
		}
	}()

	go func() {
		cfg := models.LoadMoviePilotConfig()
		interval := time.Duration(cfg.PollInterval) * time.Minute
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		helpers.AppLogger.Infof("MoviePilot 订阅下载检测已启动，轮询间隔 %v", interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			current := models.LoadMoviePilotConfig()
			if !current.Enabled || strings.TrimSpace(current.BaseUrl) == "" || strings.TrimSpace(current.ApiToken) == "" {
				continue
			}
			if err := checkCompletedDownloads(); err != nil {
				helpers.AppLogger.Errorf("MoviePilot 检测下载任务失败：%v", err)
			}
		}
	}()
}

// checkCompletedDownloads 检测 MP 已完成的下载任务并创建上传任务
func checkCompletedDownloads() error {
	cfg := models.LoadMoviePilotConfig()
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	torrents, err := client.ListDownloads(ctx)
	if err != nil {
		return err
	}
	for _, t := range torrents {
		if t.Hash == "" {
			continue
		}
		// 完成判定：进度 100% 且非下载中
		if t.Progress < 100 || t.State == "downloading" {
			continue
		}
		if models.FindMoviePilotUploadTask(t.Hash) != nil {
			continue
		}
		localPath := resolveLocalPath(t, cfg)
		if localPath == "" {
			continue
		}
		remotePath := strings.TrimRight(cfg.UploadRoot, "/")
		if remotePath == "" {
			remotePath = "/影视/订阅下载"
		}
		remotePath += "/" + filepath.Base(strings.TrimRight(localPath, "/"))

		mediaType := ""
		var tmdbId int64
		if t.Media != nil {
			if v, ok := t.Media["type"].(string); ok {
				mediaType = v
			}
			if v, ok := t.Media["tmdbid"].(float64); ok {
				tmdbId = int64(v)
			}
		}
		title := t.Title
		if title == "" {
			title = t.Name
		}

		task := &models.MoviePilotUploadTask{
			TorrentHash: t.Hash,
			Title:       title,
			MediaType:   mediaType,
			TmdbId:      tmdbId,
			Season:      t.SeasonEpisode,
			LocalPath:   localPath,
			RemotePath:  remotePath,
			Status:      models.MoviePilotUploadPending,
		}
		if err := models.CreateMoviePilotUploadTask(task); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 创建上传任务失败：%v", err)
			continue
		}
		helpers.AppLogger.Infof("MoviePilot 检测到下载完成：%s → %s（139 目标 %s）", title, localPath, remotePath)
		uploadQueue <- task
	}
	return nil
}

// resolveLocalPath 把 MP 返回的保存路径映射为容器内可访问路径
func resolveLocalPath(t *DownloadTorrent, cfg *models.MoviePilotConfig) string {
	raw := t.ContentPath
	if raw == "" {
		raw = t.SavePath
	}
	mapped := mapPathPrefix(raw, cfg.DownloadRoot, cfg.LocalViewRoot)
	// 若带前缀映射后路径不存在，尝试原路径（同机部署时两个值一致）
	if !pathExists(mapped) && pathExists(raw) {
		mapped = raw
	}
	// contentPath 可能指向单个文件，此时取其所在目录
	if info, err := os.Stat(mapped); err == nil && !info.IsDir() {
		mapped = filepath.Dir(mapped)
	}
	if !pathExists(mapped) {
		helpers.AppLogger.Warnf("MoviePilot 下载路径 %s 不存在（映射后 %s），跳过", raw, mapped)
		return ""
	}
	return strings.TrimRight(filepath.ToSlash(mapped), "/")
}

// mapPathPrefix 前缀映射：mp 侧路径 → 本容器路径
func mapPathPrefix(raw, from, to string) string {
	if raw == "" {
		return raw
	}
	from = strings.TrimRight(from, "/")
	to = strings.TrimRight(to, "/")
	if from != "" && to != "" && (raw == from || strings.HasPrefix(raw, from+"/")) {
		return to + strings.TrimPrefix(raw, from)
	}
	return raw
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// runUploadTask 执行上传任务
func runUploadTask(task *models.MoviePilotUploadTask) {
	task.Status = models.MoviePilotUploadUploading
	_ = models.UpdateMoviePilotUploadTask(task)

	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceTypePan139).Order("id asc").First(&account).Error; err != nil {
		failUploadTask(task, fmt.Errorf("未配置中国移动云盘账号"))
		return
	}

	client := account.GetPan139Client()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	files, err := CollectLocalFiles(task.LocalPath)
	if err == nil {
		var totalBytes int64
		for _, f := range files {
			totalBytes += f.Size
		}
		task.TotalFiles = len(files)
		task.TotalBytes = totalBytes
		_ = models.UpdateMoviePilotUploadTask(task)
	}

	uploaded, err := UploadLocalDirToPan139(ctx, client, task.LocalPath, task.RemotePath, func(p *UploadProgress) {
		task.UploadedFiles = p.FileIndex
		task.UploadedBytes = p.UploadedBytes
		_ = models.UpdateMoviePilotUploadTask(task)
	})
	if err != nil {
		failUploadTask(task, err)
		return
	}
	task.UploadedFiles = uploaded
	task.Status = models.MoviePilotUploadUploaded
	_ = models.UpdateMoviePilotUploadTask(task)

	helpers.AppLogger.Infof("MoviePilot 上传完成：%s 共 %d 个文件 → %s", task.Title, uploaded, task.RemotePath)
	notifyUploadFinished(task, true, "")
	triggerStrmSync(task)
}

// failUploadTask 标记失败并通知
func failUploadTask(task *models.MoviePilotUploadTask, err error) {
	task.Status = models.MoviePilotUploadFailed
	task.Error = err.Error()
	_ = models.UpdateMoviePilotUploadTask(task)
	helpers.AppLogger.Errorf("MoviePilot 上传失败：%s：%v", task.Title, err)
	notifyUploadFinished(task, false, err.Error())
}

// notifyUploadFinished 发送上传完成/失败通知
func notifyUploadFinished(task *models.MoviePilotUploadTask, success bool, errMsg string) {
	cfg := models.LoadMoviePilotConfig()
	if !cfg.NotifyEnabled {
		return
	}
	notifType := notification.MediaAdded
	title := "✅ 订阅下载已上传 139"
	if !success {
		notifType = notification.SystemAlert
		title = "❌ 订阅下载上传 139 失败"
	}
	content := fmt.Sprintf("%s\n本地路径：%s\n139 路径：%s", task.Title, task.LocalPath, task.RemotePath)
	if !success {
		content += "\n错误：" + errMsg
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		_ = notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), &notification.Notification{
			Type:    notifType,
			Title:   title,
			Content: content,
		})
	}
}

// triggerStrmSync 上传完成后触发配置的 STRM 同步目录
func triggerStrmSync(task *models.MoviePilotUploadTask) {
	cfg := models.LoadMoviePilotConfig()
	if cfg.SyncPathId == 0 {
		return
	}
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceTypePan139).Order("id asc").First(&account).Error; err != nil {
		helpers.AppLogger.Errorf("MoviePilot 触发 STRM 同步失败：未找到 139 账号")
		return
	}
	syncTask := &synccron.NewSyncTask{
		ID:         cfg.SyncPathId,
		TaskType:   synccron.SyncTaskTypeStrm,
		SourceType: models.SourceTypePan139,
		AccountId:  account.ID,
	}
	if err := synccron.AddNewSyncTask(syncTask); err != nil {
		helpers.AppLogger.Errorf("MoviePilot 触发 STRM 同步失败：%v", err)
		return
	}
	helpers.AppLogger.Infof("MoviePilot 上传完成已触发 STRM 同步（同步目录 ID=%d）", cfg.SyncPathId)
}

// RetryUploadTask 重试失败/取消的上传任务
func RetryUploadTask(taskID uint) bool {
	task := models.GetMoviePilotUploadTask(taskID)
	if task == nil {
		return false
	}
	if task.Status != models.MoviePilotUploadFailed && task.Status != models.MoviePilotUploadCanceled {
		return false
	}
	task.Status = models.MoviePilotUploadPending
	task.Error = ""
	task.UploadedFiles = 0
	task.UploadedBytes = 0
	if err := models.UpdateMoviePilotUploadTask(task); err != nil {
		return false
	}
	uploadQueue <- task
	return true
}

// CancelUploadTask 取消待处理/失败的上传任务
func CancelUploadTask(taskID uint) bool {
	task := models.GetMoviePilotUploadTask(taskID)
	if task == nil {
		return false
	}
	if task.Status == models.MoviePilotUploadUploading {
		return false
	}
	task.Status = models.MoviePilotUploadCanceled
	return models.UpdateMoviePilotUploadTask(task) == nil
}