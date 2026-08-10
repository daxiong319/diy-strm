package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/pan123"
	"diy-strm/internal/realtime"
	"diy-strm/internal/v115open"

	"gorm.io/gorm"
)

// UploadStatus 上传状态
type UploadStatus int

const (
	UploadStatusPending                        UploadStatus = iota // 等待中
	UploadStatusUploading                                          // 上传中
	UploadStatusCompleted                                          // 已完成
	UploadStatusFailed                                             // 失败
	UploadStatusCancelled                                          // 已取消
	UploadStatusRemoteCompletedPendingFinalize UploadStatus = 5    // 远端已完成，等待本地收尾
	UploadStatusRemoteCompletedFinalizing      UploadStatus = 6    // 远端已完成，正在本地收尾
	UploadStatusAll                                         = -1   // 所有状态
)

type UploadSource string

const (
	UploadSourceStrm             UploadSource = "strm_sync"
	UploadSourceScrape           UploadSource = "scrape_organize"
	UploadSourceDirectoryMonitor UploadSource = "directory_monitor"
	UploadSourceCrossTransfer    UploadSource = "cross_transfer"
)

// UploadResult 是上传任务的最终结果类型。
type UploadResult string

const (
	UploadResultUnknown               UploadResult = "unknown"
	UploadResultRapidUpload           UploadResult = "rapid_upload"
	UploadResultMultipartUploaded     UploadResult = "multipart_uploaded"
	UploadResultRemoteExists          UploadResult = "remote_exists"
	UploadResultSkippedAfterRapidWait UploadResult = "skipped_after_rapid_wait"
)

// UploadSourceCleanupStatus 是目录监控上传后的源文件清理状态。
type UploadSourceCleanupStatus string

const (
	UploadSourceCleanupStatusNone      UploadSourceCleanupStatus = "none"
	UploadSourceCleanupStatusPending   UploadSourceCleanupStatus = "pending"
	UploadSourceCleanupStatusCompleted UploadSourceCleanupStatus = "completed"
	UploadSourceCleanupStatusFailed    UploadSourceCleanupStatus = "failed"
)

const (
	uploadProgressBroadcastInterval        = time.Second
	uploadProgressBroadcastThrottleTTL     = 30 * time.Minute
	uploadProgressBroadcastCleanupInterval = 5 * time.Minute
)

type DbUploadTask struct {
	BaseModel
	Source                UploadSource              `json:"source"` // 上传来源存储值，展示文案由前端映射
	AccountId             uint                      `json:"account_id"`
	SyncFileId            uint                      `json:"sync_file_id"`                                      // 同步文件 ID
	ScrapeMediaFileId     uint                      `json:"scrape_media_file_id"`                              // 刮削文件 ID
	SyncPathId            uint                      `json:"sync_path_id" gorm:"index"`                         // 同步目录 ID
	SourceType            SourceType                `json:"source_type"`                                       // 任务来源类型
	SourceAccountId       uint                      `json:"source_account_id" gorm:"default:0;index"`          // 跨盘秒传源账号 ID
	SourceFileId          string                    `json:"source_file_id" gorm:"size:512"`                    // 跨盘秒传源文件 ID（115 pickcode/123 文件 ID/百度 fs_id/OpenList 路径）
	LocalFullPath         string                    `json:"local_full_path" gorm:"index:idx_local_full_path"`  // 本地完整文件路径，包含文件名
	RelativePath          string                    `json:"relative_path" gorm:"type:text;size:1024"`          // 目录监控源文件相对路径
	SourceFingerprint     string                    `json:"source_fingerprint" gorm:"size:128;index"`          // 目录监控源文件签名
	RemoteFileId          string                    `json:"remote_file_id" gorm:"index:idx_remote_file_id"`    // 远程文件 ID，包含完整路径
	RemotePathId          string                    `json:"remote_path_id"`                                    // 父目录 CID，如果是 115 则是文件夹 ID，如果是 OpenList 则是父文件夹路径
	FileName              string                    `json:"file_name"`                                         // 要上传的文件名
	Status                UploadStatus              `json:"status" gorm:"index:idx_status_new"`                // 任务状态
	FileSize              int64                     `json:"file_size"`                                         // 文件大小
	LocalMtime            int64                     `json:"local_mtime" gorm:"default:0"`                      // 本地源文件修改时间，用于源文件清理前校验
	LocalMtimeNs          int64                     `json:"local_mtime_ns" gorm:"default:0"`                   // 本地源文件纳秒级修改时间
	UploadedBytes         int64                     `json:"uploaded_bytes" gorm:"default:0"`                   // 已上传字节数
	UploadResult          UploadResult              `json:"upload_result" gorm:"size:32;default:unknown"`      // 上传结果
	ResumeState           UploadResumeState         `json:"resume_state" gorm:"size:32;default:none"`          // 断点续传状态
	RapidWaitAttempts     int                       `json:"rapid_wait_attempts" gorm:"default:0"`              // 秒传等待已尝试次数
	RapidWaitUntil        int64                     `json:"rapid_wait_until" gorm:"default:0"`                 // 秒传等待截止时间
	CompletedRemoteFileId string                    `json:"completed_remote_file_id" gorm:"size:128"`          // 完成后的远端文件 ID
	CompletedPickCode     string                    `json:"completed_pick_code" gorm:"size:128"`               // 完成后的 pickcode
	Error                 string                    `json:"error"`                                             // 错误信息
	StartTime             int64                     `json:"start_time"`                                        // 开始时间
	EndTime               int64                     `json:"end_time"`                                          // 结束时间
	RetryCount            int                       `json:"retry_count" gorm:"default:0"`                      // 已重试次数
	LastRetryTime         int64                     `json:"last_retry_time" gorm:"default:0"`                  // 最近重试时间
	SourceCleanupStatus   UploadSourceCleanupStatus `json:"source_cleanup_status" gorm:"size:32;default:none"` // 源文件清理状态
	SourceCleanupError    string                    `json:"source_cleanup_error" gorm:"type:text"`             // 源文件清理错误
	SourceDeletedAt       int64                     `json:"source_deleted_at" gorm:"default:0"`                // 源文件删除时间
	IsSeasonOrTvshowFile  bool                      `json:"is_season_or_tvshow_file"`                          // 是否是剧集或电视剧文件
	SyncFile              *SyncFile                 `json:"-" gorm:"-"`                                        // 同步文件
	ScrapeMediaFile       *ScrapeMediaFile          `json:"-" gorm:"-"`                                        // 刮削文件
	Account               *Account                  `json:"-" gorm:"-"`                                        // 账户
	UploadPhase           string                    `json:"upload_phase" gorm:"-"`                             // 上传阶段，仅用于队列展示
	UploadSpeedBytes      int64                     `json:"upload_speed_bytes" gorm:"-"`                       // 上传速度，仅用于队列展示
	ProgressPercent       float64                   `json:"progress_percent" gorm:"-"`                         // 上传进度，仅用于队列展示
	TotalParts            int                       `json:"total_parts" gorm:"-"`                              // 总分片数，仅用于队列展示
	UploadedParts         int                       `json:"uploaded_parts" gorm:"-"`                           // 已上传分片数，仅用于队列展示
}

