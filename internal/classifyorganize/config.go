package classifyorganize

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"litepan/internal/domain"
	"litepan/internal/settings"
)

const (
	ConfigVersion  = 2
	TemplateMedia  = "media"
	TemplateRegion = "region"
	TemplateGenre  = "genre"
	TemplateCustom = "custom"
	// TemplateUserRules 标识本次分类由用户自定义规则（导入的 YAML 规则）命中。
	TemplateUserRules = "user_rules"

	// maxRuleDepth 是模板目录的最大层数（一级 1 / 二级 2 / 三级 3）。
	// 任务书要求三级目录「仅一层」，所以三级规则下面不再允许 Children；
	// 超深配置在归一化阶段报错而不是运行时静默截断。
	maxRuleDepth = 3
	// 目录层级标签，normalizeCustomRules 靠它决定当前递归到第几层。
	levelPrimary = "一级分类"
	levelSecond  = "二级分类"
	levelThird   = "三级分类"
	// 常用条件字段名。集中在这里是因为它们同时出现在 parseCondition 的
	// 归一化、结构化字段的回落、以及证据输出三处，写散了三份早晚不一致。
	fieldType       = "type"
	fieldMediaTypes = "media_types"
	fieldGenres     = "genres"
	fieldGenreIDs   = "genre_ids"
	fieldYear       = "year"
	fieldKeywords   = "keywords"
	fieldSeries     = "series_keywords"
	fieldCountry    = "origin_country"
	fieldLanguage   = "original_language"
)

// RuleFields 是结构化条件字段（T02 C-3）。
//
// 为什么和 Condition 表达式字符串并存：Condition 是用户手写原文
// （"origin_country=CN，genres=喜剧"），RuleFields 是程序归一化后的结构。
// 两者混存一份会让「用户写了什么」和「系统理解成什么」无法区分，
// 导出 YAML 时也无从还原用户原文。所以结构化字段为空时才回落解析 Condition。
type RuleFields struct {
	// MediaTypes 一级分类用（movie/tv）。任务书把一级维度写作 media_types，
	// 仓内既有模板用的是单数 type，两者都要认。
	MediaTypes []string `json:"media_types,omitempty"`
	// GenreIDs TMDB 类型 ID。
	GenreIDs []int `json:"genre_ids,omitempty"`
	// OriginCountry ISO 3166-1。
	OriginCountry []string `json:"origin_country,omitempty"`
	// OriginalLanguage ISO 639-1。
	OriginalLanguage []string `json:"original_language,omitempty"`
	// Year 支持单值 2001 与闭区间 2000-2009（写成 {"from":2000,"to":2009}）。
	Year *YearRange `json:"year,omitempty"`
	// Keywords 命中即产出该关键词，用于三级与系列目录。
	Keywords []string `json:"keywords,omitempty"`
	// SeriesKeywords 从「只当过滤条件」升级为「产出系列目录段」（C-2）。
	SeriesKeywords []string `json:"series_keywords,omitempty"`
}

// Empty 判断结构化字段是否整体为空，为空时回落解析 Condition 表达式字符串。
func (f RuleFields) Empty() bool {
	return len(f.MediaTypes) == 0 && len(f.GenreIDs) == 0 && len(f.OriginCountry) == 0 &&
		len(f.OriginalLanguage) == 0 && f.Year == nil &&
		len(f.Keywords) == 0 && len(f.SeriesKeywords) == 0
}

// YearRange 是年份区间。From 等于 To 时表示单年。
type YearRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Contains 判断 year 是否落在区间内（闭区间）。
func (r *YearRange) Contains(year int) bool {
	if r == nil {
		return false
	}
	if year < r.From {
		return false
	}
	if r.To > 0 && year > r.To {
		return false
	}
	return true
}

// Rule 用同一种结构描述一级、二级、三级分类。
// 自定义模板允许用逗号连接多个条件；FallbackMode 控制子分类均未命中时使用一级目录或指定子目录。
type Rule struct {
	Name         string `json:"name"`
	Condition    string `json:"condition"`
	FallbackMode string `json:"fallback_mode,omitempty"`
	FallbackDir  string `json:"fallback_dir,omitempty"`
	// Fields 是结构化条件字段。为空时回落解析 Condition（T02 C-3）。
	Fields *RuleFields `json:"fields,omitempty"`
	// Series 为该目录的系列目录名（例如 "流浪地球系列"）。
	// 命中三级或二级规则时会把它作为一段追加到 RelativeSegments 末尾（C-2）。
	// 为空表示这一层不产出系列目录。
	Series string `json:"series,omitempty"`
	// Children 二级规则的子目录（= 三级目录）。三级目录下面不再允许 Children。
	Children []Rule `json:"children,omitempty"`
}

// matchField 返回该规则真正用于匹配的条件字段名：
// 结构化字段非空时用结构化字段的首个可用维度，为空时回落解析 Condition。
func (r Rule) matchField() (string, []string, error) {
	if r.Fields != nil && !r.Fields.Empty() {
		field, values := r.Fields.primaryField()
		return field, values, nil
	}
	parsed, err := parseCondition(r.Condition)
	if err != nil {
		return "", nil, err
	}
	return parsed.Field, parsed.Values, nil
}

