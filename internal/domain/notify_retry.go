package domain

import (
	"context"
	"time"
)

// 通知补发队列（outbox）的状态机。
//
// 状态流转：pending --发送成功--> sent
// 　　　　　pending --重试用尽--> failed
// 　　　　　pending --管理台手动重投--> 回到 pending（attempts 归零）
//
// sent 与 failed 都是终态，worker 不再捞。只有 pending 会被排期扫描。
type NotifyRetryStatus string

const (
	// NotifyRetryStatusPending 等待下一次重试。
	NotifyRetryStatusPending NotifyRetryStatus = "pending"
	// NotifyRetryStatusSent 补发成功。
	NotifyRetryStatusSent NotifyRetryStatus = "sent"
	// NotifyRetryStatusFailed 重试用尽，仍未送达。管理台可见并可手动重投。
	NotifyRetryStatusFailed NotifyRetryStatus = "failed"
)

// NotifyRetryEntry 补发队列里的一条记录。
//
// ⚠️ 快照语义：ChannelType / ChannelName / TargetURL / ChannelConfig 都是
// **发送当时的快照**，不是对 notify_channels 的引用。渠道被改名、改地址、
// 甚至被删除之后，这些字段仍然回答「当初那条通知是发给谁的」。
//
// 为什么不 JOIN notify_channels 现查：重试要投的是**同一次失败**给
// **同一个地址**，用户改了 webhook 地址之后自动改投新地址等于把历史失败
// 悄悄转嫁给新配置，用户以为旧地址修好了其实一条没补上。
type NotifyRetryEntry struct {
	ID int64
	// EventScene 触发来源场景；事件本身已落 notifications 表，这里便于
	// 按场景排查，不参与过滤。
	EventScene NotificationScene
	// ChannelType 只有 "webhook" 会入队（见 notifychannel/outbox.go）。
	ChannelType string
	// ChannelName 渠道名快照。
	ChannelName string
	// TargetURL 目标地址快照。
	TargetURL string
	// ChannelConfig 发送当时的 config JSON 快照。
	ChannelConfig string
	// Title / Content / Tone 原样保存用于重发。
	Title   string
	Content string
	Tone    string
	// Attempts 已尝试次数（不含还没执行的那次）。
	Attempts int
	// NextRetryAt 下次重试时间；状态非 pending 时无意义。
	NextRetryAt time.Time
	// LastError 最近一次失败原因（原样存 Send 返回的 error 文案）。
	LastError string
	Status    NotifyRetryStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NotifyRetryQuery 补发队列列表查询条件。零值 = 不筛。
type NotifyRetryQuery struct {
	// Status 只查某一状态；空串 = 全部。
	Status NotifyRetryStatus
	// Limit <= 0 时由仓储取默认（100）；上限 500。
	Limit int
	// Offset 分页偏移。
	Offset int
}

// NotifyRetryRepository 补发队列仓储。
//
// 抽成接口而不是让 notifychannel 直接依赖 store，是为了 dispatcher / worker
// 能用内存替身测退避逻辑（真 sleep 的测试没人愿意等）。
type NotifyRetryRepository interface {
	// Enqueue 新增一条待补发记录，返回自增 ID。
	Enqueue(ctx context.Context, e NotifyRetryEntry) (int64, error)
	// Due 查询到点待重试的记录（status=pending 且 next_retry_at <= now），
	// 按 next_retry_at 升序，先到先发。limit <= 0 时由仓储取默认。
	Due(ctx context.Context, now time.Time, limit int) ([]NotifyRetryEntry, error)
	// UpdateResult 记录一次重试的结果：成功标 sent，失败按 backoff 计算
	// 下次时间；attempts 超过用尽仍失败则标 failed。
	UpdateResult(ctx context.Context, id int64, attempts int, nextRetryAt time.Time, lastErr string, status NotifyRetryStatus) error
	// List 分页列出（管理台补发队列视图用）。
	List(ctx context.Context, q NotifyRetryQuery) ([]NotifyRetryEntry, int64, error)
	// Get 按 ID 取一条。
	Get(ctx context.Context, id int64) (NotifyRetryEntry, error)
	// Redrive 把一条 failed 记录重新排入 pending，attempts 归零。
	// 非 failed 状态返回错误（否则会重置正在重试中的记录）。
	Redrive(ctx context.Context, id int64, now time.Time) error
	// Clear 按状态清理：空状态 = 清全部。返回删除行数。
	Clear(ctx context.Context, status NotifyRetryStatus) (int64, error)
	// Counts 返回各状态条数（管理台顶部分栏计数用）。
	Counts(ctx context.Context) (map[NotifyRetryStatus]int64, error)
}
