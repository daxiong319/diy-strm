package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"litepan/internal/discover/ddb"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// TG 频道订阅 watcher 的数据层（对齐参考实现 media_subscriptions + monitors 体系：
// 频道表存增量游标 → 转存记录表做去重与洗版基线 → 监控历史表做全量审计展示）
// 本文件只负责持久化语义，watcher 引擎/API/前端另行实现。
// ---------------------------------------------------------------------------

// DiscoveryChannel TG 公开频道（每网盘可添加多个；watcher 按游标增量拉帖）
type DiscoveryChannel struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	SourceType string    `gorm:"size:32;index:idx_disc_channel_source" json:"source_type"`     // 目标网盘：123 / guangya / pan139
	Channel    string    `gorm:"size:128;index:idx_disc_channel_source,unique" json:"channel"` // 频道名（不带 @）
	Enabled    bool      `gorm:"default:true" json:"enabled"`                                  // 停用后 watcher 跳过，但保留历史游标便于恢复
	LastPostID string    `gorm:"size:64" json:"last_post_id"`                                  // 增量游标（频道帖 ID）
	LastRunAt  time.Time `json:"last_run_at"`
	// NextCatchupAt 追赶进度保护的下一次可抽检时间（停机过久跳最新后，分批回补中间积压帖）。
	NextCatchupAt time.Time `json:"next_catchup_at"`
	// CatchupCheckpoints 追赶开始时冻结的「上一游标」快照（JSON 数组字符串）。
	//
	// 必须与 LastPostID 分开保存：追赶期间 LastPostID 会持续推进（转存失败回退更深的帖
	// 靠它下次重扫），若借用 LastPostID 记起点，回退的帖会被推进到已处理区而永久丢失。
	CatchupCheckpoints string    `gorm:"size:512" json:"catchup_checkpoints"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (DiscoveryChannel) TableName() string { return "discovery_channels" }

// ChannelName 归一化频道名（去掉 @ 与 URL 前缀）。
// 用户粘 t.me 链接与手输 @名 都很常见，统一归一化后唯一索引才拦得住重复添加。
func (c *DiscoveryChannel) ChannelName() string {
	name := c.Channel
	for _, p := range []string{"https://t.me/s/", "https://t.me/", "t.me/s/", "t.me/", "@"} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimRight(name, "/")
	return name
}

// ListChannels 查询频道列表（sourceType 为空 = 全部网盘）
func ListChannels(sourceType string) ([]DiscoveryChannel, error) {
	var list []DiscoveryChannel
	q := ddb.Db
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListEnabledChannels 查询启用中的频道列表（watcher 每轮只跑这些）
func ListEnabledChannels(sourceType string) ([]DiscoveryChannel, error) {
	var list []DiscoveryChannel
	q := ddb.Db.Where("enabled = ?", true)
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetChannel 按 ID 查询频道
func GetChannel(id uint) (*DiscoveryChannel, error) {
	var c DiscoveryChannel
	if err := ddb.Db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetChannelBySource 按（网盘, 频道名）查询；频道名按归一化后匹配。
// 库里可能同时存着 "xxx"、"@xxx"、"t.me/xxx" 三种写法（历史数据或手工插入），
// 所以取出该网盘的候选再逐一归一化比对，而不能直接等值查。
func GetChannelBySource(sourceType, channel string) (*DiscoveryChannel, error) {
	var list []DiscoveryChannel
	if err := ddb.Db.Where("source_type = ?", sourceType).Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	want := (&DiscoveryChannel{Channel: channel}).ChannelName()
	for i := range list {
		if list[i].ChannelName() == want {
			return &list[i], nil
		}
	}
	return nil, nil
}

// SaveChannel 创建或更新频道（ID 为 0 则新建）
func SaveChannel(ch *DiscoveryChannel) error {
	now := time.Now()
	if ch.ID == 0 {
		if ch.CreatedAt.IsZero() {
			ch.CreatedAt = now
		}
		ch.UpdatedAt = now
		return ddb.Db.Create(ch).Error
	}
	ch.UpdatedAt = now
	return ddb.Db.Save(ch).Error
}

// DeleteChannel 删除频道
func DeleteChannel(id uint) error {
	return ddb.Db.Delete(&DiscoveryChannel{}, id).Error
}

// SetChannelEnabled 启用/停用频道
func SetChannelEnabled(id uint, enabled bool) error {
	return ddb.Db.Model(&DiscoveryChannel{}).Where("id = ?", id).
		Updates(map[string]any{"enabled": enabled, "updated_at": time.Now()}).Error
}

// UpdateChannelCursor 只推进增量游标与最近运行时间。
// 单独成法是为了避免 watcher 并发运行回写整个结构体、把用户在 UI 上刚改的
// enabled / channel 覆盖回旧值。
func UpdateChannelCursor(id uint, lastPostID string, runAt time.Time) error {
	if runAt.IsZero() {
		runAt = time.Now()
	}
	return ddb.Db.Model(&DiscoveryChannel{}).Where("id = ?", id).
		Updates(map[string]any{"last_post_id": lastPostID, "last_run_at": runAt, "updated_at": time.Now()}).Error
}

// ---------------------------------------------------------------------------
// 转存记录：TG 频道订阅只记成功转存，用于「该链接/该片该集是否已转存」去重，
// 以及洗版时标记被替换的旧记录。监控历史表则记录每一次尝试（含失败/跳过）。
// ---------------------------------------------------------------------------

// DiscoveryTransferRecord 转存记录（去重 + 洗版基线）
type DiscoveryTransferRecord struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SourceType     string    `gorm:"size:32;index" json:"source_type"`
	SubscriptionID uint      `gorm:"index" json:"subscription_id"`
	MediaType      string    `gorm:"size:16" json:"media_type"`
	TMDBID         int64     `gorm:"index:idx_disc_tr_tmdb_season" json:"tmdb_id"`
	Season         int       `gorm:"index:idx_disc_tr_tmdb_season" json:"season"`
	Title          string    `gorm:"size:256" json:"title"`
	PostID         string    `gorm:"size:64" json:"post_id"`
	LinkURL        string    `gorm:"size:512;index" json:"link_url"`
	Episode        string    `gorm:"size:255" json:"episode"` // 剧集标识集合，逗号分隔，如 S01E13 或 S01E24,S01E25；空=未识别
	TargetDir      string    `gorm:"size:512" json:"target_dir"`
	Resolution     int       `gorm:"index" json:"resolution"` // 洗版规格：0未知 1=720p 2=1080p 3=2160p
	Source         int       `gorm:"index" json:"source"`     // 0未知 1=HDTV 2=WEBRip 3=WEB-DL 4=BluRay 5=REMUX
	Codec          int       `json:"codec"`                   // 0未知 1=H264 2=H265
	Effect         int       `json:"effect"`                  // 0未知 1=SDR 2=HDR 3=DolbyVision
	SizeGB         float64   `json:"size_gb"`
	Status         string    `gorm:"size:16;index" json:"status"` // 空=正常 / superseded=被洗版替换（待清理旧版本）
	// IdempotencyKey 转存幂等键（唯一，仅对非空值生效）：同一订阅对同一资源只允许成功转存一次。
	// 由 TransferIdempotencyKey 按「订阅 + 资源身份」生成，走唯一索引在并发下兜底去重 ——
	// 单纯 query-then-insert（如 HasLinkRecord）在定时器与手动「立即搜索」同跑时有竞态窗口。
	//
	// 注意：用 where:"idempotency_key <> ''" 约束成**部分索引**。SQLite 的 UNIQUE 把空串
	// 视为相等值，普通唯一索引会导致「第二条无幂等键的记录」插入失败（实测
	// `constraint failed: UNIQUE constraint failed: ...idempotency_key`）——
	// 而无幂等键的记录是合法的（资源身份缺失时退化为不去重）。
	IdempotencyKey string `gorm:"size:64;index:idx_disc_transfer_idem,unique,where:idempotency_key <> ''" json:"idempotency_key"`
	// TransferState 转存三段状态机：requested（已发起，落库占位）→ confirmed（转存成功）/ failed（失败可重试）。
	TransferState string    `gorm:"size:16;index" json:"transfer_state"`
	CreatedAt     time.Time `json:"created_at"`
}

func (DiscoveryTransferRecord) TableName() string { return "discovery_transfer_records" }

// CreateTransferRecord 写入一条转存记录
func CreateTransferRecord(r *DiscoveryTransferRecord) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	return ddb.Db.Create(r).Error
}

// SaveTransferRecord 更新转存记录
func SaveTransferRecord(r *DiscoveryTransferRecord) error {
	return ddb.Db.Save(r).Error
}

// HasLinkRecord 通用订阅去重：该分享链接是否已转存过
func HasLinkRecord(linkURL string) bool {
	var cnt int64
	if err := ddb.Db.Model(&DiscoveryTransferRecord{}).Where("link_url = ?", linkURL).Count(&cnt).Error; err != nil {
		return false
	}
	return cnt > 0
}

// ---------------------------------------------------------------------------
// 转存幂等（占位 → 确认 / 失败）
//
// HasLinkRecord 这类 query-then-insert 去重在并发下有竞态窗口：定时器一轮与用户手动
// 「立即搜索」同时跑到同一条资源，两边都查到「不存在」，然后都去转存。故引入幂等键 +
// 唯一索引，把「是否已转存」的判定交给数据库的原子插入。
// ---------------------------------------------------------------------------

// 转存三段状态机取值
const (
	// TransferStateRequested 已发起转存（落库占位，尚未确认结果）
	TransferStateRequested = "requested"
	// TransferStateConfirmed 转存已确认成功
	TransferStateConfirmed = "confirmed"
	// TransferStateFailed 转存失败（可重试；占位记录保留以便重试时复用）
	TransferStateFailed = "failed"
)

// ErrTransferDuplicate 该资源已转存过（幂等键命中），调用方应跳过而非报错
var ErrTransferDuplicate = errors.New("该资源已转存过（幂等键命中）")

// TransferIdempotencyKey 转存幂等键：同一订阅对同一资源只允许成功转存一次。
//
// 组成（用 | 连接后取 sha256 前 16 字节的十六进制）：
//
//	订阅 ID | 媒体类型 | TMDB ID | 季 | 资源身份
//
// 资源身份优先用分享链接（同一资源换帖重发也能拦住），链接为空时回落「频道|帖 ID」；
// 两者都空则返回空串（无身份可比，退化为不去重）。
// 刻意不带集号：同一帖的多集共用一次转存；洗版升级由 Status=superseded 与洗版分支
// 另行处理，不靠幂等键，否则升级会被误判为重复转存而拒绝。
func TransferIdempotencyKey(subID uint, mediaType string, tmdbID int64, season int, linkURL, channel, postID string) string {
	identity := strings.TrimSpace(linkURL)
	if identity == "" {
		identity = strings.TrimSpace(channel) + "|" + strings.TrimSpace(postID)
	}
	if identity == "" {
		return ""
	}
	raw := fmt.Sprintf("%d|%s|%d|%d|%s", subID, strings.TrimSpace(mediaType), tmdbID, season, identity)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

// ReserveTransfer 转存前占位：把幂等键以 requested 状态落库。
//
// 唯一索引 idx_disc_transfer_idem 在并发（定时器与手动「立即搜索」同跑）下兜底：
// 重复插入即重复转存，返回已有的记录与 ErrTransferDuplicate，调用方据此跳过本次。
// 返回的占位记录带 ID，后续由 ConfirmTransfer / FailTransfer 推进状态。
func ReserveTransfer(r *DiscoveryTransferRecord) (*DiscoveryTransferRecord, error) {
	if r.IdempotencyKey == "" {
		// 无幂等键（资源身份缺失）：退化为普通写入，不阻断转存
		return r, CreateTransferRecord(r)
	}
	if existing, err := FindTransferByIdempotencyKey(r.IdempotencyKey); err == nil && existing != nil {
		return existing, ErrTransferDuplicate
	}
	r.TransferState = TransferStateRequested
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if err := ddb.Db.Create(r).Error; err != nil {
		// 唯一索引冲突（并发插入撞车）→ 查明是同键即视为重复
		if existing, ferr := FindTransferByIdempotencyKey(r.IdempotencyKey); ferr == nil && existing != nil {
			return existing, ErrTransferDuplicate
		}
		return nil, err
	}
	return r, nil
}

// ConfirmTransfer 把占位记录推进为 confirmed（转存成功）
func ConfirmTransfer(id uint) error {
	if id == 0 {
		return nil
	}
	return ddb.Db.Model(&DiscoveryTransferRecord{}).Where("id = ?", id).
		Update("transfer_state", TransferStateConfirmed).Error
}

// FailTransfer 把占位记录推进为 failed（转存失败，可重试）
func FailTransfer(id uint) error {
	if id == 0 {
		return nil
	}
	return ddb.Db.Model(&DiscoveryTransferRecord{}).Where("id = ?", id).
		Update("transfer_state", TransferStateFailed).Error
}

// FindTransferByIdempotencyKey 按幂等键查转存记录（不存在返回 nil, nil）
func FindTransferByIdempotencyKey(key string) (*DiscoveryTransferRecord, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	var rec DiscoveryTransferRecord
	err := ddb.Db.Where("idempotency_key = ?", key).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// HasSubscriptionRecord 影片级订阅去重：该订阅的指定片（+季）是否已转存过。
// 已被洗版替换（superseded）的记录视为待清理旧版本，不算「已转存」，否则洗版后新版本永远进不来。
func HasSubscriptionRecord(subID uint, tmdbID int64, season int) bool {
	q := ddb.Db.Model(&DiscoveryTransferRecord{}).
		Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var cnt int64
	if err := q.Count(&cnt).Error; err != nil {
		return false
	}
	return cnt > 0
}

// recordEpisodes 解析一条转存记录中已收录的剧集标识集合
func recordEpisodes(r *DiscoveryTransferRecord) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(r.Episode, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

// HasEpisodeRecord 影片级订阅按集去重：给定剧集标识是否在该订阅的指定片（+季）下全部已转存。
// 逐集订阅场景下一帖常含多集（S01E24,S01E25），且集号可能分散在多条记录里，
// 所以要把所有非 superseded 记录的 Episode 求并集后判断，而不是只看某一条。
// epKeys 为空时退化为整片判断（HasEpisodeRecord）。
func HasEpisodeRecord(subID uint, tmdbID int64, season int, epKeys []string) bool {
	if len(epKeys) == 0 {
		return HasSubscriptionRecord(subID, tmdbID, season)
	}
	q := ddb.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var rs []DiscoveryTransferRecord
	if err := q.Find(&rs).Error; err != nil {
		return false
	}
	done := map[string]bool{}
	for i := range rs {
		for k := range recordEpisodes(&rs[i]) {
			done[k] = true
		}
	}
	for _, k := range epKeys {
		if !done[k] {
			return false
		}
	}
	return true
}

// LatestSubscriptionRecord 影片级订阅当前生效（未被替换）的最新转存记录
func LatestSubscriptionRecord(subID uint, tmdbID int64, season int) *DiscoveryTransferRecord {
	q := ddb.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var r DiscoveryTransferRecord
	if err := q.Order("created_at desc").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// LatestEpisodeRecord 影片级订阅指定剧集的最新有效（未被替换）转存记录
func LatestEpisodeRecord(subID uint, tmdbID int64, season int, epKey string) *DiscoveryTransferRecord {
	q := ddb.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var rs []DiscoveryTransferRecord
	if err := q.Order("created_at desc").Find(&rs).Error; err != nil {
		return nil
	}
	for i := range rs {
		if recordEpisodes(&rs[i])[epKey] {
			return &rs[i]
		}
	}
	return nil
}

// SupersedeTransferRecords 把同片同季中除 keepIDs 外的记录标记为 superseded（洗版用）。
// keepIDs 是本轮新转存进来的版本；旧版本保留行（含目标目录）以便清理任务回删网盘文件，
// 但不再参与去重判断。返回受影响行数。
func SupersedeTransferRecords(subID uint, tmdbID int64, season int, keepIDs []uint) (int64, error) {
	q := ddb.Db.Model(&DiscoveryTransferRecord{}).
		Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	if len(keepIDs) > 0 {
		q = q.Where("id NOT IN ?", keepIDs)
	}
	res := q.Update("status", "superseded")
	return res.RowsAffected, res.Error
}

// ---------------------------------------------------------------------------
// 监控历史：三类监控入口（频道订阅/RE0订阅/TG机器人）的每一次转存尝试留痕，
// 供前端列表、筛选与来源角标统计。
// ---------------------------------------------------------------------------

// DiscoveryMonitorRecord 监控历史（全量审计，含成功/失败/跳过/洗版）
type DiscoveryMonitorRecord struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SourceType     string    `gorm:"size:32;index:idx_disc_mon_source_time" json:"source_type"`
	Entry          string    `gorm:"size:16;index" json:"entry"` // channel=TG频道订阅 / hive=RE0订阅 / bot=TG机器人
	Channel        string    `gorm:"size:128" json:"channel"`
	MessageID      string    `gorm:"size:64" json:"message_id"` // 帖子 ID / RE0 资源 slug
	MessageURL     string    `gorm:"size:512" json:"message_url"`
	TargetURL      string    `gorm:"size:512" json:"target_url"`
	TransferStatus string    `gorm:"size:32;index" json:"transfer_status"`
	TransferTime   time.Time `gorm:"index:idx_disc_mon_source_time" json:"transfer_time"`
	TransferResult string    `gorm:"type:text" json:"transfer_result"`
	Title          string    `gorm:"size:256" json:"title"`
	Total          int       `json:"total"`
	TargetDir      string    `gorm:"size:512" json:"target_dir"`
	SubscriptionID uint      `gorm:"index" json:"subscription_id"`
	TMDBID         int64     `gorm:"index" json:"tmdb_id"`
	MediaType      string    `gorm:"size:16" json:"media_type"`
	Season         string    `gorm:"size:16" json:"season"`
	Episode        string    `gorm:"size:32" json:"episode"`
	CreatedAt      time.Time `json:"created_at"`
}

func (DiscoveryMonitorRecord) TableName() string { return "discovery_monitor_records" }

// 监控历史状态常量（沿用中文取值，前端可直接展示、无需再映射）
const (
	MonitorStatusSuccess = "转存成功"
	MonitorStatusFailed  = "转存失败"
	MonitorStatusSkipped = "已跳过"
	MonitorStatusWash    = "洗版替换"
)

// MonitorRecordExists 该 (订阅, 消息链接) 是否已有监控历史记录。
// 用于回溯模式逐帖留痕的防重复：同一帖子只写一次跳过原因，重复执行不刷屏。
func MonitorRecordExists(subscriptionID uint, messageURL string) bool {
	if subscriptionID == 0 || strings.TrimSpace(messageURL) == "" {
		return false
	}
	var cnt int64
	if err := ddb.Db.Model(&DiscoveryMonitorRecord{}).
		Where("subscription_id = ? AND message_url = ?", subscriptionID, messageURL).
		Count(&cnt).Error; err != nil {
		return false
	}
	return cnt > 0
}

// CreateMonitorTransferRecord 写入一条监控历史（对调用方返回错误，由调用方决定是否仅记日志）
func CreateMonitorTransferRecord(r *DiscoveryMonitorRecord) error {
	if r.TransferTime.IsZero() {
		r.TransferTime = time.Now()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	return ddb.Db.Create(r).Error
}

// ListMonitorRecords 分页查询监控历史。
// sourceType 为空 = 全部网盘；status 为空 = 全部状态；keyword 模糊匹配标题/频道/结果描述。
func ListMonitorRecords(sourceType, status, keyword string, page, pageSize int) ([]DiscoveryMonitorRecord, int64, error) {
	q := ddb.Db.Model(&DiscoveryMonitorRecord{})
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if status != "" {
		q = q.Where("transfer_status = ?", status)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title LIKE ? OR channel LIKE ? OR transfer_result LIKE ?", like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	var records []DiscoveryMonitorRecord
	// 同时刻记录（批量转存）靠 id 兜底排序，避免分页翻页时出现重复/漏项
	if err := q.Order("transfer_time DESC, id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// CountMonitorRecordsBySource 各来源（网盘）的记录数（用于来源标签角标）
func CountMonitorRecordsBySource() (map[string]int64, error) {
	type srcRow struct {
		SourceType string
		Cnt        int64
	}
	var rows []srcRow
	if err := ddb.Db.Model(&DiscoveryMonitorRecord{}).
		Select("source_type AS source_type, COUNT(*) AS cnt").
		Group("source_type").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.SourceType] = row.Cnt
	}
	return out, nil
}

// DeleteMonitorRecords 删除指定 ID 的监控历史
func DeleteMonitorRecords(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return ddb.Db.Where("id IN ?", ids).Delete(&DiscoveryMonitorRecord{}).Error
}

// ClearMonitorRecords 清理监控历史（source 为空 = 全部；startDate/endDate 可选，按转存时间过滤）
func ClearMonitorRecords(sourceType string, startDate, endDate *time.Time) (int64, error) {
	q := ddb.Db.Where("1 = 1")
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if startDate != nil {
		q = q.Where("transfer_time >= ?", *startDate)
	}
	if endDate != nil {
		q = q.Where("transfer_time <= ?", *endDate)
	}
	res := q.Delete(&DiscoveryMonitorRecord{})
	return res.RowsAffected, res.Error
}
