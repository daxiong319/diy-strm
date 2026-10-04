package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"litepan/internal/adminauth"
	"litepan/internal/domain"
	"litepan/internal/notification"
	"litepan/internal/store"
	"litepan/internal/upload"
)

func TestSPAHandlerCompressedAsset(t *testing.T) {
	source := []byte("console.log('litepan')")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(source); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	fsys := fstest.MapFS{
		"index.html":       {Data: []byte("<main>LitePan</main>")},
		"assets/app.js.gz": {Data: compressed.Bytes()},
	}
	handler := spaHandler(fsys)

	t.Run("浏览器直接接收gzip", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		request.Header.Set("Accept-Encoding", "br, gzip")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		if got := response.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q", got)
		}
		if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control = %q", got)
		}
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, source) {
			t.Fatalf("decoded = %q", decoded)
		}
	})

	t.Run("不支持gzip时兼容解压", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		if got := response.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q", got)
		}
		if !bytes.Equal(response.Body.Bytes(), source) {
			t.Fatalf("body = %q", response.Body.Bytes())
		}
	})

	t.Run("前端深链回退首页", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := response.Body.String(); got != "<main>LitePan</main>" {
			t.Fatalf("body = %q", got)
		}
	})
}

func TestAcceptsGzip(t *testing.T) {
	tests := map[string]bool{
		"gzip":            true,
		"br, gzip":        true,
		"gzip;q=0":        false,
		"*;q=1":           true,
		"*;q=1, gzip;q=0": false,
		"br, deflate":     false,
		"gzip;q=invalid":  false,
	}
	for header, want := range tests {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestShouldSuppressAPIErrorLog(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		remoteAddr string
		err        *domain.AppError
		want       bool
	}{
		{
			name:       "本机上传任务列表401静默",
			path:       "/api/files/upload/tasks",
			remoteAddr: "127.0.0.1:5211",
			err:        domain.Errorf(domain.CodeAdminAuthRequired, "需要管理员权限"),
			want:       true,
		},
		{
			name:       "本机上传任务流401静默",
			path:       "/api/files/upload/tasks/stream",
			remoteAddr: "[::1]:5211",
			err:        domain.Errorf(domain.CodeAdminAuthRequired, "需要管理员权限"),
			want:       true,
		},
		{
			name:       "远端来源不静默",
			path:       "/api/files/upload/tasks",
			remoteAddr: "10.0.0.8:5211",
			err:        domain.Errorf(domain.CodeAdminAuthRequired, "需要管理员权限"),
			want:       false,
		},
		{
			name:       "其他管理员接口不静默",
			path:       "/api/admin/accounts",
			remoteAddr: "127.0.0.1:5211",
			err:        domain.Errorf(domain.CodeAdminAuthRequired, "需要管理员权限"),
			want:       false,
		},
		{
			name:       "其他错误码不静默",
			path:       "/api/files/upload/tasks",
			remoteAddr: "127.0.0.1:5211",
			err:        domain.Errorf(domain.CodeValidation, "参数错误"),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			req.RemoteAddr = tt.remoteAddr
			if got := shouldSuppressAPIErrorLog(req, tt.err); got != tt.want {
				t.Fatalf("shouldSuppressAPIErrorLog() = %v, want %v", got, tt.want)
			}
		})
	}
}

