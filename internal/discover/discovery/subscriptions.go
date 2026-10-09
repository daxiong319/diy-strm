package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/ddb"
	"litepan/internal/discover/hdhive"

	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// 自研订阅引擎（对齐参考实现 media_subscriptions 体系：
// 订阅 + 规则 + 执行轮次 + 候选 + 事件，按 next_check_at 周期
// 搜资源 → 规则匹配 → 自动转存）
// ---------------------------------------------------------------------------

// DiscoverySubscription 订阅
type DiscoverySubscription struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	EntityKey     string    `gorm:"unique;size:128;index" json:"entity_key"`
	Source        string    `gorm:"size:16" json:"source"` // tmdb
	EntityType    string    `gorm:"size:16" json:"entity_type"` // movie/tv/person
	ExternalID    string    `gorm:"size:32" json:"external_id"`
	TMDBID        int64     `json:"tmdb_id"`
	MediaType     string    `gorm:"size:8" json:"media_type"`
	Title         string    `gorm:"size:255" json:"title"`
	OriginalTitle string    `gorm:"size:255" json:"original_title"`
	Poster        string    `gorm:"size:512" json:"poster"`
	TargetProvider string   `gorm:"size:16" json:"target_provider"` // 123/guangya/pan139
	TransferMode  string    `gorm:"size:16" json:"transfer_mode"`   // auto
	Enabled       bool      `gorm:"default:true" json:"enabled"`
	IntervalMinutes int     `json:"interval_minutes"`
	Preferences   string    `gorm:"type:text" json:"-"` // JSON
	Metadata      string    `gorm:"type:text" json:"-"` // JSON（含 emby_missing 补档上下文）
	Status        string    `gorm:"size:16;default:pending" json:"status"`

	// ---- T05 搜索连接器 ----
	// SearchSources 逗号分隔的 connector key 列表。空串 / 未填 = 跟随全局默认
	// （mo_subscription_search_sources，默认 tgto123）。存量订阅在 0036 迁移里
	// 拿到的是 'tgto123'，与改动前唯一那个源完全一致 —— 存量行为不变。
	// default 标签与迁移 0036 的 DDL 逐字对齐，保证 AutoMigrate 补列和迁移建列
	// 产出的是同一个 schema（否则两条升级路径的列定义会分叉）。
	SearchSources string `gorm:"size:255;index;default:'tgto123'" json:"search_sources"`
	// Resolution 画质**下限**：720/1080/2160，空串 = 不限制。2160 表示「至少 4K」，
	// 1080p 会被过滤掉。不是精确匹配。
	Resolution string `gorm:"size:16" json:"resolution"`
	// Effect 必须具备的特效：dolby atmos / dolby vision / hdr / sdr。
	// 'sdr' 是例外语义 —— 必须**不含** HDR/DV。空串 = 不限制。
	Effect string `gorm:"size:32" json:"effect"`
	// Min/MaxFileSizeMB 体积闸门（MB）。两者都为 0 时不引入任何体积判定。
	MinFileSizeMB int `json:"min_file_size_mb"`
	MaxFileSizeMB int `json:"max_file_size_mb"`

	LastCheckedAt *time.Time `json:"last_checked_at"`
	NextCheckAt   *time.Time `gorm:"index" json:"next_check_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	// 非持久化
	Pref  map[string]any           `gorm:"-" json:"preferences,omitempty"`
	Meta  map[string]any           `gorm:"-" json:"metadata,omitempty"`
	Rules []DiscoverySubscriptionRule `gorm:"-" json:"rules,omitempty"`
}

func (DiscoverySubscription) TableName() string { return "discovery_subscriptions" }

// DiscoverySubscriptionRule 自动规则
type DiscoverySubscriptionRule struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	SubscriptionID uint   `gorm:"index:idx_disc_sub_rule,composite:subscription_id,position" json:"subscription_id"`
	Name           string `gorm:"size:120" json:"name"`
	Enabled        bool   `gorm:"default:true" json:"enabled"`
	TargetProvider string `gorm:"size:16" json:"target_provider"`
	MaxPoints      int    `json:"max_points"`
	Preferences    string `gorm:"type:text" json:"-"` // JSON
	Match          string `gorm:"type:text" json:"-"` // JSON {message_keywords,must_contain,must_not_contain}
	Position       int    `json:"position"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// 非持久化
	Pref  map[string]any `gorm:"-" json:"preferences,omitempty"`
	MatchData map[string]any `gorm:"-" json:"match,omitempty"`
}

func (DiscoverySubscriptionRule) TableName() string { return "discovery_subscription_rules" }

// DiscoverySubscriptionRun 执行轮次
type DiscoverySubscriptionRun struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	SubscriptionID *uint      `gorm:"index" json:"subscription_id"`
	TriggerType    string     `gorm:"size:24" json:"trigger_type"` // scheduled/manual/emby_missing_manual
	Status         string     `gorm:"size:16" json:"status"`       // running/success/partial/failed/no_update
	ResourceCount  int        `json:"resource_count"`
	SelectedCount  int        `json:"selected_count"`
	TransferredCount int      `json:"transferred_count"`
	Message        string     `gorm:"size:512" json:"message"`
	Detail         string     `gorm:"type:text" json:"-"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

func (DiscoverySubscriptionRun) TableName() string { return "discovery_subscription_runs" }

// DiscoverySubscriptionItem 订阅候选（seen 去重 + 状态机）
type DiscoverySubscriptionItem struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	SubscriptionID uint       `gorm:"uniqueIndex:idx_disc_sub_item,composite:subscription_id,item_key" json:"subscription_id"`
	ItemKey        string     `gorm:"uniqueIndex:idx_disc_sub_item,composite:subscription_id,item_key;size:190" json:"item_key"`
	RunID          *uint      `json:"run_id"`
	Provider       string     `gorm:"size:16" json:"provider"`
	Slug           string     `gorm:"size:190" json:"slug"`
	Title          string     `gorm:"size:255" json:"title"`
	Status         string     `gorm:"size:16;default:discovered" json:"status"` // discovered/selected/transferring/transferred/failed/skipped
	Candidate      string     `gorm:"type:text" json:"-"`
	FirstSeenAt    time.Time  `json:"first_seen_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	TransferredAt  *time.Time `json:"transferred_at"`
}

func (DiscoverySubscriptionItem) TableName() string { return "discovery_subscription_items" }

// DiscoverySubscriptionEvent 订阅事件流
type DiscoverySubscriptionEvent struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SubscriptionID *uint     `gorm:"index" json:"subscription_id"`
	RunID          *uint     `json:"run_id"`
	ItemID         *uint     `json:"item_id"`
	EventType      string    `gorm:"size:32" json:"event_type"`
	Status         string    `gorm:"size:16" json:"status"`
	Message        string    `gorm:"size:512" json:"message"`
	Detail         string    `gorm:"type:text" json:"-"`
	CreatedAt      time.Time `gorm:"index" json:"created_at"`
}

