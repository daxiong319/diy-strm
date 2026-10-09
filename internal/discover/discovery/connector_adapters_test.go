package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"litepan/internal/discover/connector"
	"litepan/internal/discover/hdhive"
	"litepan/internal/discover/identity"
)

// 本文件钉住 T05 在订阅执行器这侧的装配行为：连接器注册表、两个适配器、
// 画质闸门、跨源合并、以及「存量订阅默认只有 tgto123 且结果不变」。
//
// 最要紧的一条是 TestDefaultSourceSetIsTgto123OnlyAndSortedByLegacyKey：
// 它是任务书验收项 1 的落点。存量订阅的行为必须与接口化之前逐条一致，
// 而这一点靠三处设计共同保证（都写进了 searchSubscriptionResources 的注释）：
// 注册顺序（tgto123 在前）、候选回转（走既有 resourceFromHive）、
// 排序主键（candidateSortKey 不变，新维度只做 tie-break）。

// ---------------------------------------------------------------- 替身

// fakeChannelClient 实现 hdhive.ChannelClient，用来在不接真实 RE0 通道的
// 前提下验证 HdHiveConnector 的 Search / Resolve / Ready 三条路径。
type fakeChannelClient struct {
	resources   map[string][]hdhive.Resource // mediaType+"|"+tmdbID → 资源
	details     map[string]hdhive.ShareDetail
	resourceErr error
	detailErr   error

	resourceCalls []string
	detailCalls   []string
	unlockCalls   []string
}

func (f *fakeChannelClient) Ping(context.Context) error { return nil }
func (f *fakeChannelClient) Me(context.Context) (*hdhive.OAuthAPIResponse, error) {
	return &hdhive.OAuthAPIResponse{}, nil
}

func (f *fakeChannelClient) GetResources(ctx context.Context, mediaType, tmdbID string) (*hdhive.OAuthAPIResponse, error) {
	f.resourceCalls = append(f.resourceCalls, mediaType+"|"+tmdbID)
	if f.resourceErr != nil {
		return nil, f.resourceErr
	}
	items := f.resources[mediaType+"|"+tmdbID]
	if items == nil {
		items = []hdhive.Resource{}
	}
	return &hdhive.OAuthAPIResponse{Success: true, Data: mustJSON(items)}, nil
}

func (f *fakeChannelClient) GetShareDetail(ctx context.Context, slug string) (*hdhive.OAuthAPIResponse, error) {
	f.detailCalls = append(f.detailCalls, slug)
	if f.detailErr != nil {
		return nil, f.detailErr
	}
	d, ok := f.details[slug]
	if !ok {
		return nil, errors.New("没有这个分享：" + slug)
	}
	return &hdhive.OAuthAPIResponse{Success: true, Data: mustJSON(d)}, nil
}

func (f *fakeChannelClient) UnlockResource(ctx context.Context, slug string) (*hdhive.OAuthAPIResponse, error) {
	f.unlockCalls = append(f.unlockCalls, slug)
	return &hdhive.OAuthAPIResponse{Success: true}, nil
}

func (f *fakeChannelClient) Checkin(context.Context, bool) (*hdhive.OAuthAPIResponse, error) {
	return &hdhive.OAuthAPIResponse{}, nil
}

// setupConnectorTest 把连接器相关测试的公共前置铺好：
// 建库建表 + 绑定设置服务（解析搜索源与超时预算都要读设置），
// 再装上 RE0 通道替身并重建注册表。
func setupConnectorTest(t *testing.T, overrides map[string]string, c hdhive.ChannelClient) {
	t.Helper()
	setupLedgerTest(t, overrides)
	prev := HdHiveChannelFactory
	SetHdHiveChannelFactory(func(context.Context) hdhive.ChannelClient { return c })
	InitConnectors()
	t.Cleanup(func() {
		SetHdHiveChannelFactory(prev)
		InitConnectors()
	})
}

func installFakeChannel(t *testing.T, c hdhive.ChannelClient) {
	t.Helper()
	setupConnectorTest(t, nil, c)
}

