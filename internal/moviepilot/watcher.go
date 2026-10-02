package moviepilot

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"litepan/internal/domain"
)

// 轮询期内的常量。
const (
	// historyPageSize 一次拉取的下载历史条数。
	historyPageSize = 100
	// historyMaxCreate 单轮最多创建的上传任务数，避免历史积压时一次性灌爆队列。
	historyMaxCreate = 20
	// historyRetryThrottle 同一 hash 的失败重试冷却时间。
	historyRetryThrottle = time.Hour
	// resolveScanMaxDepth 本地路径回溯匹配的最大深度。
	resolveScanMaxDepth = 3
	// batchFinalizeTimeout 等待上传批次收敛的最长时长。
	batchFinalizeTimeout = 6 * time.Hour
	// batchFinalizeTick 批次收敛轮询间隔。
	batchFinalizeTick = 15 * time.Second
	// batchStableRounds 指纹连续稳定轮数，达到后方可判定批次完成。
	batchStableRounds = 2
)

// errUploadSkipped 目录已由既有任务处理且没有新增文件。
var errUploadSkipped = fmt.Errorf("upload skipped: directory already handled without new files")

// virtualMountPathPrefixes 虚拟挂载路径前缀：这些路径在本机不可见，无法生成上传任务。
var virtualMountPathPrefixes = []string{"alist:"}

// checkCompletedDownloads 检测已完成的下载并创建上传任务。
// 完成下载会从下载列表移除，因此下载列表只是快路径，历史记录才是可靠完成信号。
func (s *Service) checkCompletedDownloads(ctx context.Context, cfg *domain.MoviePilotConfig, client *Client) error {
	downloads, err := client.ListDownloads(ctx)
	if err != nil {
		return fmt.Errorf("获取下载任务失败：%w", err)
	}
	for _, t := range downloads {
		if ctx.Err() != nil {
			return nil
		}
		if t == nil || strings.TrimSpace(t.Hash) == "" {
			continue
		}
		// 完成判定：进度 100 且不在下载中（做种/完成/暂停都算完成）
		if t.Progress < 100 || t.State == "downloading" {
			continue
		}
		if existing, fErr := s.repo.FindUploadTaskByHash(ctx, t.Hash); fErr == nil && existing != nil {
			continue
		}
		localPath := resolveLocalPath(firstNonEmpty(t.ContentPath, t.SavePath), cfg.DownloadRoot, cfg.LocalViewRoot)
		if localPath == "" {
			s.log.Warn("MoviePilot 下载路径不存在，跳过", "hash", t.Hash, "content", t.ContentPath, "save", t.SavePath)
			continue
		}
		mediaType, tmdbID := mediaMetaOf(t.Media)
		if _, cErr := s.createUploadTaskFromDownload(ctx, cfg, t.Hash, firstNonEmpty(t.Title, t.Name), localPath, mediaType, tmdbID); cErr != nil && cErr != errUploadSkipped {
			s.log.Error("MoviePilot 为下载任务创建上传任务失败", "hash", t.Hash, "err", cErr)
		}
	}
	// 下载列表只覆盖仍在管理中的任务，历史记录是完成信号的权威来源
	return s.checkDownloadHistory(ctx, cfg, client)
}

// mediaMetaOf 从 MoviePilot 下载任务的 media 字段提取类型与 TMDB ID。
func mediaMetaOf(media map[string]any) (string, int64) {
	if media == nil {
		return "", 0
	}
	mediaType := ""
	if v, ok := media["type"].(string); ok {
		mediaType = strings.TrimSpace(v)
	}
	var tmdbID int64
	switch v := media["tmdbid"].(type) {
	case float64:
		tmdbID = int64(v)
	case int64:
		tmdbID = v
	case int:
		tmdbID = int64(v)
	}
	return mediaType, tmdbID
}