// primaryField 取结构化字段里第一个非空维度，作为该规则的匹配维度。
// 顺序固定为 类型 → 年份 → 类型ID → 国家 → 语言 → 关键词，
// 固定顺序是为了让同一份配置每次跑出同样的证据，不受字段填写顺序影响。
func (f RuleFields) primaryField() (string, []string) {
	switch {
	case len(f.MediaTypes) > 0:
		return fieldMediaTypes, append([]string(nil), f.MediaTypes...)
	case f.Year != nil:
		return fieldYear, []string{f.Year.String()}
	case len(f.GenreIDs) > 0:
		return fieldGenreIDs, f.GenreIDsText()
	case len(f.OriginCountry) > 0:
		return fieldCountry, append([]string(nil), f.OriginCountry...)
	case len(f.OriginalLanguage) > 0:
		return fieldLanguage, append([]string(nil), f.OriginalLanguage...)
	case len(f.Keywords) > 0:
		return fieldKeywords, append([]string(nil), f.Keywords...)
	case len(f.SeriesKeywords) > 0:
		return fieldSeries, append([]string(nil), f.SeriesKeywords...)
	}
	return "", nil
}

// GenreIDsText 把 TMDB 类型 ID 渲染成条件表达式里用的字符串形式。
func (f RuleFields) GenreIDsText() []string {
	if len(f.GenreIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.GenreIDs))
	for _, id := range f.GenreIDs {
		out = append(out, strconv.Itoa(id))
	}
	return out
}

// String 把年份区间渲染成条件表达式里的形式：单年 "2001"，区间 "2000-2009"。
// 与 parseYearToken 的解析口径一致，所以渲染出来的串能原样被 parseCondition 吃回去。
func (r *YearRange) String() string {
	if r == nil {
		return ""
	}
	if r.To <= 0 || r.To == r.From {
		return strconv.Itoa(r.From)
	}
	return strconv.Itoa(r.From) + "-" + strconv.Itoa(r.To)
}

type Template struct {
	Kind  string `json:"kind"`
	Rules []Rule `json:"rules"`
}

// SeriesRule 是全局系列目录规则（T02 C-2）。
//
// 命中时把 DirName 作为一段追加到 RelativeSegments 末尾。语义从「series_keywords
// 只是过滤条件」升级成「series_keywords 决定归到哪个系列目录」，因为
// 「同一个系列散在 12 个目录里」正是分类目录体系最难排查的问题。
type SeriesRule struct {
	// Name 是系列名（例如 "流浪地球"），也是同一性判定的键。
	Name string `json:"name"`
	// DirName 是落地的目录段（例如 "流浪地球系列"）。
	// 为空时用 Name 加「系列」后缀，这样最常见的配置只需要填 Name。
	DirName string `json:"dir_name,omitempty"`
	// MediaType 空表示电影和电视剧都算，否则只对该类型生效。
	MediaType string `json:"media_type,omitempty"`
	// SeriesKeywords 是系列关键词，命中任一即算该系列（OR 语义）。
	SeriesKeywords []string `json:"series_keywords,omitempty"`
	// Keywords 是补充关键词，和 SeriesKeywords 一起参与 OR。
	Keywords []string `json:"keywords,omitempty"`
	// Position 越小越先命中。first-match-wins 下顺序就是优先级。
	Position int    `json:"position"`
	Remark   string `json:"remark,omitempty"`
}

// DirSegment 返回该规则实际产出的目录段。
// 没有显式 DirName 时按「Name + 系列」拼，符合任务书给的
// 「流浪地球系列」这种命名习惯。
func (s SeriesRule) DirSegment() string {
	if dir := strings.TrimSpace(s.DirName); dir != "" {
		return dir
	}
	if name := strings.TrimSpace(s.Name); name != "" {
		return name + "系列"
	}
	return ""
}

