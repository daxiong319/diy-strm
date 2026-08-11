package moviepilot

import (
	"context"
	"fmt"
	"os"
	"path"
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

// checkCompletedDownloads 检测 MP 已完成的下载任务并创建上传任务。
// 先检查下载列表中的完成任务，再兜底检查下载历史（任务完成后即从下载列表移除）。
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
		if err := createUploadTaskFromDownload(client, cfg, t.Hash, t.Title, t.Name, localPath, mediaType, tmdbId, t.SeasonEpisode); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 为下载任务 %s 创建上传任务失败：%v", t.Name, err)
			continue
		}
	}
	// 兜底：下载历史中尚未捕获的完成任务
	return checkDownloadHistory()
}

// createUploadTaskFromDownload 创建上传任务并加入上传队列（hash 幂等）
func createUploadTaskFromDownload(client *Client, cfg *models.MoviePilotConfig, hash, title, name, localPath, mediaType string, tmdbId int64, seasonEpisode string) error {
	remotePath := strings.TrimRight(cfg.UploadRoot, "/")
	if remotePath == "" {
		remotePath = "/影视/订阅下载"
	}
	remotePath += "/" + filepath.Base(strings.TrimRight(localPath, "/"))
	if title == "" {
		title = name
	}
	task := &models.MoviePilotUploadTask{
		TorrentHash: hash,
		Title:       title,
		MediaType:   mediaType,
		TmdbId:      tmdbId,
		Season:      seasonEpisode,
		LocalPath:   localPath,
		RemotePath:  remotePath,
		Status:      models.MoviePilotUploadPending,
	}
	if err := models.CreateMoviePilotUploadTask(task); err != nil {
		return fmt.Errorf("创建上传任务失败：%v", err)
	}
	helpers.AppLogger.Infof("MoviePilot 检测到下载完成：%s → %s（139 目标 %s）", title, localPath, remotePath)
	uploadQueue <- task
	return nil
}

// 下载历史检测游标：已扫描到的最大历史 ID；attempts 记录本地路径未匹配的 hash 及上次尝试时间
var (
	historyMu       sync.Mutex
	lastHistoryID   int64
	historyAttempts = map[string]time.Time{}
)

// checkDownloadHistory 检查 MP 下载历史，为尚未捕获的完成下载创建上传任务。
// 历史 path 为 MP 侧保存路径（如 alist:/中国移动云盘/影视/待整理/日韩剧集/xxx），
// 实际文件位于本地视图根目录（LocalViewRoot）下同名目录，故取 path 最后一段递归匹配。
func checkDownloadHistory() error {
	cfg := models.LoadMoviePilotConfig()
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	histories, err := client.ListDownloadHistory(ctx, 1, 100)
	if err != nil {
		return err
	}
	if len(histories) == 0 {
		return nil
	}

	historyMu.Lock()
	cursor := lastHistoryID
	historyMu.Unlock()

	processed := 0
	for _, h := range histories {
		if h.ID <= cursor {
			continue
		}
		if h.DownloadHash == "" {
			cursor = h.ID
			continue
		}
		if models.FindMoviePilotUploadTask(h.DownloadHash) != nil {
			cursor = h.ID
			continue
		}
		// 本地路径未匹配过的任务 1 小时内不重试（文件可能尚未到位）
		historyMu.Lock()
		lastTry, tried := historyAttempts[h.DownloadHash]
		historyMu.Unlock()
		if tried && time.Since(lastTry) < time.Hour {
			cursor = h.ID
			continue
		}
		localPath := resolveHistoryLocalPath(h, cfg)
		if localPath == "" {
			historyMu.Lock()
			historyAttempts[h.DownloadHash] = time.Now()
			historyMu.Unlock()
			cursor = h.ID
			continue
		}
		mediaType := ""
		if h.Type == "电视剧" {
			mediaType = "tv"
		} else if h.Type == "电影" {
			mediaType = "movie"
		}
		if err := createUploadTaskFromDownload(client, cfg, h.DownloadHash, h.Title, h.TorrentName, localPath, mediaType, h.TmdbId, h.Seasons); err != nil {
			helpers.AppLogger.Errorf("MoviePilot 为历史下载 %s（%s）创建上传任务失败：%v", h.Title, h.DownloadHash, err)
		} else {
			processed++
		}
		cursor = h.ID
		if processed >= 20 {
			break
		}
	}
	historyMu.Lock()
	if cursor > lastHistoryID {
		lastHistoryID = cursor
	}
	historyMu.Unlock()
	if processed > 0 {
		helpers.AppLogger.Infof("MoviePilot 下载历史检测完成：新增 %d 个上传任务", processed)
	}
	return nil
}

