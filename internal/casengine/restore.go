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
	"diy-strm/internal/quark"
)

// ---------------------------------------------------------------------------
// 秒传恢复：按 drive_type 分发（天翼/139/夸克），优先请求原文重放
// ---------------------------------------------------------------------------

// RestoreResult 恢复结果
type RestoreResult struct {
	FileID    string `json:"file_id"`
	FileName  string `json:"file_name"`
	RecordID  uint   `json:"record_id"`
	DriveType string `json:"drive_type"`
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
	account, err := models.GetAccountById(accountID)
	if err != nil || account == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	fileID, err := rapidRestore(ctx, account, targetFolderID, manifest)
	if err != nil {
		return nil, err
	}
	// 更新记录状态（按 sha256 或 fileMd5 或 fileSize 匹配）
	upd := map[string]any{
		"status":           "restored",
		"restored_at":      time.Now().Unix(),
		"restored_file_id": fileID,
	}
	q := db.Db.Model(&CasManifestRecord{})
	if manifest.Hashes.Sha256 != "" {
		q = q.Where("sha256 = ? AND file_size = ?", strings.ToUpper(manifest.Hashes.Sha256), manifest.FileSize)
	} else if manifest.Hashes.FileMd5 != "" {
		q = q.Where("file_md5 = ? AND file_size = ?", strings.ToUpper(manifest.Hashes.FileMd5), manifest.FileSize)
	} else {
		q = q.Where("file_name = ? AND file_size = ?", manifest.FileName, manifest.FileSize)
	}
	_ = q.Updates(upd).Error
	return &RestoreResult{FileID: fileID, FileName: manifest.FileName, DriveType: string(account.SourceType)}, nil
}

// RestoreFromRecord 从 DB 记录恢复（优先用秒传请求原文重放）
func RestoreFromRecord(ctx context.Context, recordID uint, targetFolderID string) (*RestoreResult, error) {
	var rec CasManifestRecord
	if err := db.Db.First(&rec, recordID).Error; err != nil {
		return nil, fmt.Errorf("记录不存在")
	}
	account, err := models.GetAccountById(rec.AccountID)
	if err != nil || account == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	// 优先：秒传请求原文重放（最稳，保留全部原始参数）
	if rec.RapidPayload != "" {
		if fileID, err := replayRapidPayload(ctx, account, rec.RapidPayload, targetFolderID); err == nil {
			_ = db.Db.Model(&CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
				"status":           "restored",
				"restored_at":      time.Now().Unix(),
				"restored_file_id": fileID,
			}).Error
			return &RestoreResult{FileID: fileID, FileName: rec.FileName, RecordID: rec.ID, DriveType: rec.SourceType}, nil
		} else {
			helpers.AppLogger.Warnf("CAS 恢复：请求原文重放失败（%v），回退按哈希恢复", err)
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
	fileID, err := rapidRestore(ctx, account, targetFolderID, manifest)
	if err != nil {
		return nil, err
	}
	_ = db.Db.Model(&CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"status":           "restored",
		"restored_at":      time.Now().Unix(),
		"restored_file_id": fileID,
	}).Error
	return &RestoreResult{FileID: fileID, FileName: rec.FileName, RecordID: rec.ID, DriveType: rec.SourceType}, nil
}

// rapidRestore 按账号类型分发秒传
func rapidRestore(ctx context.Context, account *models.Account, targetFolderID string, m cloud189.CasManifestV2) (string, error) {
	switch account.SourceType {
	case models.SourceTypeCloud189:
		if m.Hashes.FileMd5 == "" || m.Hashes.SliceMd5 == "" {
			return "", fmt.Errorf("天翼秒传缺少 fileMd5/sliceMd5 特征")
		}
		client := account.GetCloud189Client()
		familyID, folderID := resolveRestoreTarget(targetFolderID)
		return client.RapidUpload(ctx, folderID, m.FileName, m.FileSize, m.Hashes.FileMd5, m.Hashes.SliceMd5, familyID)
	case models.SourceTypePan139:
		if m.Hashes.Sha256 == "" {
			return "", fmt.Errorf("移动云盘秒传缺少 sha256 特征")
		}
		client := account.GetPan139Client()
		parentID := targetFolderID
		if parentID == "" {
			parentID = "root"
		}
		fileID, _, rapid, err := client.UploadFile(ctx, parentID, m.FileName, m.FileSize, m.Hashes.Sha256, nil, nil)
		if err != nil {
			return "", err
		}
		if !rapid {
			return "", fmt.Errorf("移动云盘秒传未命中（云端已无该哈希文件），需重新上传源文件")
		}
		return fileID, nil
	case models.SourceTypeQuark:
		if m.Hashes.PreHash == "" {
			return "", fmt.Errorf("夸克秒传缺少 preHash 分块预检特征")
		}
		client := account.GetQuarkClient()
		return client.RapidUpload(ctx, targetFolderID, m.FileName, m.FileSize, m.Hashes.PreHash)
	default:
		return "", fmt.Errorf("该网盘类型暂不支持秒传恢复：%s", account.SourceType)
	}
}

// replayRapidPayload 重放秒传请求原文（cloud-auto-save-x 同款恢复策略）
func replayRapidPayload(ctx context.Context, account *models.Account, payload, targetFolderID string) (string, error) {
	var p struct {
		DriveType string         `json:"drive_type"`
		Kind      string         `json:"kind"`
		Name      string         `json:"name"`
		Size      int64          `json:"size"`
		Params    map[string]any `json:"params"`
	}
	if err := jsonUnmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("秒传请求原文解析失败：%v", err)
	}
	switch p.DriveType {
	case "cloud139":
		sha256, _ := p.Params["contentHash"].(string)
		if sha256 == "" {
			sha256, _ = p.Params["content_hash"].(string)
		}
		if sha256 == "" {
			return "", fmt.Errorf("139 请求原文缺 contentHash")
		}
		client := account.GetPan139Client()
		parentID := targetFolderID
		if parentID == "" {
			if v, ok := p.Params["parentFileId"].(string); ok && v != "" {
				parentID = v
			} else {
				parentID = "root"
			}
		}
		fileID, _, rapid, err := client.UploadFile(ctx, parentID, p.Name, p.Size, sha256, nil, nil)
		if err != nil {
			return "", err
		}
		if !rapid {
			return "", fmt.Errorf("139 秒传未命中")
		}
		return fileID, nil
	case "cloud189":
		fileMd5, _ := p.Params["fileMd5"].(string)
		sliceMd5, _ := p.Params["sliceMd5"].(string)
		client := account.GetCloud189Client()
		familyID, folderID := resolveRestoreTarget(targetFolderID)
		return client.RapidUpload(ctx, folderID, p.Name, p.Size, fileMd5, sliceMd5, familyID)
	case "quark":
		preHash, _ := p.Params["pre_hash"].(string)
		client := account.GetQuarkClient()
		return client.RapidUpload(ctx, targetFolderID, p.Name, p.Size, preHash)
	default:
		return "", fmt.Errorf("未知秒传 drive_type：%s", p.DriveType)
	}
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

var _ = quark.NewClient
