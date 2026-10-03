package store_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
)

// TestFallbackConfigRoundTrip 兜底配置随 MoviePilotConfig 持久化并回读。
// 默认值：阈值 3、动作 download、开关关闭。
func TestFallbackConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	cfg, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if cfg.MpFallbackEnabled || cfg.MpFallbackSearchEnabled || cfg.MpFallbackSubscriptionEnabled {
		t.Fatalf("expected fallback switches off by default, got %+v", cfg)
	}
	if cfg.MpFallbackSearchThreshold != domain.DefaultMoviePilotFallbackThreshold {
		t.Fatalf("expected search threshold %d, got %d", domain.DefaultMoviePilotFallbackThreshold, cfg.MpFallbackSearchThreshold)
	}
	if cfg.MpFallbackSubscriptionThreshold != domain.DefaultMoviePilotFallbackThreshold {
		t.Fatalf("expected subscription threshold %d, got %d", domain.DefaultMoviePilotFallbackThreshold, cfg.MpFallbackSubscriptionThreshold)
	}
	if cfg.MpFallbackSearchAction != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("expected default download action, got %q", cfg.MpFallbackSearchAction)
	}
	if cfg.MpFallbackSubscriptionAction != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("expected default download action, got %q", cfg.MpFallbackSubscriptionAction)
	}

	cfg.MpFallbackEnabled = true
	cfg.MpFallbackSearchEnabled = true
	cfg.MpFallbackSearchThreshold = 5
	cfg.MpFallbackSearchAction = domain.MoviePilotFallbackActionSubscribe
	cfg.MpFallbackSubscriptionEnabled = true
	cfg.MpFallbackSubscriptionThreshold = 2
	cfg.MpFallbackSubscriptionAction = domain.MoviePilotFallbackActionDownloadThenSubscribe
	if err := s.MoviePilot.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !got.MpFallbackEnabled || !got.MpFallbackSearchEnabled || !got.MpFallbackSubscriptionEnabled {
		t.Fatalf("expected all fallback switches on, got %+v", got)
	}
	if got.MpFallbackSearchThreshold != 5 || got.MpFallbackSubscriptionThreshold != 2 {
		t.Fatalf("thresholds not persisted: search=%d subscription=%d", got.MpFallbackSearchThreshold, got.MpFallbackSubscriptionThreshold)
	}
	if got.MpFallbackSearchAction != domain.MoviePilotFallbackActionSubscribe {
		t.Fatalf("search action not persisted: %q", got.MpFallbackSearchAction)
	}
	if got.MpFallbackSubscriptionAction != domain.MoviePilotFallbackActionDownloadThenSubscribe {
		t.Fatalf("subscription action not persisted: %q", got.MpFallbackSubscriptionAction)
	}
}

// TestFallbackConfigNormalizesInvalid 非法阈值与动作在写入时被归一。
func TestFallbackConfigNormalizesInvalid(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	cfg, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.MpFallbackSearchThreshold = 0
	cfg.MpFallbackSubscriptionThreshold = -7
	cfg.MpFallbackSearchAction = "bogus"
	cfg.MpFallbackSubscriptionAction = ""
	if err := s.MoviePilot.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.MpFallbackSearchThreshold != domain.DefaultMoviePilotFallbackThreshold {
		t.Fatalf("expected normalized search threshold, got %d", got.MpFallbackSearchThreshold)
	}
	if got.MpFallbackSubscriptionThreshold != domain.DefaultMoviePilotFallbackThreshold {
		t.Fatalf("expected normalized subscription threshold, got %d", got.MpFallbackSubscriptionThreshold)
	}
	if got.MpFallbackSearchAction != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("expected normalized search action, got %q", got.MpFallbackSearchAction)
	}
	if got.MpFallbackSubscriptionAction != domain.MoviePilotFallbackActionDownload {
		t.Fatalf("expected normalized subscription action, got %q", got.MpFallbackSubscriptionAction)
	}
}

