package store_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

func newRSSStore(t *testing.T) (*store.DB, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, store.New(db)
}

func rssPayload(name, url string) domain.RSSUpsertPayload {
	return domain.RSSUpsertPayload{
		Name: name, RssURL: url, TargetPath: "/电影/新番", MediaType: "tv",
		Action: domain.RSSActionOffline, Enabled: boolPtr(true),
	}
}

func boolPtr(b bool) *bool { return &b }

// 源的建/查/改/删全链路。
func TestRSSSourceRepositoryCRUD(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	src, err := s.RSSSources.Upsert(ctx, rssPayload("Lilith", "https://mikanani.me/RSS/Bangumi?bangumiId=1"))
	if err != nil {
		t.Fatalf("新建源失败: %v", err)
	}
	if src.ID <= 0 || src.Name != "Lilith" || src.TargetPath != "/电影/新番" {
		t.Fatalf("新建后字段不对: %+v", src)
	}
	if !src.Enabled {
		t.Fatalf("新建时 enabled 丢失: %+v", src)
	}
	if src.MediaType != "tv" || src.Action != domain.RSSActionOffline {
		t.Fatalf("media_type/action 未落库: %+v", src)
	}
	if src.CreatedAt.IsZero() {
		t.Fatalf("created_at 未回填")
	}

	got, found, err := s.RSSSources.Get(ctx, src.ID)
	if err != nil || !found {
		t.Fatalf("Get 失败: found=%v err=%v", found, err)
	}
	if got.RssURL != src.RssURL {
		t.Fatalf("rss_url 不一致: %q", got.RssURL)
	}
	_, found, _ = s.RSSSources.Get(ctx, 9999)
	if found {
		t.Fatalf("不存在的 ID 应返回 found=false")
	}

	byURL, found, err := s.RSSSources.GetByURL(ctx, src.RssURL)
	if err != nil || !found || byURL.ID != src.ID {
		t.Fatalf("GetByURL 失败: %+v found=%v err=%v", byURL, found, err)
	}
	if _, found, _ = s.RSSSources.GetByURL(ctx, "  "); found {
		t.Fatalf("空 URL 不该命中任何源")
	}

	off := false
	upd, err := s.RSSSources.Upsert(ctx, domain.RSSUpsertPayload{
		ID: src.ID, Name: "改过", RssURL: src.RssURL, MediaType: "movie",
		Action: domain.RSSActionTransfer, Enabled: &off,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if upd.Name != "改过" || upd.MediaType != "movie" || upd.Enabled {
		t.Fatalf("更新未生效: %+v", upd)
	}

	if err := s.RSSSources.Delete(ctx, src.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, found, _ := s.RSSSources.Get(ctx, src.ID); found {
		t.Fatalf("删除后仍能查到源")
	}
	// 源删掉后历史仍在 —— 孤儿历史是允许的（history.source_id 不是外键、
	// 没有 ondelete，与 参考实现 一致）。全表应为空，只是本用例没写过历史。
	if list, err := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{}); err != nil || len(list) != 0 {
		t.Fatalf("本用例没写过历史，却查到了 %d 条: err=%v", len(list), err)
	}
}

// 同一 URL 允许重复添加（参考实现 源表对 rss_url 没有唯一约束），GetByURL 只能取其一。
func TestRSSSourceDuplicateURLIsAllowedButGetByURLPicksFirst(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	first, err := s.RSSSources.Upsert(ctx, rssPayload("先加的", "https://same.example/rss"))
	if err != nil {
		t.Fatalf("首次添加失败: %v", err)
	}
	second, err := s.RSSSources.Upsert(ctx, rssPayload("后加的", "https://same.example/rss"))
	if err != nil {
		t.Fatalf("同 URL 重复添加不该被拒（源表无唯一约束）: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("重复添加却复用了同一个 ID")
	}
	got, found, err := s.RSSSources.GetByURL(ctx, "https://same.example/rss")
	if err != nil || !found {
		t.Fatalf("GetByURL 失败: found=%v err=%v", found, err)
	}
	if got.ID != first.ID {
		t.Fatalf("GetByURL 应取最早添加的那条: 期望 id=%d 实际 id=%d", first.ID, got.ID)
	}
}

// 列表过滤：启用状态、关键字、条数上限。
func TestRSSSourceRepositoryListFilters(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	on, _ := s.RSSSources.Upsert(ctx, rssPayload("A番", "https://a.example/rss"))
	off, _ := s.RSSSources.Upsert(ctx, domain.RSSUpsertPayload{
		Name: "B番", RssURL: "https://b.example/rss", Enabled: boolPtr(false),
	})
	_, _ = s.RSSSources.Upsert(ctx, rssPayload("C番", "https://c.example/rss"))

	enabledOnly, err := s.RSSSources.List(ctx, domain.RSSSourceQuery{Enabled: boolPtr(true)})
	if err != nil {
		t.Fatalf("按启用过滤失败: %v", err)
	}
	if len(enabledOnly) != 2 {
		t.Fatalf("启用过滤应得 2 条，实际 %d", len(enabledOnly))
	}
	all, _ := s.RSSSources.List(ctx, domain.RSSSourceQuery{})
	if len(all) != 3 {
		t.Fatalf("无条件应得 3 条，实际 %d", len(all))
	}
	hit, _ := s.RSSSources.List(ctx, domain.RSSSourceQuery{Keyword: "A番"})
	if len(hit) != 1 || hit[0].ID != on.ID {
		t.Fatalf("关键字匹配名称失败: %+v", hit)
	}
	byURL, _ := s.RSSSources.List(ctx, domain.RSSSourceQuery{Keyword: "b.example"})
	if len(byURL) != 1 || byURL[0].ID != off.ID {
		t.Fatalf("关键字匹配 URL 失败: %+v", byURL)
	}
	// 关键字 + 启用过滤同时生效
	none, _ := s.RSSSources.List(ctx, domain.RSSSourceQuery{Enabled: boolPtr(true), Keyword: "B番"})
	if len(none) != 0 {
		t.Fatalf("停用源不该出现在启用过滤结果里: %+v", none)
	}
	limited, _ := s.RSSSources.List(ctx, domain.RSSSourceQuery{Limit: 2})
	if len(limited) != 2 {
		t.Fatalf("Limit 应得 2 条，实际 %d", len(limited))
	}

	en, err := s.RSSSources.ListEnabled(ctx)
	if err != nil || len(en) != 2 {
		t.Fatalf("ListEnabled 应得 2 条: %d err=%v", len(en), err)
	}
}

// 同步结果回写：状态、消息、时间。
func TestRSSSourceMarkSyncResult(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	src, _ := s.RSSSources.Upsert(ctx, rssPayload("A", "https://a.example/rss"))
	at := time.Date(2026, 10, 9, 8, 30, 0, 0, time.UTC)
	if err := s.RSSSources.MarkSyncResult(ctx, src.ID, domain.RSSStatusPartial, "新增 2 失败 1", at); err != nil {
		t.Fatalf("回写失败: %v", err)
	}
	got, _, _ := s.RSSSources.Get(ctx, src.ID)
	if got.LastStatus != domain.RSSStatusPartial || got.LastMessage != "新增 2 失败 1" {
		t.Fatalf("状态/消息未落库: %+v", got)
	}
	if got.LastSyncAt == nil || !got.LastSyncAt.Equal(at) {
		t.Fatalf("同步时间不对: %v want %v", got.LastSyncAt, at)
	}
}

// 去重的核心一环：guid 命中唯一约束不报错，回查既有行。
func TestRSSHistoryInsertOnceIsIdempotentOnGuid(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	h := domain.RSSHistory{
		SourceID: 7, SourceName: "A番", Guid: "magnet:btih:abc",
		Title: "标题", Link: "https://x/1", DownloadURL: "magnet:?xt=urn:btih:abc",
		Status: domain.RSSStatusSuccess,
	}
	first, inserted, err := s.RSSHistory.InsertOnce(ctx, h)
	if err != nil {
		t.Fatalf("首次插入失败: %v", err)
	}
	if !inserted || first.ID <= 0 {
		t.Fatalf("首次插入应 inserted=true 且有 ID: %+v inserted=%v", first, inserted)
	}

	// 换源名、换状态、换标题再插一次：guid 相同就必须被拒
	again := h
	again.SourceID = 8
	again.SourceName = "B番"
	again.Title = "另一个标题"
	again.Status = domain.RSSStatusFailed
	second, inserted, err := s.RSSHistory.InsertOnce(ctx, again)
	if err != nil {
		t.Fatalf("重复 guid 不该报错（每轮同步都会命中）: %v", err)
	}
	if inserted {
		t.Fatalf("重复 guid 被插进去了 —— 去重整个失效")
	}
	if second.ID != first.ID {
		t.Fatalf("冲突分支应回查既有行: %+v", first)
	}
	if second.SourceName != "A番" || second.Status != domain.RSSStatusSuccess {
		t.Fatalf("回查到的应是首次写入的那条: %+v", second)
	}

	list, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{})
	if len(list) != 1 {
		t.Fatalf("重复 guid 后应只有 1 条历史，实际 %d", len(list))
	}
}

// 跨源 guid 冲突是**故意的**（单列全局唯一，见迁移注释），用例把这个口径钉住。
func TestRSSHistoryGuidIsGlobalNotPerSource(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	if _, inserted, err := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{
		SourceID: 1, Guid: "shared-guid", Status: domain.RSSStatusSuccess,
	}); err != nil || !inserted {
		t.Fatalf("首次插入失败: inserted=%v err=%v", inserted, err)
	}
	_, inserted, err := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{
		SourceID: 2, Guid: "shared-guid", Status: domain.RSSStatusSuccess,
	})
	if err != nil {
		t.Fatalf("跨源重复 guid 不该报错: %v", err)
	}
	if inserted {
		t.Fatalf("跨源同 guid 被插进去了 —— 这条会让同一条目被提交两次离线下载")
	}
}

