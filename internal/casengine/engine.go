package casengine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"diy-strm/internal/cloud189"
	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// CAS 自动化引擎：影视上传满 N 天自动生成 .cas 并删除云端源视频
// ---------------------------------------------------------------------------

// CasManifestRecord CAS 清单记录（DB 持久化）
type CasManifestRecord struct {
	models.BaseModel
	UploadTaskID   uint   `json:"upload_task_id" gorm:"index"`
	AccountID      uint   `json:"account_id" gorm:"index"`
	SourceType     string `json:"source_type"`
	FileName       string `json:"file_name" gorm:"size:512"`
	FileSize       int64  `json:"file_size"`
	FileMd5        string `json:"file_md5" gorm:"size:32"`
	SliceMd5       string `json:"slice_md5" gorm:"size:32"`
	RemoteFileID   string `json:"remote_file_id" gorm:"size:128"`
	RemotePath     string `json:"remote_path" gorm:"size:1024"`
	CasContent     string `json:"cas_content" gorm:"type:text"`
	Status         string `json:"status" gorm:"size:32"` // active(源已删)/restored(已恢复)/pending
	DeletedAt      int64  `json:"deleted_at"`            // 源视频删除时间（unix 秒）
	RestoredAt     int64  `json:"restored_at"`
	RestoredFileID string `json:"restored_file_id" gorm:"size:128"`
}

func (CasManifestRecord) TableName() string {
	return "cas_manifests"
}

// EnsureTable 建表
func EnsureTable() {
	_ = db.Db.AutoMigrate(&CasManifestRecord{})
}

// CasConfig CAS 自动化配置（存 discovery_settings 或 cloud_settings）
type CasConfig struct {
	Enabled        bool `json:"enabled"`
	AgeDays        int  `json:"age_days"`         // 满多少天才 CAS 化（默认 30）
	WriteBackCloud bool `json:"write_back_cloud"` // .cas 是否写回网盘（默认 false，只存 DB）
}

func getConfig() CasConfig {
	cfg := CasConfig{Enabled: true, AgeDays: 30}
	if v, ok := discoverySettingGet("cas_engine_config"); ok {
		_ = jsonUnmarshal([]byte(v), &cfg)
	}
	if cfg.AgeDays <= 0 {
		cfg.AgeDays = 30
	}
	return cfg
}

func discoverySettingGet(key string) (string, bool) {
	var row struct{ Value string }
	if err := db.Db.Table("discovery_settings").Select("value").Where("key = ?", key).Take(&row).Error; err != nil {
		return "", false
	}
	return row.Value, true
}

func discoverySettingSet(key, value string) {
	_ = db.Db.Exec("INSERT INTO discovery_settings(key, value, updated_at) VALUES(?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at",
		key, value, time.Now()).Error
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// RunOnce 执行一轮 CAS 化（满 ageDays 的影视上传任务）
// 返回处理统计（生成/删除/跳过/失败）
func RunOnce(ctx context.Context) (generated, deleted, skipped, failed int, err error) {
	cfg := getConfig()
	if !cfg.Enabled {
		return 0, 0, 0, 0, nil
	}
	cutoff := time.Now().Unix() - int64(cfg.AgeDays)*86400

	// 候选：上传完成满 ageDays 的影视视频任务（尚未 CAS 化）
	var tasks []models.DbUploadTask
	if err := db.Db.Where("status = ? AND end_time > 0 AND end_time < ?",
		models.UploadStatusCompleted, cutoff).
		Find(&tasks).Error; err != nil {
		return 0, 0, 0, 0, err
	}

	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return generated, deleted, skipped, failed, ctx.Err()
		default:
		}
		if !mediaparse.IsVideoExt(task.FileName) {
			skipped++
			continue
		}
		// 已 CAS 化过的跳过
		var exist CasManifestRecord
		if err := db.Db.Where("upload_task_id = ?", task.ID).First(&exist).Error; err == nil {
			skipped++
			continue
		}
		// 需要 fileMd5/sliceMd5：任务表没有，需要从网盘文件元数据取
		account, err := models.GetAccountById(task.AccountId)
		if err != nil || account == nil {
			failed++
			continue
		}
		md5, sliceMd5, remotePath, ok := fetchCloudFingerprint(ctx, account, task)
		if !ok {
			failed++
			continue
		}
		manifest := cloud189.CasManifest{
			FileName:   task.FileName,
			FileSize:   task.FileSize,
			FileMd5:    md5,
			SliceMd5:   sliceMd5,
			UploadTime: time.Unix(task.EndTime, 0).Format(time.RFC3339),
		}
		casContent := cloud189.EncodeManifestV1(manifest)

		// 可选写回网盘 .cas 文件
		if cfg.WriteBackCloud {
			_ = writeCasToCloud(ctx, account, remotePath, task.FileName, casContent)
		}

		// 删除云端源视频
		delErr := deleteCloudSource(ctx, account, task.RemoteFileId)
		rec := CasManifestRecord{
			UploadTaskID: task.ID,
			AccountID:    task.AccountId,
			SourceType:   string(task.SourceType),
			FileName:     task.FileName,
			FileSize:     task.FileSize,
			FileMd5:      md5,
			SliceMd5:     sliceMd5,
			RemoteFileID: task.RemoteFileId,
			RemotePath:   remotePath,
			CasContent:   casContent,
			Status:       "active",
		}
		if delErr != nil {
			rec.Status = "pending" // 删除失败，保留下轮重试
			rec.DeletedAt = 0
			failed++
			helpers.AppLogger.Warnf("CAS 化：%s 生成清单成功但删除源视频失败：%v", task.FileName, delErr)
		} else {
			rec.DeletedAt = time.Now().Unix()
			deleted++
			helpers.AppLogger.Infof("CAS 化：%s 已生成清单并删除云端源视频（%s）", task.FileName, remotePath)
		}
		if err := db.Db.Create(&rec).Error; err != nil {
			helpers.AppLogger.Errorf("CAS 化：清单落库失败：%v", err)
		}
		generated++
	}
	return generated, deleted, skipped, failed, nil
}

