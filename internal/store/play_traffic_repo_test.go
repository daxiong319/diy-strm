package store_test

import (
	"context"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// play_traffic_daily 仓储的**真 SQL** 行为。
//
// 为什么单独一个文件：T11 的 playmonitor 侧测试全用 fakeTrafficRepo，
// 绕开了真 SQL，所以「ON CONFLICT DO NOTHING 而不是 DO UPDATE」这条
// 防御零测试保护。实测：把它改成 DO UPDATE，没有任何测试变红 ——
// 而这条防御一破，「统计从启用起累计、不补算」的口径就整条失效
// （每次重启下界被推成今天，历史凭空消失）。
//
// 所以这几个用例一律走真 SQLite，不用 fake。
// ─────────────────────────────────────────────────────────────────────────────

// newSentinelStore 开一个跑完迁移的空库，并写下首次启用时刻。
// 返回 *store.DB 是因为「哨兵行唯一」只能靠 COUNT(*) 证明，
// 仓储接口不暴露那个查询。
func newSentinelStore(ctx context.Context, t *testing.T, ensureTimes int) (*store.DB, *store.Store) {
	t.Helper()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := store.New(db)
	for i := 0; i < ensureTimes; i++ {
		if err := s.PlayTraffic.EnsureEnabledSince(ctx); err != nil {
			t.Fatalf("第 %d 次 EnsureEnabledSince 失败: %v", i+1, err)
		}
	}
	return db, s
}

func sentinelCount(ctx context.Context, t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	if err := db.ReadHandle().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM play_traffic_daily WHERE day = ?`,
		domain.PlayTrafficEnabledSinceDay).Scan(&n); err != nil {
		t.Fatalf("数哨兵行失败: %v", err)
	}
	return n
}

// TestPlayTrafficEnsureEnabledSinceIsWriteOnce 首次启用时刻**只写一次**。
//
// 这是 T11 方案的核心防御：EnsureEnabledSince 每次服务启动都会调，
// 但它必须只在第一次写入。第二次调用若覆盖了 updated_at，
// 观影报告的统计下界就变成今天，之前累计的播放全部消失。
func TestPlayTrafficEnsureEnabledSinceIsWriteOnce(t *testing.T) {
	ctx := context.Background()
	_, s := newSentinelStore(ctx, t, 1)

	first, found, err := s.PlayTraffic.EnabledSince(ctx)
	if err != nil {
		t.Fatalf("首次读取 EnabledSince 失败: %v", err)
	}
	if !found {
		t.Fatal("首次写入后读不到启用时刻")
	}

	// 第二次：**必须不改**。这里睡一小会儿，保证 updated_at 在
	// RFC3339Nano 下也看得出差异，而不是靠运气撞上同值。
	time.Sleep(1100 * time.Millisecond)
	if err := s.PlayTraffic.EnsureEnabledSince(ctx); err != nil {
		t.Fatalf("二次 EnsureEnabledSince 失败: %v", err)
	}
	second, found2, err := s.PlayTraffic.EnabledSince(ctx)
	if err != nil {
		t.Fatalf("二次读取 EnabledSince 失败: %v", err)
	}
	if !found2 {
		t.Fatal("二次调用后启用时刻消失了")
	}
	if !second.Equal(first) {
		t.Fatalf("二次 EnsureEnabledSince 覆盖了启用时刻: 第一次=%s 第二次=%s —— "+
			"每次重启都会把统计下界推成今天，「从启用起累计」的口径就废了", first, second)
	}
}

// TestPlayTrafficSentinelRowIsUnique 多次调用后哨兵行仍只有一行。
//
// 唯一键 uq_play_traffic_day_user(day,user_id) 应该自己兜住这件事，
// 所以这条用例是在钉住 ON CONFLICT 的**目标**必须是 (day,user_id) ——
// 写错成别的冲突目标时重复调用会插出多行，而 EnabledSince
// 用 QueryRow 读会静默取到其中任意一行，不报错。
func TestPlayTrafficSentinelRowIsUnique(t *testing.T) {
	ctx := context.Background()
	db, s := newSentinelStore(ctx, t, 3)

	if n := sentinelCount(ctx, t, db); n != 1 {
		t.Fatalf("哨兵行数 = %d，期望 1 —— ON CONFLICT 目标不对时重复调用会插出多行", n)
	}
	// 且读到的还是第一次那个时刻。
	if _, found, err := s.PlayTraffic.EnabledSince(ctx); err != nil || !found {
		t.Fatalf("三次调用后读不到启用时刻 (found=%v, err=%v)", found, err)
	}
}

// TestPlayTrafficAccumulateDoesNotTouchSentinel 真实流量桶的写入不能
// 碰到哨兵行，哨兵行也不能被算进任何统计。
func TestPlayTrafficAccumulateDoesNotTouchSentinel(t *testing.T) {
	ctx := context.Background()
	_, s := newSentinelStore(ctx, t, 1)
	today := time.Now().Format("2006-01-02")

	// 计费会话累加真实流量。
	if err := s.PlayTraffic.Accumulate(ctx, today, 7, "小明", 1000); err != nil {
		t.Fatalf("Accumulate 失败: %v", err)
	}
	// 非计费态（bytes <= 0）直接返回，不留痕。
	if err := s.PlayTraffic.Accumulate(ctx, today, 8, "小红", 0); err != nil {
		t.Fatalf("Accumulate(bytes=0) 失败: %v", err)
	}

	buckets, err := s.PlayTraffic.ByUser(ctx, today, today)
	if err != nil {
		t.Fatalf("ByUser 失败: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("ByUser 返回 %d 行，期望 1（哨兵行必须被排除、非计费态不留痕）", len(buckets))
	}
	if buckets[0].UserID != 7 || buckets[0].UploadedBytes != 1000 {
		t.Fatalf("桶内容 = {user=%d bytes=%d}，期望 {user=7 bytes=1000}", buckets[0].UserID, buckets[0].UploadedBytes)
	}

	// 同用户当天看两部片：唯一键 (day,user_id) 会把两部的流量合并成一行。
	// 这是照搬 参考实现 表结构带来的**已知不一致**（语义是「用户×条目×天」），
	// 本用例把这个行为钉下来 —— 哪天有人改成按条目拆行，测试会提醒他
	// 这需要一次带数据迁移，不是改个约束就完事。
	if err := s.PlayTraffic.Accumulate(ctx, today, 7, "小明", 500); err != nil {
		t.Fatalf("二次 Accumulate 失败: %v", err)
	}
	buckets, err = s.PlayTraffic.ByUser(ctx, today, today)
	if err != nil {
		t.Fatalf("二次 ByUser 失败: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("同用户当天两次累计后有 %d 行，期望 1（唯一键 day+user_id 会合并，语义与「用户×条目×天」不一致）", len(buckets))
	}
	if buckets[0].UploadedBytes != 1500 {
		t.Fatalf("合并后的字节数 = %d，期望 1500", buckets[0].UploadedBytes)
	}

	// 汇总数字也不能把哨兵行算进来。
	todayBytes, monthBytes, totalBytes, err := s.PlayTraffic.TodayMonthTotal(ctx, today, today[:7])
	if err != nil {
		t.Fatalf("TodayMonthTotal 失败: %v", err)
	}
	if todayBytes != 1500 || monthBytes != 1500 || totalBytes != 1500 {
		t.Fatalf("汇总 = {today=%d month=%d total=%d}，期望全部 1500", todayBytes, monthBytes, totalBytes)
	}
}

// TestPlayTrafficClearKeepsSentinel 清空统计清的是**数字**，
// 不是口径起点。清掉哨兵行等于把「从启用起累计」重置成今天。
func TestPlayTrafficClearKeepsSentinel(t *testing.T) {
	ctx := context.Background()
	db, s := newSentinelStore(ctx, t, 1)
	today := time.Now().Format("2006-01-02")
	if err := s.PlayTraffic.Accumulate(ctx, today, 7, "小明", 5000); err != nil {
		t.Fatalf("Accumulate 失败: %v", err)
	}

	n, err := s.PlayTraffic.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("Clear 报告删除 %d 行，期望 1（只删真实桶，不动哨兵行）", n)
	}

	if _, found, err := s.PlayTraffic.EnabledSince(ctx); err != nil || !found {
		t.Fatalf("Clear 之后启用时刻丢失了 (found=%v, err=%v) —— 清统计不该动口径起点", found, err)
	}
	if c := sentinelCount(ctx, t, db); c != 1 {
		t.Fatalf("Clear 之后哨兵行数 = %d，期望 1", c)
	}
	buckets, err := s.PlayTraffic.ByUser(ctx, today, today)
	if err != nil {
		t.Fatalf("Clear 后 ByUser 失败: %v", err)
	}
	if len(buckets) != 0 {
		t.Fatalf("Clear 后仍有 %d 个桶，期望 0", len(buckets))
	}
}
