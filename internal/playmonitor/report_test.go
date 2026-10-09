package playmonitor

import (
	"testing"
	"time"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// 观影报告口径的用例：验收 ⑦⑧⑨ 的核心。
//
// 口径（照搬 参考实现 §2，不自行发明）：
//   - 播放次数 = 关播放器重开，或中断超过 gapMinutes 算新的一次
//   - 「忽略播放时长低于 N 秒」保留：低于过滤、等于保留
//   - 统计从启用起累计，不补算
//   - 0028 遗留的 Emby 播放记录要能进报告（双用户标识）
// ─────────────────────────────────────────────────────────────────────────────

// reportFixture 造一条播放记录。
func reportFixture(user string, item string, at time.Time, watched int, metered bool) domain.PlaybackRecord {
	return domain.PlaybackRecord{
		EmbyUserID:     user,
		ItemName:       item,
		PlaybackAt:     at.Format(time.RFC3339),
		Timestamp:      at.Format(time.RFC3339),
		WatchedSeconds: watched,
		Metered:        metered,
		RequestType:    domain.PlaybackRequestTypeStream,
		AppSource:      PlaySourceAuto,
	}
}

func baseInput(recs []domain.PlaybackRecord, now time.Time) reportInput {
	return reportInput{
		records:    recs,
		period:     domain.PlayReportPeriodDefault,
		top:        domain.PlayReportTopDefault,
		gapMinutes: DefaultReportGapMinutes,
		source:     domain.PlayReportSourceAuto,
		userNames:  map[string]string{},
		now:        now,
	}
}

func userResult(t *testing.T, res domain.PlayReportResult, embyUser string) *domain.PlayReport {
	t.Helper()
	for i := range res.Users {
		if res.Users[i].EmbyUserID == embyUser {
			return &res.Users[i]
		}
	}
	return nil
}

// TestReportSplitByGapMinutes 播放次数：中断 20 分钟=同一次，35 分钟=新一次（验收 ⑥ 后半）。
func TestReportSplitByGapMinutes(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	base := now.Add(-2 * time.Hour)

	cases := []struct {
		name      string
		gapSecond int
		wantCount int
	}{
		{"中断20分钟算同一次", 20, 1},
		{"中断35分钟算新的一次", 35, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput([]domain.PlaybackRecord{
				reportFixture("emby-u1", "Movie A", base, 600, true),
				reportFixture("emby-u1", "Movie A", base.Add(time.Duration(tc.gapSecond)*time.Minute), 600, true),
			}, now)
			in.gapMinutes = 30 // 统一用 30 分钟门槛
			res := BuildReport(in)
			u := userResult(t, res, "emby-u1")
			if u == nil {
				t.Fatalf("报告里没有 emby-u1（users=%+v）", res.Users)
			}
			if u.Count != tc.wantCount {
				t.Fatalf("中断 %d 分钟时播放次数 = %d，期望 %d", tc.gapSecond, u.Count, tc.wantCount)
			}
		})
	}
}

// TestReportMinSecondsThreshold 阈值保留：低于过滤、等于保留（验收 ⑦）。
func TestReportMinSecondsThreshold(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	in := baseInput([]domain.PlaybackRecord{
		reportFixture("u-low", "Short", now.Add(-3*time.Hour), 29, true), // 低于 30
		reportFixture("u-eq", "Equal", now.Add(-2*time.Hour), 30, true),  // 恰好等于 30
		reportFixture("u-high", "Long", now.Add(-time.Hour), 600, true),  // 高于
	}, now)
	in.minSeconds = 30
	res := BuildReport(in)

	if userResult(t, res, "u-low") != nil {
		t.Fatal("播放时长 29 秒低于阈值 30，却进了报告")
	}
	for _, u := range []string{"u-eq", "u-high"} {
		if userResult(t, res, u) == nil {
			t.Fatalf("用户 %s 未进报告（等于/高于阈值的记录必须保留）", u)
		}
	}
	if res.TotalCount != 2 {
		t.Fatalf("全量播放次数 = %d，期望 2（29 秒那条被过滤）", res.TotalCount)
	}
}

// TestReportZeroThresholdKeepsAll 阈值为 0 = 不过滤。
func TestReportZeroThresholdKeepsAll(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	in := baseInput([]domain.PlaybackRecord{
		reportFixture("u1", "Tiny", now.Add(-time.Hour), 1, true),
	}, now)
	in.minSeconds = 0
	res := BuildReport(in)
	if res.TotalCount != 1 {
		t.Fatalf("阈值为 0 时播放次数 = %d，期望 1（不过滤）", res.TotalCount)
	}
}

// TestReportOnlyMeteredCountsTraffic 上行只统计计费中的会话。
func TestReportOnlyMeteredCountsTraffic(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	cdn := reportFixture("u-cdn", "Direct", now.Add(-time.Hour), 600, false)
	cdn.RequestType = domain.PlaybackRequestTypeRedirect
	cdn.UploadedBytes = 999_999 // 302 直连本不该有上行，若被计入即为错
	lan := reportFixture("u-lan", "HomeLAN", now.Add(-90*time.Minute), 600, false)
	lan.UploadedBytes = 888_888
	metered := reportFixture("u-metered", "Proxy", now.Add(-80*time.Minute), 600, true)
	metered.UploadedBytes = 1234

	in := baseInput([]domain.PlaybackRecord{cdn, lan, metered}, now)
	res := BuildReport(in)
	if res.TotalUploadedBytes != 1234 {
		t.Fatalf("上行合计 = %d，期望 1234（只有计费中的会话计入）", res.TotalUploadedBytes)
	}
}

