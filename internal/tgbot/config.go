package tgbot

import (
	"strconv"
	"strings"

	"litepan/internal/settings"
)

// Config 是 Bot 的运行配置，全部来自 settings。
type Config struct {
	// Enabled 总开关。关掉时 webhook 直接 404，不是静默不响应 ——
	// 用户需要能区分「Bot 没开」和「Telegram 没送到」。
	Enabled bool
	// Token 是 Bot 凭证，来源设置，**只写不可读**（settings 里是 Sensitive）。
	Token string
	// AllowedUsers 是允许使用的 TG user id 白名单，逗号分隔。
	AllowedUsers []int64
	// AllowedChats 是允许使用 Bot 的群 id 白名单，逗号分隔。
	AllowedChats []int64
	// GroupLinkEnabled 控制群里的裸链接/磁力是否触发转存。
	GroupLinkEnabled bool
}

// ConfigFromSettings 读出 Bot 配置。
func ConfigFromSettings(s *settings.Service) Config {
	if s == nil {
		return Config{}
	}
	return Config{
		Enabled:          s.Bool(settings.KeyMOTelegramBotEnabled),
		Token:            strings.TrimSpace(s.StringAllowEmpty(settings.KeyMOTelegramBotToken)),
		AllowedUsers:     ParseIDList(s.StringAllowEmpty(settings.KeyMOTelegramBotAllowedUsers)),
		AllowedChats:     ParseIDList(s.StringAllowEmpty(settings.KeyMOTelegramBotAllowedChats)),
		GroupLinkEnabled: s.Bool(settings.KeyMOTelegramBotGroupLinkEnabled),
	}
}

// Ready 报告这份配置能不能真正接收消息。
//
// 「开了开关但没填白名单」是最危险的配置：任何人都能用你的网盘账号。
// 这里把空白名单当作**拒绝一切**，而不是当作「不限制」。
func (c Config) Ready() bool {
	return c.Enabled && c.Token != "" && (len(c.AllowedUsers) > 0 || len(c.AllowedChats) > 0)
}

// ParseIDList 解析逗号分隔的 ID 列表。
//
// 群 id 是负数（超群常见的 -100… 前缀），所以这里按 int64 解析而不是 uint。
// 解析不了的项静默跳过：配置框里粘进了一个逗号，不该让整个 Bot 起不来，
// 但静默跳过也不能让用户以为它生效了 —— 解析失败的项在设置页读回时会原样保留，
// 用户能看到自己填的东西没被消费。
func ParseIDList(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	seen := make(map[int64]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// FormatIDList 把 ID 列表拼回设置值的写法。
func FormatIDList(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