// createUploadTaskFromDownload 由一条已完成下载创建上传任务。
// 同一本地目录可能被多个种子写入（整季包 + 单集补种），因此按 hash 与本地路径双重去重：
// 目录已由其他任务处理且没有新增文件时跳过，有新增文件时放行增量上传。
func (s *Service) createUploadTaskFromDownload(ctx context.Context, cfg *domain.MoviePilotConfig, hash, title, localPath, mediaType string, tmdbID int64) (*domain.MoviePilotUploadTask, error) {
	if existing, err := s.repo.FindUploadTaskByHash(ctx, hash); err == nil && existing != nil {
		return existing, nil
	}
	if dup, err := s.repo.FindUploadTaskByLocalPath(ctx, localPath, hash); err == nil && dup != nil {
		if !s.localDirHasUnclaimedFiles(ctx, localPath, 0) {
			s.log.Info("MoviePilot 跳过重复上传：源目录已由既有任务处理",
				"local_path", localPath, "task_id", dup.ID, "hash", dup.TorrentHash)
			return nil, errUploadSkipped
		}
		s.log.Info("MoviePilot 目录已由既有任务处理过，但存在未上传的新文件，放行增量上传",
			"local_path", localPath, "task_id", dup.ID)
	}
	remotePath := strings.TrimRight(strings.TrimSpace(cfg.UploadRoot), "/")
	if remotePath == "" {
		remotePath = "/影视/订阅下载"
	}
	remotePath += "/" + filepath.Base(strings.TrimRight(localPath, "/"))
	if strings.TrimSpace(title) == "" {
		title = filepath.Base(strings.TrimRight(localPath, "/"))
	}
	task := &domain.MoviePilotUploadTask{
		TorrentHash: hash,
		Title:       title,
		MediaType:   mediaType,
		TmdbId:      tmdbID,
		LocalPath:   localPath,
		RemotePath:  remotePath,
		Status:      domain.MoviePilotUploadPending,
	}
	id, err := s.repo.CreateUploadTask(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("创建上传任务失败：%w", err)
	}
	task.ID = id
	s.log.Info("MoviePilot 检测到下载完成", "title", title, "local", localPath, "remote", remotePath, "task_id", id)
	go s.runUploadTask(context.WithoutCancel(ctx), id)
	return task, nil
}

// localDirHasUnclaimedFiles 判断目录内是否存在未被任何上传任务占用的文件。
// excludeTaskID 排除自身；0 表示不排除。
func (s *Service) localDirHasUnclaimedFiles(ctx context.Context, localPath string, excludeTaskID int64) bool {
	files, err := CollectLocalFiles(localPath)
	if err != nil || len(files) == 0 {
		return false
	}
	_ = files
	task, fErr := s.repo.FindUploadTaskByLocalPath(ctx, localPath, "")
	if fErr != nil || task == nil {
		return true // 目录没有任何任务占用
	}
	if task.ID == excludeTaskID {
		return false
	}
	// 目录已被其他任务占用：文件数多于已上传数即存在新增
	return task.TotalFiles == 0 || int64(len(files)) > int64(task.UploadedFiles)
}

