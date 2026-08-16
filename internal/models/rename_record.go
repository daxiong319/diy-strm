package models

import (
	"time"

	"diy-strm/internal/db"

	"gorm.io/gorm"
)

// RenameHistory 批量重命名历史记录（用于回滚）。
// Rules/Targets 以 JSON 文本存储，避免引入复杂关联表。
type RenameHistory struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index" json:"user_id"`
	Name        string    `gorm:"size:128" json:"name"`        // 操作名称（批量重命名）
	Rules       string    `gorm:"type:text" json:"rules"`      // 规则 JSON（renamerule.Rule 列表）
	KeepExt     bool      `gorm:"default:true" json:"keep_ext"`
	Targets     string    `gorm:"type:text" json:"targets"`    // 成功条目 JSON（file_id/old_name/new_name/parent_id）
	ItemCount   int       `json:"item_count"`                  // 本次提交总条目数
	ChangeCount int       `json:"change_count"`                // 实际改名成功数
	CreatedAt   time.Time `json:"created_at"`
}

// CreateRenameHistory 写入历史记录。
func CreateRenameHistory(h *RenameHistory) error {
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now()
	}
	return db.Db.Create(h).Error
}

// ListRenameHistories 用户的历史记录（新到旧）。
func ListRenameHistories(userID uint, limit int) []RenameHistory {
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	var list []RenameHistory
	db.Db.Where("user_id = ?", userID).Order("id desc").Limit(limit).Find(&list)
	return list
}

// GetRenameHistory 获取单条历史（校验归属）。
func GetRenameHistory(id, userID uint) *RenameHistory {
	var h RenameHistory
	if err := db.Db.Where("id = ? AND user_id = ?", id, userID).First(&h).Error; err != nil {
		return nil
	}
	return &h
}

// DeleteRenameHistory 删除历史记录。
func DeleteRenameHistory(id, userID uint) error {
	return db.Db.Where("id = ? AND user_id = ?", id, userID).Delete(&RenameHistory{}).Error
}

// SaveRenameHistory 更新历史记录（回滚后移除已还原条目）。
func SaveRenameHistory(h *RenameHistory) error {
	return db.Db.Save(h).Error
}

// RenamePreset 批量重命名常用组合（规则快照）。
type RenamePreset struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Name      string    `gorm:"size:128" json:"name"`
	Rules     string    `gorm:"type:text" json:"rules"` // 规则 JSON（renamerule.Rule 列表）
	KeepExt   bool      `gorm:"default:true" json:"keep_ext"`
	UseCount  int       `gorm:"default:0" json:"use_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateRenamePreset 保存常用组合；同名组合更新规则并保留原创建时间。
func CreateRenamePreset(p *RenamePreset) error {
	now := time.Now()
	var existing RenamePreset
	if err := db.Db.Where("user_id = ? AND name = ?", p.UserID, p.Name).First(&existing).Error; err == nil {
		return db.Db.Model(&RenamePreset{}).
			Where("id = ?", existing.ID).
			Updates(map[string]interface{}{
				"rules":      p.Rules,
				"keep_ext":   p.KeepExt,
				"updated_at": now,
			}).Error
	}
	p.CreatedAt = now
	p.UpdatedAt = now
	return db.Db.Create(p).Error
}

// ListRenamePresets 用户的常用组合（新到旧）。
func ListRenamePresets(userID uint) []RenamePreset {
	var list []RenamePreset
	db.Db.Where("user_id = ?", userID).Order("id desc").Limit(100).Find(&list)
	return list
}

// DeleteRenamePreset 删除常用组合。
func DeleteRenamePreset(id, userID uint) error {
	return db.Db.Where("id = ? AND user_id = ?", id, userID).Delete(&RenamePreset{}).Error
}

// IncrementRenamePresetUse 常用组合使用次数 +1。
func IncrementRenamePresetUse(userID uint, rulesJSON string, keepExt bool) {
	db.Db.Model(&RenamePreset{}).
		Where("user_id = ? AND rules = ? AND keep_ext = ?", userID, rulesJSON, keepExt).
		UpdateColumn("use_count", gorm.Expr("use_count + 1"))
}