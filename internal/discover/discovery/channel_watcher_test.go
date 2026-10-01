package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"litepan/internal/discover/tgchannel"
)

// ctxTODO 测试用的背景 context。
func ctxTODO() context.Context { return context.Background() }

// TestPostIDGreater 覆盖频道游标比较的核心语义：
// TG post id 是递增整数，但位数可能超过 int64 安全范围，故必须按「长度优先、字典序次之」比较。
func TestPostIDGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"100", "99", true},   // 位数多者更大
		{"99", "100", false},  // 位数少者更小
		{"100", "100", false}, // 相等不算大于
		{"101", "100", true},  // 同位数比字典序
		{"99999999999999999999999", "9999999999999999999999", true}, // 超过 int64 也不误判
		{" 100 ", "99", true}, // 容忍空白
	}
	for _, c := range cases {
		if got := postIDGreater(c.a, c.b); got != c.want {
			t.Errorf("postIDGreater(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestSubPrefReaders 覆盖从 Preferences JSON 读扩展字段的各种类型（含 nil Pref 兜底）。
func TestSubPrefReaders(t *testing.T) {
	// nil Pref：所有读取都必须回落到默认值，不能 panic。
	var empty DiscoverySubscription
	if got := subPrefBool(empty, "wash", false); got != false {
		t.Errorf("nil Pref subPrefBool = %v, want false", got)
	}
	if got := subPrefInt(empty, "backfill_pages", 50); got != 50 {
		t.Errorf("nil Pref subPrefInt = %v, want 50", got)
	}
	if got := subPrefString(empty, "wash_target", "4k"); got != "4k" {
		t.Errorf("nil Pref subPrefString = %v, want 4k", got)
	}
	if got := subPrefStrings(empty, "keywords"); got != nil {
		t.Errorf("nil Pref subPrefStrings = %v, want nil", got)
	}

	sub := DiscoverySubscription{Pref: map[string]any{
		"wash":           true,
		"season":         float64(2), // JSON 解出来是 float64
		"backfill_pages": "80",       // 兼容 UI 传字符串
		"wash_target":    "4k_remux",
		"keywords":       []any{"无上神帝", " 斗破苍穹 "},
		"auto_finish":    "on",
	}}
	if !subPrefBool(sub, "wash", false) {
		t.Error("subPrefBool(wash) = false, want true")
	}
	if !subPrefBool(sub, "auto_finish", false) {
		t.Error("subPrefBool(auto_finish) = false, want true")
	}
	if got := subPrefInt(sub, "season", 0); got != 2 {
		t.Errorf("subPrefInt(season) = %d, want 2", got)
	}
	if got := subPrefInt(sub, "backfill_pages", 50); got != 80 {
		t.Errorf("subPrefInt(backfill_pages) = %d, want 80", got)
	}
	if got := subPrefString(sub, "wash_target", ""); got != "4k_remux" {
		t.Errorf("subPrefString(wash_target) = %q, want 4k_remux", got)
	}
	kws := subPrefStrings(sub, "keywords")
	if len(kws) != 2 || kws[0] != "无上神帝" || kws[1] != "斗破苍穹" {
		t.Errorf("subPrefStrings(keywords) = %v, want [无上神帝 斗破苍穹]", kws)
	}

	// 逗号分隔字符串也要能拆开（用户在 UI 里可能这么填）。
	comma := DiscoverySubscription{Pref: map[string]any{"keywords": "A, B；C"}}
	if got := subPrefStrings(comma, "keywords"); len(got) != 3 {
		t.Errorf("comma keywords = %v, want 3 项", got)
	}
}

// TestIsRateLimitErr 限流识别必须宽松命中各家网盘的不同文案，且不能把普通错误误判成限流。
func TestIsRateLimitErr(t *testing.T) {
	rateLimited := []string{
		"rate limit exceeded",
		"HTTP 429 Too Many Requests",
		"请求频率过快",
		"操作太频繁，请稍后再试",
		"触发限流",
		"quota exceeded",
		"please slow down",
	}
	for _, msg := range rateLimited {
		if !isRateLimitErr(errors.New(msg)) {
			t.Errorf("isRateLimitErr(%q) = false, want true", msg)
		}
	}
	notLimited := []string{"分享链接已失效", "提取码错误", "目标目录不存在", "network unreachable"}
	for _, msg := range notLimited {
		if isRateLimitErr(errors.New(msg)) {
			t.Errorf("isRateLimitErr(%q) = true, want false", msg)
		}
	}
	if isRateLimitErr(nil) {
		t.Error("isRateLimitErr(nil) = true, want false")
	}
}

// TestExtract123ShareKey 聚合帖防误转依赖从分享链接提 shareKey；
// 非 123 链接必须返回空串（进而走「放行」降级）。
func TestExtract123ShareKey(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.123pan.com/s/abc123XYZ", "abc123XYZ"},
		{"https://123pan.com/s/abc123XYZ?pwd=1234", "abc123XYZ"},
		{"https://www.123684.com/s/AbC-123_x", "AbC-123_x"},
		{"https://www.123865.com/123pan/abc123XYZ", "abc123XYZ"},
		{"https://pan.quark.cn/s/abcdef", ""},
		{"https://www.guangyapan.com/s/abcdef", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := extract123ShareKey(c.url); got != c.want {
			t.Errorf("extract123ShareKey(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestVerifyShareTitleMatches 聚合帖防误转的降级策略：
// 只要无法确定地拿到分享标题，就必须放行（宁放勿拦）。
func TestVerifyShareTitleMatches(t *testing.T) {
	restore := ShareTitleProbeFn
	defer func() { ShareTitleProbeFn = restore }()

	// 关键词为空 → 放行
	ShareTitleProbeFn = func(_ context.Context, _, _ string) (string, error) {
		return "完全无关的剧", nil
	}
	if !verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", nil) {
		t.Error("空关键词应放行")
	}

	// 非 123 分享 → 放行（不支持探测）
	if !verifyShareTitleMatches(ctxTODO(), "https://pan.quark.cn/s/abcdef", "", []string{"无上神帝"}) {
		t.Error("非 123 分享应放行")
	}

	// 探测函数未绑定 → 放行
	ShareTitleProbeFn = nil
	if !verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", []string{"无上神帝"}) {
		t.Error("未绑定探测函数应放行")
	}

	// 查询失败 → 放行
	ShareTitleProbeFn = func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("boom")
	}
	if !verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", []string{"无上神帝"}) {
		t.Error("查询失败应放行")
	}

	// 名称为空 → 放行
	ShareTitleProbeFn = func(_ context.Context, _, _ string) (string, error) { return "   ", nil }
	if !verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", []string{"无上神帝"}) {
		t.Error("名称为空应放行")
	}

	// 标题不匹配 → 拦截（这是唯一会返回 false 的路径）
	ShareTitleProbeFn = func(_ context.Context, _, _ string) (string, error) {
		return "斗破苍穹 4K 合集", nil
	}
	if verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", []string{"无上神帝"}) {
		t.Error("标题不匹配应拦截")
	}

	// 标题匹配 → 放行
	ShareTitleProbeFn = func(_ context.Context, _, _ string) (string, error) {
		return "无上神帝 第01-100集", nil
	}
	if !verifyShareTitleMatches(ctxTODO(), "https://www.123pan.com/s/abc123XYZ", "", []string{"无上神帝"}) {
		t.Error("标题匹配应放行")
	}
}

// TestLinkTypeMatchesProvider 频道帖链接类型与订阅目标网盘的匹配（含别名归一）。
func TestLinkTypeMatchesProvider(t *testing.T) {
	cases := []struct {
		linkType, provider string
		want               bool
	}{
		{"123", "123", true},
		{"123", "123pan", true},
		{"guangyapan", "guangya", true},
		{"pan139", "139", true},
		{"123", "guangya", false},
		{"", "123", false},
	}
	for _, c := range cases {
		if got := linkTypeMatchesProvider(c.linkType, c.provider); got != c.want {
			t.Errorf("linkTypeMatchesProvider(%q, %q) = %v, want %v", c.linkType, c.provider, got, c.want)
		}
	}
}

// TestBuildChannelSummaryAndURL 汇总文案与帖子永久链接的格式。
func TestBuildChannelSummaryAndURL(t *testing.T) {
	got := buildChannelSummary("test_ch", 3, 5, 7, 4, 2, "12345", false)
	want := "频道 test_ch：翻 3 页，命中 5 帖，链接 7 个，转存成功 4 次，跳过 2 次，游标推进至 12345"
	if got != want {
		t.Errorf("buildChannelSummary = %q, want %q", got, want)
	}
	backfill := buildChannelSummary("test_ch", 3, 5, 7, 4, 2, "12345", true)
	if !strings.Contains(backfill, "回溯模式，未推进游标") {
		t.Errorf("回溯文案应说明未推进游标，got %q", backfill)
	}

	if got := buildTGMessageURL("@test_ch", "12345"); got != "https://t.me/test_ch/12345" {
		t.Errorf("buildTGMessageURL = %q", got)
	}
	if got := buildTGMessageURL("test_ch/", ""); got != "" {
		t.Errorf("空 postID 应返回空串，got %q", got)
	}
}

// TestPreviewChannelLimitClamp 预览接口的 limit 归一（<=0 或 >50 回落 10），
// 这里只验证入参校验分支（空频道名直接报错，不触网）。
func TestPreviewChannelLimitClamp(t *testing.T) {
	if _, err := PreviewChannel("   ", 10); err == nil {
		t.Error("空频道名应报错")
	}
}

// TestMediaSpecHelpersUsable 确认洗版分支依赖的媒体层符号可直接调用（不校验具体解析结果，
// 那是 T2a 的 media_spec_test.go 覆盖面）。
func TestMediaSpecHelpersUsable(t *testing.T) {
	spec := ParseMediaSpec("无上神帝 第01-03集 2160p REMUX")
	_ = spec.Score()
	_ = spec.BetterThan(MediaSpec{})
	if JoinEpisodeKeys(ParseEpisodeKeys("第01集", 1)) == "" {
		// 允许解析不出（不同片源文案差异大），但 JoinEpisodeKeys 对空输入必须安全。
		_ = JoinEpisodeKeys(nil)
	}
	if got := JoinEpisodeKeys(nil); got != "" {
		t.Errorf("JoinEpisodeKeys(nil) = %q, want \"\"", got)
	}
}

var _ = tgchannel.MatchKeywords // 保留 tgchannel 引用，确保测试文件与实现同包依赖一致
