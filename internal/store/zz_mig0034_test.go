package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 本文件是迁移 0034_subscription_identity.sql 的验收：任务书要求它「在空库和
// 已有库上都能跑通」，两件事要分开验：
//
//	1. 空库全量迁移 —— 0034 自己的 DDL 不能有语法/类型错误；
//	2. 已有库升级 —— 生产环境升级时表已经存在过、或者用户库里没有这张表，
//	   两种情况都得幂等。这条路径全量迁移测试覆盖不到：空库里 0034 只跑一次。
//
// 为什么用文件库而不是内存库：需要两次独立 Open 模拟「重启后再迁移」，
// 内存库是共享缓存池，两次 Open 拿到的是同一个底层库。

const identityCheckTable = "discovery_subscription_identity_checks"

// TestMigration0034OnEmptyDB 空库：0034 能建出完整结构。
func TestMigration0034OnEmptyDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库迁移失败：%v", err)
	}
	assertIdentityCheckTable(t, db)
}

// TestMigration0034IsIdempotentOnExistingDB 已有库升级：重复跑 Migrate 不报错，
// 表结构不丢、已有数据不丢。
func TestMigration0034IsIdempotentOnExistingDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")

	// 第一次：模拟「这台机器上 litepan 已经在跑了」。
	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if _, err := db.write.ExecContext(ctx, `INSERT INTO `+identityCheckTable+
		` (subscription_id, run_id, reason_code, passed) VALUES (1, 2, 'TITLE_MISMATCH', 0)`); err != nil {
		t.Fatalf("写入判定记录失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 第二次：模拟「升级到新版本后重启」。schema_migrations 里已有 34，
	// Migrate 应当跳过 0034 而不是重复执行。
	db2, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("已有库二次迁移失败：%v", err)
	}
	assertIdentityCheckTable(t, db2)

	var n int
	if err := db2.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+identityCheckTable).Scan(&n); err != nil {
		t.Fatalf("统计记录失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("已有判定记录应保留，实际 %d 条", n)
	}

	// 幂等语义再补一刀：把 0034 从 schema_migrations 里摘掉后重跑，
	// DDL 里的 IF NOT EXISTS 必须让迁移仍然成功（这正是「手动回滚版本号后重启」会遇到的）。
	if _, err := db2.write.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 34`); err != nil {
		t.Fatalf("回退版本记录失败：%v", err)
	}
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("0034 重跑失败（DDL 缺 IF NOT EXISTS）：%v", err)
	}
	assertIdentityCheckTable(t, db2)
	if err := db2.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+identityCheckTable).Scan(&n); err != nil {
		t.Fatalf("重跑后统计记录失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("0034 重跑不应丢数据，实际 %d 条", n)
	}
}

// assertIdentityCheckTable 校验表结构覆盖 identity_gate.go 实际写入的列。
// 少一列就会在生产上报 "no such column"，所以这里逐列钉死。
func assertIdentityCheckTable(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	var name string
	err := db.read.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, identityCheckTable).Scan(&name)
	if err != nil {
		t.Fatalf("表 %s 未建出：%v", identityCheckTable, err)
	}

	for _, col := range []string{
		"id", "subscription_id", "run_id", "rule_id", "item_key", "source", "provider",
		"slug", "tmdb_id", "expected_title", "candidate_title", "passed", "reason_code",
		"dimension", "evidence_kind", "media_type", "year", "season", "episode",
		"purity_ratio", "detail", "created_at",
	} {
		var got string
		if err := db.read.QueryRowContext(ctx,
			`SELECT name FROM pragma_table_info(?) WHERE name=?`, identityCheckTable, col).Scan(&got); err != nil {
			t.Fatalf("表 %s 缺少列 %s：%v", identityCheckTable, col, err)
		}
	}
}
