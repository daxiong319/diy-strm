package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/apikey"
	"litepan/internal/domain"
	"litepan/internal/logx"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// TestMcpEndpointEndToEnd 端到端验证 HTTP → 鉴权 → JSON-RPC 全链路。
//
// 这是路由分层修复（RegisterMcpPublicRoutes 必须挂在 requireAdmin 之外）
// 的**唯一直接证据**：前面几处检查都只能证明「路径存在」，而这里带一个真实
// 签发的 API Key 走到协议层，证明外部客户端（无 Cookie）确实能用。
//
// 同时也反向锁死「不能用会话中间件包住外部端点」——
// 若把本测试挂载方式换成 requireAdmin，请求会得到 401 而不是 JSON-RPC 响应。
func TestMcpEndpointEndToEnd(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	st := store.New(db)

	settingsSvc, err := settings.New(ctx, st.Configs)
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	// 打开 MCP 总开关，否则端点会（按设计）返回 404。
	if err := settingsSvc.Update(ctx, map[string]string{
		settings.KeyMcpEnabled: "true",
	}); err != nil {
		t.Fatalf("写入 mcp_enabled 失败：%v", err)
	}

	keySvc := apikey.New(apikey.Options{Repo: st.ApiKeys, Settings: settingsSvc, Secret: []byte("test-secret-32-bytes-long-abcdef")})
	created, err := keySvc.Create(ctx, apikey.CreateInput{Name: "e2e", KeyType: domain.ApiKeyTypeReadonly, Status: domain.ApiKeyStatusActive})
	if err != nil {
		t.Fatalf("签发 API Key 失败：%v", err)
	}
	if created.Key == "" {
		t.Fatal("签发结果未返回明文 Key，无法测试")
	}

	h := &Handler{settings: settingsSvc, apiKeys: keySvc, logs: logx.NewDiscard()}

	// 完全按生产分层挂载：只登记公共路由，且不施加任何会话中间件。
	r := chi.NewRouter()
	h.RegisterMcpPublicRoutes(r)

	do := func(headers map[string]string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// 1) 无凭据必须被拒。
	rec := do(nil, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("无 API Key 的请求不应成功：%s", rec.Body.String())
	}

	// 2) 错误凭据必须被拒，且不得进入协议层。
	rec = do(map[string]string{"X-API-Key": "definitely-not-a-real-key"}, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("错误 API Key 不应成功：%s", rec.Body.String())
	}

	// 3) 头部凭据：应走到协议层并完成 initialize 协商。
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}`
	rec = do(map[string]string{"X-API-Key": created.Key}, initBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("带合法 API Key 的 initialize 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var initResp struct {
		Data struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("initialize 响应不是合法 JSON：%v（body=%s）", err, rec.Body.String())
	}
	if initResp.Data.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("协商协议版本 = %q，期望 2025-06-18", initResp.Data.Result.ProtocolVersion)
	}

	// 4) 查询参数凭据同样可用（无浏览器环境常用）。
	rec = do(nil, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if rec.Code == http.StatusOK {
		t.Fatal("无凭据的 tools/list 不应成功（确认上一步成功确因凭据）")
	}

	// 5) tools/list 必须返回非空工具清单。
	req := httptest.NewRequest(http.MethodPost, "/mcp?api_key="+created.Key,
		strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询参数传 API Key 的 tools/list 应返回 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Data struct {
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("tools/list 响应不是合法 JSON：%v", err)
	}
	if len(listResp.Data.Result.Tools) == 0 {
		t.Fatalf("tools/list 返回空清单：%s", rec.Body.String())
	}

	// 6) 通知类请求按规范不返回响应体 —— 用 202 表明「已接受，无内容」。
	rec = do(map[string]string{"X-API-Key": created.Key}, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("通知类请求应返回 202，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("通知类请求不应有响应体，实际：%s", rec.Body.String())
	}
}
