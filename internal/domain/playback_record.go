package domain

import "context"

// PlaybackRecord 一条 Emby 302 反代播放记录（对齐老版
// internal/models/emby_playback_record.go）。字段与前端 DTO 一一对应，
// 由 internal/store/playback_record_repo.go 落主库 playback_records 表。
type PlaybackRecord struct {
	ID         int64  `json:"id"`
	RuleID     string `json:"rule_id"`     // 反代规则 ID（老版单实例固定 "1"）
	UserID     string `json:"user_id"`     // Emby UserId（播放请求 query）
	Client     string `json:"client"`      // 播放端 Client 标识
	DeviceID   string `json:"device_id"`   // 播放设备 ID
	ItemName   string `json:"item_name"`   // 媒体文件名（STRM path 基名）
	StrmPath   string `json:"strm_path"`   // 完整云盘路径
	Provider   string `json:"provider"`    // 网盘标识（115/123/guangya/baidu/139/openlist）
	PlaybackAt string `json:"playback_at"` // 播放时间（RFC3339，UTC）
}

// PlaybackRecordQuery 播放记录分页查询条件。空字段表示该维度不限。
type PlaybackRecordQuery struct {
	RuleID   string
	UserID   string
	Keyword  string
	Provider string
	Page     int
	PageSize int
}

// PlaybackRecordStats 播放记录概览统计。
type PlaybackRecordStats struct {
	Total     int64  `json:"total"`
	LastAt    string `json:"last_at"`
	UserCount int64  `json:"user_count"`
	ItemCount int64  `json:"item_count"`
}

// PlaybackRecordRepository 持久化 Emby 302 播放记录。
// 读取走主库读连接池，写入走写连接池，避免与主库写连接争用。
type PlaybackRecordRepository interface {
	// Insert 落一条播放记录，返回自增 ID。
	Insert(ctx context.Context, rec *PlaybackRecord) (int64, error)
	// List 分页查询，按播放时间倒序，返回当前页与命中总数。
	List(ctx context.Context, q PlaybackRecordQuery) ([]PlaybackRecord, int64, error)
	// Delete 按 ID 删除一条，不存在时返回 nil（幂等）。
	Delete(ctx context.Context, id int64) error
	// Clear 按条件清空，返回删除条数；ruleID/userID 均为空时清空全部。
	Clear(ctx context.Context, ruleID, userID string) (int64, error)
	// Stats 返回概览统计。
	Stats(ctx context.Context) (PlaybackRecordStats, error)
}