// String 返回状态的字符串表示
func (s UploadStatus) String() string {
	switch s {
	case UploadStatusPending:
		return "pending"
	case UploadStatusUploading:
		return "uploading"
	case UploadStatusCompleted:
		return "completed"
	case UploadStatusFailed:
		return "failed"
	case UploadStatusCancelled:
		return "cancelled"
	case UploadStatusRemoteCompletedPendingFinalize:
		return "remote_completed_pending_finalize"
	case UploadStatusRemoteCompletedFinalizing:
		return "remote_completed_finalizing"
	default:
		return "unknown"
	}
}

func activeUploadTaskStatuses() []UploadStatus {
	return []UploadStatus{
		UploadStatusPending,
		UploadStatusUploading,
		UploadStatusRemoteCompletedPendingFinalize,
		UploadStatusRemoteCompletedFinalizing,
	}
}

// CanRetry 判断上传任务是否还能重试
func (task *DbUploadTask) CanRetry(maxRetry int) bool {
	return task != nil && task.Status == UploadStatusFailed && task.RetryCount < maxRetry
}

var uploadQueueProgressBroadcast = struct {
	sync.Mutex
	lastAt      map[uint]time.Time
	lastCleanup time.Time
}{
	lastAt: make(map[uint]time.Time),
}

func publishUploadQueueChanged(task *DbUploadTask, reason string) {
	payload := realtime.QueueChangedPayload{Reason: reason}
	if task != nil {
		if reason == "progress" && shouldThrottleUploadProgressBroadcast(task.ID) {
			return
		}
		task.applyUploadQueueDisplayFields(nil)
		payload.TaskID = task.ID
		payload.Status = int(task.Status)
		payload.Source = string(task.Source)
		payload.UploadedBytes = task.UploadedBytes
		payload.FileSize = task.FileSize
		payload.ProgressPercent = task.ProgressPercent
		payload.UploadSpeedBytes = task.UploadSpeedBytes
		payload.UploadPhase = task.UploadPhase
		payload.UploadResult = string(task.UploadResult)
		payload.ResumeState = string(task.ResumeState)
		payload.RapidWaitUntil = task.RapidWaitUntil
		payload.TotalParts = task.TotalParts
		payload.UploadedParts = task.UploadedParts
		if task.Source == UploadSourceDirectoryMonitor {
			payload.SourceCleanupStatus = string(task.SourceCleanupStatus)
			cleanupError := task.SourceCleanupError
			payload.SourceCleanupError = &cleanupError
		}
	}
	if reason == "progress" {
		realtime.TryBroadcastEvent(realtime.EventUploadQueueChanged, payload)
		return
	}
	realtime.BroadcastQueueChanged(realtime.EventUploadQueueChanged, payload)
}

func shouldThrottleUploadProgressBroadcast(taskID uint) bool {
	if taskID == 0 {
		return false
	}
	now := time.Now()
	uploadQueueProgressBroadcast.Lock()
	defer uploadQueueProgressBroadcast.Unlock()
	pruneUploadProgressThrottleLocked(now)
	lastAt := uploadQueueProgressBroadcast.lastAt[taskID]
	if !lastAt.IsZero() && now.Sub(lastAt) < uploadProgressBroadcastInterval {
		return true
	}
	uploadQueueProgressBroadcast.lastAt[taskID] = now
	return false
}

func pruneUploadProgressThrottleLocked(now time.Time) {
	if !uploadQueueProgressBroadcast.lastCleanup.IsZero() &&
		now.Sub(uploadQueueProgressBroadcast.lastCleanup) < uploadProgressBroadcastCleanupInterval {
		return
	}
	cutoff := now.Add(-uploadProgressBroadcastThrottleTTL)
	for taskID, lastAt := range uploadQueueProgressBroadcast.lastAt {
		if lastAt.Before(cutoff) {
			delete(uploadQueueProgressBroadcast.lastAt, taskID)
		}
	}
	uploadQueueProgressBroadcast.lastCleanup = now
}

func clearUploadProgressThrottle(taskID uint) {
	if taskID == 0 {
		return
	}
	uploadQueueProgressBroadcast.Lock()
	delete(uploadQueueProgressBroadcast.lastAt, taskID)
	uploadQueueProgressBroadcast.Unlock()
}

func clearAllUploadProgressThrottle() {
	uploadQueueProgressBroadcast.Lock()
	uploadQueueProgressBroadcast.lastAt = make(map[uint]time.Time)
	uploadQueueProgressBroadcast.lastCleanup = time.Time{}
	uploadQueueProgressBroadcast.Unlock()
}

// PrepareUploadRetry 将上传失败任务重新放回等待中
func (task *DbUploadTask) PrepareUploadRetry(maxRetry int) {
	if !task.CanRetry(maxRetry) {
		return
	}
	task.Status = UploadStatusPending
	task.Error = ""
	task.RetryCount++
	task.LastRetryTime = time.Now().Unix()
}

func (task *DbUploadTask) Complete() {
	if err := task.complete(); err != nil {
		helpers.AppLogger.Warnf("[上传] 标记为已完成失败：%s", err.Error())
	}
}

func (task *DbUploadTask) complete() error {
	// 标记为已完成
	task.Status = UploadStatusCompleted
	task.EndTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		return err
	}
	clearUploadProgressThrottle(task.ID)
	publishUploadQueueChanged(task, "status_changed")
	return nil
}

func (task *DbUploadTask) MarkRemoteCompletedPendingFinalize() error {
	if task == nil {
		return errors.New("上传任务为空")
	}
	task.Status = UploadStatusRemoteCompletedPendingFinalize
	task.EndTime = time.Now().Unix()
	if err := db.Db.Save(task).Error; err != nil {
		helpers.AppLogger.Warnf("[上传] 持久化远端完成待收尾状态失败：%s", err.Error())
		return err
	}
	publishUploadQueueChanged(task, "status_changed")
	return nil
}

func (task *DbUploadTask) claimRemoteCompletedFinalize() (bool, error) {
	if task == nil {
		return false, errors.New("上传任务为空")
	}
	result := db.Db.Model(&DbUploadTask{}).
		Where("id = ? AND status = ?", task.ID, UploadStatusRemoteCompletedPendingFinalize).
		Updates(map[string]interface{}{
			"status": UploadStatusRemoteCompletedFinalizing,
			"error":  "",
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	task.Status = UploadStatusRemoteCompletedFinalizing
	task.Error = ""
	publishUploadQueueChanged(task, "status_changed")
	return true, nil
}

func (task *DbUploadTask) revertRemoteCompletedFinalizing(err error) error {
	if task == nil {
		return errors.New("上传任务为空")
	}
	updateData := map[string]interface{}{
		"status": UploadStatusRemoteCompletedPendingFinalize,
	}
	if err != nil {
		updateData["error"] = err.Error()
	}
	result := db.Db.Model(&DbUploadTask{}).
		Where("id = ? AND status = ?", task.ID, UploadStatusRemoteCompletedFinalizing).
		Updates(updateData)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	task.Status = UploadStatusRemoteCompletedPendingFinalize
	if err != nil {
		task.Error = err.Error()
	}
	publishUploadQueueChanged(task, "status_changed")
	return nil
}

func (task *DbUploadTask) Fail(err error) {
	// 标记为失败
	task.Status = UploadStatusFailed
	task.EndTime = time.Now().Unix()
	task.Error = err.Error()
	err = db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[上传] 标记为失败失败：%s", err.Error())
		return
	}
	clearUploadProgressThrottle(task.ID)
	publishUploadQueueChanged(task, "status_changed")
}

func (task *DbUploadTask) Cancel() {
	// 标记为已取消
	task.Status = UploadStatusCancelled
	task.EndTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[上传] 标记为已取消失败：%s", err.Error())
		return
	}
	clearUploadProgressThrottle(task.ID)
	publishUploadQueueChanged(task, "status_changed")
}

func (task *DbUploadTask) cancelWithError(err error) {
	if err == nil {
		task.Cancel()
		return
	}
	task.Status = UploadStatusCancelled
	task.EndTime = time.Now().Unix()
	task.Error = err.Error()
	if saveErr := db.Db.Save(task).Error; saveErr != nil {
		helpers.AppLogger.Warnf("[上传] 标记为已取消失败：%s", saveErr.Error())
		return
	}
	clearUploadProgressThrottle(task.ID)
	publishUploadQueueChanged(task, "status_changed")
}

func (task *DbUploadTask) Uploading() {
	task.Status = UploadStatusUploading
	task.StartTime = time.Now().Unix()
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Warnf("[上传] 标记为上传中失败：%s", err.Error())
		return
	}
	publishUploadQueueChanged(task, "status_changed")
}

func (task *DbUploadTask) GetAccount() *Account {
	if task.Account != nil {
		return task.Account
	}
	// 通过 AccountId 查询账户，然后判断是什么来源
	account, err := GetAccountById(task.AccountId)
	if err != nil {
		task.Fail(err)
		return nil
	}
	task.Account = account
	return account
}

// 执行上传
func (task *DbUploadTask) Upload() {
	if task.Status == UploadStatusRemoteCompletedPendingFinalize {
		if err := task.finalizeRemoteCompletedUploadWithClaim(); err != nil {
			helpers.AppLogger.Warnf("[上传] 远端完成任务收尾失败：task_id=%d err=%v", task.ID, err)
		}
		return
	}
	// 跨盘秒传兜底：先把源网盘文件下载到本地临时路径
	if task.Source == UploadSourceCrossTransfer {
		if !helpers.PathExists(task.LocalFullPath) {
			if err := task.downloadCrossTransferSource(); err != nil {
				task.Fail(err)
				return
			}
		}
	}
	if !helpers.PathExists(task.LocalFullPath) {
		task.Fail(fmt.Errorf("本地文件 %s 不存在", task.LocalFullPath))
		return
	}
	if err := task.validateDirectoryMonitorSourceFingerprint(); err != nil {
		task.cancelWithError(err)
		return
	}
	// 本地文件指纹秒传：先对本地文件计算指纹并尝试目标网盘秒传，命中则跳过普通上传；
	// 秒传尝试失败时回退普通上传，不阻塞上传流程
	if task.tryLocalFingerprintRapid() {
		if err := task.MarkRemoteCompletedPendingFinalize(); err != nil {
			return
		}
		if err := task.finalizeRemoteCompletedUploadWithClaim(); err != nil {
			helpers.AppLogger.Warnf("[上传] 秒传成功后收尾失败：task_id=%d err=%v", task.ID, err)
		}
		return
	}
	switch task.SourceType {
	case SourceType115:
		if !task.Upload115File() {
			return
		}
	case SourceTypeOpenList:
		if !task.UploadOpenListFile() {
			return
		}
	case SourceTypeLocal:
		if !task.UploadLocalFile() {
			return
		}
	case SourceTypeBaiduPan:
		if !task.UploadBaiduPanFile() {
			return
		}
	case SourceType123:
		if !task.Upload123File() {
			return
		}
	case SourceTypeGuangYaPan:
		if !task.UploadGuangYaPanFile() {
			return
		}
	case SourceTypePan139:
		if !task.UploadPan139File() {
			return
		}
	default:
		task.Fail(fmt.Errorf("未知的上传来源类型 %s", task.SourceType))
		return
	}
	if err := task.MarkRemoteCompletedPendingFinalize(); err != nil {
		return
	}
	if err := task.finalizeRemoteCompletedUploadWithClaim(); err != nil {
		helpers.AppLogger.Warnf("[上传] 远端完成任务收尾失败：task_id=%d err=%v", task.ID, err)
	}
}

func (task *DbUploadTask) finalizeRemoteCompletedUploadWithClaim() error {
	claimed, err := task.claimRemoteCompletedFinalize()
	if err != nil {
		return fmt.Errorf("抢占待收尾任务失败：%w", err)
	}
	if !claimed {
		return nil
	}
	if err := task.finalizeRemoteCompletedUpload(); err != nil {
		if revertErr := task.revertRemoteCompletedFinalizing(err); revertErr != nil {
			return errors.Join(err, fmt.Errorf("恢复待收尾状态失败：%w", revertErr))
		}
		return err
	}
	return nil
}