// resolveHistoryLocalPath 从下载历史记录定位容器内可访问的本地路径。
// 取历史 path 最后一段（MP 转移后的目录/文件名），在本地视图根下递归匹配（最多 3 层）。
func resolveHistoryLocalPath(h *DownloadHistory, cfg *models.MoviePilotConfig) string {
	lastSeg := path.Base(strings.TrimRight(strings.ReplaceAll(h.Path, "\\", "/"), "/"))
	if lastSeg == "" || lastSeg == "." || lastSeg == "/" {
		return ""
	}
	root := strings.TrimRight(cfg.LocalViewRoot, "/")
	if root == "" {
		root = strings.TrimRight(cfg.DownloadRoot, "/")
	}
	if root == "" {
		return ""
	}
	var found string
	count := 0
	scanDirMatch(root, lastSeg, 0, 3, &found, &count)
	// 历史指向单个文件时，匹配到文件取所在目录
	if count == 0 {
		scanFileMatch(root, lastSeg, 0, 3, &found, &count)
	}
	if count == 1 {
		return strings.TrimRight(filepath.ToSlash(found), "/")
	}
	if count > 1 {
		helpers.AppLogger.Warnf("MoviePilot 历史 %s 本地匹配到多个路径，跳过：%s", lastSeg, h.Path)
	}
	return ""
}

// scanDirMatch 在 root 下递归（maxDepth 层内）查找与 target 同名的目录，命中即停止
func scanDirMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(root, e.Name())
		if e.Name() == target {
			*found = p
			*count++
			return
		}
		scanDirMatch(p, target, depth+1, maxDepth, found, count)
	}
}

// scanFileMatch 在 root 下递归查找与 target 同名的文件，命中取所在目录
func scanFileMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() {
			if !strings.HasPrefix(e.Name(), ".") {
				scanFileMatch(p, target, depth+1, maxDepth, found, count)
			}
			continue
		}
		if e.Name() == target {
			*found = filepath.Dir(p)
			*count++
		}
	}
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

// runUploadTask 执行上传任务：创建文件级上传任务走系统统一上传队列
func runUploadTask(task *models.MoviePilotUploadTask) {
	cfg := models.LoadMoviePilotConfig()
	var account models.Account
	if err := db.Db.First(&account, cfg.UploadAccountId).Error; err != nil {
		failUploadTask(task, fmt.Errorf("上传账号不存在（ID=%d），请在设置中配置", cfg.UploadAccountId))
		return
	}

	task.Status = models.MoviePilotUploadUploading
	task.Error = ""
	_ = models.UpdateMoviePilotUploadTask(task)

	// 统计本地文件
	if files, err := CollectLocalFiles(task.LocalPath); err == nil {
		var totalBytes int64
		for _, f := range files {
			totalBytes += f.Size
		}
		task.TotalFiles = len(files)
		task.TotalBytes = totalBytes
		_ = models.UpdateMoviePilotUploadTask(task)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	// 重试场景：清理该任务旧的批次记录
	purgeMoviePilotDbTasks(task.ID)

	baseDirID, err := EnsureRemoteDir(ctx, &account, task.RemotePath)
	if err != nil {
		failUploadTask(task, fmt.Errorf("定位网盘上传目录失败：%v", err))
		return
	}
	created, err := CreateMoviePilotUploadTasks(ctx, &account, task.ID, task.LocalPath, task.RemotePath, baseDirID)
	if err != nil {
		failUploadTask(task, fmt.Errorf("创建上传任务失败：%v", err))
		return
	}
	if created == 0 {
		failUploadTask(task, fmt.Errorf("没有可上传的文件"))
		return
	}
	helpers.AppLogger.Infof("MoviePilot 已创建上传批次：%s 共 %d 个文件 → %s", task.Title, created, task.RemotePath)
	// 异步等待批次完成，避免阻塞串行上传队列
	go waitMoviePilotBatchFinalize(task, &account, cfg)
}

// waitMoviePilotBatchFinalize 轮询批次文件任务完成，聚合进度并执行收尾（整理/STRM/通知）
func waitMoviePilotBatchFinalize(task *models.MoviePilotUploadTask, account *models.Account, cfg *models.MoviePilotConfig) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(6 * time.Hour)
	for range ticker.C {
		if moviePilotDbTasksFinished(task.ID) {
			break
		}
		totalBytes, uploadedBytes, totalFiles, uploadedFiles := moviePilotDbTaskProgress(task.ID)
		task.TotalBytes = totalBytes
		task.UploadedBytes = uploadedBytes
		task.TotalFiles = int(totalFiles)
		task.UploadedFiles = int(uploadedFiles)
		_ = models.UpdateMoviePilotUploadTask(task)
		if time.Now().After(deadline) {
			helpers.AppLogger.Errorf("MoviePilot 等待批次完成超时：%s", task.Title)
			break
		}
	}

	var failedCount int64
	db.Db.Model(&models.DbUploadTask{}).
		Where("movie_pilot_task_id = ? AND status IN ?", task.ID, []models.UploadStatus{
			models.UploadStatusFailed, models.UploadStatusCancelled,
		}).
		Count(&failedCount)
	totalBytes, uploadedBytes, totalFiles, uploadedFiles := moviePilotDbTaskProgress(task.ID)
	task.TotalBytes = totalBytes
	task.UploadedBytes = uploadedBytes
	task.TotalFiles = int(totalFiles)
	task.UploadedFiles = int(uploadedFiles)

	if failedCount > 0 {
		if int64(totalFiles) > 0 && failedCount >= int64(totalFiles) {
			failUploadTask(task, fmt.Errorf("全部文件上传失败（%d 个）", failedCount))
			return
		}
		task.Status = models.MoviePilotUploadUploaded
		task.Error = fmt.Sprintf("部分文件上传失败：%d 个", failedCount)
		_ = models.UpdateMoviePilotUploadTask(task)
		helpers.AppLogger.Warnf("MoviePilot 上传部分失败：%s：%s", task.Title, task.Error)
		notifyUploadFinished(task, false, task.Error)
	} else {
		task.Status = models.MoviePilotUploadUploaded
		task.Error = ""
		_ = models.UpdateMoviePilotUploadTask(task)
		helpers.AppLogger.Infof("MoviePilot 上传完成：%s 共 %d 个文件 → %s", task.Title, totalFiles, task.RemotePath)
		notifyUploadFinished(task, true, "")
	}

	// 网盘端整理（文件名解析）→ 整理成功目录生成 STRM
	organizeAndSyncStrm(task, account, cfg)
}

