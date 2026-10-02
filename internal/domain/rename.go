package domain

import (
	"context"
	"encoding/json"
	"time"
)

// RenameHistory 一次批量重命名操作的记录，用于展示与回滚。
// Targets 保存的是「回滚目标」：Name 为改名后的名字、NewName 为改名前的名字，
// 因此回滚时只需把 Name 再改回去（对齐老版 internal/models/rename_record.go）。
type RenameHistory struct {
	ID          int64
	UserID      int64
	Name        string
	Rules       json.RawMessage
	KeepExt     bool
	Targets     json.RawMessage
	ItemCount   int
	ChangeCount int
	CreatedAt   time.Time
}

// RenamePreset 常用规则组合。
type RenamePreset struct {
	ID        int64
	UserID    int64
	Name      string
	Rules     json.RawMessage
	KeepExt   bool
	UseCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RenameRepository 批量重命名的历史与常用组合仓储。
// 现版没有多用户概念，userID 一律传 0。
type RenameRepository interface {
	// CreateHistory 写入一条重命名历史。
	CreateHistory(ctx context.Context, history *RenameHistory) error
	// ListHistories 按 id 倒序返回最近的历史，limit 上限 200。
	ListHistories(ctx context.Context, userID int64, limit int) ([]*RenameHistory, error)
	// GetHistory 读取单条历史，不存在时返回 CodeNotFound 错误。
	GetHistory(ctx context.Context, id, userID int64) (*RenameHistory, error)
	// UpdateHistoryTargets 回滚后更新剩余待回滚条目与变更数量。
	UpdateHistoryTargets(ctx context.Context, id int64, targets json.RawMessage, changeCount int) error
	// DeleteHistory 删除一条历史。
	DeleteHistory(ctx context.Context, id, userID int64) error

	// CreatePreset 保存常用组合；同名时覆盖规则并保留使用次数与创建时间。
	CreatePreset(ctx context.Context, preset *RenamePreset) error
	// ListPresets 按 id 倒序返回常用组合。
	ListPresets(ctx context.Context, userID int64) ([]*RenamePreset, error)
	// DeletePreset 删除一条常用组合，不存在时返回 CodeNotFound 错误。
	DeletePreset(ctx context.Context, id, userID int64) error
	// IncrementPresetUse 命中相同规则与扩展名设置时累加使用次数。
	IncrementPresetUse(ctx context.Context, userID int64, rules json.RawMessage, keepExt bool) error
}
