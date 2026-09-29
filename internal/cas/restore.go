package cas

import (
	"context"
	"fmt"
	"strings"
	"time"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/ddb"
)

// RestoreResult 秒传恢复结果
type RestoreResult struct {
	FileID    string `json:"file_id"`
	FileName  string `json:"file_name"`
	RecordID  uint   `json:"record_id,omitempty"`
	DriveType string `json:"drive_type"`
}

// RestoreFromCasText 从 .cas 文本恢复（兼容 V1/V2），秒传到目标账号目录。
func RestoreFromCasText(ctx context.Context, accountID int64, sourceType, targetFolderID, casText string) (*RestoreResult, error) {
	manifest, err := cloud189.ParseManifestV2(casText)
	if err != nil {
		v1, err2 := cloud189.ParseManifestText(casText)
		if err2 != nil {
			return nil, fmt.Errorf("CAS 解析失败: %v / %v", err, err2)
		}
		manifest = cloud189.UpgradeToV2(v1)
	}
	fileID, err := RapidRestore(ctx, accountID, sourceType, targetFolderID, manifest)
	if err != nil {
		return nil, err
	}
	// 更新匹配记录状态
	upd := map[string]any{
		"status":           "restored",
		"restored_at":      time.Now().Unix(),
		"restored_file_id": fileID,
	}
	q := ddb.Db.Model(&CasManifestRecord{})
	if manifest.Hashes.Sha256 != "" {
		q = q.Where("sha256 = ? AND file_size = ?", strings.ToUpper(manifest.Hashes.Sha256), manifest.FileSize)
	} else if manifest.Hashes.FileMd5 != "" {
		q = q.Where("file_md5 = ? AND file_size = ?", strings.ToUpper(manifest.Hashes.FileMd5), manifest.FileSize)
	} else {
		q = q.Where("file_name = ? AND file_size = ?", manifest.FileName, manifest.FileSize)
	}
	_ = q.Updates(upd).Error
	return &RestoreResult{FileID: fileID, FileName: manifest.FileName, DriveType: sourceType}, nil
}

// RestoreFromRecord 从 DB 记录恢复（优先秒传请求原文重放，回退按五哈希）。
func RestoreFromRecord(ctx context.Context, recordID uint, targetFolderID string) (*RestoreResult, error) {
	var rec CasManifestRecord
	if err := ddb.Db.First(&rec, recordID).Error; err != nil {
		return nil, fmt.Errorf("记录不存在")
	}
	driver := driverResolver(rec.AccountID, rec.SourceType)
	if driver == nil {
		return nil, errNoDriver(rec.SourceType)
	}
	// 优先：秒传请求原文重放
	if rec.RapidPayload != "" {
		if fileID, err := driver.ReplayRapid(ctx, rec.RapidPayload, targetFolderID); err == nil {
			_ = ddb.Db.Model(&CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
				"status":           "restored",
				"restored_at":      time.Now().Unix(),
				"restored_file_id": fileID,
			}).Error
			return &RestoreResult{FileID: fileID, FileName: rec.FileName, RecordID: rec.ID, DriveType: rec.SourceType}, nil
		}
	}
	// 回退：按五哈希恢复
	manifest := cloud189.CasManifestV2{
		Version:  2,
		FileName: rec.FileName,
		FileSize: rec.FileSize,
		Hashes: cloud189.HashSet{
			Sha1:     rec.Sha1,
			Sha256:   rec.Sha256,
			FileMd5:  rec.FileMd5,
			SliceMd5: rec.SliceMd5,
			PreHash:  rec.PreHash,
			Gcid:     rec.Gcid,
		},
		SourceDrive: rec.SourceType,
	}
	fileID, err := RapidRestore(ctx, rec.AccountID, rec.SourceType, targetFolderID, manifest)
	if err != nil {
		return nil, err
	}
	_ = ddb.Db.Model(&CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"status":           "restored",
		"restored_at":      time.Now().Unix(),
		"restored_file_id": fileID,
	}).Error
	return &RestoreResult{FileID: fileID, FileName: rec.FileName, RecordID: rec.ID, DriveType: rec.SourceType}, nil
}

// RapidRestore 按网盘类型分发秒传（走 RapidDriver）
func RapidRestore(ctx context.Context, accountID int64, sourceType, targetFolderID string, m cloud189.CasManifestV2) (string, error) {
	driver := driverResolver(accountID, sourceType)
	if driver == nil {
		return "", errNoDriver(sourceType)
	}
	// 构造秒传 payload 并重放
	payload := BuildRapidPayloadFor(sourceType, m.FileName, m.FileSize, m.Hashes)
	if payload == "" {
		return "", fmt.Errorf("网盘 %s 暂不支持秒传恢复", sourceType)
	}
	return driver.ReplayRapid(ctx, payload, targetFolderID)
}