// published_at 可空，nil 不得写成 1970。
func TestRSSHistoryNilPublishedAtStaysNull(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	_, inserted, err := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{
		SourceID: 1, Guid: "no-pub", Title: "无发布时间", Status: domain.RSSStatusSkipped,
	})
	if err != nil || !inserted {
		t.Fatalf("插入失败: inserted=%v err=%v", inserted, err)
	}
	got, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{})
	if len(got) != 1 {
		t.Fatalf("应得 1 条，实际 %d", len(got))
	}
	if got[0].PublishedAt != nil {
		t.Fatalf("nil 发布时间被写成了 %v（UI 上会显示 1970 年）", got[0].PublishedAt)
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	withTime, _, _ := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{
		SourceID: 1, Guid: "with-pub", Status: domain.RSSStatusSuccess, PublishedAt: &at,
	})
	if withTime.PublishedAt == nil || !withTime.PublishedAt.Equal(at) {
		t.Fatalf("发布时间未透传: %v", withTime.PublishedAt)
	}
	got2, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{})
	for _, g := range got2 {
		if g.Guid == "with-pub" && (g.PublishedAt == nil || !g.PublishedAt.Equal(at)) {
			t.Fatalf("回查时发布时间丢失: %+v", g)
		}
	}
}

// 历史列表过滤 + 位点。
func TestRSSHistoryListFiltersAndCursor(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, spec := range []struct {
		sourceID int64
		guid     string
		status   string
	}{
		{1, "g1", domain.RSSStatusSuccess},
		{1, "g2", domain.RSSStatusFailed},
		{2, "g3", domain.RSSStatusSuccess},
	} {
		_, _, err := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{
			SourceID: spec.sourceID, Guid: spec.guid, Title: spec.guid, Status: spec.status,
		})
		if err != nil {
			t.Fatalf("插入第 %d 条失败: %v", i, err)
		}
	}

	bySource, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{SourceID: 1})
	if len(bySource) != 2 {
		t.Fatalf("按源过滤应得 2 条，实际 %d", len(bySource))
	}
	byStatus, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{Status: domain.RSSStatusFailed})
	if len(byStatus) != 1 || byStatus[0].Guid != "g2" {
		t.Fatalf("按状态过滤失败: %+v", byStatus)
	}
	limited, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("Limit 应得 1 条，实际 %d", len(limited))
	}
	// 默认按 id 倒序（最近的在最前）
	all, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{})
	if len(all) != 3 || all[0].Guid != "g3" {
		t.Fatalf("应按 id 倒序，最新是 g3: %+v", all)
	}

	// 位点：源 1 有过处理，源 3 从未处理
	at, found, err := s.RSSHistory.LatestProcessedAt(ctx, 1)
	if err != nil || !found {
		t.Fatalf("取位点失败: found=%v err=%v", found, err)
	}
	if at.Before(base.Add(-time.Hour)) {
		t.Fatalf("位点时刻不合理: %v", at)
	}
	if _, found, err := s.RSSHistory.LatestProcessedAt(ctx, 3); err != nil || found {
		t.Fatalf("未处理过的源应 found=false: found=%v err=%v", found, err)
	}
	if _, found, err := s.RSSHistory.LatestProcessedAt(ctx, 0); err != nil || found {
		t.Fatalf("空源 ID 应 found=false: found=%v err=%v", found, err)
	}
}

