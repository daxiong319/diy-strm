package cas

import (
	"context"
	"time"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/ddb"
	"litepan/internal/discover/dutil"
	"litepan/internal/discover/mediaparse"
)

// CasCandidate CAS 化候选任务（网盘/上传无关描述，由调用方从上传任务转换）。
type CasCandidate struct {
	UploadTaskID   int64  // 上传任务 ID（幂等去重键）
	AccountID      int64  // 目标网盘账号
	SourceType     string // 驱动类型（189cloud/139_cloud/quark）
	RemoteParentID string // 云端父目录 ID
	FileName       string
	RemoteFileID   string
	FileSize       int64
	CompletedAt    time.Time // 上传完成时间
}

// RunOnce 执行一轮 CAS 化（对候选的已完成影视上传任务生成 .cas 并可选删源）。
// candidates 由装配层扫描上传任务得到；内部按配置（enabled/ageDays）过滤满 N 天的影视文件。
// 返回处理统计（生成/删除/跳过/失败）。
func RunOnce(ctx context.Context, candidates []CasCandidate) (generated, deleted, skipped, failed int, err error) {
	cfg := getConfig()
	if !cfg.Enabled {
		return 0, 0, 0, 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(cfg.AgeDays) * 24 * time.Hour)

	for _, task := range candidates {
		select {
		case <-ctx.Done():
			return generated, deleted, skipped, failed, ctx.Err()
		default:
		}
		// 满 N 天才 CAS 化
		if task.CompletedAt.After(cutoff) {
			continue
		}
		// 仅影视视频文件
		if !mediaparse.IsVideoExt(task.FileName) {
			skipped++
			continue
		}
		// 已 CAS 化过跳过（upload_task_id 幂等）
		var exist CasManifestRecord
		if e := ddb.Db.Where("upload_task_id = ?", task.UploadTaskID).First(&exist).Error; e == nil {
			skipped++
			continue
		}

		// 取五哈希
		driver := driverResolver(task.AccountID, task.SourceType)
		if driver == nil {
			failed++
			dutil.AppLogger.Warnf("CAS 化：驱动不可用（账号 %d / %s）", task.AccountID, task.SourceType)
			continue
		}
		hashes, ok := driver.FetchFingerprint(ctx, task.RemoteParentID, task.RemoteFileID, task.FileName)
		if !ok || hashSetIsEmpty(hashes) {
			failed++
			dutil.AppLogger.Warnf("CAS 化：取指纹失败 %s（%s）", task.FileName, task.SourceType)
			continue
		}

		// 生成清单 + 删源（复用 GenerateCASForFile 的核心，但保留 upload_task_id 关联）
		res, gerr := generateForTask(ctx, task, hashes, cfg.WriteBackCloud)
		if gerr != nil {
			failed++
			dutil.AppLogger.Warnf("CAS 化：%s 失败：%v", task.FileName, gerr)
			continue
		}
		if res.Skipped {
			skipped++
			continue
		}
		generated++
		if res.SourceDeleted {
			deleted++
		}
	}
	return generated, deleted, skipped, failed, nil
}

// generateForTask 对单个候选任务生成 .cas 清单并可选删源，落库保留 upload_task_id。
func generateForTask(ctx context.Context, task CasCandidate, hashes cloud189.HashSet, writeBack bool) (*struct {
	Skipped       bool
	SourceDeleted bool
}, error) {
	out := &struct {
		Skipped       bool
		SourceDeleted bool
	}{}

	// account+remote_file_id 幂等
	var exist CasManifestRecord
	if err := ddb.Db.Where("account_id = ? AND remote_file_id = ?", task.AccountID, task.RemoteFileID).First(&exist).Error; err == nil {
		out.Skipped = true
		return out, nil
	}

	manifest := cloud189.CasManifestV2{
		Version:     2,
		FileName:    task.FileName,
		FileSize:    task.FileSize,
		Hashes:      hashes,
		SourceDrive: task.SourceType,
		CreatedAt:   task.CompletedAt.Format(time.RFC3339),
	}
	casContent := cloud189.EncodeManifestV2(manifest)
	rapidPayload := BuildRapidPayloadFor(task.SourceType, task.FileName, task.FileSize, hashes)
	rapidDriveTypes := DeriveRapidDriveTypes(hashes)

	// 可选写回网盘 .cas
	casFileID := ""
	if writeBack {
		if driver := driverResolver(task.AccountID, task.SourceType); driver != nil {
			if cid, uerr := driver.UploadTextFile(ctx, task.RemoteParentID, task.FileName+".cas", casContent); uerr == nil {
				casFileID = cid
			}
		}
	}

	// 删源（删除失败不阻塞，清单仍生成，状态 pending）
	status := "active"
	var deletedAt int64
	sourceDeleted := true
	driver := driverResolver(task.AccountID, task.SourceType)
	if driver == nil {
		sourceDeleted = false
		status = "pending"
	} else if derr := driver.DeleteFile(ctx, task.RemoteFileID); derr != nil {
		sourceDeleted = false
		status = "pending"
		dutil.AppLogger.Warnf("CAS 化：%s 清单生成成功但删源失败：%v", task.FileName, derr)
	}
	if sourceDeleted {
		deletedAt = time.Now().Unix()
	}

	rec := CasManifestRecord{
		UploadTaskID:    task.UploadTaskID,
		AccountID:       task.AccountID,
		SourceType:      task.SourceType,
		FileName:        task.FileName,
		FileSize:        task.FileSize,
		FileMd5:         hashes.FileMd5,
		SliceMd5:        hashes.SliceMd5,
		Sha1:            hashes.Sha1,
		Sha256:          hashes.Sha256,
		PreHash:         hashes.PreHash,
		Gcid:            hashes.Gcid,
		RemoteFileID:    task.RemoteFileID,
		RemotePath:      task.RemoteParentID,
		CasContent:      casContent,
		CasFileID:       casFileID,
		RapidPayload:    rapidPayload,
		RapidDriveTypes: rapidDriveTypes,
		Status:          status,
		DeletedAt:       deletedAt,
	}
	if err := ddb.Db.Create(&rec).Error; err != nil {
		return out, err
	}
	out.SourceDeleted = sourceDeleted
	dutil.AppLogger.Infof("CAS 化完成：%s（删源=%v，可秒传盘=%s）", task.FileName, sourceDeleted, rapidDriveTypes)
	return out, nil
}

// hashSetIsEmpty 判断五哈希是否全空
func hashSetIsEmpty(hs cloud189.HashSet) bool {
	return hs.FileMd5 == "" && hs.SliceMd5 == "" && hs.Sha1 == "" &&
		hs.Sha256 == "" && hs.PreHash == "" && hs.Gcid == ""
}
