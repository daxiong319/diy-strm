package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"litepan/internal/settings"
)

// 助理运行参数的默认值与上下限。
//
// 上下限的意义是「防止配置写坏」：轮数太大可能让一次对话反复调用工具，
// 超时太长会长期占住 SSE 连接。
const (
	// DefaultMaxToolRounds 是助手默认的最大工具调用轮数。
	DefaultMaxToolRounds = 6
	// MaxToolRounds 是允许配置的最大工具调用轮数。
	MaxToolRounds = 20
	// DefaultTimeout 是助手默认的 LLM 单次请求超时（秒）。
	DefaultTimeout = 120
	// MaxTimeout 是允许配置的最大超时（秒）。
	MaxTimeout = 600
)

// DefaultAssistantPrompt 是内置助理的系统提示词。
//
// 内容不只是「人设」，更重要的是两条约束：优先用工具查事实（避免编造媒体库状态），
// 以及写操作必须先取得用户明确同意（避免 LLM 自作主张删文件）。
const DefaultAssistantPrompt = `你是 litepan 网盘与媒体库管理助理，负责帮助用户查询和管理影视订阅、下载队列、网盘文件等。

工作原则：
1. 需要事实时优先调用工具查询，不要凭猜测回答；工具返回的内容才是当前真实状态。
2. 只读工具（查询类）可以直接调用；写操作工具（新增/删除/重命名/移动/暂停等）必须先向用户说明将要执行的操作并取得明确同意后再调用。
3. 工具返回错误时，向用户说明失败原因并给出可行的下一步，不要反复重试同一个失败调用。
4. 回答使用简体中文，简洁准确；列举结果时使用 Markdown 列表或表格，避免堆砌原始 JSON。`

// Config 是 MCP 模块的全部配置。
//
// 持久化在既有 configs 表中（见 internal/settings/registry.go 的 mcp_* 键），
// 不另外建单行配置表：与项目其它设置保持一致，也能随备份一起导入导出。
type Config struct {
	// Enabled 是 MCP Server 总开关（控制外部 MCP 客户端能否调用工具）。
	Enabled bool `json:"enabled"`
	// AllowWriteTools 决定写类工具是否对 LLM 可见。
	AllowWriteTools bool `json:"allow_write_tools"`
	// DisabledTools 是被显式禁用的工具名。
	DisabledTools []string `json:"disabled_tools"`
	// AssistantEnabled 是站内智能助理的总开关。
	AssistantEnabled bool `json:"assistant_enabled"`
	// AssistantBaseURL 是助理专用 LLM 地址；留空则回退到 AI 识别配置。
	AssistantBaseURL string `json:"assistant_base_url"`
	// AssistantAPIKey 是助理专用 API Key；留空则回退到 AI 识别配置。
	AssistantAPIKey string `json:"assistant_api_key"`
	// AssistantModel 是助理专用模型名；留空则回退到 AI 识别配置。
	AssistantModel string `json:"assistant_model"`
	// AssistantPrompt 是系统提示词；留空使用 DefaultAssistantPrompt。
	AssistantPrompt string `json:"assistant_prompt"`
	// MaxToolRounds 是单次对话最多允许的工具调用轮数。
	MaxToolRounds int `json:"max_tool_rounds"`
	// Timeout 是单次 LLM 请求超时（秒）。
	Timeout int `json:"timeout"`
}

// SettingsReader 是本模块对设置服务的依赖（只读）。
//
// 用接口而不是直接吃 *settings.Service，是为了让单元测试不必构造整套设置服务。
type SettingsReader interface {
	String(key string) string
	// StringAllowEmpty 保留「被显式写成空」的语义：
	// 助理 API Key 与提示词允许为空，此时不能回落到默认值。
	StringAllowEmpty(key string) string
	Int(key string) int
	Bool(key string) bool
}

