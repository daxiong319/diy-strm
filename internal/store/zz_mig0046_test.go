package store

// T27 C-8 · 洗版规则「适用分类」列的迁移守卫（0046）。
//
// 0046 与其它迁移的性质不同：它不是建表，而是给一张**已存在的表**
// （media_upgrade_rules，由 0037 建）加一列。所以它的验收重点不是
// "表在不在"，而是三件更容易出事的事：
//
//  1. 存量库上能跑（表已存在 + 已有数据）；
//  2. 幂等（migrate 重复执行不炸）；
//  3. 新列有默认值 —— 存量规则行升级后 category_scope 必须是空串
//     而不是 NULL。空串在代码里是"不按分类筛选"，NULL 则会让
//     ScanRuleCategoryScope 的零值判断和"这列不存在"两种情况
//     长得一样，而查不出来的 NULL 还会让 GORM 写出零值把语义弄丢。

import (
	"context"
	"path/filepath"
	"testing"
)

const upgradeCategoryScope0046Table = "media_upgrade_rules"

func openUpgradeCategoryScopeTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "upgrade.db")})
	if err != nil {
		t.Fatalf("open upgrade db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestUpgradeCategoryScope0046AddsColumn 验收：列存在且默认值是空串。
func TestUpgradeCategoryScope0046AddsColumn(t *testing.T) {
	db := openUpgradeCategoryScopeTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 0046 是 ALTER，依赖 0037 建的那张表。先确认底表在，
	// 否则 columnExists 返 false 会被读成"0046 没跑"，
	// 而真实原因可能是 0037 整个没跑。
	if !tableExistsByName(ctx, db, upgradeCategoryScope0046Table) {
		t.Fatalf("表 %s 不存在：0046 依赖 0037 建的那张表", upgradeCategoryScope0046Table)
	}
	if !columnExists(ctx, db, upgradeCategoryScope0046Table, "category_scope") {
		t.Fatal("media_upgrade_rules 没有 category_scope 列 —— 洗版规则存不下适用分类")
	}
	// 存量规则行升级后 category_scope 必须能读成空串。
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO `+upgradeCategoryScope0046Table+`(name) VALUES ('存量规则')`); err != nil {
		t.Fatalf("插入存量规则失败: %v", err)
	}
	got := readOne(ctx, db, `SELECT category_scope FROM `+upgradeCategoryScope0046Table+` WHERE name='存量规则'`)
	if got != "" {
		t.Fatalf("存量行的 category_scope = %q，期望空串（=不按分类筛选）", got)
	}
}

// TestUpgradeCategoryScope0046IsIdempotent 验收：重复 migrate 不炸。
//
// ALTER TABLE ADD COLUMN 在 SQLite 里重复执行会报 "duplicate column name"。
// 0045 用 CREATE TABLE IF NOT EXISTS 天然幂等，0046 没有这层保护，
// 所以必须实测一次重复 migrate —— 迁移层是否带版本号去重，
// 决定这条测试是在测迁移还是在测"迁移框架会挡住重复"。
func TestUpgradeCategoryScope0046IsIdempotent(t *testing.T) {
	db := openUpgradeCategoryScopeTestDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("首次 migrate: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("重复 migrate 失败: %v", err)
	}
}

// TestUpgradeCategoryScope0046KeepsExistingRows 验收（存量兼容）：
// 升级前就在库里的规则行，升级后其余字段一个都不许变。
//
// 这是迁移里最容易出事又最难发现的一类：ALTER 顺手把某列的
// NOT NULL DEFAULT 改了，于是升级后那些行的值在没人写代码的情况下变了。
func TestUpgradeCategoryScope0046KeepsExistingRows(t *testing.T) {
	db := openUpgradeCategoryScopeTestDB(t)
	ctx := context.Background()
	// 只跑到 0037（跳过 0046），模拟"升级前"的库。
	if err := migrateUpTo(ctx, db, 37); err != nil {
		t.Fatalf("迁移到 0037: %v", err)
	}
	if _, err := db.write.ExecContext(ctx,
		`INSERT INTO `+upgradeCategoryScope0046Table+
			`(name, source, library_root, candidate_roots, min_resolution, min_channels,
			  require_subtitle, max_records_per_series, loser_action, move_dir,
			  group_priority, wash_rules, enabled, builtin)
		 VALUES ('旧规则','local','/media','/cand',2160,6,1,3,'keep','/move','a,b','[]',1,1)`); err != nil {
		t.Fatalf("写入旧规则失败: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("升级到 0046: %v", err)
	}

	checks := map[string]string{
		"name":                   "旧规则",
		"library_root":           "/media",
		"candidate_roots":        "/cand",
		"max_records_per_series": "3",
		"loser_action":           "keep",
		"move_dir":               "/move",
		"group_priority":         "a,b",
		"builtin":                "1",
	}
	for col, want := range checks {
		if got := readOne(ctx, db, `SELECT `+col+` FROM `+upgradeCategoryScope0046Table+` WHERE name='旧规则'`); got != want {
			t.Errorf("升级后 %s = %q，期望 %q（ALTER 不该动存量数据）", col, got, want)
		}
	}
	if got := readOne(ctx, db, `SELECT category_scope FROM `+upgradeCategoryScope0046Table+` WHERE name='旧规则'`); got != "" {
		t.Errorf("旧规则的 category_scope = %q，期望空串", got)
	}
}