// ---------------------------------------------------------------- 注册表

func TestRegistryRegistersTgto123First(t *testing.T) {
	InitConnectors()
	keys := ConnectorKeys()
	if len(keys) < 1 || keys[0] != KeyTgto123 {
		t.Fatalf("tgto123 必须是第一个注册的连接器（顺序决定候选顺序），实际 %v", keys)
	}
	if _, ok := ConnectorFor(KeyHdHive); !ok {
		t.Fatalf("hdhive 连接器应已注册")
	}
}

// TestDefaultSourceSetIsTgto123Only 存量订阅（search_sources 为空）必须只解析出
// tgto123 —— 这是「存量行为不变」最直接的一条。
func TestDefaultSourceSetIsTgto123Only(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{})
	sub := &DiscoverySubscription{}
	keys := subscriptionSearchSources(sub)
	if len(keys) != 1 || keys[0] != KeyTgto123 {
		t.Fatalf("默认搜索源应只有 tgto123，实际 %v", keys)
	}
	ready, skipped := SubscriptionConnectors(keys)
	if len(ready) != 1 || ready[0].Key() != KeyTgto123 {
		t.Fatalf("默认应解析出且只解析出 tgto123，实际 ready=%v skipped=%v", ready, skipped)
	}
}

// TestUnknownSourceIsReportedNotFatal 源名写错要提示，但不能让整条订阅瘫掉。
func TestUnknownSourceIsReportedNotFatal(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{})
	sub := &DiscoverySubscription{SearchSources: "tgto123,手抖打错的源"}
	keys := subscriptionSearchSources(sub)
	if len(keys) != 2 {
		t.Fatalf("应保留两个 key（未知的交给注册表判定），实际 %v", keys)
	}
	ready, skipped := SubscriptionConnectors(keys)
	if len(ready) != 1 || ready[0].Key() != KeyTgto123 {
		t.Fatalf("未知源应被跳过而不是让已知源失效，实际 ready=%v", ready)
	}
	if len(skipped) != 1 || skipped[0].Key != "手抖打错的源" {
		t.Fatalf("未知源应被记录，skipped=%v", skipped)
	}
	if desc := describeSkippedConnectors(skipped); desc == "" {
		t.Fatalf("跳过的源应有可读描述")
	}
}

// TestHdHiveNotReadyWithoutChannelFactory 未装配 RE0 通道时 hdhive 必须不就绪，
// 否则每次订阅都会白等一轮超时。
func TestHdHiveNotReadyWithoutChannelFactory(t *testing.T) {
	setupConnectorTest(t, nil, nil)
	c, ok := ConnectorFor(KeyHdHive)
	if !ok {
		t.Fatalf("hdhive 连接器应已注册")
	}
	if c.Ready() {
		t.Fatalf("未装配 RE0 通道时 hdhive 不应就绪")
	}
	_, skipped := SubscriptionConnectors([]string{KeyTgto123, KeyHdHive})
	if len(skipped) != 1 || skipped[0].Key != KeyHdHive {
		t.Fatalf("未就绪的 hdhive 应出现在跳过列表里，skipped=%v", skipped)
	}
}

// ---------------------------------------------------------------- hdhive 适配器

// TestHdHiveSearchReturnsNormalizedItems 验收项 2：第二源能完整跑通一轮检索。
func TestHdHiveSearchReturnsNormalizedItems(t *testing.T) {
	fake := &fakeChannelClient{resources: map[string][]hdhive.Resource{
		"tv|1396": {
			{Slug: "abc123", Title: "Breaking Bad S01E01 1080p WEB", Remark: "1080p|中字",
				PanType: "", VideoResolution: []string{"1080p"}},
		},
	}}
	installFakeChannel(t, fake)
	c, _ := ConnectorFor(KeyHdHive)

	items, err := c.Search(context.Background(), connector.Query{TMDBID: "1396", MediaType: "tv"})
	if err != nil {
		t.Fatalf("检索不应失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 条候选，实际 %d", len(items))
	}
	got := items[0]
	if got.Kind != connector.ItemShareLink {
		t.Fatalf("PanType 为空的 RE0 资源应判为分享链接，实际 %q", got.Kind)
	}
	if got.Slug != "abc123" || got.SourceKey != KeyHdHive {
		t.Fatalf("slug/来源不对：%+v", got)
	}
	if got.Resolution != 1080 {
		t.Fatalf("应从站方标签识别出 1080，实际 %d", got.Resolution)
	}
	if len(fake.resourceCalls) != 1 || fake.resourceCalls[0] != "tv|1396" {
		t.Fatalf("应按 mediaType+tmdbID 检索一次，实际 %v", fake.resourceCalls)
	}
}

