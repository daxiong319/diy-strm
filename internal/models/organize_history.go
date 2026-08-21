package models

import (
	"strings"
	"time"

	"diy-strm/internal/db"

	"gorm.io/gorm"
)

// 整理历史状态（与 tgto123 organize_history_records 对齐）
const (
	OrganizeStatusSuccess = "success" // 整理成功
	OrganizeStatusFailed  = "failed"  // 整理失败
	OrganizeStatusSkipped = "skipped" // 跳过（未找到匹配/TMDB 未命中/保留现有版本）
	OrganizeStatusReplace = "replace" // 洗版替换成功
	OrganizeStatusUnknown = "unknown" // 其他
)

// SourceDisplayName 网盘来源展示名（整理历史来源维度）
func SourceDisplayName(st SourceType) string {
	switch st {
	case SourceType123:
		return "123云盘"
	case SourceTypeGuangYaPan:
		return "光鸭云盘"
	case SourceType115:
		return "115网盘"
	case SourceTypeBaiduPan:
		return "百度网盘"
	case SourceTypeOpenList:
		return "OpenList"
	case SourceTypePan139:
		return "139云盘"
	default:
		return string(st)
	}
}

// OrganizeHistoryRecord 整理历史记录（每次整理动作一条）。
// 结构与 tgto123 的 organize_history_records 对齐：来源/状态/时间/路径/媒体信息/消息。
type OrganizeHistoryRecord struct {
	ID                 uint      `gorm:"primaryKey;index:idx_oh_source_id,priority:2" json:"id"`
	Source             string    `gorm:"size:32;not null;index:idx_oh_source_id,priority:1;index:idx_oh_source_status_file,priority:1" json:"source"` // 来源（123云盘/光鸭云盘/115网盘/...）
	Status             string    `gorm:"size:16;not null;index:idx_oh_status;index:idx_oh_source_status_file,priority:2" json:"status"`              // success/failed/skipped/replace/unknown
	EventTime          time.Time `gorm:"not null;index:idx_oh_event_time" json:"event_time"`                                                          // 事件时间
	FileID             string    `gorm:"size:128;index:idx_oh_source_status_file,priority:3" json:"file_id"`                                          // 网盘文件 ID
	FileName           string    `gorm:"size:512" json:"file_name"`                                                   // 处理后的文件名
	OriginalFileName   string    `gorm:"size:512" json:"original_file_name"`                                          // 处理前文件名
	SourcePath         string    `gorm:"size:1024" json:"source_path"`                                                // 源路径（网盘内路径或父目录路径语义）
	OriginalSourcePath string    `gorm:"size:1024" json:"original_source_path"`                                       // 整理前源路径（重整理场景保留）
	TargetPath         string    `gorm:"size:1024" json:"target_path"`                                                // 目标路径（相对整理根目录）
	Title              string    `gorm:"size:256" json:"title"`                                                       // 识别标题
	Year               int       `json:"year"`
	MediaType          string    `gorm:"size:16" json:"media_type"`                                                   // Movie / TV
	SeasonNum          int       `json:"season_num"`
	EpisodeNum         int       `json:"episode_num"`
	TMDBID             int64     `json:"tmdb_id"`
	Message            string    `gorm:"size:1024" json:"message"`                                                    // 成功/跳过摘要消息
	ErrorMessage       string    `gorm:"size:2048" json:"error_message"`                                              // 失败原因
	ExtraJSON          string    `gorm:"type:text" json:"extra_json"`                                                 // 扩展信息 JSON（account_id/task_id/parent_id 等）
	CreatedAt          time.Time `json:"created_at"`
}

// TableName 表名
func (OrganizeHistoryRecord) TableName() string {
	return "organize_history_records"
}

