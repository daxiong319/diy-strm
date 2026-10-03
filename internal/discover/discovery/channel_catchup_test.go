package discovery

import (
	"testing"
	"time"

	"litepan/internal/discover/dmodels"
	"litepan/internal/discover/tgchannel"
)

// ---------------------------------------------------------------------------
// 停机追赶（_limit_catchup）回归测试
//
// 这段逻辑是状态机，且失手代价极高：判错「已追平」会让积压永久丢失或无限回补。
// 故对每个分支都单独断言，尤其是 advanceChannelCatchup 的「起点整体替换」语义。
// ---------------------------------------------------------------------------

// postsWithIDs 构造按「新到旧」排列的帖子列表（与真实抓取顺序一致）。
func postsWithIDs(ids ...string) []tgchannel.ChannelPost {
	out := make([]tgchannel.ChannelPost, 0, len(ids))
	for _, id := range ids {
		out = append(out, tgchannel.ChannelPost{PostID: id})
	}
	return out
}

// setCatchupHours 覆盖追赶窗口配置，测试结束恢复。
func setCatchupHours(t *testing.T, hours int) {
	t.Helper()
	dmodels.SetChannelCatchupHoursForTest(hours)
	t.Cleanup(func() { dmodels.SetChannelCatchupHoursForTest(-1) })
}

// advanceFor 是 advanceChannelCatchup 的测试包装：顺带断言它没把快照写坏。
func (ch *DiscoveryChannel) advanceFor(t *testing.T, posts []tgchannel.ChannelPost) {
	t.Helper()
	advanceChannelCatchup(ch, posts)
}

// TestPlanCatchupSkipsOnlyAfterLongDowntime 覆盖追赶判定的全部前置条件。
func TestPlanCatchupSkipsOnlyAfterLongDowntime(t *testing.T) {
	setCatchupHours(t, 12)

	cases := []struct {
		name     string
		lastPost string
		lastRun  time.Time
		wantJump bool
	}{
		{"停机 1 小时：不跳", "1000", time.Now().Add(-1 * time.Hour), false},
		{"停机 11 小时：不跳", "1000", time.Now().Add(-11 * time.Hour), false},
		{"停机 13 小时：跳", "1000", time.Now().Add(-13 * time.Hour), true},
		{"停机 100 小时：跳", "1000", time.Now().Add(-100 * time.Hour), true},
		// 首启（无游标）：解析器天然只抓最新一页，不需要跳
		{"无游标（首启）：不跳", "", time.Now().Add(-100 * time.Hour), false},
		// LastRunAt 零值不是「停机」——历史数据/新频道尚未跑过一轮
		{"LastRunAt 零值：不跳", "1000", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &DiscoveryChannel{Channel: "testchan", LastPostID: tc.lastPost, LastRunAt: tc.lastRun}
			st := planChannelCatchup(ch, nil, 0)
			if st.Jump != tc.wantJump {
				t.Fatalf("Jump = %v, want %v", st.Jump, tc.wantJump)
			}
		})
	}
}

// TestPlanCatchupZeroHoursNeverJumps 验证 0 = 不限（原行为）。
func TestPlanCatchupZeroHoursNeverJumps(t *testing.T) {
	setCatchupHours(t, 0)
	ch := &DiscoveryChannel{Channel: "testchan", LastPostID: "1000", LastRunAt: time.Now().Add(-1000 * time.Hour)}
	if st := planChannelCatchup(ch, nil, 0); st.Jump {
		t.Fatal("追赶窗口为 0（不限）时不应跳过积压")
	}
}

