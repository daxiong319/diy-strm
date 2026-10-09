// 洗版（media upgrade）的数据模型。
//
// 核心不变式：**判定与提交分离**。
// 扫描只把结论写进 media_upgrade_records（status=pending），一个文件都不碰；
// 真正删/移文件发生在 ExecuteScan，并且要过两道闸：
//   1. 提交锁：把 status 从 pending 原子改成 executing，RowsAffected==1 才拿到执行权。
//   2. 快照复核：重新枚举该集当前的库内文件，重算 sha256，
//      和扫描时写进 snapshot_hash 的对不上就整条作废（expired），绝不按过期结论删文件。
//
// 为什么必须这样拆：洗版会删用户文件，而扫描与提交之间可能隔着几小时甚至几天。
// 这期间用户手动换过版本、Emby 重扫过、订阅又转存进了同名文件，都很正常。
// 一条「基于旧世界」的删除结论在「新世界」里执行就是在删错文件，且不可撤销。

package mediaupgrade

import "time"

// ---- 扫描任务状态 ----
const (
	ScanStatusPending = "pending" // 已创建，未开始
	ScanStatusRunning = "running" // 扫描中
	ScanStatusSuccess = "success" // 扫描完成
	ScanStatusPartial = "partial" // 完成但有记录级失败
	ScanStatusFailed  = "failed"  // 扫描失败
)

// ---- 记录状态 ----
const (
	// RecordStatusPending 待提交（扫描产出，还没有任何文件被动过）。
	RecordStatusPending = "pending"
	// RecordStatusExecuting 提交中。这是提交锁的持有态：
	// 只有把 pending 原子改成 executing 成功的那一方才拿到执行权。
	RecordStatusExecuting = "executing"
	// RecordStatusExecuted 已执行。
	RecordStatusExecuted = "executed"
	// RecordStatusSkippedLimit 超过 max_records_per_series。记录仍然入库（可复盘），但不执行。
	RecordStatusSkippedLimit = "skipped_limit"
	// RecordStatusExpired 判定已过期：提交前重扫发现旧文件集合变了，整条放弃。
	RecordStatusExpired = "expired"
	// RecordStatusSkippedNewLoses 新版比不过现版，不换。
	RecordStatusSkippedNewLoses = "skipped_new_loses"
	// RecordStatusSkippedNoSlot 新旧不在同一版本槽位（分辨率/编码/制作组/音轨/字幕/容器），
	// 按设计两个都保留 —— 这是「不同槽位两个都保留」那条验收的落点。
	RecordStatusSkippedNoSlot = "skipped_no_slot"
	// RecordStatusSkippedNoDimension 无可比维度（两边都解析不出可比的分辨率/编码等），一律不执行。
	RecordStatusSkippedNoDimension = "skipped_no_dimension"
	// RecordStatusSkippedNoAccess 路径不可达。常见于 emby/jellyfin 源：
	// 索引里记的网盘路径在本机文件系统上不一定存在，此时绝不能按路径盲删。
	RecordStatusSkippedNoAccess = "skipped_no_access"
	// RecordStatusFailed 执行失败。
	RecordStatusFailed = "failed"
)

// MessageExpired 判定已过期。这是验收要求里「执行跳过并标记判定已过期」的可见文案。
const MessageExpired = "判定已过期"

// ---- 判定关系四态（对齐 参考实现 的 quality_relation） ----
const (
	RelationNewWins     = "new_wins"     // 新版更优
	RelationNewLoses    = "new_loses"    // 新版更差
	RelationTie         = "tie"          // 持平
	RelationNoDimension = "no_dimension" // 无可比维度
)

// ---- 扫描源 ----
//
// Plex 刻意不在其中：参考实现 侧本来就写明 Plex 不支持洗版，本仓也没有 Plex 索引。
const (
	SourceLocal    = "local"
	SourceEmby     = "emby"
	SourceJellyfin = "jellyfin"
)

// ---- 败方动作（对应 参考实现 的 loser_action） ----
const (
	LoserActionKeep   = "keep"   // 保留败方文件（默认）
	LoserActionDelete = "delete" // 删除败方文件
	LoserActionMove   = "move"   // 把败方文件移到 move_dir
)

