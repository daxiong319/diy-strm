package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"litepan/internal/discover/ddb"
	"litepan/internal/settings"
)

// 本文件锁死 T04 的行为变更：从「永久去重」改成「以媒体库为真值 + 3 小时保护期」。
//
// 最关键的两条语义相反的用例：
//
//	TestLedgerDoesNotBlockAfterUserDeletes —— 用户删掉已转存的文件，下一轮**必须**能重转
//	                                         （旧行为会永久挡住，这正是本次要修的缺陷）
//	TestLedgerBlocksWithinProtectWindow    —— 刚转存完的 3 小时内**不**重复转存
//
// 与 identity 包 / identity_gate_test.go 的分工：那两个文件测「是不是要这部作品」，
// 这里测「是不是已经转过了」。

func setupLedgerTest(t *testing.T, overrides map[string]string) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "ledger_test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	for _, table := range []string{
		"discovery_transfer_items",
		"discovery_subscription_items",
		"discovery_subscriptions",
		"discovery_subscription_rules",
	} {
		if err := ddb.Db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Fatalf("清理表 %s 失败：%v", table, err)
		}
	}
	if err := ddb.Db.AutoMigrate(&DiscoveryTransferItem{}, &DiscoverySubscriptionItem{},
		&DiscoverySubscription{}, &DiscoverySubscriptionRule{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}

	values := map[string]string{}
	for k, v := range overrides {
		values[k] = v
	}
	svc, err := settings.New(context.Background(), &identitySettingsRepo{values: values})
	if err != nil {
		t.Fatalf("构造设置服务失败：%v", err)
	}
	BindSettings(svc)
	ResetMediaLibraryCache()
	setMediaLibraryProbe(fakeLibrary)
	t.Cleanup(func() {
		BindSettings(nil)
		ResetMediaLibraryCache()
		setMediaLibraryProbe(nil)
	})
}

func ledgerTestSub() *DiscoverySubscription {
	return &DiscoverySubscription{
		ID: 555, TMDBID: 1396, MediaType: "tv",
		Title: "Breaking Bad", TargetProvider: "123",
	}
}

func ledgerTestRule() DiscoverySubscriptionRule {
	return DiscoverySubscriptionRule{ID: 9, TargetProvider: "123"}
}

func ledgerTestCand(slug string) resourceCandidate {
	s1, e1 := 1, 3
	return resourceCandidate{
		Source: "re0", Provider: "123", LinkType: "share",
		Title: "Breaking Bad S01E03", Slug: slug, MediaType: "tv",
		Episode: &candidateEpisode{SeasonNum: &s1, EpisodeNum: &e1},
	}
}

// fakeLibrary 默认：媒体库里什么都没有（Available=true，Present=false）。
func fakeLibrary(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
	return MediaLibraryHit{Available: true}
}

func setMediaLibraryProbe(fn func(*DiscoverySubscription, string, resourceCandidate) MediaLibraryHit) {
	mediaLibraryProbeMu.Lock()
	defer mediaLibraryProbeMu.Unlock()
	if fn == nil {
		MediaLibraryProbe = probeMediaLibraryFromEmby
		return
	}
	MediaLibraryProbe = fn
}

var mediaLibraryProbeMu sync.Mutex

// ---------------------------------------------------------------------------
// 保护期
// ---------------------------------------------------------------------------

// TestLedgerBlocksWithinProtectWindow 验收 3：刚转存完，3 小时内不再动。
func TestLedgerBlocksWithinProtectWindow(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()
	cand := ledgerTestCand("slug-a")

	first := ClaimTransferSlot(sub, rule, cand)
	if !first.Claimed {
		t.Fatalf("首次应抢到占位，实际 reason=%s", first.ReasonCode)
	}
	if got := transferProtectHours(); got != 3*time.Hour {
		t.Fatalf("默认保护期应为 3 小时，实际 %v", got)
	}

	second := ClaimTransferSlot(sub, rule, cand)
	if second.Claimed {
		t.Fatalf("保护期内不应再抢到占位（会重复转存）")
	}
	if second.ReasonCode != ReasonCodeProtectedWindow {
		t.Fatalf("期望 %s，实际 %s", ReasonCodeProtectedWindow, second.ReasonCode)
	}
	if !second.ProtectedUntil.After(time.Now()) {
		t.Fatalf("保护期截止时间应在未来，实际 %v", second.ProtectedUntil)
	}
}

