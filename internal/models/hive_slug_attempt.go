package models

import (
	"strings"
	"time"

	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
)

// ---------------------------------------------------------------------------
// 影巢候选资源失败历史（借鉴 mediavault 的 attempt 轮转与确定性失败惩罚）：
//   - 失败的 slug 记录尝试次数与最后错误，下一轮降权（排后）
//   - 命中确定性失败标记（分享失效/违规/删除等）→ 拉长惩罚期
//   - 尝试次数达上限的 slug 跳过，避免反复撞同一批问题资源浪费配额
//   - 成功后清除记录
// ---------------------------------------------------------------------------

// HiveSlugAttempt 影巢资源 slug 失败历史
type HiveSlugAttempt struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	Slug           string     `gorm:"size:64;uniqueIndex:idx_hive_slug_attempt" json:"slug"`
	TMDBID         int64      `gorm:"index" json:"tmdb_id"`
	Attempts       int        `json:"attempts"`        // 累计尝试次数
	LastError      string     `gorm:"size:500" json:"last_error"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	PermanentFail  bool       `gorm:"index" json:"permanent_fail"`  // 确定性失败（死链/违规等）
	PenalizedUntil *time.Time `json:"penalized_until"`              // 惩罚期截止：期间跳过
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TableName 表名
func (HiveSlugAttempt) TableName() string {
	return "hive_slug_attempts"
}

// 确定性失败标记（借鉴 mediavault _PENALIZED_PERMANENT_FAIL_DEAD_SHARE_MARKERS）：
// 命中即认为该资源短期内不会恢复，拉长惩罚期
var hivePermanentFailMarkers = []string{
	"已取消", "已过期", "已失效", "已删除", "违规", "不存在",
	"操作频繁", "访问频繁", "访问受限", "稍后再试", "验证码", "安全验证",
	"resource not found", "not found", "forbidden",
}

// 惩罚期时长
const (
	hivePenaltyPermanent = 7 * 24 * time.Hour // 确定性失败：7 天
	hivePenaltyTransient = 24 * time.Hour     // 普通失败：1 天
)

// GetHiveSlugAttempt 查询 slug 的失败历史
func GetHiveSlugAttempt(slug string) *HiveSlugAttempt {
	if slug == "" {
		return nil
	}
	var a HiveSlugAttempt
	if err := db.Db.Where("slug = ?", slug).First(&a).Error; err != nil {
		return nil
	}
	return &a
}

// HiveSlugUsable 判断 slug 当前是否可尝试（惩罚期外且未达尝试上限）
func HiveSlugUsable(a *HiveSlugAttempt, maxAttempts int) (bool, string) {
	if a == nil {
		return true, ""
	}
	now := time.Now()
	if a.PermanentFail && a.PenalizedUntil != nil && a.PenalizedUntil.After(now) {
		return false, "确定性失败（" + a.LastError + "）"
	}
	if !a.PermanentFail && a.PenalizedUntil != nil && a.PenalizedUntil.After(now) {
		return false, "近期失败（" + a.LastError + "）"
	}
	if maxAttempts > 0 && a.Attempts >= maxAttempts && a.PermanentFail {
		return false, "尝试次数已达上限"
	}
	return true, ""
}

// RecordHiveSlugFailure 记录一次失败：累计次数、判定确定性失败、设置惩罚期
func RecordHiveSlugFailure(slug string, tmdbID int64, errText string) {
	if strings.TrimSpace(slug) == "" {
		return
	}
	errText = strings.TrimSpace(errText)
	if len(errText) > 480 {
		errText = errText[:480]
	}
	lower := strings.ToLower(errText)
	permanent := false
	for _, marker := range hivePermanentFailMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			permanent = true
			break
		}
	}
	now := time.Now()
	a := GetHiveSlugAttempt(slug)
	if a == nil {
		a = &HiveSlugAttempt{Slug: slug, TMDBID: tmdbID}
	}
	a.Attempts++
	a.LastError = errText
	a.LastAttemptAt = &now
	a.TMDBID = tmdbID
	a.PermanentFail = a.PermanentFail || permanent
	penalty := hivePenaltyTransient
	if a.PermanentFail {
		penalty = hivePenaltyPermanent
	}
	until := now.Add(penalty)
	a.PenalizedUntil = &until
	if err := db.Db.Save(a).Error; err != nil {
		helpers.AppLogger.Errorf("保存影巢资源失败历史失败：%v", err)
	}
}

// ClearHiveSlugAttempt 成功后清除失败历史
func ClearHiveSlugAttempt(slug string) {
	if strings.TrimSpace(slug) == "" {
		return
	}
	_ = db.Db.Where("slug = ?", slug).Delete(&HiveSlugAttempt{}).Error
}