// TestPlanCatchupFreezesStartOnlyForBackfillSubs 验证只有当存在回溯订阅时才冻结旧游标。
//
// 纯增量订阅按语义本就该丢弃旧窗口；冻结起点是给「还想要历史」的回溯订阅用的。
func TestPlanCatchupFreezesStartOnlyForBackfillSubs(t *testing.T) {
	setCatchupHours(t, 12)
	ch := &DiscoveryChannel{Channel: "testchan", LastPostID: "1000", LastRunAt: time.Now().Add(-13 * time.Hour)}

	// 注意：subPrefBool 走 subPrefValue 读的是 sub.Pref（已解析的 map），
	// 不是 Preferences 原始 JSON 串 —— 只填 Preferences 是读不到的（Pref 为 nil 直接返回默认值）。
	incremental := DiscoverySubscription{ID: 1, Pref: map[string]any{}}
	st := planChannelCatchup(ch, []DiscoverySubscription{incremental}, 0)
	if len(st.Checkpoints) != 0 {
		t.Fatalf("无回溯订阅时不应冻结起点，得到 %v", st.Checkpoints)
	}

	backfill := DiscoverySubscription{ID: 2, Pref: map[string]any{"backfill": true}}
	st = planChannelCatchup(ch, []DiscoverySubscription{incremental, backfill}, 0)
	if len(st.Checkpoints) != 1 || st.Checkpoints[0] != "1000" {
		t.Fatalf("有回溯订阅时应冻结旧游标 1000，得到 %v", st.Checkpoints)
	}
}

// TestCatchupStopIDIdleUsesLastPostID 空闲态（无快照）必须退化为原增量语义。
func TestCatchupStopIDIdleUsesLastPostID(t *testing.T) {
	ch := &DiscoveryChannel{LastPostID: "1000"}
	stop, done := catchupStopID(ch, nil, []string{"2000", "1900"})
	if stop != "1000" {
		t.Fatalf("空闲态 stopID = %q, want 1000", stop)
	}
	if done {
		t.Fatal("空闲态不应判为追平")
	}
}

// TestCatchupStopIDCatchingUpBoundsToOldestOfPage 追赶态必须以最新一页的最旧帖为界。
//
// 这是「每轮只回补一窗」的实现方式：边界钉在本页最旧帖，抓取天然只覆盖最新一页，
// 中间积压不会被一次性补完。
func TestCatchupStopIDCatchingUpBoundsToOldestOfPage(t *testing.T) {
	ch := &DiscoveryChannel{LastPostID: "5000"}
	stop, done := catchupStopID(ch, []string{"1000"}, []string{"5000", "4900", "4800"})
	if done {
		t.Fatal("起点 1000 远旧于本页最旧帖 4800，不应判为追平")
	}
	if stop != "4800" {
		t.Fatalf("追赶态 stopID = %q, want 4800（最新一页最旧帖）", stop)
	}
}

// TestCatchupStopIDDoneWhenStartReachedPage 起点已进入最新一页范围即追平。
func TestCatchupStopIDDoneWhenStartReachedPage(t *testing.T) {
	ch := &DiscoveryChannel{LastPostID: "5000"}
	// 起点 4850 落在 [4800, 5000] 内 → 积压已回补至最新
	stop, done := catchupStopID(ch, []string{"4850"}, []string{"5000", "4900", "4800"})
	if !done {
		t.Fatal("起点已在本页范围内，应判为追平")
	}
	if stop != "5000" {
		t.Fatalf("追平后 stopID 应回落到游标 5000，得到 %q", stop)
	}
}

// TestCatchupStopIDNoPostsKeepsCatchingUp 未抓到帖（频道无更新）时不能误判追平。
func TestCatchupStopIDNoPostsKeepsCatchingUp(t *testing.T) {
	ch := &DiscoveryChannel{LastPostID: "5000"}
	stop, done := catchupStopID(ch, []string{"1000"}, nil)
	if done {
		t.Fatal("未抓到任何帖时不应判为追平（否则起点会被清空、积压永久丢失）")
	}
	if stop != "1000" {
		t.Fatalf("stopID 应沿用起点 1000，得到 %q", stop)
	}
}

