package mediaorganize

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"litepan/internal/core/driverexec"
	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/file"
	"litepan/internal/settings"
)

// ---- 测试替身 ----

// nfoConfigRepo 是最简 config 仓储，只为把 settings.Service 喂起来。
type nfoConfigRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *nfoConfigRepo) Get(_ context.Context, key string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	return v, ok, nil
}

func (r *nfoConfigRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *nfoConfigRepo) All(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out, nil
}

// recordingNFO 记录落盘器收到的每一次调用，用来验证触发器的去重与过滤。
type recordingNFO struct {
	mu      sync.Mutex
	works   []ScrapeWorkInput
	seasons []ScrapeSeasonInput
	eps     []ScrapeEpisodeInput
	workErr error
	epErr   error
}

func (r *recordingNFO) WriteWorkMetadata(_ context.Context, in ScrapeWorkInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workErr != nil {
		return r.workErr
	}
	r.works = append(r.works, in)
	return nil
}

func (r *recordingNFO) WriteSeasonMetadata(_ context.Context, in ScrapeSeasonInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seasons = append(r.seasons, in)
	return nil
}

func (r *recordingNFO) WriteEpisodeMetadata(_ context.Context, in ScrapeEpisodeInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.epErr != nil {
		return r.epErr
	}
	r.eps = append(r.eps, in)
	return nil
}

func (r *recordingNFO) snapshot() ([]ScrapeWorkInput, []ScrapeSeasonInput, []ScrapeEpisodeInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ScrapeWorkInput(nil), r.works...),
		append([]ScrapeSeasonInput(nil), r.seasons...),
		append([]ScrapeEpisodeInput(nil), r.eps...)
}

// stubTMDB 返回固定的作品详情，验证「元数据没带海报时回头补齐」。
type stubTMDB struct {
	calls []string
	raw   string
	err   error
}

func (s *stubTMDB) Lookup(_ context.Context, tmdbID, mediaType string) (json.RawMessage, error) {
	s.calls = append(s.calls, tmdbID+"/"+mediaType)
	if s.err != nil {
		return nil, s.err
	}
	return json.RawMessage(s.raw), nil
}

// nfoPathDriver 实现了 driver.FullListLister，把目录 ID 翻成路径。
type nfoPathDriver struct {
	paths map[string]string
}

func (d *nfoPathDriver) Config() driver.Config      { return driver.Config{Name: "nfo-path"} }
func (d *nfoPathDriver) GetAddition() any           { return &struct{}{} }
func (d *nfoPathDriver) Init(context.Context) error { return nil }
func (d *nfoPathDriver) Drop(context.Context) error { return nil }
func (d *nfoPathDriver) Ping(context.Context) error { return nil }
func (d *nfoPathDriver) ListFiles(context.Context, string) ([]domain.FileItem, error) {
	return nil, nil
}
func (d *nfoPathDriver) ListAllFiles(context.Context, string) ([]driver.FullListEntry, error) {
	return nil, nil
}
func (d *nfoPathDriver) ResolveDirPath(_ context.Context, dirID string) (string, error) {
	return d.paths[dirID], nil
}

type nfoPathProvider struct{ drv driver.Driver }

func (p nfoPathProvider) Get(context.Context, int64) (driver.Driver, error) { return p.drv, nil }

// newNFOHookService 搭一个跑得通 applyPlanRunner 的最小 Service。
func newNFOHookService(t *testing.T, cfg map[string]string, nfo ScrapeNFOWriter, exec ExecutorApplier, paths map[string]string) (*Service, *lifecycleTaskRepo, *domain.MediaOrganizeTask) {
	t.Helper()
	repo := &lifecycleTaskRepo{tasks: map[string]*domain.MediaOrganizeTask{}}
	task := &domain.MediaOrganizeTask{ID: "task-nfo", TaskName: "庆余年", Status: domain.MediaOrganizeStatusIdle}
	if err := repo.Create(context.Background(), task); err != nil {
		t.Fatalf("准备任务失败：%v", err)
	}
	if cfg == nil {
		cfg = map[string]string{}
	}
	settingsSvc, err := settings.New(context.Background(), &nfoConfigRepo{values: cfg})
	if err != nil {
		t.Fatalf("准备设置服务失败：%v", err)
	}
	var files *file.Service
	if paths != nil {
		files = file.NewService(driverexec.New(nfoPathProvider{drv: &nfoPathDriver{paths: paths}}, nil), nil, nil, nil, nil, nil)
	}
	svc := NewService(ServiceOptions{
		Repo:     repo,
		DataDir:  t.TempDir(),
		Executor: exec,
		NFO:      nfo,
		Files:    files,
		Settings: settingsSvc,
	})
	return svc, repo, task
}

