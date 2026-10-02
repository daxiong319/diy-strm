package embyindex

import (
	"context"
	"testing"
)

// 本文件提供包内可见的测试辅助。
//
// 为什么需要它：刷新聚合器 refreshAggregator 与 Service 的 refreshSink 都是未导出的，
// 而「超过阈值改登记库级刷新」这条规则属于实现细节，不适合为了测试把它导出。
// 因此这里放一个包内辅助，让 package embyindex_test 的用例仍能从外部视角
// 驱动聚合逻辑（外部用例通过导出的 New/Options 构造服务，只把「投递 N 条变化」
// 这一步交给包内函数完成）。
//
// 这保持了「公开 API 不被测试需求污染」与「测试走真实路径」两者的平衡。

// registerTestIntents 向服务的刷新聚合器投递 n 条条目变化并冲刷。
//
// 仅供测试使用：生产路径上这些调用发生在 performSync / SyncEmbyItemByID 内部。
func registerTestIntents(t *testing.T, s *Service, libraryID, libraryName string, n int) {
	t.Helper()
	if s == nil {
		t.Fatalf("Service 为 nil，无法投递刷新意图")
	}
	agg := newRefreshAggregator(s.refreshSink, s.refreshThreshold)
	for i := 0; i < n; i++ {
		agg.add(libraryID, libraryName, itemIDForIndex(i))
	}
	agg.flush(context.Background(), s.logWarn)
}

// itemIDForIndex 生成稳定的测试条目 ID。
func itemIDForIndex(i int) string {
	return "item-" + itoa(i)
}

// itoa 是 strconv.Itoa 的本地小包装，避免为一个测试辅助引入额外 import 分组。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
