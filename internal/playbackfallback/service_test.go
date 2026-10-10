package playbackfallback

import (
	"context"
	"errors"
	"testing"
	"time"

	"litepan/internal/crosstransfer"
	"litepan/internal/domain"
)

// 本文件要证明的是三件事：
//  1. 转存**不会删源**（验收 7）；
//  2. 短暂限流**不**触发转存（验收 8）；
//  3. 只有连续不可用超过 FallbackWait 才切账号，且没有备用账号时
//     保留原始行为而不是把错误换成「没有备用账号」。

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }

type fakeAccounts struct{ accounts []*domain.Account }

func (f *fakeAccounts) List(context.Context) ([]*domain.Account, error) { return f.accounts, nil }

type fakeHealth struct {
	status map[int64]domain.AuthStatus
	err    error
}

func (f fakeHealth) Status(_ context.Context, accountID int64) (domain.AuthStatus, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	st, ok := f.status[accountID]
	return st, ok, nil
}

type fakeFiles struct {
	item  *domain.FileItem
	err   error
	calls int
}

func (f *fakeFiles) Info(context.Context, int64, string) (*domain.FileItem, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.item, nil
}

// recordingTransfer 记录每次转存请求，并记录它是否**表达过删除意图**。
type recordingTransfer struct {
	inputs []crosstransfer.ExecuteInput
	events []crosstransfer.StreamEvent
}

func (r *recordingTransfer) ExecuteStream(_ context.Context, in crosstransfer.ExecuteInput, emit func(crosstransfer.StreamEvent) error) error {
	r.inputs = append(r.inputs, in)
	for _, ev := range r.events {
		if err := emit(ev); err != nil {
			return err
		}
	}
	return nil
}

func newService(t *testing.T, clk *fakeClock, tr Transfer, files FileLookup, accounts []AccountInfo) *Service {
	t.Helper()
	accts := make([]*domain.Account, 0, len(accounts))
	for _, a := range accounts {
		accts = append(accts, &domain.Account{ID: a.ID, Name: a.Name, DriverType: a.DriverType, IsActive: a.IsActive, IsDefault: a.IsDefault, SortOrder: a.SortOrder})
	}
	hs := map[int64]domain.AuthStatus{}
	for _, a := range accounts {
		if a.AuthOK {
			hs[a.ID] = domain.AuthActive
		} else {
			hs[a.ID] = domain.AuthFailed
		}
	}
	return New(Options{
		Config:   Config{Enabled: true, FallbackWait: 30 * time.Minute},
		Transfer: tr,
		Files:    files,
		Accounts: &fakeAccounts{accounts: accts},
		Health:   fakeHealth{status: hs},
		Now:      clk.Now,
	})
}

func TestShortLivedFailureDoesNotTriggerTransfer(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv", Size: 42}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true},
		{ID: 2, Name: "备", IsActive: true, AuthOK: true},
	})

	// 观察一次失败（= 账号认证失败事件），然后只过了 1 分钟。
	s.Observe(1, false, "限流")
	clk.Add(time.Minute)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if d.Switched {
		t.Fatalf("短暂限流不应触发跨账户转存，得到 %+v", d)
	}
	if d.AccountID != 1 || d.FileID != "f1" {
		t.Fatalf("应照常用原账号原文件播放，得到 %+v", d)
	}
	if len(tr.inputs) != 0 {
		t.Fatalf("不应发起任何转存，实际 %d 次", len(tr.inputs))
	}
}

