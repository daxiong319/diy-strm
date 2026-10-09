package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"litepan/internal/discover/identity"
)

// 本文件是 T05 Connector 契约的验收：接口形状、归一化、多源合并去重、
// 串行检索的超时与总预算、跨源排序。
//
// 三条核心语义各自钉在这里，因为它们都是「接口化之后行为对不对」的地基：
//   - Merge 先出现的赢（源顺序 = 用户配置的优先级）
//   - 单源超时只掐一个源，总预算耗尽后剩余源**不发起请求**
//   - ItemLess 的层级：显式指定 > 新鲜度 > 清晰度 > 码率

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

type fakeConnector struct {
	key     string
	label   string
	purpose []string
	ready   bool
	items   []Item
	err     error
	delay   time.Duration
	// calls 记录被真正发起过几次 —— 总预算耗尽时必须是 0。
	calls *int
}

func (f *fakeConnector) Key() string   { return f.key }
func (f *fakeConnector) Label() string { return f.label }
func (f *fakeConnector) Purpose() []string {
	if f.purpose == nil {
		return []string{PurposeSubscriptions}
	}
	return f.purpose
}
func (f *fakeConnector) Ready() bool              { return f.ready }
func (f *fakeConnector) NeedsResolve(i Item) bool { return DefaultNeedsResolve(i) }

func (f *fakeConnector) Search(ctx context.Context, q Query) ([]Item, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Item, 0, len(f.items))
	for _, it := range f.items {
		it.SourceKey = f.key
		out = append(out, it)
	}
	return out, nil
}

func (f *fakeConnector) Resolve(ctx context.Context, i Item) (identity.Manifest, error) {
	return MetadataManifest(i), nil
}

// ---------------------------------------------------------------------------
// 接口形状
// ---------------------------------------------------------------------------

// TestConnectorInterfaceSatisfied 编译期断言：两个连接器实现都满足接口。
// 少一个方法会在 T03/T04 之后静默退化（接口太松，谁都能实现），
// 多一个方法则编译不过 —— 两者都在这里被钉住。
func TestConnectorInterfaceSatisfied(t *testing.T) {
	var _ Connector = (*fakeConnector)(nil)
	if got := DefaultNeedsResolve(Item{Kind: ItemShareLink}); got != false {
		t.Fatalf("分享链接不该需要 Resolve，得到 %v", got)
	}
	if got := DefaultNeedsResolve(Item{Kind: ItemMagnet}); got != true {
		t.Fatalf("磁力必须先 Resolve 才能确认身份，得到 %v", got)
	}
	if got := DefaultNeedsResolve(Item{Kind: ItemShareLink, RequiresResolve: true}); got != true {
		t.Fatalf("源要求 Resolve 时必须 Resolve，得到 %v", got)
	}
}

// TestRegistryOrderAndResolve 注册顺序即默认优先级；未就绪的源被跳过并带原因。
func TestRegistryOrderAndResolve(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&fakeConnector{key: "a", ready: true})
	reg.Register(&fakeConnector{key: "b", ready: false})
	reg.Register(&fakeConnector{key: "c", ready: true})
	reg.Register(&fakeConnector{key: "a", ready: true}) // 重复注册应覆盖而非追加

	if got := reg.Keys(); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("注册顺序不对：%v", got)
	}
	ready, skipped := reg.Resolve([]string{"c", "b", "a"})
	if len(ready) != 2 || ready[0].Key() != "c" || ready[1].Key() != "a" {
		t.Fatalf("就绪源顺序应按请求顺序，得到 %v", keysOf(ready))
	}
	if len(skipped) != 1 || skipped[0].Key != "b" || skipped[0].Reason == "" {
		t.Fatalf("未就绪源必须被跳过并带原因，得到 %+v", skipped)
	}
	if _, skipped := reg.Resolve([]string{"nope"}); len(skipped) != 1 || skipped[0].Key != "nope" {
		t.Fatalf("未注册 key 必须被跳过，得到 %+v", skipped)
	}
}

func TestSupports(t *testing.T) {
	c := &fakeConnector{key: "a", purpose: []string{PurposeSubscriptions, PurposeMovie}}
	if !Supports(c, PurposeSubscriptions) || !Supports(c, PurposeMovie) {
		t.Fatalf("应支持订阅与电影场景")
	}
	if Supports(c, PurposeBot) {
		t.Fatalf("不该支持机器人场景")
	}
}

func keysOf(cs []Connector) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Key())
	}
	return out
}

// ---------------------------------------------------------------------------
// 合并去重（验收项 4）
// ---------------------------------------------------------------------------

