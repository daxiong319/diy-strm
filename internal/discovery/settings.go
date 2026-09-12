// Package discovery 提供影视发现功能（复刻参考实现 media_discovery）：
// 影视探索、榜单推荐、追剧日历、番剧目录、收藏与发现设置。
package discovery

import (
	"encoding/json"
	"time"

	"diy-strm/internal/db"
)

// DiscoverySetting 发现页键值设置（对应参考实现 media_settings 表）
type DiscoverySetting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"` // JSON 编码
	UpdatedAt time.Time `json:"updated_at"`
}

func (DiscoverySetting) TableName() string { return "discovery_settings" }

// 默认设置项（与 参考实现 DEFAULT_SETTINGS 对齐的 diy-strm 子集）
const (
	SettingDefaultExploreSource   = "default_explore_source"   // 默认探索来源 tmdb/douban/anilist/bangumi/actors
	SettingDefaultExploreSort     = "default_explore_sort"     // 默认排序 popular/latest/rating
	SettingCalendarDays           = "calendar_days"            // 追剧日历天数 7-60
	SettingCalendarKind           = "calendar_kind"            // 日历类型 all/tv/movie/upcoming/on-air/airing-today
	SettingRankingRegion          = "ranking_region"           // 流媒体榜单默认地区 US/CN/JP...
	SettingRankingProvider        = "ranking_provider"         // 流媒体平台 netflix/apple+/disney+...
	SettingRankingMediaType       = "ranking_media_type"       // 榜单媒体类型 movie/tv/all
	SettingMatchDoubanTMDBEnabled = "match_douban_tmdb"        // 豆瓣条目自动匹配 TMDB
	SettingEmbyCheckEnabled       = "emby_check_enabled"       // 发现条目联动 Emby 入库检测
	SettingCacheTTLMinutes        = "cache_ttl_minutes"        // 目录缓存分钟数
	SettingGuanyingEnabled        = "guanying_enabled"         // 观影资源源开关
	// —— 资源闭环（对齐参考实现 media_settings）——
	SettingTGResourceChannels     = "tg_resource_channels"     // 公开 TG 资源检索频道 {123:[],guangya:[],pan139:[]}
	SettingMediaTransferTargets   = "media_transfer_targets"   // 影视发现保存目录 {123:{folder_path,folder_name},...}
	SettingMediaEmby              = "media_emby"               // Emby 媒体库 {enabled,server_url,api_key}
	SettingTargetProvider         = "target_provider"          // 默认转存目标网盘 123/guangya/pan139
	SettingCheckIntervalMinutes   = "check_interval_minutes"   // 订阅默认检查间隔（分钟）
	SettingMaxPoints              = "max_points"               // 订阅默认解锁积分上限
	SettingTgto123URL             = "tgto123_proxy_url"        // tgto123 反代地址（榜单/日历优先通道）
	SettingTgto123Username        = "tgto123_proxy_username"   // tgto123 登录用户名
	SettingTgto123Password        = "tgto123_proxy_password"   // tgto123 登录密码
	Tgto123DefaultURL             = "http://127.0.0.1:12366"  // tgto123 反代默认地址（同机部署）
	SettingEmbyMissingAutoScan    = "emby_missing_auto_scan"   // 缺集自动扫描
	SettingEmbyMissingInterval    = "emby_missing_scan_interval_minutes" // 缺集扫描间隔（分钟）
	SettingEmbyMissingAutoSubs    = "emby_missing_auto_create_subscriptions" // 自动创建补档订阅
)

// DefaultSettings 默认设置值
func DefaultSettings() map[string]any {
	return map[string]any{
		SettingDefaultExploreSource:   "tmdb",
		SettingDefaultExploreSort:     "popular",
		SettingCalendarDays:           30,
		SettingCalendarKind:           "all",
		SettingRankingRegion:          "US",
		SettingRankingProvider:        "netflix",
		SettingRankingMediaType:       "movie",
		SettingMatchDoubanTMDBEnabled: true,
		SettingEmbyCheckEnabled:       false,
		SettingCacheTTLMinutes:        30,
		SettingGuanyingEnabled:        false,
		SettingTGResourceChannels:     map[string]any{},
		SettingMediaTransferTargets:   map[string]any{},
		SettingMediaEmby:              map[string]any{},
		SettingTargetProvider:         "123",
		SettingCheckIntervalMinutes:   360,
		SettingMaxPoints:              4,
		SettingTgto123URL:             Tgto123DefaultURL,
		SettingTgto123Username:        "admin",
		SettingTgto123Password:        "",
		SettingEmbyMissingAutoScan:    false,
		SettingEmbyMissingInterval:    720,
		SettingEmbyMissingAutoSubs:    false,
	}
}

// GetSettings 读取全部设置（缺省补默认值）
func GetSettings() (map[string]any, error) {
	settings := DefaultSettings()
	var rows []DiscoverySetting
	if err := db.Db.Find(&rows).Error; err != nil {
		return settings, err
	}
	for _, row := range rows {
		if _, ok := settings[row.Key]; !ok {
			continue // 只允许已知键
		}
		var v any
		if json.Unmarshal([]byte(row.Value), &v) == nil && v != nil {
			settings[row.Key] = v
		}
	}
	return settings, nil
}

// mustSettings 忽略错误读取设置（缓存键/资源搜索等非关键路径用）
func mustSettings() map[string]any {
	settings, _ := GetSettings()
	return settings
}

// UpdateSettings 更新设置（仅接受已知键）
func UpdateSettings(values map[string]any) (map[string]any, error) {
	defaults := DefaultSettings()
	now := time.Now()
	for key, value := range values {
		if _, ok := defaults[key]; !ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		setting := DiscoverySetting{Key: key, Value: string(raw), UpdatedAt: now}
		if err := db.Db.Save(&setting).Error; err != nil {
			return nil, err
		}
	}
	return GetSettings()
}

// SettingBool / SettingInt / SettingString 快捷读取单个设置
func SettingBool(key string, def bool) bool {
	settings, err := GetSettings()
	if err != nil {
		return def
	}
	if v, ok := settings[key].(bool); ok {
		return v
	}
	return def
}

func SettingInt(key string, def int) int {
	settings, err := GetSettings()
	if err != nil {
		return def
	}
	switch v := settings[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return def
	}
}

func SettingString(key, def string) string {
	settings, err := GetSettings()
	if err != nil {
		return def
	}
	if v, ok := settings[key].(string); ok && v != "" {
		return v
	}
	return def
}

// EnsureDiscoverySchema 确保发现相关表存在（由 migrator 调用）
func EnsureDiscoverySchema() error {
	return db.Db.AutoMigrate(
		&DiscoverySetting{},
		&DiscoveryFavorite{},
		&DiscoverySubjectCache{},
		&DiscoveryCatalogState{},
		&DiscoveryExternalCache{},
		&DiscoverySubscription{},
		&DiscoverySubscriptionRule{},
		&DiscoverySubscriptionRun{},
		&DiscoverySubscriptionItem{},
		&DiscoverySubscriptionEvent{},
		&DiscoveryEmbyMissingScan{},
		&DiscoveryEmbyMissingResult{},
		&DiscoveryEmbyMissingEvent{},
	)
}
