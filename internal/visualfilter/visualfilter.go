// Package visualfilter 频道订阅白名单可视化（对齐 tgto123 新版 visual_filter + server_visual_filter）：
// 场景（scene）= 各网盘频道订阅的白名单维度，规则卡片 = 逐条关键词/正则规则，
// 支持 TMDB 海报补全（复用 /scrape/tmdb-search 后端逻辑）。
package visualfilter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/discovery"
)

// 规则场景（对齐 tgto123 scenes：ENV_FILTER=123 频道 / _115 / _189 / _GUANGYA）
const (
	Scene123     = "123"
	SceneGuangya = "guangya"
	Scene139     = "139"
)

// SceneLabel 场景显示名
var SceneLabel = map[string]string{
	Scene123:     "123 频道订阅白名单",
	SceneGuangya: "光鸭频道订阅白名单",
	Scene139:     "移动云盘频道订阅白名单",
}

// Rule 单条可视化规则（关键词语义，逐条卡片）
type Rule struct {
	ID        string `json:"id"`         // 稳定 ID（保存后不变）
	MediaName string `json:"media_name"` // 匹配关键词/片名
	MediaType string `json:"media_type"` // movie / tv / 空=通用
	TmdbID    int64  `json:"tmdb_id,omitempty"`
	Title     string `json:"title,omitempty"`      // TMDB 标题快照
	PosterURL string `json:"poster_url,omitempty"` // TMDB 海报（w185）
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Config 场景配置（持久化为 discovery_settings 键 visualfilter_<scene>）
type Config struct {
	Scene     string `json:"scene"`
	Label     string `json:"label"`
	ParseMode string `json:"parse_mode"` // all=全转存 / advanced=仅规则命中 / meta=元数据正则
	RawRegex  string `json:"raw_regex"`  // 正则视图（与规则卡片互相同步：advanced 模式下 | 分隔）
	Rules     []Rule `json:"rules"`
	UpdatedAt string `json:"updated_at"`
}

// sceneKey 场景存储键
func sceneKey(scene string) string {
	return "visualfilter_" + strings.ToLower(strings.TrimSpace(scene))
}

// ParseModeFromAllMode 对齐 tgto123 all_mode 语义
func ParseModeFromAllMode(allMode bool) string {
	if allMode {
		return "all"
	}
	return "advanced"
}

// GetConfig 读取场景配置（不存在返回默认 advanced/空规则）
func GetConfig(scene string) *Config {
	scene = normalizeScene(scene)
	cfg := &Config{
		Scene:     scene,
		Label:     SceneLabel[scene],
		ParseMode: "advanced",
		Rules:     []Rule{},
	}
	raw, ok := discoverySettingGet(sceneKey(scene))
	if !ok {
		return cfg
	}
	var stored Config
	if json.Unmarshal([]byte(raw), &stored) == nil && stored.Scene == cfg.Scene {
		stored.Scene = cfg.Scene
		stored.Label = cfg.Label
		if stored.Rules == nil {
			stored.Rules = []Rule{}
		}
		if stored.ParseMode == "" {
			stored.ParseMode = "advanced"
		}
		return &stored
	}
	return cfg
}

// SaveConfig 保存场景配置（规则 ID 缺省自动生成；同步 RawRegex = enabled 规则的 | 连接）
func SaveConfig(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, nil
	}
	cfg.Scene = normalizeScene(cfg.Scene)
	cfg.Label = SceneLabel[cfg.Scene]
	now := time.Now().Format(time.RFC3339)
	for i := range cfg.Rules {
		if strings.TrimSpace(cfg.Rules[i].ID) == "" {
			cfg.Rules[i].ID = randomRuleID()
		}
		if cfg.Rules[i].CreatedAt == "" {
			cfg.Rules[i].CreatedAt = now
		}
	}
	cfg.UpdatedAt = now
	cfg.RawRegex = SyncRawRegex(cfg.Rules)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := discoverySettingSet(sceneKey(cfg.Scene), string(raw)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SyncRawRegex 由规则生成正则（enabled 规则按 | 连接，转义特殊字符交给调用方语义——
// 这里保持关键词原文：频道匹配器 MatchKeywords 本就是子串/正则二义兼容）
func SyncRawRegex(rules []Rule) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		name := strings.TrimSpace(r.MediaName)
		if name == "" {
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, "|")
}

// RawRegexToRules 从正则字符串拆出规则卡片（| 分隔，保留既有规则的海报等快照）
func RawRegexToRules(rawRegex string, existing []Rule) []Rule {
	existingByName := make(map[string]Rule, len(existing))
	for _, r := range existing {
		existingByName[strings.TrimSpace(r.MediaName)] = r
	}
	parts := strings.Split(rawRegex, "|")
	rules := make([]Rule, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			continue
		}
		if old, ok := existingByName[name]; ok {
			rules = append(rules, old)
			continue
		}
		rules = append(rules, Rule{ID: "", MediaName: name, Enabled: true})
	}
	return rules
}

// ScenesResponse 场景清单
type ScenesResponse struct {
	Scenes      map[string]*Config `json:"scenes"`
	SceneLabels map[string]string  `json:"scene_labels"`
}

// GetAllScenes 全部场景配置
func GetAllScenes() *ScenesResponse {
	resp := &ScenesResponse{
		Scenes:      map[string]*Config{},
		SceneLabels: map[string]string{},
	}
	for scene, label := range SceneLabel {
		resp.Scenes[scene] = GetConfig(scene)
		resp.SceneLabels[scene] = label
	}
	return resp
}

// discoverySettingGet / Set 复用 discovery_settings 键值表
func discoverySettingGet(key string) (string, bool) {
	var row struct {
		Value string
	}
	if err := db.Db.Table("discovery_settings").Select("value").Where("`key` = ?", key).Take(&row).Error; err != nil {
		return "", false
	}
	return row.Value, true
}

func discoverySettingSet(key, value string) error {
	setting := discovery.DiscoverySetting{Key: key, Value: value, UpdatedAt: time.Now()}
	return db.Db.Save(&setting).Error
}

func normalizeScene(scene string) string {
	s := strings.ToLower(strings.TrimSpace(scene))
	switch s {
	case "env_filter", "123":
		return Scene123
	case "env_filter_guangya", "guangya", "guangyapan":
		return SceneGuangya
	case "env_filter_189", "139", "pan139":
		return Scene139
	default:
		return Scene123
	}
}

// randomRuleID 生成不重复的规则 ID（crypto/rand 8 字节 hex）
func randomRuleID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "r" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "r" + hex.EncodeToString(b)
}
