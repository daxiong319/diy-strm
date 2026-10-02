package discovery

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"litepan/internal/discover/ddb"
)

// 本文件锁死"UI 写入的规则真的会拦截转存"这条链路：
// SaveSubscription（写 Match JSON）→ attachSubscriptionExtras（读回 MatchData）
// → RuleFilterFromMatch → planAndTransferRuleCandidates 里真正 continue。
// 如果哪天字段在持久化层被丢掉，这里必须失败。

func setupRuleTestDB(t *testing.T) {
	t.Helper()
	if ddb.Db == nil {
		if err := ddb.Init(filepath.Join(t.TempDir(), "rule_test.db"), slog.Default()); err != nil {
			t.Fatalf("初始化测试数据库失败：%v", err)
		}
	}
	for _, table := range []string{"discovery_subscriptions", "discovery_subscription_rules", "discovery_subscription_items"} {
		if err := ddb.Db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Fatalf("清理表 %s 失败：%v", table, err)
		}
	}
	if err := ddb.Db.AutoMigrate(&DiscoverySubscription{}, &DiscoverySubscriptionRule{}, &DiscoverySubscriptionItem{}); err != nil {
		t.Fatalf("建表失败：%v", err)
	}
}

// saveRuleSubscription 建一条订阅 + 一条带词表的规则，返回读回的订阅
func saveRuleSubscription(t *testing.T, rule SubscriptionRulePayload) *DiscoverySubscription {
	t.Helper()
	saved, _, err := SaveSubscription(&SubscriptionUpsertPayload{
		Source: "tmdb", EntityType: "tv", ExternalID: "999001", TMDBID: 999001,
		MediaType: "tv", Title: "视觉过滤测试剧",
		TargetProvider: "123", TransferMode: "auto",
		Rules: []SubscriptionRulePayload{rule},
	})
	if err != nil {
		t.Fatalf("保存订阅失败：%v", err)
	}
	got, err := GetSubscription(saved.ID)
	if err != nil {
		t.Fatalf("读回订阅失败：%v", err)
	}
	return got
}

func TestSavedRuleFieldsRoundTrip(t *testing.T) {
	setupRuleTestDB(t)
	got := saveRuleSubscription(t, SubscriptionRulePayload{
		Name:            "只收 4K 国语",
		MessageKeywords: []string{"庆余年", "繁花"},
		MustContain:     []string{"4K"},
		MustNotContain:  []string{"预告"},
	})
	if len(got.Rules) != 1 {
		t.Fatalf("规则数 = %d，期望 1", len(got.Rules))
	}
	match := got.Rules[0].MatchData

	// 三个键必须都在，且顺序保留
	kw := stringListFromAny(match["message_keywords"])
	if len(kw) != 2 || kw[0] != "庆余年" || kw[1] != "繁花" {
		t.Errorf("message_keywords 未原样读回：%#v", kw)
	}
	mc := stringListFromAny(match["must_contain"])
	if len(mc) != 1 || mc[0] != "4K" {
		t.Errorf("must_contain 未原样读回：%#v", mc)
	}
	mnc := stringListFromAny(match["must_not_contain"])
	if len(mnc) != 1 || mnc[0] != "预告" {
		t.Errorf("must_not_contain 未原样读回：%#v", mnc)
	}
	// 规则名也要留住，否则 UI 里的规则列表会变成空标题
	if got.Rules[0].Name != "只收 4K 国语" {
		t.Errorf("规则名 = %q，期望 %q", got.Rules[0].Name, "只收 4K 国语")
	}
}

func TestSavedRuleDrivesProductionFilter(t *testing.T) {
	setupRuleTestDB(t)
	got := saveRuleSubscription(t, SubscriptionRulePayload{
		Name:            "只收 4K 国语",
		MessageKeywords: []string{"庆余年"},
		MustContain:     []string{"4K"},
		MustNotContain:  []string{"预告"},
	})

	// ★ 关键：从"读回的订阅"里取出规则，构造生产路径实际使用的 filter。
	//   这一步与 planAndTransferRuleCandidates 的第一步逐字等价。
	filter := RuleFilterFromMatch(got.Rules[0].MatchData)
	if !filter.RuleFilterActive() {
		t.Fatal("保存并读回后规则应处于启用状态")
	}

	cases := []struct {
		title   string
		remark  string
		blocked bool
		reason  string
	}{
		{"庆余年 第二季 4K 国语", "", false, ""},
		{"庆余年 第二季 4K 预告", "", true, RuleSkipReasonMustNotContainPrefix + "预告"},
		{"庆余年 第二季 1080P", "", true, RuleSkipReasonMustContainPrefix + "4K"},
		{"狂飙 4K", "", true, RuleSkipReasonKeywords},
	}
	for _, tc := range cases {
		reason, blocked := filter.RuleSkipReason(tc.title, tc.remark)
		if blocked != tc.blocked {
			t.Errorf("%q blocked=%v 期望 %v（reason=%q）", tc.title, blocked, tc.blocked, reason)
			continue
		}
		if reason != tc.reason {
			t.Errorf("%q reason=%q 期望 %q", tc.title, reason, tc.reason)
		}
	}
}

