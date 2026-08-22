package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/hdhive"

	"gorm.io/gorm"
)

// HiveOAuthAccount 影巢 OAuth 授权账号（主账号 + 子账号统一存储）
// 参考 tgto123 hdhive_sub_accounts 模块，diy-strm 使用数据库存储。
type HiveOAuthAccount struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	Label             string     `gorm:"size:80" json:"label"` // 账号标签（主账号/小号1）
	IsMain            bool       `gorm:"index" json:"is_main"` // 是否主账号（每库仅一条）
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	Channel           string     `gorm:"size:16;index;default:tgtodrive" json:"channel"` // 通道：tgtodrive（默认，install_id 签名中转）/ official（官方 OpenAPI）
	InstallID         string     `gorm:"size:128" json:"-"` // 独立 install_id（tgtodrive 通道，不对外暴露）
	AccessToken       string     `gorm:"type:text" json:"-"`  // 官方通道用户 Access Token
	RefreshToken      string     `gorm:"type:text" json:"-"`  // 官方通道 Refresh Token
	TokenExpiresAt    *time.Time `json:"token_expires_at"`    // 官方通道 Access Token 过期时间
	Authorized        bool       `json:"authorized"`
	AuthorizedAt      *time.Time `json:"authorized_at"`
	UserInfo          string     `gorm:"type:text" json:"-"` // 用户快照 JSON（/api/me）
	Status            string     `gorm:"type:text" json:"-"` // 授权状态 JSON（token_status）
	UserFetchedAt     *time.Time `json:"user_fetched_at"`
	LastCheckinAt     *time.Time `json:"last_checkin_at"`
	LastCheckinOK     bool       `json:"last_checkin_ok"`
	LastCheckinMsg    string     `gorm:"size:500" json:"last_checkin_message"`
	LastCheckinMode   string     `gorm:"size:16" json:"last_checkin_mode"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// TableName 表名
func (HiveOAuthAccount) TableName() string {
	return "hive_oauth_accounts"
}

// PublicHiveAccount 对外暴露的账号信息（不含 install_id）
type PublicHiveAccount struct {
	ID              uint            `json:"id"`
	Label           string          `json:"label"`
	IsMain          bool            `json:"is_main"`
	Enabled         bool            `json:"enabled"`
	Authorized      bool            `json:"authorized"`
	AuthorizedAt    *time.Time      `json:"authorized_at"`
	InstallHash     string          `json:"install_hash"`
	User            *hdhive.MeUserInfo `json:"user,omitempty"`
	UserFetchedAt   *time.Time      `json:"user_fetched_at"`
	Status          map[string]any  `json:"status,omitempty"`
	LastCheckinAt   *time.Time      `json:"last_checkin_at"`
	LastCheckinOK   bool            `json:"last_checkin_ok"`
	LastCheckinMsg  string          `json:"last_checkin_message"`
	LastCheckinMode string          `json:"last_checkin_mode"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// 主账号
// ---------------------------------------------------------------------------