// checkDownloadHistory 扫描下载历史，为尚未创建上传任务的完成记录创建任务。
func (s *Service) checkDownloadHistory(ctx context.Context, cfg *domain.MoviePilotConfig, client *Client) error {
	s.mu.Lock()
	base := s.lastHistoryID
	s.mu.Unlock()

	history, err := client.ListDownloadHistory(ctx, 1, historyPageSize)
	if err != nil {
		return fmt.Errorf("获取下载历史失败：%w", err)
	}
	s.mu.Lock()
	attempts := s.historyAttempts
	s.mu.Unlock()

	processed := 0
	var maxID, minCreatedID int64
	stoppedEarly := false
	for _, h := range history {
		if ctx.Err() != nil {
			stoppedEarly = true
			break
		}
		if h == nil || h.ID <= base {
			break
		}
		if h.ID > maxID {
			maxID = h.ID
		}
		if strings.TrimSpace(h.DownloadHash) == "" {
			continue
		}
		if existing, fErr := s.repo.FindUploadTaskByHash(ctx, h.DownloadHash); fErr == nil && existing != nil {
			continue
		}
		// 同一 hash 的失败重试冷却，避免坏记录每轮都刷日志
		if lastTry, tried := attempts[h.DownloadHash]; tried && s.now().Sub(lastTry) < historyRetryThrottle {
			continue
		}
		localPath := s.resolveHistoryLocalPath(h, cfg)
		if localPath == "" {
			if isVirtualMountPath(h.Path) {
				s.log.Debug("MoviePilot 下载历史为虚拟挂载路径，文件不在本地下载目录，跳过",
					"hash", h.DownloadHash, "path", h.Path)
			} else {
				s.recordHistoryAttempt(h.DownloadHash, s.now())
				s.log.Warn("MoviePilot 下载历史本地路径未匹配到，跳过创建上传任务",
					"hash", h.DownloadHash, "title", h.Title, "path", h.Path, "local_view_root", cfg.LocalViewRoot)
			}
			continue
		}
		mediaType := historyMediaType(h.Type)
		task, cErr := s.createUploadTaskFromDownload(ctx, cfg, h.DownloadHash, firstNonEmpty(h.Title, h.TorrentName), localPath, mediaType, h.TmdbId)
		if cErr == errUploadSkipped {
			// 首次跳过立即重试（目录可能只是尚未落盘），之后进入冷却
			if _, tried := attempts[h.DownloadHash]; !tried {
				s.recordHistoryAttempt(h.DownloadHash, s.now().Add(-historyRetryThrottle))
			} else {
				s.recordHistoryAttempt(h.DownloadHash, s.now())
			}
			continue
		}
		if cErr != nil {
			s.recordHistoryAttempt(h.DownloadHash, s.now())
			s.log.Warn("MoviePilot 由下载历史创建上传任务失败", "hash", h.DownloadHash, "err", cErr)
			continue
		}
		if task != nil {
			processed++
			if minCreatedID == 0 || h.ID < minCreatedID {
				minCreatedID = h.ID
			}
		}
		if processed >= historyMaxCreate {
			stoppedEarly = true
			break
		}
	}
	// 游标推进：提前中断时退回到本轮最早创建处，避免跳过中间未扫描的记录
	cursorTarget := maxID
	if stoppedEarly && minCreatedID > 0 && minCreatedID < maxID {
		cursorTarget = minCreatedID
	}
	if cursorTarget > 0 {
		s.mu.Lock()
		if cursorTarget > s.lastHistoryID {
			s.lastHistoryID = cursorTarget
		}
		s.mu.Unlock()
	}
	if processed > 0 {
		s.log.Info("MoviePilot 下载历史检测完成", "新增上传任务", processed)
	}
	return nil
}

// recordHistoryAttempt 记录某 hash 的重试时间。
func (s *Service) recordHistoryAttempt(hash string, at time.Time) {
	s.mu.Lock()
	if s.historyAttempts == nil {
		s.historyAttempts = make(map[string]time.Time)
	}
	s.historyAttempts[hash] = at
	s.mu.Unlock()
}

// historyMediaType 把 MoviePilot 的中文类型名转为内部类型。
func historyMediaType(t string) string {
	switch strings.TrimSpace(t) {
	case "电视剧":
		return "tv"
	case "电影":
		return "movie"
	}
	return ""
}

