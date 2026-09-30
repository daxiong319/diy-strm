package cas

import (
	"context"
	"fmt"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/discover/dutil"
)

// ProbeAndRestoreFile 为播放端探测并恢复 CAS 文件。
// 1. 若云端源文件或上次恢复的文件尚存，直接复用文件 ID，无需重复秒传。
// 2. 若文件已被删除，自动重放秒传恢复出临时视频文件，并登记延时删除任务（默认 2 小时后自动删除恢复的临时文件）。
// 返回 (清单记录, 播放用的FileID, 是否执行了秒传恢复, 错误)
func ProbeAndRestoreFile(ctx context.Context, recordID uint) (*CasManifestRecord, string, bool, error) {
	rec, err := GetRecordByID(recordID)
	if err != nil || rec == nil {
		return nil, "", false, fmt.Errorf("CAS 记录不存在: id=%d", recordID)
	}

	driver := driverResolver(rec.AccountID, rec.SourceType)
	if driver == nil {
		return nil, "", false, errNoDriver(rec.SourceType)
	}

	// 1. 先探测是否已有现成文件可用（未被清理或上次恢复的文件仍在）
	candidateFileIDs := make([]string, 0, 2)
	if rec.RestoredFileID != "" {
		candidateFileIDs = append(candidateFileIDs, rec.RestoredFileID)
	}
	if rec.RemoteFileID != "" && rec.RemoteFileID != rec.RestoredFileID {
		candidateFileIDs = append(candidateFileIDs, rec.RemoteFileID)
	}

	for _, fid := range candidateFileIDs {
		if exists, actualID := driver.ProbeFile(ctx, rec.RemotePath, fid, rec.FileName); exists && actualID != "" {
			// 文件依然存在，直接播放，无需秒传
			dutil.AppLogger.Infof("CAS 播放：检测到源文件已在云端，直接播放 (cas_id=%d file=%s fid=%s)", rec.ID, rec.FileName, actualID)
			return rec, actualID, false, nil
		}
	}

	// 2. 云端无实体，执行秒传恢复
	dutil.AppLogger.Infof("CAS 播放：源文件已删除，正在触发秒传恢复 (cas_id=%d file=%s)", rec.ID, rec.FileName)
	restoreRes, err := RestoreFromRecord(ctx, recordID, rec.RemotePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("CAS 秒传恢复失败: %w", err)
	}

	newFileID := restoreRes.FileID
	if newFileID == "" {
		return nil, "", false, fmt.Errorf("CAS 秒传恢复未返回有效文件 ID")
	}

	// 3. 恢复成功，登记延时删除
	go ScheduleCASRestoreCleanup(rec.ID)

	return rec, newFileID, true, nil
}

// ScheduleCASRestoreCleanup 登记延时删除：DelayDeleteHours 小时后自动删除恢复出来的视频文件，.cas 保留。
func ScheduleCASRestoreCleanup(recordID uint) {
	cfg := getConfig()
	if cfg.DelayDeleteHours <= 0 {
		return
	}

	delay := time.Duration(cfg.DelayDeleteHours) * time.Hour
	dutil.AppLogger.Infof("CAS 播放：已为记录 %d 登记延时清理，将在 %v 后自动删除临时恢复文件", recordID, delay)

	time.Sleep(delay)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	current, err := GetRecordByID(recordID)
	if err != nil || current == nil || current.Status != "restored" || current.RestoredFileID == "" {
		return
	}

	if err := DeleteRestoredSource(ctx, current.AccountID, current.SourceType, current.RestoredFileID); err != nil {
		dutil.AppLogger.Warnf("CAS 延时清理失败：cas_id=%d fid=%s err=%v", current.ID, current.RestoredFileID, err)
		return
	}

	_ = ddb.Db.Model(&CasManifestRecord{}).Where("id = ?", current.ID).Updates(map[string]any{
		"status":           "active",
		"deleted_at":       time.Now().Unix(),
		"restored_file_id": "",
	}).Error

	dutil.AppLogger.Infof("CAS 延时清理完成：cas_id=%d 已删除恢复的源文件 (fid=%s)，.cas 保留", current.ID, current.RestoredFileID)
}

// CleanupExpiredRestoredSources 巡检兜底：清理所有已超过延时时间的恢复文件（防止服务重启丢失内存 timer）。
func CleanupExpiredRestoredSources(ctx context.Context) (cleaned int) {
	cfg := getConfig()
	if cfg.DelayDeleteHours <= 0 {
		return 0
	}

	threshold := time.Now().Add(-time.Duration(cfg.DelayDeleteHours) * time.Hour).Unix()
	var records []CasManifestRecord
	if err := ddb.Db.Where("status = ? AND restored_at > 0 AND restored_at <= ?", "restored", threshold).Find(&records).Error; err != nil {
		return 0
	}

	for _, rec := range records {
		if rec.RestoredFileID == "" {
			continue
		}
		if err := DeleteRestoredSource(ctx, rec.AccountID, rec.SourceType, rec.RestoredFileID); err == nil {
			_ = ddb.Db.Model(&CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
				"status":           "active",
				"deleted_at":       time.Now().Unix(),
				"restored_file_id": "",
			}).Error
			cleaned++
		}
	}
	return cleaned
}
