package guanying

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestIsPoWExpiredJSON 419「浏览器验证已过期」JSON 识别（filejin PoW 墙真实响应形态）。
func TestIsPoWExpiredJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"419 验证过期", `{"code":419,"refresh":1,"msg":"浏览器验证已过期，请完成验证后继续","worker":"//static.filejin.ru/x.js"}`, true},
		{"msg 匹配无 code", `{"code":0,"msg":"浏览器验证已过期，请完成验证后继续"}`, true},
		{"正常搜索响应", `{"inlist":{"i":["123"],"d":["mv"],"title":["生逢其时"]}}`, false},
		{"正常空结果", `{"inlist":{}}`, false},
		{"HTML 页面", `<!doctype html><html><body>challenge</body></html>`, false},
		{"非 JSON", `not json at all`, false},
		{"空 body", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPoWExpiredJSON([]byte(tc.body)); got != tc.want {
				t.Fatalf("isPoWExpiredJSON(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestGetWithPoWRecoveryRecovers 419 后自愈重试成功：恢复动作注入新 cookie 后重试拿到业务数据。
func TestGetWithPoWRecoveryRecovers(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(419)
			_, _ = w.Write([]byte(`{"code":419,"refresh":1,"msg":"浏览器验证已过期，请完成验证后继续"}`))
			return
		}
		_, _ = w.Write([]byte(`{"inlist":{"i":["42"],"d":["mv"],"title":["生逢其时"]}}`))
	}))
	defer srv.Close()

	recovered := false
	c := &Client{http: srv.Client(), baseURL: srv.URL}
	c.powRecover = func(ctx context.Context) bool {
		recovered = true
		return true
	}

	body, _, err := c.getWithPoWRecovery(context.Background(), "/res/search?q=x")
	if err != nil {
		t.Fatalf("重试应成功：%v", err)
	}
	if !recovered {
		t.Fatal("419 应触发恢复动作")
	}
	var sr struct {
		Inlist map[string]any `json:"inlist"`
	}
	if json.Unmarshal(body, &sr) != nil || len(sr.Inlist) == 0 {
		t.Fatalf("重试响应应包含业务数据：%s", body)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("应恰好请求两次（实际 %d）", hits)
	}
}

// TestGetWithPoWRecoveryFailsGracefully 恢复失败时返回明确错误（而非把 419 误报为未收录）。
func TestGetWithPoWRecoveryFailsGracefully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(419)
		_, _ = w.Write([]byte(`{"code":419,"refresh":1,"msg":"浏览器验证已过期，请完成验证后继续"}`))
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	c.powRecover = func(ctx context.Context) bool { return false }

	_, _, err := c.getWithPoWRecovery(context.Background(), "/res/search?q=x")
	if err == nil {
		t.Fatal("恢复失败应返回错误")
	}
	if got := err.Error(); !contains(got, "观影安全验证已过期") {
		t.Fatalf("错误应明确指向验证过期，实际：%s", got)
	}
}

// TestGetWithPoWRecoveryNoRetryOnNormal 正常响应不触发恢复、只请求一次。
func TestGetWithPoWRecoveryNoRetryOnNormal(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inlist":{"i":["42"]}}`))
	}))
	defer srv.Close()

	called := false
	c := &Client{http: srv.Client(), baseURL: srv.URL}
	c.powRecover = func(ctx context.Context) bool {
		called = true
		return true
	}
	if _, _, err := c.getWithPoWRecovery(context.Background(), "/res/search"); err != nil {
		t.Fatalf("正常响应不应报错：%v", err)
	}
	if called || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("正常响应不应触发恢复（called=%v hits=%d）", called, hits)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestGetWithPoWRecoveryStillExpiredAfterRetry 重试后仍 419：明确报验证失败，绝不落入「未收录」误报。
func TestGetWithPoWRecoveryStillExpiredAfterRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(419)
		_, _ = w.Write([]byte(`{"code":419,"refresh":1,"msg":"浏览器验证已过期，请完成验证后继续"}`))
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	c.powRecover = func(ctx context.Context) bool { return true }

	_, _, err := c.getWithPoWRecovery(context.Background(), "/res/search?q=x")
	if err == nil {
		t.Fatal("重试后仍 419 应报错")
	}
	if got := err.Error(); !contains(got, "观影安全验证重试后仍过期") {
		t.Fatalf("错误应指向验证仍过期，实际：%s", got)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("应恰好请求两次（实际 %d）", hits)
	}
}

// TestGetWithPoWRecoveryRetriesWithNewBaseURL 域名迁移：恢复动作更新 baseURL 后，重试应打到新域名。
func TestGetWithPoWRecoveryRetriesWithNewBaseURL(t *testing.T) {
	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inlist":{"i":["42"]}}`))
	}))
	defer newSrv.Close()
	oldSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(419)
		_, _ = w.Write([]byte(`{"code":419,"msg":"浏览器验证已过期，请完成验证后继续"}`))
	}))
	defer oldSrv.Close()

	c := &Client{http: oldSrv.Client(), baseURL: oldSrv.URL}
	c.powRecover = func(ctx context.Context) bool {
		c.baseURL = newSrv.URL // 模拟恢复中域名切换
		return true
	}
	body, _, err := c.getWithPoWRecovery(context.Background(), "/res/search?q=x")
	if err != nil {
		t.Fatalf("域名迁移后重试应成功：%v", err)
	}
	if !contains(string(body), "\"inlist\"") {
		t.Fatalf("应拿到新站点业务数据：%s", body)
	}
}
