package playpath

import (
	"strings"
	"testing"
	"time"
)

// 验收 1：按顺序匹配，**第一条命中即生效**（不是最精确优先）。
// 这条用例构造两条源都能匹配同一路径的规则，且把「更精确」的那条放在
// 后面：期望用的是第一条。如果实现改成「最精确优先」，这里立刻红。
func TestMapUsesFirstMatchingRuleNotMostSpecific(t *testing.T) {
	s := New([]Rule{
		{ID: "broad", Source: "/media", Target: "/A"},
		{ID: "narrow", Source: "/media/4k", Target: "/B"},
	}, nil)
	res := s.Map("/media/4k/movie.mkv")
	if res.Matched != true {
		t.Fatalf("应当命中，实际未命中")
	}
	if res.RuleID != "broad" {
		t.Fatalf("应当用第一条 broad，实际用了 %q", res.RuleID)
	}
	if res.Path != "/A/4k/movie.mkv" {
		t.Fatalf("映射结果不对：%q", res.Path)
	}
	// 第二条被遮蔽这一事实必须被报出来 —— 这是「我改了规则没生效」的答案。
	if len(res.Candidates) != 1 || res.Candidates[0].ID != "narrow" {
		t.Fatalf("被遮蔽的规则没有被记录：%+v", res.Candidates)
	}
}

// 验收 2：区分大小写。/Media/ 和 /media/ 是两条不同的路径。
func TestMapIsCaseSensitive(t *testing.T) {
	s := New([]Rule{{ID: "r1", Source: "/media", Target: "/A"}}, nil)
	if got := s.Map("/Media/movie.mkv"); got.Matched {
		t.Fatalf("/Media/ 不应匹配源 /media，实际被映射成 %q", got.Path)
	}
	if got := s.Map("/media/movie.mkv"); !got.Matched || got.Path != "/A/movie.mkv" {
		t.Fatalf("/media/ 应当匹配，实际 matched=%v path=%q", got.Matched, got.Path)
	}
	// 目标侧也照原样保留，不做「友好」的大小写归一 —— 归一了就等于
	// 替用户猜，而本功能的原则恰恰是不能替用户猜。
	if got := s.Map("/Media2/x.mkv"); got.Matched {
		t.Fatalf("/Media2/ 不应命中，实际 %q", got.Path)
	}
}

// 验收 3：匹配不上不报错，原样返回。静默是 muvyo 的语义，
// 但 litepan 必须让「静默」在配置页上可见（命中次数恒为 0）。
func TestMapSilentlyPassesThroughWhenNoRuleMatches(t *testing.T) {
	s := New([]Rule{{ID: "r1", Source: "/media", Target: "/A"}}, nil)
	got := s.Map("/other/movie.mkv")
	if got.Matched {
		t.Fatalf("不该命中，实际映射成 %q", got.Path)
	}
	if got.Path != "/other/movie.mkv" {
		t.Fatalf("未命中时路径必须原样返回，实际 %q", got.Path)
	}
	if st := s.Stat("r1"); st.Hits != 0 {
		t.Fatalf("未命中的规则不应累加命中数：%+v", st)
	}
}

// 边界对齐：源 /media 不得吃掉 /media-old/... 的请求。
// 纯前缀匹配会让「为 /media 写的规则」悄悄劫持另一个目录树。
func TestMapRequiresBoundaryAlignedPrefix(t *testing.T) {
	s := New([]Rule{{ID: "r1", Source: "/media", Target: "/A"}}, nil)
	for _, p := range []string{"/media-old/a.mkv", "/media2/a.mkv", "/mediaserver/a.mkv"} {
		if got := s.Map(p); got.Matched {
			t.Fatalf("%q 不应被 /media 匹配，实际成 %q", p, got.Path)
		}
	}
	// 恰好相等算命中：源本身就是一条完整路径时应当生效。
	if got := s.Map("/media"); !got.Matched || got.Path != "/A" {
		t.Fatalf("/media 应命中，matched=%v path=%q", got.Matched, got.Path)
	}
}

// 源带尾斜杠与不带等价，避免用户随手加斜杠就多出一条永远命中不到的规则。
func TestNormalizeTreatsTrailingSlashAsSameRule(t *testing.T) {
	a := Normalize([]Rule{{Source: "/media/", Target: "/A/"}})
	b := Normalize([]Rule{{Source: "/media", Target: "/A"}})
	if len(a) != 1 || a[0].Source != "/media" || a[0].Target != "/A" {
		t.Fatalf("源/目标的尾斜杠未被规整：%+v", a)
	}
	if len(b) != 1 || a[0] != b[0] {
		t.Fatalf("带斜杠与不带斜杠应规整成同一条：%+v vs %+v", a, b)
	}
}

// 源与目标写反（典型：把 /A 写成源、/media 写成目标）不会报错，
// 只会映射出一个没人认识的结果。靠「测试路径」按钮让用户当场看见。
func TestReversedRuleMapsToUnrecognisedPathSilently(t *testing.T) {
	s := New([]Rule{{ID: "rev", Source: "/A", Target: "/media"}}, nil)
	res := s.Map("/A/movie.mkv")
	if !res.Matched {
		t.Fatalf("写反的规则仍然是命中的（这正是它静默的原因）")
	}
	if res.Path != "/media/movie.mkv" {
		t.Fatalf("写反后的映射结果不对：%q", res.Path)
	}
	// 源写反时，用户真正想映射的 /media/... 反而命不中。
	if got := s.Map("/media/movie.mkv"); got.Matched {
		t.Fatalf("写反的规则不应匹配 /media/...，实际 %q", got.Path)
	}
}