func TestTransferAfterSustainedFailureAndSourceIsNeverDeleted(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{events: []crosstransfer.StreamEvent{
		{"event": "item", "success": true, "mode": "rapid", "file_id": "t-file-9"},
	}}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv", Size: 42}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true},
		{ID: 2, Name: "备", IsActive: true, AuthOK: true},
	})

	s.Observe(1, false, "登录失效")
	clk.Add(31 * time.Minute)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if !d.Switched || !d.Preparing {
		t.Fatalf("持续不可用 31 分钟应触发转存，得到 %+v", d)
	}
	if d.AccountID != 2 || d.FileID != "t-file-9" {
		t.Fatalf("秒传命中后应使用目标账号的真实 fileID，得到 %+v", d)
	}
	if len(tr.inputs) != 1 {
		t.Fatalf("应恰好发起 1 次转存，实际 %d", len(tr.inputs))
	}
	in := tr.inputs[0]
	if in.SourceAccountID != 1 || in.TargetAccountID != 2 {
		t.Fatalf("转存方向不对：%+v", in)
	}
	// 验收 7 的核心：切账号只能「多一份副本」，绝不能「删掉源」。
	if !in.Fallback {
		t.Fatal("Fallback 必须为 true，否则未命中秒传时文件根本不会被复制")
	}
	// 本包没有、也不该有任何删除源的能力；这里显式断言输入里不含删除标记。
	if in.Conflict == "delete" || in.Conflict == "delete_source" {
		t.Fatalf("转存不得表达删除意图：Conflict=%q", in.Conflict)
	}
	if len(in.Files) != 1 || in.Files[0].SourceFileID != "f1" {
		t.Fatalf("转存文件清单不对：%+v", in.Files)
	}
	// 源账号在秒传成功后应被「忘掉」，否则下次再坏会立刻又转存一遍。
	if _, still := s.UnhealthyFor(1); still {
		t.Fatal("秒传成功后应清掉源账号的不可用观察")
	}
}

func TestQueuedTransferReportsPreparingWithoutFakeFileID(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{events: []crosstransfer.StreamEvent{
		{"event": "item", "success": true, "mode": "relay"},
	}}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv", Size: 42}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true},
		{ID: 2, Name: "备", IsActive: true, AuthOK: true},
	})
	s.Observe(1, false, "登录失效")
	clk.Add(31 * time.Minute)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if !d.Preparing {
		t.Fatalf("中转上传排队时也应回 Preparing，得到 %+v", d)
	}
	if d.FileID != "" {
		t.Fatalf("目标文件尚未落地，不得返回伪造的 fileID，得到 %q", d.FileID)
	}
	if d.Reason == "" {
		t.Fatal("转存期间必须给播放器一句能显示的话")
	}
	if d.AccountName != "备" {
		t.Fatalf("应说明转存到了哪个账号，得到 %q", d.AccountName)
	}
}

func TestFailedTransferFallsBackToOriginalAccount(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{events: []crosstransfer.StreamEvent{
		{"event": "item", "success": false, "mode": "error", "error": "目标拒绝"},
	}}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv"}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true},
		{ID: 2, Name: "备", IsActive: true, AuthOK: true},
	})
	s.Observe(1, false, "登录失效")
	clk.Add(31 * time.Minute)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if d.Switched {
		t.Fatalf("转存失败不应假装成功，得到 %+v", d)
	}
	if d.AccountID != 1 || d.FileID != "f1" {
		t.Fatalf("转存失败时应照常用原请求播放，得到 %+v", d)
	}
}

func TestNoBackupAccountKeepsOriginalBehaviour(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv"}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "唯一", IsActive: true, AuthOK: true},
	})
	s.Observe(1, false, "登录失效")
	clk.Add(31 * time.Minute)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if d.Switched || d.AccountID != 1 {
		t.Fatalf("只有一个账号时不应尝试转存，得到 %+v", d)
	}
	if len(tr.inputs) != 0 {
		t.Fatal("没有目标账号时不应发起转存")
	}
}

func TestRecoveredAccountClearsObservation(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1", Name: "movie.mkv"}}
	s := newService(t, clk, tr, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true},
		{ID: 2, Name: "备", IsActive: true, AuthOK: true},
	})
	s.Observe(1, false, "限流")
	s.Observe(1, true, "")
	if _, still := s.UnhealthyFor(1); still {
		t.Fatal("账号恢复后应清掉不可用观察，否则再次短暂失败就会立刻转存")
	}
}

