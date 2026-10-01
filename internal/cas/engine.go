// Package cas 移植 diy-strm 的 CAS 秒传体系：五哈希统一指纹 + .cas 清单 + 秒传请求原文。
// 网盘相关操作（取指纹/删文件/上传/秒传重放）抽象为 RapidDriver 接口，由 LitePan 驱动适配实现。
package cas

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"litepan/internal/cas/cloud189"
	"litepan/internal/discover/ddb"
	"litepan/internal/discover/dutil"
)

// CasManifestRecord CAS 清单记录（五哈希统一模型 + 秒传请求原文缓存）
type CasManifestRecord struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	UploadTaskID    int64     `gorm:"index" json:"upload_task_id"`
	AccountID       int64     `gorm:"index" json:"account_id"`
	SourceType      string    `json:"source_type"`
	FileName        string    `gorm:"size:512" json:"file_name"`
	FileSize        int64     `json:"file_size"`
	FileMd5         string    `gorm:"size:32" json:"file_md5"`
	SliceMd5        string    `gorm:"size:64" json:"slice_md5"`
	Sha1            string    `gorm:"size:40" json:"sha1"`
	Sha256          string    `gorm:"size:64" json:"sha256"`
	PreHash         string    `gorm:"size:256" json:"pre_hash"` // 夸克 4×4MB 分块 MD5 预检串
	Gcid            string    `gorm:"size:128" json:"gcid"`
	RemoteFileID    string    `gorm:"size:128" json:"remote_file_id"`
	RemotePath      string    `gorm:"size:1024" json:"remote_path"`
	CasContent      string    `gorm:"type:text" json:"cas_content"`
	RapidPayload    string    `gorm:"type:text" json:"rapid_payload"`
	RapidDriveTypes string    `gorm:"size:256" json:"rapid_drive_types"`
	CasFileID       string    `gorm:"size:128" json:"cas_file_id"`
	Status          string    `gorm:"size:32" json:"status"` // active(源已删)/restored(已恢复)/pending
	DeletedAt       int64     `json:"deleted_at"`
	RestoredAt      int64     `json:"restored_at"`
	RestoredFileID  string    `gorm:"size:128" json:"restored_file_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (CasManifestRecord) TableName() string { return "cas_manifests" }

// RapidRecord 秒传请求原文缓存表
type RapidRecord struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	DriveType     string    `gorm:"index:idx_rapid_drive_file,unique" json:"drive_type"`
	FileID        string    `gorm:"size:128;index:idx_rapid_drive_file,unique" json:"file_id"`
	Size          int64     `json:"size"`
	FileMd5       string    `gorm:"size:32" json:"file_md5"`
	SliceMd5      string    `gorm:"size:64" json:"slice_md5"`
	Sha1          string    `gorm:"size:40" json:"sha1"`
	Sha256        string    `gorm:"size:64" json:"sha256"`
	PreHash       string    `gorm:"size:256" json:"pre_hash"`
	Gcid          string    `gorm:"size:128" json:"gcid"`
	Base64Payload string    `gorm:"type:text" json:"base64_payload"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (RapidRecord) TableName() string { return "cas_rapid_records" }

// EnsureTable 建表（幂等）
func EnsureTable() {
	_ = ddb.Db.AutoMigrate(&CasManifestRecord{})
	_ = ddb.Db.AutoMigrate(&RapidRecord{})
}

// CasConfig CAS 自动化配置
type CasConfig struct {
	Enabled          bool `json:"enabled"`
	AgeDays          int  `json:"age_days"`
	DeleteSource     bool `json:"delete_source"`
	DelayDeleteHours int  `json:"delay_delete_hours"` // 播放恢复后延时删除临时文件（小时，默认 2；0=不自动删除）
	WriteBackCloud   bool `json:"write_back_cloud"`

	// 通知渠道 CAS 自动转存：收到用户发来的 .cas 文件后自动保存到下列目录。
	CASNotifyAutoSave       bool                           `json:"cas_notify_auto_save"`
	CASNotifyAutoSaveDir    string                         `json:"cas_notify_auto_save_dir"`
	CASNotifyAutoSaveDrives map[string]AutoSaveDriveConfig `json:"cas_notify_auto_save_drives,omitempty"`
}

