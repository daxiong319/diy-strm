package discovery

import "testing"

// 三条词表的语义与优先级：本文件是"预览 = 生产"这一不变量的回归防线。
// 若有人改动 rule_match.go 的判定顺序或原因串，这里必须同步失败。

func TestRuleMatchNoListsPassesEverything(t *testing.T) {
	// 三条词表全空 = 不做过滤，任何标题都放行（不能误伤）
	var f RuleFilter
	if f.RuleFilterActive() {
		t.Fatal("空词表不应被视为启用过滤")
	}
	for _, title := range []string{"", "任意标题", "Some Movie 2024 4K", "无意义文本"} {
		if reason, blocked := f.RuleSkipReason(title, ""); blocked {
			t.Errorf("空词表下 %q 应放行，实际被拦截：%s", title, reason)
		}
	}
}

func TestRuleMatchKeywordsAnyOf(t *testing.T) {
	// message_keywords 是 any-of 闸门：命中任一条即放行，全不命中才拦截
	f := RuleFilter{Keywords: []string{"庆余年", "繁花"}}
	cases := []struct {
		title   string
		blocked bool
	}{
		{"庆余年 第二季 4K", false},
		{"繁花 全集", false},
		{"狂飙 全集", true},
	}
	for _, tc := range cases {
		reason, blocked := f.RuleSkipReason(tc.title, "")
		if blocked != tc.blocked {
			t.Errorf("%q blocked=%v 期望 %v（reason=%q）", tc.title, blocked, tc.blocked, reason)
		}
		if blocked && reason != RuleSkipReasonKeywords {
			t.Errorf("%q 原因应为 %q，实际 %q", tc.title, RuleSkipReasonKeywords, reason)
		}
	}
}

func TestRuleMatchMustContainAllOf(t *testing.T) {
	// must_contain 是 all-of：缺任意一条即拦截，原因是第一条缺失的词
	f := RuleFilter{MustContain: []string{"4K", "国语"}}
	if _, blocked := f.RuleSkipReason("某剧 4K 国语中字", ""); blocked {
		t.Fatal("两条都命中时应放行")
	}
	reason, blocked := f.RuleSkipReason("某剧 4K 粤语", "")
	if !blocked {
		t.Fatal("缺「国语」时不应放行")
	}
	if want := RuleSkipReasonMustContainPrefix + "国语"; reason != want {
		t.Errorf("原因 = %q，期望 %q", reason, want)
	}
	// 缺第一条时原因报第一条，体现列表顺序
	reason2, _ := f.RuleSkipReason("某剧 粤语", "")
	if want := RuleSkipReasonMustContainPrefix + "4K"; reason2 != want {
		t.Errorf("原因 = %q，期望 %q", reason2, want)
	}
}

func TestRuleMatchMustNotContainAnyOf(t *testing.T) {
	// must_not_contain 是 any-of 排除：命中任意一条即拦截
	f := RuleFilter{MustNotContain: []string{"预告", "花絮"}}
	reason, blocked := f.RuleSkipReason("某剧 预告片", "")
	if !blocked {
		t.Fatal("命中排除词时应拦截")
	}
	if want := RuleSkipReasonMustNotContainPrefix + "预告"; reason != want {
		t.Errorf("原因 = %q，期望 %q", reason, want)
	}
	if _, blocked := f.RuleSkipReason("某剧 正片", ""); blocked {
		t.Fatal("未命中排除词时应放行")
	}
}

func TestRuleMatchPrecedenceKeywordsThenMustContainThenMustNotContain(t *testing.T) {
	// 优先级：keywords 先判 → must_contain → must_not_contain。
	// 同时"不该放行"时，必须报**最先**触发的那一条，与生产 continue 顺序一致。
	f := RuleFilter{
		Keywords:       []string{"庆余年"},
		MustContain:    []string{"4K"},
		MustNotContain: []string{"预告"},
	}
	// keywords 不中 → 报 keywords，即使 must_contain 也不满足
	if reason, _ := f.RuleSkipReason("狂飙 1080P 预告", ""); reason != RuleSkipReasonKeywords {
		t.Errorf("应优先报关键词未命中，实际 %q", reason)
	}
	// keywords 命中但 must_contain 不满足 → 报 must_contain
	if reason, _ := f.RuleSkipReason("庆余年 1080P 预告", ""); reason != RuleSkipReasonMustContainPrefix+"4K" {
		t.Errorf("应报 must_contain 缺失，实际 %q", reason)
	}
	// 前两条都过，才轮到 must_not_contain
	if reason, _ := f.RuleSkipReason("庆余年 4K 预告", ""); reason != RuleSkipReasonMustNotContainPrefix+"预告" {
		t.Errorf("应报排除词命中，实际 %q", reason)
	}
	// 三条都过
	if _, blocked := f.RuleSkipReason("庆余年 4K 正片", ""); blocked {
		t.Error("三条均满足时应放行")
	}
}

