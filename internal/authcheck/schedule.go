package authcheck

import (
	"time"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 授权检测调度（复刻 LitePan internal/auth 设计）
// ---------------------------------------------------------------------------

const (
	// activeFailedThreshold 连续失败达到此次数标记持续失效
	activeFailedThreshold = 5
	// failedRetryCooldown 持续失效后的重试冷却
	failedRetryCooldown = 24 * time.Hour
)

// steppedCooldown 失败阶梯冷却（对齐 LitePan SteppedCooldown）
func steppedCooldown(attempts int) time.Duration {
	steps := []struct {
		attempt int
		cd      time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 5 * time.Minute},
		{4, 30 * time.Minute},
	}
	for _, step := range steps {
		if attempts <= step.attempt {
			return step.cd
		}
	}
	return steps[len(steps)-1].cd
}

// authInterval 返回指定网盘类型的检测间隔（对齐 LitePan 各驱动 Config）
func authInterval(sourceType models.SourceType) time.Duration {
	switch sourceType {
	case models.SourceTypeCloud189:
		// LitePan 189Cloud: TokenLifetime=7d, RefreshAdvance=24h
		return 7*24*time.Hour - 24*time.Hour
	case models.SourceTypePan139:
		// LitePan 139Cloud: TokenLifetime=30d, RefreshAdvance=10h
		return 30*24*time.Hour - 10*time.Hour
	case models.SourceTypeQuark:
		// LitePan Quark: HealthCheckInterval=70min（Cookie 型）
		return 70 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// isAuthRejection 判断错误是否为明确的认证拒绝（401/403 类）
func isAuthRejection(detail string) bool {
	for _, kw := range []string{
		"401", "403",
		"InvalidSessionKey", "InvalidAccessToken", "UserInvalidOpenToken",
		"认证会话已失效", "登录凭证", "授权已失效", "未授权",
		"AUTH_ERROR",
	} {
		if stringsContains(detail, kw) {
			return true
		}
	}
	return false
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexOfStr(s, substr) >= 0))
}

func indexOfStr(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
