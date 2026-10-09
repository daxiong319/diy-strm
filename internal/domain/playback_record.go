package domain

import "context"

// PlaybackRecord 一条 Emby 302 反代播放记录（对齐老版
// internal/models/emby_playback_record.go）。字段与前端 DTO 一一对应，
// 由 internal/store/playback_record_repo.go 落主库 playback_records 表。
type PlaybackRecord struct {
	ID     int64  `json:"id"`
	RuleID string `json:"rule_id"` // 反代规则 ID（老版单实例固定 "1"）
	// EmbyUserID 是 0028 遗留字段 user_id 的原样语义：Emby UserId（字符串）。
	//
	// T11 的迁移 0043 没有把它改成 INTEGER —— 那会作废全部存量数据。
	// litepan RBAC 侧的用户 ID 走新增的 AppUserID（int64，0 = 未知）。
	// 这是一处**真实设计冲突**，双标识并存是刻意的，不是过渡态。
	EmbyUserID string `json:"emby_user_id"`
	AppUserID  int64  `json:"app_user_id"`
	Client     string `json:"client"`    // 播放端 Client 标识
	DeviceID   string `json:"device_id"` // 播放设备 ID
	ItemName   string `json:"item_name"` // 媒体文件名（STRM path 基名）
	StrmPath   string `json:"strm_path"` // 完整云盘路径
	Provider   string `json:"provider"`  // 网盘标识（115/123/guangya/baidu/139/openlist）
	PlaybackAt string `json:"playback_at"`
	// ── 以下为迁移 0043 新增的播放监控字段（litepan 侧命名，语义对齐 参考实现
	// 的 source_type/media_name/media_source_id）──
	RequestType    string  `json:"request_type"` // stream=流代理（计费中）/ redirect=302（CDN 直连）
	RequestURL     string  `json:"request_url"`
	OriginalURL    string  `json:"original_url"`
	UserAgent      string  `json:"user_agent"`
	ClientIP       string  `json:"client_ip"`
	ResponseStatus int     `json:"response_status"`
	ResponseTime   float64 `json:"response_time"`
	StorageSlug    string  `json:"storage_slug"`
	StorageType    string  `json:"storage_type"`
	Timestamp      string  `json:"timestamp"`
	ItemScope      string  `json:"item_scope"`
	AppSource      string  `json:"app_source"`
	AppClientIP    string  `json:"app_client_ip"`
	AppItemName    string  `json:"app_item_name"`
	AppStrmPath    string  `json:"app_strm_path"`
	WatchedSeconds int     `json:"watched_seconds"`
	// State 播放三态，由 request_type 在存储层推导（见 PlayStateFromRequestType）。
	State PlayState `json:"state"`
	// Metered 由 State 推导，不独立存储，避免库里存出矛盾的两套标记。
	Metered bool `json:"metered"`
	// UploadedBytes 该记录对应的上行字节估算值（只有计费中才 > 0）。
	UploadedBytes int64 `json:"uploaded_bytes"`
}

// PlaybackRecordQuery 播放记录分页查询条件。空字段表示该维度不限。
type PlaybackRecordQuery struct {
	RuleID    string
	UserID    string // 匹配 0028 遗留的 user_id（Emby 侧标识）
	AppUserID int64  // 匹配 0043 新增的 app_user_id（RBAC 侧）
	Keyword   string
	Provider  string
	AppSource string
	State     PlayState
	Since     string // "2006-01-02"，含端点；空串不限
	Until     string
	Page      int
	PageSize  int
}

// PlaybackRecordStats 播放记录概览统计。
type PlaybackRecordStats struct {
	Total     int64  `json:"total"`
	LastAt    string `json:"last_at"`
	UserCount int64  `json:"user_count"`
	ItemCount int64  `json:"item_count"`
}

// PlaybackRecordRepository 持久化 Emby 302 播放记录。
// 读取走主库读连接池，写入走写连接池，避免与主库写连接争用。
type PlaybackRecordRepository interface {
	// Insert 落一条播放记录，返回自增 ID。
	Insert(ctx context.Context, rec *PlaybackRecord) (int64, error)
	// List 分页查询，按播放时间倒序，返回当前页与命中总数。
	List(ctx context.Context, q PlaybackRecordQuery) ([]PlaybackRecord, int64, error)
	// ListRange 按时间倒序取出区间内的全部记录（观影报告汇总用，不分页）。
	ListRange(ctx context.Context, q PlaybackRecordQuery) ([]PlaybackRecord, error)
	// Delete 按 ID 删除一条，不存在时返回 nil（幂等）。
	Delete(ctx context.Context, id int64) error
	// Clear 按条件清空，返回删除条数；ruleID/userID 均为空时清空全部。
	Clear(ctx context.Context, ruleID, userID string) (int64, error)
	// Stats 返回概览统计。
	Stats(ctx context.Context) (PlaybackRecordStats, error)
}

// request_type 的取值。与 internal/playback.PickAction 的 Action 一一对应：
// ActionStream → RequestTypeStream；ActionRedirect → RequestTypeRedirect。
const (
	// PlaybackRequestTypeStream 字节流经自己服务器（流代理分支）。
	PlaybackRequestTypeStream = "stream"
	// PlaybackRequestTypeRedirect 302 跳转到网盘直链，CDN 直连、流量计 0。
	PlaybackRequestTypeRedirect = "redirect"
)

// PlayStateFromRequestType 由 request_type 推导播放三态。
//
// 映射规则：
//
//	stream   → 计费中（外网时）—— 字节流经过自己的服务器
//	redirect → CDN 直连 —— 302 到网盘直链，服务器一个字节都没吐，**计 0**
//
// 内网（局域网）无法从单条记录的 request_type 看出，
// 因此历史记录里的三态只区分「计费中 / CDN 直连」，
// 局域网判定只在实时会话阶段做（那里拿得到客户端 IP）。
func PlayStateFromRequestType(requestType string) PlayState {
	switch requestType {
	case PlaybackRequestTypeRedirect:
		return PlayStateCDN
	default:
		return PlayStateMetered
	}
}
