package discovery

import (
	"testing"

	"litepan/internal/discover/ddb"
)

// TestIdempotencyPartialIndex 守卫幂等键的部分唯一索引契约。
//
// SQLite 的 UNIQUE 把空串视为相等值，普通唯一索引会让「第二条无幂等键的记录」插入失败。
// 而无幂等键的记录是合法的（资源身份缺失时退化为不去重），故索引必须带 WHERE 子句。
// 同时验证非空重复键仍被唯一索引拦下（这才是幂等键的用途）。
func TestIdempotencyPartialIndex(t *testing.T) {
	setupTestDB(t)
	// 索引 DDL 应带 WHERE 子句
	var sql string
	if err := ddb.Db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name='idx_disc_transfer_idem'").Scan(&sql).Error; err != nil {
		t.Fatalf("查索引: %v", err)
	}
	t.Logf("索引 DDL: %s", sql)

	// 两条空键记录必须都能插入
	for i := 0; i < 2; i++ {
		r := &DiscoveryTransferRecord{LinkURL: "https://example.com/empty", Title: "空键"}
		if err := CreateTransferRecord(r); err != nil {
			t.Fatalf("第 %d 条空键记录插入失败：%v", i+1, err)
		}
	}
	// 同一非空键第二次必须被拦
	r1 := &DiscoveryTransferRecord{LinkURL: "https://example.com/a", IdempotencyKey: "dupkey123", Title: "A"}
	if _, err := ReserveTransfer(r1); err != nil {
		t.Fatalf("首次占位应成功：%v", err)
	}
	r2 := &DiscoveryTransferRecord{LinkURL: "https://example.com/a", IdempotencyKey: "dupkey123", Title: "A2"}
	if _, err := ReserveTransfer(r2); err != ErrTransferDuplicate {
		t.Fatalf("重复键应返回 ErrTransferDuplicate，实际 %v", err)
	}
}