func (task *DbUploadTask) finalizeRemoteCompletedUpload() error {
	if err := task.markDirectoryUploadProcessedAfterUploadComplete(); err != nil {
		return fmt.Errorf("标记目录监控源文件上传完成失败：%w", err)
	}
	if err := task.enqueueStrmGenerationAfterUploadAndMarkDirectoryProcessed(); err != nil {
		return fmt.Errorf("创建 STRM 生成任务失败：%w", err)
	}
	// 跨盘秒传兜底：清理下载的临时文件
	if task.Source == UploadSourceCrossTransfer {
		if helpers.PathExists(task.LocalFullPath) {
			if err := os.Remove(task.LocalFullPath); err != nil {
				helpers.AppLogger.Warnf("[上传] 清理跨盘秒传临时文件失败：%s，%v", task.LocalFullPath, err)
			}
		}
	}
	// 如果是刮削类型，需要进行后续通知
	if task.Source == UploadSourceScrape {
		// 通知刮削整理完成
		scrapeMediaFile := GetScrapeMediaFileById(task.ScrapeMediaFileId)
		if scrapeMediaFile == nil {
			helpers.AppLogger.Errorf("刮削文件 %d 不存在", task.ScrapeMediaFileId)
		} else {
			scrapeMediaFile.RemoveTmpFiles(task)
		}
	}
	if err := task.complete(); err != nil {
		return fmt.Errorf("标记上传任务完成失败：%w", err)
	}
	return nil
}

func (task *DbUploadTask) validateDirectoryMonitorSourceFingerprint() error {
	if task == nil || task.Source != UploadSourceDirectoryMonitor {
		return nil
	}
	if strings.TrimSpace(task.SourceFingerprint) == "" {
		return errors.New("目录监控上传任务缺少源文件 fingerprint")
	}
	info, err := os.Stat(task.LocalFullPath)
	if err != nil {
		return fmt.Errorf("读取目录监控源文件信息失败：%w", err)
	}
	current := BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	if current != task.SourceFingerprint {
		return fmt.Errorf("目录监控源文件 fingerprint 不匹配：task=%s current=%s", task.SourceFingerprint, current)
	}
	if err := task.validateDirectoryMonitorRealPathBoundary(); err != nil {
		return err
	}
	return nil
}

func (task *DbUploadTask) validateDirectoryMonitorRealPathBoundary() error {
	rule, err := task.findDirectoryMonitorRuleForValidation()
	if err != nil {
		return err
	}
	if rule == nil {
		return nil
	}
	if err := ensureLocalPathWithinRoot(rule.MonitorPath, task.LocalFullPath); err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(rule.MonitorPath)
	if err != nil {
		return fmt.Errorf("解析目录监控真实路径失败：%w", err)
	}
	realTarget, err := filepath.EvalSymlinks(task.LocalFullPath)
	if err != nil {
		return fmt.Errorf("解析目录监控源文件真实路径失败：%w", err)
	}
	if err := ensureLocalPathWithinRoot(realRoot, realTarget); err != nil {
		return fmt.Errorf("目录监控源文件真实路径越界：%s", realTarget)
	}
	return nil
}

func (task *DbUploadTask) findDirectoryMonitorRuleForValidation() (*DirectoryUploadRule, error) {
	var record DirectoryUploadProcessedFile
	if err := db.Db.Where("upload_task_id = ?", task.ID).First(&record).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	} else if record.RuleId > 0 {
		rule, err := GetDirectoryUploadRuleById(record.RuleId)
		if err != nil {
			return nil, fmt.Errorf("读取目录监控规则失败：%w", err)
		}
		return rule, nil
	}

	if task.SyncPathId == 0 || task.AccountId == 0 {
		return nil, nil
	}
	if !db.Db.Migrator().HasTable(&DirectoryUploadRule{}) {
		return nil, nil
	}
	var rules []*DirectoryUploadRule
	if err := db.Db.Where("sync_path_id = ? AND account_id = ?", task.SyncPathId, task.AccountId).Find(&rules).Error; err != nil {
		return nil, err
	}
	var selected *DirectoryUploadRule
	selectedLen := -1
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		if err := ensureLocalPathWithinRoot(rule.MonitorPath, task.LocalFullPath); err != nil {
			continue
		}
		cleanLen := len(filepath.Clean(rule.MonitorPath))
		if cleanLen > selectedLen {
			selected = rule
			selectedLen = cleanLen
		}
	}
	return selected, nil
}

func ensureLocalPathWithinRoot(rootPath string, targetPath string) error {
	rootPath = filepath.Clean(rootPath)
	targetPath = filepath.Clean(targetPath)
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return fmt.Errorf("计算目录监控路径边界失败：%w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("目录监控源文件路径越界：%s", targetPath)
	}
	return nil
}