// organizeAndSyncStrm 上传完成后执行网盘端整理并按整理成功目录触发 STRM 同步
func organizeAndSyncStrm(task *models.MoviePilotUploadTask, account *models.Account, cfg *models.MoviePilotConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	rootID := strings.TrimSpace(task.RemotePath)
	if id, err := EnsureRemoteDir(ctx, account, task.RemotePath); err == nil && id != "" {
		rootID = id
	}
	result := organizeUploadedDir(ctx, account, rootID, task.RemotePath, task)
	helpers.AppLogger.Infof("MoviePilot 整理完成：%s：成功 %d 个，失败 %d 个，无法识别 %d 个", task.Title, result.Organized, result.Failed, result.Unrecognized)
	if result.Organized == 0 {
		task.Error = fmt.Sprintf("无整理成功的文件（失败 %d，无法识别 %d）", result.Failed, result.Unrecognized)
		if task.Error == "" {
			task.Error = "无整理成功的文件"
		}
		_ = models.UpdateMoviePilotUploadTask(task)
		return
	}

	if strings.TrimSpace(cfg.StrmLocalDir) == "" {
		helpers.AppLogger.Warnf("MoviePilot 未配置 STRM 本地输出目录，跳过 STRM 生成：%s", task.Title)
		return
	}
	for _, dir := range result.SuccessDirs {
		sourcePath := strings.TrimRight(task.RemotePath, "/") + "/" + dir
		TriggerStrmSyncForDir(account, sourcePath, cfg.StrmLocalDir)
	}
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
	title := "✅ 订阅下载已上传网盘"
	if !success {
		notifType = notification.SystemAlert
		title = "❌ 订阅下载上传网盘失败"
	}
	content := fmt.Sprintf("%s\n本地路径：%s\n网盘路径：%s", task.Title, task.LocalPath, task.RemotePath)
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

// TriggerStrmSyncForDir 对整理成功的网盘目录触发手动 STRM 同步（ID=0，按路径定位）
func TriggerStrmSyncForDir(account *models.Account, sourcePath, strmLocalDir string) {
	syncTask := &synccron.NewSyncTask{
		TaskType:   synccron.SyncTaskTypeStrm,
		SourcePath: sourcePath,
		AccountId:  account.ID,
		SourceType: account.SourceType,
		TargetPath: strings.TrimRight(strmLocalDir, "/"),
	}
	if err := synccron.AddNewSyncTask(syncTask); err != nil {
		helpers.AppLogger.Errorf("MoviePilot 触发 STRM 同步失败（%s）：%v", sourcePath, err)
		return
	}
	helpers.AppLogger.Infof("MoviePilot 已触发 STRM 同步：%s → %s", sourcePath, strmLocalDir)
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
	purgeMoviePilotDbTasks(task.ID)
	uploadQueue <- task
	return true
}

// purgeMoviePilotDbTasks 删除某 MoviePilot 任务的文件级上传批次记录
func purgeMoviePilotDbTasks(mpTaskId uint) {
	db.Db.Where("movie_pilot_task_id = ?", mpTaskId).Delete(&models.DbUploadTask{})
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