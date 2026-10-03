package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Tool 是 MCP 工具的统一抽象。
//
// 实现约定（务必遵守，否则工具层会变成第二套业务实现）：
//   - 只做「参数解析 + 调用既有函数」，不在工具里实现新的业务逻辑；
//   - 会改变媒体库/网盘状态的操作必须 ReadOnly() 返回 false，
//     由调用方（配置开关 + LLM 提示词）保证调用前向用户确认。
type Tool interface {
	// Name 是工具名，必须全局唯一且稳定（前端禁用清单按名字匹配）。
	Name() string
	// Description 给 LLM 看的功能说明，应当说明「做什么」与「会改变什么」。
	Description() string
	// InputSchema 是 JSON Schema 形式的入参定义。
	InputSchema() json.RawMessage
	// ReadOnly 表示该工具是否只读（不产生副作用）。
	ReadOnly() bool
	// Handler 执行工具，返回可序列化结果。
	Handler(ctx context.Context, args map[string]any) (any, error)
}

// ToolContext 预留给工具的执行上下文（目前工具直接从各自子系统读配置）。
type ToolContext struct {
	Config *McpConfigSnapshot
}

// McpConfigSnapshot 是工具可见性判断所需的配置快照。
//
// 刻意不依赖任何持久化包：这样单元测试无需数据库即可覆盖可见性逻辑。
type McpConfigSnapshot struct {
	// Enabled 是 MCP Server 总开关。
	Enabled bool
	// AllowWriteTools 决定写类工具是否可用。
	AllowWriteTools bool
	// DisabledTools 是被显式禁用的工具名（忽略大小写与首尾空白）。
	//
	// 采用「禁用清单」而非「启用清单」：升级后新增的工具默认可用，
	// 不需要用户重新勾选一遍。
	DisabledTools []string
}

// Registry 是工具注册表，按注册顺序保存工具。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册工具。同名的后注册覆盖先注册的（测试常用），
// 顺序只在首次出现时追加，保证列表稳定。
func (r *Registry) Register(tools ...Tool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.Name()) == "" {
			continue
		}
		name := tool.Name()
		if _, exists := r.tools[name]; !exists {
			r.order = append(r.order, name)
		}
		r.tools[name] = tool
	}
}

// Get 按名字取工具（大小写不敏感，方便 LLM 拼错大小写时仍能命中）。
func (r *Registry) Get(name string) (Tool, error) {
	if r == nil {
		return nil, fmt.Errorf("%w：%s", ErrUnknownTool, name)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tool, ok := r.tools[name]; ok {
		return tool, nil
	}
	for registered, tool := range r.tools {
		if strings.EqualFold(registered, name) {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("%w：%s", ErrUnknownTool, name)
}

// All 按注册顺序返回全部工具。
func (r *Registry) All() []Tool {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if tool, ok := r.tools[name]; ok {
			out = append(out, tool)
		}
	}
	return out
}

// Visible 返回当前配置下可见（可被 LLM 调用）的工具。
func (r *Registry) Visible(snapshot *McpConfigSnapshot) []Tool {
	all := r.All()
	out := make([]Tool, 0, len(all))
	for _, tool := range all {
		if IsToolAllowed(tool, snapshot) {
			out = append(out, tool)
		}
	}
	return out
}

// IsToolAllowed 判断工具在当前配置下是否可用。
//
// snapshot 为 nil 时只放行只读工具：配置尚未加载时宁可少给能力，
// 也不能把写操作暴露出去。
func IsToolAllowed(tool Tool, snapshot *McpConfigSnapshot) bool {
	if tool == nil {
		return false
	}
	if snapshot == nil {
		return tool.ReadOnly()
	}
	if !tool.ReadOnly() && !snapshot.AllowWriteTools {
		return false
	}
	for _, disabled := range snapshot.DisabledTools {
		if strings.EqualFold(strings.TrimSpace(disabled), tool.Name()) {
			return false
		}
	}
	return true
}

// toolDescriptor 是 tools/list 返回的单个工具描述。
type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolDescriptors 把工具转成 tools/list 需要的描述列表。
func ToolDescriptors(tools []Tool) []toolDescriptor {
	out := make([]toolDescriptor, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		schema := tool.InputSchema()
		if len(trimJSONSpace(schema)) == 0 {
			schema = emptyObjectSchema()
		}
		out = append(out, toolDescriptor{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: schema,
		})
	}
	return out
}

// ToolNames 返回工具名列表。
func ToolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			out = append(out, tool.Name())
		}
	}
	return out
}

// ValidateToolNames 返回 names 中不在注册表里的工具名（已排序、去重前原样返回）。
//
// 用途：配置里写错工具名时，禁用清单会静默失效（写错名字等于没禁），
// 所以保存配置前必须先校验一遍。
func ValidateToolNames(registry *Registry, names []string) []string {
	known := make(map[string]bool)
	if registry != nil {
		for _, tool := range registry.All() {
			known[strings.ToLower(tool.Name())] = true
		}
	}
	var unknown []string
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if !known[strings.ToLower(trimmed)] {
			unknown = append(unknown, trimmed)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// ---------------------------------------------------------------------------
// JSON Schema 构造辅助
// ---------------------------------------------------------------------------

// emptyObjectSchema 返回「无参数」工具的标准 schema。
func emptyObjectSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

// objectSchema 构造对象型入参 schema。
func objectSchema(required []string, props map[string]any) json.RawMessage {
	if props == nil {
		props = map[string]any{}
	}
	payload := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		payload["required"] = required
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// schema 构造失败不该让工具从列表里消失，退回空对象更安全。
		return emptyObjectSchema()
	}
	return raw
}

// strProp 声明字符串参数。
func strProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// strEnumProp 声明枚举字符串参数。
func strEnumProp(description string, values ...string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

// intProp 声明整数参数。
func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// boolProp 声明布尔参数。
func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// boolPropDefault 声明带默认值的布尔参数。
func boolPropDefault(description string, def bool) map[string]any {
	return map[string]any{"type": "boolean", "description": description, "default": def}
}

// ---------------------------------------------------------------------------
// 参数读取辅助
//
// MCP 客户端与 LLM 经常把数字写成字符串、把布尔写成 "true" 或 0，
// 这里统一做宽松读取，避免因为一个格式差异就让工具直接失败。
// ---------------------------------------------------------------------------

// argString 读取字符串参数。
func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

// argStringSlice 读取字符串数组参数。
// 同时接受真正的数组、逗号分隔字符串与 []string。
func argStringSlice(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return trimStringList(typed)
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return trimStringList(strings.Split(typed, ","))
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text := strings.TrimSpace(fmt.Sprintf("%v", item))
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprintf("%v", typed))
		if text == "" {
			return nil
		}
		return []string{text}
	}
}

// trimStringList 去掉每项两端空白并丢弃空项。
func trimStringList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// argInt 读取整数参数。
func argInt(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		if parsed, err := typed.Float64(); err == nil {
			return int(parsed)
		}
		return 0
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil {
			return parsed
		}
		return 0
	case bool:
		if typed {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// argUint 读取无符号整数参数，负数按 0 处理（ID 为 0 表示缺省）。
func argUint(args map[string]any, key string) uint {
	value := argInt(args, key)
	if value < 0 {
		return 0
	}
	return uint(value)
}

// argBool 读取布尔参数，缺省时返回 def。
func argBool(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	value, ok := args[key]
	if !ok || value == nil {
		return def
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		default:
			return def
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed != 0
		}
		return def
	default:
		return def
	}
}

// requireString 读取必填字符串参数，缺失时报出人话错误。
func requireString(args map[string]any, key, label string) (string, error) {
	value := strings.TrimSpace(argString(args, key))
	if value == "" {
		return "", fmt.Errorf("缺少必填参数 %s（%s）", key, label)
	}
	return value, nil
}

// requireUint 读取必填正整数参数。
func requireUint(args map[string]any, key, label string) (uint, error) {
	value := argUint(args, key)
	if value == 0 {
		return 0, fmt.Errorf("缺少必填参数 %s（%s），必须为正整数", key, label)
	}
	return value, nil
}

// clampPage 归一化分页参数：页码至少 1，每页默认 20、上限 100。
func clampPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
