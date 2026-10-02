package domain

import (
	"context"
	"time"
)

// EmbyRefreshTargetType 是刷新任务的刷新目标类型。
const (
	EmbyRefreshTargetLibrary = "library"
	EmbyRefreshTargetItem    = "item"
)

// EmbyRefreshStatus 是刷新任务的状态。
const (
	EmbyRefreshStatusPending    = "pending"
	EmbyRefreshStatusRefreshing = "refreshing"
	EmbyRefreshStatusCompleted  = "completed"
	EmbyRefreshStatusFailed     = "failed"
	EmbyRefreshStatusCancelled  = "cancelled"
)

// EmbyRefreshTask 是一条持久化的 Emby/Jellyfin 刷新任务。
//
// 同一 TaskKey（"library:<id>" 或 "item:<id>"）只保留一行，新事件到来时
// 顺延 RefreshAfterAt 并合并 ItemIDs，从而实现对突发事件的防抖合并。
type EmbyRefreshTask struct {
	ID            int64
	TaskKey       string
	LibraryID     string
	LibraryName   string
	TargetType    string
	ItemIDs       string
	Status        string
	LastEventAt   int64
	RefreshAfter  int64
	DeadlineAt    int64
	LastCheckedAt int64
	LastRefreshAt int64
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// EmbyRefreshTaskRepository 持久化刷新任务队列。
type EmbyRefreshTaskRepository interface {
	// UpsertEvent 登记一次刷新事件：新建或顺延防抖窗口并合并 item_ids。
	UpsertEvent(ctx context.Context, task *EmbyRefreshTask) (*EmbyRefreshTask, error)
	// GetByKey 按 TaskKey 读取任务，不存在时返回 (nil, false, nil)。
	GetByKey(ctx context.Context, taskKey string) (*EmbyRefreshTask, bool, error)
	// ListReady 返回所有已可执行（pending 且到点）的任务，按 refresh_after_at 升序。
	ListReady(ctx context.Context, now int64, limit int) ([]*EmbyRefreshTask, error)
	// ListReadyItems 返回已到点的 pending item 级任务，用于按媒体库合并。
	ListReadyItems(ctx context.Context, now int64, limit int) ([]*EmbyRefreshTask, error)
	// EarliestDebounce 返回最早的 pending 任务防抖时刻，用于扫描器提前唤醒。
	EarliestDebounce(ctx context.Context, now int64) (int64, bool, error)
	// Claim 以 CAS 方式把 pending 任务置为 refreshing，返回是否抢占成功。
	Claim(ctx context.Context, id int64) (bool, error)
	// Complete 把任务标记为终态。
	Complete(ctx context.Context, id int64, status, errMessage string, refreshedAt int64) error
	// CancelExpired 把已过截止时间的 pending 任务标记为 cancelled，返回取消数量。
	CancelExpired(ctx context.Context, now int64) (int64, error)
	// AbsorbItemsIntoLibrary 把 item 任务并入库级任务：合并 item_ids，
	// 并把被吸收的 item 任务标记为 cancelled，返回吸收数量。
	AbsorbItemsIntoLibrary(ctx context.Context, libraryTask *EmbyRefreshTask, itemTasks []*EmbyRefreshTask, now int64) (int, error)
}