// tryLocalFingerprintRapid 本地文件指纹秒传：对本地文件计算指纹并尝试目标网盘秒传，命中返回 true。
// 未命中或调用失败（网络/授权问题）返回 false，由调用方回退普通上传。
func (task *DbUploadTask) tryLocalFingerprintRapid() bool {
	fileInfo, err := os.Stat(task.LocalFullPath)
	if err != nil {
		return false
	}
	account := task.GetAccount()
	if account == nil {
		return false
	}
	switch task.SourceType {
	case SourceType115:
		client := account.Get115Client()
		if client == nil {
			return false
		}
		fileSHA1, err := helpers.CalculateFileSHA1(task.LocalFullPath)
		if err != nil {
			return false
		}
		reuse, fileID, err := client.RapidUploadBySHA1(context.Background(), task.RemoteFileId, task.FileName, fileInfo.Size(), fileSHA1)
		if err != nil {
			helpers.AppLogger.Warnf("[上传] 115 秒传尝试失败，回退普通上传：%v", err)
			return false
		}
		if !reuse {
			return false
		}
		task.Uploading()
		task.UploadResult = UploadResultRapidUpload
		task.CompletedRemoteFileId = fileID
		task.CompletedPickCode = fileID
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
		task.applyUploadQueueDisplayFields(nil)
		return true
	case SourceType123:
		client := account.Get123Client()
		if client == nil {
			return false
		}
		fileMD5, err := helpers.CalculateFileMD5(task.LocalFullPath)
		if err != nil {
			return false
		}
		reuse, fileID, err := client.RapidUploadByHash(context.Background(), task.RemoteFileId, task.FileName, fileInfo.Size(), fileMD5, 2)
		if err != nil {
			helpers.AppLogger.Warnf("[上传] 123 秒传尝试失败，回退普通上传：%v", err)
			return false
		}
		if !reuse {
			return false
		}
		task.Uploading()
		task.UploadResult = UploadResultRapidUpload
		task.CompletedRemoteFileId = fileID
		task.CompletedPickCode = fileID
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
		task.applyUploadQueueDisplayFields(nil)
		return true
	case SourceTypeBaiduPan:
		if fileInfo.Size() > 32*1024*1024 {
			return false
		}
		client := account.GetBaiDuPanClient()
		if client == nil {
			return false
		}
		fileMD5, err := helpers.CalculateFileMD5(task.LocalFullPath)
		if err != nil {
			return false
		}
		remotePath := strings.TrimRight(strings.TrimSpace(task.RemoteFileId), "/") + "/" + task.FileName
		reuse, fileID, err := client.RapidUploadByMD5(context.Background(), remotePath, fileInfo.Size(), fileMD5)
		if err != nil {
			helpers.AppLogger.Warnf("[上传] 百度网盘秒传尝试失败，回退普通上传：%v", err)
			return false
		}
		if !reuse {
			return false
		}
		task.Uploading()
		task.UploadResult = UploadResultRapidUpload
		task.CompletedRemoteFileId = fileID
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
		task.applyUploadQueueDisplayFields(nil)
		return true
	case SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return false
		}
		fileSHA256, err := helpers.CalculateFileSHA256(task.LocalFullPath)
		if err != nil {
			return false
		}
		file, err := os.Open(task.LocalFullPath)
		if err != nil {
			return false
		}
		defer file.Close()
		fileID, _, rapid, err := client.UploadFile(context.Background(), task.RemoteFileId, task.FileName, fileInfo.Size(), fileSHA256, file, nil)
		if err != nil {
			helpers.AppLogger.Warnf("[上传] 中国移动云盘秒传尝试失败，回退普通上传：%v", err)
			return false
		}
		if !rapid {
			return false
		}
		task.Uploading()
		task.UploadResult = UploadResultRapidUpload
		task.CompletedRemoteFileId = fileID
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
		task.applyUploadQueueDisplayFields(nil)
		return true
	}
	return false
}

func (task *DbUploadTask) Upload115File() bool {
	// 检查账户是否存在
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	// 上传文件
	client := account.Get115Client()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s 115 客户端不存在", account.Name))
		return false
	}
	task.Uploading()
	result, err := currentUpload115Runner.Upload(context.Background(), task, client)
	if err != nil {
		task.Fail(fmt.Errorf("调用 115 上传 API 失败：%v", err))
		return false
	}
	task.applyUpload115TaskResult(result)
	if task.Source == UploadSourceStrm {
		if result.CompletedMtime > 0 {
			mtime := time.Unix(result.CompletedMtime, 0)
			if err = os.Chtimes(task.LocalFullPath, mtime, mtime); err != nil {
				task.Fail(fmt.Errorf("更新本地文件 %s 修改时间失败：%v", task.LocalFullPath, err))
				return false
			}
		}
	}
	return true
}

// 百度网盘上传文件
func (task *DbUploadTask) UploadBaiduPanFile() bool {
	// 检查账户是否存在
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	// 上传文件
	client := account.GetBaiDuPanClient()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s 百度网盘客户端不存在", account.Name))
		return false
	}
	task.Uploading()
	// 调用上传方法
	resp, err := client.Upload(context.Background(), task.LocalFullPath, task.RemoteFileId)
	if err != nil {
		task.Fail(fmt.Errorf("百度网盘上传文件 %s 失败：%v", task.FileName, err))
		return false
	}
	if task.Source == UploadSourceStrm {
		t := time.Unix(int64(*resp.Mtime), 0)
		// 更新本地文件的修改时间
		err = os.Chtimes(task.LocalFullPath, t, t)
		if err != nil {
			task.Fail(fmt.Errorf("更新本地文件 %s 修改时间失败：%v", task.LocalFullPath, err))
			return false
		}
	}
	return true
}

// Upload123File 123 云盘上传文件
// 上传完成后记录远端文件 ID（123 的 PickCode 即文件 ID），供 STRM 生成任务使用
func (task *DbUploadTask) Upload123File() bool {
	// 检查账户是否存在
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	// 上传文件
	client := account.Get123Client()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s 123 云盘客户端不存在", account.Name))
		return false
	}
	task.Uploading()
	// 上传文件，回调中更新上传进度
	resp, err := client.UploadFile(context.Background(), task.LocalFullPath, task.RemoteFileId, func(done, total int64) {
		if total > 0 {
			task.FileSize = total
		}
		task.UploadedBytes = done
		if err := db.Db.Model(task).Update("uploaded_bytes", done).Error; err != nil {
			helpers.AppLogger.Warnf("[上传] 保存 123 云盘上传进度失败：%s", err.Error())
		}
		publishUploadQueueChanged(task, "progress")
	})
	if err != nil {
		task.Fail(fmt.Errorf("123 云盘上传文件 %s 失败：%v", task.FileName, err))
		return false
	}
	// 记录上传结果
	fileId := fmt.Sprintf("%d", resp.Data.FileId)
	task.CompletedRemoteFileId = fileId
	task.CompletedPickCode = fileId // 123 云盘的 PickCode 即文件 ID
	if resp.Data.Reuse {
		task.UploadResult = UploadResultRapidUpload
	} else {
		task.UploadResult = UploadResultMultipartUploaded
	}
	if fileInfo, statErr := os.Stat(task.LocalFullPath); statErr == nil {
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
	}
	task.applyUploadQueueDisplayFields(nil)
	publishUploadQueueChanged(task, "progress")
	return true
}

// UploadGuangYaPanFile 光鸭云盘上传文件（内部自动尝试秒传，未命中回退 OSS 分片上传）。
func (task *DbUploadTask) UploadGuangYaPanFile() bool {
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	client := account.GetGuangYaPanClient()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s 光鸭云盘客户端不存在", account.Name))
		return false
	}
	task.Uploading()
	fileID, rapid, err := client.UploadFile(context.Background(), task.LocalFullPath, task.RemoteFileId, func(done, total int64) {
		if total > 0 {
			task.FileSize = total
		}
		task.UploadedBytes = done
		if err := db.Db.Model(task).Update("uploaded_bytes", done).Error; err != nil {
			helpers.AppLogger.Warnf("[上传] 保存光鸭云盘上传进度失败：%s", err.Error())
		}
		publishUploadQueueChanged(task, "progress")
	})
	if err != nil {
		task.Fail(fmt.Errorf("光鸭云盘上传文件 %s 失败：%v", task.FileName, err))
		return false
	}
	task.CompletedRemoteFileId = fileID
	if rapid {
		task.UploadResult = UploadResultRapidUpload
	} else {
		task.UploadResult = UploadResultMultipartUploaded
	}
	if fileInfo, statErr := os.Stat(task.LocalFullPath); statErr == nil {
		task.FileSize = fileInfo.Size()
		task.UploadedBytes = fileInfo.Size()
	}
	task.applyUploadQueueDisplayFields(nil)
	publishUploadQueueChanged(task, "progress")
	return true
}