// isVirtualMountPath 判断路径是否为虚拟挂载（本机不可见）。
func isVirtualMountPath(p string) bool {
	trimmed := strings.TrimSpace(p)
	for _, prefix := range virtualMountPathPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// resolveHistoryLocalPath 由下载历史的远端路径回溯本地目录。
// 只取末段名在本地视图根下做有界宽度扫描；命中多个则放弃（歧义）。
func (s *Service) resolveHistoryLocalPath(h *DownloadHistory, cfg *domain.MoviePilotConfig) string {
	lastSeg := path.Base(strings.TrimRight(strings.ReplaceAll(h.Path, "\\", "/"), "/"))
	if lastSeg == "" || lastSeg == "." || lastSeg == "/" {
		return ""
	}
	root := strings.TrimRight(strings.TrimSpace(cfg.LocalViewRoot), "/")
	if root == "" {
		root = strings.TrimRight(strings.TrimSpace(cfg.DownloadRoot), "/")
	}
	if root == "" || !pathExists(root) {
		return ""
	}
	var found string
	count := 0
	scanDirMatch(root, lastSeg, 0, resolveScanMaxDepth, &found, &count)
	if count == 0 {
		scanFileMatch(root, lastSeg, 0, resolveScanMaxDepth, &found, &count)
	}
	if count == 1 {
		return strings.TrimRight(filepath.ToSlash(found), "/")
	}
	if count > 1 {
		s.log.Warn("MoviePilot 历史本地匹配到多个路径，跳过", "hash", h.DownloadHash, "name", lastSeg)
	}
	return ""
}

// scanDirMatch 在根目录下按名称递归查找目录。
func scanDirMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth || *count > 1 {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if *count > 1 {
			return
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		current := filepath.Join(root, e.Name())
		if e.Name() == target {
			*found = current
			*count++
			return
		}
		scanDirMatch(current, target, depth+1, maxDepth, found, count)
	}
}

// scanFileMatch 在根目录下按文件名递归查找，命中时记录其所在目录。
func scanFileMatch(root, target string, depth, maxDepth int, found *string, count *int) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		current := filepath.Join(root, e.Name())
		if e.IsDir() {
			scanFileMatch(current, target, depth+1, maxDepth, found, count)
			continue
		}
		if e.Name() == target {
			*found = filepath.Dir(current)
			*count++
		}
	}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// recoverPendingTasks 启动恢复：把遗留的待上传任务重新排队。
func (s *Service) recoverPendingTasks(ctx context.Context) {
	tasks, err := s.repo.ListUploadTasksByStatus(ctx, domain.MoviePilotUploadPending, domain.MoviePilotUploadUploading)
	if err != nil {
		s.log.Error("MoviePilot 启动恢复读取上传任务失败", "err", err)
		return
	}
	if len(tasks) == 0 {
		return
	}
	s.log.Info("MoviePilot 启动恢复上传任务", "count", len(tasks))
	for i := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.enqueueUploadTask(tasks[i].ID)
	}
}

// enqueueUploadTask 把任务投入串行上传队列（幂等）。
func (s *Service) enqueueUploadTask(taskID int64) bool {
	if taskID <= 0 {
		return false
	}
	s.mu.Lock()
	if s.queued == nil {
		s.queued = make(map[int64]struct{})
	}
	if _, ok := s.queued[taskID]; ok {
		s.mu.Unlock()
		return true
	}
	if s.processingID == taskID {
		s.mu.Unlock()
		return true
	}
	s.queued[taskID] = struct{}{}
	s.mu.Unlock()

	select {
	case s.uploadQueue <- taskID:
		return true
	default:
		// 队列已满：撤回占位，由自愈扫描与重启恢复兜底
		s.mu.Lock()
		delete(s.queued, taskID)
		s.mu.Unlock()
		s.log.Warn("MoviePilot 上传队列已满，任务落库待人工重试", "task_id", taskID)
		return false
	}
}

// uploadWorker 串行消费上传队列。
func (s *Service) uploadWorker(ctx context.Context, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case taskID := <-s.uploadQueue:
			s.mu.Lock()
			delete(s.queued, taskID)
			s.processingID = taskID
			s.mu.Unlock()

			s.runUploadTask(ctx, taskID)

			s.mu.Lock()
			s.processingID = 0
			s.mu.Unlock()
		}
	}
}

