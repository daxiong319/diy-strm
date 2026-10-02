package embyindex_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/embyindex"
	"litepan/internal/settings"
	"litepan/internal/store"
)

// stubConfigRepo 是内存设置仓储，测试里用来打开/关闭删除联动开关。
type stubConfigRepo struct {
	values map[string]string
}

func (r *stubConfigRepo) Get(_ context.Context, key string) (string, bool, error) {
	value, ok := r.values[key]
	return value, ok, nil
}

func (r *stubConfigRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *stubConfigRepo) All(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

// newTestRepo 打开内存 SQLite 并跑完全部迁移，返回仓储与底层 Store。
func newTestRepo(t *testing.T) (*store.Store, domain.EmbyIndexRepository) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(db)
	return st, st.EmbyIndex
}

// newTestSettings 构造只带给定键值的设置服务。
func newTestSettings(t *testing.T, values map[string]string) *settings.Service {
	t.Helper()
	if values == nil {
		values = map[string]string{}
	}
	svc, err := settings.New(context.Background(), &stubConfigRepo{values: values})
	if err != nil {
		t.Fatalf("settings.New: %v", err)
	}
	return svc
}

// seedItem 写入一条 Emby 条目索引。
func seedItem(t *testing.T, repo domain.EmbyIndexRepository, item domain.EmbyMediaItem) {
	t.Helper()
	if err := repo.CreateOrUpdateItem(context.Background(), &item); err != nil {
		t.Fatalf("CreateOrUpdateItem(%s): %v", item.ItemID, err)
	}
}

// TestCreateOrUpdateItemUpsert 覆盖条目 upsert（按 item_id 覆盖而非新增）。
func TestCreateOrUpdateItemUpsert(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "1001", ItemIDInt: 1001, Name: "旧名称", Type: "Movie",
		LibraryID: "lib-a", LastSeenSyncRun: "run-1", LastSeenAt: 10,
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "1001", ItemIDInt: 1001, Name: "新名称", Type: "Movie",
		LibraryID: "lib-a", LastSeenSyncRun: "run-2", LastSeenAt: 20,
	})

	count, err := repo.CountItems(ctx)
	if err != nil {
		t.Fatalf("CountItems: %v", err)
	}
	if count != 1 {
		t.Fatalf("条目数 = %d，期望 1（upsert 不应新增）", count)
	}
	got, err := repo.GetItem(ctx, "1001")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.Name != "新名称" {
		t.Fatalf("Name = %q，期望新名称", got.Name)
	}
	if got.LastSeenSyncRun != "run-2" || got.LastSeenAt != 20 {
		t.Fatalf("LastSeen 字段未更新：%q / %d", got.LastSeenSyncRun, got.LastSeenAt)
	}
}

// TestCreateOrUpdateItemRejectsEmptyID 覆盖空 item_id 校验。
func TestCreateOrUpdateItemRejectsEmptyID(t *testing.T) {
	_, repo := newTestRepo(t)
	err := repo.CreateOrUpdateItem(context.Background(), &domain.EmbyMediaItem{Name: "无 ID"})
	if err == nil {
		t.Fatal("空 item_id 应当报错")
	}
}

// TestCleanupStaleItemsByLibrarySyncRun 覆盖按批次清理陈旧条目及其关联。
// 移植老版 TestCleanupStaleEmbyMediaItemsByLibrarySyncRun只清理当前库旧批次 的意图。
func TestCleanupStaleItemsByLibrarySyncRun(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Episode", LibraryID: "lib-a", LastSeenSyncRun: "run-old",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "102", ItemIDInt: 102, Type: "Episode", LibraryID: "lib-a", LastSeenSyncRun: "run-old",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "201", ItemIDInt: 201, Type: "Episode", LibraryID: "lib-b", LastSeenSyncRun: "run-old",
	})
	// 给 lib-a 的 101 建一条网盘关联，验证关联会被级联清理。
	if err := repo.CreateMediaSyncFile(ctx, &domain.EmbyMediaSyncFile{
		EmbyItemID: 101, SyncFileID: 7, PickCode: "pick-101", SyncPathID: 7,
	}); err != nil {
		t.Fatalf("CreateMediaSyncFile: %v", err)
	}

	removed, err := repo.CleanupStaleItemsByLibrarySyncRun(ctx, "lib-a", "run-new")
	if err != nil {
		t.Fatalf("CleanupStaleItemsByLibrarySyncRun: %v", err)
	}
	if removed != 2 {
		t.Fatalf("清理数 = %d，期望 2", removed)
	}

	// 只剩 lib-b 的 201。
	count, err := repo.CountItems(ctx)
	if err != nil {
		t.Fatalf("CountItems: %v", err)
	}
	if count != 1 {
		t.Fatalf("剩余条目数 = %d，期望 1", count)
	}
	if _, err := repo.GetItem(ctx, "201"); err != nil {
		t.Fatalf("lib-b 的条目不应被清理：%v", err)
	}
	// 关联也应被一并清理。
	rels, err := repo.MediaSyncFilesByItemID(ctx, 101)
	if err != nil {
		t.Fatalf("MediaSyncFilesByItemID: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("陈旧条目的关联未被清理，剩余 %d 条", len(rels))
	}
}

