package tgbot

import "strings"

// parsed 是一条被切分过的消息。
type parsed struct {
	Command string
	Args    []string
	// Bare 是命令名后面的原始文本（未经分词），保留给 `/download rss <链接>` 这类。
	Bare string
	// Mentions 是 @ 机器人用户名，`/strm@mybot 参数` 在群里是合法写法。
	Mention string
}

// parseText 切分一条命令文本。
//
// @提及的处理必须对：群里发 `/strm@MyBot 国产剧` 是 Telegram 的标准写法，
// 不剥掉 @ 后缀的话这条命令会落到名为 "strm@mybot" 的分支上，
// 然后用户得到「该命令暂未支持」—— 一个看起来像没实现、其实是没解析对的结果。
func parseText(text string) (parsed, bool) {
	text = strings.TrimSpace(text)
	if len(text) < 2 || text[0] != '/' {
		return parsed{}, false
	}
	rest := text[1:]
	// 命令名只到第一个空白为止：`/search abc` 的命令名是 search，「abc」是参数。
	// 整串当成命令名的话，每一条带参数的命令都会落到「不认识的命令」，
	// 而 Telegram 的提示并不会告诉用户它解析成了什么。
	name := rest
	if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
		name = rest[:i]
	}
	rest = rest[len(name):]
	mention := ""
	if i := strings.IndexByte(name, '@'); i >= 0 {
		mention = name[i+1:]
		name = name[:i]
	}
	if name == "" {
		return parsed{}, false
	}
	// Telegram 的命令实参以紧跟的 @ 提及结尾；空提及（`/strm@`）当作没有提及。
	if strings.TrimSpace(mention) == "" {
		mention = ""
	}
	bare := strings.TrimSpace(rest)
	return parsed{Command: name, Bare: bare, Args: strings.Fields(bare), Mention: mention}, true
}

// subcommand 取出第一个参数作为子命令。
func (p parsed) subcommand() string {
	if len(p.Args) == 0 {
		return ""
	}
	return strings.ToLower(p.Args[0])
}

// rest 去掉子命令后的剩余参数。
func (p parsed) rest() []string {
	if len(p.Args) <= 1 {
		return nil
	}
	return p.Args[1:]
}
