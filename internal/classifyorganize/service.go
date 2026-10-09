package classifyorganize

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"litepan/internal/mediaorganize/classification"
	"litepan/internal/settings"
)

const (
	detailCacheTTL = 30 * time.Minute
	maxDetailCache = 256

	// tmdbFuseSeconds 是 TMDB 连续失败后的熔断时长（C-5）。
	//
	// 熔断存在的原因是：整理计划会给每部影片发一次分类请求，TMDB 一旦不可用
	// （限流、网络断、token 过期），几百部影片会把同样的失败重演几百遍，
	// 每次都等一次网络超时，一个计划能拖几十分钟。而熔断期间我们直接用
	// 「TMDB 暂时不可用」降级，代价只是分类退回一级目录。
	//
	// 60 秒不是测出来的，是权衡值：短了挡不住限流恢复窗口（TMDB 的 429 恢复
	// 通常按分钟计），长了会让用户看到「暂时不可用」在 TMDB 已经恢复后仍然存在。
	tmdbFuseSeconds = 60
	// tmdbFuseThreshold 连续失败多少次才熔断。
	// 单次失败不熔断：偶发一次网络抖动是常态，为它放弃一整分钟的数据不划算。
	tmdbFuseThreshold = 3

	// degradedTmdbUnavailable 是 C-5 要求的降级原因码。
	//
	// 它必须与 degradedNoRuleMatched 分开：「没匹配到规则」说的是「这部影片
	// 不属于任何已配置的类型」，是个可以接受的结论；「TMDB 暂时不可用」说的是
	// 「我们还不知道它属于哪一类」。两者混成一个码，用户会去调规则，越调越乱。
	degradedTmdbUnavailable = "tmdb_unavailable"
	degradedDetailFailed    = "tmdb_detail_failed"
	degradedDetailNoData    = "tmdb_detail_unavailable"
	degradedNoRuleMatched   = "no_rule_matched"
	degradedAmbiguous       = "ambiguous_rule_matched"
)

type detailCacheEntry struct {
	raw       map[string]any
	expiresAt time.Time
}

type Service struct {
	settings *settings.Service

	mu    sync.Mutex
	cache map[string]detailCacheEntry

	// tmdbFail 是 TMDB 降级熔断状态（C-5）。与 cache 分开一把锁是因为
	// 熔断状态每秒都会被读（每次分类都问一句「还在熔断吗」），而 detail 缓存
	// 是按 mediaType:tmdbID 命中的，两者混在一把锁里会让无关的缓存读写排队。
	tmdbMu        sync.Mutex
	tmdbFailUntil time.Time
	tmdbFailCount int

	// now 是可注入的时钟。熔断必须能测「60 秒后自动恢复」，而 time.Now()
	// 没法在测试里推进 —— 唯一的办法是真等一分钟，那测试就废了。
	now func() time.Time
	// log 是可选的日志出口（*slog.Logger 的窄接口）。没注入时静默 ——
	// 熔断日志是诊断信息，不该让装配层变成这个包的硬依赖。
	log Logger
}

// Logger 是分类整理的窄日志接口，避免为了打一行日志把 slog 引进构造签名。
type Logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

type evaluationState struct {
	req             classification.Request
	raw             map[string]any
	detailAttempted bool
	detailLoaded    bool
	degradedReason  string
	evaluatedField  string
	evaluatedValues []string
	// lastConditionDetail 记最近一次条件求值的说明（"year 匹配 2000-2009"），
	// 进证据用。用户报「目录不对」时，这行是唯一能看出哪条条件挡住了什么的东西。
	lastConditionDetail string
	evaluated           map[string][]string
	// tmdbUnavailable 标记「TMDB 完全不可用」这一态（C-5）。
	// 与 detailLoaded=false 区分开：后者可能是「这次压根没去查」。
	tmdbUnavailable bool
}

// levels 是一次分类结果要经过的目录层级开关（C-4）。
//
// 四个开关不是并列的四个开关，而是有依赖的：关掉一级，后面的层级根本不该生效。
// 所以这里把它拍平成一个结构，是为了让 Classify 里只判断一次 level 够不够，
// 而不是在三四层目录判定里各问一遍「这个开关开了吗」。
type levels struct {
	primary   bool // 一级：总目录（电影/电视剧）
	secondary bool // 二级：维度目录（华语电影）
	tertiary  bool // 三级：年份区间目录（2000-2009）
	series    bool // 系列目录：流浪地球系列
}

// resolveLevels 按依赖关系算出本次真正生效的层级。
//
// 「关二级连带关三级与系列」不是偷懒，是分类层级本身的语义：三级目录的父目录
// 是二级目录，二级目录不存在时三级目录无处可挂；系列目录排在二级/三级之后，
// 它的上一级同样得在。只关三级不影响系列是有意的 —— 用户可能只想要年份分段
// 而不要系列分组，或者反过来。
func resolveLevels(primary, secondary, tertiary, series bool) levels {
	out := levels{primary: primary, secondary: secondary, tertiary: tertiary, series: series}
	if !out.primary {
		// 一级是整条路径的根：它没了，下面每一级的父目录都不存在。
		out.secondary = false
		out.tertiary = false
		out.series = false
		return out
	}
	if !out.secondary {
		out.tertiary = false
		out.series = false
	}
	return out
}

type customCandidate struct {
	segments    []string
	conditions  []parsedCondition
	expressions []string
}

type customMatchScore struct {
	specificity       int
	genreConditions   int
	regionConditions  int
	genreValueIndex   int
	regionValueIndex  int
	otherValueIndexes int
}

func New(settingsSvc *settings.Service) *Service {
	return &Service{settings: settingsSvc, cache: make(map[string]detailCacheEntry), now: time.Now}
}

func (s *Service) Available() bool {
	if s == nil || s.settings == nil || !s.settings.Bool(settings.KeyMOClassificationEnabled) {
		return false
	}
	_, err := normalizeConfig(s.Config())
	return err == nil
}

func (s *Service) Classify(ctx context.Context, req classification.Request) (classification.Decision, error) {
	if !s.Available() {
		return classification.Decision{}, classification.ErrUnavailable
	}
	req = prepareRequest(req)
	cfg := s.Config()
	tpl, ok := findTemplate(cfg, cfg.SelectedTemplate)
	if !ok {
		return classification.Decision{}, classification.ErrUnavailable
	}
	lv := s.resolveLevels(cfg)
	decision := classification.Decision{Applied: true, Template: tpl.Kind}
	state := evaluationState{req: req, raw: req.Raw, evaluated: make(map[string][]string)}

	// 用户自定义规则优先：配置了启用中的规则时按文件顺序评估、首个命中胜出。
	// 未配置任何自定义规则时不改变既有模板行为（向后兼容）。
	if s.hasCustomRules(ctx) {
		segments, evidence, err := s.matchCustomRules(ctx, req.MediaType, &state)
		if err != nil {
			return classification.Decision{}, err
		}
		if len(segments) > 0 {
			decision.Template = TemplateUserRules
			decision.Matched = true
			decision.RelativeSegments = appendSeriesSegment(ctx, s, lv, cfg, segments, req.MediaType, &state)
			decision.Category = decision.RelativeSegments[len(decision.RelativeSegments)-1]
			decision.Evidence = evidence
			return decision, nil
		}
		// 规则存在但均未命中：继续走所选模板，避免用户规则不全时完全失去归类能力。
	}

	if tpl.Kind == TemplateCustom {
		return s.classifyCustom(ctx, decision, &state, tpl.Rules, lv, cfg)
	}

	// 一级：走 first-match-wins。模板一级固定只有 电影/电视剧 两条，
	// 顺序即优先级，不需要再比「命中值在 TMDB 列表里的位置」。
	parent, matched, err := s.firstMatchingRule(ctx, &state, tpl.Rules)
	if err != nil {
		return classification.Decision{}, err
	}
	if matched && !lv.primary {
		// 一级被关：影片直接落到分类根目录（target_root），二级三级系列
		// 已经在 resolveLevels 里连带关掉了，这里不需要再判一次。
		//
		// 为什么还能走到这里而不是提前返回空路径：firstMatchingRule 已经算完了，
		// 顺手把它丢掉会让 TMDB 详情也被一并跳过，用户会看到"关掉一级之后
		// 连年份判断都不做了"这种找不到解释的现象。
		decision.Matched = true
		decision.Category = parent.Name
		decision.RelativeSegments = nil
		decision.Evidence = matchedEvidence(state, []string{parent.Condition})
		decision.Evidence["primary_disabled"] = true
		return decision, nil
	}

	if matched {
		if len(parent.Children) == 0 || !lv.secondary {
			// 只有一级，或二级被关：到此为止。
			// 二级关闭时三级与系列已经由 resolveLevels 连带关掉了，
			// 所以这里不需要再单独判断一次。
			decision.Matched = true
			decision.Category = parent.Name
			decision.RelativeSegments = s.decorateSegments(ctx, lv, cfg, []string{parent.Name}, parent, req.MediaType, &state)
			decision.Evidence = matchedEvidence(state, []string{parent.Condition})
			return decision, nil
		}
		child, childMatched, err := s.firstMatchingRule(ctx, &state, parent.Children)
		if err != nil {
			return classification.Decision{}, err
		}
		if !childMatched {
			// 二级未命中走一级兜底目录，三级与系列同样跟着回落 —— 挂在
			// 兜底目录下的年份段会让人以为分类成功了，其实是错的。
			//
			// Category 取兜底路径本身的末段而不是装饰后的末段：Category 的语义
			// 是「这条路径靠哪一级分类决定的」，系列目录只是附加分组。混进来的话
			// 统计会按系列名聚合，用户看到的「未命中分类 Top」全是系列目录名。
			base := fallbackSegments(parent)
			decision.Matched = true
			decision.RelativeSegments = s.decorateSegments(ctx, lv, cfg, base, parent, req.MediaType, &state)
			decision.Category = base[len(base)-1]
			decision.Evidence = unmatchedEvidence(state, parent.Condition)
			decision.Evidence["fallback"] = true
			// 兜底路径必须带上降级原因。一级命中、二级没命中，最常见的原因就是
			// TMDB 查不到（tmdb_unavailable / tmdb_detail_failed）；不记的话
			// 用户看到的是「目录莫名退到一级」，日志里一条线索都没有。
			decision.DegradedReason = fallbackReason(state.degradedReason)
			return decision, nil
		}
		segments := []string{parent.Name, child.Name}
		evidence := matchedEvidence(state, []string{parent.Condition, child.Condition})
		if lv.tertiary && len(child.Children) > 0 {
			tertiary, tertiaryMatched, err := s.firstMatchingRule(ctx, &state, child.Children)
			if err != nil {
				return classification.Decision{}, err
			}
			if tertiaryMatched {
				segments = append(segments, tertiary.Name)
				evidence = matchedEvidence(state, []string{parent.Condition, child.Condition, tertiary.Condition})
				child = tertiary
			} else {
				evidence["tertiary_unmatched"] = true
			}
		}
		decision.Matched = true
		decision.RelativeSegments = s.decorateSegments(ctx, lv, cfg, segments, child, req.MediaType, &state)
		decision.Category = child.Name
		decision.Evidence = evidence
		return decision, nil
	}

	decision.DegradedReason = fallbackReason(state.degradedReason)
	decision.Evidence = unmatchedEvidence(state, "")
	return decision, nil
}

// decorateSegments 在目录段末尾追加系列目录段（C-2）。
//
// 命中顺序：先看命中的那一层规则自己有没有 Series（「这一层就属于某个系列」），
// 再看全局系列规则。全局规则排在后面，是因为它的判定依据（series_keywords）
// 比目录匹配宽松 —— 两个不同系列的剧也可能共用一个「科幻」二级目录，
// 让宽松规则优先会把它们错误地并进同一个系列。
func (s *Service) decorateSegments(ctx context.Context, lv levels, cfg Config, segments []string, rule Rule, mediaType string, state *evaluationState) []string {
	out := append([]string(nil), segments...)
	if !lv.series {
		return out
	}
	if rule.Series != "" {
		return withSeriesSegment(out, rule.Series)
	}
	return appendSeriesSegment(ctx, s, lv, cfg, out, mediaType, state)
}

// appendSeriesSegment 按全局系列规则补一段系列目录。
func appendSeriesSegment(ctx context.Context, s *Service, lv levels, cfg Config, segments []string, mediaType string, state *evaluationState) []string {
	if !lv.series {
		return segments
	}
	name := matchSeriesRule(ctx, cfg, s, state, mediaType)
	if name == "" {
		return segments
	}
	return withSeriesSegment(segments, name)
}

// withSeriesSegment 追加一段，已存在同名段时不重复追加 ——
// 同一部剧既命中三级目录的 Series 又命中全局规则时会出现两次。
func withSeriesSegment(segments []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return segments
	}
	for _, seg := range segments {
		if strings.EqualFold(seg, name) {
			return segments
		}
	}
	return append(segments, name)
}

func (s *Service) classifyCustom(ctx context.Context, decision classification.Decision, state *evaluationState, rules []Rule, lv levels, cfg Config) (classification.Decision, error) {
	candidates, err := buildCustomCandidates(rules)
	if err != nil {
		return classification.Decision{}, err
	}
	bestIndex := -1
	bestScore := customMatchScore{specificity: -1}
	ambiguous := false
	for index, candidate := range candidates {
		matched, score, err := s.customCandidateMatches(ctx, state, candidate)
		if err != nil {
			return classification.Decision{}, err
		}
		if !matched {
			continue
		}
		switch compareCustomMatchScore(score, bestScore) {
		case 1:
			bestIndex = index
			bestScore = score
			ambiguous = false
		case 0:
			ambiguous = bestIndex >= 0
		}
	}
	if bestIndex < 0 || ambiguous {
		if ambiguous {
			state.degradedReason = degradedAmbiguous
		}
		decision.DegradedReason = fallbackReason(state.degradedReason)
		decision.Evidence = unmatchedEvidence(*state, "")
		return decision, nil
	}
	best := candidates[bestIndex]
	decision.Matched = true
	decision.Category = best.segments[len(best.segments)-1]
	// 自定义模板走的是打分择优（不是命中即停），这是任务书关键决策第 1 条
	// 要求保留的能力。这里补系列目录段用的也是打分胜出的那条规则。
	decision.RelativeSegments = appendSeriesSegment(ctx, s, lv, cfg, best.segments, state.req.MediaType, state)
	decision.Evidence = map[string]any{
		"conditions":    append([]string(nil), best.expressions...),
		"fields":        cloneEvaluatedFields(state.evaluated),
		"specificity":   bestScore.specificity,
		"match_policy":  "specificity_genres_region_tmdb_order",
		"detail_loaded": state.detailLoaded,
	}
	return decision, nil
}

// resolveLevels 读四个粒度开关（C-4）。
//
// 缺失的设置（老库里没有这些键）按「开」处理，让升级后分类能力不缩回去 —
// 用户升级 litepan 的目的是拿到新功能，不是丢功能。真正想关的人会自己去设置里关。
func (s *Service) resolveLevels(cfg Config) levels {
	// T27 C-3：一级也变成可关的一层。
	//
	// 以前 primary 恒为 true（写死），levels.primary 这个字段因此从没被读过 ——
	// 一个没人读的开关字段比没有更糟：它让人以为"关一级"这件事已经实现了。
	// 现在的语义是**扁平化**：关掉一级后所有影片直接落到分类根目录，
	// 二级三级系列连带失效（下面 resolveLevels 里的级联）。
	primary := s.settingsBool(settings.KeyMOClassificationPrimaryEnabled, true)
	secondary := s.settingsBool(settings.KeyMOClassificationSecondaryEnabled, true)
	tertiary := s.settingsBool(settings.KeyMOClassificationTertiaryEnabled, true)
	series := s.settingsBool(settings.KeyMOClassificationSeriesEnabled, true)
	return resolveLevels(primary, secondary, tertiary, series)
}

func (s *Service) settingsBool(key string, fallback bool) bool {
	if s == nil || s.settings == nil {
		return fallback
	}
	return s.settings.Bool(key)
}

// matchSeriesRule 返回命中的系列目录段，没命中返回空串。
//
// 判定走 series_keywords 与 keywords 两个字段的 OR：任一关键词命中本地化标题
// 即算该系列。title 不在这里直接取 —— 分类输入的 raw 里未必有标题，
// 所以统一从 valuesForField 走，缺数据时会触发 TMDB 详情加载。
func matchSeriesRule(ctx context.Context, cfg Config, s *Service, state *evaluationState, mediaType string) string {
	for _, rule := range cfg.Series {
		if rule.MediaType != "" && !strings.EqualFold(rule.MediaType, mediaType) {
			continue
		}
		dir := rule.DirSegment()
		if dir == "" {
			continue
		}
		if seriesRuleMatches(ctx, s, state, rule) {
			return dir
		}
	}
	return ""
}

func seriesRuleMatches(ctx context.Context, s *Service, state *evaluationState, rule SeriesRule) bool {
	keys := append(append([]string(nil), rule.SeriesKeywords...), rule.Keywords...)
	if len(keys) == 0 {
		return false
	}
	// 分两次取值而不是把关键词拼成一条条件：不同字段（series_keywords 来自
	// TMDB 的 keywords，keywords 来自 raw 里的本地化标题）落在不同的取值路径上，
	// 拼成一条会要求两个字段同时存在，于是本地化标题命中的那支永远取不到值。
	values := s.valuesForField(ctx, state, fieldSeries)
	if len(values) == 0 {
		values = s.valuesForField(ctx, state, fieldKeywords)
	}
	for _, key := range keys {
		for _, v := range values {
			if strings.EqualFold(strings.TrimSpace(v), key) {
				return true
			}
		}
	}
	return false
}

func buildCustomCandidates(rules []Rule) ([]customCandidate, error) {
	candidates := make([]customCandidate, 0)
	var walk func([]Rule, []string, []parsedCondition, []string) error
	walk = func(items []Rule, parentSegments []string, parentConditions []parsedCondition, parentExpressions []string) error {
		for _, rule := range items {
			conditions, err := parseExpression(rule.Condition)
			if err != nil {
				return err
			}
			segments := append(append([]string(nil), parentSegments...), rule.Name)
			pathConditions := append(append([]parsedCondition(nil), parentConditions...), conditions...)
			expressions := append(append([]string(nil), parentExpressions...), rule.Condition)
			if len(rule.Children) == 0 {
				candidates = append(candidates, customCandidate{
					segments: segments, conditions: pathConditions, expressions: expressions,
				})
			} else {
				candidates = append(candidates, customCandidate{
					segments: fallbackSegments(rule), conditions: pathConditions, expressions: expressions,
				})
			}
			if len(rule.Children) > 0 {
				if err := walk(rule.Children, segments, pathConditions, expressions); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(rules, nil, nil, nil); err != nil {
		return nil, err
	}
	return candidates, nil
}

func fallbackSegments(rule Rule) []string {
	segments := []string{rule.Name}
	if rule.FallbackMode == "directory" && strings.TrimSpace(rule.FallbackDir) != "" {
		segments = append(segments, rule.FallbackDir)
	}
	return segments
}

func (s *Service) customCandidateMatches(ctx context.Context, state *evaluationState, candidate customCandidate) (bool, customMatchScore, error) {
	score := customMatchScore{
		specificity:      len(candidate.conditions),
		genreValueIndex:  int(^uint(0) >> 1),
		regionValueIndex: int(^uint(0) >> 1),
	}
	for _, condition := range candidate.conditions {
		actual := s.valuesForField(ctx, state, condition.Field)
		valueIndex := firstMatchingValueIndex(condition.Values, actual)
		if valueIndex < 0 {
			return false, customMatchScore{}, nil
		}
		switch condition.Field {
		case "genres":
			score.genreConditions++
			score.genreValueIndex = valueIndex
		case "origin_country":
			score.regionConditions++
			score.regionValueIndex = valueIndex
		case "type":
		default:
			score.otherValueIndexes += valueIndex
		}
	}
	return true, score, nil
}

func compareCustomMatchScore(left, right customMatchScore) int {
	comparisons := [][2]int{
		{left.specificity, right.specificity},
		{left.genreConditions, right.genreConditions},
		{left.regionConditions, right.regionConditions},
		{right.genreValueIndex, left.genreValueIndex},
		{right.regionValueIndex, left.regionValueIndex},
		{right.otherValueIndexes, left.otherValueIndexes},
	}
	for _, values := range comparisons {
		if values[0] > values[1] {
			return 1
		}
		if values[0] < values[1] {
			return -1
		}
	}
	return 0
}

func cloneEvaluatedFields(fields map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(fields))
	for field, values := range fields {
		cloned[field] = append([]string(nil), values...)
	}
	return cloned
}

func findTemplate(cfg Config, kind string) (Template, bool) {
	for _, tpl := range cfg.Templates {
		if tpl.Kind == kind {
			return tpl, true
		}
	}
	return Template{}, false
}

// firstMatchingRule 逐条求值，首个命中即返回（first-match-wins，T02 C-1）。
//
// 这里改掉了原来的「命中值在 TMDB 列表里位置最靠前的规则胜出」：那条规则让
// 用户的排序完全不起作用，而模板一级只有电影/电视剧两条、二级也只有几条，
// 用户唯一能表达的意图就是顺序。顺序即优先级。
//
// 命中判定统一走 matchCondition 而不是 firstMatchingValueIndex 的字面量比较，
// 否则三级目录的 `year=2000-2009` 与 `-`/`+` 语法在模板路径上完全无效 ——
// 它们只在用户自定义规则那条路径上生效（engine.go 的 matchRule）。
// 症状是：用户在界面上配好了一个年份区间目录，保存成功、不报错、不生效。
func (s *Service) firstMatchingRule(ctx context.Context, state *evaluationState, rules []Rule) (Rule, bool, error) {
	for _, rule := range rules {
		field, values, err := rule.matchField()
		if err != nil {
			return Rule{}, false, err
		}
		actual := s.valuesForField(ctx, state, field)
		state.evaluatedField = field
		state.evaluatedValues = actual
		if len(values) == 0 {
			// 无条件规则不在这里命中：兜底是 Classify 的 fallback 分支的职责。
			// 让某条空条件规则静默吃掉整层目录，用户看到的路径会完全对不上
			// 自己配的任何一条规则，而且没有任何报错可查。
			continue
		}
		// 分隔符必须是逗号：matchCondition 走 splitRuleValues，它只切 `,` 与 `，`。
		// 用 `;`（parseCondition 的输出分隔符）拼回去会把「US;GB」当成一个值，
		// 结果是所有多值条件在模板路径上一条都不命中 —— 而它们在用户自定义规则
		// 路径上照常命中，因为那条路走的是 ConditionList 而不是这里的字符串拼接。
		matched, detail := matchCondition(RuleCondition{Key: field, Values: strings.Join(values, ",")}, actual)
		state.lastConditionDetail = detail
		if !matched {
			continue
		}
		return rule, true, nil
	}
	return Rule{}, false, nil
}

// mergeRaw 把 TMDB 详情响应并进已有的 raw。
//
// 已有 raw 里的同名键优先保留：调用方给的通常是它已经认定的事实（预览端点
// 的 title/year、planner 从识别结果里带出来的本地化标题），让它被一次
// TMDB 响应覆盖，等于让「预览显示的年份」和「真实执行用的年份」悄悄分家。
func mergeRaw(base, detail map[string]any) map[string]any {
	if len(detail) == 0 {
		return base
	}
	merged := make(map[string]any, len(base)+len(detail))
	for key, value := range detail {
		merged[key] = value
	}
	for key, value := range base {
		merged[key] = value
	}
	return merged
}

func (s *Service) valuesForField(ctx context.Context, state *evaluationState, field string) []string {
	if field == "type" {
		values := nonEmptyValues(state.req.MediaType)
		state.evaluated[field] = append([]string(nil), values...)
		return values
	}
	if field == "year" {
		// year 不是 TMDB 的直接字段，统一从上映/首播日期里取年份。
		// 只有真的从 raw 里取到了才提前返回：原来这里是「只要字段是 year 就
		// 直接返回」，于是 year=2000-2009 这类三级目录永远拿不到日期 ——
		// 请求里只带 origin_country 时，loadDetail 压根不会被调用。
		// 症状是用户在界面上配好年份区间目录、保存成功、不报错、不生效。
		if values := yearValues(state.raw); len(values) > 0 {
			state.evaluated[field] = append([]string(nil), values...)
			return values
		}
	}
	if raw, exists := state.raw[field]; exists {
		values := extractFieldValues(raw, field)
		state.evaluated[field] = append([]string(nil), values...)
		return values
	}
	if !state.detailAttempted {
		state.detailAttempted = true
		if strings.TrimSpace(state.req.TMDBID) == "" || state.req.Loader == nil {
			// 没配 TMDB ID 或没挂 loader：这不是「TMDB 挂了」，是这次压根
			// 没打算查。降级原因保持原来的 detail_unavailable，调用方据此
			// 知道它是一次正常的降级而不是一次故障。
			state.degradedReason = degradedDetailNoData
			return nil
		}
		if s.tmdbFused() {
			// 熔断中：直接降级，不再发起注定失败的请求。这条路径不打网络，
			// 所以也不累加失败计数 —— 否则熔断会自己把自己续命。
			state.degradedReason = degradedTmdbUnavailable
			state.tmdbUnavailable = true
			return nil
		}
		raw, err := s.loadDetail(ctx, state.req.Loader, state.req.TMDBID, state.req.MediaType)
		if err != nil {
			state.degradedReason = s.noteTmdbFailure(state, err)
			return nil
		}
		s.noteTmdbSuccess()
		// 详情响应合并进 raw 而不是整个替换：调用方可能已经带了自己认得的字段
		// （预览端点给的 title/year、planner 给的本地化标题关键词）。直接替换
		// 会把那些字段连同它们已经算出的取值一起丢掉 —— 症状是「配好了系列规则，
		// 但只要查过 TMDB 就不生效」，而请求里不带任何字段时反倒正常。
		state.raw = mergeRaw(state.raw, raw)
		state.detailLoaded = true
	}
	if field == "year" {
		// 到这里说明上面那一次没取到，而 loadDetail 刚把 raw 换成了详情响应，
		// 现在重新从新 raw 里取一次年份。
		values := yearValues(state.raw)
		state.evaluated[field] = append([]string(nil), values...)
		return values
	}
	values := extractFieldValues(state.raw[field], field)
	state.evaluated[field] = append([]string(nil), values...)
	return values
}

// tmdbFused 报告 TMDB 是否处于熔断窗口内。
//
// 只读状态、不写状态，所以这里只加读锁就够了 —— 真要开写锁也没坏处，
// 但这把锁每秒会被每部影片读一次，锁竞争不必要。
func (s *Service) tmdbFused() bool {
	s.tmdbMu.Lock()
	defer s.tmdbMu.Unlock()
	if s.tmdbFailUntil.IsZero() {
		return false
	}
	return s.clock().Before(s.tmdbFailUntil)
}

// noteTmdbSuccess 成功一次就清零失败计数。
// 不这么做的话，一次成功之后紧跟的两次失败又会重新触发熔断，
// 而「偶尔失败一次」的常态会把熔断变得非常神经质。
// SetClock 注入时钟。返回 *Service 是为了与包内其他 Set* 保持一致。
func (s *Service) SetClock(fn func() time.Time) *Service {
	if s == nil {
		return s
	}
	s.now = fn
	return s
}

// SetLogger 注入日志出口。熔断打开时记一行，解释「为什么这批文件全落在一级目录」。
func (s *Service) SetLogger(l Logger) *Service {
	if s == nil {
		return s
	}
	s.log = l
	return s
}

func (s *Service) noteTmdbSuccess() {
	s.tmdbMu.Lock()
	defer s.tmdbMu.Unlock()
	s.tmdbFailCount = 0
	s.tmdbFailUntil = time.Time{}
}

// noteTmdbFailure 记一次 TMDB 失败，达到阈值就熔断 tmdbFuseSeconds 秒，返回本次降级原因码。
//
// 三态区分（C-5 的核心）：
//   - 熔断中触发熔断 → tmdb_unavailable（TMDB 完全不可用）
//   - 未达阈值       → tmdb_detail_failed（这次失败，下次还试）
//   - 达阈值         → tmdb_unavailable（连续失败，判定为不可用）
func (s *Service) noteTmdbFailure(state *evaluationState, cause error) string {
	s.tmdbMu.Lock()
	s.tmdbFailCount++
	count := s.tmdbFailCount
	if count >= tmdbFuseThreshold {
		until := s.clock().Add(tmdbFuseSeconds * time.Second)
		if until.After(s.tmdbFailUntil) {
			s.tmdbFailUntil = until
		}
	}
	until := s.tmdbFailUntil
	s.tmdbMu.Unlock()

	if count < tmdbFuseThreshold {
		return degradedDetailFailed
	}
	state.tmdbUnavailable = true
	s.logFuseOpened(count, until, cause)
	return degradedTmdbUnavailable
}

// logFuseOpened 记一条熔断日志。
//
// 单独拎出来是因为它要回答一个具体问题：「为什么这批文件全落在一级目录」。
// 没有这行日志，用户只能看到分类结果突然变了，而界面上没有任何地方说明原因。
//
// cause 只记错误文本、不带请求参数：TMDB 请求 URL 里含 API key，
// 整条 URL 打进日志等于把密钥写进日志文件。
func (s *Service) logFuseOpened(count int, until time.Time, cause error) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warn("TMDB 连续失败，分类明细暂时停用",
		"consecutive_failures", count,
		"fuse_seconds", tmdbFuseSeconds,
		"resume_at", until.Format(time.RFC3339),
		"cause", cause.Error(),
	)
}

// clock 返回可注入的当前时间。nil receiver 或未注入时回落 time.Now，
// 保证零值 Service 不会 panic。
func (s *Service) clock() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

// prepareRequest 把 Request 上的显式字段补进 Raw。
//
// 只在对应键缺失时补，不覆盖：生产链路已经在 Raw 里放了真实数据，
// 而 Raw 里的来源可能是 TMDB 详情（authoritative），显式字段只是预览时的手填值。
// 无条件覆盖会让「预览显示年份 2009、真实执行按 2005 走」这类不一致出现。
func prepareRequest(req classification.Request) classification.Request {
	if req.Raw == nil && (strings.TrimSpace(req.Title) != "" || req.Year > 0) {
		req.Raw = map[string]any{}
	}
	if strings.TrimSpace(req.Title) != "" {
		if _, exists := req.Raw["title"]; !exists {
			req.Raw["title"] = req.Title
		}
	}
	if req.Year > 0 {
		if _, exists := req.Raw["year"]; !exists {
			req.Raw["year"] = req.Year
		}
	}
	return req
}

// yearValues 从 TMDB detail 的上映/首播日期中解析年份。
// 电影用 release_date，剧集用 first_air_date；两者都是 "YYYY-MM-DD"。
func yearValues(raw map[string]any) []string {
	if raw == nil {
		return nil
	}
	values := make([]string, 0, 2)
	for _, key := range []string{"release_date", "first_air_date", "air_date"} {
		text := strings.TrimSpace(fmt.Sprint(raw[key]))
		if text == "" || text == "<nil>" {
			continue
		}
		year := text
		if len(year) > 4 {
			year = year[:4]
		}
		if _, err := strconv.Atoi(year); err != nil {
			continue
		}
		values = append(values, year)
	}
	return uniqueValues(values)
}

func extractFieldValues(raw any, field string) []string {
	if keys, ok := ruleFieldObjectKeys[field]; ok {
		return uniqueValues(collectObjectFieldValues(raw, keys))
	}
	values := make([]string, 0)
	var appendValue func(any)
	appendValue = func(value any) {
		switch item := value.(type) {
		case nil:
			return
		case []any:
			for _, child := range item {
				appendValue(child)
			}
		case []string:
			for _, child := range item {
				appendValue(child)
			}
		case []map[string]any:
			for _, child := range item {
				appendValue(child)
			}
		case map[string]any:
			keys := []string{"name", "iso_3166_1", "iso_639_1", "code"}
			if field == "genres" {
				keys = []string{"name"}
			}
			for _, key := range keys {
				if child, ok := item[key]; ok {
					appendValue(child)
				}
			}
		default:
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				values = append(values, text)
			}
		}
	}
	appendValue(raw)
	return uniqueValues(values)
}

// ruleFieldObjectKeys 定义自定义规则条件键 → TMDB 载荷对象键的映射。
// 这些键与既有模板字段（genres/origin_country）走同一套取值与缓存路径。
var ruleFieldObjectKeys = map[string][]string{
	"genre_ids":         {"id"},
	"keywords":          {"name"},
	"series_keywords":   {"name"},
	"series_actors":     {"name"},
	"original_language": {"iso_639_1", "code"},
	"origin_country":    {"iso_3166_1", "code"},
}

// ruleFieldNestedPaths 定义需要下钻两级容器的字段（TMDB 的 keywords 形如
// {"keywords":[{"id":..,"name":".."}]}，剧集是 {"results":[...]}）。
var ruleFieldNestedPaths = map[string][]string{
	"keywords":        {"keywords", "results", "keywords"},
	"series_keywords": {"keywords", "results", "keywords"},
	"series_actors":   {"credits", "aggregate_credits", "cast"},
}

// collectObjectFieldValues 从原始载荷中递归收集对象键取值。
// 对 keywords/series_actors 这类需要下钻的字段，先在已知容器里找；
// 找不到时回落到全量递归，保证不同 TMDB 语言/接口形态都能取到值。
func collectObjectFieldValues(raw any, keys []string) []string {
	values := make([]string, 0)
	var collect func(any)
	collect = func(value any) {
		switch item := value.(type) {
		case nil:
			return
		case []any:
			for _, child := range item {
				collect(child)
			}
		case []string:
			for _, child := range item {
				text := strings.TrimSpace(child)
				if text != "" {
					values = append(values, text)
				}
			}
		case []map[string]any:
			for _, child := range item {
				collect(child)
			}
		case map[string]any:
			for _, key := range keys {
				if child, ok := item[key]; ok {
					collect(child)
				}
			}
		case float64:
			// TMDB 的数字 ID 经 JSON 解码后是 float64；整数形态去掉小数点。
			values = append(values, formatNumber(item))
		case int:
			values = append(values, strconv.Itoa(item))
		case int64:
			values = append(values, strconv.FormatInt(item, 10))
		case json.Number:
			values = append(values, string(item))
		case bool:
			values = append(values, strconv.FormatBool(item))
		default:
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				values = append(values, text)
			}
		}
	}
	collect(raw)
	return values
}

func formatNumber(value float64) string {
	if value == float64(int64(value)) {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func nonEmptyValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []string{value}
}

func uniqueValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func firstMatchingValueIndex(expected, actual []string) int {
	for index, got := range actual {
		for _, want := range expected {
			if strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(got)) {
				return index
			}
		}
	}
	return -1
}

func fallbackReason(reason string) string {
	if reason != "" {
		return reason
	}
	return degradedNoRuleMatched
}

func matchedEvidence(state evaluationState, conditions []string) map[string]any {
	return map[string]any{
		"conditions":    conditions,
		"field":         state.evaluatedField,
		"values":        state.evaluatedValues,
		"detail_loaded": state.detailLoaded,
	}
}

func unmatchedEvidence(state evaluationState, parentCondition string) map[string]any {
	evidence := map[string]any{
		"field":         state.evaluatedField,
		"values":        state.evaluatedValues,
		"detail_loaded": state.detailLoaded,
	}
	if parentCondition != "" {
		evidence["parent_condition"] = parentCondition
	}
	return evidence
}

func (s *Service) loadDetail(ctx context.Context, loader classification.DetailLoader, tmdbID, mediaType string) (map[string]any, error) {
	key := strings.ToLower(strings.TrimSpace(mediaType)) + ":" + strings.TrimSpace(tmdbID)
	now := s.clock()
	s.mu.Lock()
	if entry, ok := s.cache[key]; ok && now.Before(entry.expiresAt) {
		s.mu.Unlock()
		return entry.raw, nil
	}
	s.mu.Unlock()

	payload, err := loader.Lookup(ctx, tmdbID, mediaType)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	if raw == nil || strings.TrimSpace(fmt.Sprint(raw["id"])) == "" {
		return nil, fmt.Errorf("TMDB detail 响应缺少影片标识")
	}
	s.mu.Lock()
	if len(s.cache) >= maxDetailCache {
		for cacheKey, entry := range s.cache {
			if now.After(entry.expiresAt) {
				delete(s.cache, cacheKey)
			}
		}
	}
	if len(s.cache) >= maxDetailCache {
		for cacheKey := range s.cache {
			delete(s.cache, cacheKey)
			break
		}
	}
	s.cache[key] = detailCacheEntry{raw: raw, expiresAt: now.Add(detailCacheTTL)}
	s.mu.Unlock()
	return raw, nil
}