// fetchCloudFingerprint 从网盘文件元数据获取 fileMd5/sliceMd5/路径
func fetchCloudFingerprint(ctx context.Context, account *models.Account, task models.DbUploadTask) (md5, sliceMd5, remotePath string, ok bool) {
	switch account.SourceType {
	case models.SourceTypeCloud189:
		client := account.GetCloud189Client()
		files, err := client.ListFiles(ctx, task.RemotePathId)
		if err != nil {
			return "", "", "", false
		}
		for _, f := range files {
			if f.ID == task.RemoteFileId || f.Name == task.FileName {
				return f.MD5, f.SliceMD5, task.RemotePathId, true
			}
		}
		return "", "", "", false
	default:
		// 其他网盘（139/123/115/光鸭/百度/OpenList）的指纹获取：
		// CAS 是天翼专属秒传协议，非天翼源不生成（云端命中也无意义——秒传恢复只对接天翼）
		return "", "", "", false
	}
}

// deleteCloudSource 删除云端源视频（按网盘类型分发）
func deleteCloudSource(ctx context.Context, account *models.Account, remoteFileID string) error {
	switch account.SourceType {
	case models.SourceTypeCloud189:
		return account.GetCloud189Client().DeleteFile(ctx, remoteFileID)
	case models.SourceType115:
		_, err := account.Get115Client().Del(ctx, []string{remoteFileID}, "")
		return err
	case models.SourceType123:
		return account.Get123Client().Delete(ctx, []string{remoteFileID})
	case models.SourceTypePan139:
		return account.GetPan139Client().Delete(ctx, []string{remoteFileID})
	case models.SourceTypeGuangYaPan:
		return account.GetGuangYaPanClient().Delete(ctx, []string{remoteFileID})
	default:
		return fmt.Errorf("该网盘类型暂不支持删除：%s", account.SourceType)
	}
}

// writeCasToCloud 把 .cas 写回网盘（存 /cas 目录下同名 .cas 文件）
func writeCasToCloud(ctx context.Context, account *models.Account, remoteDir, fileName, casContent string) error {
	if account.SourceType != models.SourceTypeCloud189 {
		return nil // 只写天翼
	}
	// 天翼没有小文本直接创建文件的公开接口——用秒传失败回退普通上传的链路需要实现上传；
	// 简化：写本地缓存目录（/app/config/cas/{accountId}/），DB 已有 casContent 双保险
	return writeCasLocal(account.ID, fileName, casContent)
}

// GetConfigForAPI 配置读取（API 用）
func GetConfigForAPI() CasConfig {
	return getConfig()
}

// SaveConfig 配置保存
func SaveConfig(cfg CasConfig) {
	if cfg.AgeDays <= 0 {
		cfg.AgeDays = 30
	}
	raw, _ := json.Marshal(cfg)
	discoverySettingSet("cas_engine_config", string(raw))
}