// TestHdHiveSearchPropagatesError 单源报错要冒泡给 SearchAll 判定，不能吞掉。
func TestHdHiveSearchPropagatesError(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{resourceErr: errors.New("RE0 通道炸了")})
	c, _ := ConnectorFor(KeyHdHive)
	if _, err := c.Search(context.Background(), connector.Query{MediaType: "tv"}); err == nil {
		t.Fatalf("源报错必须冒泡，不能静默返回空")
	}
}

// TestHdHiveResolveReturnsShareDetailMetadata 验收项 6 的可验证部分：
// Resolve 必须把分享详情里的 TMDB/标题证据填进 Manifest。
func TestHdHiveResolveReturnsShareDetailMetadata(t *testing.T) {
	fake := &fakeChannelClient{details: map[string]hdhive.ShareDetail{
		"abc123": {Slug: "abc123", Title: "Pilot", IsFreeForUser: true,
			Media: &struct {
				Type   string `json:"type"`
				TMDBID string `json:"tmdb_id"`
				Title  string `json:"title"`
				Season string `json:"season"`
			}{Type: "tv", TMDBID: "1396", Title: "Breaking Bad", Season: "1"}},
	}}
	installFakeChannel(t, fake)
	c, _ := ConnectorFor(KeyHdHive)

	it := connector.Item{Kind: connector.ItemMagnet, Slug: "abc123", Title: "Pilot"}
	m, err := c.Resolve(context.Background(), it)
	if err != nil {
		t.Fatalf("Resolve 不应失败：%v", err)
	}
	if m.Kind != identity.EvidenceMetadata {
		t.Fatalf("分享详情带回媒体绑定后证据应升到 metadata 档，实际 %+v", m)
	}
	if m.TMDBID != "1396" {
		t.Fatalf("应带出分享详情里的 TMDB ID，实际 %q", m.TMDBID)
	}
	if len(fake.detailCalls) != 1 || fake.detailCalls[0] != "abc123" {
		t.Fatalf("应按 slug 查一次分享详情，实际 %v", fake.detailCalls)
	}
}

// TestHdHiveResolveMissingDetailFails 详情查不到必须报错返回 EvidenceNone，
// 不能给一个空壳 Manifest 让身份闸门以为「有证据但匹配失败」。
func TestHdHiveResolveMissingDetailFails(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{details: map[string]hdhive.ShareDetail{}})
	c, _ := ConnectorFor(KeyHdHive)
	it := connector.Item{Kind: connector.ItemMagnet, Slug: "不存在"}
	m, err := c.Resolve(context.Background(), it)
	if err == nil {
		t.Fatalf("查不到分享详情应报错")
	}
	if m.Kind != identity.EvidenceNone {
		t.Fatalf("失败时必须返回 EvidenceNone，实际 %v", m.Kind)
	}
}

