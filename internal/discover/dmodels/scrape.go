// Package dmodels 为发现板块移植提供老 diy-strm/internal/models 的最小适配，
// 核心是 GlobalScrapeSettings（TMDB 配置中心），从 LitePan settings 读取配置。
package dmodels

import (
	"encoding/json"
	"strings"
	"sync"

	"litepan/internal/discover/tmdb"
	"litepan/internal/settings"
)

// TMDB 默认值（对齐老 diy-strm/helpers）。
const (
	DEFAULT_TMDB_API_URL   = "https://api.themoviedb.org"
	DEFAULT_TMDB_IMAGE_URL = "https://image.tmdb.org"
	DEFAULT_TMDB_LANGUAGE  = "zh-CN"
	// DEFAULT_TMDB_API_KEY 老代码内置的兜底 key；LitePan 留空，强制用户配置或走 access token。
)

// ScrapeSettings 对齐老 models.ScrapeSettings 的 TMDB 相关字段子集
// （发现板块只用 TMDB 部分；AI/Fanart 等不在发现板块范围）。
type ScrapeSettings struct {
	TmdbUrl         string
	TmdbImageUrl    string
	TmdbApiKey      string
	TmdbAccessToken string
	TmdbLanguage    string
}

// GetTmdbApiKey 返回 TMDB API Key（空则返回内置兜底）。
func (s *ScrapeSettings) GetTmdbApiKey() string {
	if s.TmdbApiKey == "" {
		return ""
	}
	return s.TmdbApiKey
}

func (s *ScrapeSettings) GetTmdbAccessToken() string { return s.TmdbAccessToken }

func (s *ScrapeSettings) GetTmdbApiUrl() string {
	if s.TmdbUrl == "" {
		return DEFAULT_TMDB_API_URL
	}
	return s.TmdbUrl
}

func (s *ScrapeSettings) GetTmdbImageUrl() string {
	if s.TmdbImageUrl == "" {
		return DEFAULT_TMDB_IMAGE_URL
	}
	return s.TmdbImageUrl
}

func (s *ScrapeSettings) GetTmdbLanguage() string {
	if s.TmdbLanguage == "" {
		return DEFAULT_TMDB_LANGUAGE
	}
	return s.TmdbLanguage
}

// GetTmdbClient 对齐老方法：构造/复用全局 tmdb 客户端。
func (s *ScrapeSettings) GetTmdbClient() *tmdb.Client {
	return tmdb.NewClient(
		s.GetTmdbApiKey(),
		s.GetTmdbAccessToken(),
		s.GetTmdbApiUrl(),
		s.GetTmdbLanguage(),
		s.GetProxyUrl(),
	)
}

// GetProxyUrl 返回代理 URL（发现板块暂未接 LitePan 代理设置，留空=直连）。
func (s *ScrapeSettings) GetProxyUrl() string { return "" }

// ---------------------------------------------------------------------------
// GlobalScrapeSettings：包级全局，方法调用时惰性从 LitePan settings 刷新 TMDB 配置。
// ---------------------------------------------------------------------------

var (
	settingsSvc *settings.Service
	settingsMu  sync.RWMutex
)

// GlobalScrapeSettings 对应老 models.GlobalScrapeSettings（包级全局变量）。
var GlobalScrapeSettings = &ScrapeSettings{}

// BindSettings 在装配层注入 LitePan settings 服务，并立即刷新一次缓存。
func BindSettings(svc *settings.Service) {
	settingsMu.Lock()
	settingsSvc = svc
	settingsMu.Unlock()
	RefreshFromSettings()
}