// runUploadTask 执行单个上传任务：整理 → 交棒上传 → 等待收敛 → 触发 STRM。
func (s *Service) runUploadTask(ctx context.Context, taskID int64) {
	task, err := s.repo.GetUploadTask(ctx, taskID)
	if err != nil || task == nil {
		s.log.Warn("MoviePilot 上传任务不存在", "task_id", taskID)
		return
	}
	// 用户可能在排队期间取消：只有 pending/uploading 才执行
	if task.Status != domain.MoviePilotUploadPending && task.Status != domain.MoviePilotUploadUploading {
		s.log.Info("MoviePilot 上传任务状态已变化，跳过执行", "task_id", taskID, "status", task.Status)
		return
	}
	cfg, err := s.repo.LoadConfig(ctx)
	if err != nil {
		s.failUploadTask(ctx, task, fmt.Errorf("读取配置失败：%w", err))
		return
	}
	if cfg.UploadAccountId <= 0 {
		s.failUploadTask(ctx, task, fmt.Errorf("未配置上传账号（ID=%d），请在设置中配置", cfg.UploadAccountId))
		return
	}
	accountName, driverType := s.resolveAccount(ctx, cfg.UploadAccountId)
	if strings.TrimSpace(accountName) == "" {
		s.failUploadTask(ctx, task, fmt.Errorf("上传账号不存在（ID=%d），请在设置中配置", cfg.UploadAccountId))
		return
	}

	task.Status = domain.MoviePilotUploadUploading
	task.Error = ""
	if err := s.repo.UpdateUploadTask(ctx, task); err != nil {
		s.log.Warn("MoviePilot 更新上传任务状态失败", "task_id", taskID, "err", err)
	}

	files, err := CollectLocalFiles(task.LocalPath)
	if err != nil {
		s.failUploadTask(ctx, task, fmt.Errorf("读取源目录失败：%w", err))
		return
	}
	if len(files) == 0 {
		s.handleEmptySource(ctx, task)
		return
	}
	task.TotalFiles = len(files)
	task.TotalBytes = 0
	for _, f := range files {
		task.TotalBytes += f.Size
	}
	_ = s.repo.UpdateUploadTask(ctx, task)

	// 整理：逐文件识别并规划目标路径
	client := NewClient(cfg.BaseUrl, cfg.ApiToken)
	plan := s.PlanOrganize(ctx, client, task.LocalPath)
	for _, f := range plan.Failed {
		_ = s.RecordFailedFile(ctx, task.ID, f.SourcePath, f.Reason, nil)
	}
	if len(plan.Targets) == 0 {
		s.failUploadTask(ctx, task, fmt.Errorf("没有可整理上传的视频文件（无法识别 %d 个）", len(plan.Failed)))
		return
	}

	// 本地整理：移动到整理目录并按规范重命名
	relFiles, err := s.applyOrganizePlan(ctx, cfg, task, plan)
	if err != nil {
		s.failUploadTask(ctx, task, err)
		return
	}
	if len(relFiles) == 0 {
		s.failUploadTask(ctx, task, fmt.Errorf("整理后没有可上传的文件：%s", task.LocalPath))
		return
	}

	// 交棒既有上传链路
	opts := UploadHandoffOptions{
		AccountID:          cfg.UploadAccountId,
		AccountName:        accountName,
		DriverType:         driverType,
		TargetParentID:     strings.TrimSpace(cfg.UploadRootId),
		TargetDisplayPath:  strings.TrimRight(strings.TrimSpace(cfg.UploadRoot), "/"),
		ClientTaskIDPrefix: fmt.Sprintf("moviepilot-%d", task.ID),
	}
	created, err := s.HandoffLocalTreeToUpload(ctx, opts, relFiles)
	if err != nil {
		if isErrEmptySourceWait(err) {
			s.handleEmptySource(ctx, task)
			return
		}
		s.failUploadTask(ctx, task, fmt.Errorf("创建上传任务失败：%w", err))
		return
	}
	if created == 0 {
		s.failUploadTask(ctx, task, fmt.Errorf("源目录存在文件但未能创建任何文件上传任务：%s", task.LocalPath))
		return
	}
	task.Error = ""
	task.EmptySourceSince = nil
	_ = s.repo.UpdateUploadTask(ctx, task)
	s.log.Info("MoviePilot 已创建上传批次", "task_id", task.ID, "files", created, "remote", task.RemotePath)

	s.waitBatchFinalize(ctx, task, cfg)
}

// resolveAccount 解析上传账号名称与驱动类型；解析失败返回空名。
func (s *Service) resolveAccount(ctx context.Context, accountID int64) (string, string) {
	if s.accounts == nil {
		return "", ""
	}
	name, driver, err := s.accounts.LookupUploadAccount(ctx, accountID)
	if err != nil {
		s.log.Warn("MoviePilot 解析上传账号失败", "account_id", accountID, "err", err)
		return "", ""
	}
	return name, driver
}

