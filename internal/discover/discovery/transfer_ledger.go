// 订阅转存幂等账本：以媒体库为真值 + 3 小时保护期。
//
// 替换掉的是「永久去重」：原来只要 discovery_subscription_items 里有一条
// status='transferred' 的同 scope 记录，这个 scope 就永远不会再被订阅碰。
// 用户在网盘删掉一部剧，订阅不会拉回来；洗版换版本也被挡住。
//
// 参考实现 的不变式是「**以媒体库实际有没有为准**」：删了就重转（这正是用户想要的），
// 换版本也重转。但转存完不能马上再来一次 —— 网盘写入有延迟、文件清单可能还没刷新，
// 所以有 3 小时保护期。
//
// 三层去重各自的职责（不要混用）：
//
//	第一层  同轮候选去重  planAndTransferRuleCandidates 里的 batchScopes，
//	                       同一轮内同一个季集范围只转一次（防止一轮里 5 个帖都是同一季）。
//	第二层  本文件        账本 + 媒体库真值 + 保护期，跨轮的唯一权威判据。
//	第三层  transferredScopesFor 的 TG/RSS 位点基线，跨轮兜底，
//	                       仅在媒体库索引不可用时兜住，避免 Emby 没开时反复转存。
//
// 与 discovery_transfer_records（internal/discover/discovery/channels.go:148）的关系：
// 那张表是 TG 频道转存路径的占位账本（ReserveTransfer/ConfirmTransfer），
// 本表是订阅自动转存路径的账本，两者字段语义不同（这里有 media_scope 与保护期），
// 不合并 —— 合并会动到已在生产跑的频道路径。
package discovery

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/mediaorganize/rules"
	"litepan/internal/settings"
)

// ---------------------------------------------------------------------------
// 账本条目状态机
// ---------------------------------------------------------------------------

// litepan 不需要 参考实现 那 25 个状态：下载/离线/整理/strm 那一整段是由 mediaorganize
// 单独驱动的，不在订阅转存这条同步链路里。这里只保留订阅实际会经过的四个，
// organized_at / strm_created_at 两个时间戳仍然留字段（迁移里有），
// 由整理侧回填，供巡检与「以媒体库为真值」判定使用。
const (
	// LedgerStateCandidateSelected 已选中候选，还未发起转存
	LedgerStateCandidateSelected = "candidate_selected"
	// LedgerStateTransferRequested 已发起转存（占位成功，进入保护期）
	LedgerStateTransferRequested = "transfer_requested"
	// LedgerStateTransferConfirmed 转存已确认成功
	LedgerStateTransferConfirmed = "transfer_confirmed"
	// LedgerStateTransferFailed 转存失败，下一轮可重试
	LedgerStateTransferFailed = "transfer_failed"
)

// ---------------------------------------------------------------------------
// 设置
// ---------------------------------------------------------------------------

// transferProtectHours 读保护期配置。
//
// 保护期同时是一个限流器：即使媒体库索引完全不可用（没开 Emby），
// 同一批内容最多也只会被重转一次每 N 小时，而不是每轮订阅都转一遍。
func transferProtectHours() time.Duration {
	svc := currentSettings()
	if svc == nil {
		return defaultTransferProtectHours
	}
	hours := svc.Int(settings.KeyMOSubscriptionTransferProtectHours)
	if hours < 0 {
		hours = 0
	}
	return time.Duration(hours) * time.Hour
}

// defaultTransferProtectHours 装配不到设置服务时的兜底值，与
// registry 里 KeyMOSubscriptionTransferProtectHours 的 Default 保持一致。
const defaultTransferProtectHours = 3 * time.Hour

// ---------------------------------------------------------------------------
// 模型
// ---------------------------------------------------------------------------