// TestFallbackSaveAndGet 兜底记录按 (media_key, trigger) 读写，搜索与订阅计数互不干扰。
func TestFallbackSaveAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	searchKey := domain.MoviePilotFallbackMediaKey("tv", 12345, 2)
	rec := &domain.MoviePilotFallback{
		MediaKey:    searchKey,
		Trigger:     domain.MoviePilotFallbackTriggerSearch,
		MediaType:   "tv",
		TmdbId:      12345,
		Title:       "测试剧集",
		Season:      2,
		Status:      domain.MoviePilotFallbackPending,
		Action:      domain.MoviePilotFallbackActionDownload,
		SearchCount: 2,
	}
	if err := s.MoviePilot.SaveFallback(ctx, rec); err != nil {
		t.Fatalf("save fallback: %v", err)
	}
	if rec.ID == 0 {
		t.Fatal("expected assigned id after insert")
	}

	got, err := s.MoviePilot.GetFallback(ctx, searchKey, domain.MoviePilotFallbackTriggerSearch)
	if err != nil {
		t.Fatalf("get fallback: %v", err)
	}
	if got.SearchCount != 2 || got.SubscriptionCount != 0 {
		t.Fatalf("unexpected counts: search=%d subscription=%d", got.SearchCount, got.SubscriptionCount)
	}
	if got.Title != "测试剧集" || got.TmdbId != 12345 || got.Season != 2 {
		t.Fatalf("unexpected identity fields: %+v", got)
	}

	// 同一 key 的订阅计数是独立一行 —— 查不到（未创建）。
	if _, err := s.MoviePilot.GetFallback(ctx, searchKey, domain.MoviePilotFallbackTriggerSubscription); err == nil {
		t.Fatal("expected not-found for subscription trigger before it is created")
	} else if !isNotFoundErr(err) {
		t.Fatalf("expected not-found error, got %v", err)
	}

	// 更新走 UPDATE 分支：同一 ID 回写。
	got.SearchCount = 3
	got.Status = domain.MoviePilotFallbackSucceeded
	if err := s.MoviePilot.SaveFallback(ctx, got); err != nil {
		t.Fatalf("update fallback: %v", err)
	}
	again, err := s.MoviePilot.GetFallback(ctx, searchKey, domain.MoviePilotFallbackTriggerSearch)
	if err != nil {
		t.Fatalf("reload fallback: %v", err)
	}
	if again.ID != got.ID {
		t.Fatalf("expected same id %d, got %d", got.ID, again.ID)
	}
	if again.SearchCount != 3 || again.Status != domain.MoviePilotFallbackSucceeded {
		t.Fatalf("update not persisted: %+v", again)
	}
}

// TestFallbackSaveUpsertOnConflict 未带 ID 的同一 (media_key, trigger) 写入走 upsert，不新建重复行。
func TestFallbackSaveUpsertOnConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	key := domain.MoviePilotFallbackMediaKey("movie", 9, 0)
	first := &domain.MoviePilotFallback{
		MediaKey: key, Trigger: domain.MoviePilotFallbackTriggerSearch, MediaType: "movie",
		TmdbId: 9, Title: "电影", Status: domain.MoviePilotFallbackPending, SearchCount: 1,
	}
	if err := s.MoviePilot.SaveFallback(ctx, first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	second := &domain.MoviePilotFallback{
		MediaKey: key, Trigger: domain.MoviePilotFallbackTriggerSearch, MediaType: "movie",
		TmdbId: 9, Title: "电影改名", Status: domain.MoviePilotFallbackPending, SearchCount: 2,
	}
	if err := s.MoviePilot.SaveFallback(ctx, second); err != nil {
		t.Fatalf("save second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected upsert to reuse id %d, got %d", first.ID, second.ID)
	}

	list, total, err := s.MoviePilot.ListFallbacks(ctx, 1, 20, "")
	if err != nil {
		t.Fatalf("list fallbacks: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected exactly 1 row after upsert, got total=%d len=%d", total, len(list))
	}
	if list[0].SearchCount != 2 || list[0].Title != "电影改名" {
		t.Fatalf("upsert not applied: %+v", list[0])
	}
}

// TestFallbackListFiltersAndPaginates 列表支持 status 过滤与分页。
func TestFallbackListFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		rec := &domain.MoviePilotFallback{
			MediaKey:  domain.MoviePilotFallbackMediaKey("tv", int64(100+i), 1),
			Trigger:   domain.MoviePilotFallbackTriggerSearch,
			MediaType: "tv", TmdbId: int64(100 + i), Season: 1,
			Status: domain.MoviePilotFallbackPending,
		}
		if err := s.MoviePilot.SaveFallback(ctx, rec); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	done := &domain.MoviePilotFallback{
		MediaKey:  domain.MoviePilotFallbackMediaKey("tv", 999, 1),
		Trigger:   domain.MoviePilotFallbackTriggerSearch,
		MediaType: "tv", TmdbId: 999, Season: 1,
		Status: domain.MoviePilotFallbackSucceeded,
	}
	if err := s.MoviePilot.SaveFallback(ctx, done); err != nil {
		t.Fatalf("save succeeded: %v", err)
	}

	all, total, err := s.MoviePilot.ListFallbacks(ctx, 1, 2, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 4 || len(all) != 2 {
		t.Fatalf("expected total=4 page len=2, got total=%d len=%d", total, len(all))
	}

	page2, _, err := s.MoviePilot.ListFallbacks(ctx, 2, 2, "")
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2 rows on page 2, got %d", len(page2))
	}
	if page2[0].ID == all[0].ID {
		t.Fatal("expected page 2 to contain different rows than page 1")
	}

	pending, pendingTotal, err := s.MoviePilot.ListFallbacks(ctx, 1, 20, domain.MoviePilotFallbackPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if pendingTotal != 3 || len(pending) != 3 {
		t.Fatalf("expected 3 pending, got total=%d len=%d", pendingTotal, len(pending))
	}
	for _, rec := range pending {
		if rec.Status != domain.MoviePilotFallbackPending {
			t.Fatalf("status filter leaked %q", rec.Status)
		}
	}
}