func tvEpisodeAction(id, targetName, parentID, title string, season, episode int) PlanAction {
	return PlanAction{
		ID: id, TargetName: targetName, TargetParentID: parentID, Status: "done",
		Metadata: map[string]any{
			"title": title, "media_kind": "tv", "season": season, "episode": episode,
			"year": 2019, "tmdb_id": 123456,
		},
	}
}

// TestNFOTriggerWritesWorkSeasonAndEpisode 覆盖整理完成后的完整落盘：
// 一部剧两集 ⇒ 一份作品级、一份季级、两份集级。
func TestNFOTriggerWritesWorkSeasonAndEpisode(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "庆余年.S01E01.mkv", "season-dir", "庆余年", 1, 1),
		tvEpisodeAction("a2", "庆余年.S01E02.mkv", "season-dir", "庆余年", 1, 2),
	}}}
	svc, repo, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/庆余年/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, seasons, eps := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("两集应只落一份作品级元数据，实际 %d 份：%+v", len(works), works)
	}
	// tvshow.nfo 必须写在剧集根目录（季目录的上一层），不是季目录里。
	if works[0].Dir != filepath.Dir("/media/电视剧/庆余年/Season 01") {
		t.Fatalf("作品级落盘目录不对：%q", works[0].Dir)
	}
	if works[0].Title != "庆余年" || works[0].TMDBID != 123456 || works[0].MediaType != MediaTypeTV {
		t.Fatalf("作品级字段不对：%+v", works[0])
	}
	if len(seasons) != 1 || seasons[0].Season != 1 || seasons[0].Title != "庆余年" {
		t.Fatalf("季级应只落一份：%+v", seasons)
	}
	if len(eps) != 2 {
		t.Fatalf("两集应落两份集级元数据，实际 %d 份：%+v", len(eps), eps)
	}
	if eps[0].Season != 1 || eps[0].Episode != 1 || eps[1].Episode != 2 {
		t.Fatalf("集号不对：%+v", eps)
	}
	stored, err := repo.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.MediaOrganizeStatusIdle {
		t.Fatalf("落盘不该改变整理任务状态，实际 %q", stored.Status)
	}
	if len(stored.LastRunResult) == 0 {
		t.Fatal("整理结果应照常落库")
	}
}

// TestNFOTriggerDedupesByWorkDirTitleSeason covers 「同一部剧多集只写一次作品级」。
func TestNFOTriggerDedupesByWorkDirTitleSeason(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
		tvEpisodeAction("a2", "A.S01E02.mkv", "season-dir", "A", 1, 2),
		tvEpisodeAction("a3", "A.S01E02.中文版.mkv", "season-dir", "A", 1, 2),
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, _, eps := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("同一部剧的作品级应只写一次，实际 %d 次", len(works))
	}
	// 同一集出现两个版本（双字幕/多压制）时，集级也只写一次。
	if len(eps) != 2 {
		t.Fatalf("集级应去重成 2 份（E01/E02），实际 %d 份：%+v", len(eps), eps)
	}
}

// TestNFOTriggerSkipsNonDoneAndNonVideo covers 动作过滤：
// 没搬成功的文件、非视频文件、没有标题的动作都不该触发落盘。
func TestNFOTriggerSkipsNonDoneAndNonVideo(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
		{ID: "a2", TargetName: "A.S01E02.mkv", TargetParentID: "season-dir", Status: "failed",
			Metadata: map[string]any{"title": "A", "media_kind": "tv", "season": 1, "episode": 2}},
		{ID: "a3", TargetName: "A.S01E03.srt", TargetParentID: "season-dir", Status: "done",
			Metadata: map[string]any{"title": "A", "media_kind": "tv", "season": 1, "episode": 3}},
		// 「.mkv」剥掉扩展名后没有标题，用来验证无标题动作被跳过
		// （普通文件名会回落到文件名当标题，那是既定行为）。
		{ID: "a4", TargetName: ".mkv", TargetParentID: "season-dir", Status: "done",
			Metadata: map[string]any{"media_kind": "tv", "season": 1, "episode": 4, "title": "  "}},
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, _, eps := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("只有 done 的视频动作该触发，实际 %d 次作品级", len(works))
	}
	if len(eps) != 1 || eps[0].Episode != 1 {
		t.Fatalf("只该落 E01 的集级元数据，实际 %+v", eps)
	}
}

