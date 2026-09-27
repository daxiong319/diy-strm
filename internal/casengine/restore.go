package casengine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"diy-strm/internal/cloud189"
	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 秒传恢复：.cas → 天翼三步秒传
// ---------------------------------------------------------------------------

// RestoreResult 恢复结果
type RestoreResult struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	RecordID uint   `json:"record_id"`
}

// RestoreFromCasText 从 CAS 文本一键秒传恢复
func RestoreFromCasText(ctx context.Context, accountID uint, targetFolderID, casText string) (*RestoreResult, error) {
	manifest, err := cloud189.ParseManifestV2(casText)
	if err != nil {
		// 兼容 V1
		v1, err2 := cloud189.ParseManifestText(casText)
		if err2 != nil {
			return nil, fmt.Errorf("CAS 解析失败: %v / %v", err, err2)
		}
		manifest = cloud189.UpgradeToV2(v1)
	}
	if manifest.Hashes.Md5 == "" || manifest.Hashes.SliceMd5 == "" {
		return nil, fmt.Errorf("CAS 缺少天翼秒传必需的 fileMd5/sliceMd5 特征")
	}
	account, err := models.GetAccountById(accountID)
	if err != nil || account == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	if account.SourceType != models.SourceTypeCloud189 {
		return nil, fmt.Errorf("秒传恢复仅支持天翼云盘账号")
	}
	client := account.GetCloud189Client()
	familyID, folderID := resolveRestoreTarget(targetFolderID)
	fileID, err := client.RapidUpload(ctx, folderID, manifest.FileName, manifest.FileSize,
		manifest.Hashes.Md5, manifest.Hashes.SliceMd5, familyID)
	if err != nil {
		return nil, err
	}
	// 更新记录状态
	_ = db.Db.Model(&CasManifestRecord{}).
		Where("file_md5 = ? AND file_size = ?", strings.ToUpper(manifest.Hashes.Md5), manifest.FileSize).
		Updates(map[string]any{
			"status":           "restored",
			"restored_at":      time.Now().Unix(),
			"restored_file_id": fileID,
		}).Error
	return &RestoreResult{FileID: fileID, FileName: manifest.FileName}, nil
}

// RestoreFromRecord 从 DB 记录恢复（CAS 管理界面一键恢复）
func RestoreFromRecord(ctx context.Context, recordID uint, targetFolderID string) (*RestoreResult, error) {
	var rec CasManifestRecord
	if err := db.Db.First(&rec, recordID).Error; err != nil {
		return nil, fmt.Errorf("记录不存在")
	}
	return RestoreFromCasText(ctx, rec.AccountID, targetFolderID, rec.CasContent)
}

// resolveRestoreTarget 恢复目标：默认个人网盘根目录
func resolveRestoreTarget(targetFolderID string) (familyID, folderID string) {
	if targetFolderID != "" {
		return "", targetFolderID
	}
	return "", "-11"
}

// writeCasLocal 本地缓存 .cas（/app/config/cas/{accountId}/{fileName}.cas）
func writeCasLocal(accountID uint, fileName, casContent string) error {
	dir := filepath.Join(helpers.ConfigDir, "cas", fmt.Sprintf("%d", accountID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(fileName)
	if !cloud189.IsCasFileName(safe) {
		safe += ".cas"
	}
	return os.WriteFile(filepath.Join(dir, safe), []byte(casContent), 0644)
}

// ListRecords 分页查询 CAS 记录
func ListRecords(page, pageSize int, status, keyword string) ([]CasManifestRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	q := db.Db.Model(&CasManifestRecord{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		q = q.Where("file_name LIKE ?", "%"+keyword+"%")
	}
	var total int64
	q.Count(&total)
	var rows []CasManifestRecord
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	return rows, total, err
}

// DeleteRecord 删除记录（不恢复云端）
func DeleteRecord(recordID uint) error {
	return db.Db.Delete(&CasManifestRecord{}, recordID).Error
}

// ExportRecordText 导出 .cas 文本（下载用）
func ExportRecordText(recordID uint) (string, string, error) {
	var rec CasManifestRecord
	if err := db.Db.First(&rec, recordID).Error; err != nil {
		return "", "", fmt.Errorf("记录不存在")
	}
	return rec.FileName, rec.CasContent, nil
}