// applyOrganizePlan 按规划把本地文件移动到整理目录并重命名（含洗版判定）。
// 返回整理后（相对整理根）的上传文件列表。
func (s *Service) applyOrganizePlan(ctx context.Context, cfg *domain.MoviePilotConfig, task *domain.MoviePilotUploadTask, plan *OrganizePlan) ([]LocalFile, error) {
	organizeRoot := OrganizeRootPath(cfg.UploadRoot)
	var out []LocalFile
	for _, target := range plan.Targets {
		if ctx.Err() != nil {
			break
		}
		destDir := filepath.Join(organizeRoot, filepath.FromSlash(target.RelDir))
		// 洗版判定：目标目录内同集/同名旧文件的质量对比
		oldFiles, _ := CollectLocalFiles(destDir)
		if len(oldFiles) > 0 {
			size := int64(0)
			if info, err := os.Stat(target.SourcePath); err == nil {
				size = info.Size()
			}
			decision := DecideWash(target.Quality, target.NewName, size, oldFiles, DefaultWashRules)
			if !decision.Proceed {
				s.log.Info("MoviePilot 洗版跳过", "file", target.SourcePath, "reason", decision.Reason)
				_ = s.repo.AddOrganizeHistory(ctx, &domain.MoviePilotOrganizeHistory{
					AccountID:  cfg.UploadAccountId,
					TaskID:     task.ID,
					FileName:   filepath.Base(target.SourcePath),
					SourcePath: target.SourcePath,
					MediaType:  target.Media.Category,
					Title:      target.Media.Title,
					Year:       target.Media.Year,
					SeasonNum:  target.Media.Season,
					EpisodeNum: target.Media.Episode,
					TmdbId:     target.Media.TmdbId,
					Status:     "skipped",
					Message:    decision.Reason,
				})
				continue
			}
		}
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			s.log.Error("MoviePilot 创建整理目录失败", "dir", destDir, "err", err)
			continue
		}
		destPath := filepath.Join(destDir, target.NewName)
		if err := moveLocalFile(target.SourcePath, destPath); err != nil {
			s.log.Error("MoviePilot 移动文件失败", "src", target.SourcePath, "dst", destPath, "err", err)
			_ = s.repo.AddOrganizeHistory(ctx, &domain.MoviePilotOrganizeHistory{
				TaskID:     task.ID,
				FileName:   filepath.Base(target.SourcePath),
				SourcePath: target.SourcePath,
				TargetPath: destPath,
				MediaType:  target.Media.Category,
				Title:      target.Media.Title,
				Status:     "failed",
				Message:    err.Error(),
			})
			continue
		}
		rel := filepath.ToSlash(filepath.Join(target.RelDir, target.NewName))
		size := int64(0)
		if info, err := os.Stat(destPath); err == nil {
			size = info.Size()
		}
		out = append(out, LocalFile{AbsPath: destPath, RelPath: rel, Size: size})
		_ = s.repo.AddOrganizeHistory(ctx, &domain.MoviePilotOrganizeHistory{
			AccountID:  cfg.UploadAccountId,
			TaskID:     task.ID,
			FileName:   target.NewName,
			SourcePath: target.SourcePath,
			TargetPath: rel,
			MediaType:  target.Media.Category,
			Title:      target.Media.Title,
			Year:       target.Media.Year,
			SeasonNum:  target.Media.Season,
			EpisodeNum: target.Media.Episode,
			TmdbId:     target.Media.TmdbId,
			Status:     "success",
			Message:    "整理成功",
		})
	}
	return out, nil
}

// moveLocalFile 移动文件；跨设备时回退为复制 + 删除。
func moveLocalFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 目标已存在时先移除旧版本（更优才覆盖已在调用方判定）
	if pathExists(dst) {
		if err := os.Remove(dst); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err == nil {
			return nil
		}
	}
	return copyThenRemove(src, dst)
}

