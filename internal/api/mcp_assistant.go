package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/mcp"
)

// MCP 内置助理的 HTTP 层：一次性问答、流式对话（SSE）与会话历史。
//
// SSE 事件名是前后端契约的一部分，不可随意改动：
//
//	init        {session_id}
//	delta       {content}
//	tool_call   {tool_name, tool_args}
//	tool_result {tool_name, tool_result, tool_error}
//	error       {message}     ← 失败走 error 而不是 done.error：
//	                            前端对 error 有独立分支，能立刻显示原因
//	done        {reply, tool_calls, rounds}

// mcpAssistantRequest 是助理对话请求体。
type mcpAssistantRequest struct {
	Message string `json:"message"`
	// SessionID 为空时由服务端新建会话。
	SessionID string `json:"session_id"`
	// History 为前端直传的历史（对话页可不依赖落库），优先于库中记录。
	History []mcp.AssistantMessage `json:"history"`
}

// mcpAssistantOptions 解析出实际生效的助理运行参数。
//
// MCP 助理自己没填的项回落到「AI 识别设置」，避免同一套 LLM 凭据填两遍。
func (h *Handler) mcpAssistantOptions() (mcp.AssistantOptions, mcp.EffectiveAssistant, error) {
	if h.settings == nil {
		return mcp.AssistantOptions{}, mcp.EffectiveAssistant{},
			domain.Errorf(domain.CodeNotImplement, "设置服务未就绪")
	}
	cfg := mcp.ConfigFromSettings(h.settings)
	eff := mcp.EffectiveAssistantConfigFromSettings(h.settings, cfg)
	if strings.TrimSpace(eff.BaseURL) == "" || strings.TrimSpace(eff.APIKey) == "" {
		return mcp.AssistantOptions{}, eff, domain.Errorf(domain.CodeValidation,
			"助理未配置 LLM 接口：请在 MCP 设置中填写地址与 API Key，或在「AI 识别设置」中启用 AI")
	}
	return mcp.AssistantOptions{
		BaseURL:    eff.BaseURL,
		APIKey:     eff.APIKey,
		ModelName:  eff.ModelName,
		Prompt:     eff.Prompt,
		MaxRounds:  cfg.MaxToolRounds,
		TimeoutSec: cfg.Timeout,
	}, eff, nil
}

// mcpAssistantEnabled 判断助理是否启用（未启用时不允许对话）。
func (h *Handler) mcpAssistantEnabled() bool {
	if h.settings == nil {
		return false
	}
	return mcp.ConfigFromSettings(h.settings).AssistantEnabled
}

// bindMcpAssistantRequest 解析请求体并确定会话 ID。
func (h *Handler) bindMcpAssistantRequest(r *http.Request) (mcpAssistantRequest, string, error) {
	var req mcpAssistantRequest
	if err := decodeJSON(r, &req); err != nil {
		return req, "", err
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		return req, "", domain.Errorf(domain.CodeValidation, "消息内容不能为空")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = mcp.NewSessionID()
	}
	return req, sessionID, nil
}

// mcpHistoryFromRequest 把前端直传的历史转成内部表示。
func mcpHistoryFromRequest(items []mcp.AssistantMessage) []mcp.AssistantMessage {
	return items
}

// mcpAssistantChat 一次性问答（非流式）。
//
// 工具调用过程通过返回值带出，便于前端在不支持 SSE 时降级展示。
func (h *Handler) mcpAssistantChat(w http.ResponseWriter, r *http.Request) {
	if !h.mcpAssistantEnabled() {
		writeErr(w, domain.Errorf(domain.CodeValidation, "智能助理未启用"))
		return
	}
	req, sessionID, err := h.bindMcpAssistantRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	opts, _, err := h.mcpAssistantOptions()
	if err != nil {
		writeErr(w, err)
		return
	}

	// 历史优先取前端直传，否则回落到库中记录。
	history := mcpHistoryFromRequest(req.History)
	if len(history) == 0 {
		history = h.loadMcpHistory(r.Context(), sessionID)
	}
	h.appendMcpMessage(r.Context(), sessionID, "user", req.Message, "")

	var events []mcp.AssistantEvent
	runErr := h.mcpAssistantInstance().Chat(r.Context(), opts,
		mcp.HistoryFromMessages(history), req.Message,
		func(ev mcp.AssistantEvent) { events = append(events, ev) })
	if runErr != nil {
		writeErr(w, runErr)
		return
	}

	var reply strings.Builder
	for _, ev := range events {
		if ev.Type == "delta" {
			reply.WriteString(ev.Content)
		}
	}
	if strings.TrimSpace(reply.String()) != "" {
		h.appendMcpMessage(r.Context(), sessionID, "assistant", reply.String(), "")
	}
	writeOK(w, map[string]any{
		"session_id": sessionID,
		"reply":      reply.String(),
		"events":     events,
	})
}

