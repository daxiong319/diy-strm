package domain

import (
	"context"
	"time"
)

// T16 · RSS 订阅源的领域模型。
//
// 字段口径照搬 参考实现 的 rss_subscription.py（见
// /root/dsh/reference_recovered/src/app/models/rss_subscription.py）与
// internal/store/migrations/0044_rss_subscription.sql。
//
// 两个 参考实现 有、litepan 补上的列（迁移文件里已注明理由）：
//
//	MediaType  订阅源声明它追的是电影还是剧集，参与 T03 身份校验；
//	Action     产出候选的落地通道：网盘分享走转存，磁力/ed2k 走离线下载
//	           （磁力没有网盘分享可转存，这是 RSS 相对其它源的结构性差异）。
//
// 媒体库判定 MediaServer 是**三态**，与 参考实现 一致，不要归一化：
//
//	""           跨全部启用媒体库并集判定
//	"__none__"   哨兵字面量，不判定（照常下载，重复版本交给洗版规则处理）
//	"库 slug"    只查该库
const (
	// RSSMediaServerNone 不做媒体库判定。
	//
	// 与空串是两回事：空串是「查所有启用库」，这个是「根本不去查」。
	// 参考实现 前端的下拉里就是这三个值，归一化成 "" 会把「不判定」
	// 变成「全部库都要没有才下载」，用户会莫名发现再也下不到东西。
	RSSMediaServerNone = "__none__"
)

// RSS 产出候选的落地通道（对应 0044 的 rss_subscription_sources.action）。
const (
	// RSSActionTransfer 走网盘转存。仅对分享链接类候选可用。
	RSSActionTransfer = "transfer"
	// RSSActionOffline 走离线下载。磁力/ed2k/直链只能用这条通道。
	RSSActionOffline = "offline"
)

// RSS 同步结果状态。
//
// 参考实现 前端是硬编码这五个值并配了固定配色（success/partial/failed/
// skipped/completed），前端组件照搬，不要换。
const (
	// RSSStatusSuccess 本轮全部条目都成功落地。
	RSSStatusSuccess = "success"
	// RSSStatusPartial 部分成功：有的成功，有的失败。
	RSSStatusPartial = "partial"
	// RSSStatusFailed 本轮整体失败（抓取失败或全部条目失败）。
	RSSStatusFailed = "failed"
	// RSSStatusSkipped 没有新条目。
	RSSStatusSkipped = "skipped"
	// RSSStatusCompleted 条目被人工标记为已完成（只写历史、不提交下载）。
	RSSStatusCompleted = "completed"
)

// RSSSource 一条 RSS 订阅源。
type RSSSource struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	RssURL       string     `json:"rss_url"`
	TargetPath   string     `json:"target_path"`
	Storage      string     `json:"storage"`
	MediaServer  string     `json:"media_server"`
	PosterURL    string     `json:"poster_url"`
	IncludeRegex string     `json:"include_regex"`
	ExcludeRegex string     `json:"exclude_regex"`
	MediaType    string     `json:"media_type"`
	Action       string     `json:"action"`
	Enabled      bool       `json:"enabled"`
	LastSyncAt   *time.Time `json:"last_sync_at"`
	LastStatus   string     `json:"last_status"`
	LastMessage  string     `json:"last_message"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// RSSHistory 一条 RSS 条目的处理记录。
//
// ⚠️ 这张表的 guid 是**全局唯一**（uq_rss_subscription_history_guid，单列），
// 不带 source_id —— 与 参考实现 完全一致。含义是「同一个 feed 条目在整个系统里
// 只处理一次」，哪怕被两个源同时订阅到、哪怕源 URL 改过一次。
// 不要"好心"改成 (source_id, guid) 复合唯一：那会让同一条目被重复提交下载。
type RSSHistory struct {
	ID          int64      `json:"id"`
	SourceID    int64      `json:"source_id"`
	SourceName  string     `json:"source_name"`
	Guid        string     `json:"guid"`
	Title       string     `json:"title"`
	Link        string     `json:"link"`
	DownloadURL string     `json:"download_url"`
	TargetPath  string     `json:"target_path"`
	Status      string     `json:"status"`
	Message     string     `json:"message"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// RSSSourceQuery 源列表查询条件。
