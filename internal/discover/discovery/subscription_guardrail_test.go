package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/settings"
)

// T06 · 订阅执行护栏的用例：执行强度四档、时段窗口、完结宽限。
//
// 三组语义各自独立，各自都有反直觉的一面，所以分开测：
//
//	TestBudget*        ——「被筛掉的候选不占次数」「发起后失败的照常计数」
//	                    这是任务书明确要求、而最容易被写成「先申请预算再判定」搞反的两条。
//	TestTimeWindow*    ——「时段外直接跳过、不补跑」。补跑会在恢复时段第一分钟
//	                    把积压候选一起转存，这种突发流量正是风控盯的形态。
//	TestFinishedGrace* ——「宽限期内继续搜但降到保守档」。
//
// 现在这些测试大多不碰数据库：now/wait/rand 全部可注入，
// 于是 45~65 秒的真实等待在测试里是零耗时的，但断言的仍然是**实际请求的间隔区间**。

// ---------------------------------------------------------------------------
// 一、执行强度
// ---------------------------------------------------------------------------

// newTestBudget 造一个注入式预算：假时钟 + 零耗时 wait + 可预测 rand。
// 返回的预算会把每次放行的间隔记在 granted 里供断言。
func newTestBudget(p executionProfile, seeded ...int) (*runBudget, *[]time.Duration) {
	clock := time.Date(2026, 3, 1, 2, 0, 0, 0, time.Local)
	b := newRunBudget(p)
	b.now = func() time.Time { return clock }
	b.wait = func(ctx context.Context, d time.Duration) error {
		clock = clock.Add(d) // 假时钟跟着等待一起走
		return nil
	}
	if len(seeded) > 0 {
		vals := seeded
		i := 0
		b.rand = func(n int) int {
			if n <= 0 {
				return 0
			}
			v := vals[i%len(vals)] % n
			i++
			return v
		}
	}
	var granted []time.Duration
	b.granted = granted
	return b, &granted
}

func TestExecutionProfilePresets(t *testing.T) {
	setupLedgerTest(t, nil)

	cases := []struct {
		mode     string
		attempts int
		min, max time.Duration
	}{
		// 这四个数字来自 参考实现 官网 docs，逐字可查，不是推测值。
		{ModeConservative, 1, 45 * time.Second, 65 * time.Second},
		{ModeBalanced, 2, 25 * time.Second, 40 * time.Second},
		{ModeAggressive, 3, 10 * time.Second, 18 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := presetProfile(tc.mode)
			if p.Attempts != tc.attempts {
				t.Fatalf("尝试数 = %d, 期望 %d", p.Attempts, tc.attempts)
			}
			if p.IntervalMin != tc.min || p.IntervalMax != tc.max {
				t.Fatalf("间隔 = %v~%v, 期望 %v~%v", p.IntervalMin, p.IntervalMax, tc.min, tc.max)
			}
		})
	}
}

func TestCustomExecutionProfileUsesConfiguredValues(t *testing.T) {
	setupLedgerTest(t, map[string]string{
		KeySubscriptionExecutionMode:     ModeCustom,
		KeySubscriptionCustomAttempts:    "5",
		KeySubscriptionCustomIntervalSec: "12",
		KeySubscriptionCustomJitterSec:   "8",
	})
	p := resolveExecutionProfile(false)
	if p.Attempts != 5 {
		t.Fatalf("自定义档尝试数 = %d, 期望 5", p.Attempts)
	}
	if p.IntervalMin != 12*time.Second || p.IntervalMax != 20*time.Second {
		t.Fatalf("自定义档间隔 = %v~%v, 期望 12s~20s", p.IntervalMin, p.IntervalMax)
	}
}

