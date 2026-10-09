package discovery

import (
	"testing"

	"litepan/internal/discover/ddb"
)

// 本文件钉住 0036 迁移在**旧库**上的另一半：补列。
//
// 迁移 0036 本身在旧库上只能空转（CREATE TABLE IF NOT EXISTS），
// 新列是由 EnsureDiscoverySchema 的 GORM AutoMigrate 补上的。
// 两半拼起来才是完整升级路径，所以这条测试放在 discovery 包里 ——
// AutoMigrate 的调用方在这边，放 store 包里就只能测到空转那一半。

// legacySubscriptionTableDDL T05 之前的 discovery_subscriptions 表结构。
// 与 internal/store/zz_mig0036_test.go 里的那份保持一致（历史表由 AutoMigrate 建，
// 迁移文件建不出来，只能手抄）。
const legacySubscriptionTableDDL = `CREATE TABLE discovery_subscriptions (
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

// TestAutoMigrateAddsSearchConnectorColumns 旧表补列 + 默认值。
//
// 默认值是这里最要紧的断言：AutoMigrate 用 ALTER TABLE ADD COLUMN 补列时，
// 必须给存量行填上 'tgto123'。填不上就意味着老订阅的 search_sources 是空串，
// 而空串在运行时回落全局默认 —— 看着没差别，可一旦有人改了全局默认，
// 全站老订阅会跟着一起变。所以存量行必须**各自钉死**在 tgto123 上。
func TestAutoMigrateAddsSearchConnectorColumns(t *testing.T) {
	setupLedgerTest(t, nil)

	// setupLedgerTest 已经用当前结构体建过表了，先删掉再按 T05 之前的结构重建，
	// 模拟「老实例的库里那张表」。
	if err := ddbDbExec(`DROP TABLE IF EXISTS discovery_subscriptions`); err != nil {
		t.Fatalf("清理表失败：%v", err)
	}
	if err := ddbDbExec(legacySubscriptionTableDDL); err != nil {
		t.Fatalf("建老表失败：%v", err)
	}
	if err := ddbDbExec(
		`INSERT INTO discovery_subscriptions (entity_key, title, media_type, tmdb_id)
		 VALUES ('tmdb:tv:1396', '老订阅', 'tv', 1396)`); err != nil {
		t.Fatalf("写入老数据失败：%v", err)
	}

	if err := EnsureDiscoverySchema(); err != nil {
		t.Fatalf("AutoMigrate 失败：%v", err)
	}

	for _, col := range []string{"search_sources", "resolution", "effect", "min_file_size_mb", "max_file_size_mb"} {
		if !tableHasColumn(t, "discovery_subscriptions", col) {
			t.Fatalf("AutoMigrate 未补出列 %s", col)
		}
	}

	var sources string
	if err := ddbDbQueryRow(
		`SELECT search_sources FROM discovery_subscriptions WHERE entity_key = ?`,
		[]any{"tmdb:tv:1396"}, &sources); err != nil {
		t.Fatalf("读取老行失败：%v", err)
	}
	if sources != "tgto123" {
		t.Fatalf("存量行补列后必须落在 tgto123，实际 %q", sources)
	}

	// 闸门四列没有 DEFAULT（NULL），这才是「没配过」的诚实表示 ——
	// 硬塞 ''/0 进去反而会掩盖「这列到底是用户配的还是补列补出来的」。
	// 读的时候必须经结构体，让 GORM 把 NULL 归成 Go 零值。
	var sub DiscoverySubscription
	if err := ddb.Db.Where("entity_key = ?", "tmdb:tv:1396").First(&sub).Error; err != nil {
		t.Fatalf("读老行失败：%v", err)
	}
	if sub.Resolution != "" || sub.Effect != "" || sub.MinFileSizeMB != 0 || sub.MaxFileSizeMB != 0 {
		t.Fatalf("闸门列零值应为「不限制」，实际 %q/%q/%d/%d",
			sub.Resolution, sub.Effect, sub.MinFileSizeMB, sub.MaxFileSizeMB)
	}
	// 闸门确实是关的：老订阅不该被任何画质/体积条件挡住。
	if gateForSubscription(&sub).Enabled() {
		t.Fatalf("老订阅补列后闸门应处于关闭状态，实际 %q/%d/%d",
			sub.Resolution, sub.MinFileSizeMB, sub.MaxFileSizeMB)
	}
	// 而且它解析出的搜索源就是 tgto123 —— 存量行为不变的最后一道断言。
	if keys := subscriptionSearchSources(&sub); len(keys) != 1 || keys[0] != KeyTgto123 {
		t.Fatalf("老订阅的搜索源应只有 tgto123，实际 %v", keys)
	}

	// 索引也必须一起补上：SearchSources 的 gorm 标签带 index。
	if !tableHasIndex(t, "discovery_subscriptions", "idx_discovery_subscriptions_search_sources") {
		t.Fatalf("AutoMigrate 未建出搜索源索引")
	}
}

// TestAutoMigrateIsIdempotentOnCurrentSchema 新装之后每次启动都会跑 AutoMigrate，
// 不能因为表已经是新的就报错或改数据。
func TestAutoMigrateIsIdempotentOnCurrentSchema(t *testing.T) {
	setupLedgerTest(t, nil)
	if err := EnsureDiscoverySchema(); err != nil {
		t.Fatalf("首次 AutoMigrate 失败：%v", err)
	}
	if err := ddbDbExec(
		`INSERT INTO discovery_subscriptions (entity_key, title, media_type, search_sources)
		 VALUES ('tmdb:movie:0', '新订阅', 'movie', 'tgto123,hdhive')`); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if err := EnsureDiscoverySchema(); err != nil {
		t.Fatalf("二次 AutoMigrate 失败：%v", err)
	}
	var sources string
	if err := ddbDbQueryRow(
		`SELECT search_sources FROM discovery_subscriptions WHERE entity_key = ?`,
		[]any{"tmdb:movie:0"}, &sources); err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if sources != "tgto123,hdhive" {
		t.Fatalf("二次 AutoMigrate 不该动已有数据，实际 %q", sources)
	}
}

// ddbDbExec / ddbDbQueryRow 是直接操作测试库的小工具。
func ddbDbExec(sql string) error { return ddb.Db.Exec(sql).Error }

// ddbDbQueryRow 用 ? 占位（参数按序放在 args 里），因为 GORM 的 Raw().Row()
// 不支持具名绑定。
func ddbDbQueryRow(query string, args []any, dest ...any) error {
	return ddb.Db.Raw(query, args...).Row().Scan(dest...)
}

func tableHasColumn(t *testing.T, table, column string) bool {
	t.Helper()
	var n int
	if err := ddbDbQueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		[]any{table, column}, &n); err != nil {
		t.Fatalf("检查列 %s 失败：%v", column, err)
	}
	return n > 0
}

func tableHasIndex(t *testing.T, table, index string) bool {
	t.Helper()
	var n int
	if err := ddbDbQueryRow(
		`SELECT COUNT(*) FROM pragma_index_list(?) WHERE name = ?`,
		[]any{table, index}, &n); err != nil {
		t.Fatalf("检查索引 %s 失败：%v", index, err)
	}
	return n > 0
}
