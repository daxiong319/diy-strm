package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 本文件是迁移 0033_classification_v2.sql 的验收。
//
// 0033 只做一件事：给 classify_rules 补 SQL 侧的建表兜底。所以「空库能跑」
// 几乎不构成风险 —— 它只证明 DDL 语法没问题。真正的风险点是「有数据的旧库能跑」：
// 老库上调 0033 时 classify_rules 早就由 GORM AutoMigrate 建好了，
// CREATE TABLE IF NOT EXISTS 必须老老实实跳过，而不是报「表已存在」让整个
// 启动失败。这类问题在开发机上永远看不到（开发机每次都是空库），
// 只在真实用户升级那一刻炸，所以必须有存量库这一路用例。
//
// 另一处顺序陷阱：0033 的版本号比 0034~0042 都小，Migrate 按版本号升序应用，
// 因此在存量用例里 0033 是**第一个**被应用的，后面 0034 之后的迁移不会回头
// 碰 classify_rules。

// TestMigration0033OnEmptyDB 空库：0033 能建出完整结构。
func TestMigration0033OnEmptyDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库迁移失败：%v", err)
	}
	assertClassifyRulesTable(t, db)
}

// TestMigration0033OnLegacyDBWithExistingClassifyRules 老库升级：0033 是全序列里
// 版本号最小的迁移之一，升级时排在最前面，必须能安全跳过 AutoMigrate 建好的表。
func TestMigration0033OnLegacyDBWithExistingClassifyRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// 模拟「这台机器在 T02 之前已经跑了很久」：先只跑到 0032，
	// 再手工建出 AutoMigrate 版本的 classify_rules 并塞一条用户规则。
	if err := migrateLegacyTo(ctx, db, 32); err != nil {
		t.Fatalf("构造存量库失败：%v", err)
	}
	if _, err := db.write.ExecContext(ctx, `CREATE TABLE classify_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		media_type TEXT NOT NULL DEFAULT '',
		target_path TEXT NOT NULL DEFAULT '',
		conditions TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		position INTEGER NOT NULL DEFAULT 0,
		remark TEXT NOT NULL DEFAULT '',
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("构造存量 classify_rules 失败：%v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_rules(media_type, target_path, conditions, enabled, position)
		 VALUES ('movie', '电影/科幻', '[{"Key":"genres","Values":"科幻"}]', 1, 3)`); err != nil {
		t.Fatalf("写入存量规则失败：%v", err)
	}

	// 现在模拟升级：全量 Migrate。0033 会在这里第一次被应用。
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("存量库迁移失败（0033 应跳过已有表）：%v", err)
	}
	assertClassifyRulesTable(t, db)

	if n := readOne(ctx, db, `SELECT COUNT(*) FROM classify_rules`); n != "1" {
		t.Fatalf("存量规则应保留，实际 %s 条", n)
	}
	if got := readOne(ctx, db,
		`SELECT target_path || '|' || conditions || '|' || position FROM classify_rules WHERE id = 1`); got != "电影/科幻|[{\"Key\":\"genres\",\"Values\":\"科幻\"}]|3" {
		t.Fatalf("存量规则内容被改动：%q", got)
	}
}

// TestMigration0033IsIdempotentOnExistingDB 幂等：把 0033 从 schema_migrations 里
// 摘掉后重跑，IF NOT EXISTS 必须让迁移仍然成功且不丢数据。
// 这条对应「用户手工回滚过版本号再重启」的场景。
func TestMigration0033IsIdempotentOnExistingDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_rules(media_type, target_path, conditions, enabled, position)
		 VALUES ('movie', '电影/科幻', '[{"Key":"genres","Values":"科幻"}]', 1, 3)`); err != nil {
		t.Fatalf("写入规则失败：%v", err)
	}

	if _, err := db.write.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 33`); err != nil {
		t.Fatalf("回退版本记录失败：%v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("0033 重跑失败：%v", err)
	}
	assertClassifyRulesTable(t, db)

	if n := readOne(ctx, db, `SELECT COUNT(*) FROM classify_rules`); n != "1" {
		t.Fatalf("0033 重跑不应丢数据，实际 %s 条", n)
	}
}

// TestMigration0033ColumnDefaults 只给最少列插入，读回 enabled/position。
//
// 这点很要紧 —— 分类规则表若默认停用，老用户升级后导入的 YAML 规则会全部失效，
// 而症状是「静默不生效」，用户很难往迁移默认值上想。
// 单独成例而不是并进结构断言：结构断言被三个用例共用，往里写数据会污染
// 那些用例的 COUNT 断言。
func TestMigration0033ColumnDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO classify_rules(target_path) VALUES ('电影')`); err != nil {
		t.Fatalf("插入最小分类规则失败：%v", err)
	}
	if got := readOne(ctx, db,
		`SELECT enabled || '/' || position FROM classify_rules WHERE target_path = '电影'`); got != "1/0" {
		t.Fatalf("classify_rules 默认应为 enabled=1 position=0，实际 %q", got)
	}
}

// TestMigration0033OccupiesTheReservedSlot 0033 原本是 T05 之后为 T02 预留的空号，
// 那时立了一条守卫断言它必须空着，防止别的迁移把它占掉 —— 占用会让 T02 上线时被迫
// 改号，而改号意味着已部署实例要么漏跑要么重跑迁移。
//
// T02 开工后 0033 由 migrations/0033_classification_v2.sql 正式占用，守卫从
// 「0033 必须空」翻成「0033 必须恰好一条」。理由和当初反过来一样：现在要防的是
// 有人另起一个 0033_xxx.sql（两条同号记录会重复应用），或把 0033 从
// schema_migrations 里删掉（会让它在每次重启时重跑）。
func TestMigration0033OccupiesTheReservedSlot(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	var n int
	if err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = 33`).Scan(&n); err != nil {
		t.Fatalf("查询 0033 失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("0033 是 T02 的迁移号，应恰好一条记录，实际 %d 条", n)
	}
}

// assertClassifyRulesTable 逐列钉死 0033 建出的结构。
// 少一列就会在生产上报 "no such column"，所以这里逐列断言而不是只查表存在。
func assertClassifyRulesTable(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	if !tableExistsByName(ctx, db, "classify_rules") {
		t.Fatalf("表 classify_rules 未建出")
	}
	if !tableExistsByName(ctx, db, "idx_classify_rules_position") {
		t.Fatalf("索引 idx_classify_rules_position 未建出")
	}

	wantCols := map[string]string{
		"id": "INTEGER", "media_type": "TEXT", "target_path": "TEXT", "conditions": "TEXT",
		"enabled": "INTEGER", "position": "INTEGER", "remark": "TEXT",
		"created_at": "DATETIME", "updated_at": "DATETIME",
	}
	for col, wantType := range wantCols {
		if got := readOne(ctx, db,
			`SELECT type FROM pragma_table_info(?) WHERE name=?`, "classify_rules", col); got != wantType {
			t.Fatalf("classify_rules.%s 类型应为 %s，实际 %q", col, wantType, got)
		}
	}
	// 反向断言：列不多不少。迁移里多写一列 auto-migrate 建出来的表没有，
	// 会让「存量库跳过 IF NOT EXISTS」和「新库建全」的两种结构分叉。
	if n := readOne(ctx, db,
		`SELECT COUNT(*) FROM pragma_table_info('classify_rules')`); n != "9" {
		t.Fatalf("classify_rules 应恰好 9 列，实际 %s 列", n)
	}
}