// GetHiveMainAccount 获取主账号（不存在则自动创建）
func GetHiveMainAccount() (*HiveOAuthAccount, error) {
	var acc HiveOAuthAccount
	err := db.Db.Where("is_main = ?", true).First(&acc).Error
	if err == nil {
		return &acc, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// 自动创建主账号
	acc = HiveOAuthAccount{
		Label:     "主账号",
		IsMain:    true,
		Enabled:   true,
		InstallID: newHiveInstallID(),
	}
	if err := db.Db.Create(&acc).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

// GetHiveAccountByID 按 ID 查询账号
func GetHiveAccountByID(id uint) (*HiveOAuthAccount, error) {
	var acc HiveOAuthAccount
	if err := db.Db.First(&acc, id).Error; err != nil {
		return nil, err
	}
	return &acc, nil
}

// SaveHiveAccount 保存账号
// FindOrCreateHiveOfficialAccount 获取或创建官方通道账号（channel=official 固定一条）
func FindOrCreateHiveOfficialAccount(label string) *HiveOAuthAccount {
	var acc HiveOAuthAccount
	if err := db.Db.Where("channel = ?", HiveChannelOfficial).First(&acc).Error; err == nil {
		return &acc
	}
	acc = HiveOAuthAccount{
		Label:    label,
		Channel:  HiveChannelOfficial,
		Enabled:  true,
	}
	_ = db.Db.Create(&acc).Error
	return &acc
}

func SaveHiveAccount(acc *HiveOAuthAccount) error {
	acc.UpdatedAt = time.Now()
	return db.Db.Save(acc).Error
}

// ---------------------------------------------------------------------------
// 子账号
// ---------------------------------------------------------------------------

// ListHiveSubAccounts 查询全部子账号
func ListHiveSubAccounts() ([]HiveOAuthAccount, error) {
	var list []HiveOAuthAccount
	if err := db.Db.Where("is_main = ?", false).Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// AddHiveSubAccount 新增子账号
func AddHiveSubAccount(label string) (*HiveOAuthAccount, error) {
	if strings.TrimSpace(label) == "" {
		// 默认标签：小号 N
		list, err := ListHiveSubAccounts()
		if err != nil {
			return nil, err
		}
		label = fmt.Sprintf("小号%d", len(list)+1)
	}
	acc := &HiveOAuthAccount{
		Label:     strings.TrimSpace(label),
		IsMain:    false,
		Enabled:   true,
		InstallID: newHiveInstallID(),
	}
	if err := db.Db.Create(acc).Error; err != nil {
		return nil, err
	}
	return acc, nil
}

// DeleteHiveSubAccount 删除子账号（主账号禁止删除）
func DeleteHiveSubAccount(id uint) error {
	acc, err := GetHiveAccountByID(id)
	if err != nil {
		return err
	}
	if acc.IsMain {
		return errors.New("主账号不允许删除")
	}
	return db.Db.Delete(&HiveOAuthAccount{}, id).Error
}

// UpdateHiveSubAccount 更新子账号字段（label/enabled）
func UpdateHiveSubAccount(id uint, label string, enabled *bool) error {
	acc, err := GetHiveAccountByID(id)
	if err != nil {
		return err
	}
	if label != "" {
		acc.Label = strings.TrimSpace(label)
	}
	if enabled != nil {
		acc.Enabled = *enabled
	}
	return SaveHiveAccount(acc)
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// newHiveInstallID 生成随机 install_id（与 tgto123 secrets.token_urlsafe(48) 等价）
func newHiveInstallID() string {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		// 极端情况下退化为时间戳+随机数
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HiveInstallHash 计算 install_id 的 SHA-256 哈希
func HiveInstallHash(installID string) string {
	h := sha256.Sum256([]byte(installID))
	return hex.EncodeToString(h[:])
}

// PublicHiveAccount 转为对外结构
func (a *HiveOAuthAccount) Public() *PublicHiveAccount {
	p := &PublicHiveAccount{
		ID:              a.ID,
		Label:           a.Label,
		IsMain:          a.IsMain,
		Enabled:         a.Enabled,
		Authorized:      a.Authorized,
		AuthorizedAt:    a.AuthorizedAt,
		InstallHash:     HiveInstallHash(a.InstallID),
		UserFetchedAt:   a.UserFetchedAt,
		LastCheckinAt:   a.LastCheckinAt,
		LastCheckinOK:   a.LastCheckinOK,
		LastCheckinMsg:  a.LastCheckinMsg,
		LastCheckinMode: a.LastCheckinMode,
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
	if a.UserInfo != "" {
		var u hdhive.MeUserInfo
		if err := json.Unmarshal([]byte(a.UserInfo), &u); err == nil {
			p.User = &u
		}
	}
	if a.Status != "" {
		var s map[string]any
		if err := json.Unmarshal([]byte(a.Status), &s); err == nil {
			p.Status = s
		}
	}
	return p
}