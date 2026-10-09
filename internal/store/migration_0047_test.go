package store_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// T12 · 通知场景化与 Webhook 补发 迁移 0047 的验收 ⑥⑨：
// 空库 + 有数据旧库都跑通，且**存量渠道配置一个字节都不能变**。
//
// 旧库那半边是本任务最容易出事的地方：场景订阅（scenes 键）虽然是新功能，
// 但它住在 notify_channels.config 这个 JSON 里，迁移必须完全不去碰那张表 ——
// 碰了就是存量渠道配置被改写，直接违反硬约束「存量配置必须能加载」。

func TestNotifyRetryMigrationEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("空库跑迁移失败: %v", err)
	}

	var n int
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notify_retry_queue`).Scan(&n); err != nil {
		t.Fatalf("空库迁移后 notify_retry_queue 不可查询: %v", err)
	}
	if n != 0 {
		t.Fatalf("空库迁移后 notify_retry_queue 有 %d 行，期望 0", n)
	}
}

func TestNotifyRetryMigrationKeepsExistingChannelsIntact(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 先把迁移跑到 0046（T12 迁移之前），制造一个**结构完整**的存量库。
	// 不能手工 CREATE TABLE 冒充旧库 —— 那样 0002 会因表已存在而报错，
	// 测到的其实是「迁移能否跑在残缺 schema 上」，不是「存量数据安不安全」。
	migrateUpToTo(t, db, 46)

	// 塞入存量行。config 里**故意不含 scenes 键** —— 这正是存量渠道的形态。
	const legacyWebhookConfig = `{"url":"https://legacy.test/hook","method":"POST"}`
	const legacyTelegramConfig = `{"bot_token":"123:abc","chat_id":42}`
	mustExec(t, db, `INSERT INTO notify_channels (type, name, config, enabled) VALUES ('webhook','旧 Webhook',?,1)`, legacyWebhookConfig)
	mustExec(t, db, `INSERT INTO notify_channels (type, name, config, enabled) VALUES ('telegram','旧 Telegram',?,0)`, legacyTelegramConfig)
	mustExec(t, db, `INSERT INTO notifications (level, category, title, message) VALUES ('success','cas','CAS 自动转存成功','ok')`)

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("有数据旧库跑迁移失败: %v", err)
	}

	// 存量渠道：config 文本、enabled 必须逐字不变。
	var gotCfg string
	var gotEnabled int
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT config, enabled FROM notify_channels WHERE name = '旧 Webhook'`).Scan(&gotCfg, &gotEnabled); err != nil {
		t.Fatalf("读存量 webhook 渠道失败: %v", err)
	}
	if gotCfg != legacyWebhookConfig {
		t.Fatalf("存量渠道 config 被改写: got %q want %q —— scenes 键必须靠「缺失即全订阅」实现，不能靠改写存量配置", gotCfg, legacyWebhookConfig)
	}
	if gotEnabled != 1 {
		t.Fatalf("存量渠道 enabled 被改写: got %d want 1", gotEnabled)
	}
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT config FROM notify_channels WHERE name = '旧 Telegram'`).Scan(&gotCfg); err != nil {
		t.Fatalf("读存量 telegram 渠道失败: %v", err)
	}
	if gotCfg != legacyTelegramConfig {
		t.Fatalf("存量 telegram 渠道 config 被改写: got %q want %q", gotCfg, legacyTelegramConfig)
	}

	// 存量通知行不能被迁移波及。
	var nCount int
	if err := db.ReadHandle().QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&nCount); err != nil {
		t.Fatalf("读 notifications 失败: %v", err)
	}
	if nCount != 1 {
		t.Fatalf("存量通知被迁移改动: %d 行，期望 1", nCount)
	}

	// 关键落点：存量渠道（无 scenes 键）必须被解析成「订阅全部场景」。
	// ⚠️ 这个断言盯的是「解析函数的取键位置」—— 若实现误去读整个 config
	// 文本而不是顶层 scenes 键，url/method 的值会被当成场景 ID。
	if scenes, any := domain.ParseSceneSubscriptions(legacyWebhookConfig); any {
		t.Fatalf("从存量 config 解析出了场景 %v —— scenes 必须是顶层键，不能混进 url/method 的值里", scenes)
	}
}

// TestNotifyRetryQueueDefaults 逐列断言建表默认值。
//
// 这些默认值里 status='pending' 与 attempts=0 是状态机的地基：
// 少写一个默认值，Enqueue 就会漏掉一列，worker 捞不到记录且不报错。
func TestNotifyRetryQueueDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mustExec(t, db, `INSERT INTO notify_retry_queue (channel_type, target_url) VALUES ('webhook','https://x.test/h')`)

	want := map[string]any{
		"event_scene":    "",
		"channel_name":   "",
		"channel_config": "{}",
		"title":          "",
		"content":        "",
		"tone":           "",
		"attempts":       int64(0),
		"last_error":     "",
		"status":         "pending",
		"next_retry_at":  nil,
	}
	for column, expected := range want {
		var got any
		if err := db.ReadHandle().QueryRowContext(ctx,
			`SELECT `+column+` FROM notify_retry_queue WHERE id = 1`).Scan(&got); err != nil {
			t.Fatalf("读列 %s 失败: %v", column, err)
		}
		if got != expected {
			t.Fatalf("列 %s 默认值 = %#v，期望 %#v", column, got, expected)
		}
	}
}

// TestNotifyRetryMigrationIndexes 断言两个索引都在。
//
// idx_notify_retry_pending(status, next_retry_at) 是 worker 每轮排期扫描
// 走的索引；没有它就是每轮全表扫，补发记录多了之后会随通知量线性变慢。
func TestNotifyRetryMigrationIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows, err := db.ReadHandle().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='notify_retry_queue'`)
	if err != nil {
		t.Fatalf("读索引失败: %v", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描索引名失败: %v", err)
		}
		found[n] = true
	}
	for _, want := range []string{"idx_notify_retry_pending", "idx_notify_retry_created"} {
		if !found[want] {
			t.Fatalf("索引 %s 不存在，现有索引: %v", want, found)
		}
	}
}

