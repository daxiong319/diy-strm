package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// 求片中心 0041 的全部索引名。
//
// 这份清单与 internal/mediarequest/store.go 的查询形状一一对应：
// 少一个索引功能不会坏，只会让待审队列或「我的求片」在记录多起来之后全表扫；
// 多一个则说明迁移里留了没用的东西 —— 每个索引都要付写入代价。
var mediaRequest0041Indexes = []string{
	"idx_media_requests_requester",
	"idx_media_requests_status",
	"idx_media_requests_identity",
	"idx_media_requests_pending_identity",
	"idx_media_request_tags_identity",
	"idx_media_request_tags_lookup",
	"idx_media_request_rules_enabled",
	"idx_media_request_analytics_slot",
}

// TestMediaRequest0041OnEmptyDB 验收 ⑩ 前半：空库上 0041 建表齐全且索引不多不少。
func TestMediaRequest0041OnEmptyDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "empty.db")})
	if err != nil {
		t.Fatalf("open empty db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate empty db: %v", err)
	}
	for _, tbl := range []string{
		"media_requests",
		"media_request_tags",
		"media_request_rules",
		"media_request_analytics",
	} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("表 %s 在空库迁移后不存在", tbl)
		}
	}
	for _, idx := range mediaRequest0041Indexes {
		if !tableExistsByName(ctx, db, idx) {
			t.Fatalf("索引 %s 在空库迁移后不存在", idx)
		}
	}
	// 反向：不许有多余的求片索引（留着的都是没人查的写入成本）。
	rows, err := db.read.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_media_request%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, n := range mediaRequest0041Indexes {
		want[n] = true
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !want[name] {
			t.Fatalf("出现了未预期的求片索引 %s（索引清单变了请同步更新用例）", name)
		}
	}
}

// TestMediaRequest0041OnLegacyDB 验收 ⑩ 后半：存量库（停在 0040，已有一堆 RBAC 数据）上补跑 0041。
//
// 0041 全部是 CREATE TABLE IF NOT EXISTS，不 ALTER 任何已有表，所以存量库不该受影响。
// 这条用例盯的是两件事：别把 RBAC 的表改坏，以及「有数据」的状态下建表仍成功
// （如果新表名和旧表名撞了，IF NOT EXISTS 会静默跳过然后让代码在运行时报
// 「no such column」，只有真的建出来才查得出）。
func TestMediaRequest0041OnLegacyDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "legacy.db")})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := migrateLegacyTo(ctx, db, 40); err != nil {
		t.Fatalf("迁移到 0040: %v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO rbac_users(id, username, username_fold, display_name, password_hash, enabled)
		 VALUES(1, 'family', 'family', '家人', 'x', 1)`); err != nil {
		t.Fatalf("写存量 RBAC 数据: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("在存量库上继续迁移: %v", err)
	}
	for _, tbl := range []string{"media_requests", "media_request_tags", "media_request_rules", "media_request_analytics"} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("存量库迁移后缺表 %s", tbl)
		}
	}
	if got := readOne(ctx, db, `SELECT username FROM rbac_users WHERE id=1`); got != "family" {
		t.Fatalf("存量 RBAC 数据被改坏了: %q", got)
	}
}

// TestMediaRequest0041IsIdempotent 重复跑 0041 不该报错。
func TestMediaRequest0041IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "idem.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移: %v", err)
	}
	m, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var reqSQL string
	for _, one := range m {
		if one.version == 41 {
			reqSQL = one.sql
		}
	}
	if strings.TrimSpace(reqSQL) == "" {
		t.Fatal("找不到 0041 迁移")
	}
	for i, stmt := range splitStatements(reqSQL) {
		if _, err := db.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("第 %d 段重复执行失败: %v", i, err)
		}
	}
}

// TestMediaRequest0041TagsHaveNoSeasonColumn 把「标签挂在整部剧上」钉在迁移层。
//
// 这条是任务书里点名的产品决策（「电视剧的标签标在整部剧上，不按季隔离」）。
// 它的失效方式是渐进的：哪天有人「顺手」给标签加了 season 列，功能不会立刻坏，
// 只是一季一季地对不齐标签 —— 而对不齐是查不出来的（文件确实入库了，
// 只是少标了几个标签）。所以在迁移这一层就把形状钉死并注释清楚。
func TestMediaRequest0041TagsHaveNoSeasonColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "tags.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, col := range []string{"tmdb_id", "media_type", "requester_id", "tag"} {
		if !columnExists(ctx, db, "media_request_tags", col) {
			t.Fatalf("media_request_tags 缺列 %s", col)
		}
	}
	if columnExists(ctx, db, "media_request_tags", "season") {
		t.Fatal("media_request_tags 出现了 season 列 —— 标签必须挂在整部剧上，不按季隔离")
	}
}

// TestMediaRequest0041PendingIdentityIsPartial 验证「待审期间同一部只允许一条」是 partial 索引。
//
// 索引建成非 partial 的话，驳回之后就再也不能求这部片了（家人只能找管理员），
// 而驳回本来就该是可翻案的。
func TestMediaRequest0041PendingIdentityIsPartial(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "partial.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ins := func(id int, status string) error {
		_, err := db.write.ExecContext(ctx,
			`INSERT INTO media_requests(id, tmdb_id, media_type, title, status) VALUES(?,?,?,?,?)`,
			id, 550, "tv", "剧", status)
		return err
	}
	if err := ins(1, "pending"); err != nil {
		t.Fatalf("第一条待审写入失败: %v", err)
	}
	if err := ins(2, "pending"); err == nil {
		t.Fatal("同一作品第二条待审单被放行了 —— 待审期间应该只允许一条")
	}
	if err := ins(3, "rejected"); err != nil {
		t.Fatalf("驳回后再求应被允许: %v", err)
	}
	// 另一部作品不受影响。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO media_requests(id, tmdb_id, media_type, title, status) VALUES(4,551,'tv','另一部','pending')`); err != nil {
		t.Fatalf("不同作品的待审单被拒了: %v", err)
	}
}

func columnExists(ctx context.Context, db *DB, table, col string) bool {
	var n int
	err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col).Scan(&n)
	return err == nil && n > 0
}
