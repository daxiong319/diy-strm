package models

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"diy-strm/internal/db"
)

// CloudSetting 云盘级设置（每网盘一套，key-value 存储）
type CloudSetting struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	SourceType string    `gorm:"size:32;index:idx_cloud_setting_source_key,unique" json:"source_type"`
	Key        string    `gorm:"size:64;index:idx_cloud_setting_source_key,unique" json:"key"`
	Value      string    `gorm:"type:text" json:"value"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CloudSetting 设置的 key 常量
const (
	CloudSettingKeySaveDir = "save_dir" // 值：{"path":"/影视/待整理"}
)

// 影巢（HDHive）配置 key（source_type = "hdhive"）
const (
	CloudSettingKeyHiveInterval          = "poll_interval"         // 值：轮询间隔分钟数（默认 15）
	CloudSettingKeyHiveCheckinEnabled    = "daily_checkin_enabled" // 值："true"/"false" 主账号每日自动签到
	CloudSettingKeyHiveCheckinMode       = "daily_checkin_mode"    // 值：daily/gamble 主账号签到模式
	CloudSettingKeyHiveCheckinHour       = "daily_checkin_hour"    // 值：0-23 主账号签到小时
	CloudSettingKeyHiveSubCheckinEnabled = "sub_checkin_enabled"   // 值："true"/"false" 子账号每日自动签到
	CloudSettingKeyHiveSubCheckinMode    = "sub_checkin_mode"      // 值：daily/gamble 子账号签到模式
	CloudSettingKeyHiveSubCheckinHour    = "sub_checkin_hour"      // 值：0-23 子账号签到小时
	CloudSettingKeyHiveMaxPoints         = "max_points"            // 值：解锁积分上限（0=不限）
	CloudSettingKeyHiveOnlyOfficial      = "only_official"         // 值："true"/"false" 仅收官组资源（借鉴 mediavault hdhive_only_official）
	CloudSettingKeyHivePublisherWhitelist = "publisher_whitelist"  // 值：发布者昵称白名单（逗号分隔，空=不过滤）
	CloudSettingKeyHiveExecPreset        = "exec_preset"           // 值：conservative/balanced/aggressive/custom（执行强度预设）
	CloudSettingKeyHiveMaxTransfersPerRun = "max_transfers_per_run" // 值：单轮转存上限（custom 模式用，默认 5）
	CloudSettingKeyHiveTransferMinInterval = "transfer_min_interval" // 值：两次转存最小间隔秒数（custom 模式用，默认 25）
	CloudSettingKeyHiveTransferJitter    = "transfer_jitter"       // 值：转存间隔随机抖动秒数（custom 模式用，默认 15）
	CloudSettingKeyHiveSlugMaxAttempts   = "slug_max_attempts"     // 值：单个资源 slug 最大尝试次数（默认 3）
)

// HiveTransferThrottle 执行强度参数（借鉴 mediavault 三档预设）
type HiveTransferThrottle struct {
	Preset           string // conservative / balanced / aggressive / custom
	MaxTransfersPerRun int  // 单轮转存上限
	MinInterval      time.Duration // 两次转存最小间隔
	Jitter           time.Duration // 随机抖动上限
}

// GetHiveTransferThrottle 读取执行强度参数
func GetHiveTransferThrottle() HiveTransferThrottle {
	preset := hiveSettingStr(CloudSettingKeyHiveExecPreset, "balanced")
	switch preset {
	case "conservative":
		return HiveTransferThrottle{Preset: preset, MaxTransfersPerRun: 3, MinInterval: 45 * time.Second, Jitter: 20 * time.Second}
	case "aggressive":
		return HiveTransferThrottle{Preset: preset, MaxTransfersPerRun: 10, MinInterval: 10 * time.Second, Jitter: 8 * time.Second}
	case "custom":
		return HiveTransferThrottle{
			Preset:             preset,
			MaxTransfersPerRun: hiveSettingInt(CloudSettingKeyHiveMaxTransfersPerRun, 5, 1, 50),
			MinInterval:        time.Duration(hiveSettingInt(CloudSettingKeyHiveTransferMinInterval, 25, 5, 300)) * time.Second,
			Jitter:             time.Duration(hiveSettingInt(CloudSettingKeyHiveTransferJitter, 15, 0, 120)) * time.Second,
		}
	}
	// balanced（默认）
	return HiveTransferThrottle{Preset: "balanced", MaxTransfersPerRun: 5, MinInterval: 25 * time.Second, Jitter: 15 * time.Second}
}

// GetHiveOnlyOfficial 是否仅收官组资源
func GetHiveOnlyOfficial() bool {
	return hiveSettingBool(CloudSettingKeyHiveOnlyOfficial, false)
}

// GetHivePublisherWhitelist 发布者昵称白名单（逗号分隔）
func GetHivePublisherWhitelist() []string {
	raw := hiveSettingStr(CloudSettingKeyHivePublisherWhitelist, "")
	out := make([]string, 0)
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GetHiveSlugMaxAttempts 单个资源 slug 最大尝试次数
func GetHiveSlugMaxAttempts() int {
	return hiveSettingInt(CloudSettingKeyHiveSlugMaxAttempts, 3, 1, 10)
}

// SaveDirSetting 转存目录设置值
type SaveDirSetting struct {
	Path string `json:"path"`
}

// GetCloudSetting 获取云盘设置原始值
func GetCloudSetting(sourceType, key string) (string, error) {
	var s CloudSetting
	if err := db.Db.Where("source_type = ? AND key = ?", sourceType, key).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return s.Value, nil
}

// SetCloudSetting 保存云盘设置（upsert）
func SetCloudSetting(sourceType, key, value string) error {
	var s CloudSetting
	if err := db.Db.Where("source_type = ? AND key = ?", sourceType, key).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return db.Db.Create(&CloudSetting{
				SourceType: sourceType,
				Key:        key,
				Value:      value,
				UpdatedAt:  time.Now(),
			}).Error
		}
		return err
	}
	s.Value = value
	s.UpdatedAt = time.Now()
	return db.Db.Save(&s).Error
}

// GetCloudSaveDir 获取指定网盘的转存目录路径（未配置时返回空串）
func GetCloudSaveDir(sourceType string) string {
	v, err := GetCloudSetting(sourceType, CloudSettingKeySaveDir)
	if err != nil || v == "" {
		return ""
	}
	var s SaveDirSetting
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return ""
	}
	return s.Path
}

// GetCloudSaveDirWithDefault 获取转存目录路径，未配置时返回 fallback
func GetCloudSaveDirWithDefault(sourceType, fallback string) string {
	p := GetCloudSaveDir(sourceType)
	if p == "" {
		return fallback
	}
	return p
}

// SetCloudSaveDir 保存指定网盘的转存目录路径
func SetCloudSaveDir(sourceType, key, path string) error {
	v, _ := json.Marshal(SaveDirSetting{Path: path})
	return SetCloudSetting(sourceType, key, string(v))
}

// CloudChannel 云盘资源频道（TG 公开频道，每网盘可添加多个）
type CloudChannel struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	SourceType string     `gorm:"size:32;index:idx_channel_source" json:"source_type"` // 目标网盘：123 / guangyapan / pan139
	Channel    string     `gorm:"size:128;index:idx_channel_source,unique" json:"channel"` // 频道 @名（不带 @）
	Enabled    bool       `gorm:"default:true" json:"enabled"`
	LastPostID string     `gorm:"size:64" json:"last_post_id"` // 增量游标（频道帖 ID）
	LastRunAt  time.Time  `json:"last_run_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// ChannelName 归一化频道名（去掉 @ 与 URL 前缀）