// TestNFOTriggerSkipsEpisodeWithoutNumber covers 「集号缺失就只写作品级」：
// 硬写一个 S01E00 会让媒体库里出现一个查无此集的条目。
func TestNFOTriggerSkipsEpisodeWithoutNumber(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{
			ID: "a1", TargetName: "A.S01.mkv", TargetParentID: "season-dir", Status: "done",
			Metadata: map[string]any{"title": "A", "media_kind": "tv", "season": 1, "year": 2019},
		},
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, seasons, eps := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("作品级仍应照写，实际 %d 份", len(works))
	}
	if len(seasons) != 1 {
		t.Fatalf("季级仍应照写，实际 %d 份", len(seasons))
	}
	if len(eps) != 0 {
		t.Fatalf("没有集号时不该硬造集级元数据，实际 %+v", eps)
	}
}

// TestNFOTriggerOffByDefault covers 开关默认关闭。
func TestNFOTriggerOffByDefault(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	svc, _, task := newNFOHookService(t, nil, nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if works, _, eps := nfo.snapshot(); len(works) != 0 || len(eps) != 0 {
		t.Fatalf("开关未开时不该落任何元数据，实际作品 %d 集 %d", len(works), len(eps))
	}
}

// TestNFOTriggerDisabledDoesNotBlockOrganize covers 显式关闭。
func TestNFOTriggerDisabledDoesNotBlockOrganize(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	svc, repo, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "false"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if works, _, _ := nfo.snapshot(); len(works) != 0 {
		t.Fatalf("关闭时不该落盘，实际 %d 份", len(works))
	}
	if stored, _ := repo.Get(context.Background(), task.ID); stored.Status != domain.MediaOrganizeStatusIdle {
		t.Fatalf("整理任务状态不该受影响，实际 %q", stored.Status)
	}
}

// TestNFOTriggerWithoutWriterIsSafe covers 没装配落盘器时整理流程完全不受影响。
func TestNFOTriggerWithoutWriterIsSafe(t *testing.T) {
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	svc, repo, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nil, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if stored, _ := repo.Get(context.Background(), task.ID); stored.Status != domain.MediaOrganizeStatusIdle {
		t.Fatalf("未装配落盘器时整理应正常完成，实际 %q", stored.Status)
	}
}

// TestNFOTriggerSkipsWhenDirCannotBeResolved covers 拿不到目标目录时静默跳过：
// 猜一个目录写 NFO 比不写更糟。
func TestNFOTriggerSkipsWhenDirCannotBeResolved(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	// paths 里没有 season-dir ⇒ ResolveDirPath 返回空串。
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if works, _, eps := nfo.snapshot(); len(works) != 0 || len(eps) != 0 {
		t.Fatalf("解析不到目录时不该落盘，实际作品 %d 集 %d", len(works), len(eps))
	}
}

// TestNFOTriggerWorkFailureDoesNotBlockEpisodesOrTask covers 单作品失败降级：
// 记日志、继续处理后续动作、任务本身照常完成。
func TestNFOTriggerWorkFailureDoesNotBlockEpisodesOrTask(t *testing.T) {
	nfo := &recordingNFO{workErr: errSinkBoom{}}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	svc, repo, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if stored, _ := repo.Get(context.Background(), task.ID); stored.Status != domain.MediaOrganizeStatusIdle {
		t.Fatalf("元数据落盘失败不该让整理任务失败，实际 %q", stored.Status)
	}
}

// TestNFOTriggerLooksUpTMDBWhenPosterMissing covers 海报补齐：
// 动作元数据没有 poster_path 时回头查一次 TMDB。
func TestNFOTriggerLooksUpTMDBWhenPosterMissing(t *testing.T) {
	nfo := &recordingNFO{}
	tmdb := &stubTMDB{raw: `{"title":"A","poster_path":"/p.jpg","backdrop_path":"/b.jpg","overview":"简介"}`}
	repo := &lifecycleTaskRepo{tasks: map[string]*domain.MediaOrganizeTask{}}
	task := &domain.MediaOrganizeTask{ID: "task-nfo-tmdb", TaskName: "A", Status: domain.MediaOrganizeStatusIdle}
	if err := repo.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	settingsSvc, err := settings.New(context.Background(), &nfoConfigRepo{values: map[string]string{
		settings.KeyMOScrapeNFOEnabled: "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "season-dir", "A", 1, 1),
	}}}
	svc := NewService(ServiceOptions{
		Repo:     repo,
		DataDir:  t.TempDir(),
		Executor: exec,
		NFO:      nfo,
		TMDBWork: tmdb,
		Settings: settingsSvc,
		Files: file.NewService(driverexec.New(nfoPathProvider{drv: &nfoPathDriver{paths: map[string]string{
			"season-dir": "/media/电视剧/A/Season 01",
		}}}, nil), nil, nil, nil, nil, nil),
	})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, _, _ := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("期望落一份作品级元数据，实际 %d", len(works))
	}
	if works[0].PosterURL != "/p.jpg" || works[0].FanartURL != "/b.jpg" || works[0].Plot != "简介" {
		t.Fatalf("海报/背景/简介没被补齐：%+v", works[0])
	}
	if len(tmdb.calls) != 1 || tmdb.calls[0] != "123456/tv" {
		t.Fatalf("应查一次 TMDB（id + mediaType），实际 %v", tmdb.calls)
	}
}