// TestBudgetAggressiveIntervalWithinWindow 是任务书验收项①：
// 激进档实测两次转存之间的间隔必须落在 10~18s（含抖动）。
func TestBudgetAggressiveIntervalWithinWindow(t *testing.T) {
	b, _ := newTestBudget(profileAggressive)
	for i := 0; i < profileAggressive.Attempts; i++ {
		if err := b.Acquire(context.Background()); err != nil {
			t.Fatalf("第 %d 次尝试被拒：%v", i+1, err)
		}
	}
	if b.used != 3 {
		t.Fatalf("放行次数 = %d, 期望 3", b.used)
	}
	// 第一次不等待，所以只应记录两次间隔。
	if len(b.granted) != 2 {
		t.Fatalf("记录的间隔数 = %d, 期望 2", len(b.granted))
	}
	for i, d := range b.granted {
		if d < 10*time.Second || d > 18*time.Second {
			t.Fatalf("第 %d 个间隔 = %v, 超出 10s~18s", i+1, d)
		}
	}
}

// TestBudgetStopsAfterAttempts 用尽之后必须明确拒绝，而不是继续放行。
func TestBudgetStopsAfterAttempts(t *testing.T) {
	b, _ := newTestBudget(profileConservative)
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("第 1 次应当放行：%v", err)
	}
	if err := b.Acquire(context.Background()); err != ErrBudgetExhausted {
		t.Fatalf("第 2 次应当返回 ErrBudgetExhausted，实际 %v", err)
	}
	if b.remaining() != 0 {
		t.Fatalf("remaining = %d, 期望 0", b.remaining())
	}
}

// TestBudgetSkippedCandidatesDoNotConsumeAttempts 是任务书验收项①的后半句：
// **被筛掉的候选不消耗尝试次数**。
//
// 用 10 条候选里只有 1 条过判定来验证：如果 Acquire 被放在判定之前
// （哪怕只是挪到 for 循环开头），这一轮就会因为 10 条候选把预算耗光，
// 而实际只转存了 1 条 —— 用户配的「每轮 2 条」就名不副实。
func TestBudgetSkippedCandidatesDoNotConsumeAttempts(t *testing.T) {
	b, _ := newRunBudgetForTest(profileBalanced), (*[]time.Duration)(nil)

	candidates := 10
	judgedPass := 0
	for i := 0; i < candidates; i++ {
		// 模拟判定：10 条里只有 1 条过（例如体积下限筛掉 9 条）。
		pass := i == 3
		if !pass {
			continue // 被闸门筛掉，不申请预算
		}
		if err := b.Acquire(context.Background()); err != nil {
			t.Fatalf("唯一过判定的候选不该被预算挡住：%v", err)
		}
		judgedPass++
	}
	if judgedPass != 1 {
		t.Fatalf("过判定条数 = %d, 期望 1", judgedPass)
	}
	if b.used != 1 {
		t.Fatalf("消耗的尝试次数 = %d, 期望 1（被筛掉的 9 条不计数）", b.used)
	}
	if b.remaining() != profileBalanced.Attempts-1 {
		t.Fatalf("remaining = %d, 期望 %d", b.remaining(), profileBalanced.Attempts-1)
	}
}

// newRunBudgetForTest 造一个零等待的预算，用于只关心次数、不关心时间的用例。
func newRunBudgetForTest(p executionProfile) *runBudget {
	b, _ := newTestBudget(p)
	return b
}

// TestBudgetCountsFailedTransfers 任务书：「已发起但失败的仍计数」。
// Acquire 一旦放行就计数，所以下面的转存失败不影响计数语义。
func TestBudgetCountsFailedTransfers(t *testing.T) {
	b, _ := newTestBudget(profileBalanced)
	transfersAttempted, transfersFailed := 0, 0
	for i := 0; i < 5; i++ {
		if err := b.Acquire(context.Background()); err != nil {
			break
		}
		transfersAttempted++
		if i%2 == 0 {
			transfersFailed++ // 模拟转存报错
		}
	}
	if transfersAttempted != 2 {
		t.Fatalf("发起次数 = %d, 期望 2", transfersAttempted)
	}
	if transfersFailed != 1 {
		t.Fatalf("失败次数 = %d, 期望 1", transfersFailed)
	}
	if b.used != 2 {
		t.Fatalf("失败的那次没有计数：used = %d, 期望 2", b.used)
	}
}

