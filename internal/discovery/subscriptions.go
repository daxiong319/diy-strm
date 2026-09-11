package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/hdhive"

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
	query := db.Db.Order("id desc")
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
	if err := db.Db.First(&sub, id).Error; err != nil {
		return nil, fmt.Errorf("订阅不存在")
	}
	attachSubscriptionExtras(&sub)
	return &sub, nil
}

// GetSubscriptionByKey 按 entity_key 查订阅
func GetSubscriptionByKey(entityKey string) (*DiscoverySubscription, error) {
	var sub DiscoverySubscription
	if err := db.Db.Where("entity_key = ?", entityKey).First(&sub).Error; err != nil {
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
	db.Db.Where("subscription_id = ?", sub.ID).Order("position asc, id asc").Find(&rules)
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

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	now := time.Now()
	nextCheck := now.Add(time.Duration(payload.IntervalMinutes) * time.Minute)

	var sub DiscoverySubscription
	err = db.Db.Where("entity_key = ?", entityKey).First(&sub).Error
	isNew := err != nil
	metadataJSON := marshalJSON(payload.Metadata)
	preferencesJSON := marshalJSON(payload.Preferences)
	if isNew {
		sub = DiscoverySubscription{
			EntityKey: entityKey, Source: "tmdb", EntityType: entityType,
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
		if err := db.Db.Create(&sub).Error; err != nil {
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
		if payload.TMDBID > 0 {
			updates["tmdb_id"] = payload.TMDBID
		}
		if err := db.Db.Model(&sub).Updates(updates).Error; err != nil {
			return nil, "", err
		}
	}
	if err := replaceSubscriptionRules(sub.ID, payload.Rules); err != nil {
		return nil, "", err
	}
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
	return db.Db.Transaction(func(tx *gorm.DB) error {
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
	return db.Db.Transaction(func(tx *gorm.DB) error {
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
	query := db.Db.Order("id desc").Limit(limit)
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
	query := db.Db.Order("id desc").Limit(limit)
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
	query := db.Db.Order("id desc").Limit(limit)
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
	db.Db.Create(&run)
	recordSubscriptionEvent(sub, &run.ID, nil, "check_started", "running", fmt.Sprintf("开始检查订阅「%s」", sub.Title), nil)

	interval := clampInt(sub.IntervalMinutes, 15, 10080)
	nextCheck := now.Add(time.Duration(interval) * time.Minute)
	defer func() {
		db.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).Updates(map[string]any{
			"last_checked_at": now, "next_check_at": nextCheck, "updated_at": time.Now(),
		})
	}()

	rules := effectiveSubscriptionRules(sub)
	failures := []map[string]any{}
	ruleDetails := []map[string]any{}
	transferredTotal, skippedTotal, resourceTotal := 0, 0, 0

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

		// 规则过滤 + 洗版计划 + 转存
		selected, skipped, transferred, planFailures := planAndTransferRuleCandidates(sub, rule, candidates, run.ID)
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
	db.Db.Model(&run).Updates(map[string]any{
		"status": status, "resource_count": resourceTotal,
		"selected_count": transferredTotal, "transferred_count": transferredTotal,
		"message": message, "detail": detailJSON, "finished_at": &finishedAt,
	})
	db.Db.Model(&DiscoverySubscription{}).Where("id = ?", sub.ID).Update("status", status)
	recordSubscriptionEvent(sub, &run.ID, nil, "check_finished", status, message, map[string]any{
		"transferred": transferredTotal, "skipped": skippedTotal,
	})
	fresh, _ := GetSubscription(sub.ID)
	if fresh != nil {
		*sub = *fresh
	}
	return &run, nil
}

// searchSubscriptionResources 检索 RE0 资源（tgto123 反代，按 TMDB ID）
func searchSubscriptionResources(sub *DiscoverySubscription) ([]resourceCandidate, error) {
	if sub.TMDBID <= 0 {
		return nil, fmt.Errorf("订阅缺少 TMDB ID，无法安全检索")
	}
	mediaType := sub.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}
	resources, err := Tgto123SearchResources(context.Background(), sub.Title, sub.TMDBID, mediaType, "")
	if err != nil {
		return nil, fmt.Errorf("tgto123 反代不可用：%v", err)
	}
	candidates := make([]resourceCandidate, 0, len(resources))
	for _, r := range resources {
		candidates = append(candidates, resourceFromHive(r))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidateSortKey(candidates[i]) > candidateSortKey(candidates[j])
	})
	return candidates, nil
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
func planAndTransferRuleCandidates(sub *DiscoverySubscription, rule DiscoverySubscriptionRule, candidates []resourceCandidate, runID uint) (selected, skipped, transferred int, failures []map[string]any) {
	match := rule.MatchData
	keywords := stringListFromAny(match["message_keywords"])
	mustContain := stringListFromAny(match["must_contain"])
	mustNotContain := stringListFromAny(match["must_not_contain"])
	maxPoints := rule.MaxPoints
	if v, ok := toFloat(rule.Pref["max_points"]); ok && int(v) > 0 {
		maxPoints = int(v)
	}

	// 已转存 scope（跨轮洗版基线）
	transferredScopes := transferredScopesFor(sub.ID, rule.ID)
	batchScopes := map[string]bool{}

	for _, cand := range candidates {
		itemKey := ruleItemKey(rule, cand)
		rememberSubscriptionItem(sub.ID, runID, itemKey, cand)
		text := strings.ToLower(firstNonEmptyStr(cand.ChannelTitle, "") + "\n" + cand.Title + "\n" + cand.Remark)
		_ = text
		matchText := strings.ToLower(cand.Title + "\n" + cand.Remark)
		if len(keywords) > 0 && !containsAny(matchText, keywords) {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", "未命中消息正文关键词")
			skipped++
			continue
		}
		if missing := firstMissing(matchText, mustContain); missing != "" {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", "标题正文未包含："+missing)
			skipped++
			continue
		}
		if hit := firstHit(matchText, mustNotContain); hit != "" {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", "标题正文命中排除词："+hit)
			skipped++
			continue
		}
		// 洗版去重：同 scope 已转存跳过
		scope := candidateScopeKey(cand)
		if scope != "unknown" && (transferredScopes[scope] || batchScopes[scope]) {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", "该影片或季集范围已有已转存版本，自动规则不会洗版")
			skipped++
			continue
		}
		// 转存前置校验
		if blockReason := candidateBlockReason(cand, rule.TargetProvider, maxPoints); blockReason != "" {
			setSubscriptionItemState(sub.ID, itemKey, "skipped", blockReason)
			skipped++
			continue
		}
		// 执行转存
		setSubscriptionItemState(sub.ID, itemKey, "transferring", "自动转存中")
		title, total, err := transferSubscriptionCandidate(cand, rule.TargetProvider)
		if err != nil {
			setSubscriptionItemState(sub.ID, itemKey, "failed", err.Error())
			failures = append(failures, map[string]any{"item": cand.Title, "error": err.Error()})
			recordSubscriptionEvent(sub, &runID, nil, "transfer_failed", "failed", fmt.Sprintf("「%s」转存失败：%v", cand.Title, err), nil)
			continue
		}
		transferred++
		batchScopes[scope] = true
		now := time.Now()
		db.Db.Model(&DiscoverySubscriptionItem{}).
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
	db.Db.Where("subscription_id = ? AND status = 'transferred'", subscriptionID).Find(&items)
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
	if err := db.Db.Where("subscription_id = ? AND item_key = ?", subscriptionID, itemKey).First(&exist).Error; err == nil {
		db.Db.Model(&exist).Updates(map[string]any{"last_seen_at": now, "run_id": runID, "candidate": candJSON})
		return
	}
	runRef := runID
	db.Db.Create(&DiscoverySubscriptionItem{
		SubscriptionID: subscriptionID, ItemKey: itemKey, RunID: &runRef,
		Provider: cand.Provider, Slug: cand.Slug, Title: cand.Title,
		Status: "discovered", Candidate: candJSON, FirstSeenAt: now, LastSeenAt: now,
	})
}

// setSubscriptionItemState 更新候选状态
func setSubscriptionItemState(subscriptionID uint, itemKey, status, message string) {
	db.Db.Model(&DiscoverySubscriptionItem{}).
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
	db.Db.Create(&event)
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
	if err := db.Db.Where("enabled = ? AND next_check_at <= ? AND entity_type != 'person'", true, now).
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

var episodeRangeRe = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})(?:[-~]\s*E?(\d{1,3}))?`)
var seasonOnlyRe = regexp.MustCompile(`(?i)(?:第\s*(\d{1,2})\s*季|S(\d{1,2})\b)`)

// extractEpisodeEvidence 从标题/备注提取季集证据
func extractEpisodeEvidence(parts ...string) (string, *candidateEpisode) {
	text := strings.Join(nonEmpty(parts), " ")
	if m := episodeRangeRe.FindStringSubmatch(text); m != nil {
		season := atoiSafe(m[1], 1)
		episode := atoiSafe(m[2], 0)
		ep := &candidateEpisode{SeasonNum: &season, EpisodeNum: &episode}
		if m[3] != "" {
			end := atoiSafe(m[3], episode)
			if end > episode {
				ep.EndEpisodeNum = &end
			}
		}
		return "tv", ep
	}
	if m := seasonOnlyRe.FindStringSubmatch(text); m != nil {
		raw := firstNonEmptyStr(m[1], m[2])
		season := atoiSafe(raw, 1)
		return "tv", &candidateEpisode{SeasonNum: &season}
	}
	return "movie", nil
}

func nonEmpty(list []string) []string {
	out := []string{}
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
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
