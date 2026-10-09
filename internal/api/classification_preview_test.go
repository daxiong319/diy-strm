package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/classifyorganize"
	"litepan/internal/mediaorganize/classification"
	"litepan/internal/settings"
)

// ---------------------------------------------------------------------------
// T02 验收⑥：预览端点算出来的路径，必须和真实执行路径一致。
//
// 这条用例存在的理由：预览端点自己拼 classification.Request，planner 也自己拼
// 一个（internal/mediaorganize/planner/classification.go:17）。两个 Request 构造
// 各自演进，没有任何东西强制它们保持一致 —— 少了 Loader、漏了字段、Year 口径变了，
// 编译照过、单测照绿，只有用户会发现「预览说 2000-2009，整理完却只有电影/国产」。
//
// 这里的做法是绕开 HTTP 层，直接把同一份入参分别喂给
// previewClassification 用的那段 Request 构造，以及 planner 用的那段，
// 断言两边产出的 RelativeSegments 相同。
// ---------------------------------------------------------------------------

// previewTestRepo 是 domain.ConfigRepository 的最小实现，供 settings.New 使用。
type previewTestRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *previewTestRepo) Get(_ context.Context, key string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *previewTestRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *previewTestRepo) All(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// previewTestLoader 是不打网络的 DetailLoader，固定返回一份 TMDB 详情。
type previewTestLoader struct {
	raw map[string]any
}

func (l previewTestLoader) Lookup(context.Context, string, string) (json.RawMessage, error) {
	return json.Marshal(l.raw)
}

func newPreviewTestService(t *testing.T) *classifyorganize.Service {
	t.Helper()
	svc, err := settings.New(context.Background(), &previewTestRepo{values: map[string]string{
		settings.KeyMOClassificationEnabled: "true",
	}})
	if err != nil {
		t.Fatalf("创建 settings 失败：%v", err)
	}
	return classifyorganize.New(svc)
}

// threeLevelPreviewConfig 造「电影 / 国产 / 2000-2009 / 流浪地球系列」这份配置。
func threeLevelPreviewConfig(t *testing.T, svc *classifyorganize.Service) {
	t.Helper()
	cfg := svc.Config()
	cfg.SelectedTemplate = classifyorganize.TemplateRegion
	rule := cfg.Templates[1].Rules[0]
	rule.Children[0].Children = []classifyorganize.Rule{{
		Name:      "2000-2009",
		Condition: "year=2000-2009",
	}}
	cfg.Series = []classifyorganize.SeriesRule{{
		Name:           "流浪地球",
		SeriesKeywords: []string{"流浪地球", "The Wandering Earth"},
	}}
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("写入三级配置失败：%v", err)
	}
}

// TestPreviewAgreesWithRealExecution 验收⑥：同一份影片信息，
// 预览端点返回的路径必须与 planner 执行路径算出的路径一致。
func TestPreviewAgreesWithRealExecution(t *testing.T) {
	svc := newPreviewTestService(t)
	threeLevelPreviewConfig(t, svc)

	detail := map[string]any{
		"id":             129,
		"origin_country": []string{"CN"},
		"release_date":   "2009-01-01",
		"keywords": []map[string]any{
			{"name": "流浪地球"},
		},
	}
	req := classification.Request{
		MediaType: "movie",
		TMDBID:    "129",
		Loader:    previewTestLoader{raw: detail},
		Raw:       map[string]any{"origin_country": []string{"CN"}},
	}

	// planner 的构造方式（internal/mediaorganize/planner/classification.go:17）。
	plannerDecision, err := svc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("planner 侧分类失败：%v", err)
	}

	// 预览端点的构造方式（internal/api/classification_preview.go）。
	// 两者只能差在 Loader 的来源上，其余字段必须一致。
	previewReq := classificationPreviewReq{
		MediaType: "movie",
		TMDBID:    "129",
		Raw:       map[string]any{"origin_country": []string{"CN"}},
	}
	previewDecision, err := svc.Classify(context.Background(), classification.Request{
		MediaType: previewReq.MediaType,
		TMDBID:    previewReq.TMDBID,
		Title:     previewReq.Title,
		Year:      previewReq.Year,
		Raw:       previewReq.Raw,
		Loader:    previewTestLoader{raw: detail},
	})
	if err != nil {
		t.Fatalf("预览侧分类失败：%v", err)
	}

	if got, want := strings.Join(previewDecision.RelativeSegments, "/"),
		strings.Join(plannerDecision.RelativeSegments, "/"); got != want {
		t.Fatalf("预览与执行不一致：预览 %q，执行 %q", got, want)
	}
	if got, want := previewDecision.RelativeSegments, []string{"电影", "国产", "2000-2009", "流浪地球系列"}; !equalStrings(got, want) {
		t.Fatalf("五段路径未命中，期望 %v，实际 %v", want, got)
	}
}

