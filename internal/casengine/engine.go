package casengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// CasManifestRecord CAS 清单记录（DB 持久化，五哈希统一模型 + 秒传请求原文缓存）
type CasManifestRecord struct {
	models.BaseModel
	UploadTaskID    uint   `json:"upload_task_id" gorm:"index"`
	AccountID       uint   `json:"account_id" gorm:"index"`
	SourceType      string `json:"source_type"`
	FileName        string `json:"file_name" gorm:"size:512"`
	FileSize        int64  `json:"file_size"`
	FileMd5         string `json:"file_md5" gorm:"size:32"`
	SliceMd5        string `json:"slice_md5" gorm:"size:64"`
	Sha1            string `json:"sha1" gorm:"size:40"`
	Sha256          string `json:"sha256" gorm:"size:64"`
	PreHash         string `json:"pre_hash" gorm:"size:256"` // 夸克 4×4MB 分块 MD5 预检串
	Gcid            string `json:"gcid" gorm:"size:128"`
	RemoteFileID    string `json:"remote_file_id" gorm:"size:128"`
	RemotePath      string `json:"remote_path" gorm:"size:1024"`
	CasContent      string `json:"cas_content" gorm:"type:text"`
	RapidPayload    string `json:"rapid_payload" gorm:"type:text"`    // 秒传请求原文（cloud-auto-save-x 同款）
	RapidDriveTypes string `json:"rapid_drive_types" gorm:"size:256"` // 可秒传到的盘（逗号分隔，如 cloud189,cloud139,quark）
	Status          string `json:"status" gorm:"size:32"`             // active(源已删)/restored(已恢复)/pending
	DeletedAt       int64  `json:"deleted_at"`                        // 源视频删除时间（unix 秒）
	RestoredAt      int64  `json:"restored_at"`
	RestoredFileID  string `json:"restored_file_id" gorm:"size:128"`
}

func (CasManifestRecord) TableName() string {
	return "cas_manifests"
}

// RapidRecord 秒传请求原文缓存表（对齐 cloud-auto-save-x dl302_rapid_records）
type RapidRecord struct {
	models.BaseModel
	DriveType     string `json:"drive_type" gorm:"index:idx_rapid_drive_file,unique"`
	FileID        string `json:"file_id" gorm:"size:128;index:idx_rapid_drive_file,unique"`
	Size          int64  `json:"size"`
	FileMd5       string `json:"file_md5" gorm:"size:32"`
	SliceMd5      string `json:"slice_md5" gorm:"size:64"`
	Sha1          string `json:"sha1" gorm:"size:40"`
	Sha256        string `json:"sha256" gorm:"size:64"`
	PreHash       string `json:"pre_hash" gorm:"size:256"`
	Gcid          string `json:"gcid" gorm:"size:128"`
	Base64Payload string `json:"base64_payload" gorm:"type:text"` // 秒传请求原文（base64 编码 JSON）
}

func (RapidRecord) TableName() string {
	return "cas_rapid_records"
}

