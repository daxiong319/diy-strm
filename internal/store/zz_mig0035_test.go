package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// 本文件是迁移 0035_subscription_ledger.sql 的验收，结构和 zz_mig0034_test.go 同形：
// 空库全量迁移 + 已有库升级（含「手动回退版本号后重启」这条幂等路径）。
//
// 除了 DDL 本身，这里还要钉死**唯一索引**——它是账本 CAS 的唯一裁决者，
// 少了它并发下会重复转存（见 internal/discover/discovery/transfer_ledger.go 的注释）。

const transferItemTable = "discovery_transfer_items"

// TestMigration0035OnEmptyDB 空库：0035 能建出完整结构与索引。
func TestMigration0035OnEmptyDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库迁移失败：%v", err)
	}
	assertTransferItemTable(t, db)
}

// TestMigration0035IsIdempotentOnExistingDB 已有库升级：重复 Migrate 不报错、不丢数据。
func TestMigration0035IsIdempotentOnExistingDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")

	db, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	const keyA = "key-existing-1"
	const keyB = "key-existing-2"
	if _, err := db.write.ExecContext(ctx, `INSERT INTO `+transferItemTable+
		` (idempotency_key, storage_slug, content_key, media_scope, subscription_id, state, candidate_selected_at)
		 VALUES (?, '123', 'res:slug-a', 'tmdb:1396:s1e3', 7, 'transfer_confirmed', ?)`,
		keyA, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("写入账本记录失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("已有库二次迁移失败：%v", err)
	}
	assertTransferItemTable(t, db2)

	var n int
	if err := db2.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+transferItemTable).Scan(&n); err != nil {
		t.Fatalf("统计账本记录失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("已有账本记录应保留，实际 %d 条", n)
	}

	// 回退版本号后重跑，DDL 的 IF NOT EXISTS 必须让迁移仍然成功且不丢数据。
	if _, err := db2.write.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 35`); err != nil {
		t.Fatalf("回退版本记录失败：%v", err)
	}
	if err := db2.Migrate(ctx); err != nil {
		t.Fatalf("0035 重跑失败（DDL 缺 IF NOT EXISTS）：%v", err)
	}
	assertTransferItemTable(t, db2)
	if err := db2.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+transferItemTable).Scan(&n); err != nil {
		t.Fatalf("重跑后统计失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("0035 重跑不应丢数据，实际 %d 条", n)
	}

	// 唯一索引在数据已存在时同样生效：同键再插必须失败。
	if _, err := db2.write.ExecContext(ctx, `INSERT INTO `+transferItemTable+
		` (idempotency_key, storage_slug, content_key) VALUES (?, '123', 'res:slug-a')`, keyB); err == nil {
		t.Fatalf("不同的 idempotency_key 应允许插入，这里本应成功（key 不同）")
	}
	if _, err := db2.write.ExecContext(ctx, `INSERT INTO `+transferItemTable+
		` (idempotency_key, storage_slug, content_key) VALUES (?, '123', 'res:slug-a')`, keyA); err == nil {
		t.Fatalf("唯一索引失效：重复的 idempotency_key 本应插入失败")
	}
}

// assertTransferItemTable 逐列钉死 transfer_ledger.go 实际读写的字段，并校验唯一索引。
func assertTransferItemTable(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	var name string
	err := db.read.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, transferItemTable).Scan(&name)
	if err != nil {
		t.Fatalf("表 %s 未建出：%v", transferItemTable, err)
	}

	for _, col := range []string{
		"id", "idempotency_key", "storage_slug", "content_key", "sha1", "media_scope",
		"subscription_id", "rule_id", "title", "state", "reason_code",
		"candidate_selected_at", "transfer_requested_at", "transfer_confirmed_at",
		"organized_at", "strm_created_at", "deleted_at", "created_at", "updated_at",
	} {
		var got string
		if err := db.read.QueryRowContext(ctx,
			`SELECT name FROM pragma_table_info(?) WHERE name=?`, transferItemTable, col).Scan(&got); err != nil {
			t.Fatalf("表 %s 缺少列 %s：%v", transferItemTable, col, err)
		}
	}

	// 唯一索引清单：少任何一个，账本的 CAS 与去重都不成立。
	wantUnique := map[string]bool{
		"idx_transfer_items_content": true, // (storage_slug, content_key) —— 去重真键
	}
	for indexName := range wantUnique {
		var unique int
		err := db.read.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_index_list(?) WHERE name=? AND "unique"=1`, transferItemTable, indexName).Scan(&unique)
		if err != nil {
			t.Fatalf("检查索引 %s 失败：%v", indexName, err)
		}
		if unique != 1 {
			t.Fatalf("唯一索引 %s 缺失 —— 账本 CAS 会失效并导致重复转存", indexName)
		}
	}

	// (storage_slug, sha1) 保留为普通索引：拿不到 sha1 时那一列全是空串，
	// 建唯一索引会让第二条记录直接插入失败。理由见迁移文件顶部注释。
	var sha1Unique int
	if err := db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_index_list(?) WHERE name='idx_transfer_items_storage_sha1' AND "unique"=1`,
		transferItemTable).Scan(&sha1Unique); err != nil {
		t.Fatalf("检查 sha1 索引失败：%v", err)
	}
	if sha1Unique != 0 {
		t.Fatalf("idx_transfer_items_storage_sha1 不应是唯一索引（空串会让第二条插入失败）")
	}
}
