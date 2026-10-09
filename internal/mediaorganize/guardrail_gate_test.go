package mediaorganize

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/guardrail"
)

// fakeBreaker 记录熔断器被调用的顺序，用来断言「闸门在副作用之前」。
type fakeBreaker struct {
	mu sync.Mutex

	allowErr    error
	beginResult bool
	finish      guardrail.Decision

	allowCalls  int
	beginCalls  int
	finishCalls int
	// plannerStarted 用来证明 startRunner 真的没被调用。
	plannerStarted int
}

func (b *fakeBreaker) Allow(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.allowCalls++
	return b.allowErr
}

func (b *fakeBreaker) BeginRun(context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.beginCalls++
	return b.beginResult
}

func (b *fakeBreaker) FinishRun(context.Context) guardrail.Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finishCalls++
	return b.finish
}

func (b *fakeBreaker) counts() (allow, begin, finish, planner int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowCalls, b.beginCalls, b.finishCalls, b.plannerStarted
}

// countingPlanner 记下自己被调用过，用来证明闸门拦下时规划器一次都没跑。
type countingPlanner struct {
	mu    sync.Mutex
	calls int
}

func (p *countingPlanner) Build(_ context.Context, taskID string, _ *domain.MediaOrganizeTask, _ map[string]any, _ map[string]any, _ PlannerHooks) (*Plan, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &Plan{TaskID: taskID, Diagnostics: map[string]any{}}, nil
}

func newGuardrailRepo(taskID string) *lifecycleTaskRepo {
	return &lifecycleTaskRepo{tasks: map[string]*domain.MediaOrganizeTask{
		taskID: {
			ID:        taskID,
			AccountID: 1,
			Config:    json.RawMessage(`{"action_type":"rename","rename_marker":"off"}`),
			Status:    domain.MediaOrganizeStatusIdle,
		},
	}}
}

// 验收 ④：暂停期内直接拒绝，且不排期补跑 —— planner 一次都没被调用。
func TestRunTaskSkippedWhileGuardrailPaused(t *testing.T) {
	const taskID = "guardrail-paused"
	brk := &fakeBreaker{
		allowErr: &guardrail.PauseError{Decision: guardrail.Decision{
			Paused:   true,
			Reason:   "调用次数超限",
			ResumeAt: fixedResumeTime(),
		}},
	}
	planner := &countingPlanner{}
	svc := NewService(ServiceOptions{
		Repo:     newGuardrailRepo(taskID),
		DataDir:  t.TempDir(),
		Planner:  planner,
		Breaker:  brk,
		Settings: nil,
	})

	_, err := svc.RunTask(context.Background(), taskID)
	if err == nil {
		t.Fatal("暂停期内 RunTask 应当被拒绝")
	}
	if !strings.Contains(err.Error(), "风控暂停中") {
		t.Fatalf("错误文案未说明是风控暂停: %v", err)
	}
	// 关键断言：闸门在 startRunner 之前 —— planner 不该被碰到。
	planner.mu.Lock()
	calls := planner.calls
	planner.mu.Unlock()
	if calls != 0 {
		t.Fatalf("暂停期内仍启动了规划，planner 被调用 %d 次", calls)
	}
	allow, begin, _, _ := brk.counts()
	if allow != 1 || begin != 0 {
		t.Fatalf("熔断器调用序不对: allow=%d begin=%d", allow, begin)
	}
	// 任务状态回到 idle，不留「执行中」的假象。
	if svc.IsRunning(taskID) {
		t.Fatal("被风控拦下的任务不应处于运行态")
	}
	// 任务日志里要能看到跳过原因，否则用户只看到「没反应」。
	if msgs := logText(svc.GetLogs(taskID)); !strings.Contains(msgs, "风控暂停中") {
		t.Fatalf("任务日志缺少跳过原因: %#v", msgs)
	}
}

