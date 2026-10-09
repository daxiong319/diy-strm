package classifyorganize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"litepan/internal/mediaorganize/classification"
	"litepan/internal/settings"
)

// ---------------------------------------------------------------------------
// T02 三级分类 / 系列目录 / 结构化条件字段 的行为测试
//
// 这里刻意只测「规则能表达什么、结果长什么样」，不测工具函数的每个分支。
// 归一化函数的校验点由 config 层的用例负责（见下方 TestNormalizeRejects...）。
// ---------------------------------------------------------------------------

// threeLevelCfg 造一份「电影/华语电影/2000-2009」三层配置。
func threeLevelCfg(svc *Service, tertiary []Rule) Config {
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateRegion
	cfg.Templates[1].Rules[0].Children[0].Children = tertiary
	return cfg
}

func mustUpdate(t *testing.T, svc *Service, cfg Config) {
	t.Helper()
	if _, err := svc.Update(context.Background(), cfg); err != nil {
		t.Fatalf("更新配置失败：%v", err)
	}
}

func classifyOK(t *testing.T, svc *Service, req classification.Request) classification.Decision {
	t.Helper()
	decision, err := svc.Classify(context.Background(), req)
	if err != nil {
		t.Fatalf("分类失败：%v", err)
	}
	return decision
}

func joined(segments []string) string { return strings.Join(segments, "/") }

// TestTertiaryLevelProducesYearRangeSegment 验收①：三级分类产出年份区间目录。
func TestTertiaryLevelProducesYearRangeSegment(t *testing.T) {
	svc := newService(t, true)
	loader := &detailLoader{raw: map[string]any{
		"id":             129,
		"origin_country": []string{"CN"},
		"release_date":   "2009-01-01",
	}}
	cfg := threeLevelCfg(svc, []Rule{{
		Name:      "2000-2009",
		Condition: "year=2000-2009",
	}})
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129", Loader: loader,
		Raw: map[string]any{"origin_country": []string{"CN"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/2000-2009"; got != want {
		t.Fatalf("三级路径异常：%s，期望 %s", got, want)
	}
}

// TestTertiaryRuleFieldsAreEquivalentToCondition C-3：结构化字段与
// Condition 表达式描述同一件事时，分类结果必须一致。
//
// 这条是「两者并存」这个决策的护栏：以后有人改坏其中一条解析路径，
// 这里会红，而不是等到用户发现「我填的字段不生效」。
func TestTertiaryRuleFieldsAreEquivalentToCondition(t *testing.T) {
	svc := newService(t, true)
	mustUpdate(t, svc, threeLevelCfg(svc, []Rule{{
		Name:   "2000-2009",
		Fields: &RuleFields{Year: &YearRange{From: 2000, To: 2009}},
	}}))

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{
			"id": 129, "origin_country": []string{"CN"}, "release_date": "2009-01-01",
		}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/2000-2009"; got != want {
		t.Fatalf("结构化年份字段未生效：%s，期望 %s", got, want)
	}

	// 区间外不命中，且不回落 —— 三级未命中时只是不下钻，不产出错误目录。
	decision = classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "130",
		Loader: &detailLoader{raw: map[string]any{
			"id": 130, "origin_country": []string{"CN"}, "release_date": "1999-01-01",
		}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产"; got != want {
		t.Fatalf("区间外应停在二级：%s，期望 %s", got, want)
	}
	if decision.Evidence["tertiary_unmatched"] != true {
		t.Fatalf("三级未命中应在证据里留下痕迹：%+v", decision.Evidence)
	}
}

// TestRuleFieldsFallBackToCondition C-3：结构化字段为空时回落解析 Condition。
func TestRuleFieldsFallBackToCondition(t *testing.T) {
	svc := newService(t, true)
	mustUpdate(t, svc, threeLevelCfg(svc, []Rule{{
		Name:      "早年",
		Condition: "year=1990-1999",
	}}))

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{
			"id": 129, "origin_country": []string{"CN"}, "release_date": "1994-01-01",
		}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/早年"; got != want {
		t.Fatalf("Condition 回落失效：%s，期望 %s", got, want)
	}
}

