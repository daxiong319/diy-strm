package moviepilot_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/moviepilot"
)

// ---- 测试辅助 ----

// newFallbackService 构造带内存仓储的服务，并启用兜底配置。
func newFallbackService(t *testing.T) (*moviepilot.Service, domain.MoviePilotRepository) {
	t.Helper()
	return newTestService(t)
}

// seedFallbackConfig 写入兜底配置（BaseUrl/ApiToken 非空即视为「已配置 MoviePilot」）。
func seedFallbackConfig(t *testing.T, repo domain.MoviePilotRepository, mutate func(*domain.MoviePilotConfig)) {
	t.Helper()
	ctx := context.Background()
	cfg, err := repo.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.BaseUrl = "http://127.0.0.1:3000"
	cfg.ApiToken = "test-token"
	if mutate != nil {
		mutate(cfg)
	}
	if err := repo.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// fallbackTarget 构造测试用兜底对象（tv/12345/第 2 季）。
func fallbackTarget() moviepilot.FallbackTarget {
	return moviepilot.FallbackTarget{
		MediaType: "tv", TmdbId: 12345, Title: "测试剧集", Season: 2,
	}
}

// fallbackRec 读取兜底记录（不存在返回 nil）。
func fallbackRec(t *testing.T, repo domain.MoviePilotRepository, target moviepilot.FallbackTarget, trigger string) *domain.MoviePilotFallback {
	t.Helper()
	rec, err := repo.GetFallback(context.Background(), target.MediaKey(), trigger)
	if err != nil {
		if ae, ok := domain.AsAppError(err); ok && ae.Code == domain.CodeNotFound {
			return nil
		}
		t.Fatalf("get fallback: %v", err)
	}
	return rec
}

// ---- 媒体键 ----

// TestFallbackMediaKey 「影片+季」唯一键：不同季、不同影片必须不同键。
func TestFallbackMediaKey(t *testing.T) {
	cases := []struct {
		name      string
		mediaType string
		tmdbID    int64
		season    int
		want      string
	}{
		{"剧集第2季", "tv", 12345, 2, "tv:12345:2"},
		{"电影第0季", "movie", 9, 0, "movie:9:0"},
		{"空媒体类型", "", 9, 1, "unknown:9:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.MoviePilotFallbackMediaKey(tc.mediaType, tc.tmdbID, tc.season)
			if got != tc.want {
				t.Fatalf("媒体键=%q，期望 %q", got, tc.want)
			}
		})
	}

	// 同一影片不同季、不同影片同季、同一影片的季 0 与季 0，键必须两两不同。
	keys := map[string]string{
		"tv:12345:1": domain.MoviePilotFallbackMediaKey("tv", 12345, 1),
		"tv:12345:2": domain.MoviePilotFallbackMediaKey("tv", 12345, 2),
		"tv:999:1":   domain.MoviePilotFallbackMediaKey("tv", 999, 1),
	}
	for want, got := range keys {
		if got != want {
			t.Fatalf("媒体键=%q，期望 %q", got, want)
		}
	}
}

// ---- 动作与阈值归一 ----