// copyThenRemove 复制文件后删除源文件（跨设备回退路径）。
func copyThenRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// handleEmptySource 处理源目录暂无可上传文件：等待落盘，超时后失败。
func (s *Service) handleEmptySource(ctx context.Context, task *domain.MoviePilotUploadTask) {
	now := s.now()
	if task.EmptySourceSince == nil {
		task.EmptySourceSince = &now
	}
	waited := now.Sub(*task.EmptySourceSince)
	if waited >= emptySourceWaitLimit {
		task.EmptySourceSince = nil
		s.failUploadTask(ctx, task, fmt.Errorf("源目录长时间无文件（已等待 %v）：%s", emptySourceWaitLimit, task.LocalPath))
		return
	}
	task.Status = domain.MoviePilotUploadPending
	task.Error = fmt.Sprintf("源目录暂无文件，等待落盘后自动重试（已等待 %v）", waited.Round(time.Second))
	if err := s.repo.UpdateUploadTask(ctx, task); err != nil {
		s.log.Warn("MoviePilot 更新等待状态失败", "task_id", task.ID, "err", err)
	}
	s.log.Warn("MoviePilot 源目录暂无文件，等待落盘后自动重试", "task_id", task.ID, "local", task.LocalPath, "waited", waited.Round(time.Second))
}

// waitBatchFinalize 等待上传批次收敛：文件集稳定且全部处于终态。
func (s *Service) waitBatchFinalize(ctx context.Context, task *domain.MoviePilotUploadTask, cfg *domain.MoviePilotConfig) {
	if s.uploadTasks == nil {
		// 未注入上传任务查询能力：直接标记完成（上传链路自身会继续推进）
		s.finishUploadTask(ctx, task, cfg)
		return
	}
	deadline := s.now().Add(batchFinalizeTimeout)
	ticker := time.NewTicker(batchFinalizeTick)
	defer ticker.Stop()
	lastFP := ""
	stable := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		finished, failed, uploaded, total := s.uploadTasks.BatchProgress(task.ID)
		task.TotalFiles = total
		task.UploadedFiles = uploaded
		_ = s.repo.UpdateUploadTask(ctx, task)
		fp, _ := localDirFingerprint(task.LocalPath)
		if fp == lastFP {
			stable++
		} else {
			stable = 0
			lastFP = fp
		}
		if finished && stable >= batchStableRounds {
			if failed > 0 {
				task.Status = domain.MoviePilotUploadFailed
				task.Error = fmt.Sprintf("部分文件上传失败：%d 个（成功 %d / 共 %d），已传文件照常整理", failed, uploaded, total)
				_ = s.repo.UpdateUploadTask(ctx, task)
				s.log.Warn("MoviePilot 上传部分失败", "task_id", task.ID, "failed", failed, "uploaded", uploaded, "total", total)
				return
			}
			s.finishUploadTask(ctx, task, cfg)
			return
		}
		if s.now().After(deadline) {
			s.failUploadTask(ctx, task, fmt.Errorf("批次收敛超时，仍有 %d 个文件任务未完成（共 %d 个）", total-uploaded, total))
			return
		}
	}
}

// finishUploadTask 上传完成后的收尾：标记状态并触发 STRM。
func (s *Service) finishUploadTask(ctx context.Context, task *domain.MoviePilotUploadTask, cfg *domain.MoviePilotConfig) {
	task.Status = domain.MoviePilotUploadUploaded
	task.Error = ""
	if err := s.repo.UpdateUploadTask(ctx, task); err != nil {
		s.log.Warn("MoviePilot 更新上传任务完成状态失败", "task_id", task.ID, "err", err)
	}
	s.log.Info("MoviePilot 上传完成", "task_id", task.ID, "files", task.TotalFiles, "remote", task.RemotePath)

	strmDir := strings.TrimSpace(cfg.StrmLocalDir)
	if strmDir == "" {
		s.log.Warn("MoviePilot 未配置 STRM 本地输出目录，跳过 STRM 生成", "path", task.RemotePath)
		return
	}
	if s.strm == nil {
		s.log.Warn("MoviePilot STRM 生成能力未装配，跳过", "path", task.RemotePath)
		return
	}
	organizedRoot := OrganizeRootPath(cfg.UploadRoot)
	sourcePath := strings.TrimRight(strings.TrimSpace(cfg.UploadRoot), "/")
	if sourcePath != "" {
		sourcePath = path.Join(sourcePath, path.Base(organizedRoot))
	}
	if err := s.strm.TriggerStrmForDir(ctx, cfg.UploadAccountId, sourcePath, strmDir); err != nil {
		s.log.Warn("影视订阅触发 STRM 同步失败", "source", sourcePath, "err", err)
		return
	}
	s.log.Info("[影视订阅] 上传整理完成，已触发 STRM 同步", "source", sourcePath, "target", strmDir)
}

