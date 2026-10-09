package store_test

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/store"
)

// T31 · 洗版结构化驳回理由 迁移 0048 的验收 ⑤：空库 + 有数据旧库都跑通。
//
// 这条迁移只做两件事：给 media_upgrade_records 加两列。最容易出事的不是「新库能不能建」，
// 而是**存量行**：ALTER ADD COLUMN NOT NULL DEFAULT '' 会给每一行填空串，
// 而如果迁移顺手改写了旧行（或者将来有人把 DEFAULT 改成 null），
// 存量记录的 trace 与判定结论就会静默变化 —— 那些是已经发生过的洗版事实。

func TestWashReasonMigrationEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库跑迁移失败: %v", err)
	}
	for _, col := range []string{"reject_reasons", "rule_fingerprint"} {
		if !columnExists(t, db, "media_upgrade_records", col) {
			t.Fatalf("空库迁移后 media_upgrade_records 缺列 %s", col)
		}
	}
}

func TestWashReasonMigrationKeepsLegacyRecordsIntact(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 先造一个「0048 之前」的库结构（migrateUpToTo 会把新列摘掉）。
	migrateUpToTo(t, db, 47)

	// 存量记录：trace 有值，两列还不存在。
	mustExec(t, db, `INSERT INTO media_upgrade_records
		(scan_id, slot_key, quality_relation, trace, loser_action)
		VALUES (1, 'res=2160p', 'new_loses', '分辨率 1080p<2160p（新差）', 'delete')`)
	mustExec(t, db, `INSERT INTO media_upgrade_records
		(scan_id, slot_key, quality_relation, trace, loser_action)
		VALUES (1, 'res=1080p', 'new_wins', '分辨率 2160p>1080p（新优）', 'delete')`)

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("存量库跑 0048 失败: %v", err)
	}

	rows, err := db.ReadHandle().QueryContext(ctx,
		`SELECT quality_relation, trace, reject_reasons, rule_fingerprint FROM media_upgrade_records ORDER BY id`)
	if err != nil {
		t.Fatalf("读存量记录失败: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var relation, trace, reasons, fp string
		if err := rows.Scan(&relation, &trace, &reasons, &fp); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		n++
		if reasons != "" {
			t.Fatalf("存量行的 reject_reasons 应为空串，实际 %q —— 迁移猜了历史判定结论", reasons)
		}
		if fp != "" {
			t.Fatalf("存量行的 rule_fingerprint 应为空串，实际 %q —— 迁移猜了历史规则版本", fp)
		}
		if trace == "" {
			t.Fatalf("存量行的 trace 被清空了")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("存量记录数 = %d，期望 2", n)
	}
}

// TestWashReasonMigrationNewColumnsRoundTrip 新列能写能读，且不影响老列。
func TestWashReasonMigrationNewColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	const reasons = `[{"code":"inferior_dimension","field":"bitdepth","new":"8bit","old":"10bit"}]`
	mustExec(t, db, `INSERT INTO media_upgrade_records
		(scan_id, slot_key, quality_relation, trace, loser_action, reject_reasons, rule_fingerprint)
		VALUES (1, 'k', 'new_loses', '色深 8bit<10bit（新差）', 'delete', ?, 'a01beccb894678ca')`, reasons)

	var gotReasons, gotFP, gotTrace string
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT reject_reasons, rule_fingerprint, trace FROM media_upgrade_records LIMIT 1`).
		Scan(&gotReasons, &gotFP, &gotTrace); err != nil {
		t.Fatalf("读新列失败: %v", err)
	}
	if gotReasons != reasons {
		t.Fatalf("reject_reasons 往返不一致：%q != %q", gotReasons, reasons)
	}
	if gotFP != "a01beccb894678ca" {
		t.Fatalf("rule_fingerprint 往返不一致：%q", gotFP)
	}
	if !strings.Contains(gotTrace, "色深") {
		t.Fatalf("trace 被新列影响了：%q", gotTrace)
	}
}

// TestWashReasonMigrationNoIndexDeliberately 刻意不加索引，且要在迁移文件里写明理由。
//
// 「为什么没建索引」这种话只能写在迁移文件里 —— 半年后有人看到这张表
// 按 code 查不到索引，第一反应是「迁移漏了」而不是「当初刻意不加」。
// 这个用例的作用就是把那句理由钉住，防止有人顺手加个索引（SQLite 的
// TEXT 索引对 JSON 数组成员查询没用，只会让每次写入多付一次索引维护）。
func TestWashReasonMigrationNoIndexDeliberately(t *testing.T) {
	db := mustMigrated(t)
	rows, err := db.ReadHandle().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='media_upgrade_records'`)
	if err != nil {
		t.Fatalf("读索引失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描索引失败: %v", err)
		}
		if strings.Contains(name, "reject_reasons") {
			t.Fatalf("给 reject_reasons 建了索引 %q —— 当初刻意不建（JSON 数组成员查询用不上）", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历索引失败: %v", err)
	}
}

// columnExists 查某张表有没有某列。
func columnExists(t *testing.T, db *store.DB, table, column string) bool {
	t.Helper()
	rows, err := db.ReadHandle().QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("读 %s 表结构失败: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败: %v", err)
	}
	return false
}

// mustMigrated 开一个跑完全部迁移的内存库。
func mustMigrated(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}