// TestMergeFirstWinsAndDedups 同一条内容出现在两个源里只能活一个，
// 活的是源顺序在前的那个 —— 用户把哪个源写在前面就等于给它更高优先级。
func TestMergeFirstWinsAndDedups(t *testing.T) {
	// 真实的跨源重复形态：tgto123 与 hdhive 都是 RE0 资源源，同一个资源在两边
	// 的 slug 一致，而 slug 就是它在 RE0 侧的身份（见 resourceFromHive 用 r.Slug）。
	outcomes := []SearchOutcome{
		{Key: "tgto123", Items: []Item{
			{Kind: ItemShareLink, Slug: "slug-a", ShareCode: "slug-a", Title: "A"},
			{Kind: ItemShareLink, Slug: "slug-b", ShareCode: "slug-b", Title: "B"},
		}},
		{Key: "hdhive", Items: []Item{
			// 与 A 同一份内容（slug 相同）→ 应被去掉
			{Kind: ItemShareLink, Slug: "slug-a", ShareCode: "slug-a", Title: "A"},
			{Kind: ItemShareLink, Slug: "slug-d", Title: "D"},
		}},
	}

	merged, dropped := Merge(outcomes)
	if len(merged) != 3 {
		t.Fatalf("应合并出 3 条，实际 %d：%v", len(merged), titlesOf(merged))
	}
	if merged[0].SourceKey != "tgto123" {
		t.Fatalf("重复内容应保留先出现的 tgto123 那条，实际 %s", merged[0].SourceKey)
	}
	if len(dropped) != 1 || dropped[0].Key != "hdhive" || dropped[0].KeptFrom != "tgto123" {
		t.Fatalf("应记录 1 条来自 hdhive 的重复，实际 %+v", dropped)
	}
}

// TestDedupKeyPrefersInfoHash 有真实种子哈希时按哈希去重（同一份内容改了分享码
// 也认得出来），没有哈希才退回资源标识。
func TestDedupKeyPrefersInfoHash(t *testing.T) {
	a := Item{Kind: ItemMagnet, InfoHash: "0123456789ABCDEF0123456789ABCDEF01234567", Slug: "s1"}
	b := Item{Kind: ItemMagnet, InfoHash: "0123456789abcdef0123456789abcdef01234567", Slug: "s2"}
	if DedupKey(a) != DedupKey(b) {
		t.Fatalf("大小写不同的 info hash 应算出同一个去重键：%s vs %s", DedupKey(a), DedupKey(b))
	}
	if DedupKey(a) == DedupKey(Item{Kind: ItemShareLink, Slug: "s1"}) {
		t.Fatalf("磁力与分享的标识空间不该撞键")
	}
	if got := DedupKey(Item{Kind: ItemShareLink, Slug: "s1"}); got != "res:s1" {
		t.Fatalf("分享候选的去重键应为 res:<slug>，实际 %s", got)
	}
}

// TestMergeSkipsErroredOutcomes 单源报错不能影响其它源的结果。
func TestMergeSkipsErroredOutcomes(t *testing.T) {
	outcomes := []SearchOutcome{
		{Key: "bad", Err: errors.New("boom")},
		{Key: "good", Items: []Item{{Kind: ItemShareLink, Slug: "s1", Title: "A"}}},
	}
	merged, _ := Merge(outcomes)
	if len(merged) != 1 || merged[0].SourceKey != "good" {
		t.Fatalf("报错源不应影响其它源，实际 %v", titlesOf(merged))
	}
}

// ---------------------------------------------------------------------------
// 超时与总预算（验收项 5）
// ---------------------------------------------------------------------------

// TestSearchAllSingleSourceTimeout 单源超时只掐掉那一个源，其余照常返回。
func TestSearchAllSingleSourceTimeout(t *testing.T) {
	slow := &fakeConnector{key: "slow", ready: true, delay: 2 * time.Second,
		items: []Item{{Kind: ItemShareLink, Slug: "s1"}}}
	fast := &fakeConnector{key: "fast", ready: true,
		items: []Item{{Kind: ItemShareLink, Slug: "s2"}}}

	outcomes := SearchAll(context.Background(), []Connector{slow, fast},
		Query{TMDBID: "1"}, 50*time.Millisecond, 5*time.Second)

	if len(outcomes) != 2 {
		t.Fatalf("应有两个源的结果，实际 %d", len(outcomes))
	}
	if outcomes[0].Err == nil {
		t.Fatalf("慢源应报超时错误")
	}
	if outcomes[1].Err != nil || len(outcomes[1].Items) != 1 {
		t.Fatalf("快源应正常返回，实际 err=%v items=%d", outcomes[1].Err, len(outcomes[1].Items))
	}
}

