package playmonitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// 三态判定的用例。
//
// T11 验收 ①②③ 的核心：外网+流代理=计费中、外网+302=CDN 直连且计 0、
// 内网=不计费。这三条各有用例，且**302 分支与流代理分支都覆盖** ——
// 只写一条就会漏掉最常见的那个错：把 302 直连也计入上行。
// ─────────────────────────────────────────────────────────────────────────────

// fakeSettings 最小 Settings 实现（无 fallback，值由测试自己给全）。
type fakeSettings struct {
	monitorEnabled bool
	sampleSeconds  int
	idleSeconds    int
	reportEnabled  bool
	minSeconds     int
	gapMinutes     int
}

func (f fakeSettings) String(string) string { return "" }
func (f fakeSettings) Bool(key string) bool {
	switch key {
	case KeyMonitorEnabled:
		return f.monitorEnabled
	case KeyReportEnabled:
		return f.reportEnabled
	}
	return false
}

func (f fakeSettings) Int(key string) int {
	switch key {
	case KeyMonitorSampleSeconds:
		if f.sampleSeconds > 0 {
			return f.sampleSeconds
		}
	case KeyMonitorIdleSeconds:
		if f.idleSeconds > 0 {
			return f.idleSeconds
		}
	case KeyReportMinSeconds:
		return f.minSeconds
	case KeyReportGapMinutes:
		if f.gapMinutes > 0 {
			return f.gapMinutes
		}
	}
	return 0
}

// newTestService 造一个不跑后台循环的监控器（测试自己调 sample）。
func newTestService(t *testing.T, s Settings, traffic domain.PlayTrafficRepository, records domain.PlaybackRecordRepository) *Service {
	t.Helper()
	return New(Options{
		Settings: s,
		Traffic:  traffic,
		Records:  records,
		Log:      func(string, ...any) {},
	})
}

func externalRequest(item string, ua string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/strm/1/ddd/movie.mkv", nil)
	r.RemoteAddr = "203.0.113.9:41234" // 公网地址段（TEST-NET-3），非内网
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Range", "bytes=0-2097151")
	return r
}

func lanRequest(item string, ua string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/strm/1/ddd/movie.mkv", nil)
	r.RemoteAddr = "192.168.1.77:41234"
	r.Header.Set("User-Agent", ua)
	r.Header.Set("Range", "bytes=0-2097151")
	return r
}

func streamEventFor(item string, bitrate int64) StreamEvent {
	return StreamEvent{
		ItemName:  item,
		Bitrate:   bitrate,
		AppUserID: 7,
		Client:    "test-client",
		StrmPath:  "/strm/1/ddd/movie.mkv",
	}
}

// TestClassifyState 三态判定顺序（纯函数层）。
func TestClassifyState(t *testing.T) {
	cases := []struct {
		name   string
		lan    bool
		origin bool
		want   domain.PlayState
	}{
		{"外网+流代理=计费中", false, true, domain.PlayStateMetered},
		{"外网+302=CDN直连", false, false, domain.PlayStateCDN},
		{"内网+流代理=局域网（不是计费中！）", true, true, domain.PlayStateLAN},
		{"内网+302=局域网", true, false, domain.PlayStateLAN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyState(tc.lan, tc.origin)
			if got != tc.want {
				t.Fatalf("classifyState(lan=%v, origin=%v) = %s，期望 %s", tc.lan, tc.origin, got, tc.want)
			}
			// 计费标记必须与状态一致：只有"计费中"计费。
			if got.Metered() != (tc.want == domain.PlayStateMetered) {
				t.Fatalf("状态 %s 的 Metered() = %v，与状态不自洽", got, got.Metered())
			}
		})
	}
}