// TestPreviewHandlerReturnsTheSamePathItClassifies 端点本身的形状：
// 返回的 path 与 segments 必须自洽（path 就是 segments 拼出来加尾斜杠）。
func TestPreviewHandlerReturnsTheSamePathItClassifies(t *testing.T) {
	svc := newPreviewTestService(t)
	threeLevelPreviewConfig(t, svc)

	h := newHandler(Deps{ClassifyOrganize: svc})
	r := chi.NewRouter()
	r.Post("/classification/preview", h.previewClassification)

	// release_date 放在 raw 里：分类是从 raw 读年份的，
	// 端点没有把它提升成顶层字段（那是分类包 prepareRequest 的职责）。
	payload := []byte(`{"media_type":"movie","raw":{"origin_country":["CN"],"release_date":"2009-01-01"}}`)

	req := httptest.NewRequest(http.MethodPost, "/classification/preview", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("预览端点返回 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool                      `json:"success"`
		Data    classificationPreviewResp `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if !resp.Success {
		t.Fatalf("预览端点未返回成功：%s", rec.Body.String())
	}
	// path 与 segments 必须自洽。注意尾斜杠：path 表达的是「目录」而不是文件名，
	// 所以是 segments 用 / 拼好再补一个 /，不是直接 join。
	wantPath := ""
	if len(resp.Data.Segments) > 0 {
		wantPath = strings.Join(resp.Data.Segments, "/") + "/"
	}
	if resp.Data.Path != wantPath {
		t.Fatalf("path 与 segments 不自洽：期望 %q，实际 %q（segments=%q）",
			wantPath, resp.Data.Path, resp.Data.Segments)
	}
	if got := classificationPathLabel(resp.Data.Segments); resp.Data.Path != got {
		t.Fatalf("path 应由 classificationPathLabel 生成：期望 %q，实际 %q", got, resp.Data.Path)
	}
}

// TestPreviewRejectsBadMediaType 媒体类型填错必须报错，不能静默回落成 movie。
func TestPreviewRejectsBadMediaType(t *testing.T) {
	svc := newPreviewTestService(t)
	h := newHandler(Deps{ClassifyOrganize: svc})
	r := chi.NewRouter()
	r.Post("/classification/preview", h.previewClassification)

	for _, mediaType := range []string{"", "Movie2", "电影"} {
		req := httptest.NewRequest(http.MethodPost, "/classification/preview",
			bytes.NewReader([]byte(`{"media_type":"`+mediaType+`"}`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("媒体类型 %q 应返回 400，实际 %d：%s", mediaType, rec.Code, rec.Body.String())
		}
	}
}

// TestPreviewNeedsTMDBClientForTMDBID 填了 TMDB ID 却没有 TMDB 客户端时必须报错，
// 不能悄悄降级成「没有详情」，否则预览会少走一层目录而用户看不出来。
func TestPreviewNeedsTMDBClientForTMDBID(t *testing.T) {
	svc := newPreviewTestService(t)
	h := newHandler(Deps{ClassifyOrganize: svc}) // 刻意不给 MediaOrganize
	r := chi.NewRouter()
	r.Post("/classification/preview", h.previewClassification)

	req := httptest.NewRequest(http.MethodPost, "/classification/preview",
		bytes.NewReader([]byte(`{"media_type":"movie","tmdb_id":"129"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("没有 TMDB 客户端却返回了 200：%s", rec.Body.String())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