// TestNFOTriggerSkipsTMDBWhenPosterAlreadyKnown covers 整理时已经带上海报时
// 不再发第二次网络请求 —— 一次整理不该因为元数据落盘多打一轮 TMDB。
func TestNFOTriggerSkipsTMDBWhenPosterAlreadyKnown(t *testing.T) {
	nfo := &recordingNFO{}
	tmdb := &stubTMDB{raw: `{"poster_path":"/other.jpg"}`}
	repo := &lifecycleTaskRepo{tasks: map[string]*domain.MediaOrganizeTask{}}
	task := &domain.MediaOrganizeTask{ID: "task-nfo-noposter", TaskName: "A", Status: domain.MediaOrganizeStatusIdle}
	if err := repo.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	settingsSvc, err := settings.New(context.Background(), &nfoConfigRepo{values: map[string]string{
		settings.KeyMOScrapeNFOEnabled: "true",
	}})
	if err != nil {
		t.Fatal(err)
	}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{
			ID: "a1", TargetName: "A.S01E01.mkv", TargetParentID: "season-dir", Status: "done",
			Metadata: map[string]any{
				"title": "A", "media_kind": "tv", "season": 1, "episode": 1, "tmdb_id": 1,
				"poster_path": "/known.jpg",
			},
		},
	}}}
	svc := NewService(ServiceOptions{
		Repo: repo, DataDir: t.TempDir(), Executor: exec, NFO: nfo, TMDBWork: tmdb, Settings: settingsSvc,
		Files: file.NewService(driverexec.New(nfoPathProvider{drv: &nfoPathDriver{paths: map[string]string{
			"season-dir": "/media/电视剧/A/Season 01",
		}}}, nil), nil, nil, nil, nil, nil),
	})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	if len(tmdb.calls) != 0 {
		t.Fatalf("已有海报时不该再查 TMDB，实际 %v", tmdb.calls)
	}
	works, _, _ := nfo.snapshot()
	if len(works) != 1 || works[0].PosterURL != "/known.jpg" {
		t.Fatalf("海报没沿用动作元数据：%+v", works)
	}
}