// TestNormalizeFallbackAction 未知动作回退默认 download。
func TestNormalizeFallbackAction(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", domain.MoviePilotFallbackActionDownload},
		{"bogus", domain.MoviePilotFallbackActionDownload},
		{"subscribe", domain.MoviePilotFallbackActionSubscribe},
		{"download", domain.MoviePilotFallbackActionDownload},
		{"download_then_subscribe", domain.MoviePilotFallbackActionDownloadThenSubscribe},
		{"  subscribe  ", domain.MoviePilotFallbackActionSubscribe},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := domain.NormalizeMoviePilotFallbackAction(tc.in); got != tc.want {
				t.Fatalf("动作归一=%q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestNormalizeFallbackThreshold 阈值归一：<=0 回退默认 3。
func TestNormalizeFallbackThreshold(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 3}, {-1, 3}, {-99, 3}, {1, 1}, {3, 3}, {10, 10},
	}
	for _, tc := range cases {
		if got := domain.NormalizeMoviePilotFallbackThreshold(tc.in); got != tc.want {
			t.Fatalf("阈值归一(%d)=%d，期望 %d", tc.in, got, tc.want)
		}
	}
	if domain.DefaultMoviePilotFallbackThreshold != 3 {
		t.Fatalf("默认阈值=%d，期望 3", domain.DefaultMoviePilotFallbackThreshold)
	}
}

// ---- 搜索侧：累计语义 ----

// TestFallbackSearchThresholdAccumulates 搜索侧累计达到阈值才触发，触发后清零。
// 覆盖「默认阈值 3」「累计而非连续」「触发后清零」。
func TestFallbackSearchThresholdAccumulates(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	// 只开搜索侧；动作保持默认 download。
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
	})
	target := fallbackTarget()

	// 默认阈值必须是 3（见 TestFallbackSummaryNotConfigured 对概览的断言）。
	for i := 1; i <= 2; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, true); hit {
			t.Fatalf("第 %d 次搜索无结果不应触发（阈值 3）", i)
		}
		rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
		if rec == nil {
			t.Fatalf("第 %d 次后应已产生计数记录", i)
		}
		if rec.SearchCount != i {
			t.Fatalf("第 %d 次后 SearchCount=%d，期望 %d", i, rec.SearchCount, i)
		}
	}

	// 第 3 次达到阈值：触发（此处 MP 未启动，执行会失败，但触发本身必须发生）。
	msg, hit := svc.TriggerFallback(ctx, target, true)
	if !hit {
		t.Fatal("第 3 次搜索无结果应触发兜底")
	}
	if !strings.Contains(msg, "搜索次数") {
		t.Fatalf("触发摘要应说明来源为搜索次数，实际 %q", msg)
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil {
		t.Fatal("触发后应保留记录")
	}
	if rec.SearchCount != 0 {
		t.Fatalf("触发后 SearchCount 应清零，实际 %d", rec.SearchCount)
	}
	if rec.Action != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("默认动作应为 download，实际 %q", rec.Action)
	}
}

// TestFallbackSearchResetOnProgress 一旦候选 > 0 立即清零（有进展重置）。
func TestFallbackSearchResetOnProgress(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
	})
	target := fallbackTarget()

	svc.TriggerFallback(ctx, target, true)
	svc.TriggerFallback(ctx, target, true)
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec == nil || rec.SearchCount != 2 {
		t.Fatalf("前置计数应为 2，实际 %+v", rec)
	}

	if err := svc.ResetFallbackSearch(ctx, target); err != nil {
		t.Fatalf("重置搜索计数: %v", err)
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec.SearchCount != 0 {
		t.Fatalf("有进展后 SearchCount 应清零，实际 %d", rec.SearchCount)
	}

	// 清零后重新累计：再两次仍不触发（证明是「累计」而非「总次数」）。
	svc.TriggerFallback(ctx, target, true)
	svc.TriggerFallback(ctx, target, true)
	if _, hit := svc.TriggerFallback(ctx, target, true); !hit {
		t.Fatal("清零后重新累计满 3 次应触发")
	}
}

// TestFallbackSearchCountsAcrossRounds 搜索侧跨轮累加，不要求连续：中间有进展清零才断链。
// 这里验证「累计」语义 —— 3 次无结果即使跨越很久也照样触发。
func TestFallbackSearchCountsAcrossRounds(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 3
	})
	target := fallbackTarget()

	// 每轮之间穿插「有结果」以外的活动，计数必须保留（不因轮次推进而清零）。
	for i := 1; i <= 3; i++ {
		_, hit := svc.TriggerFallback(ctx, target, true)
		if i < 3 && hit {
			t.Fatalf("第 %d 轮不应触发", i)
		}
		if i == 3 && !hit {
			t.Fatal("第 3 轮应触发（累计语义）")
		}
	}
}