// TestHdHiveResolveShareLinkPassesThrough 分享链接不需要二次解析
// （它本身就是可转存对象），Resolve 应当直接给出 metadata 档而不是报错。
// 这条路径在正常流程里走不到（Resolve 只对 NeedsResolve 为真的候选调用），
// 但必须有定义：连接器实现方不能对「不需要解析」的候选一律返回错误。
func TestHdHiveResolveShareLinkPassesThrough(t *testing.T) {
	fake0 := &fakeChannelClient{}
	installFakeChannel(t, fake0)
	c, _ := ConnectorFor(KeyHdHive)
	m, err := c.Resolve(context.Background(),
		connector.Item{Kind: connector.ItemShareLink, Slug: "abc", Title: "Some Movie 1080p"})
	if err != nil {
		t.Fatalf("分享链接 Resolve 不该报错，实际 %v", err)
	}
	if m.Kind != identity.EvidenceMetadata {
		t.Fatalf("应给出 metadata 档，实际 %v", m.Kind)
	}
	if len(fake0.detailCalls) != 0 {
		t.Fatalf("分享链接不该去查分享详情，实际 %v", fake0.detailCalls)
	}
}

// ---------------------------------------------------------------- tgto123 适配器

// TestTgTo123ReadyFollowsProxySetting 就绪与否跟着反代地址设置走。
// 注意 Tgto123ConfiguredURL 在设置为空时会回落到 Tgto123DefaultURL（同机默认地址），
// 所以「不配就用默认」是有意为之的运维便利：反代跟 litepan 同机部署时零配置可用。
func TestTgTo123ReadyFollowsProxySetting(t *testing.T) {
	setupConnectorTest(t, map[string]string{SettingTgto123URL: Tgto123DefaultURL}, &fakeChannelClient{})
	c, _ := ConnectorFor(KeyTgto123)
	if !c.Ready() {
		t.Fatalf("配了反代地址后 tgto123 应就绪")
	}
	if c.Label() == "" || c.Key() != KeyTgto123 {
		t.Fatalf("连接器标识不对：key=%q label=%q", c.Key(), c.Label())
	}
	if len(c.Purpose()) == 0 {
		t.Fatalf("tgto123 是唯一在跑通的源，不该声明成没有用途")
	}
}

// TestConnectorItemFromHiveMatchesResourceFromHive 这是 T05 最关键的不变式：
// tgto123 路径上的 Item 必须能无损地转回既有候选，否则存量订阅的排序、
// 洗版计划、积分判断会全部对不上。
func TestConnectorItemFromHiveMatchesResourceFromHive(t *testing.T) {
	r := hdhive.Resource{
		Slug: "abc123", Title: "Breaking Bad S01E03 1080p WEB-DL", Remark: "中字",
		PanType: "", VideoResolution: []string{"1080p"},
		ShareSize: "1.2 GB", UnlockPoints: 5, IsUnlocked: true,
		CreatedAt: "2026-10-08 10:00:00",
	}
	want := resourceFromHive(r)
	got, ok := candidateFromConnectorItem(ConnectorItemFromHive(r))
	if !ok {
		t.Fatalf("带原始 Resource 的 Item 应能转回候选")
	}
	if got.Slug != want.Slug || got.ShareURL != want.ShareURL || got.Title != want.Title {
		t.Fatalf("基本字段不一致：%+v vs %+v", got, want)
	}
	if got.UnlockPoints != want.UnlockPoints || got.IsUnlocked != want.IsUnlocked {
		t.Fatalf("积分/解锁状态不一致：%+v vs %+v", got, want)
	}
	if got.Size != want.Size {
		t.Fatalf("体积不一致：%q vs %q", got.Size, want.Size)
	}
	if !got.PointsKnown != !want.PointsKnown {
		t.Fatalf("积分可知性不一致：%+v vs %+v", got, want)
	}
}

// TestCandidateFromConnectorItemRejectsEmptyTitle 通用映射下没有标题的候选直接丢，
// 不能造出一个空壳候选混进排序（排序会拿它去比画质，空壳恒垫底）。
func TestCandidateFromConnectorItemRejectsEmptyTitle(t *testing.T) {
	if _, ok := candidateFromConnectorItem(connector.Item{Kind: connector.ItemMagnet}); ok {
		t.Fatalf("无标题候选应被丢弃")
	}
}