// 命中统计：改了目标路径不应把「这条规则生效过」这个事实抹掉。
func TestSetRulesKeepsStatsByID(t *testing.T) {
	now := time.Now()
	s := New([]Rule{{ID: "r1", Source: "/media", Target: "/A"}}, func() time.Time { return now })
	s.Map("/media/a.mkv")
	s.Map("/media/b.mkv")
	if st := s.Stat("r1"); st.Hits != 2 || !st.LastHit.Equal(now) || st.LastFrom != "/media/b.mkv" {
		t.Fatalf("命中统计不对：%+v", st)
	}
	s.SetRules([]Rule{{ID: "r1", Source: "/media", Target: "/B"}})
	if st := s.Stat("r1"); st.Hits != 2 {
		t.Fatalf("改目标后命中数被清零了：%+v", st)
	}
	// 换目标后立即按新目标映射。
	if got := s.Map("/media/a.mkv"); got.Path != "/B/a.mkv" {
		t.Fatalf("新目标未生效：%q", got.Path)
	}
	s.SetRules([]Rule{{ID: "r2", Source: "/other", Target: "/C"}})
	// 规则没了统计也要没：ID 复用时否则会继承上一条规则的历史命中数，
	// 让「这条规则生效过」这个结论指向一条已经不存在的规则。
	if st := s.Stat("r1"); st.Hits != 0 {
		t.Fatalf("被删规则的统计应一并清掉：%+v", st)
	}
}

// 同源重复：只警告不阻止（顺序匹配下它是合法配置），但必须报出来。
func TestConflictsReportsDuplicateSourceWithoutBlocking(t *testing.T) {
	in := []Rule{
		{ID: "a", Source: "/media", Target: "/A"},
		{ID: "b", Source: "/media", Target: "/B"},
		{ID: "c", Source: "/media/", Target: "/C"},
	}
	cs := Conflicts(in)
	if len(cs) != 2 {
		t.Fatalf("应当报出 2 处同源冲突，实际 %d：%+v", len(cs), cs)
	}
	if !strings.Contains(cs[0].Reason, "第一条") {
		t.Fatalf("冲突原因没说明「按顺序取第一条」：%q", cs[0].Reason)
	}
	// 但 Normalize 仍然保留第一条，配置照常可用。
	if norm := Normalize(in); len(norm) != 1 || norm[0].ID != "a" {
		t.Fatalf("规整结果不对：%+v", norm)
	}
}

// 坏 JSON 不能让播放链路 500：解析失败返回空列表（= 映射关闭）。
func TestDecodeReturnsEmptyOnGarbage(t *testing.T) {
	if got := Decode("{not json"); got != nil {
		t.Fatalf("坏 JSON 应解析为空，实际 %+v", got)
	}
	if got := Decode(""); got != nil {
		t.Fatalf("空值应解析为空，实际 %+v", got)
	}
	// 两种形状都接受：包装对象与裸数组。
	if got := Decode(`{"rules":[{"id":"a","source":"/m","target":"/A"}]}`); len(got) != 1 {
		t.Fatalf("包装对象形状解析失败：%+v", got)
	}
	if got := Decode(`[{"id":"a","source":"/m","target":"/A"}]`); len(got) != 1 {
		t.Fatalf("裸数组形状解析失败：%+v", got)
	}
}

// Encode → Decode 往返不丢东西。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := []Rule{{ID: "a", Source: "/m", Target: "/A", Note: "备注"}, {ID: "b", Source: "", Target: "/X"}}
	raw, err := Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := Decode(raw)
	if len(got) != 1 {
		t.Fatalf("空源规则应被剔除，实际 %+v", got)
	}
	if got[0].ID != "a" || got[0].Note != "备注" {
		t.Fatalf("往返丢了字段：%+v", got[0])
	}
}

// 命中回调要带 muvyo 那句可检索文本，故障排查时两边能对上。
func TestMapReportsHitsThroughLogger(t *testing.T) {
	var gotSource, gotFrom, gotTo string
	s := New([]Rule{{ID: "a", Source: "/media", Target: "/A"}}, nil)
	s.SetLogger(func(src, from, to string) { gotSource, gotFrom, gotTo = src, from, to })
	s.Map("/media/movie.mkv")
	if gotSource != "/media" || gotFrom != "/media/movie.mkv" || gotTo != "/A/movie.mkv" {
		t.Fatalf("命中回调参数不对：%q %q %q", gotSource, gotFrom, gotTo)
	}
}

// UnusedIDs 标出「库里还留着、但已无对应规则」的 ID，
// 供配置页提示用户这些历史规则已不生效。
func TestUnusedIDs(t *testing.T) {
	if got := UnusedIDs(`{"rules":[{"id":"old","source":"/m","target":"/A"}]}`, []Rule{{ID: "new", Source: "/m", Target: "/B"}}); len(got) != 1 || got[0] != "old" {
		t.Fatalf("UnusedIDs 不对：%+v", got)
	}
}
