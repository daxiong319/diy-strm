package tgbot

import (
	"litepan/internal/inboundbot"
)

// Telegram 侧不再自己定义这些类型 —— 它们全部住在 internal/inboundbot
// （命令表、权限档位、十个业务 handler 与它们依赖的窄接口）。
//
// 这里只留别名，是为了让装配层和测试继续写 `tgbot.Actor`：
// 换一个平台时 handler 一行不改，而包名差异不该顺着类型名渗进所有调用点。
type (
	// Tier 是命令的权限档位。
	Tier = inboundbot.Tier
	// Actor 是发起命令的人。
	Actor = inboundbot.Actor
	// CommandSpec 是一条命令的声明。
	CommandSpec = inboundbot.CommandSpec
	// Registry 是命令表。
	Registry = inboundbot.Registry
	// Job 是挂在 Bot 上的一条任务记录。
	Job = inboundbot.Job

	// Searcher 是 /search 需要的资源搜索能力。
	Searcher = inboundbot.Searcher
	// SearchHit 是一条搜索结果。
	SearchHit = inboundbot.SearchHit
	// TaskStore 是 Bot 挂在内存里的任务表（供 /cancel 查归属）。
	TaskStore = inboundbot.TaskStore
	// StatusLine 是一行状态。
	StatusLine = inboundbot.StatusLine
	// LinkTransferer 是转存能力。
	LinkTransferer = inboundbot.LinkTransferer
	// StatusProvider 提供 /status 需要的系统状态。
	StatusProvider = inboundbot.StatusProvider
	// DuplicateChecker 提供 /duplicate。
	DuplicateChecker = inboundbot.DuplicateChecker
	// Recognizer 提供 /recognize。
	Recognizer = inboundbot.Recognizer
	// CategoryLister 提供 /strm 的分类参数补全与合法性校验。
	CategoryLister = inboundbot.CategoryLister
	// StrmRunner 是 /strm 的执行能力。
	StrmRunner = inboundbot.StrmRunner
	// Subscriber 是 /sub 的执行能力。
	Subscriber = inboundbot.Subscriber
	// Runner 是「起一个异步任务」的统一入口。
	Runner = inboundbot.Runner
	// RunnerFunc 让普通函数直接成为 Runner。
	RunnerFunc = inboundbot.RunnerFunc
)

const (
	// TierRead 只读。
	TierRead = inboundbot.TierRead
	// TierWrite 会产生副作用。
	TierWrite = inboundbot.TierWrite
	// TierSuper 始终只有超管能用。
	TierSuper = inboundbot.TierSuper
)

// NewRegistry 建一张空命令表。
func NewRegistry() *Registry { return inboundbot.NewRegistry() }

// NewTaskStore 建一张空的 Bot 任务表。
func NewTaskStore() *TaskStore { return inboundbot.NewTaskStore() }

// ---- 常用文案转发到共用层 ----
//
// 这些话术是「Bot 对人说话」的一部分，不是 Telegram 特有的，
// 放在共用层里改一次两边都生效；只在这里包一层是为了让 tgbot 内部读起来自洽。

// errorReply 把错误翻成人话。
func errorReply(err error) string { return inboundbot.ErrorReply(err) }

// unknownCommandReply 是没注册的命令的回话。
func unknownCommandReply(name string) string { return inboundbot.UnknownCommandReply(name) }

// forbiddenReply 是权限不足的回话。
func forbiddenReply(name string) string { return inboundbot.ForbiddenReply(name) }

// notSupportedReply 是未接业务的命令的统一回话。
func notSupportedReply(name, summary string) string {
	return inboundbot.NotSupportedReply(name, summary)
}
