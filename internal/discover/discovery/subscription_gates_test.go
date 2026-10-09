package discovery

import (
	"sort"
	"strings"
	"testing"

	"litepan/internal/discover/connector"
)

// 本文件钉住 T05 的画质 / 特效 / 体积闸门。
//
// 三条设计前提，缺一不可：
//  1. 默认全开（闸门字段为空）——存量订阅不受影响；
//  2. 信息不全一律放行——源没报分辨率/体积时不能把候选筛掉，
//     否则任一家源改个字段名，整条订阅就静默什么都不转了；
//  3. 「未知」只进日志不进跳过原因——用户看到的跳过原因必须是确凿的。

func gateSub(resolution, effect string, minMB, maxMB int) *DiscoverySubscription {
	return &DiscoverySubscription{
		Resolution: resolution, Effect: effect,
		MinFileSizeMB: minMB, MaxFileSizeMB: maxMB,
	}
}

func item4k(t *testing.T) connector.Item {
	it := connector.Item{Kind: connector.ItemShareLink, Slug: "x", Title: "Movie.2160p.WEB-DL"}
	connector.ApplyQuality(&it)
	return it
}

// TestGateDisabledByDefault 存量订阅（没配任何闸门）必须一个候选都不筛。
func TestGateDisabledByDefault(t *testing.T) {
	g := gateForSubscription(&DiscoverySubscription{})
	if g.Enabled() {
		t.Fatalf("没配闸门时不应启用")
	}
	items := []connector.Item{item4k(t), {Kind: connector.ItemShareLink, Slug: "y"}}
	kept, dropped := filterByGate(g, items)
	if len(dropped) != 0 || len(kept) != 2 {
		t.Fatalf("默认闸门不应筛掉任何候选：kept=%d dropped=%d", len(kept), len(dropped))
	}
}

// TestResolutionGateIsLowerBound 验收项 3：resolution=2160 过滤掉 1080p。
func TestResolutionGateIsLowerBound(t *testing.T) {
	g := gateForSubscription(gateSub("2160", "", 0, 0))
	if g.ResolutionLevel != 2160 {
		t.Fatalf("2160 门槛解析成 %d", g.ResolutionLevel)
	}
	items := []connector.Item{
		{Kind: connector.ItemShareLink, Slug: "uhd", Title: "M.2160p.WEB-DL", Resolution: 2160},
		{Kind: connector.ItemShareLink, Slug: "fhd", Title: "M.1080p.WEB-DL", Resolution: 1080},
		{Kind: connector.ItemShareLink, Slug: "hd", Title: "M.720p.WEB-DL", Resolution: 720},
	}
	kept, dropped := filterByGate(g, items)
	if len(kept) != 1 || kept[0].Slug != "uhd" {
		t.Fatalf("应只留 2160，实际留了 %v", slugsOfItems(kept))
	}
	if len(dropped) != 2 {
		t.Fatalf("应筛掉 2 条，实际 %d", len(dropped))
	}
	// 跳过原因要能让人看懂是哪条规则筛的。
	if dropped[0].Reason == "" || !containsAll(dropped[0].Reason, "1080") {
		t.Fatalf("被筛候选必须带可读原因（应含实测分辨率），实际 %q", dropped[0].Reason)
	}
}

// TestResolutionGateAllowsUnknown 分辨率读不出来的候选必须放行。
// 把未知判成不达标，等于「源不报分辨率 ⇒ 订阅永远转不到东西」。
func TestResolutionGateAllowsUnknown(t *testing.T) {
	g := gateForSubscription(gateSub("2160", "", 0, 0))
	items := []connector.Item{
		{Kind: connector.ItemShareLink, Slug: "unknown", Title: "Movie"}, // 没有任何分辨率线索
		{Kind: connector.ItemShareLink, Slug: "uhd", Title: "M.2160p", Resolution: 2160},
	}
	kept, dropped := filterByGate(g, items)
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("未知分辨率必须放行：kept=%d dropped=%d", len(kept), len(dropped))
	}
}

// TestSizeGateSkipsUnknownSize 体积未知的候选不受体积闸门约束（只记日志）。
func TestSizeGateSkipsUnknownSize(t *testing.T) {
	const mb = 1 << 20
	g := gateForSubscription(gateSub("", "", 10, 20)) // 单位：MB（订阅表存的就是 MB）
	items := []connector.Item{
		{Kind: connector.ItemShareLink, Slug: "nosize", Title: "M"},                  // 体积未知
		{Kind: connector.ItemShareLink, Slug: "big", Title: "M", SizeBytes: 30 * mb}, // 超上限
		{Kind: connector.ItemShareLink, Slug: "ok", Title: "M", SizeBytes: 15 * mb},  // 区间内
	}
	kept, dropped := filterByGate(g, items)
	got := map[string]bool{}
	for _, k := range kept {
		got[k.Slug] = true
	}
	if !got["nosize"] || !got["ok"] || got["big"] {
		t.Fatalf("体积闸门结果不对：kept=%v dropped=%d", slugsOfItems(kept), len(dropped))
	}
}