// TestNFOTriggerWritesMovieWithoutSeason covers 电影：没有季号，
// 不该硬造一个 season1.nfo。
func TestNFOTriggerWritesMovieWithoutSeason(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{
			ID: "a1", TargetName: "流浪地球2.2023.2160p.mkv", TargetParentID: "movie-dir", Status: "done",
			Metadata: map[string]any{"title": "流浪地球2", "media_kind": "movie", "year": 2023, "tmdb_id": 76600},
		},
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"movie-dir": "/media/电影/流浪地球2"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, seasons, eps := nfo.snapshot()
	if len(works) != 1 || works[0].MediaType != MediaTypeMovie {
		t.Fatalf("应落一份电影作品级元数据：%+v", works)
	}
	// 电影的作品级目录就是动作所在目录，不该再往上退一层。
	if works[0].Dir != "/media/电影/流浪地球2" {
		t.Fatalf("电影作品级目录不该上移：%q", works[0].Dir)
	}
	if len(seasons) != 0 || len(eps) != 0 {
		t.Fatalf("电影不该落季级/集级元数据，实际季 %d 集 %d", len(seasons), len(eps))
	}
}

// TestNFOTriggerCloudTargetForwardsAccount covers 目标是网盘时把账号透给落盘器。
func TestNFOTriggerCloudTargetForwardsAccount(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		{
			ID: "a1", TargetName: "A.S01E01.mkv", TargetParentID: "season-dir", Status: "done",
			Metadata: map[string]any{"title": "A", "media_kind": "tv", "season": 1, "episode": 1, "tmdb_id": 5},
		},
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{
			settings.KeyMOScrapeNFOEnabled: "true",
			settings.KeyMOScrapeNFOTarget:  "cloud",
		},
		nfo, exec, map[string]string{"season-dir": "/media/电视剧/A/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, _, _ := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("应落一份作品级元数据，实际 %d", len(works))
	}
	if works[0].AccountID != 1 {
		t.Fatalf("网盘目标应透传任务账号（applyPlanRunner 传的就是 1），实际 %d", works[0].AccountID)
	}
}

// TestNFOSettingsLocalTargetKeepsAccountZero covers 目标是本地时不带账号：
// 带账号会让落盘器误判成网盘。
func TestNFOSettingsLocalTargetKeepsAccountZero(t *testing.T) {
	repo := &nfoConfigRepo{values: map[string]string{
		settings.KeyMOScrapeNFOEnabled: "true",
		settings.KeyMOScrapeNFOTarget:  "local",
	}}
	settingsSvc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{DataDir: t.TempDir(), Settings: settingsSvc})
	got := svc.loadNFOSettings(context.Background(), map[string]any{}, 42)
	if got.Enabled != true {
		t.Fatal("开关应生效")
	}
	if got.Cloud {
		t.Fatal("local 目标不该被判成网盘")
	}
	if got.AccountID != 0 {
		t.Fatalf("local 目标应清空账号，实际 %d", got.AccountID)
	}
}

// TestNFOSettingsIgnoresUnknownTarget covers 只认 cloud：
// 「网盘」这类手写值宁可当本地，也不要悄悄写到别的账号去。
func TestNFOSettingsIgnoresUnknownTarget(t *testing.T) {
	for _, target := range []string{"", " 网盘 ", "LOCAL", "quark"} {
		repo := &nfoConfigRepo{values: map[string]string{
			settings.KeyMOScrapeNFOEnabled: "true",
			settings.KeyMOScrapeNFOTarget:  target,
		}}
		settingsSvc, err := settings.New(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		svc := NewService(ServiceOptions{DataDir: t.TempDir(), Settings: settingsSvc})
		if got := svc.loadNFOSettings(context.Background(), map[string]any{}, 42); got.Cloud {
			t.Fatalf("target=%q 不该被当成 cloud", target)
		}
	}
}

// TestNFOSettingsRequiresService covers 没装配 settings 服务时按关闭处理，
// 而不是 panic —— 单测里大量 Service 就是这么构造的。
func TestNFOSettingsRequiresService(t *testing.T) {
	svc := NewService(ServiceOptions{DataDir: t.TempDir()})
	if got := svc.loadNFOSettings(context.Background(), map[string]any{}, 1); got.Enabled {
		t.Fatal("无 settings 服务时必须按关闭处理")
	}
}

// TestNFOSettingsUsesTaskTargetDirectory covers 网盘目标的父目录取自任务的
// 目标目录（target_directory_id），元数据与视频落同一片网盘区域。
func TestNFOSettingsUsesTaskTargetDirectory(t *testing.T) {
	repo := &nfoConfigRepo{values: map[string]string{
		settings.KeyMOScrapeNFOEnabled: "true",
		settings.KeyMOScrapeNFOTarget:  "cloud",
	}}
	settingsSvc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{DataDir: t.TempDir(), Settings: settingsSvc})
	got := svc.loadNFOSettings(context.Background(), map[string]any{
		"target_directory_id": "dir-42",
	}, 7)
	if got.ParentID != "dir-42" || got.AccountID != 7 || !got.Cloud {
		t.Fatalf("网盘目标应取任务目标目录与账号：%+v", got)
	}
	// 没填目标目录时账号仍要透传，落盘器自己决定写哪。
	bare := svc.loadNFOSettings(context.Background(), map[string]any{}, 7)
	if bare.ParentID != "" || bare.AccountID != 7 {
		t.Fatalf("未填目标目录时应只带账号：%+v", bare)
	}
}