// configStore/configSetter 配置读写（由装配层注入；默认空实现，未接 settings 前不持久化）。
// CAS 配置统一存 discovery_settings 表的 cas_engine_config 键。
var configStore = func(key string) (string, bool) { return "", false }
var configSetter = func(key, value string) {}

const configKey = "cas_engine_config"

// BindConfigStore 注入配置读写（通常桥接到 discovery_settings 或 LitePan settings）。
func BindConfigStore(get func(key string) (string, bool), set func(key, value string)) {
	if get != nil {
		configStore = get
	}
	if set != nil {
		configSetter = set
	}
}

func getConfig() CasConfig {
	cfg := CasConfig{
		Enabled:                 true,
		AgeDays:                 30,
		DeleteSource:            true,
		DelayDeleteHours:        2,
		WriteBackCloud:          false,
		CASNotifyAutoSave:       true,
		CASNotifyAutoSaveDir:    "CAS",
		CASNotifyAutoSaveDrives: map[string]AutoSaveDriveConfig{},
	}
	if v, ok := configStore(configKey); ok && v != "" {
		_ = json.Unmarshal([]byte(v), &cfg)
	}
	if cfg.AgeDays <= 0 {
		cfg.AgeDays = 30
	}
	if cfg.DelayDeleteHours < 0 {
		cfg.DelayDeleteHours = 0
	}
	normalizeAutoSave(&cfg)
	return cfg
}

// normalizeAutoSave 归一 CAS 自动转存配置（去空白/斜杠、初始化 map）。
func normalizeAutoSave(cfg *CasConfig) {
	cfg.CASNotifyAutoSaveDir = strings.Trim(strings.TrimSpace(cfg.CASNotifyAutoSaveDir), "/")
	if cfg.CASNotifyAutoSaveDrives == nil {
		cfg.CASNotifyAutoSaveDrives = map[string]AutoSaveDriveConfig{}
	}
	normalized := make(map[string]AutoSaveDriveConfig, len(cfg.CASNotifyAutoSaveDrives))
	for k, v := range cfg.CASNotifyAutoSaveDrives {
		key := NormalizeDriveType(k)
		if key == "" {
			continue
		}
		v.SaveDir = strings.Trim(strings.TrimSpace(v.SaveDir), "/")
		normalized[key] = v
	}
	cfg.CASNotifyAutoSaveDrives = normalized
}

// GetConfigForAPI 配置读取（API 用）
func GetConfigForAPI() CasConfig { return getConfig() }

// SaveConfig 配置保存
func SaveConfig(cfg CasConfig) {
	if cfg.AgeDays <= 0 {
		cfg.AgeDays = 30
	}
	if cfg.DelayDeleteHours < 0 {
		cfg.DelayDeleteHours = 0
	}
	normalizeAutoSave(&cfg)
	raw, _ := json.Marshal(cfg)
	configSetter(configKey, string(raw))
}

// ---------------------------------------------------------------------------
// 网盘无关纯逻辑：五哈希 → 可秒传盘 / 秒传 payload
// ---------------------------------------------------------------------------

// DeriveRapidDriveTypes 由五哈希非空字段推导可秒传到的盘
func DeriveRapidDriveTypes(hs cloud189.HashSet) string {
	types := make([]string, 0, 4)
	if hs.FileMd5 != "" || hs.SliceMd5 != "" {
		types = append(types, "cloud189")
	}
	if hs.Sha256 != "" {
		types = append(types, "cloud139")
	}
	if hs.FileMd5 != "" && hs.PreHash != "" {
		types = append(types, "quark")
	}
	if hs.FileMd5 != "" {
		types = append(types, "123_open")
	}
	return strings.Join(types, ",")
}

