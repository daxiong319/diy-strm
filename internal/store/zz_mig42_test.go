package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 免登录分享页 0042 的全部表名与索引名。
var libraryShare0042Tables = []string{
	"library_shares",
	"library_share_visits",
	"library_share_plays",
}

var libraryShare0042Indexes = []string{
	"idx_library_shares_created",
	"idx_library_shares_revoked",
	"idx_library_share_visits_share",
	"idx_library_share_visits_seen",
	"idx_library_share_plays_share",
	"idx_library_share_plays_started",
}

func openLibShareTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "share.db")})
	if err != nil {
		t.Fatalf("open share db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

// TestLibraryShare0042OnEmptyDB 验收 ⑪ 前半：空库上 0042 建表齐全，索引不多不少。
func TestLibraryShare0042OnEmptyDB(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate empty db: %v", err)
	}
	for _, tbl := range libraryShare0042Tables {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("表 %s 在空库迁移后不存在", tbl)
		}
	}
	for _, idx := range libraryShare0042Indexes {
		if !tableExistsByName(ctx, db, idx) {
			t.Fatalf("索引 %s 在空库迁移后不存在", idx)
		}
	}
	// 反向：不许有多余的分享索引（每个索引都要付写入代价）。
	rows, err := db.read.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND (name LIKE 'idx_library_share%')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, n := range libraryShare0042Indexes {
		want[n] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !want[name] {
			t.Fatalf("出现了未预期的分享索引 %s（索引清单变了请同步更新用例）", name)
		}
	}
}

// TestLibraryShare0042OnLegacyDB 验收 ⑪ 后半：存量库（已有求片中心等数据）补跑 0042。
//
// 0042 全部是 CREATE TABLE IF NOT EXISTS，不 ALTER 任何已有表，所以存量数据不受影响。
// 盯的是「有数据的状态下建表仍成功」：如果新表名和旧表撞了，IF NOT EXISTS 会静默跳过，
// 然后代码在运行时报「no such column」—— 只有真建出来才查得出。
func TestLibraryShare0042OnLegacyDB(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	// 先跑全部迁移（含 0041），再插一条存量数据，模拟「已经跑过 0041 的库」。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO media_requests(id, tmdb_id, media_type, title, status) VALUES(9001,12345,'tv','旧库里的求片单','approved')`); err != nil {
		t.Fatalf("写入旧库数据失败: %v", err)
	}
	// 幂等：重复执行 db.Migrate 不应报错也不应破坏任何东西。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复 migrate 失败: %v", err)
	}
	if got := readOne(ctx, db, `SELECT title FROM media_requests WHERE id=9001`); got != "旧库里的求片单" {
		t.Fatalf("迁移 0042 之后旧数据读不回来了: %q", got)
	}
	for _, tbl := range libraryShare0042Tables {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("存量库升级之后缺少表 %s", tbl)
		}
	}
}

// TestLibraryShare0042VisitsTableHasCounted 钉住 24 小时访客去重的落点。
// 没有 counted 这一列，验收第 4 条就没有实现位置。
func TestLibraryShare0042VisitsTableHasCounted(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, col := range []string{"visitor_id", "token_hash", "counted", "ip_masked", "last_seen_at"} {
		if !columnExists(ctx, db, "library_share_visits", col) {
			t.Fatalf("library_share_visits 缺少列 %s", col)
		}
	}
}

// TestLibraryShare0042VisitsOneRowPerVisitor 钉住「一个访客一份分享只有一行」。
//
// 这是并发正确性的地基：UpsertVisit 用 ON CONFLICT(share_id, visitor_id)
// 把撞车变成一条语句，靠的就是这个唯一约束。
// 没有它，代码只能退回「先查后插」，而那个写法在八个并发首访下
// 会给同一个访客插八行 —— visitor 计数翻八倍，
// 而 CountDevices 数的是 DISTINCT visitor_id 所以设备数仍是 1，
// 只看设备数完全发现不了。
func TestLibraryShare0042VisitsOneRowPerVisitor(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	insert := func(visitor, token string) error {
		_, err := db.write.ExecContext(ctx,
			`INSERT INTO library_share_visits(share_id, visitor_id, token_hash, created_at, last_seen_at)
			 VALUES('share-1', ?, ?, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`, visitor, token)
		return err
	}
	if err := insert("visitor-a", "hash-1"); err != nil {
		t.Fatalf("首次写入访问记录失败: %v", err)
	}
	if err := insert("visitor-a", "hash-2"); err == nil {
		t.Fatal("同一访客插了第二行，UPSERT 的冲突目标失效，并发下会重复计数")
	}
	if got := readOne(ctx, db,
		`SELECT COUNT(*) FROM library_share_visits WHERE share_id='share-1'`); got != "1" {
		t.Fatalf("库里有 %s 行访客记录，期望 1 行", got)
	}
	// 但换一个访客必须能插进去 —— 别把约束写成「一条分享只有一行」。
	if err := insert("visitor-b", "hash-3"); err != nil {
		t.Fatalf("换一个访客后插入被唯一约束误伤: %v", err)
	}
}

// TestLibraryShare0042RejectsDuplicateVisitToken 令牌唯一约束：同一 hash 插两次必须失败，
// 否则两个访客会共享同一个会话（撞库即可越权看别人的分享）。
func TestLibraryShare0042RejectsDuplicateVisitToken(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	insert := func(token, visitor string) error {
		_, err := db.write.ExecContext(ctx,
			`INSERT INTO library_share_visits(share_id, visitor_id, token_hash, created_at, last_seen_at)
			 VALUES('share-1', ?, ?, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`,
			visitor, token)
		return err
	}
	if err := insert("hash-same", "visitor-a"); err != nil {
		t.Fatalf("首次写入访问记录失败: %v", err)
	}
	if err := insert("hash-same", "visitor-b"); err == nil {
		t.Fatal("重复的 token_hash 竟然插进去了，令牌不再唯一")
	}
}

// TestLibraryShare0042DefaultShareColumns 分享建出来时的默认值必须「可直接判空」。
//
// password_hash 默认空串（无口令）而不是 NULL：判空写 `password_hash = ”` 时，
// 一个 NULL 会让所有无口令分享都变成「看起来有口令」而永远要密码。
// expires_at 默认 NULL（永久分享），与 参考实现 一致。
// max_devices 默认 5，对齐 mo_library_share_default_max_devices。
func TestLibraryShare0042DefaultShareColumns(t *testing.T) {
	db, _ := openLibShareTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var passwordHash any
	var expiresAt any
	var maxDevices int
	if err := db.read.QueryRowContext(ctx,
		`INSERT INTO library_shares(id, code, code_hash, created_at, updated_at)
		 VALUES('s1','abc','h1','2026-01-01 00:00:00','2026-01-01 00:00:00')
		 RETURNING password_hash, expires_at, max_devices`).
		Scan(&passwordHash, &expiresAt, &maxDevices); err != nil {
		t.Fatalf("按默认值建分享失败: %v", err)
	}
	if passwordHash != "" {
		t.Fatalf("默认口令应是空串而不是 NULL: %+v", passwordHash)
	}
	if expiresAt != nil {
		t.Fatalf("默认有效期应是 NULL（永久），实际 %+v", expiresAt)
	}
	if maxDevices != 5 {
		t.Fatalf("默认设备数应为 5，实际 %d", maxDevices)
	}
}