// ConfigFromSettings 从设置服务读出 MCP 配置并归一化。
func ConfigFromSettings(reader SettingsReader) *Config {
	if reader == nil {
		return DefaultConfig()
	}
	cfg := &Config{
		Enabled:          reader.Bool(settings.KeyMcpEnabled),
		AllowWriteTools:  reader.Bool(settings.KeyMcpAllowWriteTools),
		DisabledTools:    ParseDisabledTools(reader.String(settings.KeyMcpDisabledTools)),
		AssistantEnabled: reader.Bool(settings.KeyMcpAssistantEnabled),
		AssistantBaseURL: strings.TrimSpace(reader.String(settings.KeyMcpAssistantBaseURL)),
		AssistantAPIKey:  strings.TrimSpace(reader.StringAllowEmpty(settings.KeyMcpAssistantAPIKey)),
		AssistantModel:   strings.TrimSpace(reader.String(settings.KeyMcpAssistantModel)),
		AssistantPrompt:  strings.TrimSpace(reader.StringAllowEmpty(settings.KeyMcpAssistantPrompt)),
		MaxToolRounds:    reader.Int(settings.KeyMcpMaxToolRounds),
		Timeout:          reader.Int(settings.KeyMcpTimeout),
	}
	return cfg.Normalized()
}

// DefaultConfig 返回内置默认配置（测试与设置服务不可用时使用）。
func DefaultConfig() *Config {
	return (&Config{
		AssistantEnabled: true,
		AssistantPrompt:  "",
		MaxToolRounds:    DefaultMaxToolRounds,
		Timeout:          DefaultTimeout,
	}).Normalized()
}

// Normalized 归一化数值与清单字段，保证越界配置不会传到运行时。
func (c *Config) Normalized() *Config {
	if c == nil {
		return DefaultConfig()
	}
	c.MaxToolRounds = NormalizeMaxToolRounds(c.MaxToolRounds)
	c.Timeout = NormalizeTimeout(c.Timeout)
	c.DisabledTools = trimStringList(c.DisabledTools)
	return c
}

// EffectivePrompt 返回实际生效的提示词（空配置回落到内置默认）。
func (c *Config) EffectivePrompt() string {
	if c == nil || strings.TrimSpace(c.AssistantPrompt) == "" {
		return DefaultAssistantPrompt
	}
	return c.AssistantPrompt
}

// Snapshot 转成工具层使用的可见性快照。
func (c *Config) Snapshot() *McpConfigSnapshot {
	if c == nil {
		return &McpConfigSnapshot{}
	}
	return &McpConfigSnapshot{
		Enabled:         c.Enabled,
		AllowWriteTools: c.AllowWriteTools,
		DisabledTools:   append([]string(nil), c.DisabledTools...),
	}
}

// NormalizeMaxToolRounds 把轮数收敛到 [1, MaxToolRounds]；非法值回默认。
func NormalizeMaxToolRounds(v int) int {
	if v <= 0 || v > MaxToolRounds {
		return DefaultMaxToolRounds
	}
	return v
}

// NormalizeTimeout 把超时收敛到 [10, MaxTimeout]；非法值回默认。
func NormalizeTimeout(v int) int {
	if v <= 0 || v > MaxTimeout {
		return DefaultTimeout
	}
	return v
}

// ParseDisabledTools 解析禁用工具清单。
//
// 解析失败时退回「按逗号切分」而不是报错：配置损坏不该让整个 MCP 功能不可用，
// 退回逗号切分至少能保住用户手写的意图。
func ParseDisabledTools(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
		return trimStringList(strings.Split(trimmed, ","))
	}
	return trimStringList(list)
}

// MarshalDisabledTools 序列化禁用工具清单，失败时返回 "[]" 而不是让保存失败。
func MarshalDisabledTools(list []string) string {
	if len(list) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(trimStringList(list))
	if err != nil {
		packageLog().Warn("序列化 MCP 禁用工具清单失败", "error", err)
		return "[]"
	}
	return string(raw)
}

// ConfigWriter 是本模块对设置服务的依赖（写入）。
type ConfigWriter interface {
	Update(ctx context.Context, in map[string]string) error
}

