package cas

import (
	"context"
	"fmt"

	"litepan/internal/discover/ddb"
)

// ListRecords 分页查询 CAS 清单记录
func ListRecords(page, pageSize int, status, keyword string) ([]CasManifestRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	q := ddb.Db.Model(&CasManifestRecord{})
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
	return ddb.Db.Delete(&CasManifestRecord{}, recordID).Error
}

// ExportRecordText 导出 .cas 文本（下载用）
func ExportRecordText(recordID uint) (string, string, error) {
	var rec CasManifestRecord
	if err := ddb.Db.First(&rec, recordID).Error; err != nil {
		return "", "", fmt.Errorf("记录不存在")
	}
	return rec.FileName, rec.CasContent, nil
}

// GetRecordByID 按 ID 取 CAS 清单记录
func GetRecordByID(id uint) (*CasManifestRecord, error) {
	var rec CasManifestRecord
	if err := ddb.Db.First(&rec, id).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// DeleteRestoredSource 播放恢复后的延时清理：删除恢复的源文件（.cas 保留）
func DeleteRestoredSource(ctx context.Context, accountID uint, sourceType, remoteFileID string) error {
	driver := driverResolver(accountID, sourceType)
	if driver == nil {
		return errNoDriver(sourceType)
	}
	return driver.DeleteFile(ctx, remoteFileID)
}