func (DiscoverySubscriptionEvent) TableName() string { return "discovery_subscription_events" }

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

// ListSubscriptions 订阅列表（附规则，可按 enabled 过滤）
func ListSubscriptions(enabled *bool) ([]DiscoverySubscription, error) {
	query := ddb.Db.Order("id desc")
	if enabled != nil {
		query = query.Where("enabled = ?", *enabled)
	}
	var list []DiscoverySubscription
	if err := query.Find(&list).Error; err != nil {
		return nil, err
	}
	for i := range list {
		attachSubscriptionExtras(&list[i])
	}
	return list, nil
}

// GetSubscription 单条订阅
func GetSubscription(id uint) (*DiscoverySubscription, error) {
	var sub DiscoverySubscription
	if err := ddb.Db.First(&sub, id).Error; err != nil {
		return nil, fmt.Errorf("订阅不存在")
	}
	attachSubscriptionExtras(&sub)
	return &sub, nil
}

// GetSubscriptionByKey 按 entity_key 查订阅
func GetSubscriptionByKey(entityKey string) (*DiscoverySubscription, error) {
	var sub DiscoverySubscription
	if err := ddb.Db.Where("entity_key = ?", entityKey).First(&sub).Error; err != nil {
		return nil, fmt.Errorf("订阅不存在")
	}
	attachSubscriptionExtras(&sub)
	return &sub, nil
}

// attachSubscriptionExtras 解析 JSON 字段 + 附规则
func attachSubscriptionExtras(sub *DiscoverySubscription) {
	sub.Pref = parseJSONObject(sub.Preferences)
	sub.Meta = parseJSONObject(sub.Metadata)
	var rules []DiscoverySubscriptionRule
	ddb.Db.Where("subscription_id = ?", sub.ID).Order("position asc, id asc").Find(&rules)
	for i := range rules {
		rules[i].Pref = parseJSONObject(rules[i].Preferences)
		rules[i].MatchData = parseJSONObject(rules[i].Match)
	}
	sub.Rules = rules
}

// parseJSONObject JSON 对象解析（失败返回空 map）
func parseJSONObject(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = jsonUnmarshal([]byte(raw), &out)
	return out
}

// SubscriptionUpsertPayload 创建/更新订阅请求体（对齐参考实现字段）
type SubscriptionUpsertPayload struct {
	Source          string          `json:"source"`
	EntityType      string          `json:"entity_type"`
	ExternalID      string          `json:"external_id"`
	TMDBID          int64           `json:"tmdb_id"`
	MediaType       string          `json:"media_type"`
	Title           string          `json:"title"`
	OriginalTitle   string          `json:"original_title"`
	Poster          string          `json:"poster_url"`
	TargetProvider  string          `json:"target_provider"`
	TransferMode    string          `json:"transfer_mode"`
	Enabled         *bool           `json:"enabled"`
	IntervalMinutes int             `json:"interval_minutes"`
	Preferences     map[string]any  `json:"preferences"`
	Rules           []SubscriptionRulePayload `json:"rules"`
	Metadata        map[string]any  `json:"metadata"`

	// ---- T05 搜索连接器 ----
	// SearchSources 逗号分隔的 connector key；nil = 不改（更新时），
	// 空串 = 跟随全局默认（mo_subscription_search_sources）。
	SearchSources *string `json:"search_sources"`
	Resolution    string  `json:"resolution"`
	Effect        string  `json:"effect"`
	// Min/MaxFileSizeMB 指针而非值：0 是有意义的取值（「不限体积」），
	// 用值就无法区分「用户显式要 0」和「这次没传这个字段」。
	MinFileSizeMB *int `json:"min_file_size_mb"`
	MaxFileSizeMB *int `json:"max_file_size_mb"`
}

// SubscriptionRulePayload 规则请求体
type SubscriptionRulePayload struct {
	Name           string         `json:"name"`
	Enabled        *bool          `json:"enabled"`
	TargetProvider string         `json:"target_provider"`
	MaxPoints      int            `json:"max_points"`
	Preferences    map[string]any `json:"preferences"`
	MessageKeywords []string      `json:"message_keywords"`
	MustContain    []string       `json:"must_contain"`
	MustNotContain []string       `json:"must_not_contain"`
}