// TestNotifyRetryRepoLifecycle 走真 SQL 覆盖仓储的状态流转。
//
// 这组测试是 T16 留下的教训的反面：光用内存替身测退避不够，仓储里的
// `next_retry_at IS NOT NULL` 这类条件写错，替身根本发现不了。
func TestNotifyRetryRepoLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := store.New(db)
	now := time.Now().UTC()

	// 入队时 status 必须显式给 pending；NextRetryAt 设为 1 分钟后。
	id, err := s.NotifyRetries.Enqueue(ctx, domain.NotifyRetryEntry{
		EventScene:    domain.SceneStrm,
		ChannelType:   "webhook",
		ChannelName:   "运维告警",
		TargetURL:     "https://hook.test/notify",
		ChannelConfig: `{"url":"https://hook.test/notify","method":"POST"}`,
		Title:         "STRM 扫描部分失败",
		Content:       "3 个任务失败",
		Tone:          "warn",
		Status:        domain.NotifyRetryStatusPending,
		NextRetryAt:   now.Add(time.Minute),
		LastError:     "webhook HTTP 500",
	})
	if err != nil {
		t.Fatalf("Enqueue 失败: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Enqueue 返回 id=%d，期望正数", id)
	}

	// 还没到点时 Due 必须捞不到（这是退避能被真正测出来的关键）。
	due, err := s.NotifyRetries.Due(ctx, now, 10)
	if err != nil {
		t.Fatalf("Due 失败: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("未到点的记录被捞出来了: %d 条 —— 退避间隔等于失效", len(due))
	}

	// 到点后捞到，且字段完整。
	due, err = s.NotifyRetries.Due(ctx, now.Add(2*time.Minute), 10)
	if err != nil {
		t.Fatalf("Due 失败: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("到点记录数 = %d，期望 1", len(due))
	}
	got := due[0]
	if got.EventScene != domain.SceneStrm || got.TargetURL != "https://hook.test/notify" ||
		got.Title != "STRM 扫描部分失败" || got.Tone != "warn" || got.Attempts != 0 {
		t.Fatalf("捞出的记录字段不符: %+v", got)
	}

	// 失败一次：attempts+1，下次时间推后，状态仍 pending。
	next := now.Add(11 * time.Minute)
	if err := s.NotifyRetries.UpdateResult(ctx, id, 1, next, "webhook HTTP 502", domain.NotifyRetryStatusPending); err != nil {
		t.Fatalf("UpdateResult 失败: %v", err)
	}
	e, err := s.NotifyRetries.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if e.Attempts != 1 || e.LastError != "webhook HTTP 502" || e.Status != domain.NotifyRetryStatusPending {
		t.Fatalf("失败后状态不符: attempts=%d last=%q status=%q", e.Attempts, e.LastError, e.Status)
	}
	if e.NextRetryAt.Before(next.Add(-time.Second)) {
		t.Fatalf("下次重试时间没推后: %v", e.NextRetryAt)
	}

	// 标失败态：管理台可见，且 Due 不再捞它。
	if err := s.NotifyRetries.UpdateResult(ctx, id, 5, time.Time{}, "webhook HTTP 500", domain.NotifyRetryStatusFailed); err != nil {
		t.Fatalf("UpdateResult(failed) 失败: %v", err)
	}
	due, err = s.NotifyRetries.Due(ctx, now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("Due 失败: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("failed 记录仍被排期扫描捞到: %d 条", len(due))
	}

	// 手动重投：attempts 归零、状态回 pending、错误清空。
	if err := s.NotifyRetries.Redrive(ctx, id, now); err != nil {
		t.Fatalf("Redrive 失败: %v", err)
	}
	e, err = s.NotifyRetries.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if e.Status != domain.NotifyRetryStatusPending || e.Attempts != 0 || e.LastError != "" {
		t.Fatalf("重投后状态不符: status=%q attempts=%d last=%q", e.Status, e.Attempts, e.LastError)
	}

	// 重复重投必须被拒：pending 状态上 Redrive 会把正在退避的记录 attempts 归零。
	if err := s.NotifyRetries.Redrive(ctx, id, now); err == nil {
		t.Fatal("对 pending 记录重投成功了 —— 会免费插队，退避上限形同虚设")
	}

	// 补发成功 → sent，且是终态。
	if err := s.NotifyRetries.UpdateResult(ctx, id, 1, time.Time{}, "", domain.NotifyRetryStatusSent); err != nil {
		t.Fatalf("UpdateResult(sent) 失败: %v", err)
	}
	counts, err := s.NotifyRetries.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts 失败: %v", err)
	}
	if counts[domain.NotifyRetryStatusSent] != 1 || counts[domain.NotifyRetryStatusPending] != 0 {
		t.Fatalf("状态计数不符: %v", counts)
	}

	// 分页 + 按状态筛选。
	items, total, err := s.NotifyRetries.List(ctx, domain.NotifyRetryQuery{Status: domain.NotifyRetryStatusSent})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("List(sent) = %d 条 / 总数 %d，期望 1/1", len(items), total)
	}
	if _, total, err = s.NotifyRetries.List(ctx, domain.NotifyRetryQuery{Status: domain.NotifyRetryStatusFailed}); err != nil {
		t.Fatalf("List(failed) 失败: %v", err)
	} else if total != 0 {
		t.Fatalf("List(failed) 总数 = %d，期望 0", total)
	}

	// 按状态清理；清空状态清全部。
	n, err := s.NotifyRetries.Clear(ctx, domain.NotifyRetryStatusSent)
	if err != nil || n != 1 {
		t.Fatalf("Clear(sent) = %d, %v，期望 1", n, err)
	}
	n, err = s.NotifyRetries.Clear(ctx, "")
	if err != nil {
		t.Fatalf("Clear(全部) 失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("Clear(全部) 删了 %d 行，期望 0（已清空）", n)
	}
}

// TestNotifyRetryGetMissingReturnsNotFound 缺失记录必须给可识别的 NOT_FOUND，
// 管理台才能把「记录被别的会话删了」和「数据库坏了」区分开。
func TestNotifyRetryGetMissingReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := store.New(db)
	_, err = s.NotifyRetries.Get(ctx, 4242)
	if err == nil {
		t.Fatal("取不存在的补发记录没有报错")
	}
	var appErr *domain.AppError
	if !errorsAs(err, &appErr) || appErr.Code != domain.CodeNotFound {
		t.Fatalf("错误码 = %v，期望 NOT_FOUND（err=%v）", err, err)
	}
}