// TestSearchAllBudgetSkipsRemainingSources 总预算耗尽后，剩余的源
// **一次请求都不发**。这条要钉的是 calls==0：若实现是先调后判，
// 预算就是摆设 —— 省下的是等待时间，拦不住的是请求数。
func TestSearchAllBudgetSkipsRemainingSources(t *testing.T) {
	firstCalls, laterCalls := 0, 0
	// 预算 80ms、单源超时 10s ⇒ 第一个源被收口到 80ms（remaining < perSourceTimeout）。
	// 它赖着不退，正好把预算吃干，于是第二个源不该再被发起。
	first := &fakeConnector{key: "first", ready: true, delay: time.Second,
		items: []Item{{Kind: ItemShareLink, Slug: "s1"}}, calls: &firstCalls}
	later := &fakeConnector{key: "later", ready: true,
		items: []Item{{Kind: ItemShareLink, Slug: "s2"}}, calls: &laterCalls}

	outcomes := SearchAll(context.Background(), []Connector{first, later},
		Query{TMDBID: "1"}, 10*time.Second, 80*time.Millisecond)

	if firstCalls != 1 {
		t.Fatalf("第一个源应被调用一次，实际 %d", firstCalls)
	}
	if laterCalls != 0 {
		t.Fatalf("预算耗尽后第二个源一次都不该被调用，实际调用了 %d 次", laterCalls)
	}
	if !outcomes[1].Skipped || !errors.Is(outcomes[1].Err, ErrBudgetExhausted) {
		t.Fatalf("第二个源应标记为跳过+预算耗尽，实际 skipped=%v err=%v",
			outcomes[1].Skipped, outcomes[1].Err)
	}
	// 第一个源被预算掐断（拿不到候选），第二个源被跳过 ⇒ 这轮确实一条都没有。
	merged, _ := Merge(outcomes)
	if len(merged) != 0 {
		t.Fatalf("预算耗尽时应拿不到候选，实际 %d 条", len(merged))
	}
}

// TestSearchAllBudgetAllowsAllSourcesWhenSufficient 预算充裕时所有源都要跑，
// 防止上一条测试的收口逻辑写过头、把正常场景也掐了。
func TestSearchAllBudgetAllowsAllSourcesWhenSufficient(t *testing.T) {
	a := &fakeConnector{key: "a", ready: true, items: []Item{{Kind: ItemShareLink, Slug: "s1", Title: "A"}}}
	b := &fakeConnector{key: "b", ready: true, items: []Item{{Kind: ItemShareLink, Slug: "s2", Title: "B"}}}
	outcomes := SearchAll(context.Background(), []Connector{a, b}, Query{TMDBID: "1"},
		time.Second, 30*time.Second)
	if len(outcomes) != 2 || outcomes[0].Skipped || outcomes[1].Skipped {
		t.Fatalf("预算充裕时不应跳过任何源：%+v", outcomes)
	}
	merged, _ := Merge(outcomes)
	if len(merged) != 2 {
		t.Fatalf("应拿到两个源的结果，实际 %d 条", len(merged))
	}
}

// TestSearchAllIsSequential 检索必须串行：候选顺序要稳定，验收项 1
// 要求「默认单源场景结果与改动前一致」，并发返回的顺序是没法保证的。
// 这里用两个互相记录入场的源证明第二个在第一个退出前没有开始。
func TestSearchAllIsSequential(t *testing.T) {
	var order []string
	c1 := &sequentialRecorder{key: "one", order: &order}
	c2 := &sequentialRecorder{key: "two", order: &order}

	SearchAll(context.Background(), []Connector{c1, c2}, Query{TMDBID: "1"},
		2*time.Second, 5*time.Second)

	if len(order) != 4 || order[0] != "one:start" || order[1] != "one:end" ||
		order[2] != "two:start" || order[3] != "two:end" {
		t.Fatalf("应严格串行 enter/end/enter/end，实际 %v", order)
	}
}

type sequentialRecorder struct {
	key   string
	order *[]string
}

func (s *sequentialRecorder) Key() string   { return s.key }
func (s *sequentialRecorder) Label() string { return s.key }
func (s *sequentialRecorder) Purpose() []string {
	return []string{PurposeSubscriptions}
}
func (s *sequentialRecorder) Ready() bool              { return true }
func (s *sequentialRecorder) NeedsResolve(i Item) bool { return false }
func (s *sequentialRecorder) Search(ctx context.Context, q Query) ([]Item, error) {
	*s.order = append(*s.order, s.key+":start")
	defer func() { *s.order = append(*s.order, s.key+":end") }()
	return nil, nil
}
func (s *sequentialRecorder) Resolve(ctx context.Context, i Item) (identity.Manifest, error) {
	return MetadataManifest(i), nil
}

