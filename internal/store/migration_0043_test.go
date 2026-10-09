package store_test

import (
	"context"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// 验收 ⑪：0043 迁移在**有存量数据的旧库**上跑通。
//
// 新建空库跑通是 TestMigrateAppliesAllMigrationsOnce 覆盖的；这里补另一半：
// 0028 以来的 Emby 播放记录必须原样保留，否则观影报告里就少了这段历史
// （验收第 9 条）。做法是真的开一个库、跑到 0042 为止、塞数据、再跑 0043。
func TestMigration0043RunsOnLegacyDBWithRows(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 跑到「0042 为止」：先手工应用 0028 那张表 + 塞存量数据，
	// 再把 0043 之前的迁移标成已应用，最后让 Migrate 只补 0043。
	if _, err := db.WriteHandle().ExecContext(ctx, `CREATE TABLE playback_records (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		rule_id     TEXT NOT NULL DEFAULT '1',
		user_id     TEXT NOT NULL DEFAULT '',
		client      TEXT NOT NULL DEFAULT '',
		device_id   TEXT NOT NULL DEFAULT '',
		item_name   TEXT NOT NULL DEFAULT '',
		strm_path   TEXT NOT NULL DEFAULT '',
		provider    TEXT NOT NULL DEFAULT '',
		playback_at TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("建 0028 的表: %v", err)
	}
	const legacyRows = 3
	for i := 0; i < legacyRows; i++ {
		if _, err := db.WriteHandle().ExecContext(ctx,
			`INSERT INTO playback_records(rule_id, user_id, item_name, provider, playback_at)
			 VALUES('1', ?, ?, 'emby', '2024-05-0' || ? || 'T10:00:00Z')`,
			"emby-user-a", "Legacy.MKV", string(rune('1'+i))); err != nil {
			t.Fatalf("塞 0028 存量数据: %v", err)
		}
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("在有存量数据的旧库上跑迁移失败: %v", err)
	}

	// ① 存量行必须一条不少、user_id 原样保留（双标识设计的关键前提）。
	var got, dup int
	if err := db.ReadHandle().QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_records`).Scan(&got); err != nil {
		t.Fatalf("数存量行: %v", err)
	}
	if got != legacyRows {
		t.Fatalf("存量记录数 = %d，期望 %d（0043 必须走 ALTER 而不是重建）", got, legacyRows)
	}
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playback_records WHERE user_id = 'emby-user-a'`).Scan(&dup); err != nil {
		t.Fatalf("查 user_id: %v", err)
	}
	if dup != legacyRows {
		t.Fatalf("user_id = 'emby-user-a' 的行数 = %d，期望 %d（TEXT 标识不能被 ALTER 破坏）", dup, legacyRows)
	}

	// ② 存量行的 app_user_id 应为 0（未知），**不回填** ——
	//    回填等于补算，会破坏「统计从启用起累计」的口径。
	var unknown int
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playback_records WHERE app_user_id = 0`).Scan(&unknown); err != nil {
		t.Fatalf("查 app_user_id: %v", err)
	}
	if unknown != legacyRows {
		t.Fatalf("app_user_id = 0 的存量行 = %d，期望 %d（迁移不得回填 app_source/app_user_id）", unknown, legacyRows)
	}

	// ③ 新链路写入：双标识并存，老列用 Emby 字符串、新列用 RBAC 整数。
	if _, err := db.ReadHandle().ExecContext(ctx,
		`UPDATE playback_records SET app_user_id = 42, request_type = 'stream', watched_seconds = 600
		 WHERE id = (SELECT MIN(id) FROM playback_records)`); err != nil {
		t.Fatalf("写新列失败: %v", err)
	}

	// ④ play_traffic_daily 建出来了，且首启哨兵行可用。
	var dayCount int
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM play_traffic_daily WHERE day = ?`,
		domain.PlayTrafficEnabledSinceDay).Scan(&dayCount); err != nil {
		t.Fatalf("play_traffic_daily 没建出来: %v", err)
	}
	t.Logf("首启哨兵行数 = %d（0 表示还没启用监控，符合预期）", dayCount)
}
