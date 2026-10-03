package subtitle

import (
	"strconv"
	"strings"

	"litepan/internal/settings"
)

// Config 是字幕模块的运行时配置快照。
//
// 对齐老版 diy-strm 的 models.SubtitleConfig（那边是单行 subtitle_configs 表），
// 现版改为从 LitePan 的声明式设置注册表读取：每个字段对应 registry.go 里的一个
// KeySubtitle* 常量。抽成结构体是为了让匹配/检索/校正这些纯逻辑不依赖 settings 包，
// 也方便单测直接构造配置而不动全局设置。
type Config struct {
	Enabled bool

	// 来源开关与凭证。
	AssrtEnabled           bool
	AssrtAPIKey            string
	OpenSubtitlesEnabled   bool
	OpenSubtitlesAPIKey    string
	OpenSubtitlesUserAgent string
	OpenSubtitlesUsername  string
	OpenSubtitlesPassword  string
	SubhdEnabled           bool
	SubhdCookie            string
	ZimukuEnabled          bool
	ZimukuCookie           string

	// 匹配策略。
	LanguagePriority string
	FormatPriority   string
	AutoMatch        bool
	AutoDownload     bool
	MinMatchScore    int

	// 时间轴校正。
	AutoSync          bool
	SyncMinConfidence float64
	SyncMode          string
	SyncDryRun        bool

	// 落盘策略。
	TargetDirPolicy string
	KeepOriginal    bool
	Overwrite       bool

	// 运行参数。
	Concurrency    int
	TimeoutSeconds int
	Proxy          string
	UserAgent      string
}

// ConfigReader 是读取字幕配置所需的最小设置接口。
// *settings.Service 天然满足；接口化是为了单测能注入假实现。
type ConfigReader interface {
	String(key string) string
	Int(key string) int
	Bool(key string) bool
}

// DefaultConfig 返回与 registry.go 默认值一致的配置。
// settings 服务不可用时（例如未注入）作为兜底，避免模块因缺配置而 panic。
func DefaultConfig() Config {
	return Config{
		LanguagePriority:       "zh-cn,zh-tw,zh,en",
		FormatPriority:         "ass,srt,ssa,sub,sup",
		AutoDownload:           true,
		MinMatchScore:          60,
		SyncMinConfidence:      0.7,
		SyncMode:               "vad",
		SyncDryRun:             true,
		TargetDirPolicy:        "same",
		KeepOriginal:           true,
		Concurrency:            2,
		TimeoutSeconds:         30,
		OpenSubtitlesUserAgent: "litepan v1.0",
	}
}

// LoadConfig 从设置注册表读出字幕配置。
// reader 为 nil 时直接返回默认配置（与老版 LoadSubtitleConfig 的兜底语义一致）。
func LoadConfig(reader ConfigReader) Config {
	cfg := DefaultConfig()
	if reader == nil {
		return cfg
	}
	cfg.Enabled = reader.Bool(settings.KeySubtitleEnabled)

	cfg.AssrtEnabled = reader.Bool(settings.KeySubtitleAssrtEnabled)
	cfg.AssrtAPIKey = reader.String(settings.KeySubtitleAssrtAPIKey)
	cfg.OpenSubtitlesEnabled = reader.Bool(settings.KeySubtitleOpenSubtitlesEnabled)
	cfg.OpenSubtitlesAPIKey = reader.String(settings.KeySubtitleOpenSubtitlesAPIKey)
	cfg.OpenSubtitlesUsername = reader.String(settings.KeySubtitleOpenSubtitlesUsername)
	cfg.OpenSubtitlesPassword = reader.String(settings.KeySubtitleOpenSubtitlesPassword)
	cfg.SubhdEnabled = reader.Bool(settings.KeySubtitleSubhdEnabled)
	cfg.SubhdCookie = reader.String(settings.KeySubtitleSubhdCookie)
	cfg.ZimukuEnabled = reader.Bool(settings.KeySubtitleZimukuEnabled)
	cfg.ZimukuCookie = reader.String(settings.KeySubtitleZimukuCookie)

	if ua := reader.String(settings.KeySubtitleOpenSubtitlesUserAgent); ua != "" {
		cfg.OpenSubtitlesUserAgent = ua
	}

	if v := reader.String(settings.KeySubtitleLanguagePriority); v != "" {
		cfg.LanguagePriority = v
	}
	if v := reader.String(settings.KeySubtitleFormatPriority); v != "" {
		cfg.FormatPriority = v
	}
	cfg.AutoMatch = reader.Bool(settings.KeySubtitleAutoMatch)
	cfg.AutoDownload = reader.Bool(settings.KeySubtitleAutoDownload)
	cfg.MinMatchScore = reader.Int(settings.KeySubtitleMinMatchScore)

	cfg.AutoSync = reader.Bool(settings.KeySubtitleAutoSync)
	cfg.SyncMinConfidence = parseFloatDefault(reader.String(settings.KeySubtitleSyncMinConfidence), cfg.SyncMinConfidence)
	if v := reader.String(settings.KeySubtitleSyncMode); v != "" {
		cfg.SyncMode = v
	}
	cfg.SyncDryRun = reader.Bool(settings.KeySubtitleSyncDryRun)

	if v := reader.String(settings.KeySubtitleTargetDirPolicy); v != "" {
		cfg.TargetDirPolicy = v
	}
	cfg.KeepOriginal = reader.Bool(settings.KeySubtitleKeepOriginal)
	cfg.Overwrite = reader.Bool(settings.KeySubtitleOverwrite)

	cfg.Concurrency = reader.Int(settings.KeySubtitleConcurrency)
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}
	cfg.TimeoutSeconds = reader.Int(settings.KeySubtitleTimeoutSeconds)
	cfg.Proxy = reader.String(settings.KeySubtitleProxy)
	cfg.UserAgent = reader.String(settings.KeySubtitleUserAgent)
	return cfg
}

// parseFloatDefault 解析浮点设置项；空串或非法值回落 def 并夹到 [0,1]。
func parseFloatDefault(raw string, def float64) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
