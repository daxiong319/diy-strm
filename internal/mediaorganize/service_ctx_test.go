package mediaorganize

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"litepan/internal/domain"
)

// ctxProbeRepo 在 Get 上记录收到的上下文，用于断言「前置校验用请求 ctx、规划阶段用后台 ctx」。
type ctxProbeRepo struct {
	mu    sync.Mutex
	tasks map[string]*domain.MediaOrganizeTask
	getCh chan context.Context
}

func newCtxProbeRepo(tasks map[string]*domain.MediaOrganizeTask) *ctxProbeRepo {
	return &ctxProbeRepo{tasks: tasks, getCh: make(chan context.Context, 4)}
}

func (r *ctxProbeRepo) Create(_ context.Context, task *domain.MediaOrganizeTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *task
	r.tasks[task.ID] = &copy
	return nil
}

func (r *ctxProbeRepo) Update(_ context.Context, task *domain.MediaOrganizeTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tasks[task.ID]; ok {
		copy := *task
		r.tasks[task.ID] = &copy
	}
	return nil
}

func (r *ctxProbeRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, id)
	return nil
}

func (r *ctxProbeRepo) Get(ctx context.Context, id string) (*domain.MediaOrganizeTask, error) {
	r.mu.Lock()
	task, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok {
		return nil, domain.Errorf(domain.CodeNotFound, "任务不存在")
	}
	select {
	case r.getCh <- ctx:
	default:
	}
	copy := *task
	return &copy, nil
}

func (r *ctxProbeRepo) List(context.Context) ([]*domain.MediaOrganizeTask, error) {
	return nil, nil
}

func (r *ctxProbeRepo) ListByAccount(context.Context, int64) ([]*domain.MediaOrganizeTask, error) {
	return nil, nil
}

// ctxProbePlanner 抓住规划阶段收到的上下文，等测试放行后再返回。
type ctxProbePlanner struct {
	started chan context.Context
	release chan struct{}
	result  func(PlannerHooks) error
}

func (p *ctxProbePlanner) Build(ctx context.Context, taskID string, _ *domain.MediaOrganizeTask, _ map[string]any, _ map[string]any, hooks PlannerHooks) (*Plan, error) {
	p.started <- ctx
	<-p.release
	if p.result != nil {
		if err := p.result(hooks); err != nil {
			return nil, err
		}
	}
	return &Plan{TaskID: taskID, Diagnostics: map[string]any{}}, nil
}

func ctxProbeTask(taskID string) *domain.MediaOrganizeTask {
	return &domain.MediaOrganizeTask{
		ID:        taskID,
		AccountID: 1,
		Config:    json.RawMessage(`{"action_type":"rename","rename_marker":"off"}`),
		Status:    domain.MediaOrganizeStatusIdle,
	}
}

// 规划阶段必须脱离 HTTP 请求上下文：请求 ctx 被取消后规划仍要能跑完。
// 这是 T01 的核心回归用例 —— 修复前 r.Context() 会一路传到 Build，浏览器刷新即中断规划。
func TestPlanTaskSurvivesRequestContextCancel(t *testing.T) {
	const taskID = "ctx-detach"
	repo := newCtxProbeRepo(map[string]*domain.MediaOrganizeTask{taskID: ctxProbeTask(taskID)})
	planner := &ctxProbePlanner{started: make(chan context.Context, 1), release: make(chan struct{})}
	svc := NewService(ServiceOptions{Repo: repo, DataDir: t.TempDir(), Planner: planner})

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	type planOutcome struct {
		result map[string]any
		err    error
	}
	done := make(chan planOutcome, 1)
	go func() {
		res, err := svc.PlanTask(reqCtx, taskID)
		done <- planOutcome{res, err}
	}()

	// 前置校验拿到的仍然是请求 ctx。
	var preflightCtx context.Context
	select {
	case preflightCtx = <-repo.getCh:
	case <-time.After(5 * time.Second):
		t.Fatal("前置校验未执行")
	}

	var planCtx context.Context
	select {
	case planCtx = <-planner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("规划阶段未启动")
	}

	// 模拟浏览器刷新 / 客户端断连。
	cancelReq()
	waitCtxDone(t, preflightCtx, "前置校验 ctx 应随请求取消而结束")
	waitCtxAlive(t, planCtx, "规划阶段 ctx 不应随请求取消而结束")

	close(planner.release)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("请求 ctx 取消后规划仍应成功，实际报错: %v", out.err)
		}
		if out.result["plan"] == nil {
			t.Fatalf("响应缺少 plan: %#v", out.result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("规划未在请求 ctx 取消后收敛")
	}

	// 计划落库（落盘），状态回落到 idle，不能卡在 planning。
	if plan, err := svc.loadPlan(taskID); err != nil || plan == nil {
		t.Fatalf("请求 ctx 取消后计划未保存: %v", err)
	}
	task, err := repo.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Status != domain.MediaOrganizeStatusIdle {
		t.Fatalf("任务状态应回落 idle，实际 %q", task.Status)
	}
	if svc.IsRunning(taskID) {
		t.Fatal("规划结束后仍处于运行中")
	}
}