// BuildRapidPayloadFor 生成秒传请求原文（cloud-auto-save-x 同款：恢复时重放）
func BuildRapidPayloadFor(sourceType, fileName string, fileSize int64, hs cloud189.HashSet) string {
	switch strings.ToLower(sourceType) {
	case "189cloud", "cloud189":
		return marshalJSON(map[string]any{
			"drive_type": "cloud189", "kind": "rapid_upload",
			"name": fileName, "size": fileSize,
			"params": map[string]any{"fileMd5": hs.FileMd5, "sliceMd5": hs.SliceMd5},
		})
	case "139cloud", "pan139", "cloud139":
		return marshalJSON(map[string]any{
			"drive_type": "cloud139", "kind": "file.create",
			"name": fileName, "size": fileSize,
			"params": map[string]any{
				"contentHash": hs.Sha256, "contentHashAlgorithm": "SHA256",
				"fileRenameMode": "auto_rename", "name": fileName,
				"parentFileId": "", "size": fileSize, "type": "file",
			},
		})
	case "quark":
		// ★ 夸克秒传（drivers/Quark/upload.go:696 RapidUploadByHashes）同时需要 md5 与 sha1，
		// 缺任一直接报「夸克秒传缺少 md5/sha1 特征」。preHash 在驱动里从不被读取
		// （pathUpdateHash 只发 md5+sha1），故这里补上 sha1，preHash 保留仅为兼容旧记录。
		return marshalJSON(map[string]any{
			"drive_type": "quark", "kind": "file",
			"name": fileName, "size": fileSize,
			"params": map[string]any{
				"file_md5": hs.FileMd5, "pre_hash": hs.PreHash, "sha1": hs.Sha1,
			},
		})
	case "123", "123_open", "123open":
		// 123 云盘开放平台秒传特征 = 全量 MD5（Etag）
		return marshalJSON(map[string]any{
			"drive_type": "123_open", "kind": "file",
			"name": fileName, "size": fileSize,
			"params": map[string]any{"md5": hs.FileMd5},
		})
	}
	return ""
}

func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------------------
// RapidDriver 网盘秒传适配接口（由 LitePan 驱动实现）
// ---------------------------------------------------------------------------

// RapidDriver 抽象 CAS 所需的网盘操作，解耦老 diy-strm 网盘客户端。
type RapidDriver interface {
	// SourceType 返回驱动类型标识（cloud189/cloud139/quark）
	SourceType() string
	// FetchFingerprint 免下载取五哈希指纹（189:fileMd5+sliceMd5 / 139:sha256 / 夸克:md5+preHash）
	FetchFingerprint(ctx context.Context, parentID, fileID, fileName string) (cloud189.HashSet, bool)
	// ProbeFile 探测文件是否在云端目录存在（优先按 fileID，后备按 parentID+fileName），存在返回 (true, actualFileID)
	ProbeFile(ctx context.Context, parentID, fileID, fileName string) (bool, string)
	// DeleteFile 删除云端文件
	DeleteFile(ctx context.Context, fileID string) error
	// UploadTextFile 上传文本文件（.cas 清单写回网盘），返回文件 ID
	UploadTextFile(ctx context.Context, parentID, fileName, content string) (string, error)
	// ReplayRapid 重放秒传请求原文，返回恢复出的文件 ID
	ReplayRapid(ctx context.Context, payload, targetFolderID string) (string, error)
}

// driverResolver 由装配层注入：按账号 ID + sourceType 取网盘适配器
var driverResolver = func(accountID int64, sourceType string) RapidDriver { return nil }

// BindDriverResolver 注入驱动解析器
func BindDriverResolver(resolver func(accountID int64, sourceType string) RapidDriver) {
	if resolver != nil {
		driverResolver = resolver
	}
}

// ---------------------------------------------------------------------------
// CAS 链接联动：生成 .cas 后把对应的老 .strm 改写成 CAS 播放直链
// （播放 → 读 cas strm → 秒传恢复 → 302）
// ---------------------------------------------------------------------------

// StrmLinker 由装配层注入：把指向 sourceFileID 的 .strm 首行改写成 CAS 播放直链。
// 返回 matched（命中的 .strm 数量）与 updated（实际写盘数量）。
type StrmLinker func(sourceFileID string, recordID uint, fileName string) (matched, updated int, err error)

// strmLinker 默认空实现：未注入时不影响 CAS 化主流程。
var strmLinker StrmLinker = func(string, uint, string) (int, int, error) { return 0, 0, nil }

// BindStrmLinker 注入 STRM 改写器（由装配层提供 STRM 根目录配置）。
func BindStrmLinker(linker StrmLinker) {
	if linker != nil {
		strmLinker = linker
	}
}

// linkCASStrm 在 CAS 记录落库后联动改写老 .strm。失败只记日志，不阻断 CAS 化。
func linkCASStrm(rec *CasManifestRecord) {
	if rec == nil || strmLinker == nil {
		return
	}
	matched, updated, err := strmLinker(rec.RemoteFileID, rec.ID, rec.FileName)
	if err != nil {
		dutil.AppLogger.Warnf("CAS 化：%s 联动 STRM 失败：%v", rec.FileName, err)
		return
	}
	if matched > 0 {
		dutil.AppLogger.Infof("CAS 化：%s 联动 STRM（命中 %d，改写 %d）", rec.FileName, matched, updated)
	} else {
		dutil.AppLogger.Infof("CAS 化：%s 未找到对应 STRM，跳过联动（可稍后执行 STRM 任务生成）", rec.FileName)
	}
}

// ---------------------------------------------------------------------------
// CAS 生成（GenerateCASForFile：整理后处理钩子，网盘无关骨架）
// ---------------------------------------------------------------------------

// CASOrganizeResult CAS 化单文件结果
type CASOrganizeResult struct {
	Success      bool   `json:"success"`
	RemoteFileID string `json:"remote_file_id"`
	CasRecordID  int64  `json:"cas_record_id"`
	Skipped      bool   `json:"skipped"`
}

// GenerateCASForFile 对一个已整理到网盘的影视文件生成 .cas 并可选删源。
// driver 为对应网盘的适配器（由调用方/装配层解析）。
func GenerateCASForFile(ctx context.Context, accountID int64, sourceType, remoteParentID, fileName, remoteFileID string, fileSize int64, deleteSource bool) (*CASOrganizeResult, error) {
	result := &CASOrganizeResult{}

	// 1. 已 CAS 化过跳过（account+remote_file_id 幂等）
	var exist CasManifestRecord
	if err := ddb.Db.Where("account_id = ? AND remote_file_id = ?", accountID, remoteFileID).First(&exist).Error; err == nil {
		result.Skipped = true
		return result, nil
	}

	// 2. 取五哈希
	driver := driverResolver(accountID, sourceType)
	if driver == nil {
		return nil, errNoDriver(sourceType)
	}
	hashes, ok := driver.FetchFingerprint(ctx, remoteParentID, remoteFileID, fileName)
	if !ok {
		return nil, &CasError{Op: "fetch_fingerprint", Drive: sourceType}
	}

	// 3. 生成 .cas 清单
	manifest := cloud189.CasManifestV2{
		Version:     2,
		FileName:    fileName,
		FileSize:    fileSize,
		Hashes:      hashes,
		SourceDrive: sourceType,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}
	casContent := cloud189.EncodeManifestV2(manifest)
	rapidPayload := BuildRapidPayloadFor(sourceType, fileName, fileSize, hashes)
	rapidDriveTypes := DeriveRapidDriveTypes(hashes)

	// 4. 可选写回网盘 .cas（失败不影响）
	casFileID := ""
	if cid, uerr := driver.UploadTextFile(ctx, remoteParentID, fileName+".cas", casContent); uerr == nil {
		casFileID = cid
	}

	// 5. 可选删源
	if deleteSource {
		if derr := driver.DeleteFile(ctx, remoteFileID); derr != nil {
			// 删除失败不影响 .cas 已生成
			_ = derr
		}
	}

	// 6. 落库
	rec := CasManifestRecord{
		AccountID:       accountID,
		SourceType:      sourceType,
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
		rec.Status = "pending_delete"
	}
	if err := ddb.Db.Create(&rec).Error; err != nil {
		return nil, &CasError{Op: "persist", Drive: sourceType, Err: err}
	}
	// 联动：把指向源文件的 .strm 改写成 CAS 播放直链，打通"播放 → 秒传恢复 → 302"。
	linkCASStrm(&rec)
	result.Success = true
	result.RemoteFileID = remoteFileID
	result.CasRecordID = int64(rec.ID)
	return result, nil
}

// CasError CAS 操作错误
type CasError struct {
	Op    string
	Drive string
	Err   error
}

func (e *CasError) Error() string {
	if e.Err != nil {
		return "CAS " + e.Op + " [" + e.Drive + "]: " + e.Err.Error()
	}
	return "CAS " + e.Op + " [" + e.Drive + "]"
}

func errNoDriver(drive string) error {
	return &CasError{Op: "resolve_driver", Drive: drive}
}