// 删单条 = 放回队列；按源清理 = 删源时可选调用。
func TestRSSHistoryDeleteSemantics(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	keep, _, _ := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{SourceID: 1, Guid: "keep", Status: domain.RSSStatusCompleted})
	drop, _, _ := s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{SourceID: 2, Guid: "drop", Status: domain.RSSStatusCompleted})
	_, _, _ = s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{SourceID: 2, Guid: "drop2", Status: domain.RSSStatusCompleted})

	if has, _ := s.RSSHistory.HasGuid(ctx, "keep"); !has {
		t.Fatalf("HasGuid 查不到已写入的 guid")
	}
	if has, _ := s.RSSHistory.HasGuid(ctx, "nope"); has {
		t.Fatalf("HasGuid 对不存在的 guid 返回了 true")
	}
	if has, err := s.RSSHistory.HasGuid(ctx, "  "); err != nil || has {
		t.Fatalf("空 guid 应查不到且不报错: has=%v err=%v", has, err)
	}

	if err := s.RSSHistory.DeleteByID(ctx, keep.ID); err != nil {
		t.Fatalf("删单条失败: %v", err)
	}
	if has, _ := s.RSSHistory.HasGuid(ctx, "keep"); has {
		t.Fatalf("删掉后 guid 仍被判定为已处理 —— 豁免功能失效")
	}
	if has, _ := s.RSSHistory.HasGuid(ctx, "drop"); !has {
		t.Fatalf("误删了别的源的历史")
	}

	n, err := s.RSSHistory.DeleteBySource(ctx, 2)
	if err != nil || n != 2 {
		t.Fatalf("按源清理应删 2 条: n=%d err=%v", n, err)
	}
	if list, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{}); len(list) != 0 {
		t.Fatalf("清理后应为空，实际 %d 条", len(list))
	}
	_ = drop
}

// 空源 ID 的 DeleteBySource 不应把全表删光。
func TestRSSHistoryDeleteBySourceZeroIsNoop(t *testing.T) {
	ctx := context.Background()
	_, s := newRSSStore(t)
	_, _, _ = s.RSSHistory.InsertOnce(ctx, domain.RSSHistory{SourceID: 5, Guid: "x", Status: domain.RSSStatusSuccess})
	if _, err := s.RSSHistory.DeleteBySource(ctx, 0); err != nil {
		t.Fatalf("空源 ID 应返回 nil 错误: %v", err)
	}
	if list, _ := s.RSSHistory.List(ctx, domain.RSSHistoryQuery{}); len(list) != 1 {
		t.Fatalf("空源 ID 误删了历史，实际剩 %d 条", len(list))
	}
}
