package cas

import (
	"time"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/ddb"
)

// LocalManifestResult 本地文件生成 CAS 清单的结果（供 API 直接回给前端）。
type LocalManifestResult struct {
	ID              uint             `json:"id"`
	FileName        string           `json:"file_name"`
	FileSize        int64            `json:"file_size"`
	Hashes          cloud189.HashSet `json:"hashes"`
	RapidDriveTypes string           `json:"rapid_drive_types"`
	CasContent      string           `json:"cas_content"`
	// Restorable 表示该记录是否指定了真实目标盘账号+类型（可走秒传恢复）。
	Restorable bool `json:"restorable"`
}

// LocalManifestSourceType 本地文件来源的 SourceType 标记。
// 恢复时必须靠 AccountID 指定真实目标盘账号，故它只是一个溯源标签。
const LocalManifestSourceType = "local"

// BuildLocalManifest 用本地算出的五哈希构造 .cas 清单并落库，不触碰任何网盘。
//
// 与 GenerateCASForFile 的区别：源是本地磁盘文件，没有账号/远端文件，
// 因此 UploadTaskID 恒为 0、RemoteFileID/RemotePath 留空、CasFileID 留空
// （不回写网盘），Status 固定 "active"（本地源文件不删除，无待删源状态）。
//
// ★ targetDriveType 必须是真实网盘类型（如 "123_open"/"cloud189"/"cloud139"），
// 不能是 "local"：restore 路径 RestoreFromRecord(restore.go:59) 用 driverResolver(
// accountID, sourceType) 取驱动，而 NormalizeDriveType("local") → "localfs"
// （autosave.go:104）没有任何驱动实现，会让记录永远无法恢复。
// accountID 同理必须是真实账号 ID，否则 driverResolver 取不到账号。
// 两者都为 0/空时仅生成一份「可携带」清单，不承诺可恢复。
func BuildLocalManifest(fileName string, fileSize int64, hashes cloud189.HashSet, accountID int64, targetDriveType string) (*LocalManifestResult, error) {
	driveType := NormalizeDriveType(targetDriveType)
	if driveType == "" || driveType == "localfs" {
		// 未指定目标盘：不生成 rapid payload（重放无意义），但仍落库为可携带清单。
		driveType = ""
	}
	manifest := cloud189.CasManifestV2{
		Version:     2,
		FileName:    fileName,
		FileSize:    fileSize,
		Hashes:      hashes,
		SourceDrive: LocalManifestSourceType,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}
	casContent := cloud189.EncodeManifestV2(manifest)
	rapidPayload := ""
	if driveType != "" {
		rapidPayload = BuildRapidPayloadFor(driveType, fileName, fileSize, hashes)
		// ★ 防御：BuildRapidPayloadFor（engine.go:184）对没有分支的盘返回空串。
		// 若照常落库，就会出现「SourceType/AccountID 看着可恢复、RapidPayload 实为空
		// 被 restore.go:64 的 `if rec.RapidPayload != ""` 静默跳过重放」的死记录。
		// 此处直接降级为不可恢复，由上层拒绝或明确告知。
		if rapidPayload == "" {
			driveType = ""
		}
	}
	rapidDriveTypes := DeriveRapidDriveTypes(hashes)

	rec := CasManifestRecord{
		// UploadTaskID 恒为 0：本地文件不来自上传任务。
		UploadTaskID: 0,
		// AccountID 必须是真实账号，否则恢复时 driverResolver 取不到驱动。
		AccountID: accountID,
		// SourceType 用真实网盘类型，保证 restore 能解析到驱动；"local" 无法恢复。
		SourceType:      driveType,
		FileName:        fileName,
		FileSize:        fileSize,
		FileMd5:         hashes.FileMd5,
		SliceMd5:        hashes.SliceMd5,
		Sha1:            hashes.Sha1,
		Sha256:          hashes.Sha256,
		PreHash:         hashes.PreHash,
		Gcid:            hashes.Gcid,
		RemoteFileID:    "",
		RemotePath:      "",
		CasContent:      casContent,
		CasFileID:       "",
		RapidPayload:    rapidPayload,
		RapidDriveTypes: rapidDriveTypes,
		Status:          "active",
	}
	if err := ddb.Db.Create(&rec).Error; err != nil {
		return nil, err
	}
	return &LocalManifestResult{
		ID:              rec.ID,
		FileName:        rec.FileName,
		FileSize:        rec.FileSize,
		Hashes:          hashes,
		RapidDriveTypes: rapidDriveTypes,
		CasContent:      casContent,
		// 恢复能力说明：只有指定了真实目标盘账号+类型才可恢复。
		Restorable: driveType != "" && accountID > 0,
	}, nil
}