// mcpAssistantStream 流式对话（SSE）。
func (h *Handler) mcpAssistantStream(w http.ResponseWriter, r *http.Request) {
	if !h.mcpAssistantEnabled() {
		writeErr(w, domain.Errorf(domain.CodeValidation, "智能助理未启用"))
		return
	}
	req, sessionID, err := h.bindMcpAssistantRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	opts, _, err := h.mcpAssistantOptions()
	if err != nil {
		writeErr(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, domain.Errorf(domain.CodeValidation, "当前连接不支持流式响应"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	writeEvent := func(event string, payload any) {
		raw, merr := json.Marshal(payload)
		if merr != nil {
			return
		}
		// SSE 载荷必须单行：把 JSON 里的换行转义掉。
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, strings.ReplaceAll(string(raw), "\n", "\\n"))
		flusher.Flush()
	}

	writeEvent("init", map[string]any{"session_id": sessionID})

	history := mcpHistoryFromRequest(req.History)
	if len(history) == 0 {
		history = h.loadMcpHistory(r.Context(), sessionID)
	}
	h.appendMcpMessage(r.Context(), sessionID, "user", req.Message, "")

	var (
		reply     strings.Builder
		rounds    int
		toolCalls []map[string]any
	)
	runErr := h.mcpAssistantInstance().Chat(r.Context(), opts,
		mcp.HistoryFromMessages(history), req.Message,
		func(ev mcp.AssistantEvent) {
			switch ev.Type {
			case "delta":
				reply.WriteString(ev.Content)
				writeEvent("delta", map[string]any{"content": ev.Content})
			case "tool_call":
				toolCalls = append(toolCalls, map[string]any{
					"name": ev.ToolName, "arguments": ev.ToolArgs,
				})
				writeEvent("tool_call", map[string]any{
					"tool_name": ev.ToolName, "tool_args": ev.ToolArgs,
				})
			case "tool_result":
				if len(toolCalls) > 0 {
					last := toolCalls[len(toolCalls)-1]
					last["result"] = ev.ToolResult
					last["is_error"] = ev.ToolError
				}
				writeEvent("tool_result", map[string]any{
					"tool_name":   ev.ToolName,
					"tool_result": ev.ToolResult,
					"tool_error":  ev.ToolError,
				})
			case "done":
				rounds = ev.Rounds
			}
		})

	if runErr != nil {
		writeEvent("error", map[string]any{"message": runErr.Error()})
		return
	}
	if strings.TrimSpace(reply.String()) != "" {
		h.appendMcpMessage(r.Context(), sessionID, "assistant", reply.String(), marshalMcpToolCalls(toolCalls))
	}
	writeEvent("done", map[string]any{
		"reply":      reply.String(),
		"tool_calls": toolCalls,
		"rounds":     rounds,
	})
}

// mcpAssistantTest 测试助理连通性（不落库、不调工具）。
func (h *Handler) mcpAssistantTest(w http.ResponseWriter, r *http.Request) {
	opts, eff, err := h.mcpAssistantOptions()
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := mcp.TestAssistantConnection(r.Context(), opts); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "连接成功", "model": eff.ModelName})
}

// mcpAssistantSessions 列出对话会话。
func (h *Handler) mcpAssistantSessions(w http.ResponseWriter, r *http.Request) {
	if h.mcpChat == nil {
		writeOK(w, map[string]any{"sessions": []mcp.ChatSession{}})
		return
	}
	sessions, err := h.mcpChat.ListSessions(r.Context(), 100)
	if err != nil {
		writeErr(w, err)
		return
	}
	if sessions == nil {
		sessions = []mcp.ChatSession{}
	}
	writeOK(w, map[string]any{"sessions": sessions})
}

// mcpAssistantSessionMessages 读取单个会话的消息。
func (h *Handler) mcpAssistantSessionMessages(w http.ResponseWriter, r *http.Request) {
	if h.mcpChat == nil {
		writeOK(w, map[string]any{"messages": []mcp.ChatMessage{}})
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	if sessionID == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "会话 ID 不能为空"))
		return
	}
	messages, err := h.mcpChat.ListMessages(r.Context(), sessionID, mcpChatHistoryLimit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if messages == nil {
		messages = []mcp.ChatMessage{}
	}
	writeOK(w, map[string]any{"messages": messages})
}

// mcpAssistantSessionDelete 删除单个会话的全部消息。
func (h *Handler) mcpAssistantSessionDelete(w http.ResponseWriter, r *http.Request) {
	if h.mcpChat == nil {
		writeOK(w, map[string]any{"deleted": 0})
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	if sessionID == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "会话 ID 不能为空"))
		return
	}
	deleted, err := h.mcpChat.DeleteSession(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": deleted})
}

// loadMcpHistory 读取会话历史（仓储不可用时返回空，不阻断对话）。
func (h *Handler) loadMcpHistory(ctx context.Context, sessionID string) []mcp.AssistantMessage {
	if h.mcpChat == nil {
		return nil
	}
	items, err := h.mcpChat.ListMessages(ctx, sessionID, mcpChatHistoryLimit)
	if err != nil {
		h.log.Warn("读取 MCP 对话历史失败（按空历史继续）", "error", err)
		return nil
	}
	args := make([]mcp.AssistantMessage, 0, len(items))
	for _, it := range items {
		args = append(args, mcp.AssistantMessage{Role: it.Role, Content: it.Content})
	}
	return args
}

// appendMcpMessage 落库一条消息（失败只记日志，不影响对话）。
func (h *Handler) appendMcpMessage(ctx context.Context, sessionID, role, content, toolCalls string) {
	if h.mcpChat == nil || strings.TrimSpace(content) == "" {
		return
	}
	msg := &mcp.ChatMessage{SessionID: sessionID, Role: role, Content: content, ToolCalls: toolCalls}
	if err := h.mcpChat.AppendMessage(ctx, msg); err != nil {
		h.log.Warn("写入 MCP 对话历史失败", "error", err)
	}
}

// marshalMcpToolCalls 序列化工具调用记录；失败返回 "[]" 而不是让保存失败。
func marshalMcpToolCalls(calls []map[string]any) string {
	if len(calls) == 0 {
		return ""
	}
	raw, err := json.Marshal(calls)
	if err != nil {
		return ""
	}
	return string(raw)
}

// errNoAssistantRunner 占位：保留 hooks 未注册时的可读错误文案。
var errNoAssistantRunner = errors.New("订阅执行器未注册")