// TestMagnetCandidateMarksPointsUnknown 纯磁力源的「积分」没有意义，
// 标成未知才不会让 candidateSortKey 按 0 分白捡便宜。
func TestMagnetCandidateMarksPointsUnknown(t *testing.T) {
	cand, ok := candidateFromConnectorItem(connector.Item{
		Kind: connector.ItemMagnet, SourceKey: "seed", Title: "Some.Movie.2024.1080p",
		MagnetURI: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	})
	if !ok {
		t.Fatalf("磁力候选应可转成资源候选")
	}
	if cand.PointsKnown {
		t.Fatalf("磁力源没有积分概念，PointsKnown 应为 false")
	}
	if cand.ShareURL != "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("磁力链接应落到 ShareURL，实际 %q", cand.ShareURL)
	}
}

// ---------------------------------------------------------------- 排序不变式

// TestDefaultSourceSetIsTgto123OnlyAndSortedByLegacyKey 验收项 1：
// 默认只有 tgto123 时，候选顺序完全由既有 candidateSortKey 决定。
func TestDefaultSourceSetIsTgto123OnlyAndSortedByLegacyKey(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{})

	rLocked := hdhive.Resource{Slug: "s1", Title: "A 2160p HEVC", IsUnlocked: true, UnlockPoints: 90}
	rOpen := hdhive.Resource{Slug: "s2", Title: "B 1080p H264", IsUnlocked: true, UnlockPoints: 2}
	rNew := hdhive.Resource{Slug: "s3", Title: "C 720p X264", IsUnlocked: false, UnlockPoints: 0}
	r := []hdhive.Resource{rLocked, rOpen, rNew}

	// 走「连接器 Item → 候选」这条真实链路，而不是直接拿 resourceCandidate。
	entries := make([]subscriptionCandidate, 0, len(r))
	for _, x := range r {
		it := ConnectorItemFromHive(x)
		if c, ok := candidateFromConnectorItem(it); ok {
			entries = append(entries, subscriptionCandidate{cand: c, it: it})
		}
	}
	sortCandidates(entries)
	got := slugsOfEntries(entries)

	if len(entries) != 3 {
		t.Fatalf("应有 3 条候选，实际 %d", len(entries))
	}
	// 既有排序：已解锁 > 积分低 > 解锁人数。s2(积分2) 必须压过 s1(积分90)。
	// s3 未解锁，无论画质多差都排最后 —— 这条优先级不能被 tie-break 越过。
	if got[0] != "s2" {
		t.Fatalf("积分低的应排最前，实际 %q（%v）", got[0], got)
	}
	if got[1] != "s1" || got[2] != "s3" {
		t.Fatalf("顺序与 candidateSortKey 不符：%v", got)
	}
}

// TestSortCandidatesTieBreakUsesConnectorScore 积分/解锁状态完全相同时，
// 才用跨源维度（清晰度、显式指定）决定先后。
func TestSortCandidatesTieBreakUsesConnectorScore(t *testing.T) {
	installFakeChannel(t, &fakeChannelClient{})
	build := func(slug, title string, res int, explicit bool) subscriptionCandidate {
		it := connector.Item{
			Kind: connector.ItemShareLink, Slug: slug, ShareCode: slug, SourceKey: KeyTgto123,
			Title: title, Resolution: res, Explicit: explicit,
			IsUnlocked: true, UnlockPoints: 10,
		}
		c, _ := candidateFromConnectorItem(it)
		return subscriptionCandidate{cand: c, it: it}
	}
	entries := []subscriptionCandidate{
		build("low", "X 720p", 720, false),
		build("high", "Y 2160p", 2160, false),
		build("tagged", "Z 1080p", 1080, true),
	}
	sortCandidates(entries)
	// 显式指定 > 清晰度：tagged 必须在 high 前面，即使它分辨率更低。
	if got := slugsOfEntries(entries); got[0] != "tagged" || got[1] != "high" || got[2] != "low" {
		t.Fatalf("并列时的 tie-break 顺序不对：%v", got)
	}
}

func slugsOfEntries(entries []subscriptionCandidate) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.cand.Slug)
	}
	return out
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
