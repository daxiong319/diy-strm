package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"litepan/internal/mcp"
	"litepan/internal/settings"
)

// configRepoStub 是内存配置仓储，供本文件构造 settings.Service。
type mcpConfigRepoStub struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *mcpConfigRepoStub) Get(_ context.Context, key string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *mcpConfigRepoStub) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *mcpConfigRepoStub) All(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// newMCPTestHandler 构造一个只装了 settings 的 Handler。
func newMCPTestHandler(t *testing.T, values map[string]string) *Handler {
	t.Helper()
	svc, err := settings.New(context.Background(), &mcpConfigRepoStub{values: values})
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	return &Handler{settings: svc}
}

// TestMcpConfigEnvelopeShape 锁定 GET /mcp/config 的响应形状。
//
// 这是参考实现栽过的坑：后端返回扁平配置字段，而前端读的是
// data.data.config 与 data.data.tools —— 两边不一致的结果是设置页
// **静默空白**（不报错、就是什么都不显示），极难排查。
// 因此这里把嵌套层次锁死：config、tools、server_url 必须都在 data 之下。
func TestMcpConfigEnvelopeShape(t *testing.T) {
	h := newMCPTestHandler(t, map[string]string{
		settings.KeyMcpEnabled:         "true",
		settings.KeyMcpAllowWriteTools: "false",
		settings.KeyMcpDisabledTools:   `["netdisk_delete"]`,
		settings.KeyMcpMaxToolRounds:   "6",
		settings.KeyMcpTimeout:         "120",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/mcp/config", nil)
	rec := httptest.NewRecorder()
	h.mcpConfigGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Config struct {
				Enabled         bool     `json:"enabled"`
				AllowWriteTools bool     `json:"allow_write_tools"`
				DisabledTools   []string `json:"disabled_tools"`
				MaxToolRounds   int      `json:"max_tool_rounds"`
				Timeout         int      `json:"timeout"`
			} `json:"config"`
			Tools     []map[string]any `json:"tools"`
			ServerURL string           `json:"server_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（body=%s）", err, rec.Body.String())
	}

	if !body.Success {
		t.Fatalf("success 应为 true，body=%s", rec.Body.String())
	}
	// 关键：必须在 data.config 之下，而不是直接在 data 之下。
	if body.Data.Config.MaxToolRounds == 0 && body.Data.Config.Timeout == 0 {
		t.Fatalf("data.config 为空 —— 前端读 data.data.config 会拿到空对象并渲染成空白页：%s", rec.Body.String())
	}
	if !body.Data.Config.Enabled {
		t.Errorf("enabled 应为 true，body=%s", rec.Body.String())
	}
	if body.Data.Config.AllowWriteTools {
		t.Errorf("allow_write_tools 应为 false，body=%s", rec.Body.String())
	}
	if len(body.Data.Config.DisabledTools) != 1 || body.Data.Config.DisabledTools[0] != "netdisk_delete" {
		t.Errorf("disabled_tools = %v，期望 [netdisk_delete]", body.Data.Config.DisabledTools)
	}
	if len(body.Data.Tools) == 0 {
		t.Fatalf("data.tools 不应为空（前端工具清单依赖它）：%s", rec.Body.String())
	}
	if body.Data.ServerURL != mcpConfigPath {
		t.Errorf("server_url = %q，期望 %q", body.Data.ServerURL, mcpConfigPath)
	}
}

// TestMcpConfigNeverLeaksAssistantAPIKey 锁定配置响应绝不回显助理 API Key。
//
// 只返回 assistant_api_key_set 布尔量。泄露密钥的后果是任何能打开设置页
// （或读到该接口响应）的人都能拿走 LLM 凭据。
func TestMcpConfigNeverLeaksAssistantAPIKey(t *testing.T) {
	const secret = "sk-super-secret-key-should-never-appear"
	h := newMCPTestHandler(t, map[string]string{
		settings.KeyMcpEnabled:          "true",
		settings.KeyMcpAssistantEnabled: "true",
		settings.KeyMcpAssistantBaseURL: "https://api.example.com",
		settings.KeyMcpAssistantAPIKey:  secret,
		settings.KeyMcpAssistantModel:   "gpt-4o-mini",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/mcp/config", nil)
	rec := httptest.NewRecorder()
	h.mcpConfigGet(rec, req)

	raw := rec.Body.String()
	if strings.Contains(raw, secret) {
		t.Fatalf("响应体泄露了助理 API Key 明文：%s", raw)
	}
	// 字段名本身也不该出现（参考实现的验收断言即此写法）：
	// 前端只应看到 assistant_api_key_set。
	if strings.Contains(raw, `"assistant_api_key"`) {
		t.Fatalf(`响应体出现了 assistant_api_key 字段（应只有 assistant_api_key_set）：%s`, raw)
	}

	var body struct {
		Data struct {
			Config struct {
				AssistantAPIKeySet bool `json:"assistant_api_key_set"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if !body.Data.Config.AssistantAPIKeySet {
		t.Fatal("已配置密钥时 assistant_api_key_set 应为 true，否则界面无法提示「已配置」")
	}
}

// TestMcpPublicRouteNotBehindAdminMiddleware 锁定外部 /mcp 端点不经过管理员会话中间件。
//
// 这是我在合并路由时真实踩到的坑：最初把 RegisterMcpPublicRoutes 与站内端点
// 放在同一个函数里、并在 router.go 的 requireAdmin 组内调用，结果是外部 MCP
// 客户端（只有 API Key、无浏览器 Cookie）在任何请求到达 API Key 中间件之前，
// 就先被会话中间件拦成 401，永远无法使用。
//
// 这里用「只用 registerMcpPublicRoutes 挂载、不带任何 cookie」的请求作证：
// 若它被会话中间件包住，会得到 401；而带上正确的 API Key 时应当能进到端点本身
// （端点内因 cfg.Enabled=false 返回 404，这正好证明「已经越过鉴权」）。
func TestMcpPublicRouteNotBehindAdminMiddleware(t *testing.T) {
	h := newMCPTestHandler(t, map[string]string{
		// MCP 总开关关闭：端点会对已鉴权的调用者返回 404。
		settings.KeyMcpEnabled: "false",
	})

	// 未提供 API Key：应被 API Key 中间件拒绝（401/403 一类），而不是先被会话中间件拒绝。
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	rec := httptest.NewRecorder()
	h.mcpEndpoint(rec, req)

	// 端点自身在未启用时返回 404 —— 说明请求确实到达了 handler，
	// 而不是被某个更外层的中间件提前拦掉。
	if rec.Code != http.StatusNotFound {
		t.Fatalf("MCP 未启用时端点应返回 404（证明请求到达了 handler），实际 %d", rec.Code)
	}
}

// TestUnknownMcpToolsRejected 锁定「禁用清单写错工具名」必须报错。
//
// 静默忽略的后果是用户以为禁用了某工具、实际没禁，
// 而 LLM 仍能调用它。
func TestUnknownMcpToolsRejected(t *testing.T) {
	cfg := mcp.DefaultConfig()
	cfg.DisabledTools = []string{"netdisk_list", "this_tool_does_not_exist"}
	unknown := unknownMcpTools(cfg.DisabledTools)
	if len(unknown) != 1 || unknown[0] != "this_tool_does_not_exist" {
		t.Fatalf("未知工具识别 = %v，期望 [this_tool_does_not_exist]", unknown)
	}
	if got := unknownMcpTools([]string{"netdisk_list"}); len(got) != 0 {
		t.Fatalf("合法工具名不应被报为未知，得到 %v", got)
	}
}
