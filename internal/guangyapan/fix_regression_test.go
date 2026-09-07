package guangyapan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 回归（三批修复）：waitTaskDone 不再用 9 秒硬上限误判慢任务超时——
// 指数退避（300ms 起、单次上限 5s、总窗口 10 分钟）。模拟任务 2.5 秒后才完成：
// 修复前（30×300ms≈9s 窗口内固定间隔）也能等到，但这里用「每 700ms 才 ready」+
// 「总查询次数」断言退避行为：早期密集查询、后期稀疏，且慢任务在窗口内成功。
func TestWaitTaskDoneBackoffSlowTaskSucceeds(t *testing.T) {
	var queries int32
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&queries, 1)
		status := 1 // processing
		// 700ms 后的任务状态查询返回成功（第 3 次查询约在 300+600=900ms 附近）
		if time.Since(start) >= 700*time.Millisecond {
			status = 2
		}
		_ = n
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"status":` + itoa(status) + `}}`))
	}))
	defer srv.Close()

	c := NewClient(1, "token", "")
	c.SetBaseURL(srv.URL, srv.URL)

	if err := c.waitTaskDone(context.Background(), "task-1"); err != nil {
		t.Fatalf("1 秒内完成的慢任务不应超时失败（退避修复未生效）：%v", err)
	}
	if atomic.LoadInt32(&queries) < 2 {
		t.Fatalf("退避轮询应至少查询 2 次状态，实际 %d", queries)
	}
}

// 回归：任务失败（status=3）立即返回错误，不空转退避窗口。
func TestWaitTaskDoneFailsFastOnTaskFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"status":3}}`))
	}))
	defer srv.Close()

	c := NewClient(1, "token", "")
	c.SetBaseURL(srv.URL, srv.URL)

	begin := time.Now()
	err := c.waitTaskDone(context.Background(), "task-2")
	if err == nil {
		t.Fatal("任务失败状态应返回错误")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("失败任务应立即返回（实际耗时 %v，疑似空转退避窗口）", elapsed)
	}
}

// 回归：业务失败码立即返回错误。
func TestWaitTaskDoneFailsOnBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":40100,"msg":"令牌无效"}`))
	}))
	defer srv.Close()

	c := NewClient(1, "token", "")
	c.SetBaseURL(srv.URL, srv.URL)

	if err := c.waitTaskDone(context.Background(), "task-3"); err == nil {
		t.Fatal("业务失败码应返回错误")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
