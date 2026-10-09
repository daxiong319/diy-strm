package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// 本文件是迁移 0036_search_connectors.sql 的验收，结构同 zz_mig0035_test.go：
// 空库全量迁移 + 已有库升级（含「手动回退版本号后重启」这条幂等路径）。
//
// 0035 验收的是「能不能建出来」，0036 还多钉一件事：**默认值必须让存量订阅
// 行为完全不变**。这是 T05 唯一的迁移，如果它把 search_sources 的默认值写错，
// 升级即等于给全站订阅换了一套检索源 —— 而且不会报错，只会安静地少转或多转。
// 所以下面逐列断言默认值，而不只断言列存在。

const subscriptionTable = "discovery_subscriptions"

// TestMigration0036OnEmptyDB 空库：0036 能建出完整结构、索引与默认值。
func TestMigration0036OnEmptyDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库迁移失败：%v", err)
	}
	assertSubscriptionColumns(t, db)

	// 默认值：只写标题不写任何新列，默认必须拿到「只有 tgto123 + 不限闸门」。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO `+subscriptionTable+` (entity_key, title, media_type) VALUES ('tmdb:movie:0', '测试订阅', 'movie')`); err != nil {
		t.Fatalf("按默认值插入失败：%v", err)
	}
	assertStockSubscriptionDefaults(t, db, "测试订阅")
}

// TestMigration0036KeepsExistingRows 旧库升级：历史订阅行必须原样保留，
// 并被补上「默认只有 tgto123」这个存量语义。
func TestMigration0036KeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 先只跑到 0035，把表建成 T05 之前的样子（没有那 5 个新列），
	// 从而真实复现「老库升级」这条路径。
	if err := migrateUpTo(ctx, db, 35); err != nil {
		t.Fatalf("迁移到 0035 失败：%v", err)
	}
	// 这张表历史上是由 discovery 包的 GORM AutoMigrate 建的，不是迁移建的
	// （discovery 反过来依赖 store，所以 store 的测试没法 import discovery 来建表）。
	// 这里手工复刻 T05 之前的表结构，等价于「已经在跑的老实例」的状态。
	if _, err := db.write.ExecContext(ctx, legacySubscriptionsDDL); err != nil {
		t.Fatalf("建老表失败：%v", err)
	}
	cols, err := columnNames(ctx, db, subscriptionTable)
	if err != nil {
		t.Fatalf("读取列失败：%v", err)
	}
	for _, c := range []string{"search_sources", "resolution", "effect", "min_file_size_mb", "max_file_size_mb"} {
		if _, ok := cols[c]; ok {
			t.Fatalf("前置迁移后不应已有列 %s（测试前提失效）", c)
		}
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO `+subscriptionTable+` (entity_key, title, media_type, tmdb_id) VALUES ('tmdb:tv:1396', '老订阅', 'tv', 1396)`); err != nil {
		t.Fatalf("写入老数据失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 真实升级路径：重开 + 跑全量迁移。
	db2, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	// 迁移本身必须跑通且不毁数据。
	// 注意这里**不**断言新列已存在 —— 旧库的列是由随后的 GORM AutoMigrate
	// 补的（EnsureDiscoverySchema 在 wire_http.go 里，晚于 Migrate）。
	// 补列这件事由 discovery 包侧的 TestAutoMigrateAddsSearchConnectorColumns 钉。
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("升级迁移失败：%v", err)
	}

	var tmdb int
	if err := db2.read.QueryRowContext(ctx,
		`SELECT tmdb_id FROM `+subscriptionTable+` WHERE title = '老订阅'`).Scan(&tmdb); err != nil {
		t.Fatalf("读取老数据失败：%v", err)
	}
	if tmdb != 1396 {
		t.Fatalf("升级不该改动老数据，tmdb_id=%d", tmdb)
	}

	// 回退版本号后重跑，CREATE TABLE IF NOT EXISTS 必须让迁移仍然成功。
	if _, err := db2.write.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 36`); err != nil {
		t.Fatalf("回退版本记录失败：%v", err)
	}
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("0036 重跑失败（DDL 缺 IF NOT EXISTS）：%v", err)
	}
	if err := db2.read.QueryRowContext(ctx,
		`SELECT tmdb_id FROM `+subscriptionTable+` WHERE title = '老订阅'`).Scan(&tmdb); err != nil {
		t.Fatalf("重跑后读取老数据失败：%v", err)
	}
	if tmdb != 1396 {
		t.Fatalf("0036 重跑不该丢数据，tmdb_id=%d", tmdb)
	}
}