// TestCleanupStaleItemsKeepsCurrentRun 覆盖「本批次已见到的条目不清理」。
func TestCleanupStaleItemsKeepsCurrentRun(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Movie", LibraryID: "lib-a", LastSeenSyncRun: "run-new",
	})
	removed, err := repo.CleanupStaleItemsByLibrarySyncRun(ctx, "lib-a", "run-new")
	if err != nil {
		t.Fatalf("CleanupStaleItemsByLibrarySyncRun: %v", err)
	}
	if removed != 0 {
		t.Fatalf("清理数 = %d，期望 0（本批次条目应保留）", removed)
	}
}

// TestCleanupOrphanedItems 覆盖孤儿条目清理。
func TestCleanupOrphanedItems(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "keep", ItemIDInt: 1, Type: "Movie"})
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "drop", ItemIDInt: 2, Type: "Movie"})

	removed, err := repo.CleanupOrphanedItems(ctx, []string{"keep"})
	if err != nil {
		t.Fatalf("CleanupOrphanedItems: %v", err)
	}
	if removed != 1 {
		t.Fatalf("清理数 = %d，期望 1", removed)
	}
	if _, err := repo.GetItem(ctx, "drop"); err == nil {
		t.Fatal("孤儿条目 drop 应被删除")
	}
}

// TestCleanupOrphanedItemsEmptyMeansAll 覆盖空列表等同于清空全部。
func TestCleanupOrphanedItemsEmptyMeansAll(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "a", ItemIDInt: 1, Type: "Movie"})
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "b", ItemIDInt: 2, Type: "Movie"})

	removed, err := repo.CleanupOrphanedItems(ctx, nil)
	if err != nil {
		t.Fatalf("CleanupOrphanedItems: %v", err)
	}
	if removed != 2 {
		t.Fatalf("清理数 = %d，期望 2", removed)
	}
}

// TestDeleteItemByIDCascade 覆盖按条目删除及其关联级联。
func TestDeleteItemByIDCascade(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "101", ItemIDInt: 101, Type: "Movie"})
	if err := repo.CreateMediaSyncFile(ctx, &domain.EmbyMediaSyncFile{
		EmbyItemID: 101, SyncFileID: 5, PickCode: "p",
	}); err != nil {
		t.Fatalf("CreateMediaSyncFile: %v", err)
	}

	if err := repo.DeleteItemByID(ctx, "101"); err != nil {
		t.Fatalf("DeleteItemByID: %v", err)
	}
	if _, err := repo.GetItem(ctx, "101"); err == nil {
		t.Fatal("条目应被删除")
	}
	rels, err := repo.MediaSyncFilesByItemID(ctx, 101)
	if err != nil {
		t.Fatalf("MediaSyncFilesByItemID: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("关联未级联删除，剩余 %d 条", len(rels))
	}
}

