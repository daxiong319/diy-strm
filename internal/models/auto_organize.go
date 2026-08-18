package models

import (
	"time"

	"gorm.io/gorm"

	"diy-strm/internal/db"
)

// AutoOrganizeConfig 云盘自动整理配置（每个网盘账号一条）。
// 监控程序发现「待整理目录」新增资源后，按该账号配置的分类策略 yaml
// 把资源整理到「已整理根目录」下的分类目录，并重命名（保留质量标签）。
type AutoOrganizeConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AccountID uint      `gorm:"index:idx_auto_organize_account,unique" json:"account_id"` // 网盘账号 ID
	Enabled   bool      `gorm:"default:false" json:"enabled"`                             // 是否启用自动整理
	// PendingDir 待整理目录（监控扫描目录），如 媒体库/待整理
	PendingDir string `gorm:"size:255" json:"pending_dir"`
	// OrganizedRoot 已整理根目录（新资源整理到其下分类目录），
	// 为空时自动推导：待整理目录的父目录/已整理（如 媒体库/待整理 → 媒体库/已整理）
	OrganizedRoot string `gorm:"size:255" json:"organized_root"`
	// FailedDir 整理失败目录（识别失败/TMDB 查不到的资源移入），
	// 为空时识别失败的不移动，仅记录。
	FailedDir string `gorm:"size:255" json:"failed_dir"`
	// CategoryConfig 该账号的分类策略 yaml（MoviePilot category.yaml 风格），
	// 为空使用默认分类策略（与影视订阅一致）。
	CategoryConfig string `gorm:"type:text" json:"category_config"`
	// Overwrite 同一部影片（同一 TMDB）目标目录已存在时是否覆盖（洗版），
	// true=删除旧文件重新整理；false=跳过并在报告中提示。
	Overwrite bool `gorm:"default:true" json:"overwrite"`
	// LastRunAt 最近一次自动整理运行时间
	LastRunAt time.Time `json:"last_run_at"`
	// LastResult 最近一次自动整理结果摘要（JSON 文本，前端展示）
	LastResult string `gorm:"type:text" json:"last_result"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 指定表名
func (AutoOrganizeConfig) TableName() string {
	return "auto_organize_configs"
}

// GetAutoOrganizeConfig 按 ID 查询配置
func GetAutoOrganizeConfig(id uint) (*AutoOrganizeConfig, error) {
	var c AutoOrganizeConfig
	if err := db.Db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetAutoOrganizeConfigByAccount 按账号 ID 查询配置
func GetAutoOrganizeConfigByAccount(accountID uint) (*AutoOrganizeConfig, error) {
	var c AutoOrganizeConfig
	if err := db.Db.Where("account_id = ?", accountID).First(&c).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// ListAutoOrganizeConfigs 查询全部自动整理配置
func ListAutoOrganizeConfigs() ([]AutoOrganizeConfig, error) {
	var list []AutoOrganizeConfig
	if err := db.Db.Order("account_id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListEnabledAutoOrganizeConfigs 查询启用中的自动整理配置
func ListEnabledAutoOrganizeConfigs() ([]AutoOrganizeConfig, error) {
	var list []AutoOrganizeConfig
	if err := db.Db.Where("enabled = ?", true).Order("account_id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SaveAutoOrganizeConfig 创建或更新配置（每账号仅一条，按 account_id upsert）
func SaveAutoOrganizeConfig(c *AutoOrganizeConfig) error {
	if c.AccountID == 0 {
		return nil
	}
	existing, err := GetAutoOrganizeConfigByAccount(c.AccountID)
	if err != nil {
		return err
	}
	if existing == nil {
		c.CreatedAt = time.Now()
		c.UpdatedAt = time.Now()
		return db.Db.Create(c).Error
	}
	c.ID = existing.ID
	c.CreatedAt = existing.CreatedAt
	c.UpdatedAt = time.Now()
	return db.Db.Save(c).Error
}

// DeleteAutoOrganizeConfig 删除配置
func DeleteAutoOrganizeConfig(id uint) error {
	return db.Db.Delete(&AutoOrganizeConfig{}, id).Error
}

// UpdateAutoOrganizeLastRun 更新最近运行时间与结果摘要
func UpdateAutoOrganizeLastRun(id uint, resultJSON string) {
	now := time.Now()
	updates := map[string]any{
		"last_run_at": now,
	}
	if resultJSON != "" {
		updates["last_result"] = resultJSON
	}
	if err := db.Db.Model(&AutoOrganizeConfig{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		_ = err
	}
}