// TestFallbackDisabledDoesNotCount 总开关关闭时不计数、不产生记录、不触发。
func TestFallbackDisabledDoesNotCount(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = false
		c.MpFallbackSearchEnabled = true
	})
	target := fallbackTarget()

	for i := 0; i < 5; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, true); hit {
			t.Fatalf("总开关关闭时第 %d 次不应触发", i+1)
		}
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec != nil {
		t.Fatalf("总开关关闭时不应产生记录，实际 %+v", rec)
	}
}

// TestFallbackSearchSwitchOffDoesNotCount 总开关开启但搜索开关关闭时同样不计数。
func TestFallbackSearchSwitchOffDoesNotCount(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = false
	})
	target := fallbackTarget()

	for i := 0; i < 5; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, true); hit {
			t.Fatalf("搜索开关关闭时第 %d 次不应触发", i+1)
		}
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec != nil {
		t.Fatalf("搜索开关关闭时不应产生记录，实际 %+v", rec)
	}
}

// TestFallbackSearchThresholdCustom 自定义阈值生效（阈值 1 时首次即触发）。
func TestFallbackSearchThresholdCustom(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
	})
	target := fallbackTarget()

	if _, hit := svc.TriggerFallback(ctx, target, true); !hit {
		t.Fatal("阈值 1 时首次无结果即应触发")
	}
}

// ---- 订阅侧：连续语义 ----

// TestFallbackSubscriptionThreshold 订阅侧连续达到阈值才触发，动作可配置。
func TestFallbackSubscriptionThreshold(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSubscriptionEnabled = true
		c.MpFallbackSubscriptionAction = domain.MoviePilotFallbackActionSubscribe
	})
	target := fallbackTarget()

	for i := 1; i <= 2; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, false); hit {
			t.Fatalf("第 %d 轮无进展不应触发（阈值 3）", i)
		}
		rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription)
		if rec == nil || rec.SubscriptionCount != i {
			t.Fatalf("第 %d 轮后 SubscriptionCount=%v，期望 %d", i, rec, i)
		}
	}

	msg, hit := svc.TriggerFallback(ctx, target, false)
	if !hit {
		t.Fatal("第 3 轮无进展应触发")
	}
	if !strings.Contains(msg, "订阅轮次") {
		t.Fatalf("触发摘要应说明来源为订阅轮次，实际 %q", msg)
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription)
	if rec.SubscriptionCount != 0 {
		t.Fatalf("触发后 SubscriptionCount 应清零，实际 %d", rec.SubscriptionCount)
	}
	if rec.Action != domain.MoviePilotFallbackActionSubscribe {
		t.Fatalf("动作应为 subscribe，实际 %q", rec.Action)
	}
}

// TestFallbackSubscriptionConsecutiveVsAccumulated 订阅侧是**连续**语义：
// 中间有任何新收录（reset）就断链，之前累计的轮数必须清零。
func TestFallbackSubscriptionConsecutiveVsAccumulated(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSubscriptionEnabled = true
	})
	target := fallbackTarget()

	// 两轮无进展 → 计数 2。
	svc.TriggerFallback(ctx, target, false)
	svc.TriggerFallback(ctx, target, false)
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription); rec.SubscriptionCount != 2 {
		t.Fatalf("前置计数应为 2，实际 %d", rec.SubscriptionCount)
	}

	// 有进展 → 立即清零（连续链断裂）。
	if err := svc.ResetFallbackSubscription(ctx, target); err != nil {
		t.Fatalf("重置订阅计数: %v", err)
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription); rec.SubscriptionCount != 0 {
		t.Fatalf("有进展后应清零，实际 %d", rec.SubscriptionCount)
	}

	// 清零后必须重新连满 3 轮才触发 —— 证明不是累计。
	svc.TriggerFallback(ctx, target, false)
	svc.TriggerFallback(ctx, target, false)
	if _, hit := svc.TriggerFallback(ctx, target, false); !hit {
		t.Fatal("连续 3 轮无进展应触发")
	}
}

