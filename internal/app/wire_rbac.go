package app

import (
	"context"
	"database/sql"
	"log/slog"

	"litepan/internal/adminauth"
	"litepan/internal/logx"
	"litepan/internal/rbac"
)

// wireRBAC 构造用户与权限服务（参考实现 移植⑧）。
//
// 刻意不放在 wireServices 里而放在这里：RBAC 的播种（EnsureSeed）要在
// 迁移跑完之后、写库句柄可用之后才做，与洗版同一条纪律。
//
// 与洗版不同的是 RBAC 只用 database/sql（rbac.Store 的选择理由写在
// store.go 的文件头），所以直接用 store.DB 的读写句柄，不碰 GORM。
//
// 返回 nil 时接口层一律按「功能未启用」处理：RBAC 关着本来就是默认状态，
// 没有任何理由让一个没装上的功能变成 500。
func wireRBAC(st *storeBundle, logs *logx.Manager) *rbac.Service {
	if st == nil || st.settings == nil || st.store == nil || st.store.DB == nil {
		return nil
	}
	var write, read *sql.DB
	if st.store.DB.WriteHandle() != nil {
		write = st.store.DB.WriteHandle()
	}
	if st.store.DB.ReadHandle() != nil {
		read = st.store.DB.ReadHandle()
	}
	if write == nil {
		// 只有只读句柄时也能跑：Store 会把读句柄同时当写句柄用，
		// 但播种需要写，所以这里宁可不给 —— 宁可不启用，也不要开一个
		// 看起来能管权限、实际写不进去的半成品。
		return nil
	}
	svc := rbac.NewService(
		rbac.NewStore(write, read),
		// 第二个实参必须包一层 SettingsWithRaw：`admin_username` 没登记在
		// settings 的 registry 里（它是不参与设置页的凭据项），
		// 直传 st.settings 会让超管身份判定永远读出空串。
		// 为什么去读写 而不是直接传 settings.Service，是因为 raw 配置源是
		// st.store.Configs，与 adminauth 读凭据同源，两边不会读到两个值。
		rbac.SettingsWithRaw(st.settings, st.store.Configs),
		rbacLogAdapter{log: logs.For(logx.ModuleAPI)},
	)
	// 播种失败只记日志不阻断启动：权限目录播种不出来时，
	// 超管身份仍然成立（它不查库），只是所有权限项都判为拒绝 ——
	// 失败要朝着「更严」的方向倒，不能朝着「更松」。
	if err := svc.EnsureSeed(context.Background()); err != nil {
		logs.For(logx.ModuleAPI).Warn("用户与权限字典播种失败", "err", err)
	}
	return svc
}

// rbacLogAdapter 把 slog 适配成 rbac.Logger。
//
// 单独一个类型而不是让 rbac 直接收 *slog.Logger：rbac 是纯业务包，
// 不该知道日志库长什么样。nil 日志时静默丢弃 —— 播种日志不该让启动失败。
type rbacLogAdapter struct {
	log *slog.Logger
}

func (a rbacLogAdapter) Info(msg string, args ...any) {
	if a.log != nil {
		a.log.Info(msg, args...)
	}
}

func (a rbacLogAdapter) Warn(msg string, args ...any) {
	if a.log != nil {
		a.log.Warn(msg, args...)
	}
}

// rbacExtraUserAuth 把 rbac.Service 适配成 adminauth.ExtraUserAuth。
//
// 这一层适配存在的原因很具体：rbac 不能 import adminauth（否则成环，
// 因为 adminauth 要注入它），所以两边各有一套 Session 结构。
// 转换只搬四个字段，不做逻辑。
type rbacExtraUserAuth struct {
	svc *rbac.Service
}

// Enabled 报告 RBAC 是否开启。关着的时候 adminauth 连问都不会问。
func (a rbacExtraUserAuth) Enabled(ctx context.Context) bool {
	return a.svc != nil && a.svc.Enabled(ctx)
}

// Authenticate 校验一个受管用户的用户名密码。
func (a rbacExtraUserAuth) Authenticate(ctx context.Context, username, password string) (adminauth.Session, bool, error) {
	if a.svc == nil {
		return adminauth.Session{}, false, nil
	}
	sess, ok, err := a.svc.Authenticate(ctx, username, password)
	if err != nil || !ok {
		return adminauth.Session{}, false, err
	}
	return adminauth.Session{
		// IsAdmin 必须为真：这个会话要能通过 requireAdmin 这层会话鉴权，
		// 否则委托用户登录成功了却连自己的身份接口都读不到。
		IsAdmin:  true,
		Username: sess.Username,
		UserID:   sess.UserID,
		IsSuper:  false,
	}, true, nil
}

// bindRBAC 把 RBAC 服务挂到管理员鉴权服务上。
//
// 必须在 adminauth.New 之后调用：SetExtraUserAuth 只是赋值一个可选依赖，
// 早一步调用会被后面那次 New 直接覆盖掉 —— 这就是「假接线」的另一个变体，
// 编译通过、单测全绿、生产上委托用户永远登不进来。
func bindRBAC(admin *adminauth.Service, svc *rbac.Service) {
	if admin == nil || svc == nil {
		return
	}
	admin.SetExtraUserAuth(rbacExtraUserAuth{svc: svc})
}
