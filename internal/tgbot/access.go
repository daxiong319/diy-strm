package tgbot

// AccessDecision 是一次入站消息的准入裁决，以及拒绝时的说法。
type AccessDecision struct {
	Allowed bool
	// Reason 是拒绝原因，会原样回给发消息的人。
	// Bot 是外部入口：被拒时必须说清是「不是你」还是「得先开开关」，
	// 「静默忽略」会让用户以为 Bot 坏了，而真坏了的时候他也是这么想的。
	Reason string
	// Anonymous 标记「这条不该被回应」。
	//
	// 群里的裸链接来自白名单外的人：回一句「你没权限」等于把这个 Bot 的存在、
	// 它的名字和它的能力告诉了群里每一个人。不回应是对的。
	Anonymous bool
}

// Allow 返回一个放行裁决。
func Allow() AccessDecision { return AccessDecision{Allowed: true} }

// Deny 返回一个会回话的拒绝裁决。
func Deny(reason string) AccessDecision { return AccessDecision{Allowed: false, Reason: reason} }

// DenyQuietly 返回一个静默拒绝：不回复，也不留下任何痕迹。
func DenyQuietly() AccessDecision { return AccessDecision{Allowed: false, Anonymous: true} }

// CheckAccess 判定一条消息能不能被处理。
//
// 权限模型是**显式白名单**（配置里列出的 TG user id / 群 id），不是用户绑定：
// 白名单是这个外部入口唯一能自证安全的机制 —— 绑定 litepan 用户需要先有账号体系，
// 而 Bot 的第一条消息就来自「还没有 litepan 用户」的人。
//
// 私聊与群聊分开判定：
//   - 私聊：发消息的人必须在 AllowedUsers 里。群里被 @ 到但本人不在白名单，同样按人判。
//   - 群聊：只有群本身在 AllowedChats 里才放行，且**群发命令不需要人也在白名单里**
//     —— 否则「把群加进白名单」这个动作毫无意义。
//   - 群里的裸链接/磁力额外要 GroupLinkEnabled：允许群发命令 ≠ 允许群里发链接就转存。
//     后者会让任何一个能发言的人动用你的网盘账号，而前者至少留了命令痕迹。
func CheckAccess(cfg Config, msg Message) AccessDecision {
	if !cfg.Enabled {
		return Deny("Bot 未启用，请先在管理台打开 Telegram Bot。")
	}
	if cfg.Token == "" {
		return Deny("Bot Token 未配置，请先在管理台填写。")
	}

	chat := msg.Chat
	from := msg.From
	// Bot 自己的消息和群里的匿名管理员消息没有 From.ID，一律按群判定。
	fromKnown := from.ID != 0 && !from.IsBot

	if chat.IsPrivate() {
		if !fromKnown {
			return Deny("无法识别发消息的人。")
		}
		if len(cfg.AllowedUsers) == 0 {
			return Deny("未配置允许使用的 TG 用户 ID 白名单，当前所有私聊都被拒绝。")
		}
		if containsID(cfg.AllowedUsers, from.ID) {
			return Allow()
		}
		return DenyQuietly()
	}

	// 群/频道：先看群是否被放行。
	if !containsID(cfg.AllowedChats, chat.ID) {
		return DenyQuietly()
	}

	// 群里的裸链接与磁力：独立的、更严格的开关。
	if !isCommandText(msg.body()) {
		if !cfg.GroupLinkEnabled {
			return DenyQuietly()
		}
		if !containsID(cfg.AllowedUsers, from.ID) && fromKnown {
			// 群在白名单里，但这个人不在：允许群里发命令，不允许他用你的账号转存。
			return DenyQuietly()
		}
	}
	return Allow()
}

// isCommandText 判断一条文本是不是命令（/ 开头）。
func isCommandText(text string) bool {
	return len(text) > 1 && text[0] == '/'
}

func containsID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// AllowedUserList 报告哪些 user id 被放行，供设置页回显说明用。
func (c Config) AllowedUserList() string {
	if len(c.AllowedUsers) == 0 {
		return "（空：当前没有任何用户被放行）"
	}
	return FormatIDList(c.AllowedUsers)
}

// AllowedChatList 报告哪些群被放行。
func (c Config) AllowedChatList() string {
	if len(c.AllowedChats) == 0 {
		return "（空：没有任何群被放行）"
	}
	return FormatIDList(c.AllowedChats)
}
