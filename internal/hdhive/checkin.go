package hdhive

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// CheckinMode 签到模式
type CheckinMode string

const (
	CheckinModeDaily  CheckinMode = "daily"  // 普通签到
	CheckinModeGamble CheckinMode = "gamble" // 赌狗签到
)

// CheckinResult 签到结果
type CheckinResult struct {
	Success  bool
	Message  string
	Mode     CheckinMode
}

// ResolveCheckinMode 解析签到模式
// 优先级：checkinMode 参数 > env ENV_HDHIVE_CHECKIN_TYPE > 默认普通
func ResolveCheckinMode(checkinMode string) CheckinMode {
	// 先看参数
	if checkinMode != "" {
		cm := strings.TrimSpace(strings.ToLower(checkinMode))
		gambleSet := map[string]bool{
			"1": true, "true": true, "yes": true, "on": true,
			"gamble": true, "gambler": true, "赌狗": true,
		}
		dailySet := map[string]bool{
			"0": true, "false": true, "no": true, "off": true,
			"daily": true, "normal": true, "普通": true,
		}
		if gambleSet[cm] {
			return CheckinModeGamble
		}
		if dailySet[cm] {
			return CheckinModeDaily
		}
	}

	// 再看环境变量 ENV_HDHIVE_CHECKIN_TYPE
	envMode := strings.TrimSpace(strings.ToLower(os.Getenv("ENV_HDHIVE_CHECKIN_TYPE")))
	switch envMode {
	case "1", "true", "yes", "on", "gamble", "gambler":
		return CheckinModeGamble
	}
	return CheckinModeDaily
}

// MeUserInfo /api/me 返回的用户信息（关键字段）
type MeUserInfo struct {
	Username                string   `json:"username"` // symedia 通道 status 返回
	Nickname                string   `json:"nickname"`
	AvatarURL               string   `json:"avatar_url"`
	Points                  float64  `json:"points"`
	Level                   string   `json:"level"`
	IsForeverVIP            bool     `json:"is_forever_vip"`
	CheckedInToday          bool     `json:"checked_in_today"`
	CheckinDaysTotal        int      `json:"checkin_days_total"`
	SigninDaysTotal         int      `json:"signin_days_total"`
	WeeklyFreeQuota         float64  `json:"weekly_free_quota"`
	WeeklyFreeQuotaRemaining float64 `json:"weekly_free_quota_remaining"`
	WeeklyFreeQuotaUnlimited bool    `json:"weekly_free_quota_unlimited"`
	WeeklyBonusQuota        float64  `json:"weekly_bonus_quota"`
	BonusQuota              float64  `json:"bonus_quota"`
	ShareNum                int      `json:"share_num"`
	IsUnlocked              bool     `json:"is_unlocked"`
}

// FormatUserSnapshot 格式化用户快照为多行文本
func FormatUserSnapshot(user *MeUserInfo) string {
	if user == nil {
		return ""
	}
	displayName := user.Nickname
	if displayName == "" {
		displayName = user.Username
	}
	lines := []string{
		fmt.Sprintf("账号: %s", displayName),
		fmt.Sprintf("等级: %s", formatLevel(user)),
		fmt.Sprintf("积分: %s", formatNumber(user.Points, "")),
		fmt.Sprintf("签到: %s，累计 %s", formatCheckin(user), formatNumber(float64(user.CheckinDaysTotal), "天")),
		fmt.Sprintf("额度: 周免费 %s，奖励 %s", formatWeeklyQuota(user), formatNumber(user.BonusQuota, "")),
		fmt.Sprintf("分享数: %s", formatNumber(float64(user.ShareNum), "")),
	}
	return strings.Join(lines, "\n")
}

// formatLevel 格式化等级（终身VIP 显示小写）
func formatLevel(user *MeUserInfo) string {
	if user.IsForeverVIP {
		return strings.ToLower(user.Level)
	}
	return user.Level
}

// formatCheckin 格式化签到状态
func formatCheckin(user *MeUserInfo) string {
	if user.CheckedInToday {
		return "已签到"
	}
	return "未签到"
}

// formatNumber 格式化数字（整数去小数位）
func formatNumber(value float64, suffix string) string {
	if value == math.Trunc(value) {
		return fmt.Sprintf("%.0f%s", value, suffix)
	}
	return fmt.Sprintf("%g%s", value, suffix)
}

// formatWeeklyQuota 格式化周额度
func formatWeeklyQuota(user *MeUserInfo) string {
	if user.WeeklyFreeQuotaUnlimited {
		return "不限"
	}
	if user.WeeklyFreeQuota > 0 {
		return fmt.Sprintf("%.0f/%.0f", user.WeeklyFreeQuotaRemaining, user.WeeklyFreeQuota)
	}
	return "-"
}

// IsAlreadyChecked 判断签到消息是否表示已签到
func IsAlreadyChecked(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	keywords := []string{"已签到", "今天已签到", "今日已签到", "already checked in"}
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// IsCheckinSuccess 判断签到是否成功（code=200 或 已签到）
func IsCheckinSuccess(code string, checkedInToday *bool, message string) bool {
	if code == "200" {
		return true
	}
	if checkedInToday != nil && !*checkedInToday {
		return false
	}
	return IsAlreadyChecked(message)
}