// ApplyTask 走的是另一条入口，必须同样被闸门挡住。
func TestApplyTaskSkippedWhileGuardrailPaused(t *testing.T) {
	const taskID = "guardrail-apply-paused"
	brk := &fakeBreaker{allowErr: guardrail.ErrPaused}
	svc := NewService(ServiceOptions{
		Repo:    newGuardrailRepo(taskID),
		DataDir: t.TempDir(),
		Breaker: brk,
	})
	if err := svc.savePlan(taskID, &Plan{TaskID: taskID}); err != nil {
		t.Fatalf("保存计划失败: %v", err)
	}

	_, err := svc.ApplyTask(context.Background(), taskID)
	if err == nil {
		t.Fatal("暂停期内 ApplyTask 应当被拒绝")
	}
	allow, begin, _, _ := brk.counts()
	if allow != 1 || begin != 0 {
		t.Fatalf("熔断器调用序不对: allow=%d begin=%d", allow, begin)
	}
}

// 熔断器没注入时整理功能照常可用 —— 熔断是护栏，不是依赖。
func TestRunTaskWorksWithoutGuardrail(t *testing.T) {
	const taskID = "guardrail-absent"
	planner := &countingPlanner{}
	svc := NewService(ServiceOptions{
		Repo:    newGuardrailRepo(taskID),
		DataDir: t.TempDir(),
		Planner: planner,
	})
	if _, err := svc.RunTask(context.Background(), taskID); err != nil {
		t.Fatalf("未注入熔断器时 RunTask 应成功: %v", err)
	}
}

// BeginRun 返回 false 时同样不得启动执行 —— 这是比 Allow 更靠后的一道闸。
func TestRunTaskSkippedWhenBeginRunRefused(t *testing.T) {
	const taskID = "guardrail-begin-refused"
	brk := &fakeBreaker{beginResult: false}
	planner := &countingPlanner{}
	svc := NewService(ServiceOptions{
		Repo:    newGuardrailRepo(taskID),
		DataDir: t.TempDir(),
		Planner: planner,
		Breaker: brk,
	})
	if _, err := svc.RunTask(context.Background(), taskID); err == nil {
		t.Fatal("BeginRun 拒绝时 RunTask 应失败")
	}
	planner.mu.Lock()
	calls := planner.calls
	planner.mu.Unlock()
	if calls != 0 {
		t.Fatalf("BeginRun 拒绝后仍启动了规划，planner 被调用 %d 次", calls)
	}
}

// 收尾必须结算时长：漏掉 FinishRun 会让「连续整理超限」永远不触发。
func TestRunTaskFinishesGuardrailRun(t *testing.T) {
	const taskID = "guardrail-finish"
	brk := &fakeBreaker{beginResult: true, finish: guardrail.Decision{
		Triggered: true,
		Paused:    true,
		Reason:    "整理时长超限",
	}}
	svc := NewService(ServiceOptions{
		Repo:    newGuardrailRepo(taskID),
		DataDir: t.TempDir(),
		Planner: &countingPlanner{},
		Breaker: brk,
	})
	if _, err := svc.RunTask(context.Background(), taskID); err != nil {
		t.Fatalf("RunTask 应提交成功: %v", err)
	}
	waitRunDone(t, svc, taskID)
	_, _, finish, _ := brk.counts()
	if finish == 0 {
		t.Fatal("一轮结束后没有调用 FinishRun，整理时长永远不会超限")
	}
}

func logText(entries []LogEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func fixedResumeTime() time.Time {
	return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
}

// waitRunDone 轮询到这一轮结束。RunTask 是异步提交的，返回时 goroutine 还在跑，
// 直接断言 FinishRun 会偶发失败 —— 那正是这类测试最常见的假阴性来源。
func waitRunDone(t *testing.T, svc *Service, taskID string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if !svc.IsRunning(taskID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("任务 %s 在 5 秒内没有结束", taskID)
}
