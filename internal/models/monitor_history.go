package models

import (
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
)

// MonitorTransferRecord 监控历史转存记录（对齐 tgto123 的 messages 表）
// 记录 TG 频道订阅 / 影巢订阅 / TG 机器人三类监控入口的每一次转存尝试（成功/失败/跳过），
// 与 CloudTransferRecord 的分工：后者只记成功转存、用于订阅去重与完结判定；本表为全量审计展示。
type MonitorTransferRecord struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SourceType     string    `gorm:"size:32;index:idx_mtr_source_time" json:"source_type"` // 目标网盘：123 / guangyapan / pan139
	Entry          string    `gorm:"size:16;index" json:"entry"`                           // 监控入口：channel=TG频道订阅 / hive=影巢订阅 / bot=TG机器人
	Channel        string    `gorm:"size:128" json:"channel"`                              // 频道名（影巢为空、机器人为 TG机器人）
	MessageID      string    `gorm:"size:64" json:"message_id"`                            // 帖子 ID / 影巢资源 slug
	MessageURL     string    `gorm:"size:512" json:"message_url"`                          // 消息链接（https://t.me/频道/帖子ID）
	TargetURL      string    `gorm:"size:512" json:"target_url"`                           // 网盘分享链接
	TransferStatus string    `gorm:"size:32;index" json:"transfer_status"`                 // 转存成功 / 转存失败 / 已跳过 / 洗版替换
	TransferTime   time.Time `gorm:"index:idx_mtr_source_time" json:"transfer_time"`       // 转存发生时间
	TransferResult string    `gorm:"type:text" json:"transfer_result"`                     // 结果描述（失败原因 / 跳过原因 / 成功摘要）
	Title          string    `gorm:"size:256" json:"title"`                                 // 分享标题
	Total          int       `json:"total"`                                                // 转存文件数
	TargetDir      string    `gorm:"size:512" json:"target_dir"`                           // 转存目标目录
	SubscriptionID uint      `gorm:"index" json:"subscription_id"`                         // 关联订阅 ID（机器人入口为 0）
	CreatedAt      time.Time `json:"created_at"`
}

// 监控历史状态常量（对齐 tgto123：转存成功/转存失败；跳过与洗版为本项目扩展）
const (
	MonitorStatusSuccess = "转存成功"
	MonitorStatusFailed  = "转存失败"
	MonitorStatusSkipped = "已跳过"
	MonitorStatusWash    = "洗版替换"
)

// CreateMonitorTransferRecord 写入一条监控历史（失败仅记日志，不影响主流程）
func CreateMonitorTransferRecord(r *MonitorTransferRecord) {
	if r.TransferTime.IsZero() {
		r.TransferTime = time.Now()
	}
	if err := db.Db.Create(r).Error; err != nil {
		helpers.AppLogger.Errorf("写入监控历史失败：%v", err)
	}
}

// QueryMonitorTransferRecords 分页查询监控历史
// sourceType 为空 = 全部网盘；status 为空 = 全部状态；keyword 模糊匹配结果列
func QueryMonitorTransferRecords(sourceType, status, keyword string, page, pageSize int) (records []MonitorTransferRecord, total int64, statusOptions []string, statusCounts map[string]int64, err error) {
	q := db.Db.Model(&MonitorTransferRecord{})
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if status != "" {
		q = q.Where("transfer_status = ?", status)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("transfer_result ILIKE ?", "%"+kw+"%")
	}
	if err = q.Count(&total).Error; err != nil {
		return
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	if err = q.Order("transfer_time DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return
	}
	// 状态选项（当前来源下的全部状态及计数，用于筛选下拉）
	sq := db.Db.Model(&MonitorTransferRecord{})
	if sourceType != "" {
		sq = sq.Where("source_type = ?", sourceType)
	}
	type statusRow struct {
		Status string
		Cnt    int64
	}
	var rows []statusRow
	if err = sq.Select("transfer_status AS status, COUNT(*) AS cnt").Group("transfer_status").Order("cnt DESC, transfer_status ASC").Scan(&rows).Error; err != nil {
		return
	}
	statusCounts = make(map[string]int64, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Status)
		if name == "" {
			name = "（空状态）"
		}
		statusOptions = append(statusOptions, name)
		statusCounts[name] = row.Cnt
	}
	return
}

// CountMonitorHistoryBySource 各来源（网盘）的记录数（用于来源标签角标）
func CountMonitorHistoryBySource() (map[string]int64, error) {
	type srcRow struct {
		SourceType string
		Cnt        int64
	}
	var rows []srcRow
	if err := db.Db.Model(&MonitorTransferRecord{}).
		Select("source_type AS source_type, COUNT(*) AS cnt").
		Group("source_type").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.SourceType] = row.Cnt
	}
	return out, nil
}

// DeleteMonitorTransferRecords 删除指定 ID 的监控历史
func DeleteMonitorTransferRecords(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Db.Where("id IN ?", ids).Delete(&MonitorTransferRecord{}).Error
}

// ClearMonitorTransferRecords 清理监控历史（source 为空 = 全部；startDate/endDate 可选，按转存时间过滤）
func ClearMonitorTransferRecords(source string, startDate, endDate *time.Time) (int64, error) {
	q := db.Db.Where("1 = 1")
	if source != "" {
		q = q.Where("source_type = ?", source)
	}
	if startDate != nil {
		q = q.Where("transfer_time >= ?", *startDate)
	}
	if endDate != nil {
		q = q.Where("transfer_time <= ?", *endDate)
	}
	res := q.Delete(&MonitorTransferRecord{})
	return res.RowsAffected, res.Error
}