// TestLedgerAllowsRerunAfterUserDeletes 验收 2：**本次改动的核心语义**。
//
// 用户在网盘删掉一部已转存的作品，下一轮必须能重新拉回来。
// 旧行为是 transferredScopes 永久去重，删了也拉不回来 —— 这就是要修的缺陷。
// 模拟方式：把账本条目的保护期起算时间改到过去（等价于「很久以前转的」）。
func TestLedgerAllowsRerunAfterUserDeletes(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()
	cand := ledgerTestCand("slug-b")

	if !ClaimTransferSlot(sub, rule, cand).Claimed {
		t.Fatalf("首次应抢到占位")
	}
	key, _, _ := LedgerEntryFor(sub, rule, cand)
	if _, ok := FindLedgerEntry(key); !ok {
		t.Fatalf("账本条目未落库")
	}

	// 用户删除 → 模拟「这条记录已经是 4 小时前的」，出了 3 小时保护期。
	past := time.Now().Add(-4 * time.Hour)
	if err := ddb.Db.Model(&DiscoveryTransferItem{}).
		Where("idempotency_key = ?", key).
		Update("transfer_requested_at", past).Error; err != nil {
		t.Fatalf("改写时间失败：%v", err)
	}

	again := ClaimTransferSlot(sub, rule, cand)
	if !again.Claimed {
		t.Fatalf("保护期已过（等价于用户删除后重转），应能再次占位，实际 reason=%s", again.ReasonCode)
	}
	if again.ID == 0 {
		t.Fatalf("重占时应复用同一条账本条目（幂等键不变）")
	}
	if got := ledgerRowCount(); got != 1 {
		t.Fatalf("幂等键不变，不应新增条目，实际 %d 条", got)
	}
}

// TestLedgerProtectWindowZero 验收 4：保护期设为 0 时立刻可重转。
func TestLedgerProtectWindowZero(t *testing.T) {
	setupLedgerTest(t, map[string]string{settings.KeyMOSubscriptionTransferProtectHours: "0"})
	if got := transferProtectHours(); got != 0 {
		t.Fatalf("保护期应为 0，实际 %v", got)
	}
	sub, rule := ledgerTestSub(), ledgerTestRule()
	cand := ledgerTestCand("slug-c")

	if !ClaimTransferSlot(sub, rule, cand).Claimed {
		t.Fatalf("首次应抢到")
	}
	if second := ClaimTransferSlot(sub, rule, cand); !second.Claimed {
		t.Fatalf("保护期为 0 时应立刻可重转，实际 reason=%s", second.ReasonCode)
	}
}

// TestLedgerFailedEntryRetries 转存失败必须能重试，不能被保护期白挡一轮。
func TestLedgerFailedEntryRetries(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()
	cand := ledgerTestCand("slug-f")

	if !ClaimTransferSlot(sub, rule, cand).Claimed {
		t.Fatalf("首次应抢到占位")
	}
	key, _, _ := LedgerEntryFor(sub, rule, cand)
	entry, ok := FindLedgerEntry(key)
	if !ok {
		t.Fatalf("账本条目未落库")
	}
	FailLedgerEntry(entry, "TRANSFER_FAILED")

	if !ClaimTransferSlot(sub, rule, cand).Claimed {
		t.Fatalf("失败条目应立即可重试")
	}
}

