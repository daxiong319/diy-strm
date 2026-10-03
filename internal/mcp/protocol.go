// Package mcp 实现 MCP（Model Context Protocol）Server 侧能力。
//
// 本包不引入任何第三方依赖：MCP 的 JSON-RPC 2.0 协议层、Streamable HTTP
// 与兼容的 HTTP+SSE 传输均基于标准库手写实现。
//
// 本包只把 litepan 已有的业务能力包装成 MCP 工具暴露出去，
// 不实现任何新的业务逻辑——工具的实现必须转发到既有函数。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// JSON-RPC 2.0 标准错误码。
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
)

// JSONRPCVersion 是唯一支持的 JSON-RPC 版本。
const JSONRPCVersion = "2.0"

// Request 表示一条 JSON-RPC 请求或通知。
// 通知与请求的区别只在于是否带 id。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification 判断是否为通知（无 id）。规范规定通知不得返回响应体。
func (r *Request) IsNotification() bool {
	if r == nil {
		return false
	}
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// Response 表示一条 JSON-RPC 响应。
// Result 与 Error 互斥：成功时填 Result，失败时填 Error。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError 表示 JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error 让 RPCError 满足 error 接口，便于直接返回与包装。
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("JSON-RPC 错误 %d：%s", e.Code, e.Message)
}

// newRPCError 构造一个 RPCError。
func newRPCError(code int, message string) *RPCError {
	return &RPCError{Code: code, Message: message}
}

// newRPCErrorf 构造带格式化消息的 RPCError。
func newRPCErrorf(code int, format string, args ...any) *RPCError {
	return &RPCError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// NewSuccessResponse 构造成功响应。id 为空时按规范填 null。
func NewSuccessResponse(id json.RawMessage, result any) *Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

// NewErrorResponse 构造错误响应。
func NewErrorResponse(id json.RawMessage, err *RPCError) *Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Error: err}
}

// ParseRequest 解析请求体。
//
// 对 jsonrpc 字段采取宽容策略：完全缺失时按 2.0 处理（部分客户端会省略），
// 但填了非 2.0 的值则明确报错，避免把不兼容的协议悄悄当成兼容处理。
func ParseRequest(data []byte) (*Request, *RPCError) {
	if len(trimJSONSpace(data)) == 0 {
		return nil, newRPCError(ErrCodeParseError, "请求体为空")
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, newRPCErrorf(ErrCodeParseError, "解析 JSON 失败：%v", err)
	}
	if req.JSONRPC != JSONRPCVersion {
		if req.JSONRPC != "" {
			return nil, newRPCErrorf(ErrCodeInvalidRequest, "不支持的 jsonrpc 版本：%s", req.JSONRPC)
		}
		req.JSONRPC = JSONRPCVersion
	}
	if req.Method == "" {
		return nil, newRPCError(ErrCodeInvalidRequest, "缺少 method 字段")
	}
	return &req, nil
}

// DecodeParams 把 params 解到目标结构。
// params 允许为空或 null；数组形式按规范报参数错误（本服务端只接受对象）。
func DecodeParams(params json.RawMessage, target any) *RPCError {
	trimmed := trimJSONSpace(params)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '[' {
		return newRPCError(ErrCodeInvalidParams, "params 必须为对象")
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(trimmed, target); err != nil {
		return newRPCErrorf(ErrCodeInvalidParams, "解析 params 失败：%v", err)
	}
	return nil
}

// trimJSONSpace 去掉 JSON 报文两端的空白字符（只处理 ASCII 空白）。
func trimJSONSpace(raw []byte) []byte {
	start := 0
	end := len(raw)
	for start < end && isJSONSpace(raw[start]) {
		start++
	}
	for end > start && isJSONSpace(raw[end-1]) {
		end--
	}
	return raw[start:end]
}

// isJSONSpace 判断是否为 JSON 允许的空白字符。
func isJSONSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

// 工具层的公共错误。用哨兵错误便于调用方 errors.Is 判断。
var (
	// ErrServerDisabled 表示 MCP Server 总开关未开启。
	ErrServerDisabled = errors.New("MCP Server 未启用")
	// ErrUnknownTool 表示请求的工具名不在注册表中。
	ErrUnknownTool = errors.New("未找到该工具")
	// ErrToolDisabled 表示工具被配置禁用了。
	ErrToolDisabled = errors.New("该工具已被禁用")
)
