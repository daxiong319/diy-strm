package app

import (
	"time"

	"litepan/internal/crosstransfer"
	"litepan/internal/eventbus"
	"litepan/internal/file"
	"litepan/internal/logx"
	"litepan/internal/playbackfallback"
	"litepan/internal/playpath"
	"litepan/internal/settings"
)

// wirePlayPath 构造播放路径映射服务并按当前设置刷新一次。
//
// 装配期必须 Sync 一次：配置里的规则在 DB 里，而规则表在内存里，
// 不刷新的话服务会以「无规则」启动，而症状是「规则明明配了却不生效」——
// 一个看不出是自己没装配的失败。
func wirePlayPath(st *storeBundle, logs *logx.Manager) *playpath.Service {
	svc := playpath.New(nil, time.Now)
	svc.Sync(playpath.Config{
		Enabled: st.settings.Bool(settings.KeyMOPlayPathMappingEnabled),
		Rules:   st.settings.StringAllowEmpty(settings.KeyMOPathMappingRules),
	})
	// 保留 muvyo 侧可检索的文本「Mount path replaced」：
	// 出问题时除了看页面上的命中统计，日志是第二处能说真话的地方。
	svc.SetLogger(func(source, from, to string) {
		logs.For(logx.ModuleSystem).Debug("Mount path replaced",
			"source", source, "from", from, "to", to)
	})
	return svc
}

// wireCrossAccount 构造跨账户播放转移服务。
//
// 「不可用起点」只在进程内观察，所以这里还要订阅事件总线：
// 只靠播放路径现查的话，一个被后台任务反复打失败的账号，
// 等到用户真的来播时它仍然是「第一次失败」，永远触发不了转存。
func wireCrossAccount(st *storeBundle, files *file.Service, transfer *crosstransfer.Service, bus *eventbus.Bus, logs *logx.Manager) *playbackfallback.Service {
	log := logs.For(logx.ModuleSystem)
	svc := playbackfallback.New(playbackfallback.Options{
		Config:   crossAccountConfig(st),
		Transfer: transfer,
		Files:    playbackfallback.FilesFromService(files),
		Accounts: st.store.Accounts,
		Health:   playbackfallback.HealthFromStore(st.store.AuthStates),
		Log:      log,
		Now:      time.Now,
	})
	playbackfallback.ObserveBus(bus, svc, log)
	return svc
}

// crossAccountConfig 读出跨账户转存的两个开关。
// 关闭时 FallbackWait 仍按设置解析，这样页面能把「等待时长」原样显示出来。
func crossAccountConfig(st *storeBundle) playbackfallback.Config {
	minutes := st.settings.Int(settings.KeyMOCrossAccountFallbackMinutes)
	if minutes <= 0 {
		minutes = 30
	}
	return playbackfallback.Config{
		Enabled:      st.settings.Bool(settings.KeyMOCrossAccountPlaybackEnabled),
		FallbackWait: time.Duration(minutes) * time.Minute,
	}
}
