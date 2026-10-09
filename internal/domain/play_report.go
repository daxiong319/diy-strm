package domain

import "context"

// T11 观影报告领域模型。
//
// 口径全部照搬 参考实现 §2，不自创：
//
//   - 周期 7 / 14 / 30 天；
//   - 数据来源 自动 / Emby / Vyo / Plex；
//   - 「忽略播放时长低于 N 秒」：**低于阈值的记录不进入排行，
//     刚好等于阈值的记录仍然保留**，默认 0 秒（不过滤）；
//   - 统计**从启用插件起累计，之前不补算；停用期间的播放同样不记录**；
//   - 生成图片并推送，没勾选渠道时**当场提示、不假装已发送**。
//
// 播放次数的定义：**关掉播放器重开，或中断超过 mo_play_report_gap_minutes
// （默认 30 分钟）才算新的一次**；同一次播放里的多段记录合并为一次。
//
// ⚠️ 汇总逻辑不在本包，而在 internal/playmonitor/report.go 的纯函数
// BuildReport：口径有六条验收，纯函数才能逐条单测。

// PlayCountGapDefault 默认的中断间隔阈值（分钟）。
const PlayCountGapDefault = 30

// PlayReportPeriodDefault 报告默认周期（天）。
const PlayReportPeriodDefault = PlayReportPeriod7

// 数据来源口径。litepan 本期只做 Emby 自动链路 + 0028 遗留 Emby 记录，
// Vyo/Plex 保留取值以对齐 参考实现，值本身在报告里会原样显示。
const (
	PlayReportSourceAuto = "auto"
	PlayReportSourceEmby = "emby"
	PlayReportSourceVyo  = "vyo"
	PlayReportSourcePlex = "plex"
)

// PlayReportSourceValid 数据来源是否合法。
func PlayReportSourceValid(s string) bool {
	switch s {
	case PlayReportSourceAuto, PlayReportSourceEmby, PlayReportSourceVyo, PlayReportSourcePlex:
		return true
	}
	return false
}

// PlayReportPeriod 报告周期：7 / 14 / 30 天（照搬 参考实现 §2）。
type PlayReportPeriod int

const (
	PlayReportPeriod7  PlayReportPeriod = 7
	PlayReportPeriod14 PlayReportPeriod = 14
	PlayReportPeriod30 PlayReportPeriod = 30
)

// Valid 周期是否在支持范围内。
func (p PlayReportPeriod) Valid() bool {
	return p == PlayReportPeriod7 || p == PlayReportPeriod14 || p == PlayReportPeriod30
}

// Days 周期天数。
func (p PlayReportPeriod) Days() int { return int(p) }

// PlayReportPeriods 支持的周期列表，供 API 与前端共用。
func PlayReportPeriods() []PlayReportPeriod {
	return []PlayReportPeriod{PlayReportPeriod7, PlayReportPeriod14, PlayReportPeriod30}
}

// PlayReportTopDefault 默认取前多少位用户（参考实现 默认 Top 10）。
const PlayReportTopDefault = 10

// PlayReportTopMax 生成图片时最多画多少行（参考实现 的 min(limit,5)）。
const PlayReportTopMax = 5

// PlayReportRule 观影报告里的一行：同一用户、同一媒体条目。
type PlayReportRule struct {
	AppUserID      int64  `json:"app_user_id"`
	EmbyUserID     string `json:"emby_user_id"`
	UserName       string `json:"user_name"`
	ItemName       string `json:"item_name"`
	ItemScope      string `json:"item_scope"`
	Provider       string `json:"provider"`
	AppSource      string `json:"app_source"`
	Count          int    `json:"count"`
	WatchedSeconds int    `json:"watched_seconds"`
	// UploadedBytes 该条目本周期内的上行字节估算值。
	// 只有「计费中」的播放计入，CDN 直连与局域网一律按 0 处理。
	UploadedBytes int64  `json:"uploaded_bytes"`
	FirstAt       string `json:"first_at"`
	LastAt        string `json:"last_at"`
}

// PlayReport 观影报告里的一位用户。
type PlayReport struct {
	AppUserID      int64            `json:"app_user_id"`
	EmbyUserID     string           `json:"emby_user_id"`
	UserName       string           `json:"user_name"`
	Count          int              `json:"count"`
	WatchedSeconds int              `json:"watched_seconds"`
	UploadedBytes  int64            `json:"uploaded_bytes"`
	Items          []PlayReportRule `json:"items"`
	FirstAt        string           `json:"first_at"`
	LastAt         string           `json:"last_at"`
}

// PlayReportResult 一次报告生成的完整结果。
type PlayReportResult struct {
	// Since/Until 是**实际生效**的统计区间。它取的是
	// max(周期起点, 插件启用时刻) —— 启用前的播放不补算，
	// 所以启用不到一个周期时，实际区间会比标称周期短。
	Since  string `json:"since"`
	Until  string `json:"until"`
	Period int    `json:"period"`
	// Source 数据来源口径（auto/emby/vyo/plex）。
	Source string `json:"source"`
	// MinSeconds 「忽略播放时长低于 N 秒」阈值。
	MinSeconds int `json:"min_seconds"`
	// GapMinutes 播放次数的合并窗口：中断超过该分钟数算新的一次。
	GapMinutes int `json:"gap_minutes"`
	// Top 本次实际取前多少位用户。
	Top int `json:"top"`
	// EnabledSince 插件启用时刻（RFC3339）。前端要显示它，
	// 让用户知道「从启用起累计」是从什么时候开始算的。
	EnabledSince string `json:"enabled_since"`
	// Users 按播放次数降序的用户排行。
	Users []PlayReport `json:"users"`
	// Notices 统计口径说明，逐条由服务端下发。
	//
	// 前端必须原样渲染这几行 —— 口径是照搬 参考实现 的，
	// 让前端自己重写一遍就等于凭空多出第二套口径。
	Notices []string `json:"notices"`
	// TotalCount / TotalWatchedSeconds / TotalUploadedBytes 全量合计
	// （不受 Top 截断影响）。
	TotalCount          int   `json:"total_count"`
	TotalWatchedSeconds int   `json:"total_watched_seconds"`
	TotalUploadedBytes  int64 `json:"total_uploaded_bytes"`
}

// PlayReportRepository 观影报告的读侧仓储。
//
// ⚠️ 这里**刻意不提供** "汇总" 方法：观影报告必须先把区间内的记录
// **一次性全量取回**，在内存里按 gapMinutes 把相邻记录拆成不同的
// 「一次播放」；SQL 层分页会把同一次播放劈成两半、算重次数。
// 所以读侧只给全量区间读取，汇总逻辑是纯函数（见上）。
type PlayReportRepository interface {
	// ListRange 按时间升序取回区间内的全部记录（不分页）。
	ListRange(ctx context.Context, q PlaybackRecordQuery) ([]PlaybackRecord, error)
}

// PlayReportRuleContext 生成一份报告时的过滤参数（API → Service）。
type PlayReportRuleContext struct {
	Period     PlayReportPeriod
	Top        int
	Source     string
	AppUserID  int64
	EmbyUserID string
	Keyword    string
	Provider   string
	MinSeconds int
	GapMinutes int
}