// SaveSubscription 创建/更新订阅（按 entity_key UPSERT + 全量替换规则）
func SaveSubscription(payload *SubscriptionUpsertPayload) (*DiscoverySubscription, string, error) {
	if strings.TrimSpace(payload.Title) == "" {
		return nil, "", fmt.Errorf("订阅标题不能为空")
	}
	if payload.ExternalID == "" && payload.TMDBID <= 0 {
		return nil, "", fmt.Errorf("缺少条目 ID，无法安全订阅")
	}
	entityType := strings.ToLower(strings.TrimSpace(payload.EntityType))
	if entityType == "" {
		entityType = payload.MediaType
	}
	if entityType != "movie" && entityType != "tv" && entityType != "person" {
		return nil, "", fmt.Errorf("订阅类型仅支持 movie/tv/person")
	}
	targetProvider, err := NormalizeTransferProvider(payload.TargetProvider)
	warning := ""
	if err != nil {
		if entityType != "person" {
			return nil, "", err
		}
		targetProvider = "" // 人物订阅不转存
	}
	if entityType != "person" && !TransferTargetConfigured(targetProvider) {
		warning = "保存目录尚未配置，转存前请到影视发现 - 基础配置页面选择目录"
	}
	if payload.IntervalMinutes <= 0 {
		payload.IntervalMinutes = SettingInt(SettingCheckIntervalMinutes, 360)
	}
	payload.IntervalMinutes = clampInt(payload.IntervalMinutes, 15, 10080)

	mediaType := strings.ToLower(strings.TrimSpace(payload.MediaType))
	if mediaType == "" {
		if entityType == "movie" {
			mediaType = "movie"
		} else if entityType == "tv" {
			mediaType = "tv"
		}
	}
	entityKey := fmt.Sprintf("tmdb:%s:%s", entityType, firstNonEmptyStr(payload.ExternalID, strconvI64(payload.TMDBID)))

	// T05：搜索源与画质闸门。搜索源只做归一化不拒绝未知 key —— 连接器集合会随
	// 版本增补，界面里存过时的 key 时静默失效比直接报错更糟（检索时按「跳过未装配
	// 的源」处理，并在订阅运行日志里点名，用户能看见）。画质列则必须校验，
	// 因为写错了会静默地放过或拦掉所有候选。
	gateWarnings, gateErr := normalizeSubscriptionGates(payload)
	if gateErr != nil {
		return nil, "", gateErr
	}

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	now := time.Now()
	nextCheck := now.Add(time.Duration(payload.IntervalMinutes) * time.Minute)

	var sub DiscoverySubscription
	err = ddb.Db.Where("entity_key = ?", entityKey).First(&sub).Error
	isNew := err != nil
	metadataJSON := marshalJSON(payload.Metadata)
	preferencesJSON := marshalJSON(payload.Preferences)
	if isNew {
		sub = DiscoverySubscription{
			EntityKey: entityKey, Source: firstNonEmptyStr(payload.Source, "tmdb"), EntityType: entityType,
			ExternalID: firstNonEmptyStr(payload.ExternalID, strconvI64(payload.TMDBID)),
			TMDBID:     payload.TMDBID, MediaType: mediaType,
			Title: payload.Title, OriginalTitle: payload.OriginalTitle,
			Poster: payload.Poster, TargetProvider: targetProvider,
			TransferMode: "auto", Enabled: enabled,
			IntervalMinutes: payload.IntervalMinutes,
			Preferences: preferencesJSON, Metadata: metadataJSON,
			Status: "pending", NextCheckAt: &nextCheck,
			CreatedAt: now, UpdatedAt: now,
		}
		if payload.SearchSources != nil {
			sub.SearchSources = *payload.SearchSources
		}
		sub.Resolution = payload.Resolution
		sub.Effect = payload.Effect
		if payload.MinFileSizeMB != nil {
			sub.MinFileSizeMB = *payload.MinFileSizeMB
		}
		if payload.MaxFileSizeMB != nil {
			sub.MaxFileSizeMB = *payload.MaxFileSizeMB
		}
		if err := ddb.Db.Create(&sub).Error; err != nil {
			return nil, "", err
		}
	} else {
		updates := map[string]any{
			"title": payload.Title, "original_title": payload.OriginalTitle,
			"poster": payload.Poster, "target_provider": targetProvider,
			"transfer_mode": "auto", "enabled": enabled,
			"interval_minutes": payload.IntervalMinutes,
			"preferences": preferencesJSON, "metadata": metadataJSON,
			"updated_at": now,
		}
		// 画质列走「空值即不改」：界面不碰这几个字段时不该把用户已设的闸门抹掉。
		if payload.Resolution != "" || payload.Effect != "" {
			updates["resolution"] = payload.Resolution
			updates["effect"] = payload.Effect
		}
		if payload.MinFileSizeMB != nil {
			updates["min_file_size_mb"] = *payload.MinFileSizeMB
		}
		if payload.MaxFileSizeMB != nil {
			updates["max_file_size_mb"] = *payload.MaxFileSizeMB
		}
		if payload.SearchSources != nil {
			updates["search_sources"] = *payload.SearchSources
		}
		// source 只在调用方显式给了的时候才改。求片建出来的订阅标着 "request"，
		// 管理台编辑这条订阅时表单不带 source（字段为空），这行就让它保持原样 ——
		// 不然每保存一次就把「这条是家人求来的」这个唯一线索抹成 tmdb。
		if payload.Source != "" {
			updates["source"] = payload.Source
		}
		if payload.TMDBID > 0 {
			updates["tmdb_id"] = payload.TMDBID
		}
		if err := ddb.Db.Model(&sub).Updates(updates).Error; err != nil {
			return nil, "", err
		}
	}
	if err := replaceSubscriptionRules(sub.ID, payload.Rules); err != nil {
		return nil, "", err
	}
	warning = joinWarnings(warning, gateWarnings)
	recordSubscriptionEvent(&sub, nil, nil, map[bool]string{true: "subscription_created", false: "subscription_updated"}[isNew], map[bool]string{true: "pending", false: sub.Status}[isNew], fmt.Sprintf("订阅「%s」已保存", payload.Title), nil)
	fresh, err := GetSubscription(sub.ID)
	if err != nil {
		return nil, "", err
	}
	return fresh, warning, nil
}

// replaceSubscriptionRules 全量替换规则（最多 30 条）
func replaceSubscriptionRules(subscriptionID uint, rules []SubscriptionRulePayload) error {
	if len(rules) == 0 {
		return nil
	}
	if len(rules) > 30 {
		rules = rules[:30]
	}
	return ddb.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("subscription_id = ?", subscriptionID).Delete(&DiscoverySubscriptionRule{}).Error; err != nil {
			return err
		}
		for i, rule := range rules {
			enabled := true
			if rule.Enabled != nil {
				enabled = *rule.Enabled
			}
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = fmt.Sprintf("自动规则 %d", i+1)
			}
			provider, perr := NormalizeTransferProvider(rule.TargetProvider)
			if perr != nil {
				provider = "123"
			}
			match := map[string]any{
				"message_keywords": normalizeStringList(rule.MessageKeywords),
				"must_contain":     normalizeStringList(rule.MustContain),
				"must_not_contain": normalizeStringList(rule.MustNotContain),
			}
			pref := rule.Preferences
			if pref == nil {
				pref = map[string]any{}
			}
			row := DiscoverySubscriptionRule{
				SubscriptionID: subscriptionID, Name: name, Enabled: enabled,
				TargetProvider: provider, MaxPoints: clampInt(rule.MaxPoints, 0, 99999),
				Preferences: marshalJSON(pref), Match: marshalJSON(match),
				Position: i, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteSubscription 删除订阅（级联清理）
func DeleteSubscription(id uint) error {
	return ddb.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("subscription_id = ?", id).Delete(&DiscoverySubscriptionRule{}).Error; err != nil {
			return err
		}
		if err := tx.Where("subscription_id = ?", id).Delete(&DiscoverySubscriptionItem{}).Error; err != nil {
			return err
		}
		if err := tx.Where("subscription_id = ?", id).Delete(&DiscoverySubscriptionEvent{}).Error; err != nil {
			return err
		}
		return tx.Delete(&DiscoverySubscription{}, id).Error
	})
}

// ListSubscriptionRuns 执行轮次历史
func ListSubscriptionRuns(subscriptionID uint, limit int) ([]DiscoverySubscriptionRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var runs []DiscoverySubscriptionRun
	query := ddb.Db.Order("id desc").Limit(limit)
	if subscriptionID > 0 {
		query = query.Where("subscription_id = ?", subscriptionID)
	}
	err := query.Find(&runs).Error
	return runs, err
}

// ListSubscriptionEvents 事件流
func ListSubscriptionEvents(subscriptionID uint, limit int) ([]DiscoverySubscriptionEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	var events []DiscoverySubscriptionEvent
	query := ddb.Db.Order("id desc").Limit(limit)
	if subscriptionID > 0 {
		query = query.Where("subscription_id = ?", subscriptionID)
	}
	err := query.Find(&events).Error
	return events, err
}