// DiscoveryTransferItem 账本条目（discovery_transfer_items）。
// 与 internal/store/migrations/0035_subscription_ledger.sql 逐列对应。
type DiscoveryTransferItem struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Idempotency string `gorm:"column:idempotency_key;size:64;uniqueIndex" json:"idempotency_key"`
	StorageSlug string `gorm:"size:32;uniqueIndex:idx_transfer_items_content" json:"storage_slug"`
	ContentKey  string `gorm:"size:128;uniqueIndex:idx_transfer_items_content" json:"content_key"`
	Sha1        string `gorm:"size:40;index" json:"sha1"`
	MediaScope  string `gorm:"size:64;index" json:"media_scope"`
	SubID       uint   `gorm:"column:subscription_id;index:idx_transfer_items_sub_scope" json:"subscription_id"`
	RuleID      uint   `gorm:"column:rule_id" json:"rule_id"`
	Title       string `gorm:"size:255" json:"title"`
	State       string `gorm:"size:32;index" json:"state"`
	ReasonCode  string `gorm:"column:reason_code;size:48" json:"reason_code"`
	// 五个时间戳与迁移一一对应。前三个由本文件写，后两个由整理侧回填。
	CandidateSelectedAt time.Time  `json:"candidate_selected_at"`
	TransferRequestedAt *time.Time `json:"transfer_requested_at"`
	TransferConfirmedAt *time.Time `json:"transfer_confirmed_at"`
	OrganizedAt         *time.Time `json:"organized_at"`
	StrmCreatedAt       *time.Time `json:"strm_created_at"`
	DeletedAt           *time.Time `json:"deleted_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func (DiscoveryTransferItem) TableName() string { return "discovery_transfer_items" }

// ---------------------------------------------------------------------------
// 键的构造
// ---------------------------------------------------------------------------

// ledgerContentKey 算条目的内容身份。
//
// 真实 sha1 优先；拿不到时退化为资源级身份 res:<slug>。
// 用前缀区分是为了让账本里一眼能看出这条的键是「文件级」还是「资源级」——
// 资源级键的粒度粗，同一个分享洗版后 slug 会变、键也就变了，不会被旧条目挡住；
// 但同一个 slug 指向的内容被替换（站方原地洗版）时，资源级键挡不住，
// 这种情况只能靠媒体库真值判定。
func ledgerContentKey(cand resourceCandidate) (contentKey, sha1 string) {
	if h := normalizeLedgerSha1(cand.Sha1); h != "" {
		return "sha1:" + h, h
	}
	identity := firstNonEmptyStr(cand.Slug, cand.ShareURL)
	if identity == "" {
		identity = firstNonEmptyStr(cand.Title, cand.ChannelTitle, cand.ItemKey)
	}
	return "res:" + identity, ""
}

func normalizeLedgerSha1(raw string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	if len(text) != 40 {
		return ""
	}
	for _, ch := range text {
		if ch < '0' || (ch > '9' && ch < 'a') || ch > 'f' {
			return ""
		}
	}
	return text
}

// ledgerMediaScope 算媒体范围键，形如 tmdb:129 / tmdb:1396:s1 / tmdb:1396:s1e3。
//
// 与任务书给的示例一致。没有 TMDB ID 时退回用订阅标题，这样「媒体库真值」查询
// 仍有一个可用的匹配口径（按标题+年份查本地库）。
func ledgerMediaScope(sub *DiscoverySubscription, cand resourceCandidate) string {
	scope := "tmdb:"
	if sub != nil && sub.TMDBID > 0 {
		scope += strconv.FormatInt(sub.TMDBID, 10)
	} else if sub != nil {
		scope += "?" + rules.NormalizeTitleKey(firstNonEmptyStr(sub.Title, sub.OriginalTitle))
	} else {
		scope += "?" + rules.NormalizeTitleKey(cand.Title)
	}
	if sub != nil && strings.EqualFold(strings.TrimSpace(sub.MediaType), "tv") {
		ep := cand.Episode
		if ep != nil {
			season := derefInt(ep.SeasonNum, 1)
			if ep.EpisodeNum != nil {
				return fmt.Sprintf("%s:s%de%d", scope, season, *ep.EpisodeNum)
			}
			return fmt.Sprintf("%s:s%d", scope, season)
		}
	}
	return scope
}

// LedgerIdempotencyKey 账本幂等键：sha256(storage_slug \0 content_key \0 media_scope)。
//
// 用 \0 而不是 | 做分隔：slug 里理论上可能出现 |，而 \0 不会，
// 避免 (a|b, c) 与 (a, b|c) 拼出同一个键。
func LedgerIdempotencyKey(storageSlug, contentKey, mediaScope string) string {
	raw := strings.Join([]string{strings.TrimSpace(storageSlug), strings.TrimSpace(contentKey), strings.TrimSpace(mediaScope)}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// 抢占用例：CAS
// ---------------------------------------------------------------------------

// LedgerClaim 一次抢占的结果。
type LedgerClaim struct {
	// Claimed 是否抢到（= 是否允许发起转存）
	Claimed bool
	// ReasonCode 未抢到时的原因，写进账本与日志便于事后排查
	ReasonCode string
	// ProtectedUntil 保护期截止时间；抢到时非零
	ProtectedUntil time.Time
	// ID 条目 ID，推进状态时要用
	ID uint
}

// ledgerReserveWindowSQL 保护期判定的 SQL 片段。
//
// 起算字段用 COALESCE(transfer_requested_at, candidate_selected_at)：
// 「刚转存完」的时刻是发起转存的时刻，不是发起前那一刻，也不是整理完成的时刻。
// 理由见迁移文件顶部注释。
const ledgerReserveWindowSQL = `COALESCE(transfer_requested_at, candidate_selected_at)`

// ClaimTransferSlot 抢占用例，决定这一轮能不能发起转存。
//
// 用「先 INSERT ... ON CONFLICT DO NOTHING，冲突再条件 UPDATE」的 CAS，
// **不要**写成先 SELECT 再 INSERT —— 定时器一轮与用户手动「立即搜索」可能同时跑到
// 同一个条目，两者都查到「不在保护期」就会都去转存。唯一索引是唯一的裁决者。
//
// 条件 UPDATE 的 WHERE 带上了保护期判定，等于把「是不是还在保护期内」也做成了原子操作：
// RowsAffected == 1 说明只有一方赢了。
func ClaimTransferSlot(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate) LedgerClaim {
	if ddb.Db == nil {
		// 没有数据库就没法裁决。退化为允许 —— 否则整个订阅功能在装配异常时全停。
		log.Printf("[discovery] transfer_ledger 数据库不可用，跳过账本判定并放行：sub_id=%d item=%s",
			subIDOf(sub), firstNonEmptyStr(cand.Slug, cand.Title))
		return LedgerClaim{Claimed: true, ReasonCode: "ledger_unavailable"}
	}

	storageSlug := firstNonEmptyStr(rule.TargetProvider, cand.Provider)
	contentKey, sha1 := ledgerContentKey(cand)
	mediaScope := ledgerMediaScope(sub, cand)
	key := LedgerIdempotencyKey(storageSlug, contentKey, mediaScope)

	protect := transferProtectHours()
	now := time.Now()
	protectedUntil := now.Add(protect)

	// 第一步：原子插入。抢到即全新条目。
	res := ddb.Db.Exec(`
		INSERT INTO discovery_transfer_items
			(idempotency_key, storage_slug, content_key, sha1, media_scope, subscription_id, rule_id,
			 title, state, reason_code, candidate_selected_at, transfer_requested_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(idempotency_key) DO NOTHING`,
		key, storageSlug, contentKey, sha1, mediaScope, subIDOf(sub), rule.ID, cand.Title,
		LedgerStateTransferRequested, "", now, now, now, now)
	if err := res.Error; err != nil {
		log.Printf("[discovery] transfer_ledger 插入失败，降级为按无账本处理：%v", err)
		return LedgerClaim{Claimed: true, ReasonCode: "ledger_insert_failed"}
	}
	if res.RowsAffected == 1 {
		return LedgerClaim{Claimed: true, ProtectedUntil: protectedUntil, ID: ledgerIDByKey(key)}
	}

	// 第二步：条目已存在。保护期内直接判定为抢不到。
	var existing DiscoveryTransferItem
	if err := ddb.Db.Where("idempotency_key = ?", key).First(&existing).Error; err != nil {
		if err == sql.ErrNoRows {
			return LedgerClaim{Claimed: true, ReasonCode: "ledger_race_lost"}
		}
		log.Printf("[discovery] transfer_ledger 回读失败，降级放行：%v", err)
		return LedgerClaim{Claimed: true, ReasonCode: "ledger_read_failed"}
	}

	// 失败态的旧条目不算数：上一轮转存失败本来就该重试。
	if existing.State == LedgerStateTransferFailed {
		if rows := ddb.Db.Model(&DiscoveryTransferItem{}).
			Where("idempotency_key = ?", key).
			Updates(map[string]any{
				"state":                 LedgerStateTransferRequested,
				"reason_code":           "",
				"transfer_requested_at": now,
				"updated_at":            now,
				"transfer_confirmed_at": nil,
			}).RowsAffected; rows == 1 {
			return LedgerClaim{Claimed: true, ProtectedUntil: protectedUntil, ID: existing.ID}
		}
		return LedgerClaim{Claimed: false, ReasonCode: "ledger_race_lost", ProtectedUntil: protectedUntil, ID: existing.ID}
	}

	// 第三步：条件 UPDATE —— 只有当条目已经**出了**保护期才抢得到。
	// 注意方向：COALESCE(transfer_requested_at, candidate_selected_at) <= cutoff
	// 表示「上次发起转存的时刻早于 cutoff」，也就是保护期已经走完，可以再来一次。
	// 写成 > 会把语义整个反过来（永远判定成「还在保护期」）。
	// protect == 0 时 cutoff 就是 now，任何在 now 之前发起的转存都能重抢（验收项 4）。
	cutoff := now.Add(-protect)
	rows := ddb.Db.Model(&DiscoveryTransferItem{}).
		Where("idempotency_key = ? AND "+ledgerReserveWindowSQL+" <= ?", key, cutoff).
		Updates(map[string]any{
			"state":                 LedgerStateTransferRequested,
			"reason_code":           "",
			"transfer_requested_at": now,
			"updated_at":            now,
		}).RowsAffected

	if rows == 1 {
		return LedgerClaim{Claimed: true, ProtectedUntil: protectedUntil, ID: existing.ID}
	}
	// rows == 0：还在保护期内。
	return LedgerClaim{Claimed: false, ReasonCode: ReasonCodeProtectedWindow, ProtectedUntil: protectedUntil, ID: existing.ID}
}

// 账本 reason_code
const (
	// ReasonCodeProtectedWindow 仍在保护期内（刚转存完，不重复动）
	ReasonCodeProtectedWindow = "PROTECTED_WINDOW"
	// ReasonCodeLedgerRaceLost 并发下别人先抢到
	ReasonCodeLedgerRaceLost = "LEDGER_RACE_LOST"
	// ReasonCodePresentInLibrary 媒体库里已经有了（以媒体库为真值）
	ReasonCodePresentInLibrary = "PRESENT_IN_LIBRARY"
	// ReasonCodeBaselineHit 第三层跨轮基线命中
	ReasonCodeBaselineHit = "BASELINE_SCOPE_HIT"
)

// ledgerIDByKey 回读刚插入的条目 ID。回读失败返回 0，不影响占位结论。
func ledgerIDByKey(key string) uint {
	var item DiscoveryTransferItem
	if err := ddb.Db.Where("idempotency_key = ?", key).First(&item).Error; err != nil {
		return 0
	}
	return item.ID
}

func subIDOf(sub *DiscoverySubscription) uint {
	if sub == nil {
		return 0
	}
	return sub.ID
}

// tmdbIDOf 取订阅的 TMDB ID，nil 安全。
//
// 与 subIDOf 成对存在：去重链路上有几处日志要同时打 sub_id 与 tmdb_id，
// 而 RSS 投递（rss_delivery.go）会把 nil 传进 decideTransfer ——
// 那里原来是裸取 sub.TMDBID，会在第一次判重就 panic，且 panic 点在日志参数里，
// 栈会指向 log.Printf 而不是调用方，排查成本极高。
func tmdbIDOf(sub *DiscoverySubscription) int64 {
	if sub == nil {
		return 0
	}
	return sub.TMDBID
}

// ---------------------------------------------------------------------------
// 状态推进
// ---------------------------------------------------------------------------

// ConfirmLedgerEntry 转存成功后把条目推进为 confirmed。
func ConfirmLedgerEntry(item *DiscoveryTransferItem) {
	if item == nil {
		return
	}
	now := time.Now()
	updates := map[string]any{
		"state":                 LedgerStateTransferConfirmed,
		"transfer_confirmed_at": now,
		"updated_at":            now,
	}
	if item.ID != 0 {
		ddb.Db.Model(&DiscoveryTransferItem{}).Where("id = ?", item.ID).Updates(updates)
		return
	}
	if item.Idempotency != "" {
		ddb.Db.Model(&DiscoveryTransferItem{}).Where("idempotency_key = ?", item.Idempotency).Updates(updates)
	}
}

// FailLedgerEntry 转存失败：标记为可重试。
//
// ⚠️ 失败不释放保护期。网盘写入失败后立刻重试很可能再失败（配额、限流、路径问题），
// 让它按保护期自然过期即可；真正的重试由下一轮订阅周期驱动。
func FailLedgerEntry(item *DiscoveryTransferItem, reason string) {
	if item == nil {
		return
	}
	now := time.Now()
	updates := map[string]any{
		"state":       LedgerStateTransferFailed,
		"reason_code": truncateStr(reason, 48),
		"updated_at":  now,
	}
	if item.ID != 0 {
		ddb.Db.Model(&DiscoveryTransferItem{}).Where("id = ?", item.ID).Updates(updates)
		return
	}
	if item.Idempotency != "" {
		ddb.Db.Model(&DiscoveryTransferItem{}).Where("idempotency_key = ?", item.Idempotency).Updates(updates)
	}
}

// FindLedgerEntry 按幂等键查条目，供巡检与测试使用。
func FindLedgerEntry(key string) (*DiscoveryTransferItem, bool) {
	if ddb.Db == nil || strings.TrimSpace(key) == "" {
		return nil, false
	}
	var item DiscoveryTransferItem
	if err := ddb.Db.Where("idempotency_key = ?", key).First(&item).Error; err != nil {
		return nil, false
	}
	return &item, true
}

// LedgerEntryFor 算出一条候选对应的幂等键（不落库），供巡检与测试预置数据。
func LedgerEntryFor(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, cand resourceCandidate) (key, mediaScope, contentKey string) {
	storageSlug := firstNonEmptyStr(rule.TargetProvider, cand.Provider)
	contentKey, _ = ledgerContentKey(cand)
	mediaScope = ledgerMediaScope(sub, cand)
	return LedgerIdempotencyKey(storageSlug, contentKey, mediaScope), mediaScope, contentKey
}