func TestSizeGateBounds(t *testing.T) {
	g := gateForSubscription(gateSub("", "", 10, 20))
	if g.MinBytes != 10*(1<<20) || g.MaxBytes != 20*(1<<20) {
		t.Fatalf("MB→字节换算不对：%d ~ %d", g.MinBytes, g.MaxBytes)
	}
	// 只配上限时下限不该变成 0（那样体积 0 的脏数据会混进来）。
	g2 := gateForSubscription(gateSub("", "", 0, 20))
	if g2.MaxBytes != 20*(1<<20) || g2.MinBytes != 0 {
		t.Fatalf("单边闸门不对：%d ~ %d", g2.MinBytes, g2.MaxBytes)
	}
	// 上下限写反了（最小比最大大）要收口，否则闸门会筛掉一切。
	g3 := gateForSubscription(gateSub("", "", 50, 10))
	if g3.MinBytes > g3.MaxBytes {
		t.Fatalf("上下限写反时不能留下一个恒空的区间：%d ~ %d", g3.MinBytes, g3.MaxBytes)
	}
}

// TestEffectSDRGate 特效闸门。
//
// 注意：闸门只读 Item 上归一化好的结构化字段（HasHDR/HasAtmos），不自己再解析一遍
// 标题 —— 解析在连接器归一化那一步已经做过一遍，闸门再解析就会有两处口径不一致。
// 所以这里的候选要先过 ApplyQuality 才有意义，这也正是真实链路上的样子。
func TestEffectSDRGate(t *testing.T) {
	g := gateForSubscription(gateSub("", "sdr", 0, 0))
	items := normalizedItems(t,
		map[string]string{"plain": "M.1080p.WEB-DL", "hdr": "M.1080p.HDR10", "dv": "M.1080p.Dolby.Vision"})
	kept, dropped := filterByGate(g, items)
	if len(kept) != 1 || kept[0].Slug != "plain" {
		t.Fatalf("sdr 闸门应只留非 HDR/DV，kept=%v", slugsOfItems(kept))
	}
	if len(dropped) != 2 {
		t.Fatalf("应筛掉 HDR 与 DV 两条，实际 %d", len(dropped))
	}
}

// TestEffectHDRGate 要求 HDR 时，普通 SDR 候选要挡掉。
// 这一批里有 hdr / dv 两条带特效信息，所以闸门是「生效」状态，plain 那条缺标记即判否。
func TestEffectHDRGate(t *testing.T) {
	g := gateForSubscription(gateSub("", "hdr", 0, 0))
	items := normalizedItems(t,
		map[string]string{"plain": "M.1080p.WEB-DL", "hdr": "M.1080p.HDR10", "dv": "M.1080p.Dolby.Vision"})
	kept, _ := filterByGate(g, items)
	if len(kept) != 2 {
		t.Fatalf("hdr 闸门应留 HDR 与 DV 两条，kept=%v", slugsOfItems(kept))
	}
	if kept[0].Slug == "plain" || kept[1].Slug == "plain" {
		t.Fatalf("普通 SDR 不该被 hdr 闸门留下：%v", slugsOfItems(kept))
	}
}

// TestGatesCombine 分辨率 + 特效 + 体积三条同时生效时是「与」关系。
// 体积区间 1000~6000 MB（1~6 GB），对应 4K WEB-DL 的常见体量。
func TestGatesCombine(t *testing.T) {
	const mb = 1 << 20
	g := gateForSubscription(gateSub("1080", "sdr", 1000, 6000))
	items := []connector.Item{
		{Kind: connector.ItemShareLink, Slug: "pass", Title: "M.1080p", Resolution: 1080, SizeBytes: 5000 * mb},
		{Kind: connector.ItemShareLink, Slug: "lowres", Title: "M.720p", Resolution: 720, SizeBytes: 5000 * mb},
		{Kind: connector.ItemShareLink, Slug: "hdr", Title: "M.1080p.HDR10", Resolution: 1080, HasHDR: true, SizeBytes: 5000 * mb},
		{Kind: connector.ItemShareLink, Slug: "big", Title: "M.1080p", Resolution: 1080, SizeBytes: 50 * mb},
	}
	kept, dropped := filterByGate(g, items)
	if len(kept) != 1 || kept[0].Slug != "pass" || len(dropped) != 3 {
		t.Fatalf("三条闸门应是「与」关系，kept=%v dropped=%d", slugsOfItems(kept), len(dropped))
	}
}

