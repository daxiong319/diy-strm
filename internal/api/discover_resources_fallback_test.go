package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"litepan/internal/domain"
)

// 本文件覆盖「资源搜索 → MoviePilot 降级兜底计数」这条接线的回归测试。
//
// 为什么需要这些测试：internal/moviepilot/fallback.go 曾有一整套引擎与
// 观测端点，但**没有任何生产调用方**，导致计数永远不会增长、兜底永不触发。
// 这些测试直接锁定「无候选累加 / 有候选清零」的真实行为，防止该缺陷复发。
//
// 注意：不经过 searchMediaResources 的完整 HTTP 路径，因为任一资源来源
// （tg/seedhub）都会触及包级 discovery_settings 全局单例，而在内存仓储的
// 单元测试里该全局未初始化。这里直接调用被接线的方法，覆盖的正是新增逻辑；
// 真实来源的端到端行为由实跑冒烟验证覆盖。

func newFallbackWiringHandler(t *testing.T) (*Handler, domain.MoviePilotRepository) {
	t.Helper()
	return newFallbackAPIHandler(t)
}

func enableFallbackSearch(t *testing.T, repo domain.MoviePilotRepository, threshold int) {
	t.Helper()
	cfg := domain.MoviePilotConfig{
		MpFallbackEnabled:         true,
		MpFallbackSearchEnabled:   true,
		MpFallbackSearchThreshold: threshold,
	}
	if err := repo.SaveConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("保存兜底配置失败：%v", err)
	}
}

// fallbackSearchReq 构造一个带 TMDB ID 的请求体，模拟剧集搜索。
func fallbackSearchReq(tmdbID int64, season int) resourceSearchRequest {
	return resourceSearchRequest{
		Title:     "测试剧集",
		TmdbID:    tmdbID,
		MediaType: "tv",
		Season:    season,
		Sources:   []string{"tg"},
	}
}

// TestRecordSearchFallbackOutcomeAccumulatesOnZeroCandidates 验证「无候选 → 累加」。
func TestRecordSearchFallbackOutcomeAccumulatesOnZeroCandidates(t *testing.T) {
	h, repo := newFallbackWiringHandler(t)
	ctx := context.Background()
	enableFallbackSearch(t, repo, 3)

	req := fallbackSearchReq(88001, 2)
	key := domain.MoviePilotFallbackMediaKey("tv", 88001, 2)

	h.recordSearchFallbackOutcome(ctx, req, "tv", false)

	rec, err := repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSearch)
	if err != nil {
		t.Fatalf("读取兜底记录失败：%v", err)
	}
	if rec == nil {
		t.Fatalf("无候选搜索后应产生兜底记录（media_key=%s）", key)
	}
	if rec.SearchCount != 1 {
		t.Errorf("SearchCount 应为 1，实际 %d", rec.SearchCount)
	}
	if rec.TmdbId != 88001 || rec.Season != 2 {
		t.Errorf("TMDB ID / 季号应透传，实际 tmdb=%d season=%d", rec.TmdbId, rec.Season)
	}
}

// TestRecordSearchFallbackOutcomeResetsOnCandidates 验证「有候选 → 清零」。
//
// 这是累计语义的关键：搜索侧允许跨轮累加，但一旦本轮拿到候选就必须归零，
// 否则用户明明搜到了资源，计数器还在涨、迟早误触发降级。
func TestRecordSearchFallbackOutcomeResetsOnCandidates(t *testing.T) {
	h, repo := newFallbackWiringHandler(t)
	ctx := context.Background()
	enableFallbackSearch(t, repo, 3)

	req := fallbackSearchReq(88002, 1)
	key := domain.MoviePilotFallbackMediaKey("tv", 88002, 1)
	// repo 已由 newFallbackWiringHandler 返回

	h.recordSearchFallbackOutcome(ctx, req, "tv", false)
	h.recordSearchFallbackOutcome(ctx, req, "tv", false)
	if rec, _ := repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSearch); rec == nil || rec.SearchCount != 2 {
		t.Fatalf("前置条件不满足：期望 SearchCount=2，实际 %+v", rec)
	}

	h.recordSearchFallbackOutcome(ctx, req, "tv", true)

	rec, err := repo.GetFallback(ctx, key, domain.MoviePilotFallbackTriggerSearch)
	if err != nil {
		t.Fatalf("读取兜底记录失败：%v", err)
	}
	if rec != nil && rec.SearchCount != 0 {
		t.Errorf("有候选时应清零 SearchCount，实际 %d", rec.SearchCount)
	}
}

// TestRecordSearchFallbackOutcomeIgnoresZeroTmdbID 验证缺 TMDB ID 时不计数。
func TestRecordSearchFallbackOutcomeIgnoresZeroTmdbID(t *testing.T) {
	h, repo := newFallbackWiringHandler(t)
	ctx := context.Background()
	enableFallbackSearch(t, repo, 3)

	h.recordSearchFallbackOutcome(ctx, fallbackSearchReq(0, 1), "tv", false)

	list, total, err := repo.ListFallbacks(ctx, 1, 50, "")
	if err != nil {
		t.Fatalf("列出兜底记录失败：%v", err)
	}
	if len(list) != 0 || total != 0 {
		t.Errorf("缺 TMDB ID 时不应产生记录，实际 %d 条（total=%d）", len(list), total)
	}
}

// TestRecordSearchFallbackOutcomeSilentWhenDisabled 验证兜底未启用时完全静默。
func TestRecordSearchFallbackOutcomeSilentWhenDisabled(t *testing.T) {
	h, repo := newFallbackWiringHandler(t)
	ctx := context.Background()

	h.recordSearchFallbackOutcome(ctx, fallbackSearchReq(88003, 1), "tv", false)

	list, total, err := repo.ListFallbacks(ctx, 1, 50, "")
	if err != nil {
		t.Fatalf("列出兜底记录失败：%v", err)
	}
	if len(list) != 0 || total != 0 {
		t.Errorf("兜底未启用时不应产生记录，实际 %d 条（total=%d）", len(list), total)
	}
}

// TestRecordSearchFallbackOutcomeNilServiceIsSafe 验证未装配服务时不 panic。
func TestRecordSearchFallbackOutcomeNilServiceIsSafe(t *testing.T) {
	h := &Handler{}
	h.recordSearchFallbackOutcome(context.Background(), fallbackSearchReq(1, 1), "tv", false)
}

// TestSearchHandlerRejectsMissingTitleAndTmdb 锁定入参校验分支，
// 确保接线插入位置（排序后、写响应前）没有破坏既有校验顺序。
func TestSearchHandlerRejectsMissingTitleAndTmdb(t *testing.T) {
	h, _ := newFallbackWiringHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/media-discovery/resources/search",
		strings.NewReader(`{"sources":["tg"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.searchMediaResources(rec, req)

	if rec.Code == http.StatusOK {
		t.Errorf("标题与 TMDB ID 都缺失时应报错，实际 200：%s", rec.Body.String())
	}
}