// 停止按钮是权威通道：脱钩之后仍要能停，且要能通过 ctx 取消兜住只认 ctx 的路径。
func TestPlanTaskStopStillWorksAfterDetach(t *testing.T) {
	const taskID = "ctx-detach-stop"
	repo := newCtxProbeRepo(map[string]*domain.MediaOrganizeTask{taskID: ctxProbeTask(taskID)})

	planCtxCh := make(chan context.Context, 1)
	planner := &ctxProbePlanner{started: make(chan context.Context, 1), release: make(chan struct{})}
	planner.result = func(hooks PlannerHooks) error {
		// 模拟只认 ctx 取消的规划循环：先等停止信号，再确认 ctx 真的被取消了。
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := hooks.CheckStop(); err != nil {
				plan := <-planCtxCh
				select {
				case <-plan.Done():
					return err
				case <-time.After(3 * time.Second):
					t.Error("停止后兜底 ctx 未被取消")
					return err
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		return errors.New("等待停止信号超时")
	}
	svc := NewService(ServiceOptions{Repo: repo, DataDir: t.TempDir(), Planner: planner})

	done := make(chan error, 1)
	go func() {
		_, err := svc.PlanTask(context.Background(), taskID)
		done <- err
	}()

	var planCtx context.Context
	select {
	case planCtx = <-planner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("规划阶段未启动")
	}
	planCtxCh <- planCtx
	close(planner.release)

	svc.RequestStop(taskID)

	select {
	case err := <-done:
		if !errors.Is(err, ErrTaskAborted) {
			t.Fatalf("停止后应返回 ErrTaskAborted，实际 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("停止请求后规划未退出")
	}

	if svc.IsRunning(taskID) {
		t.Fatal("停止后仍处于运行中")
	}
}

// detachRunContext 的两条契约：保留 ctx 取值（trace / 日志字段），且不被调用方取消传播。
func TestDetachRunContextKeepsValuesAndIgnoresParentCancel(t *testing.T) {
	type ctxKey string
	const key ctxKey = "trace"

	svc := NewService(ServiceOptions{DataDir: t.TempDir()})
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key, "abc123"))

	ctx, cancel := svc.detachRunContext(parent, "detach-values")
	defer cancel()

	if got := ctx.Value(key); got != "abc123" {
		t.Fatalf("上下文取值丢失: %v", got)
	}

	cancelParent()
	waitCtxAlive(t, ctx, "调用方 ctx 取消不应传播到脱离后的 ctx")

	// 停止信号命中后兜底取消 ctx。
	svc.RequestStop("detach-values")
	waitCtxDone(t, ctx, "命中停止信号后兜底 ctx 应被取消")
}

func waitCtxDone(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitCtxAlive(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("%s（ctx.Err=%v）", msg, ctx.Err())
	case <-time.After(300 * time.Millisecond):
	}
}