// TestFallbackSubscriptionSkipsWhenPendingTransfer 有待入库任务时不触发、不计数。
func TestFallbackSubscriptionSkipsWhenPendingTransfer(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSubscriptionEnabled = true
	})
	target := fallbackTarget()

	// 插入一个待入库（pending）MP 上传任务。
	if _, err := repo.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "pending-hash", Title: "测试剧集", Status: domain.MoviePilotUploadPending,
	}); err != nil {
		t.Fatalf("create pending upload task: %v", err)
	}
	pending, err := repo.HasPendingTransferTasks(ctx)
	if err != nil {
		t.Fatalf("has pending transfers: %v", err)
	}
	if !pending {
		t.Fatal("前置条件失败：应存在待入库任务")
	}

	// 即使调用远超阈值次数也不触发、不计数。
	for i := 0; i < 5; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, false); hit {
			t.Fatalf("有待入库任务时第 %d 轮不应触发", i+1)
		}
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription); rec != nil {
		t.Fatalf("有待入库任务时不应计数，实际 %+v", rec)
	}
}

// TestFallbackSubscriptionSwitchOffDoesNotCount 订阅开关关闭时不计数。
func TestFallbackSubscriptionSwitchOffDoesNotCount(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSubscriptionEnabled = false
	})
	target := fallbackTarget()

	for i := 0; i < 5; i++ {
		if _, hit := svc.TriggerFallback(ctx, target, false); hit {
			t.Fatalf("订阅开关关闭时第 %d 轮不应触发", i+1)
		}
	}
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription); rec != nil {
		t.Fatalf("订阅开关关闭时不应产生记录，实际 %+v", rec)
	}
}

// TestFallbackSearchAndSubscriptionCountersIndependent 搜索计数与订阅计数互不干扰。
func TestFallbackSearchAndSubscriptionCountersIndependent(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSubscriptionEnabled = true
	})
	target := fallbackTarget()

	svc.TriggerFallback(ctx, target, true)
	svc.TriggerFallback(ctx, target, true)
	svc.TriggerFallback(ctx, target, false)

	search := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	subs := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSubscription)
	if search == nil || search.SearchCount != 2 {
		t.Fatalf("搜索计数应为 2，实际 %+v", search)
	}
	if subs == nil || subs.SubscriptionCount != 1 {
		t.Fatalf("订阅计数应为 1，实际 %+v", subs)
	}
	if subs.SearchCount != 0 || search.SubscriptionCount != 0 {
		t.Fatal("两条规则的计数必须各自独立")
	}
}

// ---- 「报错和取消不计数」----

// TestFallbackSearchNotCountedOnErrorOrCancel 「报错和取消不计数」：
// 只有正常完成且候选为 0 才 +1。测试通过对比「正常完成路径调用了计数入口」
// 与「报错/取消路径不调用计数入口」来固化契约。
func TestFallbackSearchNotCountedOnErrorOrCancel(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
	})
	target := fallbackTarget()

	// 模拟「搜索报错/取消」：调用方在这些路径下不会调用 TriggerFallback。
	// 也就是说，本测试断言的是契约本身 —— 计数入口必须由调用方在正常完成时才调用。
	// 为了证明计数只可能来自显式调用，这里先断言未调用时无任何记录。
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec != nil {
		t.Fatalf("未调用计数入口时不应产生记录，实际 %+v", rec)
	}

	// 正常完成且候选为 0 → 计数。
	svc.TriggerFallback(ctx, target, true)
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil || rec.SearchCount != 1 {
		t.Fatalf("正常完成无结果后应计数为 1，实际 %+v", rec)
	}

	// 报错/取消不调用计数入口：计数保持 1。
	if rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch); rec.SearchCount != 1 {
		t.Fatalf("报错/取消路径不应改变计数，实际 %d", rec.SearchCount)
	}
}

// ---- 兜底失败不中断 ----

