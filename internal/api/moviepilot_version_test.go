package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"litepan/internal/moviepilot"
)

// TestMoviePilotVersionHandlerShape 锁定 /api/moviepilot/version 的响应形状。
//
// 该端点是 参考实现 移植①（MoviePilot 版本探测）的对外消费者：前端据 major_version/display
// 展示版本、据 degraded 提示「探测失败，已回退 v1/v2 行为」。探测失败必须是 200 + degraded=true，
// 而不是错误响应 —— 否则「MoviePilot 没起」会让整个设置页报错，而这恰恰是最常见的状态。
//
// 服务未配置时应返回 200 + degraded=true（而不是 500），因为「没配 MoviePilot」是合法状态。
func TestMoviePilotVersionHandlerShape(t *testing.T) {
	h := &Handler{} // moviePilot 为 nil：等价于服务未就绪

	req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/version", nil)
	rec := httptest.NewRecorder()
	h.getMoviePilotVersion(rec, req)

	// 服务未配置：ensureServiceReady 会写错误响应，不应 panic
	if rec.Code == http.StatusOK {
		var body struct {
			Success bool `json:"success"`
			Data    struct {
				MajorVersion int    `json:"major_version"`
				Display      string `json:"display"`
				Degraded     bool   `json:"degraded"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应不是合法 JSON：%v（body=%s）", err, rec.Body.String())
		}
		if !body.Success {
			t.Fatalf("200 响应应 success=true，body=%s", rec.Body.String())
		}
	}

	// 版本展示文案映射（纯函数，与 HTTP 无关，但同属本端点的对外契约）
	for _, c := range []struct {
		in   int
		want string
	}{
		{moviepilot.MajorVersionV3, "v3"},
		{moviepilot.MajorVersionV1V2, "v1/v2"},
		{moviepilot.MajorVersionUnknown, "未知"},
	} {
		if got := moviePilotVersionDisplay(c.in); got != c.want {
			t.Fatalf("display(%d) = %q, want %q", c.in, got, c.want)
		}
	}

	// degraded 语义：只有 Unknown 才是降级
	if moviepilot.MajorVersionUnknown == moviepilot.MajorVersionV1V2 ||
		moviepilot.MajorVersionUnknown == moviepilot.MajorVersionV3 {
		t.Fatal("MajorVersionUnknown 不得与已知版本取值相同，否则 degraded 判定失效")
	}
}