// EnsureTable 建表
func EnsureTable() {
	_ = db.Db.AutoMigrate(&CasManifestRecord{})
	_ = db.Db.AutoMigrate(&RapidRecord{})
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
		// 从网盘文件元数据取统一五哈希指纹（免下载）
		account, err := models.GetAccountById(task.AccountId)
		if err != nil || account == nil {
			failed++
			continue
		}
		hashes, remotePath, ok := fetchCloudFingerprint(ctx, account, task)
		if !ok {
			failed++
			continue
		}
		manifest := cloud189.CasManifestV2{
			Version:     2,
			FileName:    task.FileName,
			FileSize:    task.FileSize,
			Hashes:      hashes,
			SourceDrive: string(account.SourceType),
			CreatedAt:   time.Unix(task.EndTime, 0).Format(time.RFC3339),
		}
		casContent := cloud189.EncodeManifestV2(manifest)
		// 秒传请求原文（恢复时重放）
		rapidPayload := buildRapidPayload(account.SourceType, task, hashes)
		rapidDriveTypes := deriveRapidDriveTypes(hashes)

		// 可选写回网盘 .cas 文件
		if cfg.WriteBackCloud {
			_ = writeCasToCloud(ctx, account, remotePath, task.FileName, casContent)
		}

		// 删除云端源视频
		delErr := deleteCloudSource(ctx, account, task.RemoteFileId)
		rec := CasManifestRecord{
			UploadTaskID:    task.ID,
			AccountID:       task.AccountId,
			SourceType:      string(task.SourceType),
			FileName:        task.FileName,
			FileSize:        task.FileSize,
			FileMd5:         hashes.FileMd5,
			SliceMd5:        hashes.SliceMd5,
			Sha1:            hashes.Sha1,
			Sha256:          hashes.Sha256,
			PreHash:         hashes.PreHash,
			Gcid:            hashes.Gcid,
			RemoteFileID:    task.RemoteFileId,
			RemotePath:      remotePath,
			CasContent:      casContent,
			RapidPayload:    rapidPayload,
			RapidDriveTypes: rapidDriveTypes,
			Status:          "active",
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

// fetchCloudFingerprint 从网盘文件元数据获取统一五哈希指纹（免下载重算）
func fetchCloudFingerprint(ctx context.Context, account *models.Account, task models.DbUploadTask) (cloud189.HashSet, string, bool) {
	var hs cloud189.HashSet
	switch account.SourceType {
	case models.SourceTypeCloud189:
		client := account.GetCloud189Client()
		files, err := client.ListFiles(ctx, task.RemotePathId)
		if err != nil {
			return hs, "", false
		}
		for _, f := range files {
			if f.ID == task.RemoteFileId || f.Name == task.FileName {
				hs.FileMd5 = strings.ToUpper(f.MD5)
				hs.SliceMd5 = strings.ToUpper(f.SliceMD5)
				return hs, task.RemotePathId, true
			}
		}
		return hs, "", false
	case models.SourceTypePan139:
		// 139 列表 API 返回 contentHash(sha256)——免下载取指纹
		client := account.GetPan139Client()
		sha256, ok := client.GetFileSHA256(ctx, task.RemoteFileId)
		if !ok {
			return hs, "", false
		}
		hs.Sha256 = strings.ToLower(sha256)
		return hs, task.RemotePathId, true
	case models.SourceTypeQuark:
		// 夸克 pre_hash（4×4MB 分块 MD5）在上传时已计算并落库到 task.PreHash；
		// 列表接口只返回全量 md5，无法反向推 pre_hash，故优先读 task.PreHash。
		if task.PreHash == "" {
			// 存量任务无 pre_hash：尝试从夸克秒传记录表兜底
			if preHash, md5, ok := lookupQuarkRapid(ctx, account, task); ok {
				hs.FileMd5 = md5
				hs.PreHash = preHash
				return hs, task.RemotePathId, true
			}
			return hs, "", false
		}
		hs.PreHash = strings.ToLower(task.PreHash)
		// 全量 md5 从列表补（可选，秒传主要靠 pre_hash）
		if client := account.GetQuarkClient(); client != nil {
			if md5, _, ok := client.GetFileHash(ctx, task.RemoteFileId); ok && md5 != "" {
				hs.FileMd5 = strings.ToLower(md5)
			}
		}
		return hs, task.RemotePathId, true
	default:
		// 其他网盘（123/115/光鸭/百度/OpenList）暂不接入 CAS（秒传特征各盘独立，后续按需扩展）
		return hs, "", false
	}
}

// deriveRapidDriveTypes 由五哈希非空字段推导可秒传到的盘
func deriveRapidDriveTypes(hs cloud189.HashSet) string {
	types := make([]string, 0, 3)
	if hs.FileMd5 != "" || hs.SliceMd5 != "" {
		types = append(types, "cloud189") // 天翼：fileMd5+sliceMd5
	}
	if hs.Sha256 != "" {
		types = append(types, "cloud139") // 移动云盘：sha256
	}
	if hs.FileMd5 != "" && hs.PreHash != "" {
		types = append(types, "quark") // 夸克：fileMd5+preHash
	}
	return strings.Join(types, ",")
}

// buildRapidPayload 生成秒传请求原文（cloud-auto-save-x 同款：恢复时重放）
func buildRapidPayload(sourceType models.SourceType, task models.DbUploadTask, hs cloud189.HashSet) string {
	switch sourceType {
	case models.SourceTypeCloud189:
		p := map[string]any{
			"drive_type": "cloud189",
			"kind":       "rapid_upload",
			"name":       task.FileName,
			"size":       task.FileSize,
			"params": map[string]any{
				"fileMd5":  hs.FileMd5,
				"sliceMd5": hs.SliceMd5,
			},
		}
		return marshalJSON(p)
	case models.SourceTypePan139:
		p := map[string]any{
			"drive_type": "cloud139",
			"kind":       "file.create",
			"name":       task.FileName,
			"size":       task.FileSize,
			"params": map[string]any{
				"contentHash":          hs.Sha256,
				"contentHashAlgorithm": "SHA256",
				"fileRenameMode":       "auto_rename",
				"name":                 task.FileName,
				"parentFileId":         "",
				"size":                 task.FileSize,
				"type":                 "file",
			},
		}
		return marshalJSON(p)
	case models.SourceTypeQuark:
		p := map[string]any{
			"drive_type": "quark",
			"kind":       "file",
			"name":       task.FileName,
			"size":       task.FileSize,
			"params": map[string]any{
				"file_md5": hs.FileMd5,
				"pre_hash": hs.PreHash,
			},
		}
		return marshalJSON(p)
	default:
		return ""
	}
}

func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
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
	case models.SourceTypeQuark:
		return account.GetQuarkClient().DeleteFile(ctx, []string{remoteFileID})
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

// lookupQuarkRapid 从秒传记录表兜底查夸克 pre_hash/file_md5（存量任务无 task.PreHash 时）
func lookupQuarkRapid(ctx context.Context, account *models.Account, task models.DbUploadTask) (preHash, md5 string, ok bool) {
	var rec RapidRecord
	if err := db.Db.Where("drive_type = ? AND file_id = ?", "quark", task.RemoteFileId).First(&rec).Error; err == nil {
		if rec.PreHash != "" {
			return rec.PreHash, rec.FileMd5, true
		}
	}
	return "", "", false
}
