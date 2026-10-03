package discovery

import (
	"testing"
)

// TestIdempotencyIndexEnforcedByDB 直接绕开应用层预检，验证唯一索引本身能挡住重复插入。
//
// 为什么需要单独测：ReserveTransfer 的应用层预检在单连接下已足够，
// 索引作为第二层防护平时不会被触发（见 TestReserveTransferRealConcurrency 的说明）。
// 这里用裸 DB.Create 绕过预检，才能证明索引真的存在且生效 —— 若索引缺失，
// 未来一旦放开连接池或多进程共库，重复转存就会静默发生。
func TestIdempotencyIndexEnforcedByDB(t *testing.T) {
	setupTestDB(t)

	key := TransferIdempotencyKey(1, "movie", 555, 0, "https://pan.example.com/s/idx", "ch", "9")
	first := &DiscoveryTransferRecord{
		SubscriptionID: 1, MediaType: "movie", TMDBID: 555,
		LinkURL: "https://pan.example.com/s/idx", PostID: "9",
		IdempotencyKey: key, Title: "索引测试",
	}
	if err := CreateTransferRecord(first); err != nil {
		t.Fatalf("第一条应能插入：%v", err)
	}

	second := &DiscoveryTransferRecord{
		SubscriptionID: 1, MediaType: "movie", TMDBID: 555,
		LinkURL: "https://pan.example.com/s/idx", PostID: "9",
		IdempotencyKey: key, Title: "索引测试重复",
	}
	if err := CreateTransferRecord(second); err == nil {
		t.Fatal("绕过应用层预检后，数据库唯一索引应拒绝重复幂等键，但插入成功了")
	}

	// 反向：空幂等键必须仍然可以重复插入（合法的「无身份」记录）。
	// 这正是部分索引 where:idempotency_key <> '' 存在的理由 ——
	// 普通唯一索引会把空串视为相等值，导致第二条空键记录插入失败。
	for i := 0; i < 3; i++ {
		rec := &DiscoveryTransferRecord{
			SubscriptionID: 1, MediaType: "movie", TMDBID: 555,
			LinkURL: "https://pan.example.com/s/nokey", PostID: "10", Title: "无键记录",
		}
		if err := CreateTransferRecord(rec); err != nil {
			t.Fatalf("第 %d 条空幂等键记录应能插入（部分索引须排除空串）：%v", i+1, err)
		}
	}
}