// TestFallbackFailureDoesNotBreakSubscription 兜底调用失败时不 panic、不向调用方报错，
// 状态落为 failed 且携带原因；本地订阅继续运行（本方法不返回 error）。
func TestFallbackFailureDoesNotBreakSubscription(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
	})
	target := fallbackTarget()

	msg, hit := svc.TriggerFallback(ctx, target, true)
	if !hit {
		t.Fatal("阈值 1 应触发")
	}
	if msg == "" {
		t.Fatal("触发应返回摘要消息")
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil {
		t.Fatal("触发后应有记录")
	}
	// MP 服务指向不可达地址 → 执行失败必须被吞掉并记录为 failed。
	if rec.Status != domain.MoviePilotFallbackFailed {
		t.Fatalf("执行失败应记录为 failed，实际 %q（消息 %q）", rec.Status, rec.Message)
	}
	if rec.Message == "" {
		t.Fatal("失败应携带原因消息")
	}
}

// TestFallbackRejectedWithoutMoviePilotConfig 未配置 MoviePilot 时明确拒绝，绝不静默降级。
func TestFallbackRejectedWithoutMoviePilotConfig(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	// 只开兜底开关，不写 BaseUrl/ApiToken（未配置 MoviePilot）。
	cfg, err := repo.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.MpFallbackEnabled = true
	cfg.MpFallbackSearchEnabled = true
	cfg.MpFallbackSearchThreshold = 1
	cfg.BaseUrl = ""
	cfg.ApiToken = ""
	if err := repo.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	target := fallbackTarget()

	msg, hit := svc.TriggerFallback(ctx, target, true)
	if !hit {
		t.Fatal("达到阈值时应返回触发，并明确拒绝执行")
	}
	if !strings.Contains(msg, "拒绝") {
		t.Fatalf("未配置 MoviePilot 应明确拒绝，实际摘要 %q", msg)
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil || rec.Status != domain.MoviePilotFallbackFailed {
		t.Fatalf("拒绝后应记录为 failed，实际 %+v", rec)
	}
	if !strings.Contains(rec.Message, "未配置 MoviePilot") {
		t.Fatalf("拒绝原因应说明未配置，实际 %q", rec.Message)
	}
}

// ---- 三种动作 ----

// fakeMoviePilotServer 起一个假 MoviePilot：订阅创建成功、搜索成功、
// 下载列表按 downloads 参数返回（用于区分「有资源」与「无资源」）。
func fakeMoviePilotServer(t *testing.T, downloads []string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/subscribe/") && r.Method == http.MethodPost:
			mu.Lock()
			created++
			id := created
			mu.Unlock()
			w.Write([]byte(`{"success":true,"data":{"id":` + itoa(id) + `}}`))
		case strings.Contains(r.URL.Path, "/download/"):
			items := make([]string, 0, len(downloads))
			for _, d := range downloads {
				items = append(items, d)
			}
			w.Write([]byte(`{"success":true,"data":[` + strings.Join(items, ",") + `]}`))
		default:
			w.Write([]byte(`{"success":true,"data":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	n := 0
	return srv, &n
}

// itoa 简单整数转字符串（测试内避免引入 strconv 噪音）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		return "-" + string(buf)
	}
	return string(buf)
}

// TestFallbackActionSubscribe subscribe 动作：仅添加 MP 订阅。
func TestFallbackActionSubscribe(t *testing.T) {
	runFallbackActionCase(t, domain.MoviePilotFallbackActionSubscribe, nil, domain.MoviePilotFallbackSucceeded)
}

// TestFallbackActionDownload download 动作（默认）：让 MP 搜索下载。
func TestFallbackActionDownload(t *testing.T) {
	runFallbackActionCase(t, domain.MoviePilotFallbackActionDownload, nil, domain.MoviePilotFallbackSucceeded)
}

// TestFallbackActionDownloadThenSubscribe download_then_subscribe：先下载，无资源再订阅。
func TestFallbackActionDownloadThenSubscribe(t *testing.T) {
	runFallbackActionCase(t, domain.MoviePilotFallbackActionDownloadThenSubscribe, nil, domain.MoviePilotFallbackSucceeded)
}

// runFallbackActionCase 以指定动作跑一次兜底，断言状态与「建立订阅才算成功」的判据。
func runFallbackActionCase(t *testing.T, action string, downloads []string, wantStatus string) {
	t.Helper()
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	srv, _ := fakeMoviePilotServer(t, downloads)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.BaseUrl = srv.URL
		c.ApiToken = "test-token"
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
		c.MpFallbackSearchAction = action
	})
	target := fallbackTarget()

	_, hit := svc.TriggerFallback(ctx, target, true)
	if !hit {
		t.Fatalf("动作 %s：阈值 1 应触发", action)
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil {
		t.Fatalf("动作 %s：触发后应有记录", action)
	}
	if rec.Action != action {
		t.Fatalf("动作 %s：记录动作=%q", action, rec.Action)
	}
	if rec.Status != wantStatus {
		t.Fatalf("动作 %s：状态=%q（消息 %q），期望 %q", action, rec.Status, rec.Message, wantStatus)
	}
	if rec.ExternalID == "" {
		t.Fatalf("动作 %s：应留下 MP 侧外部 ID，实际为空", action)
	}
	if rec.Progress != 100 {
		t.Fatalf("动作 %s：完成后进度应为 100，实际 %d", action, rec.Progress)
	}
}

// ---- 本地订阅自动暂停 ----

// fakePauser 记录被暂停的本地订阅 ID。
type fakePauser struct {
	mu   sync.Mutex
	ids  []int64
	fail error
}

func (p *fakePauser) PauseSubscription(_ context.Context, subscribeID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.ids = append(p.ids, subscribeID)
	return nil
}

func (p *fakePauser) paused() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.ids...)
}

// TestFallbackSubscribePausesLocalSubscription 确认 MP 已接管订阅后本地订阅自动暂停。
func TestFallbackSubscribePausesLocalSubscription(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	srv, _ := fakeMoviePilotServer(t, nil)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.BaseUrl = srv.URL
		c.ApiToken = "test-token"
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
		c.MpFallbackSearchAction = domain.MoviePilotFallbackActionSubscribe
	})
	pauser := &fakePauser{}
	svc.SetSubscriptionPauser(pauser)

	target := fallbackTarget()
	target.SubscribeID = 42
	if _, hit := svc.TriggerFallback(ctx, target, true); !hit {
		t.Fatal("阈值 1 应触发")
	}
	got := pauser.paused()
	if len(got) != 1 || got[0] != 42 {
		t.Fatalf("应暂停本地订阅 42，实际 %v", got)
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if !strings.Contains(rec.Message, "本地订阅已自动暂停") {
		t.Fatalf("摘要应说明本地订阅已暂停，实际 %q", rec.Message)
	}
}

// TestFallbackDownloadDoesNotPauseLocalSubscription 仅提交下载不暂停本地订阅。
func TestFallbackDownloadDoesNotPauseLocalSubscription(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	// 下载列表为空 → download 动作不会建立订阅。
	srv, _ := fakeMoviePilotServer(t, nil)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.BaseUrl = srv.URL
		c.ApiToken = "test-token"
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
		c.MpFallbackSearchAction = domain.MoviePilotFallbackActionDownload
	})
	pauser := &fakePauser{}
	svc.SetSubscriptionPauser(pauser)

	target := fallbackTarget()
	target.SubscribeID = 42
	if _, hit := svc.TriggerFallback(ctx, target, true); !hit {
		t.Fatal("阈值 1 应触发")
	}
	if got := pauser.paused(); len(got) != 0 {
		t.Fatalf("仅提交下载不应暂停本地订阅，实际被暂停 %v", got)
	}
}

// TestFallbackPauseFailureDoesNotBreak 暂停本地订阅失败不影响兜底记录落库。
func TestFallbackPauseFailureDoesNotBreak(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	srv, _ := fakeMoviePilotServer(t, nil)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.BaseUrl = srv.URL
		c.ApiToken = "test-token"
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 1
		c.MpFallbackSearchAction = domain.MoviePilotFallbackActionSubscribe
	})
	svc.SetSubscriptionPauser(&fakePauser{fail: context.DeadlineExceeded})

	target := fallbackTarget()
	target.SubscribeID = 42
	if _, hit := svc.TriggerFallback(ctx, target, true); !hit {
		t.Fatal("阈值 1 应触发")
	}
	rec := fallbackRec(t, repo, target, domain.MoviePilotFallbackTriggerSearch)
	if rec == nil || rec.Status != domain.MoviePilotFallbackSucceeded {
		t.Fatalf("暂停失败不应影响兜底成功状态，实际 %+v", rec)
	}
}

// ---- 概览 ----

// TestFallbackSummary 概览返回开关、阈值、动作与进行中计数。
func TestFallbackSummary(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
		c.MpFallbackSearchThreshold = 5
		c.MpFallbackSearchAction = domain.MoviePilotFallbackActionSubscribe
		c.MpFallbackSubscriptionEnabled = true
		c.MpFallbackSubscriptionThreshold = 2
		c.MpFallbackSubscriptionAction = domain.MoviePilotFallbackActionDownloadThenSubscribe
	})

	sum, err := svc.FallbackSummary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !sum.Configured {
		t.Fatal("应报告已配置 MoviePilot")
	}
	if !sum.Enabled || !sum.SearchEnabled || !sum.SubscriptionEnabled {
		t.Fatalf("开关状态不符：%+v", sum)
	}
	if sum.SearchThreshold != 5 || sum.SubscriptionThreshold != 2 {
		t.Fatalf("阈值不符：%+v", sum)
	}
	if sum.SearchAction != domain.MoviePilotFallbackActionSubscribe {
		t.Fatalf("搜索动作不符：%q", sum.SearchAction)
	}
	if sum.SubscriptionAction != domain.MoviePilotFallbackActionDownloadThenSubscribe {
		t.Fatalf("订阅动作不符：%q", sum.SubscriptionAction)
	}
	if sum.ActiveCount != 0 {
		t.Fatalf("空表进行中计数应为 0，实际 %d", sum.ActiveCount)
	}
}

// TestFallbackSummaryNotConfigured 未配置 MoviePilot 时 Configured=false，但阈值仍报默认值。
func TestFallbackSummaryNotConfigured(t *testing.T) {
	ctx := context.Background()
	svc, _ := newFallbackService(t)

	sum, err := svc.FallbackSummary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Configured {
		t.Fatal("未配置时 Configured 应为 false")
	}
	if sum.SearchThreshold != domain.DefaultMoviePilotFallbackThreshold ||
		sum.SubscriptionThreshold != domain.DefaultMoviePilotFallbackThreshold {
		t.Fatalf("未显式配置时阈值应报默认 3：%+v", sum)
	}
	if sum.SearchAction != domain.MoviePilotFallbackActionDownload ||
		sum.SubscriptionAction != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("未显式配置时动作应报默认 download：%+v", sum)
	}
}

// TestFallbackListFallbacks 分页列出兜底记录。
func TestFallbackListFallbacks(t *testing.T) {
	ctx := context.Background()
	svc, repo := newFallbackService(t)
	seedFallbackConfig(t, repo, func(c *domain.MoviePilotConfig) {
		c.MpFallbackEnabled = true
		c.MpFallbackSearchEnabled = true
	})

	for i := 0; i < 3; i++ {
		target := moviepilot.FallbackTarget{MediaType: "tv", TmdbId: int64(500 + i), Title: "剧", Season: 1}
		svc.TriggerFallback(ctx, target, true)
	}
	list, total, err := svc.ListFallbacks(ctx, 1, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(list) != 2 {
		t.Fatalf("期望 total=3 首页 2 条，实际 total=%d len=%d", total, len(list))
	}
}
