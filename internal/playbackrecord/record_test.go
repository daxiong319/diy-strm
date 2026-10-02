package playbackrecord

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"litepan/internal/store"
)

// newTestService 用临时文件库（非内存库）建表并返回服务：
// 本包走手写 SQL，需要真实迁移出的表结构，因此直接复用生产迁移脚本，
// 保证被测 schema 与线上一致（内存库无法跑 migrate 的 embed 目录语义差异无，但文件库更贴近生产）。
func newTestService(t *testing.T) (*Service, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "playback.db")})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移测试库失败：%v", err)
	}
	svc := New(db.WriteHandle(), db.ReadHandle())
	return svc, func() { _ = db.Close() }
}

func TestRecordDefaultsAndPersistence(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	entry := &Record{
		UserID:   "u1",
		ItemName: "影片A.mkv",
		StrmPath: "/云盘/电影/影片A.mkv",
		Provider: "115",
	}
	svc.Record(ctx, entry)

	// RuleID 与 PlaybackAt 应被补默认值（与老版 AddEmbyPlaybackRecord 行为一致）。
	if entry.RuleID != "1" {
		t.Fatalf("RuleID 默认值应为 1，实际 %q", entry.RuleID)
	}
	if entry.PlaybackAt.IsZero() {
		t.Fatal("PlaybackAt 应被补为当前时间")
	}

	got, err := svc.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("应落 1 条记录，实际 total=%d len=%d", got.Total, len(got.Items))
	}
	rec := got.Items[0]
	if rec.UserID != "u1" || rec.ItemName != "影片A.mkv" || rec.Provider != "115" {
		t.Fatalf("记录字段不匹配：%+v", rec)
	}
	if rec.PlaybackAt.IsZero() {
		t.Fatal("回读的 PlaybackAt 不应为零值（时间往返解析失败）")
	}
}

func TestRecordSkipsEmptyAndNil(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	// nil 与全空条目都不应落库，也不应 panic。
	svc.Record(ctx, nil)
	svc.Record(ctx, &Record{})

	got, err := svc.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if got.Total != 0 {
		t.Fatalf("空条目不应落库，实际 total=%d", got.Total)
	}
}

func TestListFiltersAndPagination(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	seed := []*Record{
		{RuleID: "1", UserID: "u1", ItemName: "甲.mkv", Provider: "115", PlaybackAt: base},
		{RuleID: "1", UserID: "u2", ItemName: "乙.mkv", Provider: "123", PlaybackAt: base.Add(time.Minute)},
		{RuleID: "2", UserID: "u1", ItemName: "丙.mkv", Provider: "115", PlaybackAt: base.Add(2 * time.Minute)},
		{RuleID: "2", UserID: "u3", ItemName: "丁.mkv", Provider: "guangya", PlaybackAt: base.Add(3 * time.Minute)},
	}
	for _, r := range seed {
		svc.Record(ctx, r)
	}

	cases := []struct {
		name     string
		q        ListQuery
		wantN    int
		wantName string // 期望首条（最新）条目名，空则不校验
	}{
		{"全部按时间倒序", ListQuery{}, 4, "丁.mkv"},
		{"按规则过滤", ListQuery{RuleID: "1"}, 2, "乙.mkv"},
		{"按用户过滤", ListQuery{UserID: "u1"}, 2, "丙.mkv"},
		{"按网盘过滤", ListQuery{Provider: "115"}, 2, "丙.mkv"},
		{"关键词匹配条目名", ListQuery{Keyword: "甲"}, 1, "甲.mkv"},
		{"组合过滤", ListQuery{RuleID: "2", Provider: "115"}, 1, "丙.mkv"},
		{"无匹配", ListQuery{Keyword: "不存在"}, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.List(ctx, tc.q)
			if err != nil {
				t.Fatalf("查询失败：%v", err)
			}
			if got.Total != int64(tc.wantN) {
				t.Fatalf("total 应为 %d，实际 %d", tc.wantN, got.Total)
			}
			if tc.wantName != "" {
				if len(got.Items) == 0 {
					t.Fatal("结果为空，无法校验首条")
				}
				if got.Items[0].ItemName != tc.wantName {
					t.Fatalf("首条应为 %q，实际 %q", tc.wantName, got.Items[0].ItemName)
				}
			}
		})
	}

	// 分页：page_size=2 时第 2 页应有 2 条且与第 1 页不重叠。
	page1, err := svc.List(ctx, ListQuery{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("分页查询失败：%v", err)
	}
	page2, err := svc.List(ctx, ListQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("分页查询失败：%v", err)
	}
	if len(page1.Items) != 2 || len(page2.Items) != 2 {
		t.Fatalf("每页应 2 条，实际 %d / %d", len(page1.Items), len(page2.Items))
	}
	if page1.Total != 4 || page2.Total != 4 {
		t.Fatalf("total 应为 4，实际 %d / %d", page1.Total, page2.Total)
	}
	if page1.Items[0].ID == page2.Items[0].ID {
		t.Fatal("第 1、2 页首条重复，OFFSET 分页失效")
	}
}