// ListSubscriptionItems 候选列表（candidates 审阅）
func ListSubscriptionItems(subscriptionID uint, status string, limit int) ([]DiscoverySubscriptionItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := ddb.Db.Order("id desc").Limit(limit)
	if subscriptionID > 0 {
		query = query.Where("subscription_id = ?", subscriptionID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var items []DiscoverySubscriptionItem
	err := query.Find(&items).Error
	return items, err
}

// ---------------------------------------------------------------------------
// 执行引擎
// ---------------------------------------------------------------------------

// ProcessSubscription 执行一次订阅检查（manual/scheduled，同订阅串行）
func ProcessSubscription(id uint, trigger string) (*DiscoverySubscriptionRun, error) {
	lock := lockForKey(fmt.Sprintf("sub-%d", id))
	lock.Lock()
	defer lock.Unlock()
	return processSubscriptionLocked(id, trigger)
}

// processSubscriptionLocked 单订阅处理（持锁）
func processSubscriptionLocked(id uint, trigger string) (*DiscoverySubscriptionRun, error) {
	sub, err := GetSubscription(id)
	if err != nil {
		return nil, err
	}
	if sub.EntityType == "person" {
		return nil, fmt.Errorf("人物订阅仅记录后续新作品，不发起资源转存")
	}
	now := time.Now()
	run := DiscoverySubscriptionRun{
		SubscriptionID: &sub.ID, TriggerType: trigger, Status: "running", StartedAt: now,
	}
	ddb.Db.Create(&run)
	recordSubscriptionEvent(sub, &run.ID, nil, "check_started", "running", fmt.Sprintf("开始检查订阅「%s」", sub.Title), nil)

	interval := clampInt(sub.IntervalMinutes, 15, 10080)
	nextCheck := now.Add(time.Duration(interval) * time.Minute)
	defer func() {
		ddb.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).Updates(map[string]any{
			"last_checked_at": now, "next_check_at": nextCheck, "updated_at": time.Now(),
		})
	}()

	// T06 · 时段窗口：**只拦定时触发**，且直接跳过不补跑。
	// next_check_at 照常推进（上面的 defer），下一轮按正常周期再来，
	// 不是「攒着等时段到了集中补跑」—— 集中补跑的突发流量正是风控盯的形态。
	windows := parseTimeWindows(guardrailString(KeySubscriptionTimeWindows, ""))
	if !triggerExemptFromWindow(trigger) && outsideTimeWindows(windows, now) {
		finishedAt := now
		ddb.Db.Model(&run).Updates(map[string]any{
			"status": "skipped", "message": timeWindowSkipMessage,
			"detail":     marshalJSON(map[string]any{"windows": windows, "reason": timeWindowSkipMessage}),
			"finished_at": &finishedAt,
		})
		log.Printf("[subscription] sub=%d %s：%s（时段 %s），本轮不检索、不补跑",
			sub.ID, trigger, timeWindowSkipMessage, describeTimeWindows(windows))
		recordSubscriptionEvent(sub, &run.ID, nil, "check_skipped", "skipped",
			fmt.Sprintf("时段外跳过（%s），本轮不检索", describeTimeWindows(windows)), nil)
		return &run, nil
	}

	rules := effectiveSubscriptionRules(sub)
	failures := []map[string]any{}
	ruleDetails := []map[string]any{}
	transferredTotal, skippedTotal, resourceTotal := 0, 0, 0
	// T06 · 执行预算整轮共享（参考实现 口径是「单订阅的候选尝试数」，不是单条规则）。
	budget := newRunBudget(resolveExecutionProfile(false))

	for _, rule := range rules {
		detail := map[string]any{
			"rule_id": rule.ID, "rule_name": rule.Name,
			"resource_count": 0, "selected_count": 0, "transferred_count": 0, "skipped_count": 0,
			"status": "no_update",
		}
		if !rule.Enabled {
			detail["status"] = "paused"
			ruleDetails = append(ruleDetails, detail)
			continue
		}
		// T06 · 完结宽限 · 先判一次（在检索之前）。
		// 早该收手的剧不该再白花一次检索请求：门禁在门里那一步
		// 就该拦，而不是进门之后才知道今天不营业。
		if done := deriveFinishedState(sub, rule, nil); done.Finished {
			detail["status"] = "skipped"
			detail["finished_reason"] = done.Reason
			detail["finished_at"] = done.FinishedAt
			ruleDetails = append(ruleDetails, detail)
			recordSubscriptionEvent(sub, &run.ID, nil, "rule_finished", "skipped",
				fmt.Sprintf("规则「%s」本季已完结：%s", rule.Name, done.Reason), nil)
			continue
		}
		// 搜索（RE0 开放接口按 TMDB ID 检索）
		candidates, err := searchSubscriptionResources(sub)
		if err != nil {
			detail["status"] = "failed"
			detail["error"] = err.Error()
			failures = append(failures, map[string]any{"rule": rule.Name, "error": err.Error()})
			ruleDetails = append(ruleDetails, detail)
			recordSubscriptionEvent(sub, &run.ID, nil, "rule_failed", "failed", fmt.Sprintf("规则「%s」检索失败：%v", rule.Name, err), nil)
			continue
		}
		resourceTotal += len(candidates)
		detail["resource_count"] = len(candidates)

		// T06 · 完结宽限 · 检索之后再判一次。
		// 补上两个上面看不到的情况：本轮候选第一次给出总集数（此前一直未知，
		// 所以检索前推不出分母），以及本轮刚好把最后一集补齐。
		if done := deriveFinishedState(sub, rule, candidates); done.Finished {
			detail["status"] = "skipped"
			detail["finished_reason"] = done.Reason
			detail["finished_at"] = done.FinishedAt
			ruleDetails = append(ruleDetails, detail)
			recordSubscriptionEvent(sub, &run.ID, nil, "rule_finished", "skipped",
				fmt.Sprintf("规则「%s」本季已完结：%s", rule.Name, done.Reason), nil)
			continue
		} else if done.InGrace {
			// 宽限期内照常检索，但把整轮的执行强度压到保守档。
			// 场景很具体：刚转存完最后一集，站方随后补出更优的压制组版本，
			// 这几天正是捡漏的时候，但不能按用户选的强度去抢。
			detail["finished_grace"] = done.Reason
			detail["finished_at"] = done.FinishedAt
			budget.enforceGrace()
			log.Printf("[subscription] sub=%d rule=%s 宽限期内：%s，执行强度降为保守档（%s）",
				sub.ID, rule.Name, done.Reason, budget.describe())
		}

		// 规则过滤 + 洗版计划 + 转存
		selected, skipped, transferred, planFailures := planAndTransferRuleCandidates(sub, rule, candidates, run.ID, budget)
		detail["selected_count"] = selected
		detail["skipped_count"] = skipped
		detail["transferred_count"] = transferred
		transferredTotal += transferred
		skippedTotal += skipped
		if len(planFailures) > 0 {
			failures = append(failures, planFailures...)
		}
		if transferred > 0 {
			detail["status"] = "success"
		} else if len(planFailures) > 0 {
			detail["status"] = "partial"
		}
		ruleDetails = append(ruleDetails, detail)
	}

	// 汇总
	status := "no_update"
	messageParts := []string{}
	if transferredTotal > 0 {
		status = "success"
		messageParts = append(messageParts, fmt.Sprintf("已自动转存 %d 条", transferredTotal))
	}
	if skippedTotal > 0 {
		messageParts = append(messageParts, fmt.Sprintf("已跳过 %d 条", skippedTotal))
	}
	if len(failures) > 0 {
		status = map[bool]string{true: "partial", false: "failed"}[transferredTotal > 0]
		messageParts = append(messageParts, fmt.Sprintf("%d 条转存失败", len(failures)))
	}
	if len(messageParts) == 0 {
		messageParts = append(messageParts, "未发现新的匹配资源")
	}
	message := strings.Join(messageParts, "；")
	finishedAt := time.Now()
	detailJSON := marshalJSON(map[string]any{"rules": ruleDetails, "failures": failures})
	ddb.Db.Model(&run).Updates(map[string]any{
		"status": status, "resource_count": resourceTotal,
		"selected_count": transferredTotal, "transferred_count": transferredTotal,
		"message": message, "detail": detailJSON, "finished_at": &finishedAt,
	})
	ddb.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).Update("status", status)
	recordSubscriptionEvent(sub, &run.ID, nil, "check_finished", status, message, map[string]any{
		"transferred": transferredTotal, "skipped": skippedTotal,
	})
	fresh, _ := GetSubscription(sub.ID)
	if fresh != nil {
		*sub = *fresh
	}
	return &run, nil
}

