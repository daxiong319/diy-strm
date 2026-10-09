package backuprestore

import "time"

const (
	FormatVersion = 1
	ScopeSettings = "settings"
	ScopeFull     = "full"

	StateIdle            = "idle"
	StateWaitingRestart  = "waiting_restart"
	StateRestoreSuccess  = "restore_success"
	StateRestoreRollback = "restore_rollback"
)

type KDFManifest struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	Salt      string `json:"salt"`
}

// Manifest 是 .lpb 公开头，不包含凭据和设置值。
type Manifest struct {
	FormatVersion int         `json:"format_version"`
	BackupID      string      `json:"backup_id"`
	AppVersion    string      `json:"app_version"`
	SchemaVersion int         `json:"schema_version"`
	CreatedAt     string      `json:"created_at"`
	Note          string      `json:"note,omitempty"`
	Scope         string      `json:"scope"`
	Components    []string    `json:"components"`
	AccountCount  int         `json:"account_count,omitempty"`
	TaskCount     int         `json:"task_count,omitempty"`
	KDF           KDFManifest `json:"kdf"`
	NoncePrefix   string      `json:"nonce_prefix"`
	ChunkSize     int         `json:"chunk_size"`
	PlainSize     int64       `json:"plain_size"`
	EncryptedSize int64       `json:"encrypted_size"`
	PayloadSHA256 string      `json:"payload_sha256"`
}

type Record struct {
	ID            string   `json:"id"`
	BackupID      string   `json:"backup_id"`
	AppVersion    string   `json:"app_version"`
	SchemaVersion int      `json:"schema_version"`
	CreatedAt     string   `json:"created_at"`
	Note          string   `json:"note"`
	Scope         string   `json:"scope"`
	Components    []string `json:"components"`
	AccountCount  int      `json:"account_count"`
	TaskCount     int      `json:"task_count"`
	Size          int64    `json:"size"`
}

type CreateRequest struct {
	Note            string `json:"note"`
	Password        string `json:"password"`
	IncludeAccounts bool   `json:"include_accounts"`
}

type RestoreRequest struct {
	Password     string `json:"password"`
	RestoreAdmin bool   `json:"restore_admin"`
	// STRMTarget 说清 STRM 目录恢复到哪（nil 等价于 Local）。
	STRMTarget *RestoreTarget `json:"strm_target,omitempty"`
}

type Summary struct {
	Record        Record `json:"record"`
	AccountCount  int    `json:"account_count"`
	TaskCount     int    `json:"task_count"`
	RestoreAdmin  bool   `json:"restore_admin"`
	NeedsRestart  bool   `json:"needs_restart"`
	SecretFromEnv bool   `json:"secret_from_env"`
	// STRMCount 是这次恢复落到 STRM 目录里的文件数（0 = 备份里没有 STRM）。
	STRMCount int `json:"strm_count"`
	// STRMTarget 说明 STRM 恢复到了哪里（"local" / "cloud"）。
	STRMTarget string `json:"strm_target,omitempty"`
	// STRMFailures 是恢复 STRM 时失败的条目。整体不失败，但如实列出，
	// 因为「恢复完成但少了几集」比「恢复失败」更难被用户自己发现。
	STRMFailures []string `json:"strm_failures,omitempty"`
}

type Status struct {
	State        string `json:"state"`
	Message      string `json:"message,omitempty"`
	BackupID     string `json:"backup_id,omitempty"`
	BackupNote   string `json:"backup_note,omitempty"`
	Scope        string `json:"scope,omitempty"`
	RestoreAdmin bool   `json:"restore_admin,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type payloadFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type payloadManifest struct {
	FormatVersion int           `json:"format_version"`
	Scope         string        `json:"scope"`
	SchemaVersion int           `json:"schema_version"`
	AccountCount  int           `json:"account_count"`
	TaskCount     int           `json:"task_count"`
	StrmCount     int           `json:"strm_count"`
	Files         []payloadFile `json:"files"`
}

type pendingPlan struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	SourceID      string `json:"source_id"`
	BackupNote    string `json:"backup_note,omitempty"`
	Scope         string `json:"scope"`
	RestoreAdmin  bool   `json:"restore_admin"`
	StageDir      string `json:"stage_dir"`
	ReplaceSecret bool   `json:"replace_secret"`
	ReplaceFavs   bool   `json:"replace_favorites"`
	// STRMRestore 记着 STRM 目录要恢复到哪；STRMStaged 是解出来的暂存目录名。
	// 两者都空表示这次备份没有 STRM 目录，或恢复已在 PrepareRestore 阶段完成。
	STRMTarget *RestoreTarget `json:"strm_target,omitempty"`
	STRMStaged string         `json:"strm_staged,omitempty"`
	CreatedAt  string         `json:"created_at"`
}

type restoreResult struct {
	State        string `json:"state"`
	Message      string `json:"message"`
	BackupID     string `json:"backup_id"`
	BackupNote   string `json:"backup_note,omitempty"`
	Scope        string `json:"scope"`
	RestoreAdmin bool   `json:"restore_admin"`
	UpdatedAt    string `json:"updated_at"`
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }
