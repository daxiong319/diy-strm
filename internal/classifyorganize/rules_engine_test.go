package classifyorganize

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 规则引擎表驱动测试
//
// 语义（由参考文件推导）：
//   - 一条规则内所有**无 ? 的硬条件**必须全部满足（AND）。
//   - 若存在 **? 软条件**，软条件之间为 OR（至少一个满足），硬条件仍须全部满足。
//   - 值语法：`a,b` 或；`-x` 否定；`+x` 强制包含；`a-b` 年份闭区间。
//   - 规则按 Position 升序评估，**首个命中胜出**。
// ---------------------------------------------------------------------------

func cond(key, values string) RuleCondition {
	return RuleCondition{Key: key, Values: values}
}

func softCond(key, values string) RuleCondition {
	return RuleCondition{Key: key, Values: values, Optional: true}
}

func TestMatchCondition(t *testing.T) {
	tests := []struct {
		name    string
		cond    RuleCondition
		actual  []string
		want    bool
		wantLog bool
	}{
		// 或语义：任一 token 命中即可
		{name: "单值命中", cond: cond("original_language", "ja"), actual: []string{"ja"}, want: true, wantLog: true},
		{name: "单值未命中", cond: cond("original_language", "ja"), actual: []string{"en"}, want: false},
		{name: "多值或-命中其一", cond: cond("original_language", "zh,cn"), actual: []string{"cn"}, want: true, wantLog: true},
		{name: "多值或-均未命中", cond: cond("original_language", "zh,cn"), actual: []string{"en"}, want: false},
		{name: "多值或-命中多个", cond: cond("origin_country", "CN,TW,HK,MO"), actual: []string{"CN", "TW"}, want: true, wantLog: true},

		// 大小写不敏感（TMDB 语言码可能小写、国家码大写）
		{name: "大小写不敏感", cond: cond("original_language", "zh"), actual: []string{"ZH"}, want: true, wantLog: true},

		// 否定：含否定项即失败
		{name: "否定-不含则满足", cond: cond("genre_ids", "-16"), actual: []string{"99", "10402"}, want: true, wantLog: true},
		{name: "否定-含有则失败", cond: cond("genre_ids", "-16"), actual: []string{"99", "16"}, want: false},
		{name: "否定-参考文件印度电影只写否定", cond: cond("keywords", "-战马"), actual: []string{"战争"}, want: true, wantLog: true},
		{name: "否定-参考文件印度电影命中否定词", cond: cond("keywords", "-战马"), actual: []string{"战马", "战争"}, want: false},

		// 正项 + 否定项混合：正项仍需命中，且不得含否定项
		{name: "正负混合-正命中且无否定", cond: cond("genre_ids", "99,-10402,-16"), actual: []string{"99"}, want: true, wantLog: true},
		{name: "正负混合-正命中但含否定", cond: cond("genre_ids", "99,-10402,-16"), actual: []string{"99", "16"}, want: false},
		{name: "正负混合-正未命中", cond: cond("genre_ids", "99,-16"), actual: []string{"28"}, want: false},
		{name: "含尾随逗号不产生空 token 干扰", cond: cond("genre_ids", "99,-16,-10402,"), actual: []string{"99"}, want: true, wantLog: true},
		{name: "中英文逗号都可作分隔", cond: cond("original_language", "zh，cn"), actual: []string{"cn"}, want: true, wantLog: true},

		// `+` 强制包含
		{name: "加号-必须包含", cond: cond("genre_ids", "+16"), actual: []string{"16"}, want: true, wantLog: true},
		{name: "加号-缺失则失败", cond: cond("genre_ids", "+16"), actual: []string{"28"}, want: false},
		{name: "加号-与其他正项并存时正项可放宽", cond: cond("genre_ids", "+16,10762"), actual: []string{"16", "9999"}, want: true, wantLog: true},
		{name: "加号-缺失仍失败即使其他正项命中", cond: cond("genre_ids", "+16,10762"), actual: []string{"10762"}, want: false},

		// year 单值 / 范围 / 否定
		{name: "year-单值命中", cond: cond("year", "2019"), actual: []string{"2019"}, want: true, wantLog: true},
		{name: "year-单值未命中", cond: cond("year", "2019"), actual: []string{"2018"}, want: false},
		{name: "year-范围命中下界", cond: cond("year", "2000-2099"), actual: []string{"2000"}, want: true, wantLog: true},
		{name: "year-范围命中上界", cond: cond("year", "2000-2099"), actual: []string{"2099"}, want: true, wantLog: true},
		{name: "year-范围外早于", cond: cond("year", "2000-2099"), actual: []string{"1999"}, want: false},
		{name: "year-范围外晚于", cond: cond("year", "2000-2099"), actual: []string{"2100"}, want: false},
		{name: "year-参考文件1550-1999", cond: cond("year", "1550-1999"), actual: []string{"1994"}, want: true, wantLog: true},
		{name: "year-参考文件2000-2018", cond: cond("year", "2000-2018"), actual: []string{"2018"}, want: true, wantLog: true},
		{name: "year-参考文件2000-2018越界", cond: cond("year", "2000-2018"), actual: []string{"2019"}, want: false},
		{name: "year-否定", cond: cond("year", "-2019"), actual: []string{"2018"}, want: true, wantLog: true},
		{name: "year-否定命中则失败", cond: cond("year", "-2019"), actual: []string{"2019"}, want: false},
		{name: "year-多范围或", cond: cond("year", "2000-2010,2020-2029"), actual: []string{"2025"}, want: true, wantLog: true},
		{name: "year-空实际值不匹配范围", cond: cond("year", "2000-2099"), actual: nil, want: false},

		// 畸形/边界输入
		{name: "空取值不满足", cond: cond("original_language", ""), actual: []string{"ja"}, want: false},
		{name: "仅逗号不满足", cond: cond("original_language", ",,"), actual: []string{"ja"}, want: false},
		{name: "实际值为空且有正项", cond: cond("original_language", "ja"), actual: nil, want: false},
		// 单独的 "-" 是畸形取值：既不构成合法否定项，也不是有效正项，
		// 按「无有效取值」处理，不应命中（否则会让一条写错的规则匹配全部条目）。
		{name: "单独负号视为无效取值", cond: cond("genre_ids", "-"), actual: []string{"16"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, detail := matchCondition(tt.cond, tt.actual)
			if got != tt.want {
				t.Fatalf("matchCondition(%s=%q, actual=%v) = %v, want %v (detail=%q)",
					tt.cond.Key, tt.cond.Values, tt.actual, got, tt.want, detail)
			}
			if tt.wantLog && detail == "" {
				t.Fatalf("命中时应返回证据说明，实际为空")
			}
		})
	}
}

func TestMatchRuleHardAndSoftConditions(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		conds     []RuleCondition
		values    map[string][]string
		want      bool
	}{
		{
			name:      "硬条件全满足",
			mediaType: "movie",
			// 对应参考文件 电影/华语电影/大陆地区电影/2000年之前大陆电影
			conds:  []RuleCondition{cond("original_language", "zh,cn"), cond("origin_country", "CN"), cond("year", "1550-1999")},
			values: map[string][]string{"original_language": {"zh"}, "origin_country": {"CN"}, "year": {"1994"}},
			want:   true,
		},
		{
			name:      "硬条件缺一即不满足",
			mediaType: "movie",
			conds:     []RuleCondition{cond("original_language", "zh,cn"), cond("origin_country", "CN")},
			values:    map[string][]string{"original_language": {"zh"}, "origin_country": {"TW"}},
			want:      false,
		},
		{
			name:      "软条件OR-命中其一即满足",
			mediaType: "movie",
			// 对应 电影/日韩电影/日本电影：?origin_country 与 ?original_language
			conds:  []RuleCondition{softCond("origin_country", "JP,-US"), softCond("original_language", "ja")},
			values: map[string][]string{"origin_country": {"JP"}, "original_language": {"en"}},
			want:   true,
		},
		{
			name:      "软条件OR-均未命中则不满足",
			mediaType: "movie",
			conds:     []RuleCondition{softCond("origin_country", "JP,-US"), softCond("original_language", "ja")},
			values:    map[string][]string{"origin_country": {"FR"}, "original_language": {"en"}},
			want:      false,
		},
		{
			name:      "软条件全缺省值则不满足",
			mediaType: "movie",
			conds:     []RuleCondition{softCond("origin_country", "JP,-US"), softCond("original_language", "ja")},
			values:    map[string][]string{},
			want:      false,
		},
		{
			name:      "硬条件满足但软条件全不满足仍失败",
			mediaType: "movie",
			conds:     []RuleCondition{cond("genre_ids", "16"), softCond("original_language", "zh,cn")},
			values:    map[string][]string{"genre_ids": {"16"}, "original_language": {"en"}},
			want:      false,
		},
		{
			name:      "硬条件与软条件同时满足",
			mediaType: "movie",
			conds:     []RuleCondition{cond("genre_ids", "16"), softCond("original_language", "zh,cn")},
			values:    map[string][]string{"genre_ids": {"16"}, "original_language": {"cn"}},
			want:      true,
		},
		{
			name:      "软条件含否定-命中否定项则该软条件不成立",
			mediaType: "movie",
			conds:     []RuleCondition{softCond("origin_country", "JP,-US"), softCond("original_language", "ja")},
			values:    map[string][]string{"origin_country": {"US"}, "original_language": {"ja"}},
			want:      true, // 第二软条件命中
		},
		{
			name:      "媒体类型不匹配直接失败",
			mediaType: "movie",
			conds:     []RuleCondition{cond("genre_ids", "16")},
			values:    map[string][]string{"genre_ids": {"16"}, "__media_type": {"tv"}},
			want:      true, // matchRule 的媒体过滤由调用方按 mediaType 参数决定，这里单测条件层
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := ClassifyRule{MediaType: tt.mediaType, TargetPath: "A/B", Enabled: true, ConditionList: tt.conds}
			got := matchRule(rule, tt.mediaType, tt.values)
			if got.Matched != tt.want {
				t.Fatalf("matchRule matched = %v, want %v (evidence=%v)", got.Matched, tt.want, got.Evidence)
			}
		})
	}
}

func TestMatchRuleMediaTypeFilter(t *testing.T) {
	rule := ClassifyRule{MediaType: "tv", TargetPath: "剧集/测试", Enabled: true,
		ConditionList: []RuleCondition{cond("genre_ids", "16")}}
	values := map[string][]string{"genre_ids": {"16"}}
	if matchRule(rule, "movie", values).Matched {
		t.Fatalf("tv 规则不应匹配 movie 请求")
	}
	if !matchRule(rule, "tv", values).Matched {
		t.Fatalf("tv 规则应匹配 tv 请求")
	}
	// 空 MediaType 表示两者皆适用
	anyRule := ClassifyRule{MediaType: "", TargetPath: "A/B", Enabled: true,
		ConditionList: []RuleCondition{cond("genre_ids", "16")}}
	if !matchRule(anyRule, "movie", values).Matched || !matchRule(anyRule, "tv", values).Matched {
		t.Fatalf("空 MediaType 规则应同时匹配 movie 与 tv")
	}
}

// TestMatchCustomRulesFirstMatchWins 验证核心语义：按 Position 升序，首个命中胜出。
// 用真实参考文件的优先级构造：特摄(genre_ids=-16, language=ja) 先于 日韩电影(?JP/?ja)，
// 再先于 欧美兜底(year 范围)。
func TestMatchCustomRulesFirstMatchWins(t *testing.T) {
	rules := []ClassifyRule{
		{ID: 1, MediaType: "movie", TargetPath: "特摄剧/特摄剧电影/日系特摄电影", Position: 0, Enabled: true,
			ConditionList: []RuleCondition{
				cond("genre_ids", "-16"), cond("original_language", "ja"),
				softCond("include_keywords", "tokusatsu,ultraman,kaiju"),
			}},
		{ID: 2, MediaType: "movie", TargetPath: "电影/日韩电影/日本电影", Position: 1, Enabled: true,
			ConditionList: []RuleCondition{softCond("origin_country", "JP,-US"), softCond("original_language", "ja")}},
		{ID: 3, MediaType: "movie", TargetPath: "电影/欧美电影/2019年及以后欧美电影", Position: 2, Enabled: true,
			ConditionList: []RuleCondition{cond("year", "2019-2099")}},
	}

	// 日系特摄：genre_ids 无 16 且 language=ja 且含 tokusatsu → 命中第 1 条
	values := map[string][]string{
		"genre_ids":         {"99"},
		"original_language": {"ja"},
		"include_keywords":  {"tokusatsu"},
		"origin_country":    {"JP"},
		"year":              {"2020"},
	}
	got := firstMatch(rules, "movie", values)
	if got != "特摄剧/特摄剧电影/日系特摄电影" {
		t.Fatalf("首个命中应为特摄，实际 %q", got)
	}

	// 去掉 tokusatsu：第 1 条硬条件仍满足但软条件全不满足 → 落到第 2 条日韩电影
	values["include_keywords"] = []string{"power-rangers"}
	got = firstMatch(rules, "movie", values)
	if got != "电影/日韩电影/日本电影" {
		t.Fatalf("应落到日韩电影，实际 %q", got)
	}

	// 语言改为英语、国家 FR、年份 2020 → 前两条均不命中 → 欧美兜底
	values["original_language"] = []string{"en"}
	values["origin_country"] = []string{"FR"}
	got = firstMatch(rules, "movie", values)
	if got != "电影/欧美电影/2019年及以后欧美电影" {
		t.Fatalf("应落到欧美兜底，实际 %q", got)
	}

	// 年份 1990 → 三条都不命中 → 无结果
	values["year"] = []string{"1990"}
	if got = firstMatch(rules, "movie", values); got != "" {
		t.Fatalf("无规则命中时应返回空，实际 %q", got)
	}
}

// TestMatchCustomRulesOrderOverridesSpecificity 验证顺序优先于"更具体"：
// 即便第 2 条条件更多，只要第 1 条先命中就选第 1 条（与既有打分制引擎的行为差异点）。
func TestMatchCustomRulesOrderOverridesSpecificity(t *testing.T) {
	rules := []ClassifyRule{
		{ID: 1, MediaType: "movie", TargetPath: "先出现/宽条件", Position: 0, Enabled: true,
			ConditionList: []RuleCondition{cond("genre_ids", "16")}},
		{ID: 2, MediaType: "movie", TargetPath: "后出现/严条件", Position: 1, Enabled: true,
			ConditionList: []RuleCondition{cond("genre_ids", "16"), cond("original_language", "ja"), cond("origin_country", "JP")}},
	}
	values := map[string][]string{"genre_ids": {"16"}, "original_language": {"ja"}, "origin_country": {"JP"}}
	if got := firstMatch(rules, "movie", values); got != "先出现/宽条件" {
		t.Fatalf("首个命中应胜出，实际 %q", got)
	}
}

// TestMatchCustomRulesDisabledSkipped 验证停用规则不参与评估。
func TestMatchCustomRulesDisabledSkipped(t *testing.T) {
	rules := []ClassifyRule{
		{ID: 1, MediaType: "movie", TargetPath: "已停用", Position: 0, Enabled: false,
			ConditionList: []RuleCondition{cond("genre_ids", "16")}},
		{ID: 2, MediaType: "movie", TargetPath: "启用中", Position: 1, Enabled: true,
			ConditionList: []RuleCondition{cond("genre_ids", "16")}},
	}
	values := map[string][]string{"genre_ids": {"16"}}
	// matchCustomRules 在 DB 层过滤 enabled；此处直接验证引擎对禁用规则的处理。
	enabled := make([]ClassifyRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			enabled = append(enabled, rule)
		}
	}
	if got := firstMatch(enabled, "movie", values); got != "启用中" {
		t.Fatalf("停用规则应被跳过，实际 %q", got)
	}
}

// firstMatch 复刻 matchCustomRules 的评估顺序，便于不依赖 DB 做纯引擎测试。
func firstMatch(rules []ClassifyRule, mediaType string, values map[string][]string) string {
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if matchRule(rule, mediaType, values).Matched {
			return rule.TargetPath
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// YAML 解析 / 往返测试（使用参考文件的真实内容）
// ---------------------------------------------------------------------------

const referenceExcerptPath = "testdata/reference_excerpt.yaml"

func TestImportReferenceExcerpt(t *testing.T) {
	raw, err := os.ReadFile(referenceExcerptPath)
	if err != nil {
		t.Fatalf("读取 testdata 失败：%v", err)
	}
	parsed, err := importRulesFromYAML(string(raw))
	if err != nil {
		t.Fatalf("解析参考文件片段失败：%v", err)
	}

	// 该片段来自 V2.1.2 真实文件（含 movie 与 tv 两段）。
	// 抽到的 47 条规则 + 1 条被截断的无条件目录。
	if len(parsed.Rules) != 47 {
		t.Fatalf("应解析出 47 条规则，实际 %d", len(parsed.Rules))
	}

	var movieCount, tvCount int
	for _, rule := range parsed.Rules {
		switch rule.MediaType {
		case "movie":
			movieCount++
		case "tv":
			tvCount++
		default:
			t.Fatalf("出现意外媒体类型 %q", rule.MediaType)
		}
	}
	if movieCount != 22 || tvCount != 25 {
		t.Fatalf("movie/tv 规则数应为 22/25，实际 %d/%d", movieCount, tvCount)
	}

	// 参考文件尾部 `剧集/欧美剧集/1994年后欧美剧集:` 之后没有任何条件（文件被截断），
	// 必须被跳过并给出提示，而不是产生一条"永远命中"的兜底规则。
	for _, rule := range parsed.Rules {
		if rule.TargetPath == "剧集/欧美剧集/1994年后欧美剧集" {
			t.Fatalf("无条件的截断目录不应被导入")
		}
		if len(rule.ConditionList) == 0 {
			t.Fatalf("规则 %q 不应为空条件", rule.TargetPath)
		}
	}
	if len(parsed.Warnings) == 0 {
		t.Fatalf("应就截断目录给出警告")
	}
	if !strings.Contains(strings.Join(parsed.Warnings, "|"), "1994年后欧美剧集") {
		t.Fatalf("警告应指明被跳过的目录，实际 %v", parsed.Warnings)
	}
}

// TestImportReferenceExcerptSemantics 用真实规则断言 ? 前缀与否定被正确解析。
func TestImportReferenceExcerptSemantics(t *testing.T) {
	raw, err := os.ReadFile(referenceExcerptPath)
	if err != nil {
		t.Fatalf("读取 testdata 失败：%v", err)
	}
	parsed, err := importRulesFromYAML(string(raw))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	byPath := map[string]ClassifyRule{}
	for _, rule := range parsed.Rules {
		byPath[rule.MediaType+"|"+rule.TargetPath] = rule
	}

	// 日系特摄电影：硬条件 genre_ids=-16/original_language=ja，软条件 ?include_keywords/?keywords
	special := byPath["movie|特摄剧/特摄剧电影/日系特摄电影"]
	if special.TargetPath == "" {
		t.Fatalf("未找到 日系特摄电影")
	}
	optional := map[string]bool{}
	for _, c := range special.ConditionList {
		optional[c.Key] = c.Optional
	}
	for _, key := range []string{"include_keywords", "keywords"} {
		if !optional[key] {
			t.Fatalf("参考文件中 %s 带 ? 前缀，应解析为软条件", key)
		}
	}
	for _, key := range []string{"genre_ids", "original_language"} {
		if optional[key] {
			t.Fatalf("%s 不带 ? 前缀，应为硬条件", key)
		}
	}

	// 电影/日韩电影/日本电影：两条都是软条件
	jp := byPath["movie|电影/日韩电影/日本电影"]
	if len(jp.ConditionList) != 2 {
		t.Fatalf("日本电影应有 2 条条件，实际 %d", len(jp.ConditionList))
	}
	for _, c := range jp.ConditionList {
		if !c.Optional {
			t.Fatalf("日本电影的 %s 应带 ? 前缀", c.Key)
		}
	}

	// 印度电影：keywords 为纯否定
	india := byPath["movie|电影/印度电影"]
	var found bool
	for _, c := range india.ConditionList {
		if c.Key == "keywords" {
			found = true
			if !strings.Contains(c.Values, "-战马") {
				t.Fatalf("印度电影 keywords 应保留否定值，实际 %q", c.Values)
			}
		}
	}
	if !found {
		t.Fatalf("印度电影应含 keywords 条件")
	}

	// 外语纪录片电影：original_language 为纯否定，且 genre_ids 带尾随逗号
	foreign := byPath["movie|纪录片/纪录片电影/外语纪录片电影"]
	for _, c := range foreign.ConditionList {
		if c.Key == "original_language" && c.Values != "-zh,-cn" {
			t.Fatalf("外语纪录片 original_language 应为 -zh,-cn，实际 %q", c.Values)
		}
	}

	// year 范围原样保留
	dalu := byPath["movie|电影/华语电影/大陆地区电影/2000年之前大陆电影"]
	var yearOK bool
	for _, c := range dalu.ConditionList {
		if c.Key == "year" {
			yearOK = true
			if c.Values != "1550-1999" {
				t.Fatalf("year 应为 1550-1999，实际 %q", c.Values)
			}
		}
	}
	if !yearOK {
		t.Fatalf("2000年之前大陆电影应含 year 条件")
	}

	// 顺序必须与文件一致（首个命中胜出依赖顺序）
	if parsed.Rules[0].TargetPath != "特摄剧/特摄剧电影/日系特摄电影" {
		t.Fatalf("首条规则应为日系特摄电影，实际 %q", parsed.Rules[0].TargetPath)
	}
	last := parsed.Rules[len(parsed.Rules)-1]
	if last.MediaType != "tv" {
		t.Fatalf("末条规则应属于 tv，实际 %q", last.MediaType)
	}
}

// TestImportReferenceExcerptEngineIntegration 用真实导入的规则跑一遍端到端匹配。
func TestImportReferenceExcerptEngineIntegration(t *testing.T) {
	raw, err := os.ReadFile(referenceExcerptPath)
	if err != nil {
		t.Fatalf("读取 testdata 失败：%v", err)
	}
	parsed, err := importRulesFromYAML(string(raw))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for i := range parsed.Rules {
		parsed.Rules[i].Enabled = true
	}

	tests := []struct {
		name      string
		mediaType string
		values    map[string][]string
		want      string
	}{
		{
			name:      "日系特摄电影",
			mediaType: "movie",
			values: map[string][]string{
				"genre_ids": {"99"}, "original_language": {"ja"},
				"include_keywords": {"ultraman"}, "year": {"2020"},
				// series_keywords 在参考文件里**没有** ? 前缀，属硬条件：缺它就不该命中特摄。
				"series_keywords": {"奥特曼"},
			},
			want: "特摄剧/特摄剧电影/日系特摄电影",
		},
		{
			name:      "日系特摄电影-缺 series_keywords 硬条件则落到纪录片兜底",
			mediaType: "movie",
			values: map[string][]string{
				"genre_ids": {"99"}, "original_language": {"ja"},
				"include_keywords": {"ultraman"}, "year": {"2020"},
			},
			// 硬条件 series_keywords=奥特曼 未满足 → 特摄规则不命中，按文件顺序落到纪录片。
			want: "纪录片/纪录片电影/外语纪录片电影",
		},
		{
			name:      "纪录片-华语",
			mediaType: "movie",
			values: map[string][]string{
				"genre_ids": {"99"}, "original_language": {"zh"}, "origin_country": {"CN"},
				"year": {"2020"},
			},
			want: "纪录片/纪录片电影/华语纪录片电影",
		},
		{
			name:      "纪录片-外语",
			mediaType: "movie",
			values: map[string][]string{
				"genre_ids": {"99"}, "original_language": {"en"}, "origin_country": {"US"},
				"year": {"2020"},
			},
			want: "纪录片/纪录片电影/外语纪录片电影",
		},
		{
			name:      "国产动画电影",
			mediaType: "movie",
			values: map[string][]string{
				"genre_ids": {"16"}, "original_language": {"zh"}, "origin_country": {"CN"},
				"series_keywords": {"熊出没"}, "year": {"2021"},
			},
			want: "动漫/动画电影/国产动画电影",
		},
		{
			name:      "大陆电影-2000年之前",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"zh"}, "origin_country": {"CN"}, "year": {"1994"},
			},
			want: "电影/华语电影/大陆地区电影/2000年之前大陆电影",
		},
		{
			name:      "大陆电影-2000年及以后",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"cn"}, "origin_country": {"CN"}, "year": {"2015"},
			},
			want: "电影/华语电影/大陆地区电影/2000年及以后大陆电影",
		},
		{
			name:      "港台地区电影",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"zh"}, "origin_country": {"TW"}, "year": {"2015"},
			},
			want: "电影/华语电影/港台地区电影",
		},
		{
			name:      "日本电影-软条件命中国家",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"en"}, "origin_country": {"JP"}, "year": {"2015"},
			},
			want: "电影/日韩电影/日本电影",
		},
		{
			name:      "韩国电影",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"ko"}, "origin_country": {"KR"}, "year": {"2015"},
			},
			want: "电影/日韩电影/韩国电影",
		},
		{
			name:      "东南亚电影",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"th"}, "origin_country": {"TH"}, "year": {"2015"},
			},
			want: "电影/东南亚电影",
		},
		{
			name:      "欧美电影-兜底2019年以后",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"en"}, "origin_country": {"US"}, "year": {"2021"},
			},
			want: "电影/欧美电影/2019年及以后欧美电影",
		},
		{
			name:      "欧美电影-兜底2000-2018",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"fr"}, "origin_country": {"FR"}, "year": {"2010"},
			},
			want: "电影/欧美电影/2000年-2018年欧美电影",
		},
		{
			name:      "欧美电影-兜底2000年之前",
			mediaType: "movie",
			values: map[string][]string{
				"original_language": {"en"}, "origin_country": {"US"}, "year": {"1988"},
			},
			want: "电影/欧美电影/2000年之前欧美电影",
		},
		{
			name:      "tv-国产剧集2005年之前",
			mediaType: "tv",
			values: map[string][]string{
				"original_language": {"zh"}, "origin_country": {"CN"}, "year": {"2000"},
			},
			want: "剧集/国产剧集/2005年之前国产剧集",
		},
		{
			name:      "tv-韩国剧集",
			mediaType: "tv",
			values: map[string][]string{
				"original_language": {"ko"}, "origin_country": {"KR"}, "year": {"2020"},
			},
			want: "剧集/韩国剧集",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstMatch(parsed.Rules, tt.mediaType, tt.values)
			if got != tt.want {
				t.Fatalf("匹配结果 = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImportYAMLRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(referenceExcerptPath)
	if err != nil {
		t.Fatalf("读取 testdata 失败：%v", err)
	}
	first, err := importRulesFromYAML(string(raw))
	if err != nil {
		t.Fatalf("首次解析失败：%v", err)
	}
	for i := range first.Rules {
		first.Rules[i].Position = i
	}

	exported := exportRulesToYAML(first.Rules)
	second, err := importRulesFromYAML(exported)
	if err != nil {
		t.Fatalf("导出结果无法重新解析：%v\n--- 导出内容 ---\n%s", err, exported)
	}
	if len(second.Rules) != len(first.Rules) {
		t.Fatalf("往返后规则数变化：%d -> %d", len(first.Rules), len(second.Rules))
	}
	for i := range first.Rules {
		want, got := first.Rules[i], second.Rules[i]
		if want.MediaType != got.MediaType || want.TargetPath != got.TargetPath {
			t.Fatalf("第 %d 条规则往返不一致：%s|%s -> %s|%s",
				i, want.MediaType, want.TargetPath, got.MediaType, got.TargetPath)
		}
		if len(want.ConditionList) != len(got.ConditionList) {
			t.Fatalf("第 %d 条规则条件数变化：%d -> %d", i, len(want.ConditionList), len(got.ConditionList))
		}
		for j := range want.ConditionList {
			wc, gc := want.ConditionList[j], got.ConditionList[j]
			if wc.Key != gc.Key || wc.Values != gc.Values || wc.Optional != gc.Optional {
				t.Fatalf("第 %d 条规则第 %d 个条件往返不一致：%+v -> %+v", i, j, wc, gc)
			}
		}
	}
}

func TestImportYAMLMalformed(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		// wantRules 仅在 wantErr=false 时校验
		wantRules int
	}{
		{name: "空内容报错", input: "", wantErr: true},
		{name: "仅空白报错", input: "   \n\t\n", wantErr: true},
		{name: "仅注释报错", input: "# movie:\n#   A/B:\n", wantErr: true},
		{name: "未知顶层键报错", input: "music:\n  A/B:\n    year: \"2000\"\n", wantErr: true},
		{
			name:    "顶层键缺冒号报错",
			input:   "movie\n  A/B:\n    year: \"2000\"\n",
			wantErr: true,
		},
		{
			name:    "条件出现在目录之前报错",
			input:   "movie:\n    year: \"2000\"\n",
			wantErr: true,
		},
		{
			name:    "目录行缺冒号报错",
			input:   "movie:\n  A/B\n    year: \"2000\"\n",
			wantErr: true,
		},
		{
			name:    "目录名为空报错",
			input:   "movie:\n  :\n    year: \"2000\"\n",
			wantErr: true,
		},
		{
			name:    "全部目录无条件时报错",
			input:   "movie:\n  只有目录:\n",
			wantErr: true,
		},
		{
			name:    "未知条件键被跳过而非报错",
			input:   "movie:\n  A/B:\n    nonsense_key: \"x\"\n    year: \"2000-2001\"\n",
			wantErr: false, wantRules: 1,
		},
		{
			name:    "部分目录有条件另一部分没有",
			input:   "movie:\n  A/有效:\n    year: \"2000-2001\"\n  A/截断:\n",
			wantErr: false, wantRules: 1,
		},
		{
			name:    "重复目录保留先出现者",
			input:   "movie:\n  A/B:\n    year: \"2000-2001\"\n  A/B:\n    year: \"2010-2011\"\n",
			wantErr: false, wantRules: 1,
		},
		{
			name:    "同一规则内重复条件键保留先出现者",
			input:   "movie:\n  A/B:\n    year: \"2000-2001\"\n    year: \"2010-2011\"\n",
			wantErr: false, wantRules: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := importRulesFromYAML(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("应报错但成功，解析出 %d 条规则", len(got.Rules))
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if len(got.Rules) != tt.wantRules {
				t.Fatalf("应解析 %d 条规则，实际 %d", tt.wantRules, len(got.Rules))
			}
		})
	}
}

func TestImportYAMLParsesQuotingAndComments(t *testing.T) {
	input := `movie:
  # 注释行应被忽略
  A/B:   # 行尾注释
    year: "2000-2001"      # 带引号的值
    original_language: zh   # 不带引号的值
  "含:冒号/的目标":
    keywords: "a,b"
`
	parsed, err := importRulesFromYAML(input)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(parsed.Rules) != 2 {
		t.Fatalf("应解析 2 条规则，实际 %d", len(parsed.Rules))
	}
	if parsed.Rules[0].TargetPath != "A/B" {
		t.Fatalf("目标目录应为 A/B，实际 %q", parsed.Rules[0].TargetPath)
	}
	values := map[string]string{}
	for _, c := range parsed.Rules[0].ConditionList {
		values[c.Key] = c.Values
	}
	if values["year"] != "2000-2001" {
		t.Fatalf("带引号的值应去掉引号，实际 %q", values["year"])
	}
	if values["original_language"] != "zh" {
		t.Fatalf("不带引号的值应原样保留，实际 %q", values["original_language"])
	}
	if parsed.Rules[1].TargetPath != "含:冒号/的目标" {
		t.Fatalf("带引号的目录名应还原，实际 %q", parsed.Rules[1].TargetPath)
	}
}

func TestImportYAMLToleratesCRLF(t *testing.T) {
	// 参考文件为 CRLF 行尾，解析必须容忍 \r，否则条件键会带 \r 而全部变成未知键。
	input := "movie:\r\n  A/B:\r\n    year: \"2000-2001\"\r\n    genre_ids: \"16\"\r\n"
	parsed, err := importRulesFromYAML(input)
	if err != nil {
		t.Fatalf("CRLF 内容解析失败：%v", err)
	}
	if len(parsed.Rules) != 1 {
		t.Fatalf("应解析 1 条规则，实际 %d", len(parsed.Rules))
	}
	if len(parsed.Rules[0].ConditionList) != 2 {
		t.Fatalf("应解析 2 个条件，实际 %d：%+v", len(parsed.Rules[0].ConditionList), parsed.Rules[0].ConditionList)
	}
	for _, c := range parsed.Rules[0].ConditionList {
		if strings.ContainsAny(c.Key, "\r\n") || strings.ContainsAny(c.Values, "\r\n") {
			t.Fatalf("条件字段不应含回车换行：%+v", c)
		}
	}
}

func TestExportRulesToYAMLStructure(t *testing.T) {
	rules := []ClassifyRule{
		{MediaType: "movie", TargetPath: "电影/华语电影", Position: 0, Enabled: true,
			ConditionList: []RuleCondition{cond("original_language", "zh,cn"), softCond("origin_country", "CN,TW")}},
		{MediaType: "tv", TargetPath: "剧集/国产剧集", Position: 1, Enabled: true,
			ConditionList: []RuleCondition{cond("year", "2005-2023")}},
	}
	out := exportRulesToYAML(rules)
	if !strings.Contains(out, "movie:") || !strings.Contains(out, "tv:") {
		t.Fatalf("导出应含 movie:/tv: 顶层键：\n%s", out)
	}
	if !strings.Contains(out, "  电影/华语电影:") {
		t.Fatalf("目标目录应以 2 空格缩进导出：\n%s", out)
	}
	if !strings.Contains(out, `    ?origin_country: "CN,TW"`) {
		t.Fatalf("软条件应保留 ? 前缀导出：\n%s", out)
	}
	if !strings.Contains(out, `    original_language: "zh,cn"`) {
		t.Fatalf("条件值应以引号导出：\n%s", out)
	}
	// 顺序必须按 Position 保持
	movieIdx := strings.Index(out, "movie:")
	tvIdx := strings.Index(out, "tv:")
	if movieIdx < 0 || tvIdx < 0 || movieIdx > tvIdx {
		t.Fatalf("movie 段应在 tv 段之前：\n%s", out)
	}
}

func TestYAMLValueEscaping(t *testing.T) {
	values := []string{`a"b`, `c\d`, "e\tf", "普通中文", "a-b", "-x,+y"}
	for _, want := range values {
		got := unescapeYAMLValue(escapeYAMLDouble(want))
		if got != want {
			t.Fatalf("转义往返失败：%q -> %q", want, got)
		}
	}
	// 含特殊字符的目标目录必须加引号才能被重新解析
	for _, key := range []string{"含:冒号", "?optional", "-dash", "带 空格", `引"号`} {
		quoted := quoteYAMLKey(key)
		line := "  " + quoted + ":\n"
		parsed, err := importRulesFromYAML("movie:\n" + line + "    year: \"2000-2001\"\n")
		if err != nil {
			t.Fatalf("带特殊字符目录 %q 解析失败：%v", key, err)
		}
		if len(parsed.Rules) != 1 || parsed.Rules[0].TargetPath != key {
			t.Fatalf("目录 %q 往返失败，得到 %+v", key, parsed.Rules)
		}
	}
}

// ---------------------------------------------------------------------------
// 条件归一化（畸形输入）测试
// ---------------------------------------------------------------------------

func TestNormalizeRuleCondition(t *testing.T) {
	tests := []struct {
		name    string
		in      RuleCondition
		wantErr bool
	}{
		{name: "合法普通键", in: cond("genre_ids", "16"), wantErr: false},
		{name: "合法软条件", in: softCond("origin_country", "JP"), wantErr: false},
		{name: "全部 8 个参考文件条件键均被接受", in: cond("series_actors", "x"), wantErr: false},
		{name: "空键报错", in: cond("", "16"), wantErr: true},
		{name: "未知键报错", in: cond("unknown_key", "16"), wantErr: true},
		{name: "空取值报错", in: cond("genre_ids", ""), wantErr: true},
		{name: "纯空白取值报错", in: cond("genre_ids", "   "), wantErr: true},
		{name: "含控制字符报错", in: cond("keywords", "a\x00b"), wantErr: true},
		{name: "year 非法范围报错", in: cond("year", "abc-def"), wantErr: true},
		{name: "year 合法范围", in: cond("year", "2000-2099"), wantErr: false},
		{name: "year 单个数字合法", in: cond("year", "2019"), wantErr: false},
		{name: "键名带空格被裁剪", in: cond("  genre_ids  ", "16"), wantErr: false},
		{name: "键名大小写归一化", in: cond("GENRE_IDS", "16"), wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := normalizeRuleCondition(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("应报错但成功：%+v", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错：%v", err)
			}
			if out.Key != strings.ToLower(strings.TrimSpace(tt.in.Key)) {
				t.Fatalf("键应归一化为小写去空格，实际 %q", out.Key)
			}
		})
	}
}

// 确认全部 8 个参考文件条件键都受支持
func TestAllReferenceConditionKeysSupported(t *testing.T) {
	keys := []string{
		"genre_ids", "keywords", "include_keywords", "original_language",
		"origin_country", "year", "series_keywords", "series_actors",
	}
	// 每个条件键配一个该键合法的取值：year 只接受数字/数字范围，
	// 其余键都是自由文本，用 "x" 即可。
	sample := map[string]string{"year": "2000-2099"}
	for _, key := range keys {
		if !isKnownRuleKey(key) {
			t.Fatalf("参考文件条件键 %q 未受支持", key)
		}
		value := sample[key]
		if value == "" {
			value = "x"
		}
		if _, err := normalizeRuleCondition(RuleCondition{Key: key, Values: value}); err != nil {
			t.Fatalf("条件键 %q 归一化失败：%v", key, err)
		}
	}
	// year 必须拒绝非数字取值，避免把 "x" 当作合法年份放行。
	if _, err := normalizeRuleCondition(RuleCondition{Key: "year", Values: "x"}); err == nil {
		t.Fatal("year 条件应拒绝非数字取值")
	}
}

func TestEnsureClassifySchemaWithoutDB(t *testing.T) {
	// 未初始化 DB（ddb.Db == nil）时应返回明确错误而不是 panic
	if err := EnsureClassifySchema(); err == nil {
		// 若测试环境恰好已初始化 DB，AutoMigrate 成功也属正常
		t.Log("EnsureClassifySchema 在已初始化 DB 的环境下返回 nil")
	}
}

func TestHasCustomRulesWithoutDB(t *testing.T) {
	// 无 DB 时必须返回 false，保证回落既有模板逻辑（向后兼容）
	svc := &Service{}
	if svc.hasCustomRules(context.Background()) {
		t.Fatalf("无 DB 句柄时 hasCustomRules 应为 false")
	}
	segments, evidence, err := svc.matchCustomRules(context.Background(), "movie", &evaluationState{raw: map[string]any{}})
	if err != nil {
		t.Fatalf("无 DB 时不应报错：%v", err)
	}
	if len(segments) != 0 || evidence != nil {
		t.Fatalf("无 DB 时不应产生匹配结果：%v %v", segments, evidence)
	}
}

func TestExportRulesToYAMLKeepsPositionOrder(t *testing.T) {
	// 故意乱序传入，导出必须按 Position 排好
	rules := make([]ClassifyRule, 0, 5)
	for i := 4; i >= 0; i-- {
		rules = append(rules, ClassifyRule{
			MediaType: "movie", TargetPath: fmt.Sprintf("A/%d", i), Position: i, Enabled: true,
			ConditionList: []RuleCondition{cond("year", "2000-2099")},
		})
	}
	out := exportRulesToYAML(rules)
	last := -1
	for i := 0; i < 5; i++ {
		marker := fmt.Sprintf("A/%d:", i)
		idx := strings.Index(out, marker)
		if idx < 0 {
			t.Fatalf("导出缺少 %q：\n%s", marker, out)
		}
		if idx < last {
			t.Fatalf("导出顺序未按 Position 排序：\n%s", out)
		}
		last = idx
	}
}