func (c *CloudChannel) ChannelName() string {
	name := c.Channel
	for _, p := range []string{"https://t.me/s/", "https://t.me/", "t.me/s/", "t.me/", "@"} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimRight(name, "/")
	return name
}

// ListCloudChannels 查询频道列表
func ListCloudChannels(sourceType string) ([]CloudChannel, error) {
	var list []CloudChannel
	q := db.Db
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListEnabledCloudChannels 查询启用中的频道列表
func ListEnabledCloudChannels(sourceType string) ([]CloudChannel, error) {
	var list []CloudChannel
	if err := db.Db.Where("source_type = ? AND enabled = ?", sourceType, true).Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetCloudChannel 按 ID 查询频道
func GetCloudChannel(id uint) (*CloudChannel, error) {
	var c CloudChannel
	if err := db.Db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveCloudChannel 创建或更新频道
func SaveCloudChannel(c *CloudChannel) error {
	if c.ID == 0 {
		c.CreatedAt = time.Now()
		c.UpdatedAt = time.Now()
		return db.Db.Create(c).Error
	}
	c.UpdatedAt = time.Now()
	return db.Db.Save(c).Error
}

// DeleteCloudChannel 删除频道
func DeleteCloudChannel(id uint) error {
	return db.Db.Delete(&CloudChannel{}, id).Error
}

// CloudSubscription 资源订阅规则（TG 频道订阅 / 影巢订阅共用）
type CloudSubscription struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	SourceType   string     `gorm:"size:32;index" json:"source_type"`       // 目标网盘：123 / guangyapan / pan139
	ResourceSource string   `gorm:"size:16;index" json:"resource_source"`   // 资源来源：空=TG 频道 / hdhive=影巢
	Channel      string     `gorm:"size:128;index" json:"channel"`          // 频道 @名（不带 @）或 URL（影巢订阅为空）
	Keywords     string     `gorm:"type:text" json:"keywords"`              // JSON 数组
	TargetDir    string     `gorm:"size:512" json:"target_dir"`             // 网盘内目标目录
	MediaType    string     `gorm:"size:16" json:"media_type"`              // 选片类型：movie / tv / 空=通用订阅
	TMDBID       int64      `gorm:"index:idx_sub_tmdb" json:"tmdb_id"`
	TMDBTitle    string     `gorm:"size:256" json:"tmdb_title"`   // 选片标题快照
	Season       int        `json:"season"`                       // 选季：0=全部季 / N=第N季（仅 tv）
	TotalSeasons int        `json:"total_seasons"`                // 全部季订阅时 TMDB 当前总季数快照
	TotalEpisodes int       `json:"total_episodes"`                // TV 订阅目标总集数快照（Season>0 为该季集数；Season=0 为全剧总集数，运行时由 TMDB 刷新）
	AutoFinish   bool       `json:"auto_finish"`                  // 自动完结开关（收录完毕后自动停用订阅）
	Wash         bool       `json:"wash"`                         // 洗版开关（影片级订阅）：同片更高规格自动替换
	WashTarget   string     `gorm:"size:32" json:"wash_target"`   // 洗版目标：空=无限制 / 1080p / 4k / 4k_remux
	ReplaceOld   bool       `json:"replace_old"`                  // 洗版后旧版本处理：true=删除旧文件 / false=保留共存
	OldCount     int64      `gorm:"-" json:"old_count"`           // 待清理旧版本数（只读，由接口填充）
	Enabled      bool       `gorm:"default:true" json:"enabled"`
	Status       string     `gorm:"size:16;default:subscribing" json:"status"` // 订阅状态：subscribing（进行中）/ completed（已完结）/ paused（已暂停，跳过定时检索；借鉴 mediavault 三态状态机）
	LastPostID   string     `gorm:"size:64" json:"last_post_id"`  // 增量游标（频道帖 ID；影巢订阅为已处理资源 slug）
	FinishedAt   *time.Time `json:"finished_at"`                  // 自动完结时间
	LastRecheckAt *time.Time `json:"last_recheck_at"`             // 已完结 TV 订阅的上次 TMDB 复查时间（宽限复活用）
	LastRunAt    time.Time  `json:"last_run_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// KeywordList 解析关键词 JSON
func (s *CloudSubscription) KeywordList() []string {
	var kws []string
	if err := json.Unmarshal([]byte(s.Keywords), &kws); err != nil {
		return nil
	}
	out := make([]string, 0, len(kws))
	for _, k := range kws {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// ChannelName 归一化频道名（去掉 @ 与 URL 前缀）
func (s *CloudSubscription) ChannelName() string {
	name := s.Channel
	for _, p := range []string{"https://t.me/s/", "https://t.me/", "t.me/s/", "t.me/", "@"} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimRight(name, "/")
	return name
}

// ListCloudSubscriptions 查询订阅列表
func ListCloudSubscriptions(sourceType string) ([]CloudSubscription, error) {
	var list []CloudSubscription
	q := db.Db
	if sourceType != "" {
		q = q.Where("source_type = ?", sourceType)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListSubscriptionsByResourceSource 按资源来源查询订阅（空=TG 频道 / hdhive=影巢）
func ListSubscriptionsByResourceSource(resourceSource string) ([]CloudSubscription, error) {
	var list []CloudSubscription
	if err := db.Db.Where("resource_source = ?", resourceSource).Order("id asc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetHivePollInterval 影巢轮询间隔（分钟，默认 15）
func GetHivePollInterval() int {
	v, err := GetCloudSetting("hdhive", CloudSettingKeyHiveInterval)
	if err != nil || v == "" {
		return 15
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 5 {
		return 15
	}
	if n > 1440 {
		return 1440
	}
	return n
}

// hiveSettingBool 影巢布尔值设置读取（带默认值）
func hiveSettingBool(key string, def bool) bool {
	v, err := GetCloudSetting("hdhive", key)
	if err != nil || v == "" {
		return def
	}
	return strings.TrimSpace(v) == "true"
}

// hiveSettingInt 影巢整数设置读取（带默认值及范围限制）
func hiveSettingInt(key string, def, min, max int) int {
	v, err := GetCloudSetting("hdhive", key)
	if err != nil || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// hiveSettingStr 影巢字符串设置读取（带默认值）
func hiveSettingStr(key, def string) string {
	v, err := GetCloudSetting("hdhive", key)
	if err != nil || v == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// GetHiveCheckinEnabled 主账号每日自动签到是否启用（默认启用）
func GetHiveCheckinEnabled() bool {
	return hiveSettingBool(CloudSettingKeyHiveCheckinEnabled, true)
}

// GetHiveCheckinMode 主账号签到模式（默认 daily）
func GetHiveCheckinMode() string {
	return hiveSettingStr(CloudSettingKeyHiveCheckinMode, "daily")
}

// GetHiveCheckinHour 主账号签到小时（默认 8，范围 0-23）
func GetHiveCheckinHour() int {
	return hiveSettingInt(CloudSettingKeyHiveCheckinHour, 8, 0, 23)
}

// GetHiveSubCheckinEnabled 子账号每日自动签到是否启用（默认启用）
func GetHiveSubCheckinEnabled() bool {
	return hiveSettingBool(CloudSettingKeyHiveSubCheckinEnabled, true)
}

// GetHiveSubCheckinMode 子账号签到模式（默认 daily）
func GetHiveSubCheckinMode() string {
	return hiveSettingStr(CloudSettingKeyHiveSubCheckinMode, "daily")
}

// GetHiveSubCheckinHour 子账号签到小时（默认 8，范围 0-23）
func GetHiveSubCheckinHour() int {
	return hiveSettingInt(CloudSettingKeyHiveSubCheckinHour, 8, 0, 23)
}

// GetHiveMaxPoints 解锁积分上限（0=不限，默认 0）
func GetHiveMaxPoints() int {
	return hiveSettingInt(CloudSettingKeyHiveMaxPoints, 0, 0, 999999)
}

// SaveCloudSubscription 创建或更新订阅
func SaveCloudSubscription(s *CloudSubscription) error {
	if s.ID == 0 {
		s.CreatedAt = time.Now()
		s.UpdatedAt = time.Now()
		return db.Db.Create(s).Error
	}
	s.UpdatedAt = time.Now()
	return db.Db.Save(s).Error
}

// DeleteCloudSubscription 删除订阅
func DeleteCloudSubscription(id uint) error {
	return db.Db.Delete(&CloudSubscription{}, id).Error
}

// CreateCloudSubscription 创建订阅
func CreateCloudSubscription(s *CloudSubscription) error {
	normalizeKeywords(s)
	if s.ID != 0 {
		return UpdateCloudSubscription(s.ID, s, nil)
	}
	now := time.Now()
	s.CreatedAt = now
	s.UpdatedAt = now
	return db.Db.Create(s).Error
}

// normalizeKeywords 将关键词统一规范化为 JSON 数组存储（兼容空格分隔输入）
func normalizeKeywords(s *CloudSubscription) {
	if kws := strings.Fields(s.Keywords); len(kws) > 0 {
		if b, err := json.Marshal(kws); err == nil {
			s.Keywords = string(b)
		}
	}
}

// UpdateCloudSubscription 更新订阅字段（fields 为空时更新全部）
func UpdateCloudSubscription(id uint, req *CloudSubscription, fields map[string]bool) error {
	var old CloudSubscription
	if err := db.Db.First(&old, id).Error; err != nil {
		return err
	}
	normalizeKeywords(req)
	if len(fields) == 0 {
		fields = map[string]bool{}
		for _, k := range []string{"source_type", "resource_source", "channel", "keywords", "target_dir", "media_type", "tmdb_id", "tmdb_title", "season", "total_seasons", "total_episodes", "auto_finish", "wash", "wash_target", "replace_old", "enabled"} {
			fields[k] = true
		}
	}
	has := func(k string) bool { return fields[k] }
	if has("source_type") && req.SourceType != "" {
		old.SourceType = req.SourceType
	}
	if has("resource_source") {
		old.ResourceSource = req.ResourceSource
	}
	if has("channel") && req.Channel != "" {
		old.Channel = req.Channel
	}
	if has("keywords") && req.Keywords != "" {
		old.Keywords = req.Keywords
	}
	if has("target_dir") && req.TargetDir != "" {
		old.TargetDir = req.TargetDir
	}
	if has("media_type") {
		old.MediaType = req.MediaType
	}
	if has("tmdb_id") {
		old.TMDBID = req.TMDBID
	}
	if has("tmdb_title") {
		old.TMDBTitle = req.TMDBTitle
	}
	if has("season") {
		old.Season = req.Season
	}
	if has("total_seasons") {
		old.TotalSeasons = req.TotalSeasons
	}
	if has("total_episodes") {
		old.TotalEpisodes = req.TotalEpisodes
	}
	if has("auto_finish") {
		old.AutoFinish = req.AutoFinish
	}
	if has("wash") {
		old.Wash = req.Wash
	}
	if has("wash_target") {
		old.WashTarget = req.WashTarget
	}
	if has("replace_old") {
		old.ReplaceOld = req.ReplaceOld
	}
	if has("enabled") {
		old.Enabled = req.Enabled
	}
	return SaveCloudSubscription(&old)
}

// UpdateCloudSubscriptionEnabled 启用/禁用订阅
func UpdateCloudSubscriptionEnabled(id uint, enabled bool) error {
	var old CloudSubscription
	if err := db.Db.First(&old, id).Error; err != nil {
		return err
	}
	old.Enabled = enabled
	return SaveCloudSubscription(&old)
}

// GetCloudSubscription 按 ID 查询订阅
func GetCloudSubscription(id uint) (*CloudSubscription, error) {
	var s CloudSubscription
	if err := db.Db.First(&s, id).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// CloudTransferRecord 云盘转存记录（订阅去重与自动完结判定）
type CloudTransferRecord struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SourceType     string    `gorm:"size:32;index" json:"source_type"`
	SubscriptionID uint      `gorm:"index" json:"subscription_id"`
	MediaType      string    `gorm:"size:16" json:"media_type"`
	TMDBID         int64     `gorm:"index:idx_transfer_tmdb_season" json:"tmdb_id"`
	Season         int       `gorm:"index:idx_transfer_tmdb_season" json:"season"`
	Title          string    `gorm:"size:256" json:"title"`
	PostID         string    `gorm:"size:64" json:"post_id"`
	LinkURL        string    `gorm:"size:512;index" json:"link_url"`
	Episode        string    `gorm:"size:255" json:"episode"` // 该帖对应的剧集标识集合（逗号分隔，如 S01E13 / S01E24,S01E25…；空=未识别到集号）
TargetDir      string    `gorm:"size:512" json:"target_dir"`
	Resolution     int       `gorm:"index" json:"resolution"` // 洗版规格：0未知 1=720p 2=1080p 3=2160p(4K)
	Source         int       `gorm:"index" json:"source"`     // 0未知 1=HDTV 2=WEBRip 3=WEB-DL 4=BluRay 5=REMUX
	Codec          int       `json:"codec"`                   // 0未知 1=H264 2=H265
	Effect         int       `json:"effect"`                  // 0未知 1=SDR 2=HDR 3=Dolby Vision
	SizeGB         float64   `json:"size_gb"`                 // 体积（GB）
	Status         string    `gorm:"size:16;index" json:"status"` // 空=正常 / superseded=被洗版替换（待清理旧版本）
	CreatedAt      time.Time `json:"created_at"`
}

// CreateTransferRecord 写入一条转存记录
func CreateTransferRecord(r *CloudTransferRecord) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	return db.Db.Create(r).Error
}

// SaveTransferRecord 更新转存记录
func SaveTransferRecord(r *CloudTransferRecord) error {
	return db.Db.Save(r).Error
}

// LatestSubscriptionRecord 影片级订阅当前生效（未被替换）的最新转存记录
func LatestSubscriptionRecord(subID uint, tmdbID int64, season int) *CloudTransferRecord {
	q := db.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var r CloudTransferRecord
	if err := q.Order("created_at desc").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// SupersededRecords 订阅的待清理旧版本记录（已被洗版替换）
func SupersededRecords(subID uint) []CloudTransferRecord {
	var rs []CloudTransferRecord
	db.Db.Where("subscription_id = ? AND status = ?", subID, "superseded").Order("created_at asc").Find(&rs)
	return rs
}

// ActiveRecords 订阅当前生效（未被替换）的转存记录
func ActiveRecords(subID uint) []CloudTransferRecord {
	var rs []CloudTransferRecord
	db.Db.Where("subscription_id = ? AND status != ?", subID, "superseded").Order("created_at asc").Find(&rs)
	return rs
}

// CountSuperseded 订阅待清理旧版本数量
func CountSuperseded(subID uint) int64 {
	var cnt int64
	db.Db.Model(&CloudTransferRecord{}).Where("subscription_id = ? AND status = ?", subID, "superseded").Count(&cnt)
	return cnt
}

// DeleteTransferRecords 删除指定 ID 的转存记录
func DeleteTransferRecords(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Db.Where("id IN ?", ids).Delete(&CloudTransferRecord{}).Error
}

// HasSubscriptionRecord 影片级订阅去重：该订阅的指定片（+季）是否已转存过
func HasSubscriptionRecord(subID uint, tmdbID int64, season int) bool {
	q := db.Db.Where("subscription_id = ? AND tmdb_id = ?", subID, tmdbID)
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var cnt int64
	q.Model(&CloudTransferRecord{}).Count(&cnt)
	return cnt > 0
}

// HasLinkRecord 通用订阅去重：该分享链接是否已转存过
func HasLinkRecord(linkURL string) bool {
	var cnt int64
	db.Db.Model(&CloudTransferRecord{}).Where("link_url = ?", linkURL).Count(&cnt)
	return cnt > 0
}

// recordEpisodes 解析一条转存记录中已收录的剧集标识集合
func recordEpisodes(r *CloudTransferRecord) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(r.Episode, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

// HasEpisodeRecord 影片级订阅按集去重：给定剧集标识是否在该订阅的指定片（+季）下全部已转存。
// epKeys 为空时退化为整片判断（HasSubscriptionRecord）。
func HasEpisodeRecord(subID uint, tmdbID int64, season int, epKeys []string) bool {
	if len(epKeys) == 0 {
		return HasSubscriptionRecord(subID, tmdbID, season)
	}
	q := db.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var rs []CloudTransferRecord
	q.Find(&rs)
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

// LatestEpisodeRecord 影片级订阅指定剧集的最新有效（未被替换）转存记录
func LatestEpisodeRecord(subID uint, tmdbID int64, season int, epKey string) *CloudTransferRecord {
	q := db.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var rs []CloudTransferRecord
	q.Order("created_at desc").Find(&rs)
	for i := range rs {
		if recordEpisodes(&rs[i])[epKey] {
			return &rs[i]
		}
	}
	return nil
}

// CountDistinctEpisodes 统计订阅已转存的不同剧集数（按剧集标识去重，不含已被洗版替换的记录）
func CountDistinctEpisodes(subID uint, tmdbID int64, season int) int {
	q := db.Db.Where("subscription_id = ? AND tmdb_id = ? AND status != ?", subID, tmdbID, "superseded")
	if season > 0 {
		q = q.Where("season = ?", season)
	}
	var rs []CloudTransferRecord
	q.Find(&rs)
	seen := map[string]bool{}
	for i := range rs {
		for k := range recordEpisodes(&rs[i]) {
			seen[k] = true
		}
	}
	return len(seen)
}

// ListTransferRecords 分页查询订阅的转存记录（含已被洗版替换的旧记录）
func ListTransferRecords(subID uint, page, pageSize int) ([]CloudTransferRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := db.Db.Model(&CloudTransferRecord{}).Where("subscription_id = ?", subID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rs []CloudTransferRecord
	if err := db.Db.Where("subscription_id = ?", subID).
		Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rs).Error; err != nil {
		return nil, 0, err
	}
	return rs, total, nil
}

// CountSubscriptionRecords 统计订阅已收录数量
func CountSubscriptionRecords(subID uint) int64 {
	var cnt int64
	db.Db.Model(&CloudTransferRecord{}).Where("subscription_id = ?", subID).Count(&cnt)
	return cnt
}

// DeleteSubscriptionRecords 删除订阅时清理其转存记录
func DeleteSubscriptionRecords(subID uint) error {
	return db.Db.Where("subscription_id = ?", subID).Delete(&CloudTransferRecord{}).Error
}
