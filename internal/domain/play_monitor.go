package domain

import (
	"context"
	"time"
)

// T11 播放监控与观影报告的领域模型。
//
// 设计要点：playback_records 表是 0028 迁移建的（T11 的 0043 只做 ALTER 补列），
// 它的 user_id 是 TEXT、默认空串，语义是「Emby 侧的用户标识（字符串）」；
// 而 RBAC（T08）的 user_id 是 int64、非空。两者是**真实设计冲突**，不能靠把
// TEXT 改成 INTEGER 抹平 —— 那是破坏性 DDL，会把 0028 已有数据全部作废。
// 因此本包全程使用双标识：
//
//	user_id     (TEXT)  = Emby / 媒体服务器侧的用户标识，0028 遗留字段，保持原样
//	app_user_id (INT)   = litepan RBAC 侧的用户 ID（T11 迁移新增，默认 0 = 未知）
//
// 详见 migrations/0043_play_traffic.sql 顶部注释。

// ─────────────────────────────────────────────────────────────────────────────
// 三态
// ─────────────────────────────────────────────────────────────────────────────

// PlayState 播放会话三态。这是播放监控最核心的概念：
//
//	PlayStateMetered  计费中 —— 外网播放且字节流经过自己的服务器，上行流量可计费
//	PlayStateCDN      CDN 直连 —— 走 115 直链，字节流不经过自己服务器，计 0
//	PlayStateLAN      局域网 —— 内网播放，不计费
//
// 判定规则（与 internal/playback.PickAction 严格对应）：
//
//	PickAction 返回 ActionStream（流代理）→ 字节流过自己服务器 → 外网即 StateMetered
//	PickAction 返回 ActionRedirect（302）  → 客户端去 115 直链 → StateCDN，**计 0**
//	客户端 IP 落在内网网段              → StateLAN，**不计**
//
// 三者里最容易做错的是 CDN 直连：302 分支绝不能记流量，
// 因为那是网盘直链直发，服务器一个字节都没吐。
type PlayState string

const (
	// PlayStateMetered 计费中：外网 + 字节流经过自己服务器（流代理分支）。
	PlayStateMetered PlayState = "metered"
	// PlayStateCDN CDN 直连：302 跳转到网盘直链，字节流不经过自己服务器，流量计 0。
	PlayStateCDN PlayState = "cdn"
	// PlayStateLAN 局域网：内网播放，不计费。
	PlayStateLAN PlayState = "lan"
)

// DisplayText 三态的中文标签，供 API 与前端共用同一份口径，避免两处各写一套。
func (s PlayState) DisplayText() string {
	switch s {
	case PlayStateMetered:
		return "计费中"
	case PlayStateCDN:
		return "CDN 直连"
	case PlayStateLAN:
		return "局域网"
	default:
		return "未知"
	}
}

// Metered 是否计费。只有「计费中」计费；CDN 直连与局域网都是 0。
// 报告与排行图一律经过这个函数判口径，不允许在别处另写 if。
func (s PlayState) Metered() bool { return s == PlayStateMetered }

// Valid 合法值校验，供 API 入参使用。
func (s PlayState) Valid() bool {
	return s == PlayStateMetered || s == PlayStateCDN || s == PlayStateLAN
}

// ─────────────────────────────────────────────────────────────────────────────
// 实时播放会话
// ─────────────────────────────────────────────────────────────────────────────