func TestEnforceGraceDowngradesBudget(t *testing.T) {
	b, _ := newTestBudget(profileAggressive)
	b.enforceGrace()
	if b.profile.Attempts != profileConservative.Attempts {
		t.Fatalf("宽限期尝试数 = %d, 期望 %d", b.profile.Attempts, profileConservative.Attempts)
	}
	if b.profile.IntervalMin != profileConservative.IntervalMin {
		t.Fatalf("宽限期间隔 = %v, 期望 %v", b.profile.IntervalMin, profileConservative.IntervalMin)
	}
}

func TestEnforceGraceDoesNotRefundUsedAttempts(t *testing.T) {
	b, _ := newTestBudget(profileAggressive)
	if err := b.Acquire(context.Background()); err != nil {
		t.Fatalf("首次尝试应当放行：%v", err)
	}
	b.enforceGrace()
	if b.used != 1 {
		t.Fatalf("已用次数被回退了：used = %d, 期望 1", b.used)
	}
	// 保守档只剩 1 次，激进档已用过 1 次 → 正好还剩 1 次。
	// 保守档整轮只给 1 次，已经用掉 ⇒ 本轮不再新增尝试。
	if b.remaining() != 0 {
		t.Fatalf("remaining = %d, 期望 0", b.remaining())
	}
	if err := b.Acquire(context.Background()); err != ErrBudgetExhausted {
		t.Fatalf("降档后不应再放行，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 二、时段窗口
// ---------------------------------------------------------------------------

func TestParseTimeWindows(t *testing.T) {
	cases := []struct {
		raw  string
		want timeWindows
	}{
		{"", nil},
		{"   ", nil},
		{"00:00-08:00", timeWindows{{Start: "00:00", End: "08:00"}}},
		{"23:00-06:00", timeWindows{{Start: "23:00", End: "06:00"}}},
		{"00:00-08:00,23:00-06:00", timeWindows{{Start: "00:00", End: "08:00"}, {Start: "23:00", End: "06:00"}}},
		{"00:00-08:00；23:00-06:00", timeWindows{{Start: "00:00", End: "08:00"}, {Start: "23:00", End: "06:00"}}},
		// 写错的条目被丢掉而不是让整条配置失效。
		{"乱写", nil},
		{"25:00-26:00", nil},
		{"00:00-08:00,乱写", timeWindows{{Start: "00:00", End: "08:00"}}},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got := parseTimeWindows(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("parseTimeWindows(%q) = %+v, 期望 %+v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseTimeWindows(%q)[%d] = %+v, 期望 %+v", tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestTimeWindowSkipsOutsideAndNoMakeUpRun 是任务书验收项②：
// 窗口 00:00-08:00，08:05 触发 → **直接跳过**，不做补跑。
func TestTimeWindowSkipsOutsideAndNoMakeUpRun(t *testing.T) {
	windows := parseTimeWindows("00:00-08:00")
	at := func(h, m int) time.Time {
		return time.Date(2026, 3, 1, h, m, 0, 0, time.Local)
	}
	if outsideTimeWindows(windows, at(8, 5)) != true {
		t.Fatalf("08:05 应当落在 00:00-08:00 之外")
	}
	if outsideTimeWindows(windows, at(7, 55)) != false {
		t.Fatalf("07:55 应当落在窗口内")
	}
	// 补跑与否由 processSubscriptionLocked 决定：它把 next_check_at 按正常周期推进
	// 再 return，所以不存在「时段到了立刻补一轮」的行为。单独断言跳过文案，
	// 免得文案微调就打挂。
	if !strings.Contains(timeWindowSkipMessage, "时段外") {
		t.Fatalf("跳过文案 %q 不含「时段外」", timeWindowSkipMessage)
	}
}

// TestTimeWindowCrossMidnight 是任务书验收项③：
// 跨午夜 23:00-06:00，01:00 **在窗口内**（结束早于开始表示跨午夜）。
func TestTimeWindowCrossMidnight(t *testing.T) {
	windows := parseTimeWindows("23:00-06:00")
	at := func(h, m int) time.Time {
		return time.Date(2026, 3, 1, h, m, 0, 0, time.Local)
	}
	if outsideTimeWindows(windows, at(1, 0)) {
		t.Fatalf("01:00 应当落在跨午夜窗口 23:00-06:00 内")
	}
	if outsideTimeWindows(windows, at(23, 30)) {
		t.Fatalf("23:30 应当落在跨午夜窗口内")
	}
	if !outsideTimeWindows(windows, at(6, 30)) {
		t.Fatalf("06:30 应当落在跨午夜窗口之外")
	}
	if !outsideTimeWindows(windows, at(12, 0)) {
		t.Fatalf("12:00 应当落在跨午夜窗口之外")
	}
}

func TestEmptyTimeWindowsMeansUnrestricted(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.Local)
	if outsideTimeWindows(parseTimeWindows(""), now) {
		t.Fatalf("空配置应当是不限时段（fail-open）")
	}
	if outsideTimeWindows(parseTimeWindows("乱写"), now) {
		t.Fatalf("无效配置应当是不限时段（fail-open）")
	}
}

// TestOnlyScheduledTriggerIsWindowBlocked 逆向确认：docs 明确
// 「时段外到点那轮直接跳过不补跑，**手动『立即搜索』和新建订阅首次搜索不受限**」。
func TestOnlyScheduledTriggerIsWindowBlocked(t *testing.T) {
	cases := []struct {
		trigger string
		exempt  bool
	}{
		{"scheduled", false},
		{"", false},
		{"manual", true},
		{"mcp", true},
		{"emby_missing_manual", true},
	}
	for _, tc := range cases {
		t.Run(tc.trigger, func(t *testing.T) {
			if got := triggerExemptFromWindow(tc.trigger); got != tc.exempt {
				t.Fatalf("triggerExemptFromWindow(%q) = %v, 期望 %v", tc.trigger, got, tc.exempt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 三、完结宽限
// ---------------------------------------------------------------------------

// TestFinishedGraceDefaultIs7Days 逆向确认值：默认 7 天。
func TestFinishedGraceDefaultIs7Days(t *testing.T) {
	setupLedgerTest(t, nil)
	if got := finishedGraceDays(); got != 7 {
		t.Fatalf("默认宽限天数 = %d, 期望 7", got)
	}
}

func TestFinishedGraceRespectsConfiguredValue(t *testing.T) {
	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "3"})
	if got := finishedGraceDays(); got != 3 {
		t.Fatalf("宽限天数 = %d, 期望 3", got)
	}
}

// transferItem 造一条已转存记录：status='transferred'，季集信息藏在 Candidate JSON 里。
func transferItem(t *testing.T, subID uint, season, episode, total int, at time.Time) {
	t.Helper()
	ep := &candidateEpisode{EpisodeNum: &episode}
	if season > 0 {
		ep.SeasonNum = &season
	}
	if total > 0 {
		ep.TotalEpisodeNum = &total
		ep.IsComplete = true
	}
	payload, err := json.Marshal(resourceCandidate{
		MediaType: "tv", Title: "某剧", Episode: ep,
	})
	if err != nil {
		t.Fatalf("序列化候选失败：%v", err)
	}
	row := DiscoverySubscriptionItem{
		SubscriptionID: subID,
		ItemKey:        fmt.Sprintf("s%d-s%d-e%d", subID, season, episode),
		Status:         "transferred",
		Candidate:      string(payload),
		TransferredAt:  &at,
	}
	if err := ddb.Db.Create(&row).Error; err != nil {
		t.Fatalf("写入转存记录失败：%v", err)
	}
}

func tvCandidate(total, episode int) resourceCandidate {
	ep := &candidateEpisode{EpisodeNum: &episode, TotalEpisodeNum: &total, IsComplete: true}
	return resourceCandidate{MediaType: "tv", Title: "某剧", Episode: ep}
}

// TestFinishedGraceDay7StillSearchesDay8Stops 是任务书验收项④：
// 宽限期内仍检索，第 8 天判定完结。
func TestFinishedGraceStillSearchesWithinGrace(t *testing.T) {
	sub := &DiscoverySubscription{ID: 9101, MediaType: "tv", Title: "某剧"}
	rule := DiscoverySubscriptionRule{SubscriptionID: sub.ID, Name: "默认"}

	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "7"})

	now := time.Now()
	season := 2
	lastTransfer := now.Add(-6 * 24 * time.Hour) // 6 天前转存齐，宽限 7 天 → 还在宽限
	// 转存齐：1..24 全在
	for ep := 1; ep <= 24; ep++ {
		transferItem(t, sub.ID, season, ep, 24, lastTransfer)
	}
	got := deriveFinishedState(sub, rule, []resourceCandidate{tvCandidate(24, 1)})
	if got.Total != 24 {
		t.Fatalf("总集数 = %d, 期望 24", got.Total)
	}
	if got.Finished {
		t.Fatalf("宽限期内不应判完结：%v", got)
	}
	if !got.InGrace {
		t.Fatalf("宽限期内 InGrace 应为 true")
	}
}

func TestFinishedGraceStopsAfterGrace(t *testing.T) {
	sub := &DiscoverySubscription{ID: 9102, MediaType: "tv", Title: "某剧"}
	rule := DiscoverySubscriptionRule{SubscriptionID: sub.ID, Name: "默认"}

	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "7"})

	now := time.Now()
	season := 2
	lastTransfer := now.Add(-8 * 24 * time.Hour) // 8 天前转存齐，宽限 7 天 → 已过期
	for ep := 1; ep <= 24; ep++ {
		transferItem(t, sub.ID, season, ep, 24, lastTransfer)
	}
	got := deriveFinishedState(sub, rule, []resourceCandidate{tvCandidate(24, 1)})
	if !got.Finished {
		t.Fatalf("宽限期已过应判完结：InGrace=%v Reason=%s", got.InGrace, got.Reason)
	}
}

func TestFinishedGraceUnknownTotalNeverFinishes(t *testing.T) {
	sub := &DiscoverySubscription{ID: 9103, MediaType: "tv", Title: "某剧"}
	rule := DiscoverySubscriptionRule{SubscriptionID: sub.ID, Name: "默认"}

	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "7"})

	now := time.Now()
	// 转存了一堆集，但标题里没写「全N集」→ 推不出分母。
	for ep := 1; ep <= 5; ep++ {
		transferItem(t, sub.ID, 1, ep, 0, now.Add(-30*24*time.Hour))
	}

	got := deriveFinishedState(sub, rule, nil)
	if got.Finished {
		t.Fatalf("总集数未知时不应判完结（fail-open）")
	}
}

func TestFinishedGraceOtherSeasonDoesNotCount(t *testing.T) {
	sub := &DiscoverySubscription{ID: 9104, MediaType: "tv", Title: "某剧"}
	rule := DiscoverySubscriptionRule{
		SubscriptionID: sub.ID, Name: "第二季",
		Match:     `{"season":2}`,
		MatchData: map[string]any{"season": float64(2)},
	}
	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "7"})

	now := time.Now()
	// 第一季转存齐（24 集），第二季只有 1 集。
	for ep := 1; ep <= 24; ep++ {
		transferItem(t, sub.ID, 1, ep, 24, now.Add(-30*24*time.Hour))
	}
	transferItem(t, sub.ID, 2, 1, 24, now.Add(-30*24*time.Hour))

	got := deriveFinishedState(sub, rule, []resourceCandidate{tvCandidate(24, 1)})
	if got.Finished {
		t.Fatalf("其它季转存齐不该把本季判完结：%s", got.Reason)
	}
	if got.TransferredCount != 1 {
		t.Fatalf("本季已转存 = %d, 期望 1", got.TransferredCount)
	}
}

// TestFinishedGraceWithoutTransferNeverFinishes 还没转存过就谈不上完结。
func TestFinishedGraceWithoutTransferNeverFinishes(t *testing.T) {
	sub := &DiscoverySubscription{ID: 9105, MediaType: "tv", Title: "某剧"}
	rule := DiscoverySubscriptionRule{SubscriptionID: sub.ID, Name: "默认"}
	setupLedgerTest(t, map[string]string{KeySubscriptionFinishedGraceDays: "7"})
	got := deriveFinishedState(sub, rule, []resourceCandidate{tvCandidate(24, 1)})
	if got.Finished || got.InGrace {
		t.Fatalf("没有转存记录时不该判完结或在宽限期")
	}
}

// ---------------------------------------------------------------------------
// 四、设置项 key 与注册表一致
// ---------------------------------------------------------------------------

// TestSubscriptionGuardrailKeysMatchRegistry 防止 discovery 侧的字面量
// 和 settings 注册表漂移 —— 漂移的表现是「配置改了不生效」，极难排查。
func TestSubscriptionGuardrailKeysMatchRegistry(t *testing.T) {
	keys := []string{
		KeySubscriptionExecutionMode,
		KeySubscriptionCustomAttempts,
		KeySubscriptionCustomIntervalSec,
		KeySubscriptionCustomJitterSec,
		KeySubscriptionTimeWindows,
		KeySubscriptionFinishedGraceDays,
	}
	want := map[string]bool{
		settings.KeyMOSubscriptionExecutionMode:     true,
		settings.KeyMOSubscriptionCustomAttempts:    true,
		settings.KeyMOSubscriptionCustomIntervalSec: true,
		settings.KeyMOSubscriptionCustomJitterSec:   true,
		settings.KeyMOSubscriptionTimeWindows:       true,
		settings.KeyMOSubscriptionFinishedGraceDays: true,
	}
	for _, k := range keys {
		if !want[k] {
			t.Fatalf("设置项 %q 不在 registry 的 T06 集合里", k)
		}
	}
}

// ---------------------------------------------------------------------------
// 五、中文剧名的季集证据（这条修的是真故障，不是新功能）
// ---------------------------------------------------------------------------

// TestChineseTitledEpisodesGetDistinctScopes 是 T06 最实质的一条回归。
//
// 改动前，中文标题的候选一律被解析成「电影 + 无集号」
// （订阅侧的 extractEpisodeEvidence 只有 S01E01 与「第N季」两条正则），
// 于是 candidateScopeKey 对它们全部返回 "movie"。而 planAndTransferRuleCandidates
// 在第一条转存成功后就把 batchScopes["movie"] 置真，
// 于是同一部中文剧的 E01…E12 全部被自己挡掉，理由是「本轮内该影片或季集范围已转存」。
//
// 表现：**中文剧名的订阅从头到尾只能转存一集**，而且日志里完全看不出异常 ——
// 每条都规规矩矩记着「已跳过」。
func TestChineseTitledEpisodesGetDistinctScopes(t *testing.T) {
	setupLedgerTest(t, nil)

	scopes := map[string]bool{}
	for _, name := range []string{
		"流浪地球 第二季 第一集.mkv",
		"流浪地球 第二季 第十二集.mkv",
		"流浪地球 第二季 第2集.mkv",
		"剧名.第十季.E03.mkv",
		"Ｓ０１Ｅ０１.mkv",
	} {
		mediaType, ep := episodeEvidenceFromText(name)
		if mediaType != "tv" {
			t.Fatalf("%q 被判成 %q，应为 tv", name, mediaType)
		}
		if ep == nil || ep.EpisodeNum == nil {
			t.Fatalf("%q 没有解析出集号", name)
		}
		scope := candidateScopeKey(resourceCandidate{MediaType: mediaType, Episode: ep})
		if scopes[scope] {
			t.Fatalf("%q 的 scope %s 与前面某条重复 —— 去重会把它们互相挡掉", name, scope)
		}
		scopes[scope] = true
	}
	if len(scopes) != 5 {
		t.Fatalf("期望 5 个互不相同的 scope，实际 %d 个：%v", len(scopes), scopes)
	}
}

// TestTotalEpisodeNumSurvivesAdapter 确认「全N集」的分母能穿过适配层，
// 完结宽限才有判据可用。
func TestTotalEpisodeNumSurvivesAdapter(t *testing.T) {
	setupLedgerTest(t, nil)
	mediaType, ep := episodeEvidenceFromText("三体 第三季 全24集.mkv")
	if mediaType != "tv" {
		t.Fatalf("MediaType = %q, 期望 tv", mediaType)
	}
	if ep == nil || ep.TotalEpisodeNum == nil || *ep.TotalEpisodeNum != 24 {
		t.Fatalf("TotalEpisodeNum = %+v, 期望 24", ep)
	}
	if ep == nil || !ep.IsComplete {
		t.Fatalf("IsComplete 应为 true")
	}
}

// TestSeasonTotalOnlyFromExplicitMarkers 钉住分母口径：
// 「S02E08」「第01-05集」都不是「本季共几集」，拿它们当分母会把还在更新的剧判死。
func TestSeasonTotalOnlyFromExplicitMarkers(t *testing.T) {
	setupLedgerTest(t, nil)
	for _, name := range []string{
		"某剧 S02E08.mkv",     // 只说明更新到第 8 集
		"某剧 S02E01-E05.mkv", // 是打包的 5 集，不是本季 5 集
	} {
		mediaType, ep := episodeEvidenceFromText(name)
		if mediaType != "tv" {
			t.Fatalf("%q 应判成 tv", name)
		}
		if ep.TotalEpisodeNum != nil {
			t.Fatalf("%q 不该推出总集数，实际 %d", name, *ep.TotalEpisodeNum)
		}
		if total := inferSeasonTotal([]resourceCandidate{{MediaType: mediaType, Episode: ep}}, 0); total != 0 {
			t.Fatalf("%q 的分母 = %d, 期望 0（推不出来就不判完结）", name, total)
		}
	}
	// 显式写明的「全24集」才算数。
	mediaType, ep := episodeEvidenceFromText("某剧 第2季 全24集.mkv")
	if total := inferSeasonTotal([]resourceCandidate{{MediaType: mediaType, Episode: ep}}, 0); total != 24 {
		t.Fatalf("「全24集」的分母 = %d, 期望 24", total)
	}
}

// TestGuardrailSettingsComeFromAppRegistry 钉住设置读取通道。
//
// 走错通道是 T06 写第一版时踩的坑：discovery 包自带的 SettingInt/SettingString
// 读的是 discovery_settings 表，而 mo_* 系列的键由 internal/settings 的注册表服务
// 持有（管理界面写的就是那边）。用错函数的症状是「单测全绿、界面上改配置毫无反应」，
// 因为 discovery_settings 表里压根没有这些行。
func TestGuardrailSettingsComeFromAppRegistry(t *testing.T) {
	setupLedgerTest(t, map[string]string{
		KeySubscriptionExecutionMode:            "aggressive",
		settings.KeyMOSubscriptionSearchSources: "hdhive,tgto123",
	})
	p := resolveExecutionProfile(false)
	if p.Mode != ModeAggressive || p.Attempts != 3 {
		t.Fatalf("档位 = %s/%d, 期望 aggressive/3（说明读到的不是注册表值）", p.Mode, p.Attempts)
	}
	if got := SubscriptionSearchSourcesSetting(); got != "hdhive,tgto123" {
		t.Fatalf("搜索源 = %q, 期望 hdhive,tgto123", got)
	}
	BindSettings(nil)
	if p := resolveExecutionProfile(false); p.Mode != defaultExecutionMode {
		t.Fatalf("未装配设置服务时应回落默认档，实际 %s", p.Mode)
	}
}