// UploadPan139File 中国移动云盘上传文件（内部自动尝试秒传，未命中回退分片上传）。
func (task *DbUploadTask) UploadPan139File() bool {
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	client := account.GetPan139Client()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s 中国移动云盘客户端不存在", account.Name))
		return false
	}
	fileInfo, err := os.Stat(task.LocalFullPath)
	if err != nil {
		task.Fail(fmt.Errorf("读取本地文件 %s 失败：%v", task.LocalFullPath, err))
		return false
	}
	sha256, err := helpers.CalculateFileSHA256(task.LocalFullPath)
	if err != nil {
		task.Fail(fmt.Errorf("计算文件 SHA256 失败：%v", err))
		return false
	}
	file, err := os.Open(task.LocalFullPath)
	if err != nil {
		task.Fail(fmt.Errorf("打开本地文件 %s 失败：%v", task.LocalFullPath, err))
		return false
	}
	defer file.Close()
	task.Uploading()
	fileID, _, rapid, err := client.UploadFile(context.Background(), task.RemoteFileId, task.FileName, fileInfo.Size(), sha256, file, func(done int64) {
		task.UploadedBytes = done
		if err := db.Db.Model(task).Update("uploaded_bytes", done).Error; err != nil {
			helpers.AppLogger.Warnf("[上传] 保存中国移动云盘上传进度失败：%s", err.Error())
		}
		publishUploadQueueChanged(task, "progress")
	})
	if err != nil {
		task.Fail(fmt.Errorf("中国移动云盘上传文件 %s 失败：%v", task.FileName, err))
		return false
	}
	task.CompletedRemoteFileId = fileID
	if rapid {
		task.UploadResult = UploadResultRapidUpload
	} else {
		task.UploadResult = UploadResultMultipartUploaded
	}
	task.FileSize = fileInfo.Size()
	task.UploadedBytes = fileInfo.Size()
	task.applyUploadQueueDisplayFields(nil)
	publishUploadQueueChanged(task, "progress")
	return true
}

func (task *DbUploadTask) UploadOpenListFile() bool {
	// 检查账户是否存在
	account := task.GetAccount()
	if account == nil {
		task.Fail(fmt.Errorf("账户 %d 不存在", task.AccountId))
		return false
	}
	// 上传文件
	client := account.GetOpenListClient()
	if client == nil {
		task.Fail(fmt.Errorf("账户 %s OpenList 客户端不存在", account.Name))
		return false
	}
	task.Uploading()
	_, err := client.Upload(task.LocalFullPath, task.RemoteFileId)
	if err != nil {
		task.Fail(fmt.Errorf("OpenList 上传文件 %s 失败：%v", task.FileName, err))
		return false
	}
	if task.Source == UploadSourceStrm {
		// 查询文件详情
		detail, err := client.FileDetail(task.RemoteFileId)
		if err != nil {
			task.Fail(fmt.Errorf("OpenList 查询文件详情 %s 失败：%s", task.RemoteFileId, err.Error()))
			return false
		}
		// 将 ISO 8601 格式的日期字符串转换为时间戳
		t, err := time.Parse(time.RFC3339, detail.Modified)
		if err != nil {
			helpers.AppLogger.Warnf("解析时间格式失败：%v, 时间字符串：%s", err, detail.Modified)
			return true
		}
		// 更新本地文件的修改时间
		err = os.Chtimes(task.LocalFullPath, t, t)
		if err != nil {
			task.Fail(fmt.Errorf("更新本地文件 %s 修改时间失败：%v", task.LocalFullPath, err))
			return false
		}
	}
	return true
}

func (task *DbUploadTask) UploadLocalFile() bool {
	task.Uploading()
	err := helpers.CopyFile(task.LocalFullPath, task.RemoteFileId)
	if err != nil {
		task.Fail(fmt.Errorf("本地文件 %s 复制到 %s 失败：%v", task.LocalFullPath, task.RemoteFileId, err))
		return false
	}
	return true
}

func CheckCanUploadByLocalPath(source UploadSource, localPath string) bool {
	var total int64
	err := db.Db.Model(&DbUploadTask{}).
		Where("source = ? AND local_full_path = ? AND status IN ?", source, localPath, activeUploadTaskStatuses()).
		Count(&total).Error
	if err != nil {
		helpers.AppLogger.Warnf("检查本地路径上传任务失败：source=%s, local_path=%s, err=%v", source, localPath, err)
		return true
	}
	return total == 0
}

// 检查任务是否已经存在，通过 Source + RemoteFileId
func CheckUploadTaskExist(source UploadSource, remoteFileId string) *DbUploadTask {
	var task *DbUploadTask
	err := db.Db.Model(&DbUploadTask{}).
		Where("source = ? AND remote_file_id = ?", source, remoteFileId).
		First(&task).Error
	if err != nil {
		return nil
	}
	return task
}

// AddDirectoryMonitorUploadTask 添加目录监控产生的上传任务。
func AddDirectoryMonitorUploadTask(task *DbUploadTask) error {
	if err := SaveDirectoryMonitorUploadTaskWithDB(db.Db, task); err != nil {
		return err
	}
	PublishUploadTaskCreated(task)
	return nil
}

// SaveDirectoryMonitorUploadTaskWithDB 在指定事务中保存目录监控上传任务。
func SaveDirectoryMonitorUploadTaskWithDB(tx *gorm.DB, task *DbUploadTask) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	if task == nil {
		return errors.New("上传任务为空")
	}
	task.Source = UploadSourceDirectoryMonitor
	if task.Status == 0 {
		task.Status = UploadStatusPending
	}
	if task.UploadResult == "" {
		task.UploadResult = UploadResultUnknown
	}
	if task.SourceCleanupStatus == "" {
		task.SourceCleanupStatus = UploadSourceCleanupStatusNone
	}
	return tx.Save(task).Error
}

// PublishUploadTaskCreated 发布上传任务创建事件。
func PublishUploadTaskCreated(task *DbUploadTask) {
	publishUploadQueueChanged(task, "created")
}

