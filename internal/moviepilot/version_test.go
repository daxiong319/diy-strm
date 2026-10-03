package moviepilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestMajorVersionProbeSuccessAndCache 验证探测成功时判定为 v1/v2 且结果被缓存。
// 用 httptest 假服务统计命中次数：探测结果必须缓存，重复调用不能重复发请求。
func TestMajorVersionProbeSuccessAndCache(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token")
	if got := c.MajorVersion(context.Background()); got != MajorVersionV1V2 {
		t.Fatalf("首次探测版本=%d，期望 %d", got, MajorVersionV1V2)
	}
	// 再调用两次，必须走缓存
	if got := c.MajorVersion(context.Background()); got != MajorVersionV1V2 {
		t.Fatalf("二次探测版本=%d，期望 %d", got, MajorVersionV1V2)
	}
	if got := c.MajorVersion(context.Background()); got != MajorVersionV1V2 {
		t.Fatalf("三次探测版本=%d，期望 %d", got, MajorVersionV1V2)
	}
	mu.Lock()
	n := hits
	mu.Unlock()
	if n != 1 {
		t.Fatalf("探针请求次数=%d，期望 1（结果必须缓存）", n)
	}
}

// TestMajorVersionProbeFailureFallsBack 验证探测失败时回退 Unknown 且不 panic、不报错中断。
func TestMajorVersionProbeFailureFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":"token 无效"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-token")
	if got := c.MajorVersion(context.Background()); got != MajorVersionUnknown {
		t.Fatalf("探测失败应返回 Unknown，实际=%d", got)
	}
}

// TestMajorVersionProbeUnreachableFallsBack 验证服务不可达时回退 Unknown（不中断调用方）。
func TestMajorVersionProbeUnreachableFallsBack(t *testing.T) {
	// 指向一个未监听端口
	c := NewClient("http://127.0.0.1:1", "token")
	if got := c.MajorVersion(context.Background()); got != MajorVersionUnknown {
		t.Fatalf("不可达应返回 Unknown，实际=%d", got)
	}
}

// TestMajorVersionNilClientSafe 验证 nil 客户端不 panic
func TestMajorVersionNilClientSafe(t *testing.T) {
	var c *Client
	if got := c.MajorVersion(context.Background()); got != MajorVersionUnknown {
		t.Fatalf("nil 客户端应返回 Unknown，实际=%d", got)
	}
}

// TestMajorVersionConcurrent 验证并发调用下的缓存安全（-race 下应无数据竞争）。
//
// versionOnce 提供 single-flight：冷缓存下 16 个并发调用只发一次探针，其余等待复用，
// 避免对不可达的 MoviePilot 把 60s 超时放大成 16 次串行等待。
func TestMajorVersionConcurrent(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "token")
	var wg sync.WaitGroup
	results := make([]int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = c.MajorVersion(context.Background())
		}(i)
	}
	wg.Wait()
	for i, v := range results {
		if v != MajorVersionV1V2 {
			t.Fatalf("并发第 %d 个结果=%d，期望 %d", i, v, MajorVersionV1V2)
		}
	}
	// single-flight：16 个并发调用只应有 1 次探针
	mu.Lock()
	n := hits
	mu.Unlock()
	if n != 1 {
		t.Fatalf("并发探测次数=%d，期望 1（single-flight 应合并冷缓存并发探针）", n)
	}

	// 缓存后串行再调多次，必须完全不再发请求
	for i := 0; i < 5; i++ {
		_ = c.MajorVersion(context.Background())
	}
	mu.Lock()
	after := hits
	mu.Unlock()
	if after != n {
		t.Fatalf("缓存生效后仍发生探测：%d → %d", n, after)
	}
}
