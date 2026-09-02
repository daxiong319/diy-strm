package models

import (
	"strings"
	"time"

	"diy-strm/internal/db"

	"gorm.io/gorm"
)

// 待洗版条目状态（借鉴 mediavault media_upgrade_records.upgrade_status）
const (
	WashStatusPending   = "pending"   // 待洗版（扫描发现不达标）
	WashStatusAbandoned = "abandoned" // 已放弃（用户手动放弃，重新扫描保留）
	WashStatusWashed    = "washed"    // 已洗版替换（条目对应旧文件已不存在时会自动清理）
)

// WashScanItem 违规扫描清单：已整理媒体库中不达标（待洗版）的视频文件。
// 每条对应一个已整理库中的视频文件（整理目录 + 文件名定位）。
type WashScanItem struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	AccountID  uint   `gorm:"index:idx_wash_item_account_path,priority:1" json:"account_id"`
	RelPath    string `gorm:"size:1024;index:idx_wash_item_account_path,priority:2" json:"rel_path"` // 相对已整理根目录的目录路径（不含文件名）
	FileName   string `gorm:"size:512" json:"file_name"`
	MediaType  string `gorm:"size:16" json:"media_type"` // Movie / TV
	Title      string `gorm:"size:256" json:"title"`
	Year       int    `json:"year"`
	SeasonNum  int    `json:"season_num"`
	EpisodeNum int    `json:"episode_num"`
	TMDBID     int64  `json:"tmdb_id"`
	// 质量快照
	Resolution int    `json:"resolution"` // 0/480/720/1080/2160
	ResTag     string `gorm:"size:32" json:"res_tag"`
	Codec      string `gorm:"size:32" json:"codec"` // h265/h264/av1/unknown
	CodecTag   string `gorm:"size:64" json:"codec_tag"`
	AudioTag   string `gorm:"size:128" json:"audio_tag"`
	Channels   int    `json:"channels"`
	// 违规说明（分号分隔，如 "分辨率 720p 低于 1080；编码 H.264 非 HEVC/AV1"）
	Violations string     `gorm:"type:text" json:"violations"`
	Status     string     `gorm:"size:16;default:pending;index:idx_wash_item_status" json:"status"`
	WashedAt   *time.Time `json:"washed_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TableName 表名
func (WashScanItem) TableName() string { return "wash_scan_items" }

// WashLog 洗版动作日志（每次「覆盖/跳过/归档」一条），前端可查可清空
type WashLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	AccountID    uint      `gorm:"index:idx_wash_log_account_time,priority:1" json:"account_id"`
	Action       string    `gorm:"size:24;index" json:"action"`  // wash_replace/wash_no_better/wash_skip/wash_archive/loser_source_deleted/scan_summary/filter_blocked/score_filter/renewal_tip/...
	TargetPath   string    `gorm:"size:1024" json:"target_path"` // 目标相对目录
	Title        string    `gorm:"size:256" json:"title"`
	MediaType    string    `gorm:"size:16" json:"media_type"`
	SeasonNum    int       `json:"season_num"`
	EpisodeNum   int       `json:"episode_num"`
	TMDBID       int64     `json:"tmdb_id"`
	OldName      string    `gorm:"size:512" json:"old_name"`
	OldQuality   string    `gorm:"size:256" json:"old_quality"`
	NewName      string    `gorm:"size:512" json:"new_name"`
	NewQuality   string    `gorm:"size:256" json:"new_quality"`
	LoserTreated string    `gorm:"size:32" json:"loser_treated"` // none/delete/archive/keep_source/deleted_source
	Message      string    `gorm:"type:text" json:"message"`
	EventTime    time.Time `gorm:"index:idx_wash_log_account_time,priority:2" json:"event_time"`
}

// TableName 表名
func (WashLog) TableName() string { return "wash_logs" }

// ---- 查询 helper ----

// UpsertWashItem 按 (account_id, rel_path, file_name) upsert 扫描条目
func UpsertWashItem(item *WashScanItem) error {
	var existing WashScanItem
	err := db.Db.Where("account_id = ? AND rel_path = ? AND file_name = ?", item.AccountID, item.RelPath, item.FileName).First(&existing).Error
	now := time.Now()
	if err == gorm.ErrRecordNotFound {
		item.CreatedAt = now
		item.UpdatedAt = now
		return db.Db.Create(item).Error
	}
	if err != nil {
		return err
	}
	// 已放弃的条目保持放弃状态（用户主动放弃），其余字段刷新；状态为 pending/washed 时回 pending
	if existing.Status != WashStatusAbandoned {
		existing.Status = WashStatusPending
	}
	existing.RelPath = item.RelPath
	existing.FileName = item.FileName
	existing.MediaType = item.MediaType
	existing.Title = item.Title
	existing.Year = item.Year
	existing.SeasonNum = item.SeasonNum
	existing.EpisodeNum = item.EpisodeNum
	existing.TMDBID = item.TMDBID
	existing.Resolution = item.Resolution
	existing.ResTag = item.ResTag
	existing.Codec = item.Codec
	existing.CodecTag = item.CodecTag
	existing.AudioTag = item.AudioTag
	existing.Channels = item.Channels
	existing.Violations = item.Violations
	existing.UpdatedAt = now
	return db.Db.Save(&existing).Error
}

// DeleteWashItemMissing 删除本次扫描未再出现（文件已被替换/删除）的非放弃条目
func DeleteWashItemMissing(accountID uint, seen map[string]bool) error {
	var items []WashScanItem
	if err := db.Db.Where("account_id = ? AND status <> ?", accountID, WashStatusAbandoned).Find(&items).Error; err != nil {
		return err
	}
	for i := range items {
		key := washItemKey(items[i].RelPath, items[i].FileName)
		if seen[key] {
			continue
		}
		if err := db.Db.Delete(&WashScanItem{}, items[i].ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// DeleteWashItemForFile 删除指定文件对应的待洗版条目（文件已达标/已被替换时调用）。
// 用户主动放弃的条目保留（尊重用户意愿）。
func DeleteWashItemForFile(accountID uint, relPath, fileName string) error {
	return db.Db.Where("account_id = ? AND rel_path = ? AND file_name = ? AND status <> ?",
		accountID, relPath, fileName, WashStatusAbandoned).Delete(&WashScanItem{}).Error
}

func washItemKey(relPath, fileName string) string {
	return strings.TrimRight(relPath, "/") + "|" + fileName
}

// SetWashItemStatus 批量设置条目状态（放弃/恢复待洗版）
func SetWashItemStatus(ids []uint, status string) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Db.Model(&WashScanItem{}).Where("id IN ?", ids).
		Updates(map[string]any{"status": status, "updated_at": time.Now()}).Error
}

// ListWashItems 查询待洗版清单（account_id 可选 0=全部；status 可选空=全部）
func ListWashItems(accountID uint, status string) ([]WashScanItem, error) {
	q := db.Db.Order("account_id asc, updated_at desc")
	if accountID > 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var list []WashScanItem
	if err := q.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// AddWashLog 写一条洗版日志
func AddWashLog(log *WashLog) error {
	if log.EventTime.IsZero() {
		log.EventTime = time.Now()
	}
	if err := db.Db.Create(log).Error; err != nil {
		return err
	}
	// 轻量保留：每账号最多保留最近 2000 条
	var count int64
	db.Db.Model(&WashLog{}).Where("account_id = ?", log.AccountID).Count(&count)
	if count > 2000 {
		db.Db.Where("account_id = ?", log.AccountID).
			Order("id asc").Limit(int(count - 2000)).Delete(&WashLog{})
	}
	return nil
}

// ListWashLogs 查询洗版日志
func ListWashLogs(accountID uint, limit int) ([]WashLog, error) {
	q := db.Db.Order("id desc")
	if accountID > 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q = q.Limit(limit)
	var list []WashLog
	if err := q.Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ClearWashLogs 清空洗版日志（可选按账号）
func ClearWashLogs(accountID uint) error {
	if accountID > 0 {
		return db.Db.Where("account_id = ?", accountID).Delete(&WashLog{}).Error
	}
	return db.Db.Where("1 = 1").Delete(&WashLog{}).Error
}

// CountWashItems 统计待洗版数量（状态可选）
func CountWashItems(accountID uint, status string) int64 {
	q := db.Db.Model(&WashScanItem{})
	if accountID > 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var n int64
	q.Count(&n)
	return n
}

// ---------------------------------------------------------------------------
// 影巢签到历史（S3，借鉴 symedia checkin_success.json 记录）
// ---------------------------------------------------------------------------

// HiveCheckinRecord 签到历史记录（主/子账号统一，每条签到动作一行）
type HiveCheckinRecord struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AccountID uint      `gorm:"index:idx_hcr_account_time,priority:1" json:"account_id"`
	Label     string    `gorm:"size:80" json:"label"` // 账号标签快照（主账号/小号1）
	IsMain    bool      `json:"is_main"`
	Channel   string    `gorm:"size:16" json:"channel"` // symedia/tgtodrive/nanshare/official
	Mode      string    `gorm:"size:16" json:"mode"`    // daily/gamble
	OK        bool      `json:"ok"`
	Points    *int      `json:"points"`  // 获得积分（赌狗可能为负）
	Balance   *int      `json:"balance"` // 签到后余额
	Streak    int       `json:"streak"`
	Trigger   string    `gorm:"size:16;default:manual" json:"trigger"` // manual/daily/catchup/retry
	Message   string    `gorm:"size:500" json:"message"`
	CheckinAt time.Time `gorm:"index:idx_hcr_account_time,priority:2" json:"checkin_at"`
}

// TableName 表名
func (HiveCheckinRecord) TableName() string { return "hive_checkin_records" }

// AddHiveCheckinRecord 写入签到历史并轻量清理（每账号最多 500 条，对齐 symedia _MAX_RECORDS=500）
func AddHiveCheckinRecord(r *HiveCheckinRecord) error {
	if r.CheckinAt.IsZero() {
		r.CheckinAt = time.Now()
	}
	if err := db.Db.Create(r).Error; err != nil {
		return err
	}
	var count int64
	db.Db.Model(&HiveCheckinRecord{}).Where("account_id = ?", r.AccountID).Count(&count)
	if count > 500 {
		db.Db.Where("account_id = ?", r.AccountID).Order("id asc").
			Limit(int(count - 500)).Delete(&HiveCheckinRecord{})
	}
	return nil
}

// ListHiveCheckinRecords 查询签到历史
func ListHiveCheckinRecords(accountID uint, limit int) ([]HiveCheckinRecord, error) {
	q := db.Db.Order("id desc")
	if accountID > 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var list []HiveCheckinRecord
	if err := q.Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// DeleteHiveCheckinRecords 删除签到历史（ids 为空=清空全部；accountID 可选限定）
func DeleteHiveCheckinRecords(accountID uint, ids []uint) error {
	q := db.Db
	if accountID > 0 {
		q = q.Where("account_id = ?", accountID)
	}
	if len(ids) > 0 {
		return q.Where("id IN ?", ids).Delete(&HiveCheckinRecord{}).Error
	}
	return q.Where("1 = 1").Delete(&HiveCheckinRecord{}).Error
}

// HasCheckedInToday 账号今日是否已签到（防重复签到/补签判断）
func HasCheckedInToday(accountID uint) bool {
	start := time.Now().Truncate(24 * time.Hour)
	var n int64
	db.Db.Model(&HiveCheckinRecord{}).
		Where("account_id = ? AND checkin_at >= ? AND ok = ?", accountID, start, true).
		Count(&n)
	return n > 0
}
