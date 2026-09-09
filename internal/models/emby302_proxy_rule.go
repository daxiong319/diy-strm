// Package models —— Emby302ProxyRule 多规则反代（对齐 tgto123 新版 emby_proxy/config）：
// 每条规则一个独立端口监听（proxy_type: emby / feiniu / feiniu_music），
// 规则复用既有 emby302 反代引擎（302 直链/STRM 指针/播放记录/弹幕钩子）。
package models

import (
	"encoding/json"
	"time"

	"diy-strm/internal/db"
)

// ProxyType 常量
const (
	ProxyTypeEmby        = "emby"
	ProxyTypeFeiniu      = "feiniu"
	ProxyTypeFeiniuMusic = "feiniu_music"
)

// Emby302ProxyRule 反代规则（每条一个监听端口）
type Emby302ProxyRule struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	ProxyType    string    `gorm:"size:24;index;default:emby" json:"proxy_type"` // emby / feiniu / feiniu_music
	Name         string    `gorm:"size:128" json:"name"`                         // 规则名（展示用）
	Host         string    `gorm:"size:500" json:"host"`                         // 源服务器地址（http://ip:port）
	APIKey       string    `gorm:"size:200" json:"-"`                            // Emby API Key（仅 emby 类型需要）
	ListenPort   int       `gorm:"index" json:"listen_port"`                     // 监听端口（0=未启动）
	Enabled      bool      `gorm:"default:false" json:"enabled"`
	PathMappings string    `gorm:"type:text" json:"path_mappings"` // JSON 数组 [{provider,local_path,cloud_path}]（飞牛音乐用）
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (Emby302ProxyRule) TableName() string { return "emby302_proxy_rules" }

// PathMappingList 解析路径映射
func (r *Emby302ProxyRule) PathMappingList() []map[string]string {
	out := make([]map[string]string, 0)
	if r.PathMappings == "" {
		return out
	}
	_ = json.Unmarshal([]byte(r.PathMappings), &out)
	return out
}

// Validate 规则校验。
// 当前版本约束：所有启用规则共用主 Emby 配置的源地址（emby302 引擎全局单 Host），
// 多端口 = 同一源的多入口；飞牛协议适配完成前暂不开放 feiniu 类型。
func (r *Emby302ProxyRule) Validate() error {
	if r.ProxyType != ProxyTypeEmby {
		return ErrProxyRuleInvalid("当前版本仅支持 Emby 类型规则（飞牛协议适配开发中）")
	}
	if r.Host == "" || r.APIKey == "" {
		return ErrProxyRuleInvalid("规则需要 host 与 api_key")
	}
	if GlobalEmbyConfig != nil && GlobalEmbyConfig.EmbyUrl != "" && r.Host != GlobalEmbyConfig.EmbyUrl {
		return ErrProxyRuleInvalid("源地址必须与「Emby 服务器配置」一致（当前版本多端口共用同一源）")
	}
	if r.ListenPort < 0 || r.ListenPort > 65535 {
		return ErrProxyRuleInvalid("监听端口必须是 0-65535")
	}
	return nil
}

// ErrProxyRuleInvalid 规则校验错误
type ErrProxyRuleInvalid string

func (e ErrProxyRuleInvalid) Error() string { return string(e) }

// --- CRUD ---

// ListEmby302ProxyRules 全部规则
func ListEmby302ProxyRules() ([]Emby302ProxyRule, error) {
	var rules []Emby302ProxyRule
	if err := db.Db.Order("id ASC").Find(&rules).Error; err != nil {
		return nil, err
	}
	return rules, nil
}

// GetEmby302ProxyRule 按 ID 取规则
func GetEmby302ProxyRule(id uint) (*Emby302ProxyRule, error) {
	var rule Emby302ProxyRule
	if err := db.Db.First(&rule, id).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// SaveEmby302ProxyRule 新建/更新规则
func SaveEmby302ProxyRule(rule *Emby302ProxyRule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	return db.Db.Save(rule).Error
}

// DeleteEmby302ProxyRule 删除规则
func DeleteEmby302ProxyRule(id uint) error {
	return db.Db.Delete(&Emby302ProxyRule{}, id).Error
}

// EnsureEmby302ProxyRuleTable 建表（启动时）
func EnsureEmby302ProxyRuleTable() error {
	return db.Db.AutoMigrate(&Emby302ProxyRule{})
}
