package app

import (
	"context"
	"time"

	"litepan/internal/playmonitor"
	"litepan/internal/rbac"
)

// playMonitorUserAdapter 把 RBAC 用户解析接到播放监控上。
//
// 放在装配层的理由与 playReportAdapter 相同：playmonitor 只需要一个
// 「按用户 ID 查显示名」的小口子，不需要知道 rbac 的存在。
type playMonitorUserAdapter struct {
	svc *rbac.Service
}

// DisplayName 实现 playmonitor.UserResolver。
//
// 显示名为空时回落到登录名：排行榜上出现"用户#12"对用户毫无信息量，
// 而登录名至少能让人认出这是谁。两处都空才返回 false，
// 那时排行退到 playmonitor 自己的兜底名。已停用的用户不解析 ——
// 他的历史记录仍在报告里（统计不该因为人被停用就消失），
// 只是不给他出现在实时会话的名字。
func (a playMonitorUserAdapter) DisplayName(userID int64) (string, bool) {
	if a.svc == nil || userID <= 0 {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveUserTimeout)
	defer cancel()
	u, err := a.svc.GetUser(ctx, userID)
	if err != nil || !u.Enabled {
		return "", false
	}
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	if name == "" {
		return "", false
	}
	return name, true
}

var _ playmonitor.UserResolver = playMonitorUserAdapter{}

// resolveUserTimeout 解析用户名的单次超时。
//
// 显示名解析发生在取流/报告的旁路上，DB 卡住不该把播放拖住。
const resolveUserTimeout = 3 * time.Second
