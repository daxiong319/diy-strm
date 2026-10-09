package store_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// T16 · RSS 订阅迁移 0044 的验收 ⑨：空库 + 有数据旧库都跑通。
//
// 旧库那半边是有意义的：新表是**增量**创建的，不能碰到任何已有表 ——
// 这一组测试里刻意往 discovery_channels / discovery_subscriptions 塞真实行，
// 跑完迁移后逐表点数，任何一张被动到都直接红。

func TestRSSMigrationEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库跑迁移失败: %v", err)
	}

	// 两张表都必须建出来，且是空表。
	for _, table := range []string{"rss_subscription_sources", "rss_subscription_history"} {
		var n int
		if err := db.ReadHandle().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("空库迁移后 %s 不可查询: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("空库迁移后 %s 有 %d 行，期望 0", table, n)
		}
	}

	// guid 唯一约束必须真的生效 —— 这是 RSS 去重的唯一依据。
	// 没有它，同一条目会被反复入库，去重整个失效。
	if _, err := db.WriteHandle().ExecContext(ctx,
		`INSERT INTO rss_subscription_history (source_id, guid) VALUES (1, 'g-1')`); err != nil {
		t.Fatalf("插入历史行失败: %v", err)
	}
	if _, err := db.WriteHandle().ExecContext(ctx,
		`INSERT INTO rss_subscription_history (source_id, guid) VALUES (2, 'g-1')`); err == nil {
		t.Fatal("重复 guid 插入成功了 —— uq_rss_subscription_history_guid 没生效，RSS 去重会整个失效")
	}
}

func TestRSSMigrationWithExistingData(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 先只跑到 0043（RSS 迁移之前），制造一个「有数据的旧库」。
	// 直接手工建两张旧表塞行更省事，也更贴近「存量用户」的形态。
	mustExec(t, db, `CREATE TABLE discovery_channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT, channel_name TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '', last_post_id TEXT NOT NULL DEFAULT '',
		catchup_checkpoints TEXT NOT NULL DEFAULT '')`)
	mustExec(t, db, `CREATE TABLE discovery_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL DEFAULT '',
		tmdb_id INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT '')`)
	for i := 1; i <= 3; i++ {
		mustExec(t, db, `INSERT INTO discovery_channels (channel_name, provider, last_post_id) VALUES (?, '123', ?)`,
			"legacy-channel", "post-"+string(rune('0'+i)))
	}
	for i := 1; i <= 2; i++ {
		mustExec(t, db, `INSERT INTO discovery_subscriptions (title, tmdb_id) VALUES (?, ?)`, "legacy-sub", 550+i)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("有数据旧库跑迁移失败: %v", err)
	}

	// 存量数据一行都不能少也不能变。
	var chCount, subCount int
	if err := db.ReadHandle().QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_channels`).Scan(&chCount); err != nil {
		t.Fatalf("读 discovery_channels 失败: %v", err)
	}
	if err := db.ReadHandle().QueryRowContext(ctx, `SELECT COUNT(*) FROM discovery_subscriptions`).Scan(&subCount); err != nil {
		t.Fatalf("读 discovery_subscriptions 失败: %v", err)
	}
	if chCount != 3 || subCount != 2 {
		t.Fatalf("存量数据被迁移改动: channels=%d（期望 3）subscriptions=%d（期望 2）", chCount, subCount)
	}
	var lastPostID string
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT last_post_id FROM discovery_channels ORDER BY id LIMIT 1`).Scan(&lastPostID); err != nil {
		t.Fatalf("读 last_post_id 失败: %v", err)
	}
	if lastPostID != "post-1" {
		t.Fatalf("存量字段被改动: last_post_id=%q，期望 %q", lastPostID, "post-1")
	}

	// 新表建出来且可用。
	if _, err := db.WriteHandle().ExecContext(ctx,
		`INSERT INTO rss_subscription_sources (name, rss_url) VALUES (?, ?)`,
		"Mikan", "https://mikanani.me/RSS/MyBangumi?xmltype=2"); err != nil {
		t.Fatalf("有数据旧库里插入 RSS 源失败: %v", err)
	}
}

// TestRSSSourceDefaultsMatchReferenceDefaults 逐列断言建表默认值。
//
// 照搬 参考实现 模型的默认空串/1 是有意义的：这些列要么参与等值比较
// （enabled=1 表示启用），要么被 UI 直接读出来显示（不写默认会显示成 NULL）。
// 一旦某列默认值漂了，源列表会显示 NULL、启用筛选会把新源漏掉。
func TestRSSSourceDefaultsMatchReferenceDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 只给 name / rss_url 两列，其余全走默认。
	if _, err := db.WriteHandle().ExecContext(ctx,
		`INSERT INTO rss_subscription_sources (name, rss_url) VALUES ('默认源', 'https://example.test/feed')`); err != nil {
		t.Fatalf("插入失败: %v", err)
	}

	want := map[string]any{
		"target_path":   "",
		"storage":       "",
		"media_server":  "",
		"poster_url":    "",
		"include_regex": "",
		"exclude_regex": "",
		"media_type":    "tv",
		"action":        "transfer",
		"enabled":       int64(1),
		"last_sync_at":  nil,
		"last_status":   "",
		"last_message":  "",
	}
	for column, expected := range want {
		var got any
		if err := db.ReadHandle().QueryRowContext(ctx,
			`SELECT `+column+` FROM rss_subscription_sources WHERE name = '默认源'`).Scan(&got); err != nil {
			t.Fatalf("读列 %s 失败: %v", column, err)
		}
		if got != expected {
			t.Fatalf("列 %s 默认值 = %#v，期望 %#v —— 与 参考实现 模型或本任务的口径不一致", column, got, expected)
		}
	}
}

// TestRSSHistoryDefaultsMatchReferenceDefaults 历史表默认值同样逐列断言。
func TestRSSHistoryDefaultsMatchReferenceDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.WriteHandle().ExecContext(ctx,
		`INSERT INTO rss_subscription_history (source_id, guid) VALUES (1, 'g-1')`); err != nil {
		t.Fatalf("插入失败: %v", err)
	}

	want := map[string]any{
		"source_name":  "",
		"title":        "",
		"link":         "",
		"download_url": "",
		"target_path":  "",
		"status":       "",
		"message":      "",
		"published_at": nil,
	}
	for column, expected := range want {
		var got any
		if err := db.ReadHandle().QueryRowContext(ctx,
			`SELECT `+column+` FROM rss_subscription_history WHERE guid = 'g-1'`).Scan(&got); err != nil {
			t.Fatalf("读列 %s 失败: %v", column, err)
		}
		if got != expected {
			t.Fatalf("列 %s 默认值 = %#v，期望 %#v", column, got, expected)
		}
	}
}

func mustExec(t *testing.T, db *store.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.WriteHandle().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

// 引用 domain 保证本文件与仓储层的常量同源（下面几条用到哨兵口径时不会漂）。
var _ = domain.PlayTrafficEnabledSinceDay
