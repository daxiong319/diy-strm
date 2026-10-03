package discovery

import (
	"testing"
)

// TestReserveTransferBlocksConcurrentDuplicate 模拟「定时器一轮」与「手动立即搜索」
// 同时跑到同一条资源的并发窗口：两边都通过了 HasLinkRecord 预检（都查到 0 条），
// 随后各自调用 ReserveTransfer —— 只有一方能占位成功，另一方必须拿到 ErrTransferDuplicate。
//
// 这是幂等键存在的唯一理由：HasLinkRecord 是 query-then-insert，两个 goroutine 都能
// 在对方插入前完成查询。此测试把该序列显式串起来，确保唯一索引真的兜住了。
func TestReserveTransferBlocksConcurrentDuplicate(t *testing.T) {
	setupTestDB(t)

	link := "https://pan.example.com/s/abc123"
	key := TransferIdempotencyKey(7, "tv", 12345, 2, link, "somechannel", "999")

	// 并发前后各调一次 HasLinkRecord，模拟两个轮次都走完预检的时序。
	if HasLinkRecord(link) {
		t.Fatal("前置条件不成立：此时不应已有该链接的记录")
	}
	if HasLinkRecord(link) {
		t.Fatal("前置条件不成立：此时不应已有该链接的记录")
	}

	// 第一轮占位成功
	first := &DiscoveryTransferRecord{
		SubscriptionID: 7, MediaType: "tv", TMDBID: 12345, Season: 2,
		PostID: "999", LinkURL: link, IdempotencyKey: key, Title: "示例剧",
	}
	saved, err := ReserveTransfer(first)
	if err != nil {
		t.Fatalf("首个占位应成功，实际 %v", err)
	}
	if saved.ID == 0 {
		t.Fatal("占位应返回带 ID 的记录，供 ConfirmTransfer/FailTransfer 使用")
	}

	// 第二轮占位必须被拦下
	second := &DiscoveryTransferRecord{
		SubscriptionID: 7, MediaType: "tv", TMDBID: 12345, Season: 2,
		PostID: "999", LinkURL: link, IdempotencyKey: key, Title: "示例剧",
	}
	if _, err := ReserveTransfer(second); err != ErrTransferDuplicate {
		t.Fatalf("并发第二条应用 ErrTransferDuplicate 拦下，实际 %v", err)
	}

	// 占位后 HasLinkRecord 才为真（说明预检与占位的顺序确实是「先查后插」）
	if !HasLinkRecord(link) {
		t.Fatal("占位后应能查到该链接记录")
	}
}

// TestReserveTransferStateMachine 占位 → 确认 / 失败的三种状态推进。
func TestReserveTransferStateMachine(t *testing.T) {
	setupTestDB(t)

	mk := func(key string) *DiscoveryTransferRecord {
		return &DiscoveryTransferRecord{
			SubscriptionID: 1, MediaType: "movie", TMDBID: 100, LinkURL: "https://x/" + key,
			IdempotencyKey: key, Title: "t",
		}
	}

	// 失败路径：failed 后仍应能查到记录（保留供重试与审计）
	rec, err := ReserveTransfer(mk("keyA"))
	if err != nil {
		t.Fatalf("占位失败：%v", err)
	}
	got, err := FindTransferByIdempotencyKey("keyA")
	if err != nil || got == nil {
		t.Fatalf("占位后应能按键查到记录：got=%v err=%v", got, err)
	}
	if got.TransferState != TransferStateRequested {
		t.Fatalf("初始状态应为 requested，实际 %q", got.TransferState)
	}
	if err := FailTransfer(rec.ID); err != nil {
		t.Fatalf("FailTransfer 失败：%v", err)
	}
	got, _ = FindTransferByIdempotencyKey("keyA")
	if got.TransferState != TransferStateFailed {
		t.Fatalf("失败后状态应为 failed，实际 %q", got.TransferState)
	}

	// 成功路径
	recB, err := ReserveTransfer(mk("keyB"))
	if err != nil {
		t.Fatalf("占位失败：%v", err)
	}
	if err := ConfirmTransfer(recB.ID); err != nil {
		t.Fatalf("ConfirmTransfer 失败：%v", err)
	}
	gotB, _ := FindTransferByIdempotencyKey("keyB")
	if gotB.TransferState != TransferStateConfirmed {
		t.Fatalf("确认后状态应为 confirmed，实际 %q", gotB.TransferState)
	}

	// 幂等键为空不应 panic、不应报错（退化为普通写入）
	empty := &DiscoveryTransferRecord{LinkURL: "https://x/empty", Title: "e"}
	if _, err := ReserveTransfer(empty); err != nil {
		t.Fatalf("空键占位不应报错：%v", err)
	}
}

// TestTransferIdempotencyKeyExcludesEpisode 幂等键刻意不含集号：
// 同一帖的多集共用一次转存；洗版升级靠 Status=superseded 而非幂等键，
// 若键里带上集号或版本号，升级会被误判为重复而拒绝。
func TestTransferIdempotencyKeyExcludesEpisode(t *testing.T) {
	link := "https://pan.example.com/s/same"
	a := TransferIdempotencyKey(1, "tv", 100, 1, link, "chan", "555")
	b := TransferIdempotencyKey(1, "tv", 100, 1, link, "chan", "555")
	if a != b {
		t.Fatal("同参数幂等键应稳定")
	}
	// 链接相同但帖子不同（同一资源换帖重发）→ 键相同，应拦住
	c := TransferIdempotencyKey(1, "tv", 100, 1, link, "otherchan", "999")
	if a != c {
		t.Fatal("资源身份优先用链接：同链接不同帖应得到同一幂等键（换帖重发也要拦住）")
	}
	// 季不同 → 键不同（不同季是不同资源）
	d := TransferIdempotencyKey(1, "tv", 100, 2, link, "chan", "555")
	if a == d {
		t.Fatal("不同季应得到不同幂等键")
	}
	// 订阅不同 → 键不同
	e := TransferIdempotencyKey(2, "tv", 100, 1, link, "chan", "555")
	if a == e {
		t.Fatal("不同订阅应得到不同幂等键")
	}
	// 链接为空时回落到 频道|帖ID
	f := TransferIdempotencyKey(1, "tv", 100, 1, "", "chan", "555")
	if f == "" {
		t.Fatal("链接为空但有频道+帖 ID 时应仍能生成键")
	}
	// 两者都空 → 无身份可比，返回空串（调用方据此退化为不去重）
	if g := TransferIdempotencyKey(1, "tv", 100, 1, "", "", ""); g != "" {
		t.Fatalf("无任何资源身份时应返回空键，实际 %q", g)
	}
}