// searchSubscriptionResources 检索候选资源（T05 起走搜索连接器，可多源）。
//
// T05 之前这里是写死的一行 Tgto123SearchResources(...)，之后是：
//
//  1. 按订阅的 search_sources 取连接器（空 ⇒ 回落全局默认，默认只有 tgto123）；
//  2. SearchAll 串行检索，每源独立超时，整轮有总预算；
//  3. Merge 按源顺序合并去重（去重键与 T04 账本 content_key 同口径）；
//  4. 按订阅的画质/特效/体积闸门过滤；
//  5. 转回 resourceCandidate，用**既有**的 candidateSortKey 排序。
//
// 关于「存量订阅结果不变」这条硬验收，关键在第 5 步不做任何替换：
// candidateSortKey（已解锁 > 积分低 > 解锁人数）是改动前就有的排序，
// 它仍然是唯一的排序主键，多源场景下也不改它的口径。
// 跨源比较要用到的「显式指定 > 新鲜度 > 清晰度 > 码率」只作为**并列时的
// tie-break**（见 sortCandidates），权重刻意取得极小，绝不会跨过主键的差异。
// 另外 tgto123 路径上 Item 带着原始 hdhive.Resource，转回候选时直接复用
// resourceFromHive，与接口化之前同一个函数、同一个输入。
func searchSubscriptionResources(sub *DiscoverySubscription) ([]resourceCandidate, error) {
	if sub.TMDBID <= 0 {
		return nil, fmt.Errorf("订阅缺少 TMDB ID，无法安全检索")
	}
	mediaType := sub.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}
	ctx := context.Background()
	items, skipped := searchWithConnectors(ctx, sub, connector.Query{
		Title: sub.Title, OriginalTitle: sub.OriginalTitle,
		TMDBID: strconvI64(sub.TMDBID), MediaType: mediaType,
	})

	gate := gateForSubscription(sub)
	if gate.Enabled() {
		var dropped []gatedOutItem
		items, dropped = filterByGate(gate, items)
		if len(dropped) > 0 {
			log.Printf("[discovery] subscription_gate sub_id=%d kept=%d filtered=%d（画质下限=%d 特效=%s 体积=%d~%d MB）",
				sub.ID, len(items), len(dropped), gate.ResolutionLevel, gate.Effect,
				sub.MinFileSizeMB, sub.MaxFileSizeMB)
			// 逐条再记一行带 reason 的，便于事后按 reason_code 统计用户最常撞到的闸门。
			for _, d := range dropped {
				log.Printf("[discovery] subscription_gate_drop sub_id=%d source=%s item=%s title=%s reason=%s",
					sub.ID, d.Item.SourceKey, d.Item.Slug, d.Item.Title, d.Reason)
	}
	}
	}

	entries := make([]subscriptionCandidate, 0, len(items))
	for _, it := range items {
		cand, ok := candidateFromConnectorItem(it)
		if !ok {
			continue
		}
		entries = append(entries, subscriptionCandidate{cand: cand, it: it})
	}
	sortCandidates(entries)
	candidates := make([]resourceCandidate, 0, len(entries))
	for _, e := range entries {
		candidates = append(candidates, e.cand)
	}
	if len(candidates) == 0 && len(skipped) > 0 {
		// 一个候选都没有 + 有源被跳过 = 用户配的源全都不工作。
		// 这种情况要报错而不是返回空，否则订阅显示「未发现新资源」，
		// 用户根本看不出是自己的配置错了还是真的没有资源。
		return nil, fmt.Errorf("订阅配置的搜索源均不可用：%s", describeSkippedConnectors(skipped))
	}
	return candidates, nil
}

// candidateFromConnectorItem connector.Item → 订阅候选。
// 带原始 hdhive.Resource 的走 resourceFromHive（存量路径逐字节一致），
// 其余（未来的纯磁力/直链源）走通用字段映射。
func candidateFromConnectorItem(it connector.Item) (resourceCandidate, bool) {
	if r, ok := hiveResourceOf(it); ok {
		cand := resourceFromHive(r)
		cand.Source = firstNonEmptyStr(it.SourceKey, cand.Source)
		return cand, true
	}
	title := strings.TrimSpace(it.Title)
	if title == "" {
		return resourceCandidate{}, false
	}
	cand := resourceCandidate{
		Source: it.SourceKey, Provider: it.Provider, Title: title, Slug: it.Slug,
		LinkType: string(it.Kind), Remark: it.Remark, MediaType: firstNonEmptyStr(it.MediaType, "movie"),
		Size: formatBytes(it.SizeBytes), SpecTags: it.SpecTags,
		SubtitleLanguages: it.SubtitleLanguages,
		IsUnlocked:        it.IsUnlocked, UnlockPoints: it.UnlockPoints,
		UnlockedUsersCount: it.UnlockedUsersCount,
	}
	switch it.Kind {
	case connector.ItemShareLink:
		cand.ShareURL = firstNonEmptyStr(it.Slug, it.ShareCode)
	case connector.ItemMagnet:
		cand.ShareURL = it.MagnetURI
	case connector.ItemEd2k:
		cand.ShareURL = it.Ed2kLink
	case connector.ItemDirectLink:
		cand.ShareURL = it.DirectURL
	case connector.ItemTorrent:
		cand.ShareURL = it.InfoHash
	default:
		return resourceCandidate{}, false
	}
	cand.PointsKnown = cand.LinkType != "magnet" && cand.LinkType != "ed2k" && it.UnlockPoints > 0
	if cand.MediaType == "tv" && it.Episode > 0 {
		ep := &candidateEpisode{}
		if it.Season > 0 {
			s := it.Season
			ep.SeasonNum = &s
		}
		e := it.Episode
		ep.EpisodeNum = &e
		if it.EndEpisode > it.Episode {
			ee := it.EndEpisode
			ep.EndEpisodeNum = &ee
		}
		cand.Episode = ep
	}
	return cand, true
}