func TestRuleMatchCaseInsensitive(t *testing.T) {
	// 生产把文本 ToLower、词表逐条 ToLower，故大小写不敏感（两侧都要覆盖）
	f := RuleFilter{
		Keywords:       []string{"WEB-DL"},
		MustContain:    []string{"HDR"},
		MustNotContain: []string{"CAM"},
	}
	if _, blocked := f.RuleSkipReason("Movie web-dl hdr", ""); blocked {
		t.Error("大小写不同不应影响放行判定")
	}
	if reason, _ := f.RuleSkipReason("movie WEB-DL hdr cam", ""); reason != RuleSkipReasonMustNotContainPrefix+"CAM" {
		t.Errorf("排除词应大小写不敏感命中，实际 %q", reason)
	}
	// 词表本身大写、标题小写
	if _, blocked := f.RuleSkipReason("movie web-dl hdr", ""); blocked {
		t.Error("大写的词表应能命中小写标题")
	}
}

func TestRuleMatchUnicodeChineseTitles(t *testing.T) {
	// 中文标题按字节子串匹配即可，字符边界不应出问题
	f := RuleFilter{Keywords: []string{"三体"}, MustNotContain: []string{"解说"}}
	if _, blocked := f.RuleSkipReason("三体 第一季 4K 全集", ""); blocked {
		t.Error("中文关键词应命中")
	}
	if reason, _ := f.RuleSkipReason("三体 第一季 速看解说", ""); reason != RuleSkipReasonMustNotContainPrefix+"解说" {
		t.Errorf("中文排除词应命中，实际 %q", reason)
	}
	if _, blocked := f.RuleSkipReason("流浪地球 2", ""); !blocked {
		t.Error("不含中文关键词时应拦截")
	}
}

func TestRuleMatchRemarkIsPartOfMatchText(t *testing.T) {
	// 生产匹配文本 = 标题 + "\n" + 备注，备注里的词同样能命中/触发排除
	f := RuleFilter{Keywords: []string{"会员"}}
	if _, blocked := f.RuleSkipReason("某剧 4K", "网盘会员分享"); blocked {
		t.Error("备注命中关键词时应放行")
	}
	f2 := RuleFilter{MustNotContain: []string{"失效"}}
	if reason, _ := f2.RuleSkipReason("某剧 4K", "链接已失效"); reason != RuleSkipReasonMustNotContainPrefix+"失效" {
		t.Errorf("备注命中排除词时应拦截，实际 %q", reason)
	}
}

func TestRuleMatchTextIsLowercasedTitleNewlineRemark(t *testing.T) {
	if got, want := RuleMatchText("ABC", "DeF"), "abc\ndef"; got != want {
		t.Errorf("RuleMatchText = %q，期望 %q", got, want)
	}
}

func TestRuleMatchEmptyEntriesInListsAreIgnored(t *testing.T) {
	// 空串词条不应把整批条目误杀（helpers 里 k != "" 的守卫）
	f := RuleFilter{
		Keywords:       []string{"", "庆余年"},
		MustContain:    []string{"", "4K"},
		MustNotContain: []string{"", "预告"},
	}
	if _, blocked := f.RuleSkipReason("庆余年 4K 正片", ""); blocked {
		t.Error("空串词条不应导致拦截")
	}
	if reason, _ := f.RuleSkipReason("狂飙 4K 正片", ""); reason != RuleSkipReasonKeywords {
		t.Errorf("非空关键词未命中应拦截，实际 %q", reason)
	}
}

func TestRuleFilterFromMatchMatchesProductionFieldNames(t *testing.T) {
	// 生产从 rule.MatchData 的三个键取词表（subscriptions.go:593-595）
	match := map[string]any{
		"message_keywords": []any{"庆余年", "繁花"},
		"must_contain":     []any{"4K"},
		"must_not_contain": []string{"预告"},
	}
	f := RuleFilterFromMatch(match)
	if len(f.Keywords) != 2 || f.Keywords[0] != "庆余年" {
		t.Errorf("keywords 解析错误：%#v", f.Keywords)
	}
	if len(f.MustContain) != 1 || f.MustContain[0] != "4K" {
		t.Errorf("must_contain 解析错误：%#v", f.MustContain)
	}
	if len(f.MustNotContain) != 1 || f.MustNotContain[0] != "预告" {
		t.Errorf("must_not_contain 解析错误：%#v", f.MustNotContain)
	}
	// 缺键 = nil，不 panic
	empty := RuleFilterFromMatch(map[string]any{})
	if empty.RuleFilterActive() {
		t.Error("缺键时不应视为启用过滤")
	}
}