// TestFallbackCountActive 进行中计数只统计 pending/running。
func TestFallbackCountActive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	n, err := s.MoviePilot.CountActiveFallbacks(ctx)
	if err != nil {
		t.Fatalf("count empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 active on empty table, got %d", n)
	}

	statuses := []string{
		domain.MoviePilotFallbackPending,
		domain.MoviePilotFallbackRunning,
		domain.MoviePilotFallbackSucceeded,
		domain.MoviePilotFallbackFailed,
	}
	for i, st := range statuses {
		rec := &domain.MoviePilotFallback{
			MediaKey:  domain.MoviePilotFallbackMediaKey("tv", int64(200+i), 1),
			Trigger:   domain.MoviePilotFallbackTriggerSearch,
			MediaType: "tv", TmdbId: int64(200 + i), Season: 1, Status: st,
		}
		if err := s.MoviePilot.SaveFallback(ctx, rec); err != nil {
			t.Fatalf("save %s: %v", st, err)
		}
	}
	n, err = s.MoviePilot.CountActiveFallbacks(ctx)
	if err != nil {
		t.Fatalf("count active: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 active (pending+running), got %d", n)
	}
}

// TestHasPendingTransferTasksMoviePilot 待入库判定覆盖 MP 上传任务的未完成状态。
func TestHasPendingTransferTasksMoviePilot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	pending, err := s.MoviePilot.HasPendingTransferTasks(ctx)
	if err != nil {
		t.Fatalf("query empty: %v", err)
	}
	if pending {
		t.Fatal("expected no pending transfers on empty table")
	}

	// pending 属于待入库。
	if _, err := s.MoviePilot.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "hash-pending", Title: "剧集", Status: domain.MoviePilotUploadPending,
	}); err != nil {
		t.Fatalf("create pending task: %v", err)
	}
	pending, err = s.MoviePilot.HasPendingTransferTasks(ctx)
	if err != nil {
		t.Fatalf("query with pending: %v", err)
	}
	if !pending {
		t.Fatal("expected pending MP upload task to count as pending transfer")
	}
}

// TestHasPendingTransferTasksIgnoresFinished 已完成/失败/取消的 MP 任务不算待入库。
func TestHasPendingTransferTasksIgnoresFinished(t *testing.T) {
	ctx := context.Background()

	for _, st := range []string{
		domain.MoviePilotUploadUploaded,
		domain.MoviePilotUploadFailed,
		domain.MoviePilotUploadCanceled,
	} {
		t.Run(st, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.MoviePilot.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
				TorrentHash: "hash-" + st, Title: "剧集", Status: st,
			}); err != nil {
				t.Fatalf("create %s task: %v", st, err)
			}
			pending, err := s.MoviePilot.HasPendingTransferTasks(ctx)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if pending {
				t.Fatalf("expected %s task not to count as pending transfer", st)
			}
		})
	}
}

// TestHasPendingTransferTasksUploading uploading 同样属于待入库。
func TestHasPendingTransferTasksUploading(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.MoviePilot.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "hash-uploading", Title: "剧集", Status: domain.MoviePilotUploadUploading,
	}); err != nil {
		t.Fatalf("create uploading task: %v", err)
	}
	pending, err := s.MoviePilot.HasPendingTransferTasks(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !pending {
		t.Fatal("expected uploading MP upload task to count as pending transfer")
	}
}

// TestHasPendingTransferTasksLocalUpload 本地上传任务的未完成状态同样算待入库。
func TestHasPendingTransferTasksLocalUpload(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if pending, err := s.MoviePilot.HasPendingTransferTasks(ctx); err != nil {
		t.Fatalf("query empty: %v", err)
	} else if pending {
		t.Fatal("expected no pending transfers before inserting local upload task")
	}

	if _, err := s.DB.WriteHandle().ExecContext(ctx,
		`INSERT INTO upload_tasks(task_id, account_id, file_name, status) VALUES (?,?,?,?)`,
		"local-1", 1, "demo.bin", "running"); err != nil {
		t.Fatalf("insert local upload task: %v", err)
	}
	pending, err := s.MoviePilot.HasPendingTransferTasks(ctx)
	if err != nil {
		t.Fatalf("query with local task: %v", err)
	}
	if !pending {
		t.Fatal("expected running local upload task to count as pending transfer")
	}
}

// isNotFoundErr 判定仓储返回的「记录不存在」错误。
func isNotFoundErr(err error) bool {
	ae, ok := domain.AsAppError(err)
	return ok && ae.Code == domain.CodeNotFound
}