// subscriptionCandidate 候选 + 它对应的归一化 Item。
//
// 两个必须一起传：排序的主键读候选（积分/解锁状态只在候选上有），
// tie-break 读 Item（分辨率、编码、显式指定、新鲜度只在 Item 上算得出）。
// 只传候选的话 tie-break 就得从候选里把画质重新解析一遍 —— 那是同一份解析
// 跑第二遍，既浪费又可能与连接器给出的结果不一致（站方标签优先于标题）。
type subscriptionCandidate struct {
	cand  resourceCandidate
	it    connector.Item
	score float64
}

// sortCandidates 候选排序。
//
// 主键 = 既有 candidateSortKey（已解锁 > 积分低 > 解锁人数），一字未改 ——
// T05 之前就是它排的序，跨源场景下也不换。tie-break 才是连接器的
// 「显式指定 > 新鲜度 > 清晰度 > 码率」，只在主键**完全相等**时才比较，
// 所以它不可能改变既有相对顺序。这是「存量订阅结果不变」这条验收的关键：
// 新维度只有并列时才生效。
func sortCandidates(entries []subscriptionCandidate) {
	if len(entries) < 2 {
		return
	}
	for i := range entries {
		entries[i].score = candidateSortKey(entries[i].cand)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return connector.ItemLess(entries[i].it, entries[j].it)
	})
}

// describeSkippedConnectors 把被跳过的源拼成人话，用于报错信息。
func describeSkippedConnectors(skipped []connector.SkipReason) string {
	if len(skipped) == 0 {
		return "未指定"
	}
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, s.Key+"（"+s.Reason+"）")
	}
	return strings.Join(parts, "、")
}

// effectiveSubscriptionRules 有效规则（无规则时合成默认规则）
func effectiveSubscriptionRules(sub *DiscoverySubscription) []DiscoverySubscriptionRule {
	if len(sub.Rules) > 0 {
		return sub.Rules
	}
	pref := sub.Pref
	maxPoints := 4
	if v, ok := toFloat(pref["max_points"]); ok {
		maxPoints = int(v)
	}
	return []DiscoverySubscriptionRule{{
		ID: 0, Name: "自动规则 1", Enabled: true,
		TargetProvider: firstNonEmptyStr(sub.TargetProvider, "123"),
		MaxPoints:      maxPoints, Pref: pref,
		MatchData: map[string]any{},
	}}
}

// planAndTransferRuleCandidates 规则过滤 → 洗版去重 → 自动转存
func planAndTransferRuleCandidates(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, candidates []resourceCandidate, runID uint, budget *runBudget) (selected, skipped, transferred int, failures []map[string]any) {
	// ★ 词表过滤统一走 rule_match.go 的 RuleFilter：预览接口 EvaluateRuleMatch
	//   调用的就是同一组函数，保证 UI 展示的判定 = 这里实际的判定。
	filter := RuleFilterFromMatch(rule.MatchData)
	maxPoints := rule.MaxPoints
	if v, ok := toFloat(rule.Pref["max_points"]); ok && int(v) > 0 {
		maxPoints = int(v)
	}

	// 第一层：同轮去重。同一轮里 batchScopes 挡住「同一季被 5 个帖各命中一次」。
	// 第三层：历史位点基线，**只在媒体库索引不可用时**顶替（见 baselineFallback）。
	//   原来这里是唯一去重且永久生效，导致用户删掉的文件永远拉不回来 —— 这次改掉。
	batchScopes := map[string]bool{}
	baselineScopes := transferredScopesFor(sub.ID, rule.ID)

	for _, cand := range candidates {
		itemKey := ruleItemKey(rule, cand)
		rememberSubscriptionItem(sub.ID, runID, itemKey, cand)
		text := strings.ToLower(firstNonEmptyStr(cand.ChannelTitle, "") + "\n" + cand.Title + "\n" + cand.Remark)
		_ = text
		if reason, blocked := filter.RuleSkipReason(cand.Title, cand.Remark); blocked {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", reason)
			skipped++
			continue
		}
		scope := candidateScopeKey(cand)
		// 第一层：同轮去重。
		if scope != "unknown" && batchScopes[scope] {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", "本轮内该影片或季集范围已转存")
			skipped++
			continue
		}
		// 转存前置校验
		if blockReason := candidateBlockReason(cand, rule.TargetProvider, maxPoints); blockReason != "" {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", blockReason)
			skipped++
			continue
		}
		// ★ 身份校验闸门（参考实现 不变式：未通过身份校验，一律不转存）。
		//   放在 candidateBlockReason 之后、转存调用之前 —— 这是最后一道拦截。
		//   候选若是另一部剧/另一季/另一集，或者只有磁力链接拿不到任何身份证据，
		//   都在这里被挡下，不会写进用户网盘。
		if res := checkCandidateIdentity(sub, rule, cand, runID); !res.Passed {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", identitySkipReason(res))
			skipped++
			continue
		}
		// 第二/三层去重裁决：账本 CAS + 保护期 + 媒体库真值 + 位点基线兜底。
		// 放在身份校验之后：省掉身份对不上的那部分开销（媒体库查询要走 SQLite）。
		dedup := decideTransfer(sub, rule, cand, baselineScopes)
		if !dedup.Transfer {
			setSubscriptionItemState(sub.ID, itemKey, "skipped",
				fmt.Sprintf("%s：%s", describeDedupLayer(dedup.Layer), dedup.Message))
			skipped++
			continue
		}
		// T06 · 执行强度：Acquire 放在**所有判定之后、转存之前**。
		// 放这里才满足任务书两条语义：
		//   - 上面被词表/去重/身份校验/预算筛掉的候选一次都不消耗次数；
		//   - Acquire 一旦放行就已经计数，所以下面转存失败的也照常计数。
		// 用尽后直接收手，不再继续空转扫描后面的候选 —— 反正也转不动了。
		if err := budget.Acquire(context.Background()); err != nil {
			log.Printf("[subscription] sub=%d rule=%s 执行强度已用尽（%s），剩余 %d 条候选不再尝试",
				sub.ID, rule.Name, budget.describe(), len(candidates))
			recordSubscriptionEvent(sub, &runID, nil, "budget_exhausted", "skipped",
				fmt.Sprintf("执行强度已用尽（%s），本轮停止尝试", budget.describe()), nil)
			break
		}
		// 执行转存
		setSubscriptionItemState(sub.ID, itemKey, "transferring", "自动转存中")
		title, total, err := transferSubscriptionCandidate(cand, rule.TargetProvider)
		if err != nil {
			// 失败要把账本条目退回可重试状态，否则这一批内容会被保护期白白挡掉一个周期。
			FailLedgerEntry(dedup.Ledger, "TRANSFER_FAILED")
			setSubscriptionItemState(sub.ID, itemKey, "failed", err.Error())
			failures = append(failures, map[string]any{"item": cand.Title, "error": err.Error()})
			recordSubscriptionEvent(sub, &runID, nil, "transfer_failed", "failed", fmt.Sprintf("「%s」转存失败：%v", cand.Title, err), nil)
			continue
		}
		ConfirmLedgerEntry(dedup.Ledger)
		transferred++
		if scope != "unknown" {
			batchScopes[scope] = true
		}
		now := time.Now()
		ddb.Db.Model(&DiscoverySubscriptionItem{}).
			Where("subscription_id = ? AND item_key = ?", sub.ID, itemKey).
			Updates(map[string]any{"status": "transferred", "transferred_at": &now})
		recordSubscriptionEvent(sub, &runID, nil, "transfer_succeeded", "success",
			fmt.Sprintf("「%s」已转存到%s（%s，%d 个文件）", cand.Title, resourceProviderName(rule.TargetProvider), title, total), nil)
		selected++
	}
	return selected, skipped, transferred, failures
}