func TestSavedRuleNoneFilterLetsEverythingThrough(t *testing.T) {
	setupRuleTestDB(t)
	// 不设任何词表 = 不过滤（用户只想自动转存，不想被词表约束）
	got := saveRuleSubscription(t, SubscriptionRulePayload{Name: "不设过滤"})
	filter := RuleFilterFromMatch(got.Rules[0].MatchData)
	if filter.RuleFilterActive() {
		t.Fatal("未设置词表时不应启用过滤")
	}
	if _, blocked := filter.RuleSkipReason("任意标题", "任意备注"); blocked {
		t.Error("未设置词表时不应拦截任何条目")
	}
}

func TestSavedRuleBlankEntriesAreNormalizedAway(t *testing.T) {
	setupRuleTestDB(t)
	// UI 里删词条常留下空串，normalizeStringList 应在写入前清掉，
	// 否则空关键词会让 containsAny 恒真/恒假，用户会看到无法解释的结果。
	got := saveRuleSubscription(t, SubscriptionRulePayload{
		Name:            "带空词条",
		MessageKeywords: []string{"庆余年", "", "   "},
		MustContain:     []string{"", "4K"},
	})
	match := got.Rules[0].MatchData
	if kw := stringListFromAny(match["message_keywords"]); len(kw) != 1 || kw[0] != "庆余年" {
		t.Errorf("空词条应被清掉：%#v", kw)
	}
	if mc := stringListFromAny(match["must_contain"]); len(mc) != 1 || mc[0] != "4K" {
		t.Errorf("空词条应被清掉：%#v", mc)
	}
}

func TestSavedRulePreviewMatchesProductionOutcome(t *testing.T) {
	setupRuleTestDB(t)
	got := saveRuleSubscription(t, SubscriptionRulePayload{
		Name:            "预览一致性",
		MessageKeywords: []string{"庆余年"},
		MustContain:     []string{"4K"},
		MustNotContain:  []string{"预告"},
	})
	rule := got.Rules[0]

	// 同一批标题：一边走生产 filter，一边走预览接口，逐条比对判定与原因
	titles := []RulePreviewTitle{
		{Title: "庆余年 4K 国语"},
		{Title: "庆余年 4K 预告"},
		{Title: "庆余年 1080P"},
		{Title: "狂飙 4K"},
	}
	preview := EvaluateRuleMatch(&RulePreviewInput{
		Keywords:       stringListFromAny(rule.MatchData["message_keywords"]),
		MustContain:    stringListFromAny(rule.MatchData["must_contain"]),
		MustNotContain: stringListFromAny(rule.MatchData["must_not_contain"]),
		Titles:         titles,
	})
	prodFilter := RuleFilterFromMatch(rule.MatchData)

	if len(preview.Items) != len(titles) {
		t.Fatalf("预览条目数 = %d，期望 %d", len(preview.Items), len(titles))
	}
	for i, tc := range titles {
		prodReason, prodBlocked := prodFilter.RuleSkipReason(tc.Title, tc.Remark)
		item := preview.Items[i]
		if item.Blocked != prodBlocked {
			t.Errorf("%q 预览 blocked=%v 生产 blocked=%v", tc.Title, item.Blocked, prodBlocked)
		}
		if item.Reason != prodReason {
			t.Errorf("%q 预览 reason=%q 生产 reason=%q", tc.Title, item.Reason, prodReason)
		}
		// 原因串必须是生产原文，前端直接展示给用户
		if item.Blocked && !strings.Contains(item.Reason, "：") && item.Reason != RuleSkipReasonKeywords {
			t.Errorf("%q 的 reason 不像生产原因串：%q", tc.Title, item.Reason)
		}
	}
}
