package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/moviepilot"
	"litepan/internal/store"
)

// newFallbackAPIHandler 构造带内存仓储 + MoviePilot 服务的 Handler。
func newFallbackAPIHandler(t *testing.T) (*Handler, domain.MoviePilotRepository) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := store.New(db).MoviePilot
	return &Handler{moviePilot: moviepilot.New(moviepilot.Options{Repo: repo})}, repo
}

// decodeFallbackResp 解析统一响应外壳。
func decodeFallbackResp(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if !body.Success {
		t.Fatalf("success=false，body=%s", rec.Body.String())
	}
	return body.Data
}

// TestListMoviePilotFallbacksEndpoint 空表时分页字段齐备且 list 为空数组（不是 null）。
func TestListMoviePilotFallbacksEndpoint(t *testing.T) {
	h, _ := newFallbackAPIHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/fallbacks", nil)
	rec := httptest.NewRecorder()

	h.listMoviePilotFallbacks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d，body=%s", rec.Code, rec.Body.String())
	}
	data := decodeFallbackResp(t, rec)
	list, ok := data["list"].([]any)
	if !ok {
		t.Fatalf("list 应为数组，实际 %#v", data["list"])
	}
	if len(list) != 0 {
		t.Fatalf("空表 list 长度应为 0，实际 %d", len(list))
	}
	if total, _ := data["total"].(float64); total != 0 {
		t.Fatalf("total=%v，期望 0", data["total"])
	}
	if page, _ := data["page"].(float64); page != 1 {
		t.Fatalf("默认 page=%v，期望 1", data["page"])
	}
	if ps, _ := data["page_size"].(float64); ps != 20 {
		t.Fatalf("默认 page_size=%v，期望 20", data["page_size"])
	}
}

// TestListMoviePilotFallbacksEndpointPagination 分页参数与记录映射。
func TestListMoviePilotFallbacksEndpointPagination(t *testing.T) {
	h, repo := newFallbackAPIHandler(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := repo.SaveFallback(ctx, &domain.MoviePilotFallback{
			MediaKey:    domain.MoviePilotFallbackMediaKey("tv", int64(700+i), 1),
			Trigger:     domain.MoviePilotFallbackTriggerSearch,
			MediaType:   "tv",
			TmdbId:      int64(700 + i),
			Title:       "测试剧集",
			Season:      1,
			Status:      domain.MoviePilotFallbackPending,
			Action:      domain.MoviePilotFallbackActionDownload,
			SearchCount: i + 1,
		}); err != nil {
			t.Fatalf("save fallback %d: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/fallbacks?page=1&page_size=2", nil)
	rec := httptest.NewRecorder()
	h.listMoviePilotFallbacks(rec, req)

	data := decodeFallbackResp(t, rec)
	list := data["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("首页长度=%d，期望 2", len(list))
	}
	if total, _ := data["total"].(float64); total != 3 {
		t.Fatalf("total=%v，期望 3", data["total"])
	}
	if pages, _ := data["pages"].(float64); pages != 2 {
		t.Fatalf("pages=%v，期望 2", data["pages"])
	}
	first := list[0].(map[string]any)
	if first["media_key"] == "" || first["trigger"] != domain.MoviePilotFallbackTriggerSearch {
		t.Fatalf("记录字段映射异常：%#v", first)
	}
	if _, ok := first["created_at"].(string); !ok {
		t.Fatalf("created_at 应为字符串：%#v", first["created_at"])
	}
}

// TestGetMoviePilotFallbackSummaryNotConfigured 未配置 MoviePilot 时仍返回 200，
// configured=false 且阈值/动作报默认值，actions 三项齐备。
func TestGetMoviePilotFallbackSummaryNotConfigured(t *testing.T) {
	h, _ := newFallbackAPIHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/fallbacks/summary", nil)
	rec := httptest.NewRecorder()

	h.getMoviePilotFallbackSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d，body=%s", rec.Code, rec.Body.String())
	}
	data := decodeFallbackResp(t, rec)
	if data["configured"] != false {
		t.Fatalf("configured=%v，期望 false", data["configured"])
	}
	if v, _ := data["search_threshold"].(float64); int(v) != 3 {
		t.Fatalf("默认 search_threshold=%v，期望 3", data["search_threshold"])
	}
	if v, _ := data["subscription_threshold"].(float64); int(v) != 3 {
		t.Fatalf("默认 subscription_threshold=%v，期望 3", data["subscription_threshold"])
	}
	if data["search_action"] != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("默认 search_action=%v，期望 download", data["search_action"])
	}
	actions, ok := data["actions"].([]any)
	if !ok || len(actions) != 3 {
		t.Fatalf("actions 应为 3 项，实际 %#v", data["actions"])
	}
	seen := map[string]bool{}
	for _, a := range actions {
		m := a.(map[string]any)
		seen[m["value"].(string)] = true
		if m["label"] == "" {
			t.Fatalf("动作 %v 缺少中文 label", m["value"])
		}
	}
	for _, want := range []string{
		domain.MoviePilotFallbackActionSubscribe,
		domain.MoviePilotFallbackActionDownload,
		domain.MoviePilotFallbackActionDownloadThenSubscribe,
	} {
		if !seen[want] {
			t.Fatalf("actions 缺少 %q", want)
		}
	}
}