// TestReportNoticesStateTheSemantics 报告必须显式写明统计口径（验收 ⑧）。
func TestReportNoticesStateTheSemantics(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	since := now.Add(-3 * 24 * time.Hour)
	in := baseInput(nil, now)
	in.since = since
	in.minSeconds = 5
	in.gapMinutes = 30
	res := BuildReport(in)
	if len(res.Notices) == 0 {
		t.Fatal("报告没有下发任何口径说明（notices 为空）")
	}
	joined := ""
	for _, n := range res.Notices {
		joined += n + "\n"
	}
	for _, must := range []string{
		"启用",  // 「统计从启用后开始累计，之前的播放不会补算」
		"补算",  //
		"半小时", // 「中断超过半小时算新的一次」
		"等于",  // 「低于阈值的记录不进入排行，刚好等于阈值的记录仍保留」
	} {
		if !contains(joined, must) {
			t.Errorf("口径说明缺少关键词 %q，实际内容：\n%s", must, joined)
		}
	}
}

// TestReportUsesEnabledSinceAsFloor 「不补算」的下界来自启用时刻（验收 ⑧/⑨）。
func TestReportUsesEnabledSinceAsFloor(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	// 启用时刻在 2 天前，标称周期 7 天 → 实际区间应被压到 2 天。
	in := baseInput(nil, now)
	in.period = domain.PlayReportPeriod(7)
	in.since = now.Add(-48 * time.Hour)
	res := BuildReport(in)
	// Since/Until 是 RFC3339（要精确到时刻，因为下界来自启用时刻，不是整天）。
	if want := in.since.UTC().Format(time.RFC3339); res.Since != want {
		t.Fatalf("实际统计起点 = %s，期望 %s（启用时刻应当压过标称周期起点）", res.Since, want)
	}
	if res.EnabledSince == "" {
		t.Fatal("报告没有回传启用时刻，前端无法说明「从什么时候开始算」")
	}
}

// TestReportIncludesLegacyEmbyRecords 0028 遗留记录能进报告（验收 ⑨）。
//
// 这是双用户标识设计的验收点：0028 的 user_id 是 TEXT（Emby 侧标识），
// 0043 没有把它改成 INTEGER，RBAC 侧走新增的 app_user_id。
// 所以一条只有 emby_user_id、没有 app_user_id 的老记录必须仍然能归组。
func TestReportIncludesLegacyEmbyRecords(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	legacy := domain.PlaybackRecord{
		// 0028 时代写入的形态：只有 playback_at + 字符串 user_id，没有 app_user_id。
		EmbyUserID: "legacy-emby-user",
		ItemName:   "Legacy Movie",
		PlaybackAt: now.Add(-5 * time.Hour).Format(time.RFC3339),
		RuleID:     "1",
	}
	newStyle := reportFixture("modern-emby-user", "New Movie", now.Add(-time.Hour), 900, true)

	in := baseInput([]domain.PlaybackRecord{legacy, newStyle}, now)
	res := BuildReport(in)
	if userResult(t, res, "legacy-emby-user") == nil {
		t.Fatalf("0028 遗留记录没有进报告（users=%+v）", res.Users)
	}
	if userResult(t, res, "modern-emby-user") == nil {
		t.Fatalf("0043 新记录没有进报告（users=%+v）", res.Users)
	}
	if res.TotalCount != 2 {
		t.Fatalf("全量播放次数 = %d，期望 2", res.TotalCount)
	}
}

// TestReportGroupsByAppUserID RBAC 用户优先按 app_user_id 归组。
func TestReportGroupsByAppUserID(t *testing.T) {
	now := time.Date(2026, 3, 10, 20, 0, 0, 0, time.UTC)
	a := reportFixture("emby-x", "A", now.Add(-2*time.Hour), 600, true)
	a.AppUserID = 42
	b := reportFixture("emby-x", "B", now.Add(-time.Hour), 600, true)
	b.AppUserID = 42
	in := baseInput([]domain.PlaybackRecord{a, b}, now)
	in.userNames["app:42"] = "小明"
	res := BuildReport(in)
	if len(res.Users) != 1 {
		t.Fatalf("同一 app 用户被拆成了 %d 组，期望 1", len(res.Users))
	}
	if res.Users[0].Count != 2 {
		t.Fatalf("同一 app 用户播放次数 = %d，期望 2", res.Users[0].Count)
	}
	if res.Users[0].UserName != "小明" {
		t.Fatalf("用户名 = %q，期望 小明", res.Users[0].UserName)
	}
}

// contains 是极简子串判断（测试里用 strings 会多一个 import 而已，这里直接写）。
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
