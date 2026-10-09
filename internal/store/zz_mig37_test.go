package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// migrateLegacyTo 把库迁移到 uptoVersion（含）为止，用于构造存量库。
//
// 只给 T07 的旧库用例用：直接按版本号跑前 N 个迁移，模拟「用户升到 0036 时的库」。
func migrateLegacyTo(ctx context.Context, db *DB, uptoVersion int) error {
	if _, err := db.write.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.version > uptoVersion {
			continue
		}
		tx, err := db.write.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(m.sql) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply %s: %w", m.name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version) VALUES (?)`, m.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// tableExistsByName 检查表或索引是否存在（sqlite_master 里 type 不同）。
func tableExistsByName(ctx context.Context, db *DB, name string) bool {
	var got string
	err := db.read.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE name=?`, name).Scan(&got)
	return err == nil && got == name
}

func readOne(ctx context.Context, db *DB, query string, args ...any) string {
	var v string
	if err := db.read.QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
		return ""
	}
	return v
}

// TestMediaUpgrade0037OnEmptyDB 验收 ⑨ 前半：空库上 0037 建表齐全。
// TestMigrateAppliesAllMigrationsOnce 已经覆盖了空库全量迁移，这里补一条
// 针对洗版三张表的显式断言，避免将来有人改了 0037 的表名而那个用例仍然绿。
func TestMediaUpgrade0037OnEmptyDB(t *testing.T) {
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
	for _, tbl := range []string{"media_upgrade_scans", "media_upgrade_records", "media_upgrade_rules"} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("表 %s 在空库迁移后不存在", tbl)
		}
	}
	want := []string{
		"idx_media_upgrade_scans_created",
		"idx_media_upgrade_records_scan",
		"idx_media_upgrade_records_series",
		"idx_media_upgrade_records_status",
		"idx_media_upgrade_rules_enabled",
	}
	for _, idx := range want {
		if !tableExistsByName(ctx, db, idx) {
			t.Fatalf("索引 %s 在空库迁移后不存在", idx)
		}
	}
	var extra string
	_ = db.read.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_media_upgrade%' AND name NOT IN (?,?,?,?,?)`,
		want[0], want[1], want[2], want[3], want[4]).Scan(&extra)
	if extra != "" {
		t.Fatalf("出现了未预期的洗版索引 %s（索引清单变了请同步更新用例）", extra)
	}
}

// TestMediaUpgrade0037OnLegacyDB 验收 ⑨ 后半：存量库（停在 0036）上补跑 0037。
//
// 这是 T05 栽过的坑的重演防护：0036 曾在「列还没被 AutoMigrate 建出来」时
// 给 discovery_subscriptions 建索引，导致老库直接 SQL 报错、升级卡死。
// 0037 是纯新建表，理论上不该有这个问题，但必须钉住。
func TestMediaUpgrade0037OnLegacyDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "legacy.db")})
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := migrateLegacyTo(ctx, db, 36); err != nil {
		t.Fatalf("migrate legacy db to 0036: %v", err)
	}
	if tableExistsByName(ctx, db, "media_upgrade_records") {
		t.Fatal("0036 之后不应该已经有 media_upgrade_records")
	}
	// 存点存量数据，确认 0037 不碰它们。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO configs(key,value) VALUES('t07_seed','legacy')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate legacy db to latest: %v", err)
	}
	for _, tbl := range []string{"media_upgrade_scans", "media_upgrade_records", "media_upgrade_rules"} {
		if !tableExistsByName(ctx, db, tbl) {
			t.Fatalf("表 %s 在旧库补跑 0037 后不存在", tbl)
		}
	}
	if got := readOne(ctx, db, `SELECT value FROM configs WHERE key='t07_seed'`); got != "legacy" {
		t.Fatalf("旧库存量数据受损：读回 %q", got)
	}
}

// TestMediaUpgrade0037IsIdempotent 重复跑迁移不应报错（schema_migrations 已记账）。
func TestMediaUpgrade0037IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, "idem.db")})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("第 %d 次迁移失败: %v", i+1, err)
		}
	}
}
