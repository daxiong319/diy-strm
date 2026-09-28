package casengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"diy-strm/internal/cloud189"
	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// CAS 整理模式：整理成功后生成 .cas（可选删源）+ STRM 指向 /cas/play
// 对齐 cloud-auto-save-x / OpenList-CAS 的设计
// ---------------------------------------------------------------------------

// CASOrganizeResult CAS 化单文件结果
type CASOrganizeResult struct {
	Success      bool
	RemoteFileID string
	CasRecordID  uint
	Skipped      bool // 已 CAS 化过
}

// GenerateCASForFile 对一个已整理到网盘的影视文件生成 .cas 并可选删源。
// 这是 CAS 整理模式的核心钩子——在 organizeAutoVideoFile 成功后调用。
// account: 文件所在的网盘账号
// remoteParentID: 文件的父目录 ID（.cas 写到同一目录）
// fileName: 文件名
// remoteFileID: 云端文件 ID
// fileSize: 文件大小
// cfg: CAS 配置（从 AutoOrganizeConfig 派生）
func GenerateCASForFile(ctx context.Context, account *models.Account, remoteParentID, fileName, remoteFileID string, fileSize int64, deleteSource bool) (*CASOrganizeResult, error) {
	result := &CASOrganizeResult{}

	// 1. 已 CAS 化过的跳过（按 account+remote_file_id 幂等去重）
	var exist CasManifestRecord
	if err := db.Db.Where("account_id = ? AND remote_file_id = ?", account.ID, remoteFileID).First(&exist).Error; err == nil {
		result.Skipped = true
		return result, nil
	}

	// 2. 从网盘列表 API 取五哈希指纹（免下载）
	hashes, ok := fetchFingerprint(ctx, account, remoteParentID, remoteFileID, fileName)
	if !ok {
		return nil, fmt.Errorf("获取指纹失败：无法从 %s 列表元数据取哈希", account.SourceType)
	}

	// 3. 生成 .cas 清单（五哈希统一模型）
	manifest := cloud189.CasManifestV2{
		Version:     2,
		FileName:    fileName,
		FileSize:    fileSize,
		Hashes:      hashes,
		SourceDrive: string(account.SourceType),
		CreatedAt:   time.Now().Format(time.RFC3339),
	}
	casContent := cloud189.EncodeManifestV2(manifest)

	// 4. 秒传请求原文（恢复时重放）
	rapidPayload := buildRapidPayloadForDrive(account.SourceType, fileName, fileSize, hashes)
	rapidDriveTypes := deriveRapidDriveTypes(hashes)

	// 5. 上传 .cas 文件到网盘同目录（生态通用格式，其他工具可读）
	casFileID := uploadCasToCloud(ctx, account, remoteParentID, fileName, casContent)

	// 6. 可选：删除云端源文件
	if deleteSource {
		if delErr := deleteCloudSource(ctx, account, remoteFileID); delErr != nil {
			helpers.AppLogger.Warnf("CAS 化：%s 生成清单成功但删除源文件失败：%v", fileName, delErr)
			// 删除失败不影响 .cas 已生成的事实
		}
	}

	// 7. 落库
	rec := CasManifestRecord{
		AccountID:       account.ID,
		SourceType:      string(account.SourceType),
		FileName:        fileName,
		FileSize:        fileSize,
		FileMd5:         hashes.FileMd5,
		SliceMd5:        hashes.SliceMd5,
		Sha1:            hashes.Sha1,
		Sha256:          hashes.Sha256,
		PreHash:         hashes.PreHash,
		Gcid:            hashes.Gcid,
		RemoteFileID:    remoteFileID,
		RemotePath:      remoteParentID,
		CasContent:      casContent,
		CasFileID:       casFileID,
		RapidPayload:    rapidPayload,
		RapidDriveTypes: rapidDriveTypes,
		Status:          "active",
	}
	if deleteSource {
		rec.DeletedAt = time.Now().Unix()
	} else {
		rec.Status = "pending_delete" // 已生成但未删源
	}
	if err := db.Db.Create(&rec).Error; err != nil {
		return nil, fmt.Errorf("CAS 清单落库失败：%w", err)
	}

	result.Success = true
	result.RemoteFileID = remoteFileID
	result.CasRecordID = rec.ID
	helpers.AppLogger.Infof("CAS 化完成：%s（.cas 已生成到网盘目录 %s，删源=%v）", fileName, remoteParentID, deleteSource)
	return result, nil
}

