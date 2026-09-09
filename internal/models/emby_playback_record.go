package models

import (
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
)

// EmbyPlaybackRecord Emby 302 反代播放记录（对齐 tgto123 playback_record 语义）：
// 每次播放重定向成功落一条，供「播放记录」面板分页查询。
type EmbyPlaybackRecord struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	RuleID     string    `gorm:"size:32;index;default:1" json:"rule_id"` // 反代规则 ID（当前单实例固定 1）
	UserID     string    `gorm:"size:64;index" json:"user_id"`           // Emby UserId（播放请求 query）
	Client     string    `gorm:"size:128" json:"client"`                 // 播放端 Client 标识
	DeviceID   string    `gorm:"size:128" json:"device_id"`              // 播放设备 ID
	ItemName   string    `gorm:"size:512" json:"item_name"`              // 媒体文件名（STRM path 基名）
	StrmPath   string    `gorm:"type:text" json:"strm_path"`             // 完整云盘路径
	Provider   string    `gorm:"size:32" json:"provider"`                // 网盘标识（115/123/guangya/baidu/139/openlist）
	PlaybackAt time.Time `gorm:"index" json:"playback_at"`               // 播放时间
}

func (EmbyPlaybackRecord) TableName() string { return "emby_playback_records" }

// AddEmbyPlaybackRecord 落一条播放记录（失败仅记日志不阻塞播放）
func AddEmbyPlaybackRecord(record *EmbyPlaybackRecord) {
	if record == nil {
		return
	}
	if record.PlaybackAt.IsZero() {
		record.PlaybackAt = time.Now()
	}
	if record.RuleID == "" {
		record.RuleID = "1"
	}
	if err := db.Db.Create(record).Error; err != nil {
		helpers.AppLogger.Warnf("播放记录落库失败：%v", err)
	}
}

// ListEmbyPlaybackRecords 分页查询播放记录（ruleId 为空查全部）
func ListEmbyPlaybackRecords(ruleID string, page, pageSize int) ([]EmbyPlaybackRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 30
	}
	query := db.Db.Model(&EmbyPlaybackRecord{})
	if ruleID != "" {
		query = query.Where("rule_id = ?", ruleID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []EmbyPlaybackRecord
	if err := query.Order("playback_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// EnsureEmbyPlaybackRecordTable 确保播放记录表存在（main 启动时调用）
func EnsureEmbyPlaybackRecordTable() error {
	return db.Db.AutoMigrate(&EmbyPlaybackRecord{})
}
