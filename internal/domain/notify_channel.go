package domain

import "time"

// NotifyChannel 外部通知渠道配置（telegram/bark/webhook 等 17 种）。
// 配置以 JSON 字符串存于 Config，对应 notifychannel 包的 cfg map[string]string。
type NotifyChannel struct {
	ID        int64
	Type      string // 渠道类型 ID，对应 notifychannel.Registry 的 key
	Name      string // 用户自定义显示名
	Config    string // JSON 序列化的渠道配置
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}