func TestPickTargetSkipsSourceUnhealthyAndInactive(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1"}}
	s := newService(t, clk, &recordingTransfer{}, files, []AccountInfo{
		{ID: 1, Name: "源", IsActive: true, AuthOK: true, SortOrder: 0},
		{ID: 2, Name: "停用", IsActive: false, AuthOK: true, SortOrder: 1},
		{ID: 3, Name: "认证坏", IsActive: true, AuthOK: false, SortOrder: 2},
		{ID: 4, Name: "普通", IsActive: true, AuthOK: true, SortOrder: 3},
		{ID: 5, Name: "默认", IsActive: true, AuthOK: true, IsDefault: true, SortOrder: 9},
	})
	got, err := s.PickTarget(context.Background(), 1)
	if err != nil {
		t.Fatalf("应能选出目标账号：%v", err)
	}
	if got.ID != 5 {
		t.Fatalf("默认账号应优先于排序号靠前的普通账号，得到 %+v", got)
	}
	only := newService(t, clk, &recordingTransfer{}, files, []AccountInfo{{ID: 5, Name: "默认", IsActive: true, AuthOK: true}})
	if _, err := only.PickTarget(context.Background(), 5); err == nil {
		t.Fatal("排除源账号后若没有别的候选，应报「没有可用的备用账号」而不是把文件转存到它自己")
	}
}

func TestResolveIsCompleteNoOpWhenDisabled(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tr := &recordingTransfer{}
	files := &fakeFiles{item: &domain.FileItem{ID: "f1"}}
	accts := []AccountInfo{{ID: 1, Name: "源", IsActive: true, AuthOK: true}, {ID: 2, Name: "备", IsActive: true, AuthOK: true}}
	s := newService(t, clk, tr, files, accts)
	s.cfg.Enabled = false
	s.Observe(1, false, "坏了")
	clk.Add(10 * time.Hour)

	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if d.Switched || d.Preparing || len(tr.inputs) != 0 {
		t.Fatalf("开关关闭时应完全不动，得到 %+v", d)
	}
}

func TestResolveIsCompleteNoOpWhenNotWired(t *testing.T) {
	s := New(Options{})
	d := s.Resolve(context.Background(), Request{AccountID: 1, FileID: "f1"})
	if d.Switched || d.AccountID != 1 || d.FileID != "f1" {
		t.Fatalf("未装配依赖时应退化为照常播放，得到 %+v", d)
	}
	s.Observe(1, false, "x")
	if s.Transferable(1) {
		t.Fatal("未装配依赖时不得认为账号可转存")
	}
}

func TestUnhealthyTableIsBounded(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := New(Options{Config: Config{Enabled: true}, Now: clk.Now})
	for i := int64(1); i <= maxUnhealthyAccounts*2; i++ {
		s.Observe(i, false, "坏了")
		clk.Add(time.Second)
	}
	s.mu.Lock()
	n := len(s.unhealthy)
	s.mu.Unlock()
	if n > maxUnhealthyAccounts {
		t.Fatalf("不可用观察表应被限流在 %d 条以内，实际 %d", maxUnhealthyAccounts, n)
	}
}

func TestHealthAdapterTreatsMissingRowAsNotYetKnown(t *testing.T) {
	ad := HealthFromStore(failingStore{err: domain.Errorf(domain.CodeNotFound, "没有这一行")})
	st, ok, err := ad.Status(context.Background(), 7)
	if err != nil || ok || st != "" {
		t.Fatalf("查不到行应报「尚不知情」而不是错误，得到 %q/%v/%v", st, ok, err)
	}
	ad = HealthFromStore(failingStore{err: errors.New("db 挂了")})
	if _, _, err = ad.Status(context.Background(), 7); err == nil {
		t.Fatal("真实的仓储错误应向上抛，不能被当成「账号健康」")
	}
}

type failingStore struct{ err error }

func (f failingStore) Get(context.Context, int64) (*domain.AuthState, error) { return nil, f.err }

func TestObserveRejectsNonPositiveAccount(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	s := New(Options{Now: clk.Now})
	s.Observe(0, false, "x")
	if _, ok := s.UnhealthyFor(0); ok {
		t.Fatal("accountID=0 不是合法账号，不应被记入不可用观察")
	}
}