// RefreshFromSettings 从 LitePan settings 读取 TMDB 配置写入 GlobalScrapeSettings。
func RefreshFromSettings() {
	settingsMu.RLock()
	svc := settingsSvc
	settingsMu.RUnlock()
	if svc == nil {
		return
	}
	GlobalScrapeSettings.TmdbApiKey = strings.TrimSpace(svc.String(settings.KeyMOTmdbAPIKey))
	GlobalScrapeSettings.TmdbLanguage = strings.TrimSpace(svc.String(settings.KeyMOTmdbLanguage))
	GlobalScrapeSettings.TmdbUrl = strings.TrimSpace(svc.String(settings.KeyMOTmdbAPIHost))
	GlobalScrapeSettings.TmdbImageUrl = strings.TrimSpace(svc.String(settings.KeyMOTmdbImageHost))
}

// GetTmdbImageUrl 包级函数：拼完整图片 URL（对齐老 models.GetTmdbImageUrl）。
func GetTmdbImageUrl(path string) string {
	if path == "" {
		return ""
	}
	base := GlobalScrapeSettings.GetTmdbImageUrl()
	if strings.HasSuffix(base, "/t/p") || strings.Contains(base, "/t/p") {
		return base + "/original" + path
	}
	return base + "/t/p/original" + path
}

// ---------------------------------------------------------------------------
// Emby 配置（发现板块 embycheck 用）：从 LitePan settings 的 emby_proxy_instances 读第一个实例
// ---------------------------------------------------------------------------

// EmbyConfig 对齐老 models.EmbyConfig 发现板块所需的字段子集。
type EmbyConfig struct {
	EmbyUrl    string
	EmbyApiKey string
}

// embyProxyInstance 对应 LitePan embyproxy.Config 的 JSON 形态。
type embyProxyInstance struct {
	EmbyURL string `json:"emby_url"`
	APIKey  string `json:"api_key"`
}

// GetEmbyConfig 返回第一个已配置 Emby/Jellyfin 实例的地址与 Key（对齐老 models.GetEmbyConfig）。
// 未配置时返回 (nil, nil)，调用方判空。
func GetEmbyConfig() (*EmbyConfig, error) {
	settingsMu.RLock()
	svc := settingsSvc
	settingsMu.RUnlock()
	if svc == nil {
		return nil, nil
	}
	raw := svc.String("emby_proxy_instances")
	if strings.TrimSpace(raw) == "" || raw == "[]" {
		return nil, nil
	}
	var instances []embyProxyInstance
	if err := json.Unmarshal([]byte(raw), &instances); err != nil || len(instances) == 0 {
		return nil, nil
	}
	first := instances[0]
	if first.EmbyURL == "" || first.APIKey == "" {
		return nil, nil
	}
	return &EmbyConfig{EmbyUrl: first.EmbyURL, EmbyApiKey: first.APIKey}, nil
}

// ---------------------------------------------------------------------------
// TG 频道停机追赶（对应老 models.GetHiveChannelCatchupHours）
// ---------------------------------------------------------------------------

// DefaultChannelCatchupHours TG 频道停机追赶窗口默认值（小时）。
// 对齐参考实现语义：停机超过该时长就从最新消息开始，而不是深翻全部积压。
const DefaultChannelCatchupHours = 12

// GetChannelCatchupHours 返回 TG 频道停机追赶窗口（小时）。
// 0 = 不限：停机多久都从旧游标一路回补（原行为）。settings 未装配或读取失败时用默认值。
func GetChannelCatchupHours() int {
	if catchupHoursOverride >= 0 {
		return catchupHoursOverride
	}
	settingsMu.RLock()
	svc := settingsSvc
	settingsMu.RUnlock()
	if svc == nil {
		// 未装配 settings（如单元测试）：按参考实现默认值，不改变库中原行为
		return DefaultChannelCatchupHours
	}
	return svc.Int(settings.KeyDiscoverChannelCatchupHours)
}

// catchupHoursOverride 测试钩子：非负值优先于设置服务，用于在不依赖 settings.Service
// 的情况下验证追赶窗口的边界行为（0=不限、超窗跳转）。
var catchupHoursOverride = -1

// SetChannelCatchupHoursForTest 覆盖追赶窗口（仅测试用；负值恢复为读取设置服务）。
func SetChannelCatchupHoursForTest(hours int) { catchupHoursOverride = hours }