// CreateOrganizeHistoryRecord 写入一条整理历史。
func CreateOrganizeHistoryRecord(r *OrganizeHistoryRecord) error {
	if r.EventTime.IsZero() {
		r.EventTime = time.Now()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if r.Status == "" {
		r.Status = OrganizeStatusUnknown
	}
	return db.Db.Create(r).Error
}

// ListOrganizeHistoryRecords 分页查询整理历史（来源/状态/关键词过滤，新到旧）。
func ListOrganizeHistoryRecords(source, status, keyword string, page, limit int) ([]OrganizeHistoryRecord, int64, error) {
	q := db.Db.Model(&OrganizeHistoryRecord{})
	if source != "" {
		q = q.Where("source = ?", source)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		kw := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("(file_name LIKE ? OR original_file_name LIKE ? OR title LIKE ? OR message LIKE ?)", kw, kw, kw, kw)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var list []OrganizeHistoryRecord
	if err := q.Order("id desc").Offset((page - 1) * limit).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// GetOrganizeHistoryRecord 按 ID 查询单条记录。
func GetOrganizeHistoryRecord(id uint) *OrganizeHistoryRecord {
	var r OrganizeHistoryRecord
	if err := db.Db.First(&r, id).Error; err != nil {
		return nil
	}
	return &r
}

// DeleteOrganizeHistoryRecords 按 ID 批量删除记录（异步任务使用），返回删除条数。
func DeleteOrganizeHistoryRecords(ids []uint) int64 {
	if len(ids) == 0 {
		return 0
	}
	res := db.Db.Where("id IN ?", ids).Delete(&OrganizeHistoryRecord{})
	return res.RowsAffected
}

// ClearOrganizeHistoryRecords 按来源 + 日期范围清理记录，返回删除条数。
// source 为空表示全部来源；startDate/endDate 为空表示不限制。
func ClearOrganizeHistoryRecords(source, startDate, endDate string) (int64, error) {
	q := db.Db.Model(&OrganizeHistoryRecord{})
	if source != "" {
		q = q.Where("source = ?", source)
	}
	if startDate != "" {
		q = q.Where("event_time >= ?", startDate+" 00:00:00")
	}
	if endDate != "" {
		q = q.Where("event_time <= ?", endDate+" 23:59:59")
	}
	// GORM 无条件 Delete 会拒绝执行，全量清理时用恒真条件
	if source == "" && startDate == "" && endDate == "" {
		q = q.Where("1 = 1")
	}
	res := q.Delete(&OrganizeHistoryRecord{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// CountOrganizeHistoryBySource 各来源记录数（来源标签角标）。
func CountOrganizeHistoryBySource() (map[string]int64, error) {
	type row struct {
		Source string
		Count  int64
	}
	var rows []row
	if err := db.Db.Model(&OrganizeHistoryRecord{}).
		Select("source, COUNT(*) AS count").
		Group("source").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Source] = r.Count
	}
	return out, nil
}

// CountOrganizeHistoryByStatus 各状态记录数（状态筛选角标）。
func CountOrganizeHistoryByStatus() (map[string]int64, error) {
	type row struct {
		Status string
		Count  int64
	}
	var rows []row
	if err := db.Db.Model(&OrganizeHistoryRecord{}).
		Select("status, COUNT(*) AS count").
		Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// DeleteOrganizeHistoryRecordsBySourceStatusFile 按来源+状态+文件ID删除（同文件重复记录清理，重整理时使用）。
func DeleteOrganizeHistoryRecordsBySourceStatusFile(source, status, fileID string) int64 {
	res := db.Db.Where("source = ? AND status = ? AND file_id = ?", source, status, fileID).
		Delete(&OrganizeHistoryRecord{})
	return res.RowsAffected
}

// GetOrganizeHistoryRecordBySourceFile 按来源+文件ID取最新一条记录。
func GetOrganizeHistoryRecordBySourceFile(source, fileID string) *OrganizeHistoryRecord {
	var r OrganizeHistoryRecord
	if err := db.Db.Where("source = ? AND file_id = ?", source, fileID).
		Order("id desc").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// AutoMigrateOrganizeHistory 迁移整理历史表（migrator 调用）。
func AutoMigrateOrganizeHistory(d *gorm.DB) error {
	return d.AutoMigrate(&OrganizeHistoryRecord{})
}
