package tgbot

import (
	"litepan/internal/inboundbot"
)

// registerAll 登记命令全集（22 条：共用 21 条 + Telegram 私有的 /setprivacy）。
//
// 21 条直接来自 inboundbot.SharedCommandSpecs()，元数据与实现都在共用层；
// 企微侧挂的是同一份。/setprivacy 是 Telegram 独有的（BotFather 的隐私模式，
// 企微没有对应概念），所以只在 Telegram 侧加。
func registerAll(r *Registry) {
	for _, spec := range inboundbot.SharedCommandSpecs() {
		r.Register(spec)
	}
	r.Register(&CommandSpec{
		Name: "setprivacy", Tier: TierSuper, Supported: true,
		Summary: "隐私模式说明（Bot 无法自行关闭）",
		Usage:   "/setprivacy —— 打印开启/关闭隐私模式的操作步骤",
		Handler: handleSetPrivacy,
	})
}