// TestDeleteItemsBySeasonIDCascade 覆盖按季删除级联。
// 移植老版 DeleteLocalEmbyItemsBySeasonID删除季内条目和关联 的意图。
func TestDeleteItemsBySeasonIDCascade(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Episode", SeasonID: "season-a", SeriesID: "series-a",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "102", ItemIDInt: 102, Type: "Episode", SeasonID: "season-a", SeriesID: "series-a",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "201", ItemIDInt: 201, Type: "Episode", SeasonID: "season-b", SeriesID: "series-b",
	})
	if err := repo.CreateMediaSyncFile(ctx, &domain.EmbyMediaSyncFile{
		EmbyItemID: 101, SyncFileID: 5, PickCode: "p101",
	}); err != nil {
		t.Fatalf("CreateMediaSyncFile: %v", err)
	}

	if err := repo.DeleteItemsBySeasonID(ctx, "season-a"); err != nil {
		t.Fatalf("DeleteItemsBySeasonID: %v", err)
	}
	items, err := repo.ItemsBySeasonID(ctx, "season-a")
	if err != nil {
		t.Fatalf("ItemsBySeasonID: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("season-a 剩余 %d 条，期望 0", len(items))
	}
	// season-b 不受影响。
	kept, err := repo.ItemsBySeasonID(ctx, "season-b")
	if err != nil {
		t.Fatalf("ItemsBySeasonID: %v", err)
	}
	if len(kept) != 1 || kept[0].ItemID != "201" {
		t.Fatalf("season-b 应保留 201，实际 %+v", kept)
	}
	// 关联级联清理。
	rels, err := repo.MediaSyncFilesByItemID(ctx, 101)
	if err != nil {
		t.Fatalf("MediaSyncFilesByItemID: %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("season-a 关联未级联删除，剩余 %d 条", len(rels))
	}
}

// TestDeleteItemsBySeriesIDCascade 覆盖按剧删除级联。
func TestDeleteItemsBySeriesIDCascade(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Episode", SeasonID: "season-a", SeriesID: "series-a",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "102", ItemIDInt: 102, Type: "Episode", SeasonID: "season-a", SeriesID: "series-a",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "201", ItemIDInt: 201, Type: "Episode", SeasonID: "season-b", SeriesID: "series-b",
	})

	if err := repo.DeleteItemsBySeriesID(ctx, "series-a"); err != nil {
		t.Fatalf("DeleteItemsBySeriesID: %v", err)
	}
	items, err := repo.ItemsBySeriesID(ctx, "series-a")
	if err != nil {
		t.Fatalf("ItemsBySeriesID: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("series-a 剩余 %d 条，期望 0", len(items))
	}
	kept, err := repo.ItemsBySeriesID(ctx, "series-b")
	if err != nil {
		t.Fatalf("ItemsBySeriesID: %v", err)
	}
	if len(kept) != 1 || kept[0].ItemID != "201" {
		t.Fatalf("series-b 应保留 201，实际 %+v", kept)
	}
}

// TestSyncCursorRoundTrip 覆盖增量游标的读写与推进。
func TestSyncCursorRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	// 首次读取返回零值且不报错（首轮同步路径）。
	cursor, err := repo.GetSyncCursor(ctx, "cfg-1")
	if err != nil {
		t.Fatalf("GetSyncCursor: %v", err)
	}
	if cursor.LastSavedCursorAt != 0 || cursor.ConfigID != "cfg-1" {
		t.Fatalf("首次游标 = %+v，期望零值", cursor)
	}

	if err := repo.AdvanceSyncCursor(ctx, "cfg-1", 1782698400, 1782702000); err != nil {
		t.Fatalf("AdvanceSyncCursor: %v", err)
	}
	cursor, err = repo.GetSyncCursor(ctx, "cfg-1")
	if err != nil {
		t.Fatalf("GetSyncCursor: %v", err)
	}
	if cursor.LastSavedCursorAt != 1782698400 {
		t.Fatalf("LastSavedCursorAt = %d，期望 1782698400", cursor.LastSavedCursorAt)
	}
	if cursor.LastIncrementalAt != 1782702000 {
		t.Fatalf("LastIncrementalAt = %d，期望 1782702000", cursor.LastIncrementalAt)
	}

	// TouchSyncTime 只更新全量同步时间，不动游标。
	if err := repo.TouchSyncTime(ctx, "cfg-1", 1782800000); err != nil {
		t.Fatalf("TouchSyncTime: %v", err)
	}
	cursor, err = repo.GetSyncCursor(ctx, "cfg-1")
	if err != nil {
		t.Fatalf("GetSyncCursor: %v", err)
	}
	if cursor.LastSyncTime != 1782800000 {
		t.Fatalf("LastSyncTime = %d，期望 1782800000", cursor.LastSyncTime)
	}
	if cursor.LastSavedCursorAt != 1782698400 {
		t.Fatalf("TouchSyncTime 不应改动游标，实际 %d", cursor.LastSavedCursorAt)
	}
}