// PublishUploadTaskChanged 发布上传任务局部变更事件。
func PublishUploadTaskChanged(task *DbUploadTask, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "updated"
	}
	publishUploadQueueChanged(task, reason)
}

// 添加 STRM 同步产生的上传任务
func AddUploadTaskFromSyncFile(file *SyncFile) error {
	// 先检查是否存在
	if task := CheckUploadTaskExist(UploadSourceStrm, file.FileId); task != nil {
		if task.Status == UploadStatusPending {
			return errors.New("任务已存在，状态为待上传")
		}
		if task.Status == UploadStatusUploading {
			return errors.New("任务已存在，状态为上传中")
		}
		if task.Status == UploadStatusRemoteCompletedPendingFinalize {
			return errors.New("任务已存在，状态为远端已完成待收尾")
		}
		if task.Status == UploadStatusRemoteCompletedFinalizing {
			return errors.New("任务已存在，状态为远端已完成收尾中")
		}
	}
	// if file.SyncPath == nil {
	// 	file.SyncPath = GetSyncPathById(file.SyncPathId)
	// }
	remoteFileId := file.FileId
	// if file.SourceType == SourceType115 {
	// 	remoteFileId = filepath.Join(file.Path, file.FileName)
	// }
	// 插入新纪录
	task := &DbUploadTask{
		AccountId:     file.AccountId,
		SourceType:    file.SourceType,
		SyncFileId:    file.ID,
		SyncPathId:    file.SyncPathId,
		RemoteFileId:  remoteFileId,
		FileName:      file.FileName,
		RemotePathId:  file.ParentId,
		LocalFullPath: file.LocalFilePath,
		Source:        UploadSourceStrm,
		Status:        UploadStatusPending,
		FileSize:      file.FileSize,
	}
	err := db.Db.Save(task).Error
	if err != nil {
		helpers.AppLogger.Errorf("添加上传任务 %s => %s 失败：%s", file.LocalFilePath, remoteFileId, err.Error())
		return err
	}
	helpers.AppLogger.Infof("添加上传任务 %s => %s 成功", file.LocalFilePath, remoteFileId)
	publishUploadQueueChanged(task, "created")
	return nil
}

// 添加刮削整理产生的上传任务
func AddUploadTaskFromMediaFile(mediaFile *ScrapeMediaFile, scrapePath *ScrapePath, fileName, localFullPath, remoteFileId, remotePathId string, isSeasonOrTvshowFile bool) error {
	stat, err := os.Stat(localFullPath)
	if err != nil {
		helpers.AppLogger.Errorf("要上传的文件 %s 无法获取到文件信息，错误：%vs", localFullPath, err.Error())
		return err
	}
	size := stat.Size()
	// 先检查是否存在
	if task := CheckUploadTaskExist(UploadSourceScrape, remoteFileId); task != nil {
		if task.Status == UploadStatusPending {
			return errors.New("任务已存在，状态为待上传")
		}
		if task.Status == UploadStatusUploading {
			return errors.New("任务已存在，状态为上传中")
		}
		if task.Status == UploadStatusRemoteCompletedPendingFinalize {
			return errors.New("任务已存在，状态为远端已完成待收尾")
		}
		if task.Status == UploadStatusRemoteCompletedFinalizing {
			return errors.New("任务已存在，状态为远端已完成收尾中")
		}
	}
	// 插入新纪录
	task := &DbUploadTask{
		AccountId:            scrapePath.AccountId,
		ScrapeMediaFileId:    mediaFile.ID,
		SourceType:           scrapePath.SourceType,
		RemoteFileId:         remoteFileId,
		FileName:             fileName,
		RemotePathId:         remotePathId,
		LocalFullPath:        localFullPath,
		Source:               UploadSourceScrape,
		Status:               UploadStatusPending,
		FileSize:             size,
		IsSeasonOrTvshowFile: isSeasonOrTvshowFile,
	}
	derr := db.Db.Save(task).Error
	if derr == nil {
		publishUploadQueueChanged(task, "created")
	}
	return derr
}

func GetPendingUploadTasks(limit int) []*DbUploadTask {
	var tasks []*DbUploadTask
	db.Db.Model(&DbUploadTask{}).
		Where("status IN ?", []UploadStatus{UploadStatusPending, UploadStatusRemoteCompletedPendingFinalize}).
		Limit(limit).
		Order("id ASC").
		Find(&tasks)
	return tasks
}

func GetUploadingCount() int64 {
	var count int64
	db.Db.Model(&DbUploadTask{}).
		Where("status IN ?", []UploadStatus{UploadStatusUploading, UploadStatusRemoteCompletedFinalizing}).
		Count(&count)
	return count
}

// 查询上传队列任务列表
func GetUploadTaskList(status UploadStatus, page, pageSize int) ([]*DbUploadTask, int64) {
	var tasks []*DbUploadTask
	var total int64
	tx := db.Db.Model(&DbUploadTask{})
	if status >= 0 {
		tx.Where("status = ?", status)
	}
	tx.Count(&total).
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Order("id DESC").
		Find(&tasks)
	hydrateUploadTaskQueueFields(tasks)
	return tasks, total
}

func ClearPendingUploadTasks() error {
	err := db.Db.Model(&DbUploadTask{}).
		Where("status = ?", UploadStatusPending).
		Delete(&DbUploadTask{}).Error
	if err != nil {
		helpers.AppLogger.Errorf("清除待上传任务失败：%v", err)
		return err
	}
	clearAllUploadProgressThrottle()
	publishUploadQueueChanged(nil, "clear_pending")
	return err
}

func ClearExpireUploadTasks() error {
	err := db.Db.Model(&DbUploadTask{}).
		Where("created_at < ?", time.Now().AddDate(0, 0, -3).Unix()).
		Delete(&DbUploadTask{}).Error
	if err != nil {
		helpers.AppLogger.Errorf("清除 3 天前的上传任务失败：%v", err)
		return err
	} else {
		helpers.AppLogger.Infof("已清除 3 天前的上传任务")
	}
	clearAllUploadProgressThrottle()
	return err
}

func ClearUploadSuccessAndFailed() error {
	err := db.Db.Model(&DbUploadTask{}).
		Where("status IN (?, ?)", UploadStatusCompleted, UploadStatusFailed).
		Delete(&DbUploadTask{}).Error
	if err != nil {
		helpers.AppLogger.Errorf("清除上传成功和失败任务失败：%v", err)
		return err
	} else {
		helpers.AppLogger.Infof("清除上传成功和失败任务成功")
	}
	clearAllUploadProgressThrottle()
	publishUploadQueueChanged(nil, "clear_success_failed")
	return err
}