func TestListNormalizesInvalidPagination(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	cases := []struct {
		name         string
		page, size   int
		wantPage     int
		wantPageSize int
	}{
		{"page 为 0 回退 1", 0, 10, 1, 10},
		{"page 为负回退 1", -5, 10, 1, 10},
		{"size 为 0 回退默认", 1, 0, 1, DefaultPageSize},
		{"size 超上限回退默认", 1, MaxPageSize + 1, 1, DefaultPageSize},
		{"size 负数回退默认", 1, -3, 1, DefaultPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.List(ctx, ListQuery{Page: tc.page, PageSize: tc.size})
			if err != nil {
				t.Fatalf("查询失败：%v", err)
			}
			if got.Page != tc.wantPage || got.PageSize != tc.wantPageSize {
				t.Fatalf("应归一为 page=%d size=%d，实际 page=%d size=%d",
					tc.wantPage, tc.wantPageSize, got.Page, got.PageSize)
			}
		})
	}
}

func TestDeleteAndClear(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	svc.Record(ctx, &Record{RuleID: "1", UserID: "u1", ItemName: "甲.mkv"})
	svc.Record(ctx, &Record{RuleID: "1", UserID: "u2", ItemName: "乙.mkv"})
	svc.Record(ctx, &Record{RuleID: "2", UserID: "u1", ItemName: "丙.mkv"})

	all, err := svc.List(ctx, ListQuery{})
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	target := all.Items[0].ID

	if err := svc.Delete(ctx, target); err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	after, _ := svc.List(ctx, ListQuery{})
	if after.Total != 2 {
		t.Fatalf("删除后应剩 2 条，实际 %d", after.Total)
	}

	// 幂等：再删同一条不报错。
	if err := svc.Delete(ctx, target); err != nil {
		t.Fatalf("重复删除应幂等，实际报错：%v", err)
	}

	// 非法 ID 应被拒绝。
	if err := svc.Delete(ctx, 0); err == nil {
		t.Fatal("ID 为 0 应返回错误")
	}

	// 按规则清空：只删 rule 1 的记录（共 2 条）。
	n, err := svc.Clear(ctx, "1", "")
	if err != nil {
		t.Fatalf("清空失败：%v", err)
	}
	if n != 2 {
		t.Fatalf("应清空 2 条（rule 1 的全部记录），实际 %d", n)
	}
	// 此前删除的 target 属于 rule 2，故 rule 2 只剩删除后余下的那些记录。
	rest, _ := svc.List(ctx, ListQuery{})
	if rest.Total != 0 {
		t.Fatalf("rule 1 清空后不应再有记录，实际 total=%d", rest.Total)
	}

	// 全量清空：此时库中已无记录，应返回 0 且不报错（幂等）。
	n, err = svc.Clear(ctx, "", "")
	if err != nil {
		t.Fatalf("全量清空失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("此时库中已无记录，应清空 0 条，实际 %d", n)
	}
	final, _ := svc.List(ctx, ListQuery{})
	if final.Total != 0 {
		t.Fatalf("清空后应为 0，实际 %d", final.Total)
	}
}

func TestStats(t *testing.T) {
	svc, cleanup := newTestService(t)
	defer cleanup()
	ctx := context.Background()

	empty, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("空库统计失败：%v", err)
	}
	if empty.Total != 0 || empty.LastAt != "" {
		t.Fatalf("空库应 total=0 且 last_at 为空，实际 %+v", empty)
	}

	base := time.Date(2026, 3, 5, 8, 0, 0, 0, time.UTC)
	svc.Record(ctx, &Record{UserID: "u1", ItemName: "甲.mkv", PlaybackAt: base})
	svc.Record(ctx, &Record{UserID: "u1", ItemName: "甲.mkv", PlaybackAt: base.Add(time.Hour)})
	svc.Record(ctx, &Record{UserID: "u2", ItemName: "乙.mkv", PlaybackAt: base.Add(2 * time.Hour)})

	got, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("统计失败：%v", err)
	}
	if got.Total != 3 {
		t.Fatalf("total 应为 3，实际 %d", got.Total)
	}
	if got.UserCount != 2 {
		t.Fatalf("去重用户数应为 2，实际 %d", got.UserCount)
	}
	if got.ItemCount != 2 {
		t.Fatalf("去重条目数应为 2，实际 %d", got.ItemCount)
	}
	if got.LastAt == "" {
		t.Fatal("last_at 不应为空")
	}
	if parsed := parseTime(got.LastAt); !parsed.Equal(base.Add(2 * time.Hour)) {
		t.Fatalf("last_at 应取最新一条，实际 %s", got.LastAt)
	}
}