// TestCreateMediaSyncFileDedupes 覆盖关联写入的业务键去重。
func TestCreateMediaSyncFileDedupes(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	rel := &domain.EmbyMediaSyncFile{EmbyItemID: 101, SyncFileID: 5, PickCode: "pick-1", SyncPathID: 5}
	if err := repo.CreateMediaSyncFile(ctx, rel); err != nil {
		t.Fatalf("CreateMediaSyncFile: %v", err)
	}
	if err := repo.CreateMediaSyncFile(ctx, rel); err != nil {
		t.Fatalf("重复写入不应报错：%v", err)
	}
	rels, err := repo.MediaSyncFilesByItemID(ctx, 101)
	if err != nil {
		t.Fatalf("MediaSyncFilesByItemID: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("关联数 = %d，期望 1（重复写入应被去重）", len(rels))
	}
}

// TestMediaSyncFileStoresRelativeLocation 覆盖现版新增的网盘定位字段往返。
func TestMediaSyncFileStoresRelativeLocation(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	rel := &domain.EmbyMediaSyncFile{
		EmbyItemID: 101, SyncFileID: 5, PickCode: "pick-1", SyncPathID: 5,
		AccountID: 42, RootID: "root-x", RelativePath: "电影/影片.mkv", FileName: "影片.mkv",
	}
	if err := repo.CreateMediaSyncFile(ctx, rel); err != nil {
		t.Fatalf("CreateMediaSyncFile: %v", err)
	}
	rels, err := repo.MediaSyncFilesByItemID(ctx, 101)
	if err != nil {
		t.Fatalf("MediaSyncFilesByItemID: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("关联数 = %d", len(rels))
	}
	got := rels[0]
	if got.AccountID != 42 || got.RootID != "root-x" ||
		got.RelativePath != "电影/影片.mkv" || got.FileName != "影片.mkv" {
		t.Fatalf("网盘定位字段未正确往返：%+v", got)
	}
}

// TestUpsertLibrariesAndCleanupDeleted 覆盖媒体库 upsert 与删除清理。
func TestUpsertLibrariesAndCleanupDeleted(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	if err := repo.UpsertLibraries(ctx, []domain.EmbyLibrary{
		{Name: "电影", LibraryID: "lib-a"},
		{Name: "剧集", LibraryID: "lib-b"},
	}); err != nil {
		t.Fatalf("UpsertLibraries: %v", err)
	}
	// 改名再 upsert 不应新增。
	if err := repo.UpsertLibraries(ctx, []domain.EmbyLibrary{
		{Name: "电影（新）", LibraryID: "lib-a"},
	}); err != nil {
		t.Fatalf("UpsertLibraries: %v", err)
	}
	libs, err := repo.ListLibraries(ctx)
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 2 {
		t.Fatalf("媒体库数 = %d，期望 2", len(libs))
	}

	// 只保留 lib-a，lib-b 应被清理。
	if err := repo.CleanupDeletedLibraries(ctx, []string{"lib-a"}); err != nil {
		t.Fatalf("CleanupDeletedLibraries: %v", err)
	}
	libs, err = repo.ListLibraries(ctx)
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 1 || libs[0].LibraryID != "lib-a" {
		t.Fatalf("清理后媒体库 = %+v，期望只剩 lib-a", libs)
	}
}

// TestLibrarySyncPathRoundTrip 覆盖媒体库↔同步路径关联。
func TestLibrarySyncPathRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	if err := repo.CreateOrUpdateLibrarySyncPath(ctx, "lib-a", 5, "电影"); err != nil {
		t.Fatalf("CreateOrUpdateLibrarySyncPath: %v", err)
	}
	if err := repo.CreateOrUpdateLibrarySyncPath(ctx, "lib-a", 5, "电影（改名）"); err != nil {
		t.Fatalf("CreateOrUpdateLibrarySyncPath: %v", err)
	}
	got, err := repo.LibraryIDsBySyncPathID(ctx, 5)
	if err != nil {
		t.Fatalf("LibraryIDsBySyncPathID: %v", err)
	}
	if len(got) != 1 || got["lib-a"] != "电影（改名）" {
		t.Fatalf("关联 = %+v，期望 lib-a → 电影（改名）", got)
	}

	if err := repo.DeleteLibrarySyncPathsBySyncPathID(ctx, 5); err != nil {
		t.Fatalf("DeleteLibrarySyncPathsBySyncPathID: %v", err)
	}
	got, err = repo.LibraryIDsBySyncPathID(ctx, 5)
	if err != nil {
		t.Fatalf("LibraryIDsBySyncPathID: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("删除后仍有 %d 条关联", len(got))
	}
}

// TestCleanupUnselectedLibraryDataEmptyMeansAll 覆盖「未选中任何库 = 全清」的老版语义。
func TestCleanupUnselectedLibraryDataEmptyMeansAll(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	if err := repo.UpsertLibraries(ctx, []domain.EmbyLibrary{{Name: "电影", LibraryID: "lib-a"}}); err != nil {
		t.Fatalf("UpsertLibraries: %v", err)
	}
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "101", ItemIDInt: 101, Type: "Movie", LibraryID: "lib-a"})

	if err := repo.CleanupUnselectedLibraryData(ctx, nil); err != nil {
		t.Fatalf("CleanupUnselectedLibraryData: %v", err)
	}
	count, err := repo.CountItems(ctx)
	if err != nil {
		t.Fatalf("CountItems: %v", err)
	}
	if count != 0 {
		t.Fatalf("条目数 = %d，期望 0（空选中列表应清空全部）", count)
	}
	libs, err := repo.ListLibraries(ctx)
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 0 {
		t.Fatalf("媒体库数 = %d，期望 0", len(libs))
	}
}