// TestRequiredAndExcludedValueSyntax C-3：+ 必须命中、- 命中即排除。
func TestRequiredAndExcludedValueSyntax(t *testing.T) {
	svc := newService(t, true)
	mustUpdate(t, svc, threeLevelCfg(svc, []Rule{{
		Name:      "要关键词",
		Condition: "keywords=+流浪地球",
	}}))

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Raw: map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"流浪地球", "太空"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/要关键词"; got != want {
		t.Fatalf("+ 必须命中未生效：%s，期望 %s", got, want)
	}

	miss := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Raw: map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"太空"}},
	})
	if got, want := joined(miss.RelativeSegments), "电影/国产"; got != want {
		t.Fatalf("未命中 + 条件不应命中三级：%s，期望 %s", got, want)
	}

	mustUpdate(t, svc, threeLevelCfg(svc, []Rule{{
		Name:      "排除科幻",
		Condition: "keywords=-科幻",
	}}))
	excluded := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Raw: map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"科幻"}},
	})
	if got, want := joined(excluded.RelativeSegments), "电影/国产"; got != want {
		t.Fatalf("- 排除未生效：%s，期望 %s", got, want)
	}
	kept := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Raw: map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"喜剧"}},
	})
	if got, want := joined(kept.RelativeSegments), "电影/国产/排除科幻"; got != want {
		t.Fatalf("未命中排除项应命中三级：%s，期望 %s", got, want)
	}
}

// TestPrimaryLevelIsFirstMatchWins C-1：主路径改成命中即停。
//
// 原来 primary 走的是「命中值在 TMDB 列表里位置最靠前的规则胜出」。
// 改成 first-match-wins 之后，顺序即优先级，用户拖一下顺序就能换结果 ——
// 而这正是任务书要求对齐 参考实现 的那一条。
func TestPrimaryLevelIsFirstMatchWins(t *testing.T) {
	svc := newService(t, true)
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateCustom
	cfg.Templates[3].Rules = []Rule{
		{Name: "影视", Condition: "type=tv"},
		{Name: "影视2", Condition: "type=movie"},
	}
	mustUpdate(t, svc, cfg)

	for mediaType, want := range map[string]string{"tv": "影视", "movie": "影视2"} {
		decision := classifyOK(t, svc, classification.Request{MediaType: mediaType})
		if got := joined(decision.RelativeSegments); got != want {
			t.Fatalf("%s 应命中 %s，实际 %s", mediaType, want, got)
		}
	}
}

// TestCustomTemplateStillScoresBySpecificity 任务书关键决策①：
// custom 模板保留打分择优路径，删掉它等于把能力砍了。
func TestCustomTemplateStillScoresBySpecificity(t *testing.T) {
	svc := newService(t, true)
	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateCustom
	cfg.Templates[3].Rules = []Rule{
		{Name: "泛泛", Condition: "type=movie"},
		{Name: "国产", Condition: "type=movie，origin_country=CN"},
	}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", Raw: map[string]any{"origin_country": []string{"CN"}},
	})
	if got, want := joined(decision.RelativeSegments), "国产"; got != want {
		t.Fatalf("打分择优未生效：%s，期望 %s", got, want)
	}
	if got := decision.Evidence["match_policy"]; got != "specificity_genres_region_tmdb_order" {
		t.Fatalf("custom 模板的 match_policy 应保持打分语义，实际 %v", got)
	}
}

// TestSecondarySwitchOffRemovesTertiaryAndSeries 验收②：
// 关掉二级，三级与系列必须一起消失。
func TestSecondarySwitchOffRemovesTertiaryAndSeries(t *testing.T) {
	svc := newServiceWithValues(t, map[string]string{
		settings.KeyMOClassificationEnabled:          boolString(true),
		settings.KeyMOClassificationSecondaryEnabled: boolString(false),
	})
	cfg := threeLevelCfg(svc, []Rule{{Name: "2000-2009", Condition: "year=2000-2009"}})
	cfg.Series = []SeriesRule{{Name: "流浪地球", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{"id": 129, "release_date": "2009-01-01"}},
		Raw:    map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"流浪地球"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影"; got != want {
		t.Fatalf("关闭二级后只剩一级：%s，期望 %s", got, want)
	}
}

// TestSeriesSegmentAppendedToPath C-2：系列目录段挂在末尾。
func TestSeriesSegmentAppendedToPath(t *testing.T) {
	svc := newService(t, true)
	cfg := threeLevelCfg(svc, []Rule{{Name: "2000-2009", Condition: "year=2000-2009"}})
	cfg.Series = []SeriesRule{{Name: "流浪地球", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{"id": 129, "release_date": "2009-01-01"}},
		Raw:    map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"流浪地球"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/2000-2009/流浪地球系列"; got != want {
		t.Fatalf("系列目录未追加：%s，期望 %s", got, want)
	}
}

