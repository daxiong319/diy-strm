package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// newPlaybackRecordTestHandler 用内存库构造带播放记录仓储的 Handler，
// 直接打真实仓储，覆盖路由到 SQL 的完整链路。
func newPlaybackRecordTestHandler(t *testing.T) (*Handler, domain.PlaybackRecordRepository) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	repo := store.New(db).PlaybackRecords
	return &Handler{storePlaybackRecords: repo}, repo
}

// decodeData 解析统一响应体中的 data 字段。
func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应: %v, body = %s", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatalf("期望 success=true, body = %s", rec.Body.String())
	}
	return resp.Data
}

// TestPlaybackRecordsHandlerListAndStats 列表接口契约：
// items/total/page/page_size 四个字段与老版 /api/emby302/playback-records 对齐。
func TestPlaybackRecordsHandlerListAndStats(t *testing.T) {
	h, repo := newPlaybackRecordTestHandler(t)
	ctx := context.Background()

	for _, rec := range []*domain.PlaybackRecord{
		{UserID: "u1", ItemName: "三体.mkv", StrmPath: "/115/三体.mkv", Provider: "115", PlaybackAt: "2024-05-01T00:00:00Z"},
		{UserID: "u2", ItemName: "沙丘2.mkv", StrmPath: "/123/沙丘2.mkv", Provider: "123", PlaybackAt: "2024-05-02T00:00:00Z"},
	} {
		if _, err := repo.Insert(ctx, rec); err != nil {
			t.Fatalf("预置记录: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/playback-records?page=1&page_size=10", nil)
	rec := httptest.NewRecorder()
	h.playbackRecords(rec, req)
	data := decodeData(t, rec)

	if got, ok := data["total"].(float64); !ok || got != 2 {
		t.Fatalf("total = %v, 期望 2", data["total"])
	}
	if got, ok := data["page"].(float64); !ok || got != 1 {
		t.Fatalf("page = %v, 期望 1", data["page"])
	}
	if got, ok := data["page_size"].(float64); !ok || got != 10 {
		t.Fatalf("page_size = %v, 期望 10", data["page_size"])
	}
	items, ok := data["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %v, 期望 2 条", data["items"])
	}
	first := items[0].(map[string]any)
	if first["item_name"] != "沙丘2.mkv" {
		t.Fatalf("首条应为最新记录, 实际 %v", first["item_name"])
	}
	// 字段名必须与前端 DTO 一致
	for _, key := range []string{"id", "rule_id", "user_id", "client", "device_id", "item_name", "strm_path", "provider", "playback_at"} {
		if _, exists := first[key]; !exists {
			t.Fatalf("响应缺少字段 %q", key)
		}
	}

	// 统计接口
	req = httptest.NewRequest(http.MethodGet, "/api/admin/playback-records/stats", nil)
	rec = httptest.NewRecorder()
	h.playbackRecordsStats(rec, req)
	stats := decodeData(t, rec)
	if got := stats["total"].(float64); got != 2 {
		t.Fatalf("stats.total = %v, 期望 2", got)
	}
	if got := stats["user_count"].(float64); got != 2 {
		t.Fatalf("stats.user_count = %v, 期望 2", got)
	}
	if stats["last_at"] != "2024-05-02T00:00:00Z" {
		t.Fatalf("stats.last_at = %v", stats["last_at"])
	}
}

// TestPlaybackRecordsHandlerFilters 查询参数透传到仓储过滤条件。
func TestPlaybackRecordsHandlerFilters(t *testing.T) {
	h, repo := newPlaybackRecordTestHandler(t)
	ctx := context.Background()

	for _, rec := range []*domain.PlaybackRecord{
		{RuleID: "1", UserID: "u1", ItemName: "三体.mkv", StrmPath: "/115/三体.mkv", Provider: "115", PlaybackAt: "2024-05-01T00:00:00Z"},
		{RuleID: "2", UserID: "u1", ItemName: "沙丘2.mkv", StrmPath: "/123/沙丘2.mkv", Provider: "123", PlaybackAt: "2024-05-02T00:00:00Z"},
	} {
		if _, err := repo.Insert(ctx, rec); err != nil {
			t.Fatalf("预置记录: %v", err)
		}
	}

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"按规则 id", "/api/admin/playback-records?id=1", 1},
		{"按用户", "/api/admin/playback-records?user_id=u1", 2},
		{"按网盘", "/api/admin/playback-records?provider=123", 1},
		{"关键词命中条目名", "/api/admin/playback-records?keyword=三体", 1},
		{"关键词命中路径", "/api/admin/playback-records?keyword=123", 1},
		{"无匹配", "/api/admin/playback-records?keyword=不存在", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			h.playbackRecords(rec, req)
			data := decodeData(t, rec)
			if got := data["total"].(float64); int(got) != tc.want {
				t.Fatalf("total = %v, 期望 %d", got, tc.want)
			}
		})
	}
}