// TestNFOSettingsIgnoresTaskCfgOverride covers 开关只认全局 settings：
// 任务 cfg 里塞同名 key 不该偷偷打开落盘。
func TestNFOSettingsIgnoresTaskCfgOverride(t *testing.T) {
	repo := &nfoConfigRepo{values: map[string]string{}}
	settingsSvc, err := settings.New(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{DataDir: t.TempDir(), Settings: settingsSvc})
	got := svc.loadNFOSettings(context.Background(), map[string]any{
		settings.KeyMOScrapeNFOEnabled: "true",
		settings.KeyMOScrapeNFOTarget:  "cloud",
	}, 9)
	if got.Enabled || got.Cloud || got.AccountID != 0 {
		t.Fatalf("任务 cfg 不该覆盖全局开关：%+v", got)
	}
}

// TestNFOTargetDirNeverEscapes covers 目标目录必须来自驱动反查，
// 不接受直接拼一个本地绝对路径 —— 那会写到网盘根本管不到的地方。
func TestNFOTargetDirNeverEscapes(t *testing.T) {
	svc := NewService(ServiceOptions{DataDir: t.TempDir()})
	action := &PlanAction{TargetName: "x.mkv", TargetParentID: ".."}
	got, name := svc.targetDirOf(context.Background(), action, 1)
	if got != "" {
		t.Fatalf("无 files 服务时不该返回目录，实际 %q", got)
	}
	if name != "x.mkv" {
		t.Fatalf("目标名应回落为 TargetName，实际 %q", name)
	}
}

// TestNFOTriggerSurvivesNilPlan covers 空计划不该 panic。
func TestNFOTriggerSurvivesNilPlan(t *testing.T) {
	svc := NewService(ServiceOptions{DataDir: t.TempDir(), NFO: &recordingNFO{}})
	svc.processNFOMetadata(context.Background(), "task", nil, map[string]any{}, 1)
	svc.processNFOMetadata(context.Background(), "task", &Plan{}, map[string]any{}, 1)
}

// TestNFORootDirIsNeverUsedForWorkNFO covers 剧集落在盘根时的兜底：
// workDir 上移后如果等于根，就退回季目录，绝不往盘根写 tvshow.nfo。
func TestNFORootDirIsNeverUsedForWorkNFO(t *testing.T) {
	nfo := &recordingNFO{}
	exec := stubPlanExecutor{plan: &Plan{Actions: []PlanAction{
		tvEpisodeAction("a1", "A.S01E01.mkv", "root-dir", "A", 1, 1),
	}}}
	svc, _, task := newNFOHookService(t,
		map[string]string{settings.KeyMOScrapeNFOEnabled: "true"},
		nfo, exec, map[string]string{"root-dir": "/Season 01"})

	svc.applyPlanRunner(context.Background(), task.ID, exec.plan, task, map[string]any{}, 1)

	works, _, _ := nfo.snapshot()
	if len(works) != 1 {
		t.Fatalf("应落一份作品级元数据，实际 %d", len(works))
	}
	if works[0].Dir != "/Season 01" {
		t.Fatalf("剧集直接放在盘根时不该把 tvshow.nfo 写到 /，实际 %q", works[0].Dir)
	}
	_ = os.Remove(filepath.Join(os.TempDir(), "none"))
}

func newNFOTestSettings(t *testing.T, values map[string]string) *settings.Service {
	t.Helper()
	if values == nil {
		values = map[string]string{}
	}
	svc, err := settings.New(context.Background(), &nfoConfigRepo{values: values})
	if err != nil {
		t.Fatalf("准备设置服务失败：%v", err)
	}
	return svc
}

