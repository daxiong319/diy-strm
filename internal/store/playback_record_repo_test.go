package store_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
)

// TestPlaybackRecordInsertAndList 校验落库、默认规则 ID、时间补全与倒序分页。
func TestPlaybackRecordInsertAndList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rec := &domain.PlaybackRecord{
		UserID:     "user-1",
		Client:     "Emby Theater",
		DeviceID:   "dev-1",
		ItemName:   "流浪地球2.mkv",
		StrmPath:   "/media/电影/流浪地球2.mkv",
		Provider:   "115",
		PlaybackAt: "2024-05-01T10:00:00Z",
	}
	id, err := s.PlaybackRecords.Insert(ctx, rec)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}
	if rec.ID != id {
		t.Fatalf("expected record id backfilled to %d, got %d", id, rec.ID)
	}
	if rec.RuleID != "1" {
		t.Fatalf("expected default rule id 1, got %q", rec.RuleID)
	}

	// 更晚的一条应排在前面
	if _, err := s.PlaybackRecords.Insert(ctx, &domain.PlaybackRecord{
		UserID:     "user-2",
		ItemName:   "沙丘2.mkv",
		StrmPath:   "/media/电影/沙丘2.mkv",
		Provider:   "123",
		PlaybackAt: "2024-05-02T10:00:00Z",
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	items, total, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 records, got total=%d len=%d", total, len(items))
	}
	if items[0].ItemName != "沙丘2.mkv" {
		t.Fatalf("expected latest first, got %q", items[0].ItemName)
	}
	if items[0].PlaybackAt != "2024-05-02T10:00:00Z" {
		t.Fatalf("unexpected playback_at %q", items[0].PlaybackAt)
	}
}

// TestPlaybackRecordInsertSkipsEmptyEntry 空条目不应产生噪音记录。
func TestPlaybackRecordInsertSkipsEmptyEntry(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.PlaybackRecords.Insert(ctx, &domain.PlaybackRecord{Client: "Emby"})
	if err != nil {
		t.Fatalf("insert empty: %v", err)
	}
	if id != 0 {
		t.Fatalf("expected empty entry skipped, got id %d", id)
	}
	_, total, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected no records, got %d", total)
	}

	// nil 入参同样安全
	if _, err := s.PlaybackRecords.Insert(ctx, nil); err != nil {
		t.Fatalf("insert nil: %v", err)
	}
}

// TestPlaybackRecordListFilters 校验规则/用户/网盘/关键词过滤与关键词双字段匹配。
func TestPlaybackRecordListFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seed := []domain.PlaybackRecord{
		{RuleID: "1", UserID: "u1", ItemName: "三体 第一季.mkv", StrmPath: "/115/剧集/三体 第一季.mkv", Provider: "115", PlaybackAt: "2024-05-03T00:00:00Z"},
		{RuleID: "1", UserID: "u2", ItemName: "三体 第二季.mkv", StrmPath: "/123/剧集/三体 第二季.mkv", Provider: "123", PlaybackAt: "2024-05-04T00:00:00Z"},
		{RuleID: "2", UserID: "u1", ItemName: "奥本海默.mkv", StrmPath: "/123/电影/奥本海默.mkv", Provider: "123", PlaybackAt: "2024-05-05T00:00:00Z"},
	}
	for i := range seed {
		if _, err := s.PlaybackRecords.Insert(ctx, &seed[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// 按规则
	items, total, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{RuleID: "1"})
	if err != nil {
		t.Fatalf("list by rule: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("rule filter: expected 2, got total=%d len=%d", total, len(items))
	}

	// 按用户
	_, total, err = s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{UserID: "u1"})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if total != 2 {
		t.Fatalf("user filter: expected 2, got %d", total)
	}

	// 按网盘 + 规则
	_, total, err = s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{RuleID: "1", Provider: "123"})
	if err != nil {
		t.Fatalf("list by provider: %v", err)
	}
	if total != 1 {
		t.Fatalf("provider filter: expected 1, got %d", total)
	}

	// 关键词命中条目名
	_, total, err = s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Keyword: "三体"})
	if err != nil {
		t.Fatalf("list by keyword: %v", err)
	}
	if total != 2 {
		t.Fatalf("keyword filter: expected 2, got %d", total)
	}

	// 关键词也应命中 strm_path（仅命中路径、不命中条目名）
	_, total, err = s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Keyword: "剧集"})
	if err != nil {
		t.Fatalf("list by path keyword: %v", err)
	}
	if total != 2 {
		t.Fatalf("path keyword filter: expected 2, got %d", total)
	}
}

