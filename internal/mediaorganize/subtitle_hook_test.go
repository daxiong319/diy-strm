package mediaorganize

import (
	"context"
	"errors"
	"sync"
	"testing"

	"litepan/internal/domain"
)

// recordingSubtitleProcessor 记录整理流程触发了哪些视频的字幕处理。
type recordingSubtitleProcessor struct {
	mu    sync.Mutex
	calls []recordedSubtitleCall
	err   error
}

type recordedSubtitleCall struct {
	VideoPath string
	Title     string
	Year      int
	Season    int
	Episode   int
	MediaType string
	TmdbID    int64
}

func (p *recordingSubtitleProcessor) ProcessOrganizedVideo(
	_ context.Context,
	videoPath, title string,
	year, season, episode int,
	mediaType string,
	tmdbID int64,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, recordedSubtitleCall{
		VideoPath: videoPath, Title: title, Year: year, Season: season,
		Episode: episode, MediaType: mediaType, TmdbID: tmdbID,
	})
	return p.err
}

func (p *recordingSubtitleProcessor) snapshot() []recordedSubtitleCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedSubtitleCall(nil), p.calls...)
}

// stubPlanExecutor 返回预置计划，用于驱动 applyPlanRunner 走完整流程。
type stubPlanExecutor struct {
	plan *Plan
	err  error
}

func (e stubPlanExecutor) Apply(ctx context.Context, plan *Plan, taskID string, accountID int64, cfg map[string]any, settings map[string]any, hooks ExecutorHooks) error {
	if e.err != nil {
		return e.err
	}
	if e.plan != nil {
		*plan = *e.plan
	}
	return nil
}

func newSubtitleHookService(t *testing.T, proc SubtitleProcessor, exec ExecutorApplier) (*Service, *lifecycleTaskRepo, *domain.MediaOrganizeTask) {
	t.Helper()
	repo := &lifecycleTaskRepo{tasks: map[string]*domain.MediaOrganizeTask{}}
	task := &domain.MediaOrganizeTask{ID: "task-sub", TaskName: "庆余年", Status: domain.MediaOrganizeStatusIdle}
	if err := repo.Create(context.Background(), task); err != nil {
		t.Fatalf("准备任务失败：%v", err)
	}
	svc := NewService(ServiceOptions{
		Repo:     repo,
		DataDir:  t.TempDir(),
		Executor: exec,
		Subtitle: proc,
	})
	return svc, repo, task
}

// TestSubtitleHookOnlyFiresForDoneVideoActions 验证字幕钩子只在
// 「动作成功 + 目标是视频文件」时触发：
//   - failed/skipped 的动作用于没搬成功的文件，不该去找字幕；
//   - 非视频扩展名（.txt/.srt/目录名）不该触发。
func TestSubtitleHookOnlyFiresForDoneVideoActions(t *testing.T) {
	proc := &recordingSubtitleProcessor{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{ID: "a1", TargetName: "庆余年.S01E01.mkv", Status: "done", Metadata: map[string]any{
			"year": 2019, "season": 1, "episode": 1, "media_type": "tv", "tmdb_id": 12345,
		}},
		{ID: "a2", TargetName: "庆余年.S01E02.mkv", Status: "failed"},
		{ID: "a3", TargetName: "说明.txt", Status: "done"},
		{ID: "a4", TargetName: "庆余年.S01E03.mp4", Status: "skipped"},
		{ID: "a5", TargetName: "庆余年.S01E04.MKV", Status: "done", Metadata: map[string]any{
			"season": float64(1), "episode": "4", "tmdb_id": "12345",
		}},
	}}}
	svc, _, task := newSubtitleHookService(t, proc, exec)

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	calls := proc.snapshot()
	if len(calls) != 2 {
		t.Fatalf("期望只有 2 个成功视频触发字幕处理，实际 %d 个：%+v", len(calls), calls)
	}
	if calls[0].VideoPath != "庆余年.S01E01.mkv" {
		t.Errorf("第 1 个调用视频名不符：%q", calls[0].VideoPath)
	}
	if calls[0].Title != "庆余年" {
		t.Errorf("标题应取自任务名，实际 %q", calls[0].Title)
	}
	if calls[0].Year != 2019 || calls[0].Season != 1 || calls[0].Episode != 1 {
		t.Errorf("元数据解析错误：%+v", calls[0])
	}
	if calls[0].MediaType != "tv" || calls[0].TmdbID != 12345 {
		t.Errorf("媒体类型/TMDB ID 解析错误：%+v", calls[0])
	}
	// a5 用字符串/浮点两套类型混写元数据，验证 helper 的宽容解析。
	if calls[1].VideoPath != "庆余年.S01E04.MKV" {
		t.Errorf("第 2 个调用视频名不符：%q", calls[1].VideoPath)
	}
	if calls[1].Season != 1 || calls[1].Episode != 4 || calls[1].TmdbID != 12345 {
		t.Errorf("字符串/浮点元数据解析错误：%+v", calls[1])
	}
}

// TestSubtitleHookFailureDoesNotBreakOrganize 验证字幕处理失败不影响整理任务本身：
// 整理任务必须仍然是「完成」状态，且失败只记日志。
func TestSubtitleHookFailureDoesNotBreakOrganize(t *testing.T) {
	proc := &recordingSubtitleProcessor{err: errors.New("字幕源全部不可用")}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{ID: "a1", TargetName: "片子.mkv", Status: "done"},
	}}}
	svc, repo, task := newSubtitleHookService(t, proc, exec)

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if len(proc.snapshot()) != 1 {
		t.Fatalf("期望字幕处理器被调用 1 次")
	}
	stored, err := repo.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("读取任务失败：%v", err)
	}
	if stored.Status != domain.MediaOrganizeStatusIdle {
		t.Errorf("字幕失败不应改变整理任务状态，实际 %q", stored.Status)
	}
	if len(stored.LastRunResult) == 0 {
		t.Errorf("字幕失败不应阻止整理结果落库")
	}
}

// TestSubtitleHookNilProcessorIsSafe 验证未注入字幕处理器时整理流程完全不受影响。
func TestSubtitleHookNilProcessorIsSafe(t *testing.T) {
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{ID: "a1", TargetName: "片子.mkv", Status: "done"},
	}}}
	svc, repo, task := newSubtitleHookService(t, nil, exec)

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	stored, err := repo.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("读取任务失败：%v", err)
	}
	if stored.Status != domain.MediaOrganizeStatusIdle {
		t.Errorf("未注入字幕处理器时整理应正常完成，实际 %q", stored.Status)
	}
}

// TestIsVideoFileName 覆盖扩展名白名单边界（含大小写与无扩展名）。
func TestIsVideoFileName(t *testing.T) {
	cases := map[string]bool{
		"a.mkv": true, "a.MP4": true, "a.ts": true, "a.m2ts": true,
		"a.rmvb": true, "a.iso": true, "a.webm": true,
		"a.txt": false, "a.srt": false, "a.ass": false,
		"noext": false, "": false, "dir.name": false,
	}
	for name, want := range cases {
		if got := isVideoFileName(name); got != want {
			t.Errorf("isVideoFileName(%q) = %v，期望 %v", name, got, want)
		}
	}
}