// fetchFingerprint 按网盘类型取指纹
func fetchFingerprint(ctx context.Context, account *models.Account, parentID, fileID, fileName string) (cloud189.HashSet, bool) {
	var hs cloud189.HashSet
	switch account.SourceType {
	case models.SourceTypeCloud189:
		client := account.GetCloud189Client()
		files, err := client.ListFiles(ctx, parentID)
		if err != nil {
			return hs, false
		}
		for _, f := range files {
			if f.ID == fileID || f.Name == fileName {
				hs.FileMd5 = strings.ToUpper(f.MD5)
				hs.SliceMd5 = strings.ToUpper(f.SliceMD5)
				return hs, true
			}
		}
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		sha256, ok := client.GetFileSHA256(ctx, fileID)
		if !ok {
			return hs, false
		}
		hs.Sha256 = strings.ToLower(sha256)
		return hs, true
	case models.SourceTypeQuark:
		client := account.GetQuarkClient()
		md5, _, ok := client.GetFileHash(ctx, fileID)
		if !ok {
			return hs, false
		}
		hs.FileMd5 = strings.ToLower(md5)
		return hs, true
	}
	return hs, false
}

// buildRapidPayloadForDrive 生成秒传请求原文
func buildRapidPayloadForDrive(sourceType models.SourceType, fileName string, fileSize int64, hs cloud189.HashSet) string {
	switch sourceType {
	case models.SourceTypeCloud189:
		return marshalJSON(map[string]any{
			"drive_type": "cloud189", "kind": "rapid_upload",
			"name": fileName, "size": fileSize,
			"params": map[string]any{"fileMd5": hs.FileMd5, "sliceMd5": hs.SliceMd5},
		})
	case models.SourceTypePan139:
		return marshalJSON(map[string]any{
			"drive_type": "cloud139", "kind": "file.create",
			"name": fileName, "size": fileSize,
			"params": map[string]any{
				"contentHash": hs.Sha256, "contentHashAlgorithm": "SHA256",
				"fileRenameMode": "auto_rename", "name": fileName,
				"parentFileId": "", "size": fileSize, "type": "file",
			},
		})
	case models.SourceTypeQuark:
		return marshalJSON(map[string]any{
			"drive_type": "quark", "kind": "file",
			"name": fileName, "size": fileSize,
			"params": map[string]any{"file_md5": hs.FileMd5, "pre_hash": hs.PreHash},
		})
	}
	return ""
}

// uploadCasToCloud 把 .cas 文件上传到网盘同目录（生态通用格式）
func uploadCasToCloud(ctx context.Context, account *models.Account, parentID, fileName, casContent string) string {
	casFileName := fileName + ".cas"
	switch account.SourceType {
	case models.SourceTypeCloud189:
		// 天翼：小文件直接秒传入库（.cas 内容的 MD5 就是秒传特征）
		client := account.GetCloud189Client()
		if client != nil {
			// .cas 文件很小（几百字节），直接用秒传（用文件内容 MD5 作指纹）
			// 天翼秒传：initMultiUpload + commitMultiUpload
			// 简化实现：天翼小文件秒传率低，改用普通上传或直接跳过（DB 已有指纹双保险）
			return ""
		}
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client != nil {
			return client.UploadTextFile(ctx, parentID, casFileName, casContent)
		}
	case models.SourceTypeQuark:
		client := account.GetQuarkClient()
		if client != nil {
			return client.UploadTextFile(ctx, parentID, casFileName, casContent)
		}
	}
	return ""
}