// TestPlaybackRecordListPaging 分页边界：非法页码回退、页大小上限裁剪。
func TestPlaybackRecordListPaging(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := 0; i < 5; i++ {
		if _, err := s.PlaybackRecords.Insert(ctx, &domain.PlaybackRecord{
			UserID:     "u1",
			ItemName:   "ep",
			StrmPath:   "/p/ep",
			PlaybackAt: "2024-05-0" + string(rune('1'+i)) + "T00:00:00Z",
		}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	page1, total, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 5 || len(page1) != 2 {
		t.Fatalf("page1: expected total=5 len=2, got total=%d len=%d", total, len(page1))
	}
	page3, _, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("page3: expected 1 item, got %d", len(page3))
	}
	// 页码非法时回退到第 1 页而不是报错
	if items, _, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Page: -1, PageSize: 2}); err != nil {
		t.Fatalf("invalid page: %v", err)
	} else if len(items) != 2 {
		t.Fatalf("invalid page: expected fallback to page 1, got %d", len(items))
	}
	// 页大小超过上限（200）时回落默认 30，不应整体报错
	if _, _, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{Page: 1, PageSize: 9999}); err != nil {
		t.Fatalf("oversize page size: %v", err)
	}
}

// TestPlaybackRecordStats 概览统计：总数、最近时间、去重用户与条目数。
func TestPlaybackRecordStats(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	stats, err := s.PlaybackRecords.Stats(ctx)
	if err != nil {
		t.Fatalf("stats empty: %v", err)
	}
	if stats.Total != 0 || stats.LastAt != "" || stats.UserCount != 0 || stats.ItemCount != 0 {
		t.Fatalf("expected zero stats, got %+v", stats)
	}

	seed := []domain.PlaybackRecord{
		{UserID: "u1", ItemName: "a.mkv", StrmPath: "/a.mkv", PlaybackAt: "2024-05-01T00:00:00Z"},
		{UserID: "u1", ItemName: "a.mkv", StrmPath: "/a.mkv", PlaybackAt: "2024-05-03T00:00:00Z"},
		{UserID: "u2", ItemName: "b.mkv", StrmPath: "/b.mkv", PlaybackAt: "2024-05-02T00:00:00Z"},
	}
	for i := range seed {
		if _, err := s.PlaybackRecords.Insert(ctx, &seed[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	stats, err = s.PlaybackRecords.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Total != 3 {
		t.Fatalf("expected total 3, got %d", stats.Total)
	}
	if stats.LastAt != "2024-05-03T00:00:00Z" {
		t.Fatalf("expected latest 2024-05-03T00:00:00Z, got %q", stats.LastAt)
	}
	if stats.UserCount != 2 || stats.ItemCount != 2 {
		t.Fatalf("expected 2 users / 2 items, got %d / %d", stats.UserCount, stats.ItemCount)
	}
}

// TestPlaybackRecordDeleteAndClear 删除与按条件清空。
func TestPlaybackRecordDeleteAndClear(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	r1 := &domain.PlaybackRecord{RuleID: "1", UserID: "u1", ItemName: "a.mkv", StrmPath: "/a.mkv", PlaybackAt: "2024-05-01T00:00:00Z"}
	r2 := &domain.PlaybackRecord{RuleID: "1", UserID: "u2", ItemName: "b.mkv", StrmPath: "/b.mkv", PlaybackAt: "2024-05-02T00:00:00Z"}
	r3 := &domain.PlaybackRecord{RuleID: "2", UserID: "u1", ItemName: "c.mkv", StrmPath: "/c.mkv", PlaybackAt: "2024-05-03T00:00:00Z"}
	for _, rec := range []*domain.PlaybackRecord{r1, r2, r3} {
		if _, err := s.PlaybackRecords.Insert(ctx, rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// 非法 ID 应报校验错误
	if err := s.PlaybackRecords.Delete(ctx, 0); err == nil {
		t.Fatal("expected error for invalid id")
	}
	if err := s.PlaybackRecords.Delete(ctx, r1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 幂等：再删一次不报错
	if err := s.PlaybackRecords.Delete(ctx, r1.ID); err != nil {
		t.Fatalf("delete again: %v", err)
	}
	_, total, err := s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 left, got %d", total)
	}

	// 按规则清空只删 rule 2
	n, err := s.PlaybackRecords.Clear(ctx, "2", "")
	if err != nil {
		t.Fatalf("clear by rule: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	// 按用户清空
	n, err = s.PlaybackRecords.Clear(ctx, "", "u2")
	if err != nil {
		t.Fatalf("clear by user: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	// 无条件清空
	if _, err := s.PlaybackRecords.Insert(ctx, &domain.PlaybackRecord{UserID: "u9", ItemName: "z.mkv", PlaybackAt: "2024-05-09T00:00:00Z"}); err != nil {
		t.Fatalf("seed again: %v", err)
	}
	n, err = s.PlaybackRecords.Clear(ctx, "", "")
	if err != nil {
		t.Fatalf("clear all: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 cleared, got %d", n)
	}
	_, total, err = s.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected empty table, got %d", total)
	}
}
