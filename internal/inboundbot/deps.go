package inboundbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- 与现有 service 之间的全部接触面 ----
//
// 刻意不直接依赖 mediaorganize / discover / share 的具体 service：
// Bot 的职责只有「权限 + 参数解析 + 调现有能力 + 格式化」，
// 绑死具体实现会让那些包每次重构都要动 Bot，而这里只有几十行接口。

// Deps 是十个业务 handler 依赖的全部东西。
//
// 这就是抽取的枢纽：原来 handler 的签名吃的是 `*tgbot.Service` + `tgbot.Update`
// 两个 Telegram 实体，换成 Deps + Actor 之后就是纯数据了。
type Deps struct {
	// Registry 是命令表，只有 /help 与「不认识的命令」需要它。
	//
	// 它属于装配结果而不是全局单例：同一个进程里两个平台各挂一张表也没问题，
	// 而用全局变量的话，测试里换个平台就得先清理上一个。
	Registry *Registry
	// Title 是平台名字（「Telegram Bot」/「企业微信机器人」），只进 /help 抬头。
	//
	// 抽共用层之后不能继续硬编码，否则企微用户的 /help 会写着「Telegram Bot 命令」，
	// 用户会以为自己在跟两个不同的机器人说话。
	Title      string
	Runner     Runner
	Search     Searcher
	Link       LinkTransferer
	Status     StatusProvider
	Duplicate  DuplicateChecker
	Recognizer Recognizer
	Categories CategoryLister
	Strm       StrmRunner
	Subscriber Subscriber
	Tasks      *TaskStore
	Now        func() time.Time
}

func (d *Deps) now() time.Time {
	if d == nil || d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Searcher 提供 /search。
type Searcher interface {
	Search(ctx context.Context, keyword string, limit int) ([]SearchHit, error)
}

// SearchHit 是一条搜索结果。
type SearchHit struct {
	Title    string
	Source   string
	Size     string
	ShareURL string
}

// StatusProvider 提供 /status 需要的系统状态。
type StatusProvider interface {
	BotStatus(ctx context.Context) ([]StatusLine, error)
}

// StatusLine 是一行状态。
type StatusLine struct {
	Label string
	Value string
}

// DuplicateChecker 提供 /duplicate。
type DuplicateChecker interface {
	BotDuplicateReport(ctx context.Context, limit int) ([]string, error)
}

// Recognizer 提供 /recognize。
type Recognizer interface {
	BotRecognize(ctx context.Context, filename string) ([]string, error)
}

// CategoryLister 提供 /strm 的分类参数补全与合法性校验。
type CategoryLister interface {
	BotCategories(ctx context.Context) ([]string, error)
}

// StrmRunner 是 /strm 的执行能力。
type StrmRunner interface {
	BotRunStrm(ctx context.Context, actor Actor, category string) (string, error)
}

// Subscriber 是 /sub 的执行能力。
type Subscriber interface {
	BotAddSubscription(ctx context.Context, actor Actor, title string) (string, error)
}

// LinkTransferer 是「给一个分享链接/磁力，触发转存」的能力。
//
// Bot 不自己实现转存：仓内已有完整链路，这里只约定形状，保证换实现时 Bot 不用改。
type LinkTransferer interface {
	// StartLinkTransfer 发起转存，返回对外可展示的 task id。
	StartLinkTransfer(ctx context.Context, actor Actor, link string) (string, error)
	// CancelLinkTransfer 取消一个由 Bot 发起、尚未完成的转存。
	CancelLinkTransfer(ctx context.Context, actor Actor, taskID string) (bool, error)
}

// Runner 是「起一个异步任务」的统一入口。
//
// Bot 层不自己写业务：整理、离线下载、订阅刷新各自有自己的 service，
// 这里只约定一个形状，让 /cancel 能用同一套逻辑找到并取消 Bot 发起的任务。
type Runner interface {
	// Start 启动任务并返回对外可展示的 task id。
	Start(ctx context.Context, actor Actor, kind string, payload string) (string, error)
	// Cancel 取消一个由 Bot 发起的任务。
	// 返回 false 表示这个任务不由 Bot 发起、或已经结束 —— 这两种都要能被
	// /cancel 如实报告，不能当成成功。
	Cancel(ctx context.Context, actor Actor, taskID string) (bool, error)
}

// RunnerFunc 让普通函数直接成为 Runner。
type RunnerFunc struct {
	StartFn  func(ctx context.Context, actor Actor, kind string, payload string) (string, error)
	CancelFn func(ctx context.Context, actor Actor, taskID string) (bool, error)
}

func (f RunnerFunc) Start(ctx context.Context, actor Actor, kind string, payload string) (string, error) {
	if f.StartFn == nil {
		return "", fmt.Errorf("该任务类型暂未支持")
	}
	return f.StartFn(ctx, actor, kind, payload)
}

func (f RunnerFunc) Cancel(ctx context.Context, actor Actor, taskID string) (bool, error) {
	if f.CancelFn == nil {
		return false, fmt.Errorf("该任务类型暂未支持取消")
	}
	return f.CancelFn(ctx, actor, taskID)
}

// Job 是一条挂在 Bot 上的任务记录。
//
// /cancel 需要回答「这个 task id 是不是你的、能不能撤」。
// 只存内存：Bot 重启后这些任务本来也管不了了（真正的取消由各自 service 的
// 持久化状态负责），落库只会留下一堆永远撤不掉的僵尸记录。
type Job struct {
	ID        string
	Kind      string
	Actor     Actor
	CreatedAt time.Time
	Cancelled bool
	Done      bool
}

// TaskStore 是内存任务表，按 task id 索引，并保留归属。
type TaskStore struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewTaskStore() *TaskStore { return &TaskStore{jobs: map[string]*Job{}} }

// Add 记一条由 Bot 发起的任务。
func (t *TaskStore) Add(job *Job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.jobs[job.ID] = job
}

func (t *TaskStore) Get(id string) (*Job, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	return j, ok
}

func (t *TaskStore) MarkCancelled(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[id]; ok {
		j.Cancelled = true
	}
}

func (t *TaskStore) MarkDone(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[id]; ok {
		j.Done = true
	}
}

// OwnsBy 判断一个任务是不是这个 actor 发起的。
//
// 私聊按 user id，群聊按 chat id：群里 A 发的任务不该被群里 B 取消。
func OwnsBy(job *Job, actor Actor) bool {
	if job == nil {
		return false
	}
	if actor.ChatID != 0 {
		return job.Actor.ChatID == actor.ChatID
	}
	return job.Actor.UserID == actor.UserID
}

// ---- 格式化与文案 ----

// ErrorReply 把错误转成一句用户能看懂的话。
//
// 「暂未支持」的原文原样透出：那句话是给人看的状态说明，
// 再套一层「执行失败」会变成「这个功能执行失败了」，方向完全反了。
func ErrorReply(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "暂未支持") {
		return err.Error()
	}
	return "执行失败：" + err.Error()
}