// TestRuleLevelSeriesWinsOverGlobalRule C-2：命中层自带的系列名优先于全局规则。
func TestRuleLevelSeriesWinsOverGlobalRule(t *testing.T) {
	svc := newService(t, true)
	cfg := threeLevelCfg(svc, []Rule{{
		Name: "2000-2009", Condition: "year=2000-2009", Series: "本地指定系列",
	}})
	cfg.Series = []SeriesRule{{Name: "全局系列", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{"id": 129, "release_date": "2009-01-01"}},
		Raw:    map[string]any{"origin_country": []string{"CN"}, "keywords": []string{"流浪地球"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产/2000-2009/本地指定系列"; got != want {
		t.Fatalf("规则自带系列名应优先：%s，期望 %s", got, want)
	}
}

// TestSeriesSwitchOffRemovesSeriesSegment C-4：系列单独开关。
func TestSeriesSwitchOffRemovesSeriesSegment(t *testing.T) {
	svc := newServiceWithValues(t, map[string]string{
		settings.KeyMOClassificationEnabled:       boolString(true),
		settings.KeyMOClassificationSeriesEnabled: boolString(false),
	})
	cfg := threeLevelCfg(svc, nil)
	cfg.Series = []SeriesRule{{Name: "流浪地球", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{raw: map[string]any{"id": 129, "origin_country": []string{"CN"}}},
		Raw:    map[string]any{"keywords": []string{"流浪地球"}},
	})
	if got, want := joined(decision.RelativeSegments), "电影/国产"; got != want {
		t.Fatalf("关闭系列开关后不应追加系列段：%s，期望 %s", got, want)
	}
}

// TestGeneratedSegmentsRejectPathEscape C-6：所有生成段都过 validatePathSegment。
func TestGeneratedSegmentsRejectPathEscape(t *testing.T) {
	svc := newService(t, true)
	for _, bad := range []string{"..", "../逃逸", "a/b", "  "} {
		cfg := svc.Config()
		cfg.Series = []SeriesRule{{Name: "流浪地球", DirName: bad, SeriesKeywords: []string{"x"}}}
		if _, err := svc.Update(context.Background(), cfg); err == nil {
			t.Fatalf("系列目录名 %q 应被拒绝", bad)
		}
	}

	cfg := svc.Config()
	cfg.SelectedTemplate = TemplateRegion
	cfg.Templates[1].Rules[0].Children[0].Children = []Rule{{
		Name: "..", Condition: "year=2000-2009",
	}}
	if _, err := svc.Update(context.Background(), cfg); err == nil {
		t.Fatal("三级目录名 “..” 应被拒绝")
	}
}

// TestFourthLevelIsRejected C-1：三级下面再配一级要报错而不是静默截断。
func TestFourthLevelIsRejected(t *testing.T) {
	svc := newService(t, true)
	cfg := threeLevelCfg(svc, []Rule{{
		Name: "2000-2009", Condition: "year=2000-2009",
		Children: []Rule{{Name: "第四级", Condition: "year=2001"}},
	}})
	if _, err := svc.Update(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "最多支持三级") {
		t.Fatalf("应拒绝第四级，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// C-5 TMDB 三态降级 + 熔断
// ---------------------------------------------------------------------------

func tmdbCfg() map[string]string {
	return map[string]string{
		settings.KeyMOClassificationEnabled:       boolString(true),
		settings.KeyMOClassificationSeriesEnabled: boolString(true),
	}
}

// TestTmdbMissingIsNotUnverified C-5 第一态：压根没配 TMDB 时是
// 「无法查询」而不是「影片不可用」。
func TestTmdbMissingIsNotUnverified(t *testing.T) {
	svc := newServiceWithValues(t, tmdbCfg())
	// 必须挂一个二级规则，否则一层就返回了，压根走不到「要不要查 TMDB」那一步 ——
	// 那条分支在 len(parent.Children) == 0 处就 return 了。
	mustUpdate(t, svc, threeLevelCfg(svc, nil))
	decision := classifyOK(t, svc, classification.Request{MediaType: "movie"})
	if decision.DegradedReason != degradedDetailNoData {
		t.Fatalf("未配 TMDB 时降级原因应为 %s，实际 %q", degradedDetailNoData, decision.DegradedReason)
	}
	if !decision.Matched || joined(decision.RelativeSegments) != "电影" {
		t.Fatalf("未配 TMDB 不影响一级分类：%+v", decision)
	}
}

// TestTmdbFailureAndCircuitBreaker C-5：连续失败到阈值后熔断 60 秒，
// 恢复后不用重启进程就自动回到正常。
//
// 这里同时钉住「熔断期间不重复累加失败计数」—— 熔断自己给自己续命的话，
// 一次 TMDB 抖动会变成永久失效。
func TestTmdbFailureAndCircuitBreaker(t *testing.T) {
	svc := newServiceWithValues(t, tmdbCfg())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })

	cfg := threeLevelCfg(svc, nil)
	cfg.Series = []SeriesRule{{Name: "流浪地球", SeriesKeywords: []string{"流浪地球"}}}
	mustUpdate(t, svc, cfg)

	req := classification.Request{
		MediaType: "movie", TMDBID: "129",
		Loader: &detailLoader{err: errors.New("TMDB 连接被拒绝")},
		Raw:    map[string]any{"keywords": []string{"流浪地球"}},
	}

	var fuseReason string
	for attempt := 1; attempt <= tmdbFuseThreshold; attempt++ {
		decision := classifyOK(t, svc, req)
		if decision.DegradedReason == degradedTmdbUnavailable {
			fuseReason = decision.DegradedReason
		} else if decision.DegradedReason != degradedDetailFailed {
			t.Fatalf("第 %d 次失败应为 %s 或 %s，实际 %q",
				attempt, degradedDetailFailed, degradedTmdbUnavailable, decision.DegradedReason)
		}
	}
	if fuseReason != degradedTmdbUnavailable {
		t.Fatalf("连续 %d 次失败后应熔断并给出 %s", tmdbFuseThreshold, degradedTmdbUnavailable)
	}

	// 熔断期间：TMDB 已经好了也不去查 —— 这正是熔断该有的样子。
	good := &detailLoader{raw: map[string]any{"id": 129, "origin_country": []string{"CN"}}}
	decision := classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129", Loader: good,
		Raw: map[string]any{"keywords": []string{"流浪地球"}},
	})
	if decision.DegradedReason != degradedTmdbUnavailable {
		t.Fatalf("熔断期间应保持 %s，实际 %q", degradedTmdbUnavailable, decision.DegradedReason)
	}
	if good.calls != 0 {
		t.Fatalf("熔断期间不应再请求 TMDB，实际调用 %d 次", good.calls)
	}

	// 60 秒后自动恢复，不需要重启进程。
	now = now.Add(tmdbFuseSeconds * time.Second)
	decision = classifyOK(t, svc, classification.Request{
		MediaType: "movie", TMDBID: "129", Loader: good,
		Raw: map[string]any{"keywords": []string{"流浪地球"}},
	})
	if decision.DegradedReason != "" {
		t.Fatalf("熔断恢复后不应再有降级原因，实际 %q", decision.DegradedReason)
	}
	if good.calls != 1 {
		t.Fatalf("恢复后应重新查询 TMDB，实际调用 %d 次", good.calls)
	}
}

// TestTmdbUnavailableIsDistinctFromNoRuleMatched C-5：
// 「查不到」与「不属于任何类型」必须是两个码。
func TestTmdbUnavailableIsDistinctFromNoRuleMatched(t *testing.T) {
	svc := newServiceWithValues(t, tmdbCfg())
	decision := classifyOK(t, svc, classification.Request{MediaType: "unknown"})
	if decision.DegradedReason != degradedNoRuleMatched {
		t.Fatalf("未知媒体类型应为 %s，实际 %q", degradedNoRuleMatched, decision.DegradedReason)
	}
}

// ---------------------------------------------------------------------------
// C-8 YAML 往返
// ---------------------------------------------------------------------------

// TestYAMLSeriesRoundTrip C-8 + 验收⑦：导出再导入，系列规则不丢。
func TestYAMLSeriesRoundTrip(t *testing.T) {
	svc := newService(t, true)
	cfg := svc.Config()
	cfg.Series = []SeriesRule{
		{Name: "流浪地球", SeriesKeywords: []string{"流浪地球", "The Wandering Earth"}},
		{Name: "星球大战", DirName: "星战系列", MediaType: "movie", Position: 10,
			SeriesKeywords: []string{"星球大战"}, Remark: "含前传"},
	}
	mustUpdate(t, svc, cfg)

	text, err := svc.ExportRulesToYAML(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "series:") || !strings.Contains(text, "The Wandering Earth") {
		t.Fatalf("导出缺少 series 段：\n%s", text)
	}

	// 往返用私有解析器而不是 ImportRulesFromYAML：后者会连同规则表一起
	// replace，而规则表住在 GORM 里，本包没有 DB 脚手架（ddb.Init 是全进程
	// once，装在这里会连带影响 TestHasCustomRulesWithoutDB 的前提）。
	// 验收⑦要验的是「YAML 文本本身能不能把系列规则完整带回来」。
	parsed, err := validateYAMLText(text)
	if err != nil {
		t.Fatalf("重新导入失败：%v", err)
	}
	got := parsed.Series
	if len(got) != 2 {
		t.Fatalf("往返后系列规则应有 2 条，实际 %d：%+v", len(got), got)
	}
	// 星球大战 position=10，排序应落在后面。
	if got[1].Name != "星球大战" || got[1].DirName != "星战系列" || got[1].MediaType != "movie" ||
		got[1].Remark != "含前传" {
		t.Fatalf("往返丢字段：%+v", got[1])
	}
	if len(got[0].SeriesKeywords) != 2 || got[0].SeriesKeywords[0] != "流浪地球" ||
		got[0].SeriesKeywords[1] != "The Wandering Earth" {
		t.Fatalf("关键词往返变形：%v", got[0].SeriesKeywords)
	}
}

// TestLegacyYAMLWithoutSeriesStillLoads 验收⑧：
// 只有一级二级的存量 YAML 必须照常加载。
func TestLegacyYAMLWithoutSeriesStillLoads(t *testing.T) {
	// 存量文件的真实形态：只有一级二级、只用规则文件支持的键（genre_ids，
	// 不是模板二级在用的 genres）、末条可以无条件兜底。
	const legacy = `movie:
  电影/科幻:
    genre_ids: "878"
  电影/国产:
    origin_country: "CN"
tv:
  电视剧/国产剧:
    origin_country: "CN"
`
	parsed, err := validateYAMLText(legacy)
	if err != nil {
		t.Fatalf("存量 YAML 应能加载：%v", err)
	}
	if len(parsed.Rules) != 3 || len(parsed.Warnings) != 0 {
		t.Fatalf("存量 YAML 应解析出 3 条规则、0 条警告：rules=%d warnings=%v",
			len(parsed.Rules), parsed.Warnings)
	}
	// 存量文件的规则必须原样可用：媒体类型与条件值不能被归一化吃掉。
	if parsed.Rules[0].MediaType != "movie" || parsed.Rules[0].TargetPath != "电影/科幻" ||
		parsed.Rules[0].ConditionList[0].Key != "genre_ids" {
		t.Fatalf("存量规则解析变形：%+v", parsed.Rules[0])
	}
	if len(parsed.Series) != 0 {
		t.Fatalf("存量文件不含 series 段时不应凭空造出系列规则：%+v", parsed.Series)
	}
}

// TestYAMLRejectsUnconditionalFallbackOnLastTabOnly 验收④：
// 每 tab 最后一条允许无条件兜底，规则全清报错不静默通过。
func TestYAMLRejectsEmptyRuleFile(t *testing.T) {
	if _, err := validateYAMLText("movie:\n"); err == nil ||
		!strings.Contains(err.Error(), "未从文件解析出任何有效规则") {
		t.Fatalf("规则清空应报错，实际 %v", err)
	}
}

// TestSeriesRuleWithoutKeywordsIsSkipped C-8：
// 无关键词的系列规则对任何影片成立，必须跳过而不是把全库塞进一个目录。
func TestSeriesRuleWithoutKeywordsIsSkipped(t *testing.T) {
	const text = `movie:
  电影/科幻:
    genres: "科幻"
series:
  空规则:
    dir_name: "什么都往这放"
  流浪地球:
    series_keywords: "流浪地球"
`
	parsed, err := validateYAMLText(text)
	if err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	if len(parsed.Series) != 1 || parsed.Series[0].Name != "流浪地球" {
		t.Fatalf("无关键词的系列规则应被跳过：%+v", parsed.Series)
	}
	found := false
	for _, warning := range parsed.Warnings {
		if strings.Contains(warning, "空规则") {
			found = true
		}
	}
	if !found {
		t.Fatalf("跳过时应留下警告：%v", parsed.Warnings)
	}
}