// transferSubscriptionCandidate 转存单条候选（tgto123 反代：解锁+转存一体，
// 落盘目录由 tgto123 基础配置的保存目录决定）
func transferSubscriptionCandidate(cand resourceCandidate, provider string) (string, int, error) {
	if strings.TrimSpace(cand.Slug) == "" {
		return "", 0, fmt.Errorf("候选缺少 slug，无法通过 tgto123 转存")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	msg, err := Tgto123TransferResource(ctx, cand.Slug, firstNonEmptyStr(provider, "123"))
	return msg, 1, err
}

// candidateBlockReason 转存前置校验（对齐参考实现 _automatic_candidate_block_reason）
func candidateBlockReason(cand resourceCandidate, targetProvider string, maxPoints int) string {
	if cand.Source == "re0" && cand.Slug == "" && cand.ShareURL == "" {
		return "不是可由 RE0 自动解锁的资源"
	}
	if cand.Provider != "" && cand.LinkType != "magnet" && cand.LinkType != "ed2k" {
		if normalizeProviderKey(cand.Provider) != normalizeProviderKey(targetProvider) {
			return fmt.Sprintf("资源网盘类型（%s）与订阅目标（%s）不一致", resourceProviderName(normalizeProviderKey(cand.Provider)), resourceProviderName(targetProvider))
		}
	}
	if !cand.IsUnlocked && !cand.PointsKnown {
		return "资源积分未知，已跳过自动解锁"
	}
	if !cand.IsUnlocked && cand.PointsKnown && maxPoints > 0 && cand.UnlockPoints > maxPoints {
		return fmt.Sprintf("资源积分超过规则上限 %d", maxPoints)
	}
	return ""
}

// candidateScopeKey 季集范围键（洗版去重）
func candidateScopeKey(cand resourceCandidate) string {
	if cand.MediaType == "movie" {
		return "movie"
	}
	ep := cand.Episode
	if ep == nil {
		return "unknown"
	}
	if ep.EpisodeNum != nil {
		key := fmt.Sprintf("S%02dE%02d", derefInt(ep.SeasonNum, 1), *ep.EpisodeNum)
		if ep.EndEpisodeNum != nil && *ep.EndEpisodeNum > *ep.EpisodeNum {
			key += fmt.Sprintf("-E%02d", *ep.EndEpisodeNum)
		}
		return key
	}
	if ep.SeasonNum != nil {
		return fmt.Sprintf("S%02d", *ep.SeasonNum)
	}
	return "unknown"
}

// transferredScopesFor 已转存的 scope 集合（同订阅同规则）
func transferredScopesFor(subscriptionID, ruleID uint) map[string]bool {
	out := map[string]bool{}
	var items []DiscoverySubscriptionItem
	ddb.Db.Where("subscription_id = ? AND status = 'transferred'", subscriptionID).Find(&items)
	for _, item := range items {
		cand := parseJSONObject(item.Candidate)
		_ = cand
		// scope 从候选 JSON 恢复
		ep, _ := cand["episode"].(map[string]any)
		mediaType, _ := cand["media_type"].(string)
		if mediaType == "movie" {
			out["movie"] = true
			continue
		}
		if ep != nil {
			out[scopeFromEpisodeMap(ep)] = true
		}
	}
	return out
}

// scopeFromEpisodeMap 从候选 JSON 的 episode 恢复 scope
func scopeFromEpisodeMap(ep map[string]any) string {
	seasonNum := int(toFloatDefault(ep["season_num"], 1))
	if v, ok := ep["episode_num"].(float64); ok {
		key := fmt.Sprintf("S%02dE%02d", seasonNum, int(v))
		if end, ok := ep["end_episode_num"].(float64); ok && int(end) > int(v) {
			key += fmt.Sprintf("-E%02d", int(end))
		}
		return key
	}
	return fmt.Sprintf("S%02d", seasonNum)
}

// rememberSubscriptionItem 记录/刷新候选（对齐参考实现 remember_candidates）
func rememberSubscriptionItem(subscriptionID, runID uint, itemKey string, cand resourceCandidate) {
	var exist DiscoverySubscriptionItem
	candJSON := marshalJSON(cand)
	now := time.Now()
	if err := ddb.Db.Where("subscription_id = ? AND item_key = ?", subscriptionID, itemKey).First(&exist).Error; err == nil {
		ddb.Db.Model(&exist).Updates(map[string]any{"last_seen_at": now, "run_id": runID, "candidate": candJSON})
		return
	}
	runRef := runID
	ddb.Db.Create(&DiscoverySubscriptionItem{
		SubscriptionID: subscriptionID, ItemKey: itemKey, RunID: &runRef,
		Provider: cand.Provider, Slug: cand.Slug, Title: cand.Title,
		Status: "discovered", Candidate: candJSON, FirstSeenAt: now, LastSeenAt: now,
	})
}

// setSubscriptionItemState 更新候选状态
func setSubscriptionItemState(subscriptionID uint, itemKey, status, message string) {
	ddb.Db.Model(&DiscoverySubscriptionItem{}).
		Where("subscription_id = ? AND item_key = ?", subscriptionID, itemKey).
		Updates(map[string]any{"status": status})
	recordSubscriptionEvent(nil, nil, nil, "candidate_"+status, status, fmt.Sprintf("「%s」：%s", itemKey, message), nil)
}

// recordSubscriptionEvent 记录事件
func recordSubscriptionEvent(sub *DiscoverySubscription, runID, itemID *uint, eventType, status, message string, detail map[string]any) {
	event := DiscoverySubscriptionEvent{
		EventType: eventType, Status: status, Message: truncateStr(message, 500),
		Detail: marshalJSON(detail), CreatedAt: time.Now(),
	}
	if sub != nil {
		event.SubscriptionID = &sub.ID
	}
	event.RunID = runID
	event.ItemID = itemID
	ddb.Db.Create(&event)
}

// ruleItemKey 规则内候选键（对齐参考实现：rule_key|resource_key）
func ruleItemKey(rule DiscoverySubscriptionRule, cand resourceCandidate) string {
	resourceKey := firstNonEmptyStr(cand.Slug, cand.ShareURL, cand.Title)
	key := fmt.Sprintf("%d|%s", rule.ID, resourceKey)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:24]
}

