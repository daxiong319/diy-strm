package app

import (
	"database/sql"

	"litepan/internal/logx"
	"litepan/internal/medialibshare"
)

// libraryShareBundle 免登录分享装配结果（T10）。
type libraryShareBundle struct {
	svc *medialibshare.Service
}

// Enabled 分享功能这次是否真的装上了（服务在位）。
//
// 注意它答的是「装配上了没有」，不是「开关开没开」——
// 后者由 settings 决定，判定在 medialibshare.Service.Enabled() 里，
// 且那里是 fail-closed（读不到配置按关处理）。
// 两者混成一句话，就会出现「装配好了所以一定开着」这种越权假设。
func (b *libraryShareBundle) Enabled() bool { return b != nil && b.svc != nil }

// maybeService 取服务，nil 接收者返回 nil。
func (b *libraryShareBundle) maybeService() *medialibshare.Service {
	if b == nil {
		return nil
	}
	return b.svc
}

// wireLibraryShare 构造免登录分享服务（参考实现 移植⑩）。
//
// 返回 nil 时接口层一个路由都不注册 ⇒ 访客页与管理端全部 404，
// 理由与 wireRBAC / wireRequestCenter 同一条：
// 没装上的功能没有任何理由变成 500，更不该变成一个「总是回 403 的假入口」。
func wireLibraryShare(st *storeBundle, logs *logx.Manager) *libraryShareBundle {
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
		// 分享是一道**对外开门**：开了就把文件访问权发到公网上。
		// 写不进去的半成品比不开危险得多 —— 它会让人以为限制（有效期、
		// 设备数）在生效，而实际上一条都记不下来。
		return nil
	}
	return &libraryShareBundle{
		svc: medialibshare.NewService(medialibshare.Params{
			Store: medialibshare.NewStore(write, read),
			// 直接传 settings.Service：medialibshare.Settings 是
			// String/Bool/Int 三个窄方法，settings.Service 本来就满足，
			// 而它自带 registry 默认值回落 —— 分享不该再维护第二份默认值。
			Cfg: st.settings,
			Log: shareLogAdapter{log: logs.For(logx.ModuleAPI)},
		}),
	}
}

// shareLogAdapter 把 slog 适配成 medialibshare.Logger。
//
// 沿用 logx.ModuleAPI 而不是新开模块：这个功能没有独立的排障维度，
// 「分享创建失败」和「设置保存失败」在运维看来是同一类问题。
type shareLogAdapter struct {
	log interface {
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
	}
}

func (a shareLogAdapter) Info(msg string, args ...any) {
	if a.log != nil {
		a.log.Info(msg, args...)
	}
}

func (a shareLogAdapter) Warn(msg string, args ...any) {
	if a.log != nil {
		a.log.Warn(msg, args...)
	}
}