// Scan 一次洗版扫描任务。
type Scan struct {
	ID             uint       `gorm:"column:id;primaryKey" json:"id"`
	Source         string     `gorm:"column:source" json:"source"`
	LibraryRoot    string     `gorm:"column:library_root" json:"library_root"`
	CandidateRoots string     `gorm:"column:candidate_roots" json:"candidate_roots"`
	RuleID         uint       `gorm:"column:rule_id" json:"rule_id"`
	Status         string     `gorm:"column:status" json:"status"`
	LibraryFiles   int        `gorm:"column:library_files" json:"library_files"`
	CandidateFiles int        `gorm:"column:candidate_files" json:"candidate_files"`
	TotalRecords   int        `gorm:"column:total_records" json:"total_records"`
	NewWinsCount   int        `gorm:"column:new_wins_count" json:"new_wins_count"`
	SkippedCount   int        `gorm:"column:skipped_count" json:"skipped_count"`
	FailedCount    int        `gorm:"column:failed_count" json:"failed_count"`
	Message        string     `gorm:"column:message" json:"message"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"created_at"`
	StartedAt      *time.Time `gorm:"column:started_at" json:"started_at"`
	FinishedAt     *time.Time `gorm:"column:finished_at" json:"finished_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 固定表名。
func (Scan) TableName() string { return "media_upgrade_scans" }

// Record 一条洗版判定记录。
type Record struct {
	ID              uint   `gorm:"column:id;primaryKey" json:"id"`
	ScanID          uint   `gorm:"column:scan_id" json:"scan_id"`
	RuleID          uint   `gorm:"column:rule_id" json:"rule_id"`
	SeriesKey       string `gorm:"column:series_key" json:"series_key"`
	SeriesTitle     string `gorm:"column:series_title" json:"series_title"`
	EpisodeKey      string `gorm:"column:episode_key" json:"episode_key"`
	SlotKey         string `gorm:"column:slot_key" json:"slot_key"`
	NewFilePath     string `gorm:"column:new_file_path" json:"new_file_path"`
	NewFileName     string `gorm:"column:new_file_name" json:"new_file_name"`
	NewSize         int64  `gorm:"column:new_size" json:"new_size"`
	NewQuality      string `gorm:"column:new_quality" json:"new_quality"`
	OldFiles        string `gorm:"column:old_files" json:"old_files"`
	QualityRelation string `gorm:"column:quality_relation" json:"quality_relation"`
	Trace           string `gorm:"column:trace" json:"trace"`
	// RejectReasons 结构化驳回理由，JSON 数组字符串（T31，迁移 0048）。
	//
	// 与 Trace 并存、不合并：Trace 的措辞会随文案调整而变，
	// 拿它做统计（「最近一周多少条因为分辨率被驳回」）会随改版漂移；
	// Reasons 的 code 是稳定枚举，才配当查询键。
	// 空串 = 没有结构化理由（新版胜出、或只是体积没占优）。
	RejectReasons string `gorm:"column:reject_reasons" json:"reject_reasons"`
	// RuleFingerprint 判定当时的规则指纹（T31，迁移 0048）。
	//
	// 规则改过之后，旧记录里的驳回理由属于另一个口径；没有指纹就分不清
	// 「上次就是这样」与「规则变了才这样」。
	RuleFingerprint string     `gorm:"column:rule_fingerprint" json:"rule_fingerprint"`
	LoserAction     string     `gorm:"column:loser_action" json:"loser_action"`
	LoserPath       string     `gorm:"column:loser_path" json:"loser_path"`
	Snapshot        string     `gorm:"column:snapshot" json:"snapshot"`
	SnapshotHash    string     `gorm:"column:snapshot_hash" json:"snapshot_hash"`
	SnapshotAt      time.Time  `gorm:"column:snapshot_at" json:"snapshot_at"`
	Status          string     `gorm:"column:status" json:"status"`
	Message         string     `gorm:"column:message" json:"message"`
	DeleteFailures  string     `gorm:"column:delete_failures" json:"delete_failures"`
	ExecutedAt      *time.Time `gorm:"column:executed_at" json:"executed_at"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 固定表名。
func (Record) TableName() string { return "media_upgrade_records" }

// Rule 一套洗版规则（参考实现 的 upgrade_rules JSON 在本仓展开成表，以便在 UI 里逐项编辑）。
type Rule struct {
	ID                  uint   `gorm:"column:id;primaryKey" json:"id"`
	Name                string `gorm:"column:name" json:"name"`
	Source              string `gorm:"column:source" json:"source"`
	LibraryRoot         string `gorm:"column:library_root" json:"library_root"`
	CandidateRoots      string `gorm:"column:candidate_roots" json:"candidate_roots"`
	MinResolution       int    `gorm:"column:min_resolution" json:"min_resolution"`
	MinChannels         int    `gorm:"column:min_channels" json:"min_channels"`
	RequireSubtitle     bool   `gorm:"column:require_subtitle" json:"require_subtitle"`
	MaxRecordsPerSeries int    `gorm:"column:max_records_per_series" json:"max_records_per_series"`
	LoserAction         string `gorm:"column:loser_action" json:"loser_action"`
	MoveDir             string `gorm:"column:move_dir" json:"move_dir"`
	GroupPriority       string `gorm:"column:group_priority" json:"group_priority"`
	WashRules           string `gorm:"column:wash_rules" json:"wash_rules"`
	// CategoryScope 适用分类目录名（T27 C-8，迁移 0046）。
	//
	// 存 JSON 数组 `["国产","美剧"]`，空串 = 不按分类筛选。
	// 刻意存目录名而不是 classify_primary_categories 的 slug：
	// 那张表是分类模板的派生投影，模板一改就会被整批重投影覆盖，
	// 用户的洗版规则不能依赖一张会被删掉重建的表。
	CategoryScope string    `gorm:"column:category_scope" json:"category_scope"`
	Enabled       bool      `gorm:"column:enabled" json:"enabled"`
	Builtin       bool      `gorm:"column:builtin" json:"builtin"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 固定表名。
func (Rule) TableName() string { return "media_upgrade_rules" }

// DeleteFailure 一次删除失败，落到 record.delete_failures 并在日志与界面可见。
type DeleteFailure struct {
	Path   string `json:"path"`
	Stage  string `json:"stage"`  // batch=批量接口就失败 / single=降级逐条也失败
	Reason string `json:"reason"` // 失败原因原文
}

// 删除失败的阶段标记。
const (
	DeleteStageBatch  = "batch"  // 整批调用就失败了
	DeleteStageSingle = "single" // 降级逐条重试后仍失败
)

// fileSnapshot 单个文件的快照：判定时它长什么样。
//
// 只记 path/size/mtime 就够了：洗版关心的是「库里的这一集还是不是当初那批文件」，
// 内容变了（mtime/size 变）就说明世界变了，重新判定比删文件安全。
type fileSnapshot struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // Unix 秒
	SlotKey string `json:"slot_key,omitempty"`
}
