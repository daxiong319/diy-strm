package discovery

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestReserveTransferRealConcurrency 真并发验证：N 个 goroutine 同时用同一幂等键占位，
// 必须恰好 1 个成功、其余全部 ErrTransferDuplicate。
//
// 这是幂等设计的核心保证：定时器一轮与用户手动「立即搜索」可能真的同时执行。
//
// 【该测试覆盖的边界，勿高估】ReserveTransfer 有两层防护：
//   ① 应用层预检 FindTransferByIdempotencyKey
//   ② 数据库唯一索引 idx_disc_transfer_idem 兜底
// ddb 设置了 SetMaxOpenConns(1)，所有写入被串行化到单条连接，因此 ① 在写完前不会
// 被另一个 goroutine 插队 —— 实测把 ② 的唯一索引去掉后本测试仍然通过。
// 也就是说：本测试证明的是「并发调用下最终只会有 1 条记录」，**不是**「唯一索引必需」。
// 索引的价值在 ① 失效时才显现（如未来放开连接池、或多进程共库），
// 属于纵深防御。索引本身由 TestIdempotencyPartialIndex 单独断言其 DDL 存在。
func TestReserveTransferRealConcurrency(t *testing.T) {
	setupTestDB(t)

	const workers = 16
	key := TransferIdempotencyKey(42, "movie", 999, 0, "https://pan.example.com/s/race", "ch", "1")

	var wins, dups int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 尽量同时起跑
			_, err := ReserveTransfer(&DiscoveryTransferRecord{
				SubscriptionID: 42, MediaType: "movie", TMDBID: 999,
				LinkURL: "https://pan.example.com/s/race", PostID: "1",
				IdempotencyKey: key, Title: "并发测试",
			})
			switch {
			case err == nil:
				atomic.AddInt64(&wins, 1)
			case err == ErrTransferDuplicate:
				atomic.AddInt64(&dups, 1)
			default:
				t.Errorf("意外错误：%v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("应恰好 1 个占位成功，实际 %d（说明唯一索引未兜住并发）", wins)
	}
	if dups != workers-1 {
		t.Fatalf("应有 %d 个被判重复，实际 %d", workers-1, dups)
	}
}