// PlaySession 一个正在进行的播放会话（内存态）。
//
// 会话的生命周期由监控器在取流入口上驱动：
//
//	起播（流代理/302）→ 开一个 session
//	每 sampleSeconds 秒把「码率 × 采样间隔」累计进当日桶
//	心跳丢失 idleSeconds → 关 session 并上报一次 stop（从实时列表消失）
//	暂停后继续播 → 若间隔未超过 resumeSeconds，只转交一次停止事件（消噪）
type PlaySession struct {
	ID            string    `json:"id"`
	State         PlayState `json:"state"`
	AppUserID     int64     `json:"app_user_id"`
	UserName      string    `json:"user_name"`
	EmbyUserID    string    `json:"emby_user_id"`
	ItemName      string    `json:"item_name"`
	StrmPath      string    `json:"strm_path"`
	ItemScope     string    `json:"item_scope"`
	ClientIP      string    `json:"client_ip"`
	UserAgent     string    `json:"user_agent"`
	Client        string    `json:"client"`
	DeviceID      string    `json:"device_id"`
	StorageSlug   string    `json:"storage_slug"`
	StorageType   string    `json:"storage_type"`
	Bitrate       int64     `json:"bitrate"`
	UploadedBytes int64     `json:"uploaded_bytes"`
	// MeasuredBytes 本次请求实际写出的字节数（ResponseWriter 计数）。
	// UploadedBytes 是计费用估算值（码率×时长，每 5 秒一次），
	// MeasuredBytes 是实测值；两者并列展示是为了让用户能自己对账，
	// 偏离过大时监控器会打告警日志。
	MeasuredBytes int64     `json:"measured_bytes"`
	StartedAt     time.Time `json:"started_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	AccountID     int64     `json:"account_id"`
}

// PlaySessionSummary 实时列表顶部流量条所需的汇总数字（参考实现 §3）。
type PlaySessionSummary struct {
	TodayBytes   int64 `json:"today_bytes"`
	MonthBytes   int64 `json:"month_bytes"`
	TotalBytes   int64 `json:"total_bytes"`
	ActiveCount  int   `json:"active_count"`
	MeteredCount int   `json:"metered_count"`
	// WANCount 外网会话数（含计费中 + CDN 直连）。
	WANCount int `json:"wan_count"`
	// LANCount 局域网会话数（不计费）。
	LANCount int `json:"lan_count"`
	// CDNCount CDN 直连会话数（计 0）。
	CDNCount int `json:"cdn_count"`
}

// PlaySessionQuery 实时会话列表的过滤条件。
type PlaySessionQuery struct {
	State     PlayState
	AppUserID int64
	Page      int
	PageSize  int
}

// PlaySessionListResult 实时会话列表 + 汇总。
type PlaySessionListResult struct {
	Sessions []PlaySession      `json:"sessions"`
	Summary  PlaySessionSummary `json:"summary"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 流量统计
// ─────────────────────────────────────────────────────────────────────────────

// PlayTrafficRepository 播放流量仓储。
//
// play_traffic_daily 的唯一键是 (day, user_id)，而它的语义是
// 「用户 × 条目 × 天」—— 同用户同一天看两部片子只会落一行、流量被合并。
// 这是照搬 参考实现 表结构带来的**已知不一致**，代码侧统一在
// Accumulate 处累加合并（不做额外拆分），并在 RankByUser 里按需再聚合。
// 该点已标注为待确认项，写在这里以免后来人以为是 bug 而"顺手修掉"。
type PlayTrafficRepository interface {
	// EnsureEnabledSince 写入/读取「播放监控首次启用时刻」标记。
	//
	// 为什么需要它：观影报告的口径是「**统计从启用插件起累计，之前不补算**」。
	// 这个「启用时刻」必须持久化到进程之外，否则每次重启都把下界推成今天，
	// 累积的历史会凭空消失。settings 侧拿不到首次启用时刻
	// （settings 不记录更新���间），所以落在流量表的哨兵行里：
	// day = PlayTrafficEnabledSinceDay 且 user_id = 0 的一行，
	// updated_at 就是首次启用时刻。**只写一次，之后永不更新。**
	//
	// 该行 uploaded_bytes 恒为 0，且所有统计查询都要排除它。
	EnsureEnabledSince(ctx context.Context) error
	// EnabledSince 读取首次启用时刻；从未启用过时返回零值与 false。
	EnabledSince(ctx context.Context) (time.Time, bool, error)
	// Accumulate 往 (day, user_id) 桶里累加上行字节。
	Accumulate(ctx context.Context, day string, userID int64, userName string, bytes int64) error
	// ByUser 列出区间内每用户的上行流量（用于排行图）。
	ByUser(ctx context.Context, since, until string) ([]PlayTrafficBucket, error)
	// TodayMonthTotal 返回今日/本月/累计的上行字节。
	TodayMonthTotal(ctx context.Context, day, month string) (today, monthTotal, total int64, err error)
	// RankByUser 返回本月外网上行排行（按用户汇总）。
	RankByUser(ctx context.Context, month string, limit int) ([]PlayTrafficRank, error)
	// Clear 清空流量统计（不可撤销，与 参考实现 一致）。
	//
	// 清空时**保留**首次启用标记行：清的是统计数字，不是「从哪天开始算」的口径。
	Clear(ctx context.Context) (int64, error)
}

// PlayTrafficEnabledSinceDay 首次启用标记的哨兵 day 值。
//
// 它不是合法日期（日期形如 2006-01-02），所以永远不会和真实日期桶相撞，
// 所有按日期区间/前缀的统计查询都必须显式排除它。
const PlayTrafficEnabledSinceDay = "__enabled_since__"

// PlayTrafficBucket 单个 (day,user_id) 桶。
type PlayTrafficBucket struct {
	Day           string `json:"day"`
	UserID        int64  `json:"user_id"`
	UserName      string `json:"user_name"`
	UploadedBytes int64  `json:"uploaded_bytes"`
}

// PlayTrafficRank 排行条目（按用户聚合）。
type PlayTrafficRank struct {
	UserID        int64  `json:"user_id"`
	UserName      string `json:"user_name"`
	UploadedBytes int64  `json:"uploaded_bytes"`
	Sessions      int64  `json:"sessions"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 监控器接口
// ─────────────────────────────────────────────────────────────────────────────

// PlayMonitorObserver 播放监控的观察者，由 internal/playback 的取流入口驱动。
//
// Observe 是一次取流请求被"判定为开始播放"时调用的事件，
// 三态已经由 PickAction + 客户端 IP 判定完毕。
type PlayMonitorObserver interface {
	// OnStreamOpen 字节流经自己服务器（流代理分支）——可能计费。
	OnStreamOpen(ev StreamEvent)
	// OnStreamStop 心跳丢失/正常结束，会话关闭。
	OnStreamStop(id string)
	// OnRedirectOpen 302 分支，只记 open，**不记流量**（计 0）。
	OnRedirectOpen(ev StreamEvent)
}

// StreamEvent 一次播放请求的判定结果。
type StreamEvent struct {
	SessionID   string
	State       PlayState
	AccountID   int64
	AppUserID   int64
	EmbyUserID  string
	ItemName    string
	StrmPath    string
	ItemScope   string
	ClientIP    string
	UserAgent   string
	Client      string
	DeviceID    string
	StorageSlug string
	StorageType string
	Bitrate     int64
	RequestURL  string
	OriginalURL string
	AppSource   string
	RequestType string
	// UploadedBytes 已吐字节（流代理分支结束时才有值；302 分支恒为 0）。
	UploadedBytes int64
	// WatchedSeconds 本次播放时长（秒）。
	WatchedSeconds int
}