// TestThreeStatesThroughWrap 端到端三态：经 Wrap（生产入口）建会话。
func TestThreeStatesThroughWrap(t *testing.T) {
	const bitrate = 8_000_000 // 8 Mbps

	t.Run("外网流代理=计费中且累计流量", func(t *testing.T) {
		traffic := &fakeTrafficRepo{}
		svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, nil)

		r := externalRequest("A.mkv", "Player/1.0")
		w := svc.Wrap(r, streamEventFor("A.mkv", bitrate), true /*origin=流代理*/)
		if w == nil {
			t.Fatal("流代理分支应返回包装后的 writer")
		}
		if got := svc.stateOf(t, "A.mkv"); got != domain.PlayStateMetered {
			t.Fatalf("外网+流代理状态 = %s，期望计费中", got)
		}
		// 采样两次 = 10 秒 × 8Mbps/8 = 10,000,000 字节
		svc.sample(context.Background())
		svc.sample(context.Background())
		if traffic.Total != 2*(bitrate/8*5) {
			t.Fatalf("两轮采样后当日桶 = %d，期望 %d（码率×时长，每 5 秒一次）", traffic.Total, 2*(bitrate/8*5))
		}
	})

	t.Run("外网302=CDN直连且计0", func(t *testing.T) {
		traffic := &fakeTrafficRepo{}
		svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, nil)

		r := externalRequest("B.mkv", "Player/1.0")
		// origin=false → 302 直链分支
		_ = svc.Wrap(r, streamEventFor("B.mkv", bitrate), false)
		if got := svc.stateOf(t, "B.mkv"); got != domain.PlayStateCDN {
			t.Fatalf("外网+302 状态 = %s，期望 CDN 直连", got)
		}
		// 采样 5 轮：外网 302 的字节不经过自己服务器 → 桶必须恒为 0
		for i := 0; i < 5; i++ {
			svc.sample(context.Background())
		}
		if traffic.Total != 0 {
			t.Fatalf("外网 302 的当日桶 = %d，期望 0（直连不经自己服务器）", traffic.Total)
		}
	})

	t.Run("内网流代理=局域网且计0", func(t *testing.T) {
		traffic := &fakeTrafficRepo{}
		svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, nil)

		r := lanRequest("C.mkv", "Player/1.0")
		_ = svc.Wrap(r, streamEventFor("C.mkv", bitrate), true /*内网也走流代理*/)
		if got := svc.stateOf(t, "C.mkv"); got != domain.PlayStateLAN {
			t.Fatalf("内网+流代理状态 = %s，期望局域网", got)
		}
		for i := 0; i < 5; i++ {
			svc.sample(context.Background())
		}
		if traffic.Total != 0 {
			t.Fatalf("内网的当日桶 = %d，期望 0（内网不消耗上行）", traffic.Total)
		}
	})
}

// TestMeteredOnlyAccumulates 只有计费中的会话才累计。
func TestMeteredOnlyAccumulates(t *testing.T) {
	traffic := &fakeTrafficRepo{}
	svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, nil)

	// 三个会话同开：一个计费中、一个 CDN、一个局域网。
	_ = svc.Wrap(externalRequest("D.mkv", "UA/d"), streamEventFor("D.mkv", 8_000_000), true)
	_ = svc.Wrap(externalRequest("E.mkv", "UA/e"), streamEventFor("E.mkv", 8_000_000), false)
	_ = svc.Wrap(lanRequest("F.mkv", "UA/f"), streamEventFor("F.mkv", 8_000_000), true)

	svc.sample(context.Background())
	// 只有 D 计费：8Mbps/8*5 = 5,000,000
	if traffic.Total != 5_000_000 {
		t.Fatalf("混合三态当日桶 = %d，期望 %d（只有计费中计数）", traffic.Total, 5_000_000)
	}
}

// TestIdleSessionDisappears 停播约 1 分钟从列表消失（验收 ④）。
func TestIdleSessionDisappears(t *testing.T) {
	traffic := &fakeTrafficRepo{}
	records := &fakeRecordRepo{}
	svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, records)

	r := externalRequest("G.mkv", "Player/1.0")
	_ = svc.Wrap(r, streamEventFor("G.mkv", 8_000_000), true)
	if n := svc.sessionCount(); n != 1 {
		t.Fatalf("起播后会话数 = %d，期望 1", n)
	}
	// 心跳还在：60s 内不掉
	svc.ageSession(t, "G.mkv", 30*time.Second)
	svc.sample(context.Background())
	if n := svc.sessionCount(); n != 1 {
		t.Fatalf("30s 未见心跳时会话数 = %d，期望仍为 1（未达 60s 空闲）", n)
	}
	// 61s 无心跳 → 消失
	svc.ageSession(t, "G.mkv", 61*time.Second)
	svc.sample(context.Background())
	if n := svc.sessionCount(); n != 0 {
		t.Fatalf("61s 无心跳后会话数 = %d，期望 0（从列表消失）", n)
	}
	if len(records.Inserted) != 1 {
		t.Fatalf("停播应上报一次停止事件，实际 %d 次", len(records.Inserted))
	}
}