func TestNilServiceDegradesSafely(t *testing.T) {
	ctx := context.Background()
	svc := New(nil, nil)

	// 写路径静默丢弃，不 panic。
	svc.Record(ctx, &Record{UserID: "u1", ItemName: "甲.mkv"})

	if _, err := svc.List(ctx, ListQuery{}); err != ErrUnavailable {
		t.Fatalf("查询应返回 ErrUnavailable，实际 %v", err)
	}
	if _, err := svc.Stats(ctx); err != ErrUnavailable {
		t.Fatalf("统计应返回 ErrUnavailable，实际 %v", err)
	}
	if _, err := svc.Clear(ctx, "", ""); err != ErrUnavailable {
		t.Fatalf("清空应返回 ErrUnavailable，实际 %v", err)
	}
	if err := svc.Delete(ctx, 1); err != ErrUnavailable {
		t.Fatalf("删除应返回 ErrUnavailable，实际 %v", err)
	}
}

func TestErrorHookFiredOnWriteFailure(t *testing.T) {
	ctx := context.Background()
	// 关闭底层库制造写失败。
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "closed.db")})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	svc := New(db.WriteHandle(), db.ReadHandle())
	if err := db.Close(); err != nil {
		t.Fatalf("关闭测试库失败：%v", err)
	}

	fired := make(chan error, 1)
	SetErrorHook(func(err error) { fired <- err })
	defer SetErrorHook(nil)

	svc.Record(ctx, &Record{UserID: "u1", ItemName: "甲.mkv"})

	select {
	case err := <-fired:
		if err == nil {
			t.Fatal("回调应携带具体错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("写失败时未触发错误回调")
	}
}

func TestParseTimeLayouts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool // 是否应解析出非零时间
	}{
		{"空串", "", false},
		{"RFC3339Nano", "2026-01-02T03:04:05.123456789Z", true},
		{"RFC3339", "2026-01-02T03:04:05Z", true},
		{"SQLite 默认格式", "2026-01-02 03:04:05", true},
		{"无法解析", "not-a-time", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTime(tc.in)
			if got.IsZero() == tc.want {
				t.Fatalf("parseTime(%q) 零值性不符：want parsed=%v, got %v", tc.in, tc.want, got)
			}
		})
	}
}