// migrateUpToTo 把 schema 跑到指定版本为止（含），供「存量旧库」用例使用。
//
// 为什么需要它：`db.Migrate()` 一口气跑到最新版本，没有「只跑到 0046」的入口。
// 而手工 CREATE TABLE 冒充旧库是行不通的 —— 它只造出本用例用到的几张表，
// 0001~0046 里其余迁移会因表已存在直接报错，测到的就变成了
// 「迁移能否跑在残缺 schema 上」，与「存量数据安不安全」完全是两回事。
//
// 做法：先跑一次完整迁移（建出全部表），把 schema_migrations 里 >maxVersion
// 的记账行删掉，并把 0047 建的表删掉 —— 恢复成「0047 还没发生过」的状态。
// 这比逐条重放 0046 个 SQL 更贴近真实的升级路径（真实路径也是先有完整
// schema，再被新迁移改动）。
func migrateUpToTo(t *testing.T, db *store.DB, maxVersion int) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("预热迁移失败: %v", err)
	}
	if _, err := db.WriteHandle().ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version > ?`, maxVersion); err != nil {
		t.Fatalf("回退 schema_migrations 失败: %v", err)
	}
	for _, v := range []int{47} {
		if v > maxVersion {
			mustExec(t, db, `DROP TABLE IF EXISTS notify_retry_queue`)
			mustExec(t, db, `DROP INDEX IF EXISTS idx_notify_retry_pending`)
			mustExec(t, db, `DROP INDEX IF EXISTS idx_notify_retry_created`)
		}
	}
	// 0048 是给 media_upgrade_records 加列的 ALTER，回退必须把列摘掉 ——
	// 否则「旧库升级」其实是在一个已经带新列的库上跑，测不到 ALTER 本身，
	// 症状是 ALTER 在真实旧库上失败而这个用例照样绿。
	for _, v := range []int{48} {
		if v > maxVersion {
			mustExec(t, db, `ALTER TABLE media_upgrade_records DROP COLUMN reject_reasons`)
			mustExec(t, db, `ALTER TABLE media_upgrade_records DROP COLUMN rule_fingerprint`)
		}
	}
}
func errorsAs(err error, target **domain.AppError) bool {
	for e := err; e != nil; {
		if a, ok := e.(*domain.AppError); ok {
			*target = a
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
