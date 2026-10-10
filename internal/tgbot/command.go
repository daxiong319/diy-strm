package tgbot

import (
	"context"
	"sort"
	"strings"
)

// Tier 是命令的权限档位。
type Tier int

const (
	// TierRead 只读，任何被放行的来源都能用。
	TierRead Tier = iota
	// TierWrite 会产生副作用（转存、建订阅、跑整理），仍受白名单保护。
	TierWrite
	// TierSuper 始终只有超管能用，与 T08 的 RBAC 语义一致。
	//
	// 「加订阅」和「离线下载」刻意放在最高档：订阅会持续拉资源、
	// 离线下载会往网盘里塞东西，两者都不是一次性的、无所谓的手滑。
	// Bot 是外部入口 —— 白名单里的人也不该顺手就能建订阅。
	TierSuper
)

func (t Tier) String() string {
	switch t {
	case TierWrite:
		return "write"
	case TierSuper:
		return "super"
	default:
		return "read"
	}
}

// Actor 是发起命令的人。Bot 不自己做用户体系，权限由外部传入。
type Actor struct {
	TGUserID int64
	TGChatID int64
	ChatType string
	Username string
	// IsSuperAdmin 来自站内 RBAC 的判定，由调用方注入。
	IsSuperAdmin bool
}

// CommandSpec 是一条命令的声明。
//
// Bot 层不重写业务逻辑，只做四件事：权限校验、参数解析、调现有 service、
// 结果格式化。所以一条命令的声明只有元数据和参数解析，没有业务体。
type CommandSpec struct {
	// Name 不带斜杠，全小写。
	Name string
	// Aliases 是额外的名字，`/help strm` 这类子命令也走这里。
	Aliases []string
	// Summary 是 /help 里的一行说明。
	Summary string
	// Usage 是完整用法，直接进 /help。
	Usage string
	// Tier 权限档位。
	Tier Tier
	// Supported 报告这条命令是否真的接了业务。
	//
	// **24 条命令全部有分支**，未接业务的那几条回「该命令暂未支持」而不是静默。
	// 命令名照搬是因为用户已经在用 muvyo：一个不认识的命令连提示都不给，
	// 用户只能去翻文档；回一句「暂未支持」至少告诉他「Bot 收到了，是这里没有」。
	Supported bool
	// Handler 处理命令。Supported 为 false 时可以为空。
	Handler func(s *Service, ctx context.Context, u Update, args []string) (string, error)
}

// Registry 是命令表。
type Registry struct {
	specs map[string]*CommandSpec
	names []string
}

// NewRegistry 建一张空命令表。
func NewRegistry() *Registry {
	return &Registry{specs: map[string]*CommandSpec{}}
}

// Register 登记一条命令。panic 而不是返回错误：命令表是编译期常量，
// 启动就炸比运行到某个用户敲了那条命令才发现没注册要好。
func (r *Registry) Register(spec *CommandSpec) {
	if spec.Name == "" {
		panic("tgbot: 命令必须有名字")
	}
	if _, dup := r.specs[spec.Name]; dup {
		panic("tgbot: 命令重复注册 " + spec.Name)
	}
	r.specs[spec.Name] = spec
	r.names = append(r.names, spec.Name)
	for _, a := range spec.Aliases {
		if a == "" {
			continue
		}
		if _, dup := r.specs[a]; dup {
			panic("tgbot: 命令别名重复注册 " + a)
		}
		r.specs[a] = spec
	}
}

// Lookup 查一条命令（含别名）。
func (r *Registry) Lookup(name string) (*CommandSpec, bool) {
	spec, ok := r.specs[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))]
	return spec, ok
}

// List 按名字排序返回全部命令，供 /help 渲染。
func (r *Registry) List() []*CommandSpec {
	out := make([]*CommandSpec, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.specs[n])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AllNames 返回排好序的全部命令名。
func (r *Registry) AllNames() []string {
	list := r.List()
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

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