// 0033 的号位守卫（原先叫 TestMigration0036ReservesGap0033，断言它必须空着）
// 已随 T02 开工搬到 zz_mig0033_test.go，改成断言 0033 恰好被占用一次。
// 这里不再重复：两个文件各放一份同义守卫，改一个忘另一个就会得到
// 一条红的、一条绿的矛盾信号，而没有任何线索指向该信哪个。

// assertSubscriptionColumns 逐列钉死 T05 在订阅表上新增/依赖的字段。
func assertSubscriptionColumns(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	var name string
	err := db.read.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, subscriptionTable).Scan(&name)
	if err != nil {
		t.Fatalf("表 %s 未建出：%v", subscriptionTable, err)
	}

	for _, col := range []string{
		"id", "title", "tmdb_id", "media_type", "search_sources",
		"resolution", "effect", "min_file_size_mb", "max_file_size_mb",
		"target_provider", "enabled", "next_check_at",
	} {
		var got string
		if err := db.read.QueryRowContext(ctx,
			`SELECT name FROM pragma_table_info(?) WHERE name=?`, subscriptionTable, col).Scan(&got); err != nil {
			t.Fatalf("表 %s 缺少列 %s：%v", subscriptionTable, col, err)
		}
	}
}

// assertStockSubscriptionDefaults 存量语义：没显式配过的行必须等价于改动前。
func assertStockSubscriptionDefaults(t *testing.T, db *DB, title string) {
	t.Helper()
	ctx := context.Background()

	var sources, resolution, effect string
	var minMB, maxMB int
	err := db.read.QueryRowContext(ctx,
		`SELECT search_sources, resolution, effect, min_file_size_mb, max_file_size_mb
		   FROM `+subscriptionTable+` WHERE title = ?`, title).
		Scan(&sources, &resolution, &effect, &minMB, &maxMB)
	if err != nil {
		t.Fatalf("读取默认值失败：%v", err)
	}
	if sources != "tgto123" {
		t.Fatalf("search_sources 默认必须是 tgto123（存量行为不变的关键），实际 %q", sources)
	}
	if resolution != "" || effect != "" {
		t.Fatalf("画质/特效默认应为空串（=不限制），实际 %q / %q", resolution, effect)
	}
	if minMB != 0 || maxMB != 0 {
		t.Fatalf("体积闸门默认应为 0（=不限制），实际 %d / %d", minMB, maxMB)
	}
}

// columnNames 读出某张表的全部列名。
func columnNames(ctx context.Context, db *DB, table string) (map[string]bool, error) {
	rows, err := db.read.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// legacySubscriptionsDDL T05 之前的 discovery_subscriptions 表结构。
//
// 逐列照抄 GORM AutoMigrate 当时产出的形状：字符串列可空、无 DEFAULT，
// 这样「升级后新列由 AutoMigrate 补」与「新装时列由迁移建」两条路径的差异
// 才会在测试里真正暴露出来，而不是被一个过于宽松的 DDL 掩盖过去。
const legacySubscriptionsDDL = `CREATE TABLE discovery_subscriptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_key TEXT NOT NULL UNIQUE,
    source TEXT,
    entity_type TEXT,
    external_id TEXT,
    tmdb_id INTEGER,
    media_type TEXT,
    title TEXT,
    original_title TEXT,
    poster TEXT,
    target_provider TEXT,
    transfer_mode TEXT,
    enabled numeric DEFAULT true,
    interval_minutes INTEGER,
    preferences TEXT,
    metadata TEXT,
    status TEXT DEFAULT 'pending',
    last_checked_at datetime,
    next_check_at datetime,
    created_at datetime,
    updated_at datetime
)`

// migrateUpTo 只执行版本号 ≤ maxVersion 的迁移并登记它们。
//
// 用来在测试里搭出「某个历史版本之后的库状态」。刻意不改动 migrate.go：
// Migrate 是启动路径，给测试加参数等于给生产代码开测试后门。
func migrateUpTo(ctx context.Context, db *DB, maxVersion int) error {
	if _, err := db.write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.version > maxVersion {
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
			`INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