// fromFuseMountDTO 必须透传 ReadOnly/Enabled —— 曾经是 `|| true` 恒真，
// 导致"只读/启用"永远无法保存 false。
func TestFromFuseMountDTOPreservesFlags(t *testing.T) {
	m, err := fromFuseMountDTO(fuseMountDTO{
		Name:      "test",
		AccountID: 7,
		ReadOnly:  false,
		Enabled:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.ReadOnly {
		t.Fatal("ReadOnly=false 被改写为 true")
	}
	if m.Enabled {
		t.Fatal("Enabled=false 被改写为 true")
	}
	if m.Name != "test" || m.AccountID != 7 {
		t.Fatalf("字段透传异常: %+v", m)
	}

	// 真值也应保持
	m2, err := fromFuseMountDTO(fuseMountDTO{Name: "test2", ReadOnly: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !m2.ReadOnly || !m2.Enabled {
		t.Fatalf("true 值被改写: %+v", m2)
	}
}

// sseRecorder 给 recorder 的写入加锁：handler 在自己的 goroutine 里持续写，
// 测试线程需要随时读取已落盘的事件内容。
type sseRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (r *sseRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *sseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *sseRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Body.String()
}

func TestStreamNotificationUnreadPushesCount(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	svc := notification.NewService(notification.Options{Repo: store.New(db).Notifications})
	handler := &Handler{notifications: svc}

	reqCtx, cancel := context.WithCancel(ctx)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/notifications/stream", nil).WithContext(reqCtx)
	rec := &sseRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.streamNotificationUnread(rec, req)
	}()

	waitBody := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.body(), want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("未等到 %q，当前响应：%q", want, rec.body())
	}

	// 首个事件是当前未读数，前端一连上就能拿到权威值。
	waitBody("event: unread")
	waitBody(`{"count":0}`)

	// 新通知落库后应主动推送，不需要前端轮询。
	svc.Notify(ctx, "info", "system", "标题", "内容", 0, 0)
	waitBody(`{"count":1}`)

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("请求上下文取消后 SSE handler 未退出")
	}
}

// TestSubtitleRoutesExistBehindAdminAuth 锁定字幕路由的挂载位置。
//
// 真实事故：前端 subtitle.ts 曾把 base 写成 "/admin/subtitle"，而后端
// RegisterSubtitleRoutes 挂在 requireAdmin 组根（即 /api/subtitle/*，不在
// /api/admin 之下，与 mcp.ts ↔ /api/mcp/* 同一约定），两边错位导致字幕页
// 全部功能 404——chi 对未注册路径返回纯文本 404，前端只能报「请求失败 (404)」。
//
// 这里对每条已注册的字幕路由发未认证请求：路由存在时会被 requireAdmin
// 拦成 401 JSON（error_type=ADMIN_AUTH_REQUIRED）；若有人把注册位置改回
// /api/admin 之下或改名，本测试立即以 404 失败。
func TestSubtitleRoutesExistBehindAdminAuth(t *testing.T) {
	handler := NewRouter(Deps{
		AdminAuth: adminauth.New(&mcpConfigRepoStub{values: map[string]string{}}, []byte("litepan-router-test-secret"), nil),
		Uploads:   upload.NewManager(upload.Options{}),
	})

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/subtitle/config"},
		{http.MethodPut, "/api/subtitle/config"},
		{http.MethodGet, "/api/subtitle/providers"},
		{http.MethodPost, "/api/subtitle/providers/test"},
		{http.MethodPost, "/api/subtitle/search"},
		{http.MethodPost, "/api/subtitle/match"},
		{http.MethodPost, "/api/subtitle/download"},
		{http.MethodPost, "/api/subtitle/sync"},
		{http.MethodPost, "/api/subtitle/sync/check"},
		{http.MethodGet, "/api/subtitle/tasks"},
		{http.MethodPost, "/api/subtitle/tasks/1/retry"},
		{http.MethodDelete, "/api/subtitle/tasks/1"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d（404 说明该路由未挂在 /api/subtitle 下），body = %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Success   bool   `json:"success"`
				ErrorType string `json:"error_type"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应不是合法 JSON：%v（body=%s）", err, rec.Body.String())
			}
			if body.Success || body.ErrorType != string(domain.CodeAdminAuthRequired) {
				t.Fatalf("error_type = %q, success = %v，期望 %s", body.ErrorType, body.Success, domain.CodeAdminAuthRequired)
			}
		})
	}
}

// TestUnregisteredPathsStillReturn404 是上面的反向对照：确认 401 只来自
// 「路由存在 + requireAdmin」，而不是 requireAdmin 被误挂到整个 /api
// 导致未注册路径也返回 401（那种情况下存在性测试会假绿）。
func TestUnregisteredPathsStillReturn404(t *testing.T) {
	handler := NewRouter(Deps{
		AdminAuth: adminauth.New(&mcpConfigRepoStub{values: map[string]string{}}, []byte("litepan-router-test-secret"), nil),
		Uploads:   upload.NewManager(upload.Options{}),
	})

	for _, path := range []string{"/api/subtitle/nonexistent", "/api/definitely/not/registered"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d，未注册路径应保持 404（否则 401 存在性断言失去区分度）", path, rec.Code)
		}
	}
}