// TestCleanupUnselectedLibraryDataKeepsSelected 覆盖保留选中库的数据。
func TestCleanupUnselectedLibraryDataKeepsSelected(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	if err := repo.UpsertLibraries(ctx, []domain.EmbyLibrary{
		{Name: "电影", LibraryID: "lib-a"},
		{Name: "剧集", LibraryID: "lib-b"},
	}); err != nil {
		t.Fatalf("UpsertLibraries: %v", err)
	}
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "101", ItemIDInt: 101, Type: "Movie", LibraryID: "lib-a"})
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "201", ItemIDInt: 201, Type: "Movie", LibraryID: "lib-b"})

	if err := repo.CleanupUnselectedLibraryData(ctx, []string{"lib-a"}); err != nil {
		t.Fatalf("CleanupUnselectedLibraryData: %v", err)
	}
	if _, err := repo.GetItem(ctx, "101"); err != nil {
		t.Fatalf("选中库的条目应保留：%v", err)
	}
	if _, err := repo.GetItem(ctx, "201"); err == nil {
		t.Fatal("未选中库的条目应被清理")
	}
}

// TestGetItemNotFoundReturnsNotFound 覆盖缺失条目的错误码。
func TestGetItemNotFoundReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)

	_, err := repo.GetItem(ctx, "不存在")
	if err == nil {
		t.Fatal("缺失条目应当报错")
	}
	appErr, ok := domain.AsAppError(err)
	if !ok || appErr.Code != domain.CodeNotFound {
		t.Fatalf("错误 = %v，期望 CodeNotFound", err)
	}
}

// TestNewReturnsNilWithoutRepository 覆盖未装配索引时返回 nil 服务。
func TestNewReturnsNilWithoutRepository(t *testing.T) {
	if svc := embyindex.New(embyindex.Options{}); svc != nil {
		t.Fatal("Index 为空时 New 应返回 nil")
	}
}