// TestSettingsDictCarriesTheNewFields 证明 T14 的 6 个新配置真的走整理设置接口：
// 页面是通过 /admin/media-organize/settings 读写它们的，
// 不进 SettingsDict 就等于界面上改了、后端读不到。
func TestSettingsDictCarriesTheNewFields(t *testing.T) {
	svc := newNFOTestSettings(t, map[string]string{
		settings.KeyMOScrapeNFOEnabled:             "true",
		settings.KeyMOScrapeNFOTarget:              "cloud",
		settings.KeyMOScrapeUnrecognizedDir:        "/media/未识别",
		settings.KeyMOScrapeFollowExistingLocation: "true",
		settings.KeyMOScrapeSkipAction:             "move",
		settings.KeyMOBackupTarget:                 "cloud",
	})
	dict := SettingsDict(svc)
	if dict["scrape_nfo_enabled"] != true {
		t.Fatalf("scrape_nfo_enabled 必须是布尔，拿到 %#v", dict["scrape_nfo_enabled"])
	}
	if dict["scrape_follow_existing_location"] != true {
		t.Fatalf("scrape_follow_existing_location 必须是布尔，拿到 %#v", dict["scrape_follow_existing_location"])
	}
	if dict["scrape_nfo_target"] != "cloud" || dict["backup_target"] != "cloud" {
		t.Fatalf("目标项没透传：%#v", dict)
	}
	if dict["scrape_unrecognized_dir"] != "/media/未识别" || dict["scrape_skip_action"] != "move" {
		t.Fatalf("兜底项没透传：%#v", dict)
	}
}

// TestFieldGroupsAgreeWithTheRegistry：两张类型分组表必须和注册表里声明的类型一致。
// 分错组的表现是「页面能改、存进去变成字符串」，非常难查，所以直接和 registry.AllSpecs() 对账。
func TestFieldGroupsAgreeWithTheRegistry(t *testing.T) {
	types := map[string]string{}
	for _, sp := range settings.AllSpecs() {
		types[sp.Key] = string(sp.Type)
	}
	for field, key := range moSettingFieldToKey {
		want, ok := types[key]
		if !ok {
			t.Errorf("字段 %s 映射到注册表里不存在的 key %s", field, key)
			continue
		}
		switch {
		case moBoolSettingFields[field]:
			if want != string(settings.TypeBool) {
				t.Errorf("字段 %s 放在布尔组，但注册表声明 %s", field, want)
			}
		case moIntSettingFields[field]:
			if want != string(settings.TypeInt) {
				t.Errorf("字段 %s 放在整数组，但注册表声明 %s", field, want)
			}
		default:
			if want != string(settings.TypeString) && want != string(settings.TypeSelect) {
				t.Errorf("字段 %s 走默认字符串分支，但注册表声明 %s", field, want)
			}
		}
	}
	for field := range moBoolSettingFields {
		if _, ok := moSettingFieldToKey[field]; !ok {
			t.Errorf("布尔表里的 %s 不在 moSettingFieldToKey 里，是死条目", field)
		}
	}
	for field := range moIntSettingFields {
		if _, ok := moSettingFieldToKey[field]; !ok {
			t.Errorf("整数表里的 %s 不在 moSettingFieldToKey 里，是死条目", field)
		}
	}
	if moBoolSettingFields["media_tag_order"] || moIntSettingFields["media_tag_order"] {
		t.Error("media_tag_order 有自己的解析分支，不能落进通用类型分支")
	}
}

// TestUpdateSettingsRejectsNonBooleanForNewSwitches：布尔项收到字符串会直接报错，
// 而不是悄悄把 "false" 当值写进配置。
func TestUpdateSettingsRejectsNonBooleanForNewSwitches(t *testing.T) {
	svc := newNFOTestSettings(t, nil)
	err := UpdateSettings(context.Background(), svc, map[string]any{"scrape_nfo_enabled": "true"})
	if err == nil {
		t.Fatal("布尔项收到字符串应当报错")
	}
	if err := UpdateSettings(context.Background(), svc, map[string]any{"scrape_nfo_enabled": true}); err != nil {
		t.Fatalf("合法布尔值被拒: %v", err)
	}
}
