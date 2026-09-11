package discovery

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// RE0 订阅迁移：把旧 cloud_subscriptions（resource_source=hdhive）迁移到
// tgto123 对齐的 discovery_subscriptions 引擎。
// 迁移一次（幂等：已存在 entity_key 的跳过），迁移后旧订阅标记 status='migrated'。
// ---------------------------------------------------------------------------

// MigrateHiveSubscriptions 迁移 RE0 订阅到新引擎（幂等，启动时调用）
func MigrateHiveSubscriptions() error {
	var subs []models.CloudSubscription
	if err := db.Db.Where("resource_source = ? AND enabled = ? AND status != ?",
		"hdhive", true, "migrated").Find(&subs).Error; err != nil {
		return fmt.Errorf("查询旧 RE0 订阅失败：%w", err)
	}
	if len(subs) == 0 {
		return nil
	}
	migrated := 0
	for i := range subs {
		sub := &subs[i]
		entityKey := fmt.Sprintf("tmdb:%s:%d", ternaryStr(sub.MediaType == "tv", "tv", "movie"), sub.TMDBID)

		// 幂等：已有同 entity_key 的活跃订阅则跳过
		var count int64
		db.Db.Model(&DiscoverySubscription{}).Where("entity_key = ? AND enabled = ?", entityKey, true).Count(&count)
		if count > 0 {
			// 标记旧订阅已迁移
			sub.Status = "migrated"
			sub.Enabled = false
			models.SaveCloudSubscription(sub)
			migrated++
			continue
		}

		title := sub.TMDBTitle
		if title == "" {
			title = strings.Trim(sub.Keywords, `[""]`)
		}
		if title == "" || sub.TMDBID <= 0 {
			continue
		}

		// 元数据：保留 wash/total_episodes 上下文
		metadata := map[string]any{
			"media_type":     sub.MediaType,
			"migrated_from":  "cloud_subscription_" + fmt.Sprint(sub.ID),
			"total_episodes": sub.TotalEpisodes,
			"wash":           sub.Wash,
			"wash_target":    sub.WashTarget,
			"replace_old":    sub.ReplaceOld,
		}

		// 偏好：wash_target=4k → 2160p 优先
		resolutions := []string{"2160p", "1080p"}
		if sub.WashTarget != "" {
			resolutions = []string{strings.ToUpper(sub.WashTarget), "1080p"}
		}

		payload := &SubscriptionUpsertPayload{
			Source:          "tmdb",
			EntityType:      sub.MediaType,
			ExternalID:      fmt.Sprintf("%d", sub.TMDBID),
			TMDBID:          sub.TMDBID,
			MediaType:       sub.MediaType,
			Title:           title,
			TargetProvider:  sub.SourceType,
			TransferMode:    "auto",
			Enabled:         boolPtr(true),
			IntervalMinutes: 360, // 6 小时（与旧引擎轮询间隔一致）
			Preferences: map[string]any{
				"max_points": 4,
				"resolutions": resolutions,
				"qualities":  []string{"Remux", "BluRay", "WEB-DL"},
				"languages":  []string{"国语", "中字", "中文"},
			},
			Rules: []SubscriptionRulePayload{{
				Name:           "自动规则 1",
				Enabled:        boolPtr(true),
				TargetProvider: sub.SourceType,
				MaxPoints:      4,
				Preferences: map[string]any{
					"resolutions": resolutions,
					"qualities":   []string{"Remux", "BluRay", "WEB-DL"},
					"languages":   []string{"国语", "中字", "中文"},
				},
			}},
			Metadata: metadata,
		}

		sub2, warning, err := SaveSubscription(payload)
		if err != nil {
			log.Printf("[discovery] RE0订阅迁移失败 #%d（%s）：%v", sub.ID, title, err)
			continue
		}
		_ = warning

		// 标记旧订阅已迁移
		sub.Status = "migrated"
		sub.Enabled = false
		models.SaveCloudSubscription(sub)

		log.Printf("[discovery] RE0订阅 #%d（%s）已迁移到新引擎（新 ID %d）", sub.ID, title, sub2.ID)
		migrated++
	}
	if migrated > 0 {
		log.Printf("[discovery] RE0 订阅迁移完成：%d 条已迁移到 tgto123 对齐引擎", migrated)
	}
	return nil
}

// marshalMetadata 序列化 metadata map
func marshalMetadata(m map[string]any) string {
	if m == nil {
		return "{}"
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

// timeNowPtrDup 时间指针
func timeNowPtrDup() *time.Time { t := time.Now(); return &t }

func boolPtr(b bool) *bool { return &b }