type Config struct {
	Version          int        `json:"version"`
	Enabled          bool       `json:"enabled"`
	SelectedTemplate string     `json:"selected_template"`
	Templates        []Template `json:"templates"`
	// Series 是全局系列目录规则（T02 C-2）。空数组 = 不产出系列目录。
	Series []SeriesRule `json:"series,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Version:          ConfigVersion,
		SelectedTemplate: TemplateMedia,
		Templates: []Template{
			{Kind: TemplateMedia, Rules: []Rule{
				{Name: "电影", Condition: "type=movie"},
				{Name: "电视剧", Condition: "type=tv"},
			}},
			{Kind: TemplateRegion, Rules: []Rule{
				{Name: "电影", Condition: "type=movie", Children: []Rule{
					{Name: "国产", Condition: "origin_country=CN"},
					{Name: "港台", Condition: "origin_country=TW;HK"},
					{Name: "欧美", Condition: "origin_country=US;GB;FR;DE;IT;ES"},
					{Name: "日韩", Condition: "origin_country=JP;KR"},
				}},
				{Name: "电视剧", Condition: "type=tv", Children: []Rule{
					{Name: "国产剧", Condition: "origin_country=CN"},
					{Name: "港台剧", Condition: "origin_country=TW;HK"},
					{Name: "欧美剧", Condition: "origin_country=US;GB;FR;DE;IT;ES"},
					{Name: "日韩剧", Condition: "origin_country=JP;KR"},
				}},
			}},
			{Kind: TemplateGenre, Rules: []Rule{
				{Name: "电影", Condition: "type=movie", Children: []Rule{
					{Name: "动画", Condition: "genres=动画"},
					{Name: "动作冒险", Condition: "genres=动作;冒险"},
					{Name: "犯罪悬疑", Condition: "genres=犯罪;悬疑;惊悚"},
					{Name: "喜剧", Condition: "genres=喜剧"},
					{Name: "科幻奇幻", Condition: "genres=科幻;奇幻"},
					{Name: "爱情", Condition: "genres=爱情"},
					{Name: "恐怖", Condition: "genres=恐怖"},
					{Name: "战争历史", Condition: "genres=战争;历史"},
					{Name: "纪录", Condition: "genres=纪录"},
					{Name: "剧情", Condition: "genres=剧情"},
				}},
				{Name: "电视剧", Condition: "type=tv", Children: []Rule{
					{Name: "综艺", Condition: "genres=脱口秀;真人秀"},
					{Name: "动画", Condition: "genres=动画"},
					{Name: "动作冒险", Condition: "genres=动作冒险"},
					{Name: "犯罪悬疑", Condition: "genres=犯罪;悬疑"},
					{Name: "喜剧", Condition: "genres=喜剧"},
					{Name: "科幻奇幻", Condition: "genres=Sci-Fi & Fantasy"},
					{Name: "儿童家庭", Condition: "genres=儿童;家庭"},
					{Name: "战争政治", Condition: "genres=War & Politics"},
					{Name: "纪录", Condition: "genres=纪录"},
					{Name: "剧情", Condition: "genres=剧情;肥皂剧"},
				}},
			}},
			{Kind: TemplateCustom, Rules: []Rule{
				{Name: "综艺", Condition: "type=tv，genres=脱口秀;真人秀"},
				{Name: "电影", Condition: "type=movie", Children: []Rule{
					{Name: "国产", Condition: "origin_country=CN"},
					{Name: "港台", Condition: "origin_country=TW;HK"},
				}},
				{Name: "电视剧", Condition: "type=tv", Children: []Rule{
					{Name: "日本动漫", Condition: "origin_country=JP，genres=动画"},
					{Name: "国产剧", Condition: "origin_country=CN"},
				}},
			}},
		},
	}
}

func (s *Service) Config() Config {
	cfg := s.loadConfig()
	if s != nil && s.settings != nil {
		cfg.Enabled = s.settings.Bool(settings.KeyMOClassificationEnabled)
	}
	return cfg
}

func (s *Service) Update(ctx context.Context, in Config) (Config, error) {
	if s == nil || s.settings == nil {
		return Config{}, domain.Errorf(domain.CodeInternal, "分类整理配置服务未就绪")
	}
	cfg, err := normalizeConfig(in)
	if err != nil {
		return Config{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Config{}, domain.Errorf(domain.CodeInternal, "序列化分类整理配置失败")
	}
	if err := s.settings.Update(ctx, map[string]string{
		settings.KeyMOClassificationEnabled: boolString(cfg.Enabled),
		settings.KeyMOClassificationConfig:  string(raw),
	}); err != nil {
		return Config{}, err
	}
	return s.Config(), nil
}

func (s *Service) loadConfig() Config {
	fallback := DefaultConfig()
	if s == nil || s.settings == nil {
		return fallback
	}
	raw := strings.TrimSpace(s.settings.String(settings.KeyMOClassificationConfig))
	if raw == "" {
		return fallback
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fallback
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return fallback
	}
	return normalized
}

func normalizeConfig(in Config) (Config, error) {
	in.Version = ConfigVersion
	in.SelectedTemplate = strings.ToLower(strings.TrimSpace(in.SelectedTemplate))
	if in.SelectedTemplate == "" {
		in.SelectedTemplate = TemplateMedia
	}
	want := map[string]bool{TemplateMedia: true, TemplateRegion: true, TemplateGenre: true, TemplateCustom: true}
	if !want[in.SelectedTemplate] {
		return Config{}, domain.Errorf(domain.CodeValidation, "分类模板类型无效")
	}
	if len(in.Templates) != len(want) {
		return Config{}, domain.Errorf(domain.CodeValidation, "必须保留内置模板一至三和自定义模板")
	}
	seen := make(map[string]bool, len(want))
	for i := range in.Templates {
		tpl := &in.Templates[i]
		tpl.Kind = strings.ToLower(strings.TrimSpace(tpl.Kind))
		if !want[tpl.Kind] || seen[tpl.Kind] {
			return Config{}, domain.Errorf(domain.CodeValidation, "分类模板缺失或重复")
		}
		seen[tpl.Kind] = true
		if tpl.Kind == TemplateCustom {
			if err := normalizeCustomRules(&tpl.Rules, levelPrimary, 1); err != nil {
				return Config{}, err
			}
			if err := validateCustomGenreConflicts(tpl.Rules); err != nil {
				return Config{}, err
			}
		} else if err := normalizeBuiltInTemplate(tpl); err != nil {
			return Config{}, err
		}
	}
	if err := normalizeSeriesList(&in.Series); err != nil {
		return Config{}, err
	}
	return in, nil
}

// normalizeSeriesList 归一化全局系列目录规则（T02 C-2）。
//
// 为什么不放进某个模板：系列目录是横跨模板的。「流浪地球系列」这个目录名
// 在地区模板下是 华语电影/流浪地球系列，在类型模板下是 科幻奇幻/流浪地球系列，
// 目录名本身与模板无关。放进模板就得在四个模板里各配一份，改一次系列名要改四处，
// 漏一处就出现两个同名系列目录。
func normalizeSeriesList(rules *[]SeriesRule) error {
	if len(*rules) == 0 {
		*rules = nil
		return nil
	}
	if len(*rules) > 64 {
		return domain.Errorf(domain.CodeValidation, "系列目录规则不能超过 64 项")
	}
	seen := make(map[string]bool, len(*rules))
	out := make([]SeriesRule, 0, len(*rules))
	for i := range *rules {
		rule := (*rules)[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if err := validatePathSegment(rule.Name); err != nil {
			return domain.Errorf(domain.CodeValidation, "系列规则目录：%v", err)
		}
		key := strings.ToLower(rule.Name)
		if seen[key] {
			return domain.Errorf(domain.CodeValidation, "系列规则存在重复目录：%s", rule.Name)
		}
		seen[key] = true
		rule.MediaType = strings.ToLower(strings.TrimSpace(rule.MediaType))
		switch rule.MediaType {
		case "", "movie", "tv":
		default:
			return domain.Errorf(domain.CodeValidation, "系列规则“%s”的 media_type 只支持 movie 和 tv", rule.Name)
		}
		// DirName 是真正会落进 RelativeSegments 的那一段（DirSegment 优先用它），
		// 所以必须和 Name 一样过 validatePathSegment：否则配一个 "../.." 就能
		// 让目录拼出目标根之外，而这条配置在界面上看不出任何异常。
		rawDirName := rule.DirName
		rule.DirName = strings.TrimSpace(rule.DirName)
		// 用原始值判「有没有填」而不是 trim 之后的值：只敲了一串空格的目录名
		// 会被 trim 成空串，然后 DirSegment 悄悄回落到「名称+系列」——用户在
		// 界面上填了东西却看到另一个目录名，且没有任何提示。
		if rawDirName != "" {
			if err := validatePathSegment(rule.DirName); err != nil {
				return domain.Errorf(domain.CodeValidation, "系列规则“%s”的目录名：%v", rule.Name, err)
			}
		}
		rule.SeriesKeywords = normalizeTokenList(rule.SeriesKeywords, 32)
		rule.Keywords = normalizeTokenList(rule.Keywords, 32)
		if len(rule.SeriesKeywords) == 0 && len(rule.Keywords) == 0 {
			return domain.Errorf(domain.CodeValidation, "系列规则“%s”至少需要一个关键词", rule.Name)
		}
		rule.Remark = strings.TrimSpace(rule.Remark)
		if len([]rune(rule.Remark)) > 255 {
			return domain.Errorf(domain.CodeValidation, "系列规则“%s”的备注过长", rule.Name)
		}
		if rule.Position < 0 {
			rule.Position = 0
		}
		out = append(out, rule)
	}
	// 按 position 排序，让「顺序即优先级」这件事在配置里看得见。
	// 稳定排序：position 相同时保持用户填写的顺序，不引入隐式顺序。
	sort.SliceStable(out, func(a, b int) bool { return out[a].Position < out[b].Position })
	*rules = out
	return nil
}

func normalizeBuiltInTemplate(tpl *Template) error {
	// C-3 放开「恰好两条」：以前这里写死 2（电影 + 电视剧），用户改目录名可以、
	// 增删一级不行。放开的原因是 classify_primary_categories 这张白名单表
	// 需要一个可枚举的一级集合，而两条写死的记录没法表达"用户自己的第三类"。
	//
	// 放开的是**下界**（允许新增），不是上界（仍必须保留内置的两条基准）：
	// Region / Genre / Custom 三个模板的二级字段（origin_country、genres）
	// 是按"电影/电视剧之下再细分"设计的，删掉任何一条整个二级体系就没有基准了。
	// 所以真正的约束落在末尾的 requireBuiltInRoots 上：
	//   - 至少一条 movie、一条 tv 必须存在（删内置基准 ⇒ 报错）；
	//   - 类型键仍然只收 movie / tv（媒体类型只有这两种）；
	//   - 同类型可以有多个一级（这才是"新增"能成立的唯一方式）。
	if len(tpl.Rules) == 0 {
		return domain.Errorf(domain.CodeValidation, "%s至少要保留一个一级分类", builtInTemplateName(tpl.Kind))
	}
	//
	// 但 movie / tv 两条内置记录必须留着，且不许改它们的类型键：
	// Region / Genre / Custom 三个模板的二级字段（origin_country、genres）
	// 是按"这两类之下再细分"设计的，拿掉任何一条会让整个二级体系失去基准。
	// 新增的一级则可以任意取名，但类型键仍收 movie / tv —— 类型只有这两种，
	// 真正的自由是目录名与层级结构，不是凭空多出一种媒体类型。
	childField := ""
	switch tpl.Kind {
	case TemplateRegion:
		childField = fieldCountry
	case TemplateGenre:
		childField = fieldGenres
	}
	seenNames := make(map[string]bool, len(tpl.Rules))
	seenTypes := make(map[string]bool, len(tpl.Rules))
	for i := range tpl.Rules {
		rule := &tpl.Rules[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if err := validatePathSegment(rule.Name); err != nil {
			return domain.Errorf(domain.CodeValidation, "%s一级分类目录：%v", builtInTemplateName(tpl.Kind), err)
		}
		nameKey := strings.ToLower(rule.Name)
		if seenNames[nameKey] {
			return domain.Errorf(domain.CodeValidation, "%s存在重复一级目录：%s", builtInTemplateName(tpl.Kind), rule.Name)
		}
		seenNames[nameKey] = true
		condition, err := normalizeCondition(rule.Condition)
		if err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s”的一级匹配条件无效：%v", builtInTemplateName(tpl.Kind), rule.Name, err)
		}
		parsed, _ := parseCondition(condition)
		if parsed.Field != fieldType || len(parsed.Values) != 1 || (parsed.Values[0] != "movie" && parsed.Values[0] != "tv") {
			return domain.Errorf(domain.CodeValidation, "%s一级匹配条件只能是 type=movie 或 type=tv（媒体类型只有这两种），目录名可以改", builtInTemplateName(tpl.Kind))
		}
		mediaType := parsed.Values[0]
		if seenTypes[mediaType] {
			return domain.Errorf(domain.CodeValidation, "%s一级匹配条件不能重复：type=%s", builtInTemplateName(tpl.Kind), mediaType)
		}
		// C-3：同一个媒体类型允许挂多个一级目录（"电影" 与 "华语电影" 都是 movie），
		// 一级判定是 first-match-wins，排在前面的那条先命中，所以多条共存是有意义的：
		// 用户可以把 movie 先拆成"电影"，再在后面补一条兜底的"电影-其他"。
		// 重复类型键不再报错 —— 但内置基准的检查挪到了函数末尾 requireBuiltInRoots。
		rule.Condition = "type=" + mediaType
		// 一级的结构化字段归一化回 type=X：它的类型身份只由 media_types 表达，
		// 留着一个被改写过的 Fields 会让 primaryKeyOf 先读到 MediaTypes、
		// 而 matchField 读到别的东西，两处对"这是一级"给出不同答案。
		// （primaryKeyOf 与 matchField 都读 RuleFields，所以这里必须清空，
		//  否则同一个规则在 C-8 目录清单和在实际分类路径上会落到不同分支。）
		rule.Fields = nil
		if tpl.Kind == TemplateMedia {
			rule.FallbackMode = ""
			rule.FallbackDir = ""
			if len(rule.Children) > 0 {
				return domain.Errorf(domain.CodeValidation, "内置模板一只支持一级目录")
			}
			continue
		}
		if err := normalizeBuiltInChildren(&rule.Children, builtInTemplateName(tpl.Kind), rule.Name, childField); err != nil {
			return err
		}
		if err := normalizeFallbackDir(rule, builtInTemplateName(tpl.Kind)); err != nil {
			return err
		}
	}
	return requireBuiltInRoots(tpl)
}

// requireBuiltInRoots 校验内置模板仍保留 movie / tv 两条基准一级（C-3）。
//
// 为什么单独一个函数：类型键的唯一性判断放在循环里，而"至少各留一条"
// 必须在全部规则归一化完之后才能判断，两件事的时机不同。
func requireBuiltInRoots(tpl *Template) error {
	var hasMovie, hasTV bool
	for _, rule := range tpl.Rules {
		field, values, err := rule.matchField()
		if err != nil || field != fieldType || len(values) == 0 {
			continue
		}
		switch normalizePrimaryKey(values[0]) {
		case "movie":
			hasMovie = true
		case "tv":
			hasTV = true
		}
	}
	if !hasMovie || !hasTV {
		return domain.Errorf(domain.CodeValidation,
			"%s必须同时保留电影（type=movie）和电视剧（type=tv）两条基准一级分类：地区与类型两级目录是按这两类设计的，可以新增，但不能删掉它们",
			builtInTemplateName(tpl.Kind))
	}
	return nil
}

// normalizeBuiltInChildren 归一化内置模板的二级目录，并下钻三级（T02 C-1）。
//
// 二级的匹配维度仍被钉死在模板指定的字段上（地区模板 origin_country、类型模板 genres），
// 这是内置模板的既有约定，不动。三级则放开到任务书给的那一组维度。
func normalizeBuiltInChildren(rules *[]Rule, templateName, parentName, field string) error {
	if len(*rules) > 32 {
		return domain.Errorf(domain.CodeValidation, "%s“%s”的二级分类不能超过 32 项", templateName, parentName)
	}
	seenNames := make(map[string]bool, len(*rules))
	seenValues := make(map[string]string)
	for i := range *rules {
		rule := &(*rules)[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if err := validatePathSegment(rule.Name); err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s”的二级分类目录：%v", templateName, parentName, err)
		}
		nameKey := strings.ToLower(rule.Name)
		if seenNames[nameKey] {
			return domain.Errorf(domain.CodeValidation, "%s“%s”存在重复二级目录：%s", templateName, parentName, rule.Name)
		}
		seenNames[nameKey] = true
		condition, err := normalizeCondition(rule.Condition)
		if err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的匹配条件无效：%v", templateName, parentName, rule.Name, err)
		}
		parsed, _ := parseCondition(condition)
		// 二级仍走表达式字符串：内置模板的二级条件是用户直接编辑的，
		// 结构化字段只作为三级与系列目录的落点，不去改这条既有路径。
		if parsed.Field != field {
			return domain.Errorf(domain.CodeValidation, "%s二级分类只能使用 %s 匹配条件", templateName, field)
		}
		rule.Condition = condition
		rule.FallbackMode = ""
		rule.FallbackDir = ""
		if err := normalizeSeriesName(rule, templateName); err != nil {
			return err
		}
		if err := normalizeTertiaryChildren(rule, templateName, parentName); err != nil {
			return err
		}
		for _, value := range parsed.Values {
			key := strings.ToLower(value)
			if previous := seenValues[key]; previous != "" {
				return domain.Errorf(domain.CodeValidation, "%s“%s”下“%s”和“%s”存在重复匹配值：%s", templateName, parentName, previous, rule.Name, value)
			}
			seenValues[key] = rule.Name
		}
	}
	return nil
}

// normalizeTertiaryChildren 归一化三级目录（T02 C-1）。
//
// 三级只有一层：Children 下面不能再挂子目录。写成报错而不是静默截断，是因为
// 多截出来的那几段目录在 UI 上根本看不见，用户会以为配好了却发现目录没出现。
// 三级的匹配维度用结构化字段（Rule.Fields），为空时仍回落表达式字符串。
func normalizeTertiaryChildren(parent *Rule, templateName, parentName string) error {
	if len(parent.Children) == 0 {
		return nil
	}
	if parent.Name == "" {
		return domain.Errorf(domain.CodeValidation, "%s的三级分类缺少上级目录名", templateName)
	}
	if len(parent.Children) > 32 {
		return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的三级分类不能超过 32 项", templateName, parentName, parent.Name)
	}
	seenNames := make(map[string]bool, len(parent.Children))
	for i := range parent.Children {
		rule := &parent.Children[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if err := validatePathSegment(rule.Name); err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的三级分类目录：%v", templateName, parentName, rule.Name, err)
		}
		if seenNames[strings.ToLower(rule.Name)] {
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s”存在重复三级目录：%s", templateName, parentName, parent.Name, rule.Name)
		}
		seenNames[strings.ToLower(rule.Name)] = true
		if len(rule.Children) > 0 {
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s/%s”不能再配置下级目录，分类目录最多支持三级", templateName, parentName, parent.Name, rule.Name)
		}
		if err := normalizeRuleFields(rule, templateName, parentName); err != nil {
			return err
		}
		if err := normalizeSeriesName(rule, templateName); err != nil {
			return err
		}
		rule.FallbackMode = ""
		rule.FallbackDir = ""
	}
	return nil
}

// normalizeRuleFields 归一化结构化条件字段（T02 C-3）。
// 结构化字段为空时回落解析 Condition 表达式字符串，所以 Condition 的语法一字未改。
func normalizeRuleFields(rule *Rule, templateName, parentName string) error {
	if rule.Fields == nil {
		return nil
	}
	fields := *rule.Fields
	fields.MediaTypes = normalizeTokenList(fields.MediaTypes, 32)
	fields.OriginCountry = normalizeTokenList(fields.OriginCountry, 32)
	fields.OriginalLanguage = normalizeTokenList(fields.OriginalLanguage, 32)
	fields.Keywords = normalizeTokenList(fields.Keywords, 32)
	fields.SeriesKeywords = normalizeTokenList(fields.SeriesKeywords, 32)
	for i := range fields.MediaTypes {
		fields.MediaTypes[i] = strings.ToLower(fields.MediaTypes[i])
		switch fields.MediaTypes[i] {
		case "movie", "tv":
		default:
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的 media_types 只支持 movie 和 tv：%s",
				templateName, parentName, rule.Name, fields.MediaTypes[i])
		}
	}
	for i := range fields.OriginCountry {
		fields.OriginCountry[i] = strings.ToUpper(fields.OriginCountry[i])
	}
	if fields.Year != nil {
		if err := normalizeYearRange(fields.Year); err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的年份区间无效：%v", templateName, parentName, rule.Name, err)
		}
	}
	if len(fields.GenreIDs) > 32 {
		return domain.Errorf(domain.CodeValidation, "%s“%s/%s”的 genre_ids 不能超过 32 项", templateName, parentName, rule.Name)
	}
	rule.Fields = &fields
	return nil
}

// normalizeYearRange 归一化年份区间：单年写成 from==to，区间要求 from<=to。
func normalizeYearRange(r *YearRange) error {
	if r.To < 0 {
		r.To = 0
	}
	if r.To != 0 && r.To < r.From {
		return fmt.Errorf("区间结束年份 %d 小于起始年份 %d", r.To, r.From)
	}
	if r.To == 0 {
		r.To = r.From
	}
	return nil
}

// normalizeSeriesName 归一化系列目录名（C-2）。
// 空串表示这一层不产出系列目录；非空必须能安全当路径段用。
func normalizeSeriesName(rule *Rule, templateName string) error {
	rule.Series = strings.TrimSpace(rule.Series)
	if rule.Series == "" {
		return nil
	}
	if err := validatePathSegment(rule.Series); err != nil {
		return domain.Errorf(domain.CodeValidation, "%s“%s”的系列目录：%v", templateName, rule.Name, err)
	}
	return nil
}

// normalizeTokenList 去空白、去空项、按出现顺序去重，并限制长度上限。
func normalizeTokenList(in []string, max int) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func builtInTemplateName(kind string) string {
	switch kind {
	case TemplateMedia:
		return "内置模板一"
	case TemplateRegion:
		return "内置模板二"
	case TemplateGenre:
		return "内置模板三"
	default:
		return "内置模板"
	}
}

// normalizeCustomRules 归一化自定义模板的目录树（T02 C-1 起支持三级）。
//
// levelLabel 只用于报错文案；深度由 depth 累加控制，超过 maxRuleDepth 直接报错，
// 而不是多截出来的那几段在 UI 上看不见、用户却以为配好了。
func normalizeCustomRules(rules *[]Rule, levelLabel string, depth int) error {
	if len(*rules) == 0 || len(*rules) > 32 {
		return domain.Errorf(domain.CodeValidation, "%s必须配置 1～32 项", levelLabel)
	}
	if depth > maxRuleDepth {
		return domain.Errorf(domain.CodeValidation, "分类目录最多支持三级")
	}
	seenNames := make(map[string]bool, len(*rules))
	seenConditions := make(map[string]string, len(*rules))
	for i := range *rules {
		rule := &(*rules)[i]
		rule.Name = strings.TrimSpace(rule.Name)
		if err := validatePathSegment(rule.Name); err != nil {
			return domain.Errorf(domain.CodeValidation, "%s目录：%v", levelLabel, err)
		}
		nameKey := strings.ToLower(rule.Name)
		if seenNames[nameKey] {
			return domain.Errorf(domain.CodeValidation, "%s存在重复目录：%s", levelLabel, rule.Name)
		}
		seenNames[nameKey] = true

		condition, err := normalizeExpression(rule.Condition)
		if err != nil {
			return domain.Errorf(domain.CodeValidation, "%s“%s”的匹配条件无效：%v", levelLabel, rule.Name, err)
		}
		rule.Condition = condition
		if err := normalizeRuleFields(rule, levelLabel, ""); err != nil {
			return err
		}
		conditionKey := rule.matchKey()
		if previous := seenConditions[conditionKey]; previous != "" {
			return domain.Errorf(domain.CodeValidation, "%s“%s”和“%s”的匹配条件完全相同", levelLabel, previous, rule.Name)
		}
		seenConditions[conditionKey] = rule.Name
		if err := normalizeSeriesName(rule, levelLabel); err != nil {
			return err
		}

		if len(rule.Children) > 0 {
			if err := normalizeCustomRules(&rule.Children, nextLevelLabel(levelLabel), depth+1); err != nil {
				return err
			}
			if err := normalizeFallbackDir(rule, "自定义模板"); err != nil {
				return err
			}
		} else {
			rule.FallbackMode = ""
			rule.FallbackDir = ""
		}
	}
	return nil
}

// nextLevelLabel 递归下钻时的层级标签。三级及更深统一叫「三级分类」，
// 错误信息里出现「四级分类」对用户没有意义 —— 他要知道的只是不能再往下配了。
func nextLevelLabel(current string) string {
	switch current {
	case levelPrimary:
		return levelSecond
	case levelSecond:
		return levelThird
	}
	return levelThird
}

// matchKey 是同级重复条件检测用的键。
// 结构化字段非空时按结构化字段算，否则按表达式字符串算 ——
// 两条配置命中范围可能完全一样（"origin_country=CN" 与 fields.origin_country=[CN]），
// 只按其中一种算会把它们当成两条不同的规则放进同一层，实际行为是后者永远不生效。
func (r Rule) matchKey() string {
	if r.Fields != nil && !r.Fields.Empty() {
		if raw, err := json.Marshal(r.Fields); err == nil {
			return "fields:" + string(raw)
		}
	}
	return "cond:" + strings.ToLower(strings.TrimSpace(r.Condition))
}

func normalizeFallbackDir(rule *Rule, templateName string) error {
	rule.FallbackMode = strings.ToLower(strings.TrimSpace(rule.FallbackMode))
	rule.FallbackDir = strings.TrimSpace(rule.FallbackDir)
	if rule.FallbackMode == "" {
		rule.FallbackMode = "self"
	}
	if rule.FallbackMode == "self" {
		rule.FallbackDir = ""
		return nil
	}
	if rule.FallbackMode != "directory" {
		return domain.Errorf(domain.CodeValidation, "%s“%s”的未命中处理方式无效", templateName, rule.Name)
	}
	if rule.FallbackDir == "" {
		return domain.Errorf(domain.CodeValidation, "%s“%s”的未命中目录不能为空", templateName, rule.Name)
	}
	if err := validatePathSegment(rule.FallbackDir); err != nil {
		return domain.Errorf(domain.CodeValidation, "%s“%s”的未命中目录：%v", templateName, rule.Name, err)
	}
	return nil
}

func validateCustomGenreConflicts(rules []Rule) error {
	type scopedGenres struct {
		path   string
		types  map[string]bool
		genres map[string]string
	}
	entries := make([]scopedGenres, 0)
	var walk func([]Rule, []string, map[string]bool) error
	walk = func(items []Rule, parentPath []string, parentTypes map[string]bool) error {
		for _, rule := range items {
			conditions, err := parseExpression(rule.Condition)
			if err != nil {
				return err
			}
			path := append(append([]string(nil), parentPath...), rule.Name)
			types := cloneStringSet(parentTypes)
			genres := make(map[string]string)
			for _, condition := range conditions {
				switch condition.Field {
				case "type":
					types = intersectTypeScope(types, condition.Values)
				case "genres":
					for _, value := range condition.Values {
						genres[strings.ToLower(value)] = value
					}
				}
			}
			if len(genres) > 0 && (types == nil || len(types) > 0) {
				entry := scopedGenres{path: strings.Join(path, "/"), types: types, genres: genres}
				for _, previous := range entries {
					if !stringSetsOverlap(previous.types, entry.types) {
						continue
					}
					for key, value := range entry.genres {
						if previous.genres[key] != "" {
							return domain.Errorf(domain.CodeValidation, "自定义分类“%s”和“%s”在相同 type 范围内重复匹配 genres=%s", previous.path, entry.path, value)
						}
					}
				}
				entries = append(entries, entry)
			}
			if len(rule.Children) > 0 {
				if err := walk(rule.Children, path, types); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(rules, nil, nil)
}

func cloneStringSet(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	cloned := make(map[string]bool, len(source))
	for value := range source {
		cloned[value] = true
	}
	return cloned
}

func intersectTypeScope(scope map[string]bool, values []string) map[string]bool {
	next := make(map[string]bool, len(values))
	for _, value := range values {
		next[strings.ToLower(value)] = true
	}
	if scope == nil {
		return next
	}
	intersection := make(map[string]bool)
	for value := range scope {
		if next[value] {
			intersection[value] = true
		}
	}
	return intersection
}

func stringSetsOverlap(left, right map[string]bool) bool {
	// 未限定 type 视为 movie/tv 通配，会与任意显式 type 范围重叠。
	if left == nil || right == nil {
		return true
	}
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	for value := range left {
		if right[value] {
			return true
		}
	}
	return false
}

type parsedCondition struct {
	Field  string
	Values []string
}

func normalizeCondition(raw string) (string, error) {
	condition, err := parseCondition(raw)
	if err != nil {
		return "", err
	}
	return condition.Field + "=" + strings.Join(condition.Values, ";"), nil
}

func normalizeExpression(raw string) (string, error) {
	conditions, err := parseExpression(raw)
	if err != nil {
		return "", err
	}
	normalized := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		normalized = append(normalized, condition.Field+"="+strings.Join(condition.Values, ";"))
	}
	return strings.Join(normalized, "，"), nil
}

func parseExpression(raw string) ([]parsedCondition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("不能为空")
	}
	if utf8.RuneCountInString(raw) > 500 {
		return nil, fmt.Errorf("不能超过 500 个字符")
	}
	raw = strings.ReplaceAll(raw, ",", "，")
	parts := strings.Split(raw, "，")
	if len(parts) == 0 || len(parts) > 8 {
		return nil, fmt.Errorf("每条规则必须配置 1～8 个匹配条件")
	}
	conditions := make([]parsedCondition, 0, len(parts))
	seenFields := make(map[string]bool, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("逗号之间的匹配条件不能为空")
		}
		condition, err := parseCondition(part)
		if err != nil {
			return nil, err
		}
		if seenFields[condition.Field] {
			return nil, fmt.Errorf("同一条规则不能重复配置字段 %s", condition.Field)
		}
		seenFields[condition.Field] = true
		conditions = append(conditions, condition)
	}
	return conditions, nil
}

func parseCondition(raw string) (parsedCondition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedCondition{}, fmt.Errorf("不能为空")
	}
	if utf8.RuneCountInString(raw) > 500 || strings.Count(raw, "=") != 1 {
		return parsedCondition{}, fmt.Errorf("格式应为 field=value1;value2")
	}
	field, valueList, _ := strings.Cut(raw, "=")
	field = strings.ToLower(strings.TrimSpace(field))
	if !validFieldName(field) {
		return parsedCondition{}, fmt.Errorf("字段名只能包含字母、数字和下划线，且不能以数字开头")
	}
	parts := strings.Split(valueList, ";")
	if len(parts) == 0 || len(parts) > 32 {
		return parsedCondition{}, fmt.Errorf("匹配值必须为 1～32 项")
	}
	values := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		switch field {
		case "type":
			value = strings.ToLower(value)
		case "origin_country":
			value = strings.ToUpper(value)
		}
		if value == "" || utf8.RuneCountInString(value) > 80 {
			return parsedCondition{}, fmt.Errorf("匹配值不能为空且不能超过 80 个字符")
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return parsedCondition{}, fmt.Errorf("匹配值不能包含控制字符")
			}
		}
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			values = append(values, value)
		}
	}
	return parsedCondition{Field: field, Values: values}, nil
}

func validFieldName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validatePathSegment(value string) error {
	if value == "" {
		return fmt.Errorf("目录名不能为空")
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("目录名包含路径分隔符或非法层级标记")
	}
	if utf8.RuneCountInString(value) > 120 {
		return fmt.Errorf("目录名不能超过 120 个字符")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("目录名不能包含控制字符")
		}
	}
	return nil
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// replaceSeriesRules 整体替换配置里的系列目录规则（C-8）。
//
// 走 Config.Update 而不是直接写 settings：系列规则要过 normalizeSeriesList 的
// 校验（目录名合法性、重名、关键词非空），而那条校验只在 Update 这条路径上跑。
// 直接塞进设置值的后果是配置里出现一条永不命中的非法规则，而界面上看不出异常。
//
// 空列表是合法输入（一份不含 series 段的规则文件应当把系列规则清空），
// 所以这里不能用「len(in) == 0 就跳过」那种省事写法。
func (s *Service) replaceSeriesRules(ctx context.Context, rules []SeriesRule) error {
	if s == nil || s.settings == nil {
		return domain.Errorf(domain.CodeInternal, "分类整理配置服务未就绪")
	}
	cfg := s.Config()
	// 归一化先跑一遍再 Update：Update 里那遍会因为字段已经是归一化后的值而
	// 静默通过，所以这里的报错要原样抛出去，不能等 Update 再说。
	if err := normalizeSeriesList(&rules); err != nil {
		return err
	}
	cfg.Series = rules
	_, err := s.Update(ctx, cfg)
	return err
}