// TestPauseDebounceSingleStop 暂停 10 秒再继续只产生一次 stop（验收 ⑤）。
//
// 口径原文：「同一台设备在同一部片的同一位置停下，只会转交给媒体服务器一次」
// 「真正继续播放、连续播了约一分钟的量后，才重新显示『正在播放』」。
//
// 这里刻意分成两段断言，因为生产代码里「停播」有两条路径：
//   - OnStop（播放链路主动告知）关会话并记停止时刻，不落停止记录；
//   - sample 的心跳丢失路径才真正落一条 playback_records。
//
// 只测其中一条，会漏掉"暂停时多报了一次停止"这个真实问题。
func TestPauseDebounceSingleStop(t *testing.T) {
	traffic := &fakeTrafficRepo{}
	records := &fakeRecordRepo{}
	svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, traffic, records)

	ev := streamEventFor("H.mkv", 8_000_000)
	_ = svc.Wrap(externalRequest("H.mkv", "Player/1.0"), ev, true)
	id := svc.idOf(t, "H.mkv")

	// 第一次停播（用户按暂停）→ 关会话，记账停止时刻。
	svc.OnStop(id)
	if got := svc.sessionCount(); got != 0 {
		t.Fatalf("停播后会话数 = %d，期望 0", got)
	}
	if len(svc.sessions) != 0 {
		t.Fatal("停播后会话表未清空")
	}

	// 暂停 10 秒后续播 → 会话重新出现（用户看得到"正在播放"）。
	_ = svc.Wrap(externalRequest("H.mkv", "Player/1.0"), ev, true)
	if got := svc.sessionCount(); got != 1 {
		t.Fatalf("10s 后续播会话数 = %d，期望 1", got)
	}
	// 这段续播又停下 → 仍在消噪窗口内，停止事件被丢弃。
	svc.OnStop(svc.idOf(t, "H.mkv"))
	if got := svc.sessionCount(); got != 0 {
		t.Fatalf("消噪窗口内再次停播后会话数 = %d，期望 0", got)
	}
	if got := len(records.Inserted); got != 0 {
		t.Fatalf("消噪窗口内产生了 %d 条停止记录，期望 0（暂停只转交一次停止事件）", got)
	}

	// 真正继续播放：再次起播，且不再受上一次停止影响。
	_ = svc.Wrap(externalRequest("H.mkv", "Player/1.0"), ev, true)
	if got := svc.sessionCount(); got != 1 {
		t.Fatalf("真正续播后会话数 = %d，期望 1", got)
	}
	// 心跳丢失 → 这一次应当落一条停止记录（本次播放真的结束了）。
	svc.ageSession(t, "H.mkv", 61*time.Second)
	svc.sample(context.Background())
	if got := len(records.Inserted); got != 1 {
		t.Fatalf("心跳丢失落停止记录 %d 条，期望 1", got)
	}
	if got := svc.sessionCount(); got != 0 {
		t.Fatalf("心跳丢失后会话数 = %d，期望 0（从实时列表消失）", got)
	}
}

// TestSameMovieSameDeviceIsOneSession 同一台设备同一部片的多段请求只算一次播放。
func TestSameMovieSameDeviceIsOneSession(t *testing.T) {
	svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, &fakeTrafficRepo{}, nil)
	ev := streamEventFor("I.mkv", 8_000_000)
	_ = svc.Wrap(externalRequest("I.mkv", "Player/1.0"), ev, true)
	first := svc.idOf(t, "I.mkv")
	for i := 0; i < 3; i++ {
		_ = svc.Wrap(externalRequest("I.mkv", "Player/1.0"), ev, true)
	}
	if got := svc.sessionCount(); got != 1 {
		t.Fatalf("同设备同片多段请求后会话数 = %d，期望 1", got)
	}
	if got := svc.idOf(t, "I.mkv"); got != first {
		t.Fatalf("多段请求换了会话 ID：%s → %s（同一段播放不该被拆成多次）", first, got)
	}
}

// TestRelaunchIsNewSession 关播放器重开 = 新一次播放（验收 ⑥ 前半）。
func TestRelaunchIsNewSession(t *testing.T) {
	svc := newTestService(t, fakeSettings{monitorEnabled: true, sampleSeconds: 5, idleSeconds: 60}, &fakeTrafficRepo{}, nil)
	ev := streamEventFor("J.mkv", 8_000_000)
	_ = svc.Wrap(externalRequest("J.mkv", "Player/1.0"), ev, true)
	first := svc.idOf(t, "J.mkv")
	svc.OnStop(first) // 关播放器
	_ = svc.Wrap(externalRequest("J.mkv", "Player/1.0"), ev, true)
	if got := svc.idOf(t, "J.mkv"); got == first {
		t.Fatal("关播放器重开后仍是同一个会话 ID —— 播放次数会被少算")
	}
}

