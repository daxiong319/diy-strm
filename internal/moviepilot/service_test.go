package moviepilot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/moviepilot"
	"litepan/internal/store"
)

// newTestRepo 打开内存库并返回 MoviePilot 仓储。
func newTestRepo(t *testing.T) domain.MoviePilotRepository {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(db).MoviePilot
}

// newTestService 构造仅带仓储的服务（不启动后台循环）。
func newTestService(t *testing.T) (*moviepilot.Service, domain.MoviePilotRepository) {
	t.Helper()
	repo := newTestRepo(t)
	svc := moviepilot.New(moviepilot.Options{Repo: repo})
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	return svc, repo
}

// writeFile 写入测试文件。
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestNewReturnsNilWithoutRepo(t *testing.T) {
	if got := moviepilot.New(moviepilot.Options{}); got != nil {
		t.Fatalf("expected nil service when repo is nil, got %v", got)
	}
}

func TestStartStopIsIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc.Start(ctx)
	svc.Start(ctx) // 重复启动应为 no-op

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3_000_000_000)
	defer stopCancel()
	svc.Stop(stopCtx)
	svc.Stop(stopCtx) // 重复停止应为 no-op
}

func TestSaveConfigNormalizesFields(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()

	cfg, err := repo.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.BaseUrl = "http://mp.local:3000/"
	cfg.ApiToken = "  tok  "
	cfg.PromotionOrder = "FREE, normal, bogus, free"
	cfg.PollInterval = 0

	if err := svc.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := repo.LoadConfig(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.BaseUrl != "http://mp.local:3000" {
		t.Errorf("BaseUrl = %q, want trimmed", got.BaseUrl)
	}
	if got.ApiToken != "tok" {
		t.Errorf("ApiToken = %q, want trimmed", got.ApiToken)
	}
	if got.PromotionOrder != "free,normal" {
		t.Errorf("PromotionOrder = %q, want free,normal", got.PromotionOrder)
	}
	if got.PollInterval != moviepilot.DefaultPollMinutes {
		t.Errorf("PollInterval = %d, want %d", got.PollInterval, moviepilot.DefaultPollMinutes)
	}
}

func TestSaveConfigRejectsNil(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.SaveConfig(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestDecideWash(t *testing.T) {
	cases := []struct {
		name     string
		newName  string
		newSize  int64
		oldFiles []moviepilot.LocalFile
		want     bool
	}{
		{
			name:    "无旧版直接放行",
			newName: "遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4",
			newSize: 1 << 30,
			want:    true,
		},
		{
			name:    "新版分辨率更高应洗版",
			newName: "遮天.S01E01.第1集.2160p.WEB-DL.H.265.mp4",
			newSize: 4 << 30,
			oldFiles: []moviepilot.LocalFile{
				{AbsPath: "/old/遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4", Size: 1 << 30},
			},
			want: true,
		},
		{
			name:    "新版分辨率更低不洗版",
			newName: "遮天.S01E01.第1集.720p.WEB-DL.H.264.mp4",
			newSize: 500 << 20,
			oldFiles: []moviepilot.LocalFile{
				{AbsPath: "/old/遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4", Size: 2 << 30},
			},
			want: false,
		},
		{
			name:    "质量持平且新版更小不洗版",
			newName: "遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4",
			newSize: 1 << 30,
			oldFiles: []moviepilot.LocalFile{
				{AbsPath: "/old/遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4", Size: 2 << 30},
			},
			want: false,
		},
		{
			name:    "质量持平但新版更大应洗版",
			newName: "遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4",
			newSize: 3 << 30,
			oldFiles: []moviepilot.LocalFile{
				{AbsPath: "/old/遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4", Size: 2 << 30},
			},
			want: true,
		},
		{
			name:    "不同集不参与比较",
			newName: "遮天.S01E02.第2集.720p.WEB-DL.H.264.mp4",
			newSize: 500 << 20,
			oldFiles: []moviepilot.LocalFile{
				{AbsPath: "/old/遮天.S01E01.第1集.1080p.WEB-DL.H.264.mp4", Size: 2 << 30},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := moviepilot.ParseQualityFromName(tc.newName)
			got := moviepilot.DecideWash(q, tc.newName, tc.newSize, tc.oldFiles, nil)
			if got.Proceed != tc.want {
				t.Fatalf("Proceed = %v, want %v (reason: %s)", got.Proceed, tc.want, got.Reason)
			}
			if !got.Proceed && got.Reason == "" {
				t.Error("expected non-empty reason when not proceeding")
			}
		})
	}
}

func TestCollectLocalFilesSkipsPartialDownloads(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "影片A.mkv", "aaa")
	writeFile(t, dir, "影片B.mp4", "bbbb")
	writeFile(t, dir, "未完成.mkv.part", "x")
	writeFile(t, filepath.Join(dir, "sub"), "影片C.mkv", "ccccc")

	files, err := moviepilot.CollectLocalFiles(dir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3: %+v", len(files), files)
	}
	for _, f := range files {
		if filepath.Ext(f.AbsPath) == ".part" {
			t.Errorf("partial file collected: %s", f.AbsPath)
		}
		if f.RelPath == "" {
			t.Errorf("empty rel path for %s", f.AbsPath)
		}
	}
}

func TestCollectLocalFilesMissingDirIsEmpty(t *testing.T) {
	files, err := moviepilot.CollectLocalFiles(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("expected nil error for missing dir, got %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no files, got %d", len(files))
	}
}

func TestPlanOrganizeSkipsNonVideo(t *testing.T) {
	svc, _ := newTestService(t)
	dir := t.TempDir()
	writeFile(t, dir, "readme.txt", "hello")
	writeFile(t, dir, "poster.jpg", "img")

	plan := svc.PlanOrganize(context.Background(), nil, dir)
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Targets) != 0 {
		t.Fatalf("expected no targets, got %d", len(plan.Targets))
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("expected 2 skipped, got %d: %+v", len(plan.Skipped), plan.Skipped)
	}
}

func TestRecordFailedFileIsIdempotent(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeFile(t, dir, "无法识别.mkv", "x")

	for i := 0; i < 2; i++ {
		if err := svc.RecordFailedFile(ctx, 7, filepath.Join(dir, "无法识别.mkv"), "文件名无法识别", nil); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	rows, total, err := repo.ListFailedFiles(ctx, 1, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected 1 record after duplicate inserts, got %d (total %d)", len(rows), total)
	}
	if rows[0].Status != domain.MoviePilotFailedPending {
		t.Errorf("Status = %q, want pending", rows[0].Status)
	}
}

func TestFailedFileLifecycle(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	id, err := repo.CreateFailedFile(ctx, &domain.MoviePilotFailedFile{
		TaskID:   1,
		FileName: "未知影片.mkv",
		RootPath: t.TempDir(),
		Status:   domain.MoviePilotFailedPending,
		Reason:   "文件名无法识别",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.SkipFailedFile(ctx, id); err != nil {
		t.Fatalf("skip: %v", err)
	}
	got, err := repo.GetFailedFile(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.MoviePilotFailedSkipped {
		t.Fatalf("Status = %q, want skipped", got.Status)
	}

	if err := svc.ReopenFailedFile(ctx, id); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, _ = repo.GetFailedFile(ctx, id)
	if got.Status != domain.MoviePilotFailedPending {
		t.Fatalf("Status = %q, want pending", got.Status)
	}

	if err := svc.MarkFailedFileResolved(ctx, id); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, _ = repo.GetFailedFile(ctx, id)
	if got.Status != domain.MoviePilotFailedResolved {
		t.Fatalf("Status = %q, want resolved", got.Status)
	}
}

func TestResolveFailedFileRequiresTitleAndTmdb(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "未知影片.mkv", "x")
	id, err := repo.CreateFailedFile(ctx, &domain.MoviePilotFailedFile{
		TaskID:   1,
		FileName: "未知影片.mkv",
		RootPath: root,
		Status:   domain.MoviePilotFailedPending,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	cases := []struct {
		name      string
		mediaType string
		title     string
		tmdbID    int64
	}{
		{name: "缺少标题", mediaType: "movie", title: "  ", tmdbID: 100},
		{name: "缺少 TMDB ID", mediaType: "movie", title: "遮天", tmdbID: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ResolveFailedFile(ctx, id, tc.mediaType, tc.title, 2023, 1, tc.tmdbID); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestResolveFailedFileRejectsResolvedRecord(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	id, err := repo.CreateFailedFile(ctx, &domain.MoviePilotFailedFile{
		TaskID:   1,
		FileName: "a.mkv",
		RootPath: t.TempDir(),
		Status:   domain.MoviePilotFailedResolved,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.ResolveFailedFile(ctx, id, "movie", "标题", 2023, 1, 100)
	if err == nil || !contains(err.Error(), "已整理完成") {
		t.Fatalf("expected 已整理完成 error, got %v", err)
	}
}

func TestResolveFailedFileMissingSource(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	id, err := repo.CreateFailedFile(ctx, &domain.MoviePilotFailedFile{
		TaskID:   1,
		FileName: "不存在.mkv",
		RootPath: filepath.Join(t.TempDir(), "gone"),
		Status:   domain.MoviePilotFailedPending,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.ResolveFailedFile(ctx, id, "movie", "标题", 2023, 1, 100)
	if err == nil || !contains(err.Error(), "找不到文件") {
		t.Fatalf("expected 找不到文件 error, got %v", err)
	}
}

func TestCancelUploadTaskRejectsUploading(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	id, err := repo.CreateUploadTask(ctx, &domain.MoviePilotUploadTask{
		TorrentHash: "hash1",
		Title:       "遮天",
		LocalPath:   t.TempDir(),
		RemotePath:  "/影视/已整理",
		Status:      domain.MoviePilotUploadUploading,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.CancelUploadTask(ctx, id); err == nil {
		t.Fatal("expected error cancelling an uploading task")
	}

	rec, err := repo.GetUploadTask(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	rec.Status = domain.MoviePilotUploadPending
	if err := repo.UpdateUploadTask(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := svc.CancelUploadTask(ctx, id); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	got, _ := repo.GetUploadTask(ctx, id)
	if got.Status != domain.MoviePilotUploadCanceled {
		t.Fatalf("Status = %q, want canceled", got.Status)
	}
}

func TestCancelUploadTaskMissing(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.CancelUploadTask(context.Background(), 999); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestUploadTaskStillRunning(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.UploadTaskStillRunning(42) {
		t.Fatal("expected false for idle service")
	}
}

// contains 简化子串匹配，避免为测试引入额外依赖。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
