package eventbus

import "time"

// 跨模块事件契约；发布者不感知订阅者。

// AccountAuthFailed 账号认证失败。Fatal 表示需要用户重新授权而非自动恢复。
type AccountAuthFailed struct {
	AccountID int64
	Reason    string
	Fatal     bool
}

// AccountAuthRecovered 账号认证恢复可用。
type AccountAuthRecovered struct {
	AccountID int64
}

// FileMutated 文件发生增删改，用于触发缓存失效等。
type FileMutated struct {
	AccountID   int64
	Op          string // create/delete/move/rename/copy/upload
	ParentID    string
	FileID      string
	FileName    string
	FileSize    int64
	IsDir       bool
	ModTime     time.Time
	FileIDs     []string
	OldParentID string
}

// OfflineDownloadCompleted 离线下载任务首次进入成功状态。
type OfflineDownloadCompleted struct {
	TaskID            string
	AccountID         int64
	TargetParentID    string
	TargetDisplayPath string
	FileID            string
	FileName          string
}

// CasAutoSaved CAS 清单经通知渠道收到后已成功转存到网盘目录。
//
// 与 OfflineDownloadCompleted 同理，这是「把资源放进待整理目录」这一动作
// 的完成信号：自动化据此触发整理 → STRM → Emby 扫库 → 入库通知的联动。
type CasAutoSaved struct {
	AccountID int64
	DriveType string // cloud189 / quark / 123_open ...
	SaveDir   string // 相对网盘根目录，如 影视/CAS待整理
	FileName  string // 落盘后的文件名（含 .cas 后缀）
	FileID    string
	Source    string // 用户实际发来的文件名（可能是压缩包名）
}

// NotificationCreated 新通知产生。
type NotificationCreated struct {
	Level     string
	Category  string
	Title     string
	Message   string
	AccountID int64
	RefID     int64
}