// UnknownCommandReply 是「不认识的命令」。
func UnknownCommandReply(name string) string {
	return "不认识「/" + strings.TrimSpace(name) + "」这条命令。\n发 /help 看完整列表。"
}

// ForbiddenReply 是「权限不够」。
//
// 「权限不足」和「不认识的命令」必须分开说：后者意味着 Bot 收到了但没这条命令，
// 前者意味着命令存在但这个人不能用 —— 混成一句话会让人以为是前者。
func ForbiddenReply(name string) string {
	return "「/" + name + "」只有管理员可以使用。\n你的权限由站内角色决定，这里不做独立配置。"
}

// NotSupportedReply 是未接业务的命令的统一回话。
//
// 刻意不说「未知命令」：命令名是照搬 muvyo 的全集，用户敲 /douban 得到「暂未支持」
// 是「这个功能 litepan 还没有」；得到「未知命令」他会以为是 Bot 没收到。
func NotSupportedReply(name string, summary string) string {
	var b strings.Builder
	b.WriteString("「/")
	b.WriteString(name)
	b.WriteString("」暂未支持。")
	if summary != "" {
		b.WriteString("\n规划中的作用：")
		b.WriteString(summary)
	}
	b.WriteString("\n已可用的命令：/help 查看完整列表。")
	return b.String()
}

// RenderCategoryList 渲染可用分类。
func RenderCategoryList(list []string) string {
	if len(list) == 0 {
		return "（还没有配置任何分类）"
	}
	trimmed := list
	if len(trimmed) > 30 {
		trimmed = trimmed[:30]
	}
	out := "可用分类：" + strings.Join(trimmed, "、")
	if len(list) > len(trimmed) {
		out += fmt.Sprintf(" 等 %d 个", len(list))
	}
	return out
}

// ContainsFold 大小写无关的包含判断。
func ContainsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// Itoa 是小工具，避免各 handler 都 import strconv。
func Itoa(v int64) string { return fmt.Sprintf("%d", v) }
