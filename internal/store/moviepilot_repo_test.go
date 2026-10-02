package store_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
)

func TestMoviePilotConfigDefaultAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// 首次读取应创建默认行
	cfg, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if cfg.ID == 0 {
		t.Fatal("expected created default config with id")
	}
	if cfg.PollInterval != 5 {
		t.Fatalf("expected default poll interval 5, got %d", cfg.PollInterval)
	}
	if !cfg.NotifyEnabled {
		t.Fatal("expected notify enabled by default")
	}
	if cfg.PromotionOrder != domain.DefaultPromotionOrder {
		t.Fatalf("expected default promotion order, got %q", cfg.PromotionOrder)
	}
	if cfg.PromotionPatienceHours != 12 {
		t.Fatalf("expected default patience 12, got %d", cfg.PromotionPatienceHours)
	}

	// 再次读取应返回同一行（幂等）
	again, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if again.ID != cfg.ID {
		t.Fatalf("expected same row id %d, got %d", cfg.ID, again.ID)
	}

	// 更新并回读
	cfg.Enabled = true
	cfg.BaseUrl = "http://mp.local:3000"
	cfg.ApiToken = "tok"
	cfg.UploadAccountId = 7
	cfg.PromotionOrder = "free,normal"
	cfg.PromotionPatienceHours = 6
	cfg.SeedRetentionHours = 24
	cfg.QbittorrentURL = "http://qb:8080"
	cfg.QbittorrentUser = "admin"
	cfg.QbittorrentPass = "secret"
	if err := s.MoviePilot.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	got, err := s.MoviePilot.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("reload after save: %v", err)
	}
	if !got.Enabled || got.BaseUrl != "http://mp.local:3000" || got.ApiToken != "tok" {
		t.Fatalf("unexpected enabled/url/token: %+v", got)
	}
	if got.UploadAccountId != 7 || got.SeedRetentionHours != 24 {
		t.Fatalf("unexpected numeric fields: %+v", got)
	}
	if got.PromotionOrder != "free,normal" || got.PromotionPatienceHours != 6 {
		t.Fatalf("unexpected promotion fields: %+v", got)
	}
	if got.QbittorrentPass != "secret" {
		t.Fatalf("unexpected qb pass: %q", got.QbittorrentPass)
	}
}

func TestMoviePilotUploadTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.MoviePilot.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "hash-a",
		Title:       "测试剧",
		MediaType:   "tv",
		TmdbId:      123,
		Season:      "S01E01",
		LocalPath:   "/downloads/测试剧",
		RemotePath:  "/影视/订阅下载/测试剧",
		Status:      domain.MoviePilotUploadPending,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// 按 hash 查找
	found, err := s.MoviePilot.FindUploadTaskByHash(ctx, "hash-a")
	if err != nil {
		t.Fatalf("find by hash: %v", err)
	}
	if found == nil || found.ID != id {
		t.Fatalf("expected task %d by hash, got %+v", id, found)
	}

	// 未命中返回 nil, nil
	missing, err := s.MoviePilot.FindUploadTaskByHash(ctx, "hash-missing")
	if err != nil {
		t.Fatalf("find missing hash: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for missing hash, got %+v", missing)
	}

	// 状态流转
	found.Status = domain.MoviePilotUploadUploading
	found.TotalFiles = 3
	found.TotalBytes = 300
	found.UploadedFiles = 1
	found.UploadedBytes = 100
	if err := s.MoviePilot.UpdateUploadTask(ctx, found); err != nil {
		t.Fatalf("update task: %v", err)
	}
	reloaded, err := s.MoviePilot.GetUploadTask(ctx, id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if reloaded.Status != domain.MoviePilotUploadUploading || reloaded.TotalFiles != 3 || reloaded.UploadedBytes != 100 {
		t.Fatalf("unexpected reloaded task: %+v", reloaded)
	}

	// 分页列表 + 总数
	list, total, err := s.MoviePilot.ListUploadTasks(ctx, 1, 20, "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 task, got total=%d len=%d", total, len(list))
	}
	filtered, total, err := s.MoviePilot.ListUploadTasks(ctx, 1, 20, domain.MoviePilotUploadPending)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if total != 0 || len(filtered) != 0 {
		t.Fatalf("expected 0 pending tasks, got total=%d len=%d", total, len(filtered))
	}

	// 按多个状态查询（启动恢复路径）
	rows, err := s.MoviePilot.ListUploadTasksByStatus(ctx, domain.MoviePilotUploadPending, domain.MoviePilotUploadUploading)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("expected 1 recoverable task, got %+v", rows)
	}
}

func TestMoviePilotEmptySourceSinceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.MoviePilot.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "hash-b",
		LocalPath:   "/downloads/b",
		Status:      domain.MoviePilotUploadPending,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.MoviePilot.GetUploadTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EmptySourceSince != nil {
		t.Fatalf("expected nil empty_source_since, got %v", got.EmptySourceSince)
	}

	now := time.Now().UTC()
	got.EmptySourceSince = &now
	if err := s.MoviePilot.UpdateUploadTask(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	back, err := s.MoviePilot.GetUploadTask(ctx, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if back.EmptySourceSince == nil {
		t.Fatal("expected non-nil empty_source_since after update")
	}
	if !back.EmptySourceSince.UTC().Truncate(time.Second).Equal(now.UTC().Truncate(time.Second)) {
		t.Fatalf("empty_source_since mismatch: want %v got %v", now, *back.EmptySourceSince)
	}

	// 清空回 nil
	back.EmptySourceSince = nil
	if err := s.MoviePilot.UpdateUploadTask(ctx, back); err != nil {
		t.Fatalf("clear update: %v", err)
	}
	cleared, err := s.MoviePilot.GetUploadTask(ctx, id)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if cleared.EmptySourceSince != nil {
		t.Fatalf("expected cleared empty_source_since, got %v", cleared.EmptySourceSince)
	}
}

func TestMoviePilotPromotionLadderUpsert(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// 不存在时返回 not found 错误
	if _, err := s.MoviePilot.GetPromotionLadder(ctx, 42); err == nil {
		t.Fatal("expected error for missing ladder")
	}

	if err := s.MoviePilot.SavePromotionLadder(ctx, &domain.MoviePilotPromotionLadder{SubscribeID: 42, Tier: 0, TierStartedAt: 1000}); err != nil {
		t.Fatalf("save ladder: %v", err)
	}
	got, err := s.MoviePilot.GetPromotionLadder(ctx, 42)
	if err != nil {
		t.Fatalf("get ladder: %v", err)
	}
	if got.Tier != 0 || got.TierStartedAt != 1000 {
		t.Fatalf("unexpected ladder: %+v", got)
	}

	// 同 subscribe_id 再次保存应更新而非报错
	if err := s.MoviePilot.SavePromotionLadder(ctx, &domain.MoviePilotPromotionLadder{SubscribeID: 42, Tier: 2, TierStartedAt: 2000}); err != nil {
		t.Fatalf("upsert ladder: %v", err)
	}
	got, err = s.MoviePilot.GetPromotionLadder(ctx, 42)
	if err != nil {
		t.Fatalf("get ladder after upsert: %v", err)
	}
	if got.Tier != 2 || got.TierStartedAt != 2000 {
		t.Fatalf("expected updated ladder, got %+v", got)
	}

	all, err := s.MoviePilot.ListPromotionLadders(ctx)
	if err != nil {
		t.Fatalf("list ladders: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 ladder, got %d", len(all))
	}

	if err := s.MoviePilot.DeletePromotionLadder(ctx, 42); err != nil {
		t.Fatalf("delete ladder: %v", err)
	}
	if _, err := s.MoviePilot.GetPromotionLadder(ctx, 42); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMoviePilotFailedFileLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.MoviePilot.CreateFailedFile(ctx, &domain.MoviePilotFailedFile{
		TaskID:    1,
		FileName:  "unknown.file.mkv",
		ParentID:  "dir-1",
		RootPath:  "/影视/订阅下载/x",
		AccountID: 9,
		Status:    domain.MoviePilotFailedPending,
		Reason:    "文件名无法识别",
	})
	if err != nil {
		t.Fatalf("create failed file: %v", err)
	}

	// 同任务同文件名的待处理记录可查到（去重依赖）
	dup, err := s.MoviePilot.FindPendingFailedFile(ctx, 1, "unknown.file.mkv")
	if err != nil {
		t.Fatalf("find pending: %v", err)
	}
	if dup == nil || dup.ID != id {
		t.Fatalf("expected pending record %d, got %+v", id, dup)
	}
	// 不同任务不应命中
	other, err := s.MoviePilot.FindPendingFailedFile(ctx, 2, "unknown.file.mkv")
	if err != nil {
		t.Fatalf("find other task: %v", err)
	}
	if other != nil {
		t.Fatalf("expected nil for other task, got %+v", other)
	}

	// 标记为已整理
	dup.Status = domain.MoviePilotFailedResolved
	dup.Title = "确认标题"
	dup.TmdbId = 555
	dup.Year = 2024
	dup.Season = 2
	dup.MediaType = "tv"
	if err := s.MoviePilot.UpdateFailedFile(ctx, dup); err != nil {
		t.Fatalf("update failed file: %v", err)
	}
	got, err := s.MoviePilot.GetFailedFile(ctx, id)
	if err != nil {
		t.Fatalf("get failed file: %v", err)
	}
	if got.Status != domain.MoviePilotFailedResolved || got.TmdbId != 555 || got.Season != 2 {
		t.Fatalf("unexpected resolved record: %+v", got)
	}
	// 已 resolve 后不再算待处理
	again, err := s.MoviePilot.FindPendingFailedFile(ctx, 1, "unknown.file.mkv")
	if err != nil {
		t.Fatalf("find pending after resolve: %v", err)
	}
	if again != nil {
		t.Fatalf("expected no pending record after resolve, got %+v", again)
	}

	// 列表过滤
	_, total, err := s.MoviePilot.ListFailedFiles(ctx, 1, 20, domain.MoviePilotFailedPending)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 pending, got %d", total)
	}
	list, total, err := s.MoviePilot.ListFailedFiles(ctx, 1, 20, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("expected 1 record, got total=%d len=%d", total, len(list))
	}
}

func TestMoviePilotOrganizeHistory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.MoviePilot.AddOrganizeHistory(ctx, &domain.MoviePilotOrganizeHistory{
		AccountID:  3,
		TaskID:     11,
		FileName:   "raw.name.mkv",
		SourcePath: "影视/待整理/x/raw.name.mkv",
		TargetPath: "影视/已整理/剧集/示例 (2024) {tmdb=1}/Season 01/示例.2024.S01E01.第1集.mkv",
		MediaType:  "tv",
		Title:      "示例",
		Year:       2024,
		SeasonNum:  1,
		EpisodeNum: 1,
		TmdbId:     1,
		Status:     "ok",
		Message:    "整理成功",
	}); err != nil {
		t.Fatalf("add history: %v", err)
	}

	rows, err := s.MoviePilot.ListOrganizeHistory(ctx, 10)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(rows))
	}
	if rows[0].SeasonNum != 1 || rows[0].EpisodeNum != 1 || rows[0].TmdbId != 1 {
		t.Fatalf("unexpected history row: %+v", rows[0])
	}
	if rows[0].CreatedAt.IsZero() {
		t.Fatal("expected created_at to be set")
	}
}