func UpdateUploadingToPending() error {
	if err := db.Db.Model(&DbUploadTask{}).
		Where("status = ?", UploadStatusUploading).
		Update("status", UploadStatusPending).Error; err != nil {
		helpers.AppLogger.Errorf("更新上传中的任务为待上传失败：%v", err)
		return err
	}
	if err := db.Db.Model(&DbUploadTask{}).
		Where("status = ?", UploadStatusRemoteCompletedFinalizing).
		Update("status", UploadStatusRemoteCompletedPendingFinalize).Error; err != nil {
		helpers.AppLogger.Errorf("更新收尾中的上传任务为待收尾失败：%v", err)
		return err
	}
	helpers.AppLogger.Infof("更新上传中的任务为待上传成功")
	return nil
}

func RetryFailedUploadTasks(maxRetry int) error {
	updateData := map[string]interface{}{
		"status":          UploadStatusPending,
		"error":           "",
		"retry_count":     gorm.Expr("retry_count + 1"),
		"last_retry_time": time.Now().Unix(),
	}
	err := db.Db.Model(&DbUploadTask{}).
		Where("status = ? AND retry_count < ?", UploadStatusFailed, maxRetry).
		Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("重试失败的上传任务失败：%v", err)
		return err
	}
	helpers.AppLogger.Infof("重试失败的上传任务成功")
	publishUploadQueueChanged(nil, "retry_failed")
	return nil
}

func GetUnFinishUploadTaskCountByScrapeMediaId(scrapeMediaFileId uint) int64 {
	var count int64
	db.Db.Model(&DbUploadTask{}).
		Where("scrape_media_file_id = ? AND status IN ?", scrapeMediaFileId, activeUploadTaskStatuses()).
		Count(&count)
	return count
}

// CreateUploadTask 创建上传任务并发布队列变更事件。
func CreateUploadTask(task *DbUploadTask) error {
	if task == nil {
		return errors.New("上传任务为空")
	}
	if task.Status == 0 {
		task.Status = UploadStatusPending
	}
	if task.UploadResult == "" {
		task.UploadResult = UploadResultUnknown
	}
	if task.SourceCleanupStatus == "" {
		task.SourceCleanupStatus = UploadSourceCleanupStatusNone
	}
	if err := db.Db.Create(task).Error; err != nil {
		return err
	}
	PublishUploadTaskCreated(task)
	return nil
}

// UpdateUploadTaskLocalPath 更新上传任务的本地文件路径（跨盘秒传兜底任务先建任务再补路径）。
func UpdateUploadTaskLocalPath(taskID uint, localPath string) error {
	return db.Db.Model(&DbUploadTask{}).Where("id = ?", taskID).Update("local_full_path", localPath).Error
}

// downloadCrossTransferSource 跨盘秒传兜底：把源网盘文件下载到本地临时路径。
func (task *DbUploadTask) downloadCrossTransferSource() error {
	account, err := GetAccountById(task.SourceAccountId)
	if err != nil {
		return fmt.Errorf("源账号 %d 不存在", task.SourceAccountId)
	}
	task.Uploading()
	switch account.SourceType {
	case SourceType115:
		client := account.Get115Client()
		if client == nil {
			return fmt.Errorf("源账号 115 客户端不存在")
		}
		url := client.GetDownloadUrl(context.Background(), task.SourceFileId, v115open.DEFAULTUA, false)
		if url == "" {
			return fmt.Errorf("获取 115 源文件下载链接失败：%s", task.SourceFileId)
		}
		return helpers.DownloadFile(url, task.LocalFullPath, v115open.DEFAULTUA)
	case SourceType123:
		client := account.Get123Client()
		if client == nil {
			return fmt.Errorf("源账号 123 云盘客户端不存在")
		}
		file, err := client.GetFileById(context.Background(), task.SourceFileId, "")
		if err != nil {
			return fmt.Errorf("获取 123 云盘源文件信息失败：%w", err)
		}
		downloadInfo, err := client.GetDownloadInfo(context.Background(), *file)
		if err != nil {
			return fmt.Errorf("获取 123 云盘源文件下载信息失败：%w", err)
		}
		url, err := client.ResolveDownloadURL(context.Background(), downloadInfo.Data.DownloadUrl)
		if err != nil {
			return fmt.Errorf("解析 123 云盘源文件下载链接失败：%w", err)
		}
		return helpers.DownloadFile(url, task.LocalFullPath, pan123.WebUA)
	case SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		if client == nil {
			return fmt.Errorf("源账号百度网盘客户端不存在")
		}
		fileDetail, err := client.GetFileDetail(context.Background(), task.SourceFileId, 1)
		if err != nil {
			return fmt.Errorf("获取百度网盘源文件详情失败：%w", err)
		}
		url := fmt.Sprintf("%s&access_token=%s", fileDetail.Dlink, account.Token)
		return helpers.DownloadFile(url, task.LocalFullPath, "pan.baidu.com")
	case SourceTypeOpenList:
		client := account.GetOpenListClient()
		if client == nil {
			return fmt.Errorf("源账号 OpenList 客户端不存在")
		}
		url := client.GetRawUrl(task.SourceFileId)
		if url == "" {
			return fmt.Errorf("获取 OpenList 源文件直链失败：%s", task.SourceFileId)
		}
		return helpers.DownloadFile(url, task.LocalFullPath, v115open.DEFAULTUA)
	case SourceTypeGuangYaPan:
		client := account.GetGuangYaPanClient()
		if client == nil {
			return fmt.Errorf("源账号光鸭云盘客户端不存在")
		}
		defer client.Close()
		file, err := client.GetFileById(context.Background(), task.SourceFileId, "")
		if err != nil {
			return fmt.Errorf("获取光鸭云盘源文件信息失败：%w", err)
		}
		url, err := client.GetDownloadURL(context.Background(), file.GetID())
		if err != nil {
			return fmt.Errorf("获取光鸭云盘源文件下载链接失败：%w", err)
		}
		return helpers.DownloadFile(url, task.LocalFullPath, "")
	case SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("源账号中国移动云盘客户端不存在")
		}
		defer client.Close()
		url, err := client.GetDownloadURL(context.Background(), task.SourceFileId)
		if err != nil {
			return fmt.Errorf("获取中国移动云盘源文件下载链接失败：%w", err)
		}
		return helpers.DownloadFile(url, task.LocalFullPath, "")
	default:
		return fmt.Errorf("未知的源账号类型 %s", account.SourceType)
	}
}