// TestDisabledMonitorIsTransparent 监控关闭时不包装、不建会话（透传）。
func TestDisabledMonitorIsTransparent(t *testing.T) {
	traffic := &fakeTrafficRepo{}
	svc := newTestService(t, fakeSettings{monitorEnabled: false}, traffic, nil)
	if w := svc.Wrap(externalRequest("K.mkv", "Player/1.0"), streamEventFor("K.mkv", 8_000_000), true); w != nil {
		t.Fatal("监控未启用时不应包装 writer（调用方会把它当成真 writer 用）")
	}
	if got := svc.sessionCount(); got != 0 {
		t.Fatalf("监控未启用时会话数 = %d，期望 0", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 测试用的仓储替身
// ─────────────────────────────────────────────────────────────────────────────

type fakeTrafficRepo struct {
	day      string
	Total    int64
	Calls    int
	Cleared  bool
	Since    time.Time
	HasSince bool
}

func (f *fakeTrafficRepo) Accumulate(ctx context.Context, day string, userID int64, userName string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	f.Total += bytes
	f.Calls++
	return nil
}
func (f *fakeTrafficRepo) EnsureEnabledSince(ctx context.Context) error {
	f.HasSince = true
	f.Since = time.Now()
	return nil
}
func (f *fakeTrafficRepo) EnabledSince(ctx context.Context) (time.Time, bool, error) {
	return f.Since, f.HasSince, nil
}
func (f *fakeTrafficRepo) ByUser(ctx context.Context, since, until string) ([]domain.PlayTrafficBucket, error) {
	return nil, nil
}
func (f *fakeTrafficRepo) TodayMonthTotal(ctx context.Context, day, month string) (int64, int64, int64, error) {
	return f.Total, f.Total, f.Total, nil
}
func (f *fakeTrafficRepo) RankByUser(ctx context.Context, month string, limit int) ([]domain.PlayTrafficRank, error) {
	return nil, nil
}
func (f *fakeTrafficRepo) Clear(ctx context.Context) (int64, error) {
	f.Total = 0
	f.Cleared = true
	return 1, nil
}

// fakeRecordRepo 记录播放历史写入，供停播/起播断言。
type fakeRecordRepo struct {
	Inserted []*domain.PlaybackRecord
}

func (f *fakeRecordRepo) Insert(ctx context.Context, rec *domain.PlaybackRecord) (int64, error) {
	f.Inserted = append(f.Inserted, rec)
	return int64(len(f.Inserted)), nil
}
func (f *fakeRecordRepo) Get(ctx context.Context, id int64) (*domain.PlaybackRecord, error) {
	return nil, nil
}
func (f *fakeRecordRepo) Delete(ctx context.Context, id int64) error { return nil }
func (f *fakeRecordRepo) List(ctx context.Context, q domain.PlaybackRecordQuery) ([]domain.PlaybackRecord, int64, error) {
	return nil, 0, nil
}
func (f *fakeRecordRepo) ListRange(ctx context.Context, q domain.PlaybackRecordQuery) ([]domain.PlaybackRecord, error) {
	return nil, nil
}
func (f *fakeRecordRepo) Stats(ctx context.Context) (domain.PlaybackRecordStats, error) {
	return domain.PlaybackRecordStats{}, nil
}
func (f *fakeRecordRepo) Clear(ctx context.Context, since, until string) (int64, error) {
	return 0, nil
}

// keyOf 按「片名」找会话键。会话键由 (设备标识, 归一化 UA, 片名) 拼成，
// 设备标识里带 UA 归一化结果（版本号被抹掉），测试不该把这个实现细节写死，
// 按片名后缀找即可。
func (s *Service) keyOf(t *testing.T, item string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.sessions {
		if strings.HasSuffix(k, "|"+item) {
			return k
		}
	}
	t.Fatalf("会话表里没有片名 %s 的会话（现有 key：%v）", item, s.sessionKeysLocked())
	return ""
}

// stateOf / idOf / ageSession 测试辅助：读会话表内部状态。
func (s *Service) stateOf(t *testing.T, item string) domain.PlayState {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[s.keyOfLocked(t, item)].State
}

func (s *Service) idOf(t *testing.T, item string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[s.keyOfLocked(t, item)].ID
}

func (s *Service) keyOfLocked(t *testing.T, item string) string {
	t.Helper()
	for k := range s.sessions {
		if strings.HasSuffix(k, "|"+item) {
			return k
		}
	}
	t.Fatalf("会话表里没有片名 %s 的会话（现有 key：%v）", item, s.sessionKeysLocked())
	return ""
}

// ageSession 把某个会话的心跳往前挪，用于验证空闲回收。
func (s *Service) ageSession(t *testing.T, item string, d time.Duration) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[s.keyOfLocked(t, item)]
	if e == nil {
		t.Fatalf("片名 %s 的会话不存在", item)
	}
	e.LastSeenAt = time.Now().Add(-d)
}

func (s *Service) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Service) sessionKeysLocked() []string {
	out := make([]string, 0, len(s.sessions))
	for k := range s.sessions {
		out = append(out, k)
	}
	return out
}

var _ = strings.TrimSpace // 保留 strings 导入以防后续用例需要