// TestAdvanceCatchupReplacesStartWithOldestScanned 本段最关键的语义。
//
// 起点必须「整体替换」为本轮最旧帖，而不是与旧起点合并：旧起点（1000）本身不比当前游标旧，
// mergeCheckpoints 裁不掉它，保留会让下一轮又从 1000 重新回补，追赶永远推进不了。
func TestAdvanceCatchupReplacesStartWithOldestScanned(t *testing.T) {
	ch := &DiscoveryChannel{
		LastPostID:         "5000",
		CatchupCheckpoints: formatChannelCheckpoints([]string{"1000"}),
	}
	// 本轮扫过 4800..5000 这一窗
	ch.advanceFor(t, postsWithIDs("5000", "4900", "4800"))

	cps := parseChannelCheckpoints(ch.CatchupCheckpoints)
	if len(cps) != 1 {
		t.Fatalf("应只剩一个起点，得到 %v", cps)
	}
	if cps[0] != "4800" {
		t.Fatalf("起点应整体替换为本轮最旧帖 4800，得到 %q", cps[0])
	}
}

// TestAdvanceCatchupClearsWhenCaughtUp 追平后必须清空快照与 NextCatchupAt。
func TestAdvanceCatchupClearsWhenCaughtUp(t *testing.T) {
	ch := &DiscoveryChannel{
		LastPostID:         "5000",
		CatchupCheckpoints: formatChannelCheckpoints([]string{"4850"}),
		NextCatchupAt:      time.Now(),
	}
	ch.advanceFor(t, postsWithIDs("5000", "4900", "4800"))

	if ch.CatchupCheckpoints != "" {
		t.Fatalf("追平后应清空快照，得到 %q", ch.CatchupCheckpoints)
	}
	if !ch.NextCatchupAt.IsZero() {
		t.Fatal("追平后应清空 NextCatchupAt")
	}
}

// TestMergeCheckpointsTrimsAlreadyProcessed 合并去重 + 裁剪已被游标越过的起点。
//
// 裁剪方向容易搞反，务必按设计的两个不变量理解：
//   - 起点比游标【旧】= 尚未回补的积压 → 必须保留（这正是追赶的对象）
//   - 起点不比游标旧（>= 游标）= 已落在游标已处理区 → 裁掉，再补就是重复
func TestMergeCheckpointsTrimsAlreadyProcessed(t *testing.T) {
	// 全部起点都比游标 4000 旧 → 全部保留
	got := mergeCheckpoints([]string{"1000", "3000"}, "2000", "4000")
	if len(got) != 3 {
		t.Fatalf("全部起点都比游标旧，应全保留，得到 %v", got)
	}
	// 排序：新到旧
	if got[0] != "3000" || got[1] != "2000" || got[2] != "1000" {
		t.Fatalf("应按新到旧排序，得到 %v", got)
	}

	// 去重：add 已在 existing 中时只留一份
	got = mergeCheckpoints([]string{"1000", "1500"}, "1500", "1800")
	if len(got) != 2 || got[0] != "1500" || got[1] != "1000" {
		t.Fatalf("去重+降序失败：%v", got)
	}

	// 1900 比游标 1800 新 = 已被游标越过，应裁掉；1000 比游标旧 → 保留
	got = mergeCheckpoints([]string{"1000"}, "1900", "1800")
	if len(got) != 1 || got[0] != "1000" {
		t.Fatalf("应裁掉不比游标旧的起点：%v", got)
	}

	got = mergeCheckpoints([]string{"1000", "3000"}, "2000", "2500")
	// 3000 不比 2500 旧 → 裁掉；保留 1000 / 2000
	if len(got) != 2 {
		t.Fatalf("应保留 2 个起点，得到 %v", got)
	}
	if got[0] != "2000" || got[1] != "1000" {
		t.Fatalf("应按新到旧排序，得到 %v", got)
	}
}

// TestParseChannelCheckpointsToleratesGarbage 脏数据不得阻断订阅轮询。
func TestParseChannelCheckpointsToleratesGarbage(t *testing.T) {
	if got := parseChannelCheckpoints("not-json"); len(got) != 0 {
		t.Fatalf("脏数据应返回空，得到 %v", got)
	}
	if got := parseChannelCheckpoints(""); len(got) != 0 {
		t.Fatalf("空串应返回空，得到 %v", got)
	}
	// 合法 JSON 中的空串成员要被丢弃
	if got := parseChannelCheckpoints(`["1000","","  ","2000"]`); len(got) != 2 {
		t.Fatalf("应丢弃空成员，得到 %v", got)
	}
}