// UpdateConfig 把配置写回设置服务。
//
// apiKey 语义与前端表单一致：
//   - clearAPIKey 为 true 时清空；
//   - 否则 apiKey 非空则替换；
//   - 否则保持原值不变（前端从不回显密钥，因此「空」只能理解为「不改」）。
func UpdateConfig(ctx context.Context, writer ConfigWriter, cfg, current *Config, clearAPIKey bool) error {
	if writer == nil {
		return ErrServerDisabled
	}
	next := cfg.Normalized()
	apiKey := next.AssistantAPIKey
	switch {
	case clearAPIKey:
		apiKey = ""
	case strings.TrimSpace(apiKey) == "" && current != nil:
		apiKey = current.AssistantAPIKey
	}
	in := map[string]string{
		settings.KeyMcpEnabled:          boolString(next.Enabled),
		settings.KeyMcpAllowWriteTools:  boolString(next.AllowWriteTools),
		settings.KeyMcpDisabledTools:    MarshalDisabledTools(next.DisabledTools),
		settings.KeyMcpAssistantEnabled: boolString(next.AssistantEnabled),
		settings.KeyMcpAssistantBaseURL: next.AssistantBaseURL,
		settings.KeyMcpAssistantAPIKey:  apiKey,
		settings.KeyMcpAssistantModel:   next.AssistantModel,
		settings.KeyMcpAssistantPrompt:  next.AssistantPrompt,
		settings.KeyMcpMaxToolRounds:    itoa(next.MaxToolRounds),
		settings.KeyMcpTimeout:          itoa(next.Timeout),
	}
	return writer.Update(ctx, in)
}

// itoa 是 strconv.Itoa 的短别名，避免为一个转换引入额外 import 噪音。
func itoa(v int) string {
	return strconv.Itoa(v)
}

// boolString 把布尔转成设置表里存的字符串。
func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// EffectiveAssistant 是助理实际生效的运行参数（已解析回落）。
type EffectiveAssistant struct {
	BaseURL   string
	APIKey    string
	ModelName string
	Prompt    string
}

// EffectiveAssistantConfig 解析助理实际生效的参数。
//
// 回落规则（与设置页提示一致）：
//   - MCP 助理自己填了值就用自己填的；
//   - 留空则回落到「AI 识别设置」（ai_organize_*），避免用户把同一套
//     LLM 凭据填两遍；
//   - 提示词为空时回落到内置 DefaultAssistantPrompt。
//
// 注意 APIKey 的特殊性：它可能来自 ai_organize_api_key，
// 因此调用方**绝不能**把本结构直接序列化返回给前端（会泄露密钥），
// 只能用于「是否已配置」的布尔判断与实际调用。
func EffectiveAssistantConfig(cfg *Config) EffectiveAssistant {
	c := cfg
	if c == nil {
		c = DefaultConfig()
	}
	eff := EffectiveAssistant{
		BaseURL:   strings.TrimSpace(c.AssistantBaseURL),
		APIKey:    strings.TrimSpace(c.AssistantAPIKey),
		ModelName: strings.TrimSpace(c.AssistantModel),
		Prompt:    c.EffectivePrompt(),
	}
	return eff
}

// EffectiveAssistantConfigFromSettings 在配置基础上再回落读 AI 识别设置。
//
// 与 EffectiveAssistantConfig 分开，是为了让「只按 MCP 自身配置判断」的
// 纯逻辑测试不必构造设置服务。
func EffectiveAssistantConfigFromSettings(reader SettingsReader, cfg *Config) EffectiveAssistant {
	eff := EffectiveAssistantConfig(cfg)
	if reader == nil {
		return eff
	}
	if eff.BaseURL == "" {
		eff.BaseURL = strings.TrimSpace(reader.String(settings.KeyAIOrganizeBaseURL))
	}
	if eff.APIKey == "" {
		eff.APIKey = strings.TrimSpace(reader.StringAllowEmpty(settings.KeyAIOrganizeAPIKey))
	}
	if eff.ModelName == "" {
		eff.ModelName = strings.TrimSpace(reader.String(settings.KeyAIOrganizeModel))
	}
	return eff
}
