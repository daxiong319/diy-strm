package api

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/mcp"
)

// MCP Server 的 HTTP 层。
//
// 三件事：
//  1. 暴露 JSON-RPC 端点 POST /api/mcp，外部 MCP 客户端用独立 API Key 鉴权；
//  2. 暴露站内设置与内置助理的 REST/SSE 端点，走管理员会话鉴权；
//  3. 通过钩子把 internal/api 才有的能力（网盘文件操作、TG 频道订阅执行）
//     反向注册进 internal/mcp —— internal/mcp 不能 import internal/api，
//     否则与「api 注册 mcp 路由」形成循环依赖。

const (
	// mcpMaxBody 是 JSON-RPC 请求体上限。MCP 报文普遍很小，4MB 已很宽松，
	// 主要作用是挡住误发的大文件与恶意超大 body。
	mcpMaxBody = 4 << 20
	// mcpConfigPath 是返回给前端与 MCP 客户端展示的服务端地址。
	mcpConfigPath = "/api/mcp"
	// mcpChatHistoryLimit 是一次对话默认回带的历史消息条数。
	mcpChatHistoryLimit = 20
)

// mcpServerOnce 保证服务端与钩子只初始化一次。
//
// 顺序是关键：必须在同一个 Once 内先注册钩子、再构造 Server，
// 不能存在「拿到 Server 但钩子尚未注册」的窗口 —— 否则并发请求
// 可能在这段窗口里调用到未接线工具，得到「未注册」错误。
var (
	mcpServerOnce sync.Once
	mcpServerInst *mcp.Server
	mcpAssistant  *mcp.Assistant
)

// McpServer 返回 MCP 服务端单例（含钩子接线与配置快照）。
func (h *Handler) McpServer() *mcp.Server {
	mcpServerOnce.Do(func() {
		// 配置快照：每次请求实时读取，配置改动无需重建服务端。
		mcp.SetConfigSnapshotFunc(func() *mcp.McpConfigSnapshot {
			return mcp.ConfigFromSettings(h.settings).Snapshot()
		})
		h.registerMcpHooks()
		mcpServerInst = mcp.NewServer(mcp.DefaultRegistry(), mcp.ConfigSnapshot)
		mcpAssistant = mcp.NewAssistant(mcpServerInst)
	})
	return mcpServerInst
}

// mcpAssistantInstance 返回助理实例（与服务器同一 Once，保证已接线）。
func (h *Handler) mcpAssistantInstance() *mcp.Assistant {
	h.McpServer()
	return mcpAssistant
}

// MCP 路由拆成两个注册函数，因为它们必须挂在**不同的中间件层**下。
//
// 这是踩过的坑：最初把两者放在同一个函数里、并在 router.go 的
// requireAdmin 组内调用，结果是外部 MCP 客户端（只有 API Key、没有浏览器
// Cookie）在任何请求到达 API Key 中间件之前就先被会话中间件拦成 401。
// 因此：
//   - RegisterMcpPublicRoutes 必须挂在 requireAdmin 组**之外**；
//   - RegisterMcpAdminRoutes 挂在 requireAdmin 组**之内**（也可自带中间件，
//     但由调用方施加可以复用同一份鉴权配置，避免两处漂移）。

// RegisterMcpPublicRoutes 注册外部 MCP 客户端入口（POST /mcp）。
//
// 用独立 API Key 鉴权，不使用也不要求管理员会话：
// MCP 客户端是无浏览器环境，拿不到 Cookie。
func (h *Handler) RegisterMcpPublicRoutes(r chi.Router) {
	r.With(h.requireMcpAPIKey).Post("/mcp", h.mcpEndpoint)
}

// RegisterMcpAdminRoutes 注册站内设置与助理对话端点。
//
// 这些端点是给管理页面用的，由调用方置于 requireAdmin 之下；
// 本函数不再重复施加中间件，避免「以为加了两道、其实只生效一道」的错觉。
func (h *Handler) RegisterMcpAdminRoutes(r chi.Router) {
	r.Get("/mcp/config", h.mcpConfigGet)
	r.Put("/mcp/config", h.mcpConfigUpdate)
	r.Get("/mcp/tools", h.mcpToolsList)
	r.Post("/mcp/assistant/test", h.mcpAssistantTest)
	r.Post("/mcp/assistant/chat", h.mcpAssistantChat)
	r.Post("/mcp/assistant/stream", h.mcpAssistantStream)
	r.Get("/mcp/assistant/sessions", h.mcpAssistantSessions)
	r.Get("/mcp/assistant/sessions/{id}", h.mcpAssistantSessionMessages)
	r.Delete("/mcp/assistant/sessions/{id}", h.mcpAssistantSessionDelete)
}

// requireMcpAPIKey 是 /api/mcp 专用鉴权：X-API-Key 头或 api_key 查询参数。
//
// 与站内管理员会话完全独立：MCP 客户端是无浏览器环境，拿不到 Cookie，
// 因此这里不使用也不要求会话；反过来 API Key 也不会授予站内页面权限。
func (h *Handler) requireMcpAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if raw == "" {
			raw = strings.TrimSpace(r.URL.Query().Get("api_key"))
		}
		if raw == "" {
			writeErr(w, domain.Errorf(domain.CodeAdminAuthRequired,
				"缺少 API Key：请在 X-API-Key 请求头或 api_key 查询参数中提供"))
			return
		}
		if h.apiKeys == nil {
			writeErr(w, domain.Errf(domain.CodeNotImplement))
			return
		}
		if _, err := h.apiKeys.Validate(r.Context(), raw); err != nil {
			writeErr(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mcpEndpoint 处理 JSON-RPC 请求。
func (h *Handler) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	cfg := mcp.ConfigFromSettings(h.settings)
	if !cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, domain.Errorf(domain.CodeValidation, "只支持 POST"))
		return
	}

	body, err := readMCPBody(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}

	resp := h.McpServer().HandleMessage(r.Context(), body)
	if resp == nil {
		// 通知按规范不返回响应体，用 202 表明「已接受，无内容」。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, Resp{Success: true, Data: resp})
}

// readMCPBody 读取并校验请求体。
func readMCPBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength > mcpMaxBody {
		return nil, domain.Errorf(domain.CodeValidation, "请求体过大")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxBody+1))
	if err != nil {
		return nil, domain.Errorf(domain.CodeValidation, "读取请求体失败：%v", err)
	}
	if len(body) > mcpMaxBody {
		return nil, domain.Errorf(domain.CodeValidation, "请求体过大")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, domain.Errorf(domain.CodeValidation, "请求体为空")
	}
	return body, nil
}