// TestPlaybackRecordsHandlerDeleteAndClear 删除与清空接口。
func TestPlaybackRecordsHandlerDeleteAndClear(t *testing.T) {
	h, repo := newPlaybackRecordTestHandler(t)
	ctx := context.Background()

	first := &domain.PlaybackRecord{RuleID: "1", UserID: "u1", ItemName: "a.mkv", PlaybackAt: "2024-05-01T00:00:00Z"}
	second := &domain.PlaybackRecord{RuleID: "2", UserID: "u2", ItemName: "b.mkv", PlaybackAt: "2024-05-02T00:00:00Z"}
	for _, rec := range []*domain.PlaybackRecord{first, second} {
		if _, err := repo.Insert(ctx, rec); err != nil {
			t.Fatalf("预置记录: %v", err)
		}
	}

	// 删除单条：/{id} 依赖 chi 的 URL 参数，用真实路由挂载后再发请求。
	mux := chi.NewRouter()
	mux.Delete("/playback-records/{id}", h.playbackRecordDelete)
	doDelete := func(rawID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/playback-records/"+rawID, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := doDelete(strconv.FormatInt(first.ID, 10))
	data := decodeData(t, rec)
	if got := data["deleted"].(float64); got != 1 {
		t.Fatalf("deleted = %v, 期望 1", got)
	}
	if _, total, err := repo.List(ctx, domain.PlaybackRecordQuery{}); err != nil {
		t.Fatalf("查询: %v", err)
	} else if total != 1 {
		t.Fatalf("删除后剩余 %d, 期望 1", total)
	}

	// 非法 ID 应返回错误而不是 200
	if rec := doDelete("abc"); rec.Code == http.StatusOK {
		t.Fatalf("非法 ID 不应返回 200, body = %s", rec.Body.String())
	}

	// 空请求体清空全部
	req := httptest.NewRequest(http.MethodPost, "/api/admin/playback-records/clear", nil)
	clearRec := httptest.NewRecorder()
	h.playbackRecordsClear(clearRec, req)
	data = decodeData(t, clearRec)
	if got := data["deleted"].(float64); got != 1 {
		t.Fatalf("deleted = %v, 期望 1", got)
	}

	// 按规则清空
	if _, err := repo.Insert(ctx, &domain.PlaybackRecord{RuleID: "7", UserID: "u9", ItemName: "z.mkv"}); err != nil {
		t.Fatalf("预置记录: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/admin/playback-records/clear",
		strings.NewReader(`{"rule_id":"7"}`))
	clearRec = httptest.NewRecorder()
	h.playbackRecordsClear(clearRec, req)
	data = decodeData(t, clearRec)
	if got := data["deleted"].(float64); got != 1 {
		t.Fatalf("按规则清空 deleted = %v, 期望 1", got)
	}
}

// TestPlaybackRecordsHandlerUnavailable 仓储缺失时返回未接入而不是 panic。
func TestPlaybackRecordsHandlerUnavailable(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/playback-records", nil)
	rec := httptest.NewRecorder()
	h.playbackRecords(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("仓储缺失不应返回 200, body = %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.playbackRecordsStats(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("仓储缺失 stats 不应返回 200, body = %s", rec.Body.String())
	}
}
