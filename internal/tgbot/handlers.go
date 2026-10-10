package tgbot

import (
	"context"

	"litepan/internal/inboundbot"
)

// handleSetPrivacy 说明如何在 BotFather 里关掉隐私模式。
//
// 这条命令存在的意义是「解释为什么群里发链接没反应」，
// 而不是假装 Bot 能关掉它 —— Bot API 里的隐私模式是在 BotFather 里改的，
// 没有对应的 Bot API 方法。回一个假的「已关闭」会让用户一直找不到原因。
//
// 剩下的十条命令住在 internal/inboundbot：它们与 Telegram 无关，
// 换成别的平台时一行都不用改。
func handleSetPrivacy(d *inboundbot.Deps, ctx context.Context, actor inboundbot.Actor, args []string) (string, error) {
	return "Bot 无法自行关闭隐私模式，需要在 BotFather 里操作：\n\n" +
			"1. 打开 @BotFather\n" +
			"2. /mybots → 选中你的 Bot\n" +
			"3. Bot Settings → Group Privacy → 选 Disable\n" +
			"4. 群里把 Bot 设为管理员（至少需要能读消息）\n\n" +
			"关闭后，群里直接发分享链接或磁力就会自动转存。\n" +
			"注意：默认配置下群里只有 / 命令、@提及和回复会送达 Bot —— 隐私模式开着时收不到普通消息，这是 Telegram 的机制，不是故障。",
		nil
}
