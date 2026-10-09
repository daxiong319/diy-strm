package domain

import (
	"context"
	"time"
)

// InspectionRepository 是巡检快照仓储端口。
//
// 它存在不是因为「巡检需要落库」，而是因为验收要求「所有修复动作都有预览」。
// 把预览结果做成有生命周期的快照，执行接口就只认快照 key：
// 没有 key 就没有可执行动作，这是用调用路径强制出来的，而不是靠调用方自觉。
type InspectionRepository interface {
	SaveInspectionSnapshot(ctx context.Context, key, findingIDs, payload string, scannedAt time.Time) error
	// GetInspectionSnapshot 返回 payload、finding_ids，第三值为 true 表示
	// 快照不存在、已过期或已被执行过（此时调用方应拒绝执行）。
	GetInspectionSnapshot(ctx context.Context, key string) (string, []string, bool, error)
	ConsumeInspectionSnapshot(ctx context.Context, key string) error
	DeleteOldInspectionSnapshots(ctx context.Context, before time.Time) (int64, error)
}