// failUploadTask 标记上传任务失败并发送通知。
func (s *Service) failUploadTask(ctx context.Context, task *domain.MoviePilotUploadTask, err error) {
	task.Status = domain.MoviePilotUploadFailed
	task.Error = err.Error()
	if uErr := s.repo.UpdateUploadTask(ctx, task); uErr != nil {
		s.log.Warn("MoviePilot 更新上传任务失败状态出错", "task_id", task.ID, "err", uErr)
	}
	s.log.Error("MoviePilot 上传失败", "task_id", task.ID, "title", task.Title, "err", err)
	s.notifyUploadFinished(ctx, task, false, err.Error())
}

// notifyUploadFinished 发送上传完成通知（受 NotifyEnabled 控制）。
func (s *Service) notifyUploadFinished(ctx context.Context, task *domain.MoviePilotUploadTask, success bool, errMsg string) {
	if s.notifier == nil {
		return
	}
	title := "✅ 订阅下载已上传网盘"
	content := fmt.Sprintf("%s\n本地路径：%s\n网盘路径：%s", task.Title, task.LocalPath, task.RemotePath)
	if !success {
		title = "❌ 订阅下载上传网盘失败"
		content += "\n错误：" + errMsg
	}
	if err := s.notifier.NotifyMediaAdded(ctx, title, content); err != nil {
		s.log.Warn("MoviePilot 发送上传通知失败", "task_id", task.ID, "err", err)
	}
}

// RetryUploadTask 重试上传任务：重置状态并重新入队。
func (s *Service) RetryUploadTask(ctx context.Context, taskID int64) (bool, error) {
	task, err := s.repo.GetUploadTask(ctx, taskID)
	if err != nil || task == nil {
		return false, domain.Errorf(domain.CodeNotFound, "上传任务不存在")
	}
	s.mu.Lock()
	processing := s.processingID == taskID
	s.mu.Unlock()
	if processing {
		return false, domain.Errorf(domain.CodeValidation, "任务正在上传中，请稍后再试")
	}
	if task.Status == domain.MoviePilotUploadPending {
		return s.enqueueUploadTask(taskID), nil
	}
	task.Status = domain.MoviePilotUploadPending
	task.Error = ""
	task.UploadedFiles = 0
	task.UploadedBytes = 0
	if err := s.repo.UpdateUploadTask(ctx, task); err != nil {
		return false, domain.Wrap(domain.CodeInternal, err)
	}
	return s.enqueueUploadTask(taskID), nil
}

// CancelUploadTask 取消上传任务（上传中的任务不可取消）。
func (s *Service) CancelUploadTask(ctx context.Context, taskID int64) error {
	task, err := s.repo.GetUploadTask(ctx, taskID)
	if err != nil || task == nil {
		return domain.Errorf(domain.CodeNotFound, "上传任务不存在")
	}
	if task.Status == domain.MoviePilotUploadUploading {
		return domain.Errorf(domain.CodeValidation, "任务正在上传中，无法取消")
	}
	task.Status = domain.MoviePilotUploadCanceled
	if err := s.repo.UpdateUploadTask(ctx, task); err != nil {
		return domain.Wrap(domain.CodeInternal, err)
	}
	return nil
}

// UploadTaskStillRunning 判断任务是否正在本进程内执行（供前端轮询展示）。
func (s *Service) UploadTaskStillRunning(taskID int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processingID == taskID
}
