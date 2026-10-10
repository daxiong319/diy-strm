// Package inboundbot 是入站 Bot 的平台无关内核。
//
// ## 为什么抽这一层
//
// T18 的 Telegram Bot 里，只有「命令怎么解析、权限怎么分档、准入怎么判、
// 十个业务 handler 怎么调现有 service」这几件事与平台无关；
// 其余（Update 的形状、webhook 怎么鉴权、回复怎么发）都是 Telegram 特有的。
//
// 抽出内核之后，企微机器人要写的是「消息怎么进来 / Actor 怎么构造 / 怎么回复」
// 三件事，而十个业务 handler 与命令表元数据一字不改。
//
// 复制 handler 的代价是永久的：将来修 /strm 的分类校验要改两处，
// 而两处里漏改的那一处只有用户报障时才会被发现。
//
// ## 刻意没有抽进来的东西
//
//   - Service：它同时知道 Telegram 的 Update 和 HTTP 端点，抽它等于抽一个
//     「Telegram 味的 HTTP 服务」，另一个平台一个字段都用不上。
//   - webhook 鉴权：Telegram 用 X-Telegram-Bot-Api-Secret-Token，
//     企微智能机器人是 msg_signature / 长连接，没有对应物。
package inboundbot

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
//
// 字段刻意不带平台前缀：Actor 是两个平台共享的类型，
// 名字里带 TG 只会让人以为它在 TG 之外不能用。
type Actor struct {
	// UserID 是发起人。企微里可能是加密的 open_userid，由平台侧负责换算。
	UserID int64
	// ChatID 是会话（群）。单聊里它等于 UserID。
	ChatID int64
	// ChatType 是平台的会话类型串（telegram 的 private/group/channel）。
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
	// **命令全集全部有分支**，未接业务的那几条回「该命令暂未支持」而不是静默。
	// 命令名照搬是因为用户已经在用 muvyo：一个不认识的命令连提示都不给，
	// 用户只能去翻文档；回一句「暂未支持」至少告诉他「Bot 收到了，是这里没有」。
	Supported bool
	// Handler 处理命令。Supported 为 false 时可以为空。
	Handler func(d *Deps, ctx context.Context, actor Actor, args []string) (string, error)
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
		panic("inboundbot: 命令必须有名字")
	}
	if _, dup := r.specs[spec.Name]; dup {
		panic("inboundbot: 命令重复注册 " + spec.Name)
	}
	r.specs[spec.Name] = spec
	r.names = append(r.names, spec.Name)
	for _, a := range spec.Aliases {
		if a == "" {
			continue
		}
		if _, dup := r.specs[a]; dup {
			panic("inboundbot: 命令别名重复注册 " + a)
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

// CheckTier 判定权限档位。
//
// 超管档始终只有超管：Bot 里没有「谁创建了这个 Bot」的概念，
// 所以权限只能由站内 RBAC 注入，而不是由平台的身份推断。
//
// 「拿不到权限信息」不等于「是超管」—— 所以 IsSuperAdmin 没有默认值，
// 平台侧查不到时必须显式传 false。
func CheckTier(t Tier, actor Actor) bool {
	if t == TierSuper {
		return actor.IsSuperAdmin
	}
	return true
}
