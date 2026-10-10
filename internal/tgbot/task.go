package tgbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AsyncResult 是异步任务完成后主动推给用户的结果。
type AsyncResult struct {
	TaskID  string
	Title   string
	Success bool
	Body    string
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

// taskStore 是内存任务表，按 task id 索引，并保留归属。
type taskStore struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func newTaskStore() *taskStore { return &taskStore{jobs: map[string]*Job{}} }

func (t *taskStore) add(job *Job) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.jobs[job.ID] = job
}

func (t *taskStore) get(id string) (*Job, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[id]
	return j, ok
}

func (t *taskStore) markCancelled(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[id]; ok {
		j.Cancelled = true
	}
}

func (t *taskStore) markDone(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if j, ok := t.jobs[id]; ok {
		j.Done = true
	}
}

// ownsBy 判断一个任务是不是这个 actor 发起的。
//
// 私聊按 user id，群聊按 chat id：群里 A 发的任务不该被群里 B 取消。
func ownsBy(job *Job, actor Actor) bool {
	if actor.TGChatID != 0 {
		return job.Actor.TGChatID == actor.TGChatID
	}
	return job.Actor.TGUserID == actor.TGUserID
}

// notSupportedReply 是未接业务的命令的统一回话。
//
// 刻意不说「未知命令」：命令名是照搬 muvyo 的全集，用户敲 /douban 得到「暂未支持」
// 是「这个功能 litepan 还没有」；得到「未知命令」他会以为是 Bot 没收到。
func notSupportedReply(name string, summary string) string {
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