// normalizedItems 按 slug → 标题造一批走过连接器归一化的候选（按 slug 排序保证稳定）。
func normalizedItems(t *testing.T, bySlug map[string]string) []connector.Item {
	t.Helper()
	slugs := make([]string, 0, len(bySlug))
	for slug := range bySlug {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	out := make([]connector.Item, 0, len(slugs))
	for _, slug := range slugs {
		it := connector.Item{Kind: connector.ItemShareLink, Slug: slug, Title: bySlug[slug]}
		connector.ApplyQuality(&it)
		out = append(out, it)
	}
	return out
}

// TestGateRejectsInvalidEnum 画质/特效是**枚举**字段（界面上是下拉框），
// 收到枚举外的值说明客户端传错了，直接拒绝保存并说清可选值，
// 比静默改成「不限制」安全 —— 后者会让用户以为自己设了 4K 门槛其实没有。
func TestGateRejectsInvalidEnum(t *testing.T) {
	if _, err := normalizeSubscriptionGates(&SubscriptionUpsertPayload{Resolution: "8K奇幻"}); err == nil {
		t.Fatalf("非法分辨率应拒绝保存")
	}
	if _, err := normalizeSubscriptionGates(&SubscriptionUpsertPayload{Effect: "杜比全景声"}); err == nil {
		t.Fatalf("非法特效应拒绝保存")
	}
}

// TestGateWarnsUnknownSource 搜索源名拼错不是枚举错误：它只是没匹配到已注册的源，
// 仍然保存，只给一条提示 —— 因为源列表会随版本增加，不能拿今天的列表卡住明天的配置。
func TestGateWarnsUnknownSource(t *testing.T) {
	payload := &SubscriptionUpsertPayload{SearchSources: stringPointer("tgto123,不存在的源")}
	warnings, err := normalizeSubscriptionGates(payload)
	if err != nil {
		t.Fatalf("未知源名不该拒绝保存：%v", err)
	}
	if !strings.Contains(warnings, "不存在的源") {
		t.Fatalf("提示里应点名未知源，实际 %q", warnings)
	}
	// 原样保留用户填的内容（归一化只处理分隔符与空格），不擅自删掉未知项。
	if got := connector.ParseKeys(*payload.SearchSources); len(got) != 2 {
		t.Fatalf("未知源应原样保留，实际 %v", got)
	}
}

// TestGateNormalizesValidValues 合法值会被归一化成小写标准写法。
func TestGateNormalizesValidValues(t *testing.T) {
	payload := &SubscriptionUpsertPayload{Resolution: " 4K ", Effect: "HDR10+", SearchSources: stringPointer(" tgto123 , hdhive ")}
	if _, err := normalizeSubscriptionGates(payload); err != nil {
		t.Fatalf("合法值不该被拒：%v", err)
	}
	if payload.Resolution != "2160" {
		t.Fatalf("4K 应归一成 2160，实际 %q", payload.Resolution)
	}
	if payload.Effect != "hdr10+" {
		t.Fatalf("特效应归一成小写，实际 %q", payload.Effect)
	}
	if *payload.SearchSources != "tgto123,hdhive" {
		t.Fatalf("源列表应去空格，实际 %q", *payload.SearchSources)
	}
}

func TestParseResolutionLevel(t *testing.T) {
	cases := map[string]int{
		"2160": 2160, "2160p": 2160, "4k": 2160, "uhd": 2160, "4K": 2160,
		"1080": 1080, "1080p": 1080, "fhd": 1080,
		"720": 720, "720p": 720, "hd": 720,
		"480": 480, "sd": 480,
		"": 0, "x2k": 0, "不限": 0,
	}
	for raw, want := range cases {
		got, ok := parseResolutionLevel(raw)
		if want == 0 {
			if ok {
				t.Fatalf("parseResolutionLevel(%q) 应判为非法，实际 ok=%v level=%d", raw, ok, got)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("parseResolutionLevel(%q) = (%d,%v)，期望 (%d,true)", raw, got, ok, want)
		}
	}
}

// TestFormatBytesHandoff 给日志与界面用的体积文案。
func TestFormatBytesHandoff(t *testing.T) {
	if got := formatBytes(0); got != "" {
		t.Fatalf("体积未知应给空串，实际 %q", got)
	}
	if got := formatBytes(1536 * 1 << 20); got != "1.5 GB" {
		t.Fatalf("1.5 GiB 的文案应是 1.5 GB，实际 %q", got)
	}
	if got := formatBytes(800 * 1 << 20); got != "800.0 MB" {
		t.Fatalf("文案不对：%q", got)
	}
}

func stringPointer(s string) *string { return &s }

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func slugsOfItems(items []connector.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Slug)
	}
	return out
}