// RunDueSubscriptions 执行到期订阅（worker 调用）
func RunDueSubscriptions(limit int) []map[string]any {
	if limit <= 0 {
		limit = 5
	}
	var subs []DiscoverySubscription
	now := time.Now()
	if err := ddb.Db.Where("enabled = ? AND next_check_at <= ? AND entity_type != 'person'", true, now).
		Order("next_check_at asc").Limit(clampInt(limit, 1, 100)).Find(&subs).Error; err != nil {
		return nil
	}
	results := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		run, err := ProcessSubscription(sub.ID, "scheduled")
		result := map[string]any{"subscription_id": sub.ID, "success": err == nil}
		if err != nil {
			result["error"] = err.Error()
		} else if run != nil {
			result["run"] = map[string]any{"id": run.ID, "status": run.Status, "message": run.Message}
		}
		results = append(results, result)
	}
	return results
}

// subscriptionWorker 订阅调度 Worker（30s 轮询）
func subscriptionWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[discovery] 订阅 Worker 恢复自 panic：%v", r)
				}
			}()
			RunDueSubscriptions(5)
		}()
	}
}

type hdhiveUnlock struct {
	URL        string
	AccessCode string
	PanType    string
	Title      string
}

// ---------------------------------------------------------------------------
// 资源候选（RE0 响应 → 统一候选）
// ---------------------------------------------------------------------------

// resourceCandidate 订阅资源候选
type resourceCandidate struct {
	ItemKey     string         `json:"item_key"`
	Source      string         `json:"source"`
	Provider    string         `json:"provider"`
	Title       string         `json:"title"`
	Slug        string         `json:"slug"`
	ShareURL    string         `json:"share_url"`
	AccessCode  string         `json:"access_code"`
	LinkType    string         `json:"link_type"`
	Size        string         `json:"size"`
	Remark      string         `json:"remark"`
	ChannelTitle string        `json:"channel_title,omitempty"`
	IsUnlocked  bool           `json:"is_unlocked"`
	PointsKnown bool           `json:"points_known"`
	UnlockPoints int           `json:"unlock_points"`
	UnlockedUsersCount int     `json:"unlocked_users_count"`
	MediaType   string         `json:"media_type"`
	Episode     *candidateEpisode `json:"episode,omitempty"`
	SpecTags    []string       `json:"resource_spec_tags,omitempty"`
	SubtitleLanguages []string `json:"subtitle_languages,omitempty"`
	// Sha1 候选内容的文件哈希（40 位小写 hex），用于转存账本的文件级去重键。
	// 分享来源当前拿不到（驱动层只有 ProbeShareTitle，拿不到文件清单），
	// 字段先留着：本地文件 / 将来接了清单接口的来源可以直接填，账本会自动
	// 从 res:<slug> 的资源级键升级成 sha1:<hash> 的文件级键。
	Sha1 string `json:"sha1,omitempty"`
}

// candidateEpisode 季集证据
type candidateEpisode struct {
	SeasonNum       *int `json:"season_num"`
	EpisodeNum      *int `json:"episode_num"`
	EndEpisodeNum   *int `json:"end_episode_num"`
	TotalEpisodeNum *int `json:"total_episode_num"`
	IsComplete      bool `json:"is_complete"`
	IsUpdated       bool `json:"is_updated"`
}

// resourceFromHive RE0 资源 → 候选
func resourceFromHive(r hdhive.Resource) resourceCandidate {
	linkType := strings.ToLower(strings.TrimSpace(r.PanType))
	cand := resourceCandidate{
		Source: "re0", Provider: r.PanType, Title: r.Title, Slug: r.Slug,
		LinkType: linkType, Size: r.ShareSize, Remark: r.Remark,
		IsUnlocked: r.IsUnlocked, MediaType: "movie",
		SpecTags: append(append([]string{}, r.VideoResolution...), r.Source...),
		SubtitleLanguages: r.SubtitleLanguage,
	}
	cand.PointsKnown = linkType != "magnet" && linkType != "ed2k" && r.UnlockPoints > 0
	cand.UnlockPoints = r.UnlockPoints
	cand.UnlockedUsersCount = r.UnlockedUsersCount
	if linkType == "magnet" || linkType == "ed2k" {
		cand.ShareURL = r.Slug
	}
	// 从标题解析季集证据
	cand.MediaType, cand.Episode = extractEpisodeEvidence(r.Title, r.Remark)
	return cand
}

// extractEpisodeEvidence 从标题/备注提取季集证据。
//
// T06 起委托给 rules.SeasonEpisodeFromText（见同包 season_evidence.go 的适配层）。
// 保留原函数名是因为它是既有内部契约的名字，改名只会让 diff 变难读；
// 保留原语义是因为改动前能匹配的输入必须**逐条结果不变**（有表格测试钉住）。
//
// 为什么必须共用那套解析：订阅侧原本是**独立的两条正则**，跟整理侧各写各的，
// 结果是「流浪地球 第二季 第十二集」在整理侧能识别成 S02E12，在订阅侧却判成
// movie + 无集号。而 movie 候选的 scope 一律是 "movie"，于是同一轮里第一个
// 中文剧候选转存成功后 batchScopes["movie"]=true，**同一部剧的 E02…E12 全部
// 被判成本轮已转存而跳过** —— 中文剧集订阅原本是坏的。
func extractEpisodeEvidence(parts ...string) (string, *candidateEpisode) {
	return episodeEvidenceFromText(parts...)
}

func atoiSafe(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// candidateSortKey 排序权重（已解锁优先 → 积分低优先 → 解锁人数）
func candidateSortKey(cand resourceCandidate) float64 {
	score := 0.0
	if cand.IsUnlocked {
		score += 100
	}
	if cand.PointsKnown && cand.UnlockPoints > 0 {
		score += float64(1000-cand.UnlockPoints) / 10
	}
	score += float64(cand.UnlockedUsersCount) / 100
	return score
}

func containsAny(text string, keywords []string) bool {
	for _, k := range keywords {
		if k != "" && strings.Contains(text, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func firstMissing(text string, all []string) string {
	for _, k := range all {
		if k != "" && !strings.Contains(text, strings.ToLower(k)) {
			return k
		}
	}
	return ""
}

func firstHit(text string, banned []string) string {
	for _, k := range banned {
		if k != "" && strings.Contains(text, strings.ToLower(k)) {
			return k
		}
	}
	return ""
}

func normalizeStringList(list []string) []string {
	out := []string{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringListFromAny(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := []string{}
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func toFloatDefault(v any, def float64) float64 {
	if n, ok := toFloat(v); ok {
		return n
	}
	return def
}

func strconvI64(id int64) string {
	return strconv.FormatInt(id, 10)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