// TestGetMoviePilotFallbackSummaryConfigured 已配置时回显真实开关、阈值与动作。
func TestGetMoviePilotFallbackSummaryConfigured(t *testing.T) {
	h, repo := newFallbackAPIHandler(t)
	ctx := context.Background()
	cfg, err := repo.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.BaseUrl = "http://127.0.0.1:3000"
	cfg.ApiToken = "test-token"
	cfg.MpFallbackEnabled = true
	cfg.MpFallbackSearchEnabled = true
	cfg.MpFallbackSearchThreshold = 5
	cfg.MpFallbackSearchAction = domain.MoviePilotFallbackActionSubscribe
	cfg.MpFallbackSubscriptionEnabled = true
	cfg.MpFallbackSubscriptionThreshold = 2
	cfg.MpFallbackSubscriptionAction = domain.MoviePilotFallbackActionDownloadThenSubscribe
	if err := repo.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/fallbacks/summary", nil)
	rec := httptest.NewRecorder()
	h.getMoviePilotFallbackSummary(rec, req)

	data := decodeFallbackResp(t, rec)
	if data["configured"] != true {
		t.Fatalf("configured=%v，期望 true", data["configured"])
	}
	if data["enabled"] != true || data["search_enabled"] != true || data["subscription_enabled"] != true {
		t.Fatalf("开关回显异常：%#v", data)
	}
	if v, _ := data["search_threshold"].(float64); int(v) != 5 {
		t.Fatalf("search_threshold=%v，期望 5", data["search_threshold"])
	}
	if v, _ := data["subscription_threshold"].(float64); int(v) != 2 {
		t.Fatalf("subscription_threshold=%v，期望 2", data["subscription_threshold"])
	}
	if data["search_action"] != domain.MoviePilotFallbackActionSubscribe {
		t.Fatalf("search_action=%v", data["search_action"])
	}
	if data["subscription_action"] != domain.MoviePilotFallbackActionDownloadThenSubscribe {
		t.Fatalf("subscription_action=%v", data["subscription_action"])
	}
}

// TestFallbackEndpointsRejectNilService 服务未装配时明确拒绝，不 panic。
func TestFallbackEndpointsRejectNilService(t *testing.T) {
	h := &Handler{}
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"list":    h.listMoviePilotFallbacks,
		"summary": h.getMoviePilotFallbackSummary,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/moviepilot/fallbacks", nil)
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s：服务为 nil 时不应返回 200，实际 %d", name, rec.Code)
		}
	}
}
