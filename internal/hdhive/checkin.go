package hdhive

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
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
	Success bool
	Message string
	Mode    CheckinMode
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
	Username                 string  `json:"username"` // symedia 通道 status 返回
	Nickname                 string  `json:"nickname"`
	AvatarURL                string  `json:"avatar_url"`
	Points                   float64 `json:"points"`
	Level                    string  `json:"level"`
	IsForeverVIP             bool    `json:"is_forever_vip"`
	CheckedInToday           bool    `json:"checked_in_today"`
	CheckinDaysTotal         int     `json:"checkin_days_total"`
	SigninDaysTotal          int     `json:"signin_days_total"`
	WeeklyFreeQuota          float64 `json:"weekly_free_quota"`
	WeeklyFreeQuotaRemaining float64 `json:"weekly_free_quota_remaining"`
	WeeklyFreeQuotaUnlimited bool    `json:"weekly_free_quota_unlimited"`
	WeeklyBonusQuota         float64 `json:"weekly_bonus_quota"`
	BonusQuota               float64 `json:"bonus_quota"`
	ShareNum                 int     `json:"share_num"`
	IsUnlocked               bool    `json:"is_unlocked"`
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
// 关键词覆盖：diy-strm 原有关键词 + NanShare 的「已经签到/签到过/明天再来」
func IsAlreadyChecked(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	keywords := []string{
		"已签到", "今天已签到", "今日已签到",
		"已经签到", "签到过", "明天再来",
		"already checked in",
	}
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

// ---------------------------------------------------------------------------
// 签到结果富解析（借鉴 NanShare _find_checkin_award_points 的递归策略）
// ---------------------------------------------------------------------------

// checkinAwardPointKeys 常见的签到奖励积分字段名（按优先级匹配）
var checkinAwardPointKeys = []string{
	"gain_points", "point_delta", "sign_points", "award_points",
	"bonus_points", "delta_points", "points_delta", "reward_point",
	"earned_points", "gained_points", "points_gained", "reward_points",
	"signin_points", "checkin_points", "points_awarded",
}

// checkinAccountContextKeys 出现这些键说明当前 dict 是账号资料而非签到奖励，
// 其中的 points 是余额而非本次奖励，不应误判
var checkinAccountContextKeys = map[string]bool{
	"level": true, "nickname": true, "username": true, "share_num": true,
	"avatar_url": true, "oauth_status": true, "is_forever_vip": true,
	"checked_in_today": true, "signin_days_total": true,
	"weekly_free_quota": true, "weekly_free_quota_remaining": true,
}

// checkinSkipKeys 递归查找时跳过的子节点键（用户对象里的积分是余额，不是奖励）
var checkinSkipKeys = map[string]bool{
	"user": true, "account": true, "profile": true,
	"oauth_user": true, "weekly_quota": true,
}

// coerceCheckinPoints 将任意 JSON 值转换为整数积分（支持负数，赌狗可能扣分）
func coerceCheckinPoints(value any) *int {
	switch v := value.(type) {
	case bool:
		return nil
	case float64:
		if v == math.Trunc(v) {
			n := int(v)
			return &n
		}
		return nil
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		neg := false
		if strings.HasPrefix(text, "-") {
			neg = true
			text = text[1:]
		} else if strings.HasPrefix(text, "+") {
			text = text[1:]
		}
		if text == "" {
			return nil
		}
		for _, r := range text {
			if r < '0' || r > '9' {
				return nil
			}
		}
		n := 0
		for _, r := range text {
			n = n*10 + int(r-'0')
		}
		if neg {
			n = -n
		}
		return &n
	case json.Number:
		if n, err := strconvInt(v.String()); err == nil {
			return &n
		}
		return nil
	default:
		return nil
	}
}

// strconvInt 解析整数字符串（避免引入 strconv 别名冲突的包装）
func strconvInt(s string) (int, error) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid digit %q", r)
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

// findCheckinAwardPoints 递归在任意 JSON 结构中查找本次签到奖励积分。
// 匹配优先级：专用奖励字段名 > 非账号资料上下文中的通用 points 键。
func findCheckinAwardPoints(value any) *int {
	switch typed := value.(type) {
	case map[string]any:
		lowerKeys := make(map[string]string, len(typed))
		for k := range typed {
			lowerKeys[strings.ToLower(k)] = k
		}
		for _, key := range checkinAwardPointKeys {
			original, ok := lowerKeys[key]
			if !ok {
				continue
			}
			if pts := coerceCheckinPoints(typed[original]); pts != nil {
				return pts
			}
		}
		if original, ok := lowerKeys["points"]; ok && !hasAnyKey(lowerKeys, checkinAccountContextKeys) {
			if pts := coerceCheckinPoints(typed[original]); pts != nil {
				return pts
			}
		}
		for k, item := range typed {
			if checkinSkipKeys[strings.ToLower(k)] {
				continue
			}
			if pts := findCheckinAwardPoints(item); pts != nil {
				return pts
			}
		}
	case []any:
		for _, item := range typed {
			if pts := findCheckinAwardPoints(item); pts != nil {
				return pts
			}
		}
	}
	return nil
}

// hasAnyKey 判断 lowerKeys 的键集合与给定上下文键集是否存在交集
func hasAnyKey(lowerKeys map[string]string, contextKeys map[string]bool) bool {
	for k := range contextKeys {
		if _, ok := lowerKeys[k]; ok {
			return true
		}
	}
	return false
}

// CheckinStats 签到结果富信息
type CheckinStats struct {
	AwardPoints   *int // 本次获得/消耗积分（赌狗可能为负）
	BalancePoints *int // 账户余额
	StreakDays    *int // 连续签到天数
}

// ExtractCheckinStats 从签到响应 data 中提取奖励积分、余额、连签天数。
// data 为接口返回的 JSON（已 unmarshal 为 map/slice 结构）。
func ExtractCheckinStats(data any) *CheckinStats {
	stats := &CheckinStats{}
	obj, ok := data.(map[string]any)
	if !ok {
		// data 可能是数组或标量，整体交给奖励查找
		stats.AwardPoints = findCheckinAwardPoints(data)
		return stats
	}
	stats.AwardPoints = findCheckinAwardPoints(obj)
	balanceKeys := []string{"points_total", "total_points", "balance", "points_balance", "user_points"}
	lowerKeys := make(map[string]string, len(obj))
	for k := range obj {
		lowerKeys[strings.ToLower(k)] = k
	}
	for _, key := range balanceKeys {
		if original, ok := lowerKeys[key]; ok {
			if v := coerceCheckinPoints(obj[original]); v != nil {
				stats.BalancePoints = v
				break
			}
		}
	}
	streakKeys := []string{"continuous_days", "streak", "streak_days", "consecutive_days", "checkin_streak"}
	for _, key := range streakKeys {
		if original, ok := lowerKeys[key]; ok {
			if v := coerceCheckinPoints(obj[original]); v != nil && *v >= 0 {
				stats.StreakDays = v
				break
			}
		}
	}
	return stats
}

// FormatStatsSuffix 将统计信息格式化为消息后缀：「，获得 N 积分（余额 M）已连签 D 天」
func (s *CheckinStats) FormatStatsSuffix() string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.AwardPoints != nil {
		parts = append(parts, fmt.Sprintf("获得 %d 积分", *s.AwardPoints))
	}
	if s.BalancePoints != nil {
		parts = append(parts, fmt.Sprintf("余额 %s", formatNumber(float64(*s.BalancePoints), "")))
	}
	if s.StreakDays != nil {
		parts = append(parts, fmt.Sprintf("已连签 %d 天", *s.StreakDays))
	}
	if len(parts) == 0 {
		return ""
	}
	return "，" + strings.Join(parts, "，")
}

// ---------------------------------------------------------------------------
// 随机分钟定时签到（借鉴 NanShare _hdhive_random_minute 的确定性哈希策略）
// ---------------------------------------------------------------------------

// RandomCheckinMinute 计算某账号在某日期的目标签到分钟数（0-29）。
// 同一账号同一天结果稳定：sha256(id:date) 前 8 字节取模 30，
// 即每天稳定落在目标小时的前 30 分钟内的某一分钟，重启不漂移、无法被预测整点批量请求特征。
func RandomCheckinMinute(id uint, date string) int {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", id, date)))
	return int(binary.BigEndian.Uint64(h[:8]) % 30)
}

// RandomCheckinMinuteInRange 计算某账号在某日期落在 [minMinute, maxMinute]（一天内分钟数）的确定性随机分钟。
// 借鉴 RandomCheckinMinute 的哈希策略：sha256(id:date) 前 8 字节映射到窗口长度内，
// 同账号同一天结果稳定，重启不漂移（S1 随机签到窗口核心）。
// 要求 0 <= minMinute <= maxMinute < 1440。
func RandomCheckinMinuteInRange(id uint, date string, minMinute, maxMinute int) int {
	if minMinute < 0 {
		minMinute = 0
	}
	if maxMinute > 1439 {
		maxMinute = 1439
	}
	if maxMinute <= minMinute {
		return minMinute
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", id, date)))
	span := uint64(maxMinute-minMinute+1) % 1440
	off := int(binary.BigEndian.Uint64(h[:8]) % span)
	return minMinute + off
}