type RSSSourceQuery struct {
	// Enabled 非 nil 时按启用状态过滤（轮询只扫启用的源）。
	Enabled *bool
	// Keyword 按名称或 URL 模糊匹配。
	Keyword string
	// Limit 0 = 不限。
	Limit int
}

// RSSHistoryQuery 历史列表查询条件。
type RSSHistoryQuery struct {
	// SourceID > 0 时只看该源。
	SourceID int64
	// Status 非空时按状态过滤。
	Status string
	// Since 只取该时刻之后创建的记录（0 = 不限）。
	Since time.Time
	// Limit 0 = 不限；轮询取位点时用 Limit=1 + 按 created_at 倒序。
	Limit int
}

// RSSSyncCounters 一轮同步的三个计数。
//
// 参考实现 的同步接口原样返回这三个数，toast 文案逐字是
// 「同步完成：新增 N，跳过 N，失败 N」，前端不改写。
type RSSSyncCounters struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// Total 三计数之和，用于判断「本轮有没有干过活」。
func (c RSSSyncCounters) Total() int { return c.Added + c.Skipped + c.Failed }

// RSSUpsertPayload 源的新建/更新载荷。
//
// RssURL 为空且 ID > 0 视为不更新该字段（PATCH 语义）。
// UI 永远提交全量表单，所以实际走的是 PUT 语义。
type RSSUpsertPayload struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RssURL       string `json:"rss_url"`
	TargetPath   string `json:"target_path"`
	Storage      string `json:"storage"`
	MediaServer  string `json:"media_server"`
	PosterURL    string `json:"poster_url"`
	IncludeRegex string `json:"include_regex"`
	ExcludeRegex string `json:"exclude_regex"`
	MediaType    string `json:"media_type"`
	Action       string `json:"action"`
	Enabled      *bool  `json:"enabled"`
}

// RSSSourceRepository 源仓储。
type RSSSourceRepository interface {
	List(ctx context.Context, q RSSSourceQuery) ([]RSSSource, error)
	Get(ctx context.Context, id int64) (RSSSource, bool, error)
	GetByURL(ctx context.Context, rssURL string) (RSSSource, bool, error)
	Upsert(ctx context.Context, p RSSUpsertPayload) (RSSSource, error)
	Delete(ctx context.Context, id int64) error
	// MarkSyncResult 回写本轮同步的结论（时间、状态、消息）。
	MarkSyncResult(ctx context.Context, id int64, status, message string, at time.Time) error
	// ListEnabled 返回全部启用的源，供轮询逐个同步。
	ListEnabled(ctx context.Context) ([]RSSSource, error)
}

// RSSHistoryRepository 历史仓储。
type RSSHistoryRepository interface {
	// InsertOnce 写入一条历史。命中 guid 唯一约束时返回已存在的那条
	// （inserted=false），**不报错** —— 「这条已经处理过」是正常流程，
	// 不是失败。仓储内部走 ON CONFLICT(guid) DO NOTHING 判定。
	InsertOnce(ctx context.Context, h RSSHistory) (existing RSSHistory, inserted bool, err error)
	// HasGuid 判断某 guid 是否已处理过。同步前批量过滤用。
	HasGuid(ctx context.Context, guid string) (bool, error)
	List(ctx context.Context, q RSSHistoryQuery) ([]RSSHistory, error)
	// DeleteBySource 清理某源的历史。删除源时可选调用。
	DeleteBySource(ctx context.Context, sourceID int64) (int64, error)
	// DeleteByID 删单条历史 —— 语义是「把这条加入豁免列表」，
	// 下一轮同步重新处理它。
	DeleteByID(ctx context.Context, id int64) error
	// LatestProcessedAt 返回该源最近一次写入历史的时刻（= 位点）。
	// 返回零值且 found=false 表示该源从未处理过任何条目。
	LatestProcessedAt(ctx context.Context, sourceID int64) (at time.Time, found bool, err error)
}