// ---------------------------------------------------------------------------
// 跨源排序（任务书要求的四个维度）
// ---------------------------------------------------------------------------

// TestItemLessPrecedence 四层权重：显式指定 > 新鲜度 > 清晰度 > 码率。
// 用「只差一个维度」的构造逐层验证，避免权重叠加互相掩盖。
func TestItemLessPrecedence(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name          string
		better, other Item
	}{
		{
			// 显式指定压过一切：更新的 4K 也该让位给用户点名的 1080p。
			name:   "explicit_beats_freshness_and_resolution",
			better: Item{Title: "b", Explicit: true, Resolution: 1080, PublishedAt: now.Add(-2 * 365 * 24 * time.Hour)},
			other:  Item{Title: "a", Resolution: 2160, PublishedAt: now, Codec: "h265"},
		},
		{
			// 新鲜度压过清晰度：同清晰度下新资源优先。
			name:   "freshness_beats_resolution",
			better: Item{Title: "b", Resolution: 1080, PublishedAt: now},
			other:  Item{Title: "a", Resolution: 2160, PublishedAt: now.Add(-30 * 24 * time.Hour)},
		},
		{
			// 同新鲜度下清晰度压过码率。
			name:   "resolution_beats_codec",
			better: Item{Title: "b", Resolution: 2160, PublishedAt: now, Codec: "h264"},
			other:  Item{Title: "a", Resolution: 1080, PublishedAt: now, Codec: "h265"},
		},
		{
			// 最后才比编码：同分辨率时 h265 > h264 > av1。
			name:   "codec_h265_over_h264",
			better: Item{Title: "b", Resolution: 1080, PublishedAt: now, Codec: "h265"},
			other:  Item{Title: "a", Resolution: 1080, PublishedAt: now, Codec: "h264"},
		},
		{
			name:   "codec_h264_over_av1",
			better: Item{Title: "b", Resolution: 1080, PublishedAt: now, Codec: "h264"},
			other:  Item{Title: "a", Resolution: 1080, PublishedAt: now, Codec: "av1"},
		},
	}
	for _, tc := range cases {
		if !ItemLess(tc.better, tc.other) {
			t.Fatalf("%s：%s 应排在 %s 之前", tc.name, tc.better.Title, tc.other.Title)
		}
		if ItemLess(tc.other, tc.better) {
			t.Fatalf("%s：判定必须单向", tc.name)
		}
	}
}

// TestCodecRankMatches参考实现 编码权重跟 参考实现：h265/hevc > h264 > av1。
// 与 internal/moviepilot 的 codecRank（av1 与 hevc 同档）刻意不同，理由见 CodecRank 注释。
func TestCodecRankMatches参考实现(t *testing.T) {
	if CodecRank("hevc") <= CodecRank("h264") {
		t.Fatalf("hevc 应优于 h264")
	}
	if CodecRank("h264") <= CodecRank("av1") {
		t.Fatalf("h264 应优于 av1")
	}
	if CodecRank("HEVC") != CodecRank("hevc") {
		t.Fatalf("编码名大小写不应影响权重")
	}
}

// TestSortItemsStable SortItems 必须稳定：同分候选保持输入顺序，
// 否则同一批资源两次检索会排出不同结果，订阅行为不可复现。
func TestSortItemsStable(t *testing.T) {
	items := []Item{
		{Title: "a", Resolution: 1080},
		{Title: "b", Resolution: 1080},
		{Title: "c", Resolution: 1080},
	}
	SortItems(items)
	if items[0].Title != "a" || items[1].Title != "b" || items[2].Title != "c" {
		t.Fatalf("同分候选顺序应保持不变，实际 %v", titlesOf(items))
	}
}

// ---------------------------------------------------------------------------
// ParseKeys
// ---------------------------------------------------------------------------

// TestParseKeys 支持多种分隔符并去重、保序 —— 搜索源配置是从界面输入框进来的，
// 用户打「tgto123，hdhive」和「tgto123, hdhive」应该是一回事。
func TestParseKeys(t *testing.T) {
	got := ParseKeys(" tgto123，hdhive; hdhive | tgto123 ")
	want := []string{"tgto123", "hdhive"}
	if len(got) != len(want) {
		t.Fatalf("解析结果不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("解析结果不对：%v", got)
		}
	}
	if len(ParseKeys("")) != 0 || len(ParseKeys("  ,;| ")) != 0 {
		t.Fatalf("空输入应解析出空列表")
	}
}

func titlesOf(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}