// ------------------------------ 预览接口（包级函数） ------------------------------

func TestEvaluateRuleMatchReportsPerTitleVerdicts(t *testing.T) {
	out := EvaluateRuleMatch(&RulePreviewInput{
		Keywords:       []string{"庆余年"},
		MustNotContain: []string{"预告"},
		Titles: []RulePreviewTitle{
			{Title: "庆余年 第二季 4K"},
			{Title: "庆余年 预告片"},
			{Title: "狂飙 全集"},
		},
	})
	if out.Total != 3 || out.PassedCount != 1 || out.BlockedCount != 2 {
		t.Fatalf("汇总错误：total=%d passed=%d blocked=%d", out.Total, out.PassedCount, out.BlockedCount)
	}
	if !out.Active {
		t.Error("有非空词表时 active 应为 true")
	}
	// 顺序必须与提交顺序一致，前端要按行对齐
	if out.Items[0].Blocked || out.Items[0].Reason != "" {
		t.Errorf("第一条应放行，实际 blocked=%v reason=%q", out.Items[0].Blocked, out.Items[0].Reason)
	}
	if !out.Items[1].Blocked || out.Items[1].Reason != RuleSkipReasonMustNotContainPrefix+"预告" {
		t.Errorf("第二条应被排除词拦截，实际 blocked=%v reason=%q", out.Items[1].Blocked, out.Items[1].Reason)
	}
	if !out.Items[2].Blocked || out.Items[2].Reason != RuleSkipReasonKeywords {
		t.Errorf("第三条应报关键词未命中，实际 blocked=%v reason=%q", out.Items[2].Blocked, out.Items[2].Reason)
	}
}

func TestEvaluateRuleMatchEmptyListsPassAll(t *testing.T) {
	out := EvaluateRuleMatch(&RulePreviewInput{
		Titles: []RulePreviewTitle{{Title: "任意标题"}, {Title: "另一条"}},
	})
	if out.Active {
		t.Error("空词表时 active 应为 false")
	}
	if out.BlockedCount != 0 || out.PassedCount != 2 {
		t.Errorf("空词表应全部放行，实际 blocked=%d passed=%d", out.BlockedCount, out.PassedCount)
	}
}

func TestEvaluateRuleMatchSkipsBlankTitles(t *testing.T) {
	// 用户粘贴的文本常有空行，空行不应产生判定记录
	out := EvaluateRuleMatch(&RulePreviewInput{
		Keywords: []string{"庆余年"},
		Titles: []RulePreviewTitle{
			{Title: "   "},
			{Title: ""},
			{Title: "庆余年 4K"},
		},
	})
	if out.Total != 1 {
		t.Fatalf("空行应被跳过，total=%d", out.Total)
	}
	if out.Items[0].Title != "庆余年 4K" {
		t.Errorf("标题应被 TrimSpace 后回显，实际 %q", out.Items[0].Title)
	}
}

func TestEvaluateRuleMatchNilAndPreservesRemark(t *testing.T) {
	if out := EvaluateRuleMatch(nil); out == nil || out.Total != 0 {
		t.Fatal("nil 输入应返回空结果而非 panic")
	}
	out := EvaluateRuleMatch(&RulePreviewInput{
		MustNotContain: []string{"失效"},
		Titles:         []RulePreviewTitle{{Title: "某剧", Remark: "链接失效"}},
	})
	if len(out.Items) != 1 || !out.Items[0].Blocked {
		t.Fatal("备注命中应拦截")
	}
	if out.Items[0].Remark != "链接失效" {
		t.Errorf("备注应回显，实际 %q", out.Items[0].Remark)
	}
	if out.Items[0].MatchText != "某剧\n链接失效" {
		t.Errorf("match_text 错误：%q", out.Items[0].MatchText)
	}
}

func TestEvaluateRuleMatchTrimsAndDropsBlankKeywordEntries(t *testing.T) {
	// normalizeStringList 会 trim 并丢弃空串，空词条不应让整批条目被误杀
	out := EvaluateRuleMatch(&RulePreviewInput{
		Keywords: []string{"  ", "", "  庆余年  "},
		Titles:   []RulePreviewTitle{{Title: "庆余年 4K"}},
	})
	if out.BlockedCount != 0 {
		t.Fatalf("trim 后关键词应命中，实际 blocked=%d reason=%q", out.BlockedCount, out.Items[0].Reason)
	}
}
