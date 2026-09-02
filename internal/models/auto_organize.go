package models

import (
	"time"

	"gorm.io/gorm"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
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

	// ---- 洗版策略（v2，借鉴 mediavault MediaUpgradeConfig / symedia 覆盖规则）----
	// WashCompare 更优才覆盖：仅当新文件质量综合评分高于被替换旧文件时才执行覆盖
	// （关闭后回到「存在即覆盖」，但仍是同名覆盖而非整目录删除）。
	WashCompare bool `gorm:"default:true" json:"wash_compare"`
	// LoserArchiveDir 被替换旧版的归档目录（网盘内路径）。为空=旧版直接删除；
	// 配置后旧版先移入此目录保留备份（mediavault loser archive 语义），如 洗版淘汰
	LoserArchiveDir string `gorm:"size:255" json:"loser_archive_dir"`
	// LoserSourceAction 落败新文件（比较中输给库内旧版）源文件的处置：
	// keep（默认，源保留在待整理目录）/ delete（直接删除源，避免反复比对）
	LoserSourceAction string `gorm:"size:16;default:keep" json:"loser_source_action"`
	// GroupPriority 制作组优先级（高→低，逗号分隔，如 FRDS,HLJ,ZYX；不在列表的组最低）
	GroupPriority string `gorm:"type:text" json:"group_priority"`
	// WashRulesJSON 自定义比较规则 JSON（数组串）。空=默认规则：
	// [{"field":"resolution","higher":true},{"field":"codec","higher":true},
	//  {"field":"format","higher":true},{"field":"bitdepth","higher":true},
	//  {"field":"channels","higher":true},{"field":"group","higher":true}]
	WashRulesJSON string `gorm:"type:text" json:"wash_rules_json"`
	// MinResolution 违规扫描判定：分辨率低于该值视为「待洗版」（0=不检查，默认 1080）
	MinResolution int `gorm:"default:1080" json:"min_resolution"`
	// PreferredCodecs 违规扫描判定：视频编码不在该列表视为「待洗版」（空=不检查，默认 hevc,h265,av1）
	PreferredCodecs string `gorm:"size:128;default:hevc,h265,av1" json:"preferred_codecs"`
	// WashScanCron 定时扫描洗版 cron（标准 5 段；空=停用）
	WashScanCron string `gorm:"size:64" json:"wash_scan_cron"`
	// WashScanAuto 定时扫描完成后自动执行一轮整理洗版（消费待整理目录中的新源）
	WashScanAuto bool `gorm:"default:false" json:"wash_scan_auto"`
	// LastWashScanAt 最近一次（定时或手动）扫描时间
	LastWashScanAt time.Time `json:"last_wash_scan_at"`
	// LastWashScanResult 最近一次违规扫描结果摘要（JSON 文本，前端展示）
	LastWashScanResult string `gorm:"type:text" json:"last_wash_scan_result"`

	// ---- 整理过滤与命名（P2，借鉴 mediavault OrganizeConfig / symedia 归档）----
	// BlockedWords 文件名/目录名黑名单正则（每行一条）。命中视为垃圾内容跳过整理
	// （如 \b(SP|NC[OPED]+|PV|CM|特典|Preview|Trailer)\b 类），内容保留原位不移动。
	BlockedWords string `gorm:"type:text" json:"blocked_words"`
	// CustomizationWords 平台/定制词表（逗号分隔），识别标题前从文件名剥离，
	// 避免 Baha/NF/Disney+ 等词污染标题与目录名（为空用默认词表）。
	CustomizationWords string `gorm:"type:text" json:"customization_words"`
	// MovieNameTemplate 电影整理后文件名模板（pongo2/Jinja2 语法，为空=内置默认）。
	// 可用字段：title year tmdbid season episode season_episode ep_cn tags
	// video_format video_codec audio_codec channels hdr bitdepth customization release_group file_ext
	MovieNameTemplate string `gorm:"type:text" json:"movie_name_template"`
	// TvNameTemplate 剧集整理后文件名模板（同上）
	TvNameTemplate string `gorm:"type:text" json:"tv_name_template"`
	// MinTMDBScore 同名低分过滤：TMDB 校验评分（vote_average）低于该值的内容跳过整理
	// （0=关闭，参考 symedia v1.0.30.2 过滤评分低于 3 分）。
	MinTMDBScore float64 `gorm:"default:0" json:"min_tmdb_score"`
	// TrackRenewal 追更模式：整理入库的剧集若 TMDB 判定未完结，完成后推送追更提示
	TrackRenewal bool `gorm:"default:false" json:"track_renewal"`
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

// UpdateAutoOrganizeLastScan 更新最近违规扫描时间与结果摘要（按账号）
func UpdateAutoOrganizeLastScan(accountID uint, resultJSON string) {
	updates := map[string]any{
		"last_wash_scan_at": time.Now(),
	}
	if resultJSON != "" {
		updates["last_wash_scan_result"] = resultJSON
	}
	if err := db.Db.Model(&AutoOrganizeConfig{}).Where("account_id = ?", accountID).Updates(updates).Error; err != nil {
		helpers.AppLogger.Warnf("更新违规扫描时间失败（账号 %d）：%v", accountID, err)
	}
}