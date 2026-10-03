package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 协议版本。以最新版为主，同时兼容一个旧版本，避免旧客户端握手失败。
const (
	// ProtocolVersion 是本服务端首选的协议版本。
	ProtocolVersion = "2025-06-18"
	// ProtocolVersionLegacy 是兼容的旧协议版本。
	ProtocolVersionLegacy = "2024-11-05"
	// ServerName 是握手时报给客户端的服务端名。
	ServerName = "litepan-mcp"
	// ServerVersion 是握手时报给客户端的服务端版本。
	ServerVersion = "1.0.0"
)

// supportedProtocolVersions 是允许被客户端「回显」的版本集合。
var supportedProtocolVersions = map[string]bool{
	ProtocolVersion:       true,
	ProtocolVersionLegacy: true,
}

// toolCallTimeout 是单个工具执行的硬超时。
// 与助理的最大轮数配合，避免一次对话无限期占住连接。
const toolCallTimeout = 5 * time.Minute

// Server 是 MCP 服务端。
//
// 自身不持有可变状态（配置通过 snapshot 函数每次实时读取），
// 因此可以安全地被多个 HTTP 请求并发使用。
type Server struct {
	registry *Registry
	snapshot func() *McpConfigSnapshot
}

// NewServer 构造 MCP 服务端。
//
// snapshot 以函数形式传入而不是配置值：这样每次请求都能读到最新配置，
// 不需要在配置变更时重建服务端或维护变更回调。
func NewServer(registry *Registry, snapshot func() *McpConfigSnapshot) *Server {
	if registry == nil {
		registry = NewRegistry()
	}
	if snapshot == nil {
		snapshot = func() *McpConfigSnapshot { return &McpConfigSnapshot{} }
	}
	return &Server{registry: registry, snapshot: snapshot}
}

// Registry 返回工具注册表。
func (s *Server) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

// InitializeParams 是 initialize 方法的入参。
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// Implementation 是协议里的实现信息（服务端/客户端通用）。
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities 声明服务端能力。
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability 声明工具能力。
//
// ListChanged 恒为 false：工具清单只在配置变化时改变，
// 服务端不主动推送变更通知。
type ToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

// InitializeResult 是 initialize 的返回。
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// ListToolsResult 是 tools/list 的返回。
type ListToolsResult struct {
	Tools []toolDescriptor `json:"tools"`
}

// CallToolParams 是 tools/call 的入参。
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// CallToolResult 是 tools/call 的返回。
//
// 工具自身执行失败时 IsError 为 true，但仍返回成功响应：
// 让 LLM 看到失败原因并自行调整，而不是把整个协议调用判为错误。
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock 是返回内容块（当前只支持文本）。
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// HandleMessage 处理一条原始报文，返回响应。
//
// 返回 nil 表示无需响应（通知，或报文无法解析出请求 ID）。
func (s *Server) HandleMessage(ctx context.Context, data []byte) *Response {
	req, rpcErr := ParseRequest(data)
	if rpcErr != nil {
		// 解析失败时按规范回 id=null。
		return NewErrorResponse(nil, rpcErr)
	}
	if req.IsNotification() {
		s.handleNotification(req)
		return nil
	}
	return s.handleRequest(ctx, req)
}

// handleNotification 处理通知。
//
// 只识别已注册的通知方法，其余一律静默忽略：
// 对未知通知回错误会让客户端直接断开连接。
func (s *Server) handleNotification(req *Request) {
	if req == nil {
		return
	}
	switch req.Method {
	case "notifications/initialized", "notifications/cancelled":
		// 已知通知，无需处理。
	default:
		// 未识别通知按规范忽略。
	}
}

// handleRequest 分发请求。
func (s *Server) handleRequest(ctx context.Context, req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "ping":
		return NewSuccessResponse(req.ID, map[string]any{})
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "resources/list", "prompts/list":
		// 本服务端不提供资源与提示模板。返回空清单而不是「方法未实现」：
		// 部分客户端在握手阶段会调用它们，报错会导致连接中断。
		return NewSuccessResponse(req.ID, map[string]any{
			"resources": []any{},
			"prompts":   []any{},
		})
	default:
		return NewErrorResponse(req.ID, newRPCErrorf(ErrCodeMethodNotFound, "未实现的方法：%s", req.Method))
	}
}

// handleInitialize 处理握手。
//
// 版本协商采取「客户端优先」：客户端版本受支持就回显它，
// 否则回服务端首选版本。握手阶段不因版本不符直接报错，
// 否则用户只会看到一句莫名其妙的连接失败。
func (s *Server) handleInitialize(req *Request) *Response {
	var params InitializeParams
	if rpcErr := DecodeParams(req.Params, &params); rpcErr != nil {
		return NewErrorResponse(req.ID, rpcErr)
	}
	version := strings.TrimSpace(params.ProtocolVersion)
	if !supportedProtocolVersions[version] {
		version = ProtocolVersion
	}
	return NewSuccessResponse(req.ID, InitializeResult{
		ProtocolVersion: version,
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
		ServerInfo:   Implementation{Name: ServerName, Version: ServerVersion},
		Instructions: "litepan 网盘与媒体库管理工具集。查询类工具可直接调用；写类工具会改变网盘或媒体库状态，调用前请先向用户确认。",
	})
}

// handleToolsList 返回当前可见工具清单。
func (s *Server) handleToolsList(req *Request) *Response {
	visible := s.registry.Visible(s.snapshot())
	return NewSuccessResponse(req.ID, ListToolsResult{Tools: ToolDescriptors(visible)})
}

// handleToolsCall 执行工具调用。
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var params CallToolParams
	if rpcErr := DecodeParams(req.Params, &params); rpcErr != nil {
		return NewErrorResponse(req.ID, rpcErr)
	}
	if strings.TrimSpace(params.Name) == "" {
		return NewErrorResponse(req.ID, newRPCError(ErrCodeInvalidParams, "缺少参数 name（工具名）"))
	}
	tool, err := s.registry.Get(params.Name)
	if err != nil {
		return NewErrorResponse(req.ID, newRPCErrorf(ErrCodeInvalidParams, "未找到工具：%s", params.Name))
	}
	snapshot := s.snapshot()
	if !IsToolAllowed(tool, snapshot) {
		// 工具不可用属于「业务结果」而非协议错误：
		// 回成功响应 + 错误文本，LLM 才能看到原因并改用别的工具。
		return NewSuccessResponse(req.ID, toolErrorResult(
			fmt.Sprintf("工具 %s 当前不可用（未启用或被禁用）", params.Name)))
	}
	args := params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	execCtx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()
	result, execErr := executeTool(execCtx, tool, args)
	if execErr != nil {
		s.log().Warn("MCP 工具执行失败", "tool", params.Name, "error", execErr)
		return NewSuccessResponse(req.ID, toolErrorResult(execErr.Error()))
	}
	return NewSuccessResponse(req.ID, toolTextResult(result))
}

// log 返回本包的日志器；未配置时退回 slog 默认实例。
func (s *Server) log() *slog.Logger {
	if packageLogger != nil {
		return packageLogger
	}
	return slog.Default()
}

// packageLogger 是包级日志器，由宿主在启动时注入（见 SetLogger）。
var packageLogger *slog.Logger

// SetLogger 注入包级日志器。目标项目用标准库 slog，这里不依赖具体日志框架。
func SetLogger(logger *slog.Logger) {
	if logger != nil {
		packageLogger = logger
	}
}

// executeTool 执行工具并兜住 panic。
//
// 单个工具的实现缺陷不应拖垮整个 HTTP 服务，因此 panic 统一转成错误返回。
func executeTool(ctx context.Context, tool Tool, args map[string]any) (result any, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("工具执行已取消：%w", ctxErr)
	}
	defer func() {
		if r := recover(); r != nil {
			packageLog().Error("MCP 工具触发 panic", "tool", tool.Name(), "panic", r)
			err = fmt.Errorf("工具执行异常：%v", r)
		}
	}()
	return tool.Handler(ctx, args)
}

// packageLog 返回包级日志器（executeTool 等包级函数使用）。
func packageLog() *slog.Logger {
	if packageLogger != nil {
		return packageLogger
	}
	return slog.Default()
}

// toolTextResult 把工具结果包装成成功返回。
func toolTextResult(result any) CallToolResult {
	return CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: stringifyToolResult(result)}},
	}
}

// toolErrorResult 把错误包装成 IsError 的返回。
func toolErrorResult(message string) CallToolResult {
	return CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: message}},
		IsError: true,
	}
}

// stringifyToolResult 把任意工具结果转成给 LLM 看的文本。
//
// 结构化结果用缩进 JSON：LLM 对 JSON 的解析比 Go 的 %v 输出可靠得多。
func stringifyToolResult(result any) string {
	switch typed := result.(type) {
	case nil:
		return "操作已完成（无返回内容）"
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	default:
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			packageLog().Warn("MCP 工具结果序列化失败", "error", err)
			return fmt.Sprintf("%v", result)
		}
		return string(raw)
	}
}

// ToolResultText 暴露结果文本化逻辑给助手层复用。
func ToolResultText(result any) string {
	return stringifyToolResult(result)
}

// ExecuteTool 供内置助理的工具调用循环使用。
//
// 与 HTTP 路径复用同一套可见性判断与超时，保证两条入口行为一致。
func (s *Server) ExecuteTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if s == nil {
		return nil, errors.New("MCP 服务端未初始化")
	}
	tool, err := s.registry.Get(name)
	if err != nil {
		return nil, err
	}
	if !IsToolAllowed(tool, s.snapshot()) {
		return nil, fmt.Errorf("%w：%s", ErrToolDisabled, name)
	}
	if args == nil {
		args = map[string]any{}
	}
	execCtx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()
	return executeTool(execCtx, tool, args)
}

// SnapshotFromConfig 由配置构造快照（便于测试与调用方转换）。
func SnapshotFromConfig(cfg *McpConfigSnapshot) *McpConfigSnapshot {
	if cfg == nil {
		return &McpConfigSnapshot{}
	}
	return &McpConfigSnapshot{
		Enabled:         cfg.Enabled,
		AllowWriteTools: cfg.AllowWriteTools,
		DisabledTools:   append([]string(nil), cfg.DisabledTools...),
	}
}

// IsServerEnabled 判断 MCP Server 总开关是否开启。
func IsServerEnabled(snapshot *McpConfigSnapshot) bool {
	return snapshot != nil && snapshot.Enabled
}

// ErrToolExecuteFailed 表示工具执行失败（供调用方错误分类）。
var ErrToolExecuteFailed = errors.New("工具执行失败")
