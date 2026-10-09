package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"litepan/internal/domain"
	"litepan/internal/playback"
	"litepan/internal/playbackrecord"
	"litepan/internal/store"
)

// fakeAccountRepo 只实现观察者用到的 Get。
type fakeAccountRepo struct {
	domain.AccountRepository
	acc *domain.Account
	err error
}

func (f *fakeAccountRepo) Get(_ context.Context, id int64) (*domain.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.acc, nil
}

// newPlaybackRecordObserverForTest 构造带内存库的观察者与读回仓储。
func newPlaybackRecordObserverForTest(t *testing.T, accounts domain.AccountRepository) (playback.RedirectObserver, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Memory: true})
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	st := store.New(db)
	svc := playbackrecord.New(db.WriteHandle(), db.ReadHandle())
	return newPlaybackRecordObserverWithAccounts(svc, accounts), st
}

// TestPlaybackRecordObserverRecordsRedirect 观察者应把 302 请求翻译成一条播放记录：
// UserId/Client/DeviceId 来自查询串，ItemName 为文件名基名，provider 由账号解析。
func TestPlaybackRecordObserverRecordsRedirect(t *testing.T) {
	ctx := context.Background()
	observer, st := newPlaybackRecordObserverForTest(t, &fakeAccountRepo{
		acc: &domain.Account{ID: 7, Name: "115", DriverType: "quark"},
	})

	req := httptest.NewRequest(http.MethodGet,
		"/strm/play/7/k/t/n/%E6%B5%81%E6%B5%AA%E5%9C%B0%E7%90%832.mkv?UserId=u1&Client=Emby%20Theater&DeviceId=d1", nil)
	res := playback.Resolved{File: domain.FileItem{Name: "流浪地球2.mkv"}}

	observer(req, 7, res, playback.Intent{})

	items, total, err := st.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("读取记录: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("期望 1 条记录, 实际 total=%d len=%d", total, len(items))
	}
	got := items[0]
	if got.EmbyUserID != "u1" {
		t.Fatalf("EmbyUserID = %q", got.EmbyUserID)
	}
	if got.Client != "Emby Theater" {
		t.Fatalf("Client = %q", got.Client)
	}
	if got.DeviceID != "d1" {
		t.Fatalf("DeviceID = %q", got.DeviceID)
	}
	if got.ItemName != "流浪地球2.mkv" {
		t.Fatalf("ItemName = %q", got.ItemName)
	}
	if got.RuleID != "1" {
		t.Fatalf("RuleID = %q, 期望固定 1", got.RuleID)
	}
	if got.Provider != "quark" {
		t.Fatalf("Provider = %q, 期望账号 DriverType", got.Provider)
	}
}

// TestPlaybackRecordObserverPrefersIntentFileName Intent 里的文件名优先于解析结果。
func TestPlaybackRecordObserverPrefersIntentFileName(t *testing.T) {
	ctx := context.Background()
	observer, st := newPlaybackRecordObserverForTest(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/files/download?UserId=u2", nil)
	res := playback.Resolved{File: domain.FileItem{Name: "远端名.mkv"}}

	observer(req, 0, res, playback.Intent{FileName: "展示名.mkv"})

	items, _, err := st.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("读取记录: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 条记录, 实际 %d", len(items))
	}
	if items[0].ItemName != "展示名.mkv" {
		t.Fatalf("ItemName = %q, 期望取 Intent.FileName", items[0].ItemName)
	}
	// 账号仓储为 nil 时 provider 为空且不 panic
	if items[0].Provider != "" {
		t.Fatalf("Provider = %q, 期望空串", items[0].Provider)
	}
}

// TestPlaybackRecordObserverSkipsEmpty 全空事件不应落库，避免噪音。
func TestPlaybackRecordObserverSkipsEmpty(t *testing.T) {
	ctx := context.Background()
	observer, st := newPlaybackRecordObserverForTest(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/files/download", nil)
	observer(req, 0, playback.Resolved{}, playback.Intent{})

	_, total, err := st.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("读取记录: %v", err)
	}
	if total != 0 {
		t.Fatalf("期望 0 条记录, 实际 %d", total)
	}
}

// TestPlaybackRecordObserverSurvivesAccountError 账号查询失败只丢 provider，记录照常落库。
func TestPlaybackRecordObserverSurvivesAccountError(t *testing.T) {
	ctx := context.Background()
	observer, st := newPlaybackRecordObserverForTest(t, &fakeAccountRepo{err: domain.Errorf(domain.CodeNotFound, "账号不存在")})

	req := httptest.NewRequest(http.MethodGet, "/strm/play/9/k/t/n/a.mkv?UserId=u3", nil)
	observer(req, 9, playback.Resolved{File: domain.FileItem{Name: "a.mkv"}}, playback.Intent{})

	items, total, err := st.PlaybackRecords.List(ctx, domain.PlaybackRecordQuery{})
	if err != nil {
		t.Fatalf("读取记录: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("期望 1 条记录, 实际 total=%d len=%d", total, len(items))
	}
	if items[0].Provider != "" {
		t.Fatalf("Provider = %q, 期望空串", items[0].Provider)
	}
}