// TestDeleteLinkageDisabledByDefault 是删除联动最重要的安全断言：
// 开关默认关闭时，即使映射完整也绝不删除。
func TestDeleteLinkageDisabledByDefault(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, nil),
	})

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Movie", PickCode: "pick-1",
	})

	outcome, err := svc.DeleteNetdiskMovieByEmbyItemID(ctx, "101")
	if err != nil {
		t.Fatalf("关闭开关时不应报错：%v", err)
	}
	if !outcome.Skipped || outcome.Deleted {
		t.Fatalf("开关默认关闭时必须跳过，实际 %+v", outcome)
	}
	if outcome.Reason != "delete_netdisk_disabled" {
		t.Fatalf("跳过原因 = %q，期望 delete_netdisk_disabled", outcome.Reason)
	}
	// 索引必须完好无损。
	if _, err := repo.GetItem(ctx, "101"); err != nil {
		t.Fatalf("跳过时不应改动索引：%v", err)
	}
}

// TestDeleteLinkageSkipsWithoutMapping 覆盖开关打开但映射缺失时跳过。
func TestDeleteLinkageSkipsWithoutMapping(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, map[string]string{settings.KeyEmbyDeleteNetdiskEnabled: "true"}),
	})

	// 条目没有 PickCode。
	seedItem(t, repo, domain.EmbyMediaItem{ItemID: "101", ItemIDInt: 101, Type: "Movie"})

	outcome, err := svc.DeleteNetdiskMovieByEmbyItemID(ctx, "101")
	if err != nil {
		t.Fatalf("映射缺失时不应报错：%v", err)
	}
	if !outcome.Skipped || outcome.Deleted {
		t.Fatalf("映射缺失时必须跳过，实际 %+v", outcome)
	}
	if outcome.Reason != "no_backing_file_mapping" {
		t.Fatalf("跳过原因 = %q，期望 no_backing_file_mapping", outcome.Reason)
	}
	if _, err := repo.GetItem(ctx, "101"); err != nil {
		t.Fatalf("跳过时不应改动索引：%v", err)
	}
}

// TestDeleteLinkageSkipsWhenItemNotIndexed 覆盖条目不在索引时跳过。
func TestDeleteLinkageSkipsWhenItemNotIndexed(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, map[string]string{settings.KeyEmbyDeleteNetdiskEnabled: "true"}),
	})

	outcome, err := svc.DeleteNetdiskMovieByEmbyItemID(ctx, "从未索引")
	if err != nil {
		t.Fatalf("未索引条目不应报错：%v", err)
	}
	if !outcome.Skipped || outcome.Deleted {
		t.Fatalf("未索引条目必须跳过，实际 %+v", outcome)
	}
	if outcome.Reason != "emby_item_not_indexed" {
		t.Fatalf("跳过原因 = %q，期望 emby_item_not_indexed", outcome.Reason)
	}
}

// TestDeleteLinkageSkipsOnAmbiguousSeasonDirs 是核心安全断言：
// 同一季的集散落在多个目录时无法确定唯一删除目标，必须拒绝删除并标记歧义。
func TestDeleteLinkageSkipsOnAmbiguousSeasonDirs(t *testing.T) {
	ctx := context.Background()
	_, repo := newTestRepo(t)
	svc := embyindex.New(embyindex.Options{
		Index:    repo,
		Settings: newTestSettings(t, map[string]string{settings.KeyEmbyDeleteNetdiskEnabled: "true"}),
	})

	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "101", ItemIDInt: 101, Type: "Episode", SeasonID: "season-a", PickCode: "pick-1",
	})
	seedItem(t, repo, domain.EmbyMediaItem{
		ItemID: "102", ItemIDInt: 102, Type: "Episode", SeasonID: "season-a", PickCode: "pick-2",
	})

	outcome, err := svc.DeleteNetdiskSeasonByItemID(ctx, "season-a")
	if err != nil {
		t.Fatalf("歧义映射时不应报错：%v", err)
	}
	if !outcome.Skipped || outcome.Deleted {
		t.Fatalf("歧义映射必须跳过删除，实际 %+v", outcome)
	}
	if len(outcome.RemovedPaths) != 0 {
		t.Fatalf("歧义映射不应删除任何路径，实际 %v", outcome.RemovedPaths)
	}
	// 索引必须完好，供人工排查。
	items, err := repo.ItemsBySeasonID(ctx, "season-a")
	if err != nil {
		t.Fatalf("ItemsBySeasonID: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("歧义跳过时索引应完好，剩余 %d 条", len(items))
	}
}