// TestLedgerProtectHoursClamp 保护期越界要收口，不能是负数或超大值。
func TestLedgerProtectHoursClamp(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"0", 0},
		{"3", 3 * time.Hour},
		{"24", 24 * time.Hour},
		{"-1", 0},
		{"999", 24 * time.Hour},
		{"abc", 3 * time.Hour},
	} {
		setupLedgerTest(t, map[string]string{settings.KeyMOSubscriptionTransferProtectHours: tc.raw})
		if got := transferProtectHours(); got != tc.want {
			t.Fatalf("设置 %q 期望 %v，实际 %v", tc.raw, tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

// TestLedgerConcurrentClaimSingleWinner 验收 5：多路并发抢同一个键，只有一次成功。
//
// 这是「不要先 SELECT 再 INSERT」那条要求的回归测试：
// 旧写法下 N 路都会查到「不在保护期」→ N 路都去转存 → 重复转存 N 次。
// 现在唯一索引是唯一裁决者，所以结果恒定为一胜。
func TestLedgerConcurrentClaimSingleWinner(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()
	cand := ledgerTestCand("slug-race")

	const goroutines = 16
	var wg sync.WaitGroup
	results := make([]bool, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // 放大竞态窗口
			results[idx] = ClaimTransferSlot(sub, rule, cand).Claimed
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发抢占应有且仅有 1 个赢家，实际 %d 个（会造成重复转存）", winners)
	}
	if got := ledgerRowCount(); got != 1 {
		t.Fatalf("账本只应落 1 条，实际 %d 条", got)
	}
}

// TestLedgerConcurrentDifferentContentIndependent 不同内容并发互不干扰。
func TestLedgerConcurrentDifferentContentIndependent(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()

	const n = 12
	var wg sync.WaitGroup
	claimed := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			claimed[idx] = ClaimTransferSlot(sub, rule, ledgerTestCand(fmt.Sprintf("slug-p%d", idx))).Claimed
		}(i)
	}
	wg.Wait()

	for i, ok := range claimed {
		if !ok {
			t.Fatalf("第 %d 条内容应独立占位成功", i)
		}
	}
	if got := ledgerRowCount(); got != n {
		t.Fatalf("账本应有 %d 条，实际 %d 条", n, got)
	}
}

// ---------------------------------------------------------------------------
// 洗版 / 键的构造
// ---------------------------------------------------------------------------

// TestLedgerRereleaseNewSlug 验收 6：洗版换了分享 → 新条目，不被旧条目挡住。
func TestLedgerRereleaseNewSlug(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, rule := ledgerTestSub(), ledgerTestRule()

	if !ClaimTransferSlot(sub, rule, ledgerTestCand("slug-old")).Claimed {
		t.Fatalf("旧版本应占位成功")
	}
	if !ClaimTransferSlot(sub, rule, ledgerTestCand("slug-new")).Claimed {
		t.Fatalf("洗版换了 slug 应作为新条目放行，不被旧条目挡住")
	}
	if got := ledgerRowCount(); got != 2 {
		t.Fatalf("应有 2 条账本记录，实际 %d", got)
	}
}

// TestLedgerContentKeyPrefersSha1 有真实哈希时用文件级键，没有时退化成资源级键。
func TestLedgerContentKeyPrefersSha1(t *testing.T) {
	cases := []struct {
		name       string
		cand       resourceCandidate
		wantPrefix string
		wantSha1   string
	}{
		{"真实 sha1", resourceCandidate{Slug: "s1", Sha1: "da39a3ee5e6b4b0d3255bfef95601890afd80709"}, "sha1:", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{"大写 sha1 归一", resourceCandidate{Slug: "s1", Sha1: "DA39A3EE5E6B4B0D3255BFEF95601890AFD80709"}, "sha1:", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{"非法 sha1 忽略", resourceCandidate{Slug: "s1", Sha1: "not-a-sha1"}, "res:", ""},
		{"长度不对忽略", resourceCandidate{Slug: "s1", Sha1: "abc"}, "res:", ""},
		{"无哈希退化为 slug", resourceCandidate{Slug: "s1"}, "res:", ""},
		{"无 slug 退化为标题", resourceCandidate{Title: "剧名"}, "res:", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, sha1 := ledgerContentKey(tc.cand)
			if len(key) <= len(tc.wantPrefix) || key[:len(tc.wantPrefix)] != tc.wantPrefix {
				t.Fatalf("期望前缀 %s，实际 %s", tc.wantPrefix, key)
			}
			if sha1 != tc.wantSha1 {
				t.Fatalf("期望 sha1 %q，实际 %q", tc.wantSha1, sha1)
			}
		})
	}
}

// TestLedgerIdempotencyKeyCollisionSafe 分隔符不能让不同三元组撞键。
func TestLedgerIdempotencyKeyCollisionSafe(t *testing.T) {
	a := LedgerIdempotencyKey("12|3", "res:x", "tmdb:1")
	b := LedgerIdempotencyKey("12", "3|res:x", "tmdb:1")
	if a == b {
		t.Fatalf("含 | 的三元组撞键了，幂等键不可信")
	}
	if got := LedgerIdempotencyKey("s", "c", "m"); len(got) != 64 {
		t.Fatalf("幂等键应是 64 位 hex，实际 %d 位", len(got))
	}
}

// TestLedgerMediaScope media_scope 的形状要与任务书给的示例一致。
func TestLedgerMediaScope(t *testing.T) {
	sub := &DiscoverySubscription{TMDBID: 1396, MediaType: "tv"}
	s2, e7 := 2, 7
	if got := ledgerMediaScope(sub, resourceCandidate{}); got != "tmdb:1396" {
		t.Fatalf("tv 无季集应得 tmdb:1396，实际 %s", got)
	}
	if got := ledgerMediaScope(sub, resourceCandidate{Episode: &candidateEpisode{SeasonNum: &s2}}); got != "tmdb:1396:s2" {
		t.Fatalf("tv 有季无集应得 tmdb:1396:s2，实际 %s", got)
	}
	if got := ledgerMediaScope(sub, resourceCandidate{Episode: &candidateEpisode{SeasonNum: &s2, EpisodeNum: &e7}}); got != "tmdb:1396:s2e7" {
		t.Fatalf("tv 有季集应得 tmdb:1396:s2e7，实际 %s", got)
	}
	movie := &DiscoverySubscription{TMDBID: 157350, MediaType: "movie"}
	if got := ledgerMediaScope(movie, resourceCandidate{Episode: &candidateEpisode{SeasonNum: &s2}}); got != "tmdb:157350" {
		t.Fatalf("movie 不应带季集，实际 %s", got)
	}
	noTMDB := &DiscoverySubscription{Title: "流浪地球", MediaType: "movie"}
	if got := ledgerMediaScope(noTMDB, resourceCandidate{}); got != "tmdb:?流浪地球" {
		t.Fatalf("无 TMDB ID 时应回退到标题键，实际 %s", got)
	}
}

// ---------------------------------------------------------------------------
// 媒体库真值
// ---------------------------------------------------------------------------

// TestMediaLibraryPresentBlocksTransfer 媒体库里已有 ⇒ 不转存（用户没删）。
func TestMediaLibraryPresentBlocksTransfer(t *testing.T) {
	setupLedgerTest(t, nil)
	setMediaLibraryProbe(func(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
		return MediaLibraryHit{Present: true, Available: true, ReasonCode: ReasonCodePresentInLibrary}
	})

	decision := decideTransfer(ledgerTestSub(), ledgerTestRule(), ledgerTestCand("slug-lib"), map[string]bool{})
	if decision.Transfer {
		t.Fatalf("媒体库里已有，不应转存")
	}
	if decision.Layer != dedupLayerLibrary || decision.ReasonCode != ReasonCodePresentInLibrary {
		t.Fatalf("期望媒体库层判定，实际 layer=%s reason=%s", decision.Layer, decision.ReasonCode)
	}
	// 判为「已有」后账本应推进为 confirmed，否则下一轮又要去查一遍。
	key, _, _ := LedgerEntryFor(ledgerTestSub(), ledgerTestRule(), ledgerTestCand("slug-lib"))
	entry, ok := FindLedgerEntry(key)
	if !ok {
		t.Fatalf("账本条目未落库")
	}
	if entry.State != LedgerStateTransferConfirmed {
		t.Fatalf("期望 state=%s，实际 %s", LedgerStateTransferConfirmed, entry.State)
	}
}

// TestMediaLibraryMissingAllowsTransfer 媒体库里没有 ⇒ 转存（用户删掉了就要拉回来）。
// 这条与旧行为相反，是本次改动的核心。
func TestMediaLibraryMissingAllowsTransfer(t *testing.T) {
	setupLedgerTest(t, nil)
	setMediaLibraryProbe(fakeLibrary)

	decision := decideTransfer(ledgerTestSub(), ledgerTestRule(), ledgerTestCand("slug-gone"), map[string]bool{"S01E03": true})
	if !decision.Transfer {
		t.Fatalf("媒体库里没有、且索引可用时应放行（用户可能已删除），实际 layer=%s reason=%s",
			decision.Layer, decision.ReasonCode)
	}
	if decision.Layer != dedupLayerPass {
		t.Fatalf("期望放行层，实际 %s", decision.Layer)
	}
}

// TestMediaLibraryCache 验收 7：同一 scope 的重复查询只打一次外部源。
func TestMediaLibraryCache(t *testing.T) {
	setupLedgerTest(t, nil)
	var calls int
	var mu sync.Mutex
	setMediaLibraryProbe(func(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
		mu.Lock()
		calls++
		mu.Unlock()
		return MediaLibraryHit{Available: true, Present: false}
	})

	sub := ledgerTestSub()
	cand := ledgerTestCand("slug-cache")
	for i := 0; i < 5; i++ {
		MediaLibraryPresent(sub, cand)
	}
	if calls != 1 {
		t.Fatalf("5 次同 scope 查询应只打 1 次源，实际 %d 次", calls)
	}

	// 不同集号 = 不同缓存槽
	e4 := 4
	other := cand
	other.Episode = &candidateEpisode{SeasonNum: cand.Episode.SeasonNum, EpisodeNum: &e4}
	MediaLibraryPresent(sub, other)
	if calls != 2 {
		t.Fatalf("不同集号应独立查询一次，累计应为 2，实际 %d", calls)
	}

	ResetMediaLibraryCache()
	MediaLibraryPresent(sub, cand)
	if calls != 3 {
		t.Fatalf("清缓存后应重新查询，累计应为 3，实际 %d", calls)
	}
}

// TestMediaLibraryUnavailableNotCached 索引不可用时不写缓存 —— 用户刚配好 Emby
// 应当立刻生效，而不是等 30 分钟。
func TestMediaLibraryUnavailableNotCached(t *testing.T) {
	setupLedgerTest(t, nil)
	var calls int
	setMediaLibraryProbe(func(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
		calls++
		return MediaLibraryHit{Available: false}
	})

	sub := ledgerTestSub()
	cand := ledgerTestCand("slug-un")
	MediaLibraryPresent(sub, cand)
	MediaLibraryPresent(sub, cand)
	if calls != 2 {
		t.Fatalf("索引不可用时不应缓存，期望查 2 次，实际 %d", calls)
	}
}

// TestMediaLibraryBaselineFallbackOnlyWhenUnavailable 第三层兜底只在索引不可用时生效。
func TestMediaLibraryBaselineFallbackOnlyWhenUnavailable(t *testing.T) {
	setupLedgerTest(t, nil)
	sub, cand := ledgerTestSub(), ledgerTestCand("slug-base")
	rule := ledgerTestRule()
	baseline := map[string]bool{"S01E03": true}

	// 索引可用时，基线必须被忽略 —— 否则「用户删掉的文件」又拉不回来了。
	setMediaLibraryProbe(fakeLibrary)
	if d := decideTransfer(sub, rule, cand, baseline); !d.Transfer {
		t.Fatalf("索引可用且媒体库为空时应放行（用户可能已删除），实际 layer=%s", d.Layer)
	}

	// 索引不可用时基线顶上来，避免没开 Emby 的用户每轮都转一遍。
	ResetMediaLibraryCache()
	setMediaLibraryProbe(func(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
		return MediaLibraryHit{Available: false}
	})
	// 换一条候选：上一条已经占过账本，再问会撞上保护期而不是基线。
	d := decideTransfer(sub, rule, ledgerTestCand("slug-base-2"), baseline)
	if d.Transfer {
		t.Fatalf("索引不可用且命中历史基线时应跳过")
	}
	if d.Layer != dedupLayerBaseline || d.ReasonCode != ReasonCodeBaselineHit {
		t.Fatalf("期望基线层判定，实际 layer=%s reason=%s", d.Layer, d.ReasonCode)
	}
}

// TestMediaLibraryBaselineNoHitWhenScopeUnknown scope 认不出来时不拿基线乱挡。
func TestMediaLibraryBaselineNoHitWhenScopeUnknown(t *testing.T) {
	setupLedgerTest(t, nil)
	setMediaLibraryProbe(func(_ *DiscoverySubscription, _ string, _ resourceCandidate) MediaLibraryHit {
		return MediaLibraryHit{Available: false}
	})
	// movie 且无季集 → candidateScopeKey 返回 "movie"，基线里没有它。
	d := decideTransfer(ledgerTestSub(), ledgerTestRule(),
		resourceCandidate{Slug: "slug-x", Title: "流浪地球", LinkType: "share"}, map[string]bool{"S01E01": true})
	if !d.Transfer {
		t.Fatalf("基线未命中应放行，实际 layer=%s", d.Layer)
	}
}

// TestMediaLibraryProbeDefaultReadsEmbyTable 默认实现要真的能查 emby_media_items。
// 这里直接铺一张最小表，验证查询语句与列名对齐（type 是 Emby 原生字符串）。
func TestMediaLibraryProbeDefaultReadsEmbyTable(t *testing.T) {
	setupLedgerTest(t, nil)
	setMediaLibraryProbe(nil) // 回到默认实现

	if err := ddb.Db.Exec(`CREATE TABLE IF NOT EXISTS emby_media_items (
		item_id TEXT, name TEXT, type TEXT, series_name TEXT,
		production_year INTEGER, parent_index_number INTEGER, index_number INTEGER
	)`).Error; err != nil {
		t.Fatalf("铺测试表失败：%v", err)
	}
	ResetMediaLibraryCache()

	sub := &DiscoverySubscription{TMDBID: 1396, MediaType: "tv", Title: "Breaking Bad"}
	s1, e3 := 1, 3
	cand := resourceCandidate{Title: "Breaking Bad S01E03", Episode: &candidateEpisode{SeasonNum: &s1, EpisodeNum: &e3}}

	hit := probeMediaLibraryFromEmby(sub, ledgerMediaScope(sub, cand), cand)
	if !hit.Available {
		t.Fatalf("索引可用（表存在但为空），Available 应为 true，实际 %+v", hit)
	}
	if hit.Present {
		t.Fatalf("空表不应判为已存在")
	}

	if err := ddb.Db.Exec(
		`INSERT INTO emby_media_items (item_id, name, type, series_name, production_year, parent_index_number, index_number)
		 VALUES ('e1','Pilot','Episode','Breaking Bad',2008,1,3)`).Error; err != nil {
		t.Fatalf("插入测试数据失败：%v", err)
	}
	ResetMediaLibraryCache()
	hit = probeMediaLibraryFromEmby(sub, ledgerMediaScope(sub, cand), cand)
	if !hit.Present {
		t.Fatalf("第 1 季第 3 集在库里，应判为已存在，实际 %+v", hit)
	}

	// 第 9 集不在库里 ⇒ 应当转存（这正是「以媒体库为真值」的判定）。
	e9 := 9
	cand9 := resourceCandidate{Title: "Breaking Bad S01E09", Episode: &candidateEpisode{SeasonNum: &s1, EpisodeNum: &e9}}
	hit = probeMediaLibraryFromEmby(sub, ledgerMediaScope(sub, cand9), cand9)
	if hit.Present {
		t.Fatalf("第 9 集不在库里，不应判为已存在")
	}
}

// TestMediaLibraryMissingTableFailsOpen emby_media_items 不存在时判为不可用并放行，
// 不能让没开 Emby 的用户订阅全停。
func TestMediaLibraryMissingTableFailsOpen(t *testing.T) {
	setupLedgerTest(t, nil)
	setMediaLibraryProbe(nil)
	if err := ddb.Db.Exec(`DROP TABLE IF EXISTS emby_media_items`).Error; err != nil {
		t.Fatalf("删表失败：%v", err)
	}
	ResetMediaLibraryCache()

	hit := probeMediaLibraryFromEmby(ledgerTestSub(), "tmdb:1396:s1e3", ledgerTestCand("slug-noTable"))
	if hit.Available {
		t.Fatalf("表不存在时 Available 应为 false，实际 %+v", hit)
	}
	if hit.Present {
		t.Fatalf("表不存在时不得判为已存在")
	}
}

// ---------------------------------------------------------------------------
// 无数据库时的降级
// ---------------------------------------------------------------------------

// TestLedgerDegradesWithoutDB 账本不可用时放行，不能让整个订阅停摆。
func TestLedgerDegradesWithoutDB(t *testing.T) {
	BindSettings(nil)
	saved := ddb.Db
	ddb.Db = nil
	t.Cleanup(func() { ddb.Db = saved })

	claim := ClaimTransferSlot(ledgerTestSub(), ledgerTestRule(), ledgerTestCand("slug-nodb"))
	if !claim.Claimed {
		t.Fatalf("无数据库时应降级放行")
	}
	if claim.ReasonCode != "ledger_unavailable" {
		t.Fatalf("期望 ledger_unavailable，实际 %s", claim.ReasonCode)
	}
}

func ledgerRowCount() int64 {
	var n int64
	if err := ddb.Db.Model(&DiscoveryTransferItem{}).Count(&n).Error; err != nil {
		return -1
	}
	return n
}
