// Package identity 实现订阅转存前的「身份校验」。
//
// 背景：litepan 的订阅追新在搜索到候选资源后会直接发起转存，如果候选其实是
// 另一部剧、另一季、另一集，或者是一个混拼了多部作品的分享，错误的内容就会
// 进入用户网盘并且可能覆盖已有文件 —— 这是 litepan 唯一一条会损坏用户已有资产的
// 路径。参考实现 的不变式是「**未通过身份校验，一律不转存**」，本包把这条不变式落地。
//
// 六个校验维度：标题 / 类型(movie|tv) / 年份 / 季号(tv) / 集号(tv) / 纯度。
//
// 关于「拿不到文件清单怎么办」：参考实现 还有一层分享隔离区（quarantine）兜底，本期
// 不做隔离区（总纲「本期不动」第 6 条），改用等效简化 —— **拿不到任何可用证据就不
// 转存**，见 ReasonManifestUnavailable 的注释。
//
// 关于 reason_code 与维度的映射：参考实现 的校验逻辑编译在 Cython .so 里，我们只能
// 从符号名还原出 reason_code 清单，无法还原「哪个维度对应哪个 reason_code」。下面
// 的映射是按语义推断的，Result.Dimension 字段把具体维度单独记录下来，便于日志
// 检索与后续按真实行为校准。
//
// 本包是纯函数，不做 IO、不读配置、不打日志，方便单测。
package identity

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"litepan/internal/mediaorganize/rules"
)

// Reason 是身份校验的判定原因。取值与 参考实现 的 reason_code 字符串保持一致，
// 便于日后对齐两侧日志。
type Reason string

const (
	// ReasonMatched 六维全部通过。
	ReasonMatched Reason = "IDENTITY_MATCHED"
	// ReasonTitleMismatch 标题不匹配；也用于承载年份/季号/集号不匹配
	// （通过 Result.Dimension 区分：year / season / episode）。
	ReasonTitleMismatch Reason = "TITLE_MISMATCH"
	// ReasonTMDBIDMismatch 证据自带的 TMDB ID 与订阅期望的 TMDB ID 不一致。
	ReasonTMDBIDMismatch Reason = "TMDB_ID_MISMATCH"
	// ReasonUnrecognizedMedia 拿到了证据，但识别不出是电影还是剧集。
	ReasonUnrecognizedMedia Reason = "UNRECOGNIZED_MEDIA"
	// ReasonMediaTypeMismatch 类型不一致（例如电影资源撞上剧集订阅）。
	ReasonMediaTypeMismatch Reason = "MEDIA_TYPE_MISMATCH"
	// ReasonMixedOrAmbiguous 纯度不足，分享里混进了多部作品。
	ReasonMixedOrAmbiguous Reason = "MIXED_OR_AMBIGUOUS"
	// ReasonUnverifiable 拿到了证据，但缺少任何一个可评估的维度。
	ReasonUnverifiable Reason = "IDENTITY_UNVERIFIABLE"
	// ReasonManifestIncomplete 清单接口调通了，但只列出了目录、没有文件条目。
	ReasonManifestIncomplete Reason = "IDENTITY_MANIFEST_INCOMPLETE"
	// ReasonManifestUnavailable 什么都没拿到（磁力/ed2k 只有链接、没有文件清单；
	// 分享链接侧没有展开清单的接口）。这是替代隔离区的等效方案：**不转存**。
	ReasonManifestUnavailable Reason = "IDENTITY_MANIFEST_UNAVAILABLE"
)

// Evidence 是证据档位。litepan 的不同投递路径能拿到的证据强度不同：
//
//   - EvidenceNone：只有磁力/ed2k 链接，什么都验证不了。
//   - EvidenceMetadata：有资源标题/备注等资源级元数据（123、115、观影等网盘来源）。
//   - EvidenceManifest：拿到了逐文件清单（本期尚无来源产出，预留给未来的
//     分享清单接口与离线下载落地后的核对）。
type Evidence string

const (
	EvidenceNone     Evidence = "none"
	EvidenceMetadata Evidence = "metadata"
	EvidenceManifest Evidence = "manifest"
)

// File 是清单里的一个文件条目。
type File struct {
	Name string
	Size int64
}

// Manifest 是候选资源的全部可用证据。
type Manifest struct {
	// Kind 证据档位，决定后面走哪条判定路径。
	Kind Evidence

	// Files 逐文件清单，仅 Kind==EvidenceManifest 时有效。
	Files []File
	// ListingSucceeded 表示清单接口本身是否成功返回。
	// 成功但一条文件都没拿到，说明是分享内容不完整（只列目录）。
	ListingSucceeded bool
	// DirectoriesOnly 表示清单只列出了目录，没有文件条目。
	DirectoriesOnly bool

	// TMDBID 是证据自带的 TMDB ID（形如 "157350"），空串表示不提供。
	TMDBID string
	// Titles 是候选侧标题来源（资源标题、备注等），任意一个能与订阅对上即可。
	Titles []string
	// MediaType 是候选侧声明的类型，取值 movie / tv，空串表示未声明。
	MediaType string
	// Year 是候选侧年份，0 表示未知。
	Year int
	// Season / Episode / EndEpisode 是候选侧季集证据，0 表示不提供。
	Season     int
	Episode    int
	EndEpisode int
}

// Request 是订阅期望的身份。季号/集号为 0 表示该项不参与校验。
type Request struct {
	ExpectedTitle string
	// ExpectedAltTitles 是标题的其他形式（例如 TMDB 的 OriginalTitle）。
	// 必需：中文 TMDB 标题与外文原名指向同一部作品，而网盘资源标题两者皆可能出现，
	// 只比对单一标题会把正确资源误判成别的作品。
	ExpectedAltTitles []string
	ExpectedTMDBID    string
	ExpectedType      string
	ExpectedYear      int
	ExpectedSeason    int
	ExpectedEpisode   int
}

// Options 是校验参数。
type Options struct {
	// PurityRatio 纯度阈值，主标题文件占比低于它判为 MIXED_OR_AMBIGUOUS。
	//
	// ⚠️ 默认值 0.8 是**推测值**，不是从 参考实现 逆向确认的数值：参考实现 的
	// dominant_ratio 阈值只存在于 Cython 编译后的 .so 中，我们只能读到符号名，
	// 读不到字面量。0.8 是工程上的常用起点（允许 20% 的辅助文件），上线后应按
	// 实际误转存率校准。设置项 mo_subscription_identity_purity_ratio 可在线调整。
	PurityRatio float64

	// AllowManifestUnavailable 是保留的逃生阀，默认 false。
	// 置 true 会破坏「拿不到证据就不转存」的不变式，仅用于排查“为什么某个
	// 来源一条都转不了”，管理界面必须标红警告。
	AllowManifestUnavailable bool
}

// DefaultPurityRatio 是纯度阈值的推测默认值，见 Options.PurityRatio 的说明。
const DefaultPurityRatio = 0.8

// Dimension 记录判定失败落在哪个维度，供日志检索与后续校准。
const (
	DimensionEvidence     = "evidence"
	DimensionTMDBID       = "tmdb_id"
	DimensionMediaType    = "media_type"
	DimensionTitle        = "title"
	DimensionYear         = "year"
	DimensionSeason       = "season"
	DimensionEpisode      = "episode"
	DimensionPurity       = "purity"
	DimensionCompleteness = "completeness"
)

// Result 是校验结果。
type Result struct {
	// Passed 是否允许转存。只有 Passed==true 才允许发起转存。
	Passed bool
	// Reason 判定原因，见 Reason 常量。
	Reason Reason
	// Dimension 失败维度，成功时为空。
	Dimension string
	// Detail 供日志与排查使用的明细，不参与判定。
	Detail map[string]any
}

// Fail 是失败结果的快捷构造。
func Fail(reason Reason, dimension, message string, detail map[string]any) Result {
	if detail == nil {
		detail = map[string]any{}
	}
	if message != "" {
		detail["message"] = message
	}
	return Result{Passed: false, Reason: reason, Dimension: dimension, Detail: detail}
}

// Pass 是通过结果的快捷构造。
func Pass(detail map[string]any) Result {
	if detail == nil {
		detail = map[string]any{}
	}
	return Result{Passed: true, Reason: ReasonMatched, Detail: detail}
}

// item 是从一条证据（一个文件名或一段元数据标题）解析出来的身份。
type item struct {
	title      string
	titleKey   string
	year       int
	season     int
	episode    int
	endEpisode int
	isTV       bool
}

// coversEpisode 判断该条目是否覆盖指定集号。
func (i item) coversEpisode(ep int) bool {
	if i.episode <= 0 {
		return false
	}
	last := i.endEpisode
	if last < i.episode {
		last = i.episode
	}
	return ep >= i.episode && ep <= last
}

// Validate 是校验入口：req 是订阅期望，m 是候选证据，opts 是阈值。
// 无 IO、无副作用，同样的入参永远得到同样的结果。
func Validate(req Request, m Manifest, opts Options) Result {
	threshold := opts.PurityRatio
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultPurityRatio
	}
	detail := map[string]any{
		"expected_title": req.ExpectedTitle,
		"expected_type":  req.ExpectedType,
		"evidence_kind":  string(m.Kind),
	}

	switch m.Kind {
	case EvidenceManifest:
		if !m.ListingSucceeded {
			return Fail(ReasonManifestUnavailable, DimensionEvidence,
				"分享清单接口未成功返回，无法验证身份", detail)
		}
		if len(m.Files) == 0 {
			if m.DirectoriesOnly {
				return Fail(ReasonManifestIncomplete, DimensionCompleteness,
					"分享清单只列出目录，没有文件条目，无法验证身份", detail)
			}
			return Fail(ReasonManifestUnavailable, DimensionEvidence,
				"分享清单为空，无法验证身份", detail)
		}
	case EvidenceMetadata:
		if len(m.Titles) == 0 && m.TMDBID == "" && m.MediaType == "" &&
			m.Year == 0 && m.Season == 0 && m.Episode == 0 {
			return Fail(ReasonUnverifiable, DimensionEvidence,
				"候选没有提供任何可用于校验的元数据", detail)
		}
	default:
		return unavailable(m, opts, detail)
	}

	items := collectItems(m)
	if len(items) == 0 {
		return Fail(ReasonUnrecognizedMedia, DimensionEvidence,
			"证据中没有任何可以识别的影片标题", detail)
	}

	// 1. TMDB ID：证据自带且与期望冲突时直接拒绝。
	if m.TMDBID != "" && req.ExpectedTMDBID != "" && !sameTMDBID(m.TMDBID, req.ExpectedTMDBID) {
		detail["candidate_tmdb_id"] = m.TMDBID
		detail["expected_tmdb_id"] = req.ExpectedTMDBID
		return Fail(ReasonTMDBIDMismatch, DimensionTMDBID,
			"资源 TMDB ID 与订阅不一致", detail)
	}

	// 2. 类型。
	mediaType := detectMediaType(items, m)
	detail["candidate_type"] = mediaType
	if mediaType == "" {
		return Fail(ReasonUnrecognizedMedia, DimensionMediaType,
			"识别不出资源是电影还是剧集", detail)
	}
	if req.ExpectedType == "movie" || req.ExpectedType == "tv" {
		if mediaType != req.ExpectedType {
			detail["expected_type"] = req.ExpectedType
			return Fail(ReasonMediaTypeMismatch, DimensionMediaType,
				fmt.Sprintf("资源类型 %s 与订阅类型 %s 不一致", mediaType, req.ExpectedType), detail)
		}
	} else {
		detail["expected_type"] = "(未指定)"
	}

	// 3. 标题。
	expectedKeys := expectedTitleKeys(req)
	detail["expected_title_keys"] = expectedKeys
	candidateKeys := titleKeys(items)
	detail["candidate_title_keys"] = candidateKeys
	if len(expectedKeys) > 0 && !titleMatches(expectedKeys, candidateKeys) {
		return Fail(ReasonTitleMismatch, DimensionTitle,
			fmt.Sprintf("资源标题 %q 与订阅标题 %q 不匹配", items[0].title, req.ExpectedTitle), detail)
	}

	// 4. 年份。
	if req.ExpectedYear > 0 {
		years := distinctInts(yearsOf(items))
		detail["candidate_years"] = years
		if len(years) > 0 && !containsInt(years, req.ExpectedYear) {
			detail["expected_year"] = req.ExpectedYear
			return Fail(ReasonTitleMismatch, DimensionYear,
				fmt.Sprintf("资源年份 %v 与订阅年份 %d 不一致", years, req.ExpectedYear), detail)
		}
	}

	if req.ExpectedType == "tv" {
		// 5. 季号。
		if req.ExpectedSeason > 0 {
			seasons := distinctInts(seasonsOf(items))
			detail["candidate_seasons"] = seasons
			if len(seasons) > 0 && !containsInt(seasons, req.ExpectedSeason) {
				detail["expected_season"] = req.ExpectedSeason
				return Fail(ReasonTitleMismatch, DimensionSeason,
					fmt.Sprintf("资源季号 %v 与订阅季号 %d 不一致", seasons, req.ExpectedSeason), detail)
			}
		}
		// 6. 集号。
		if req.ExpectedEpisode > 0 {
			covered := coversEpisode(items, req.ExpectedEpisode)
			detail["expected_episode"] = req.ExpectedEpisode
			detail["candidate_episodes"] = episodesOf(items)
			if len(covered) == 0 {
				return Fail(ReasonTitleMismatch, DimensionEpisode,
					fmt.Sprintf("资源集号 %v 未覆盖订阅需要的第 %d 集", episodesOf(items), req.ExpectedEpisode), detail)
			}
		}
	}

	// 7. 纯度：清单里混进多部作品。
	if m.Kind == EvidenceManifest {
		ratio, dominant, groups := purity(items)
		detail["dominant_ratio"] = ratio
		detail["dominant_title_key"] = dominant
		detail["title_groups"] = groups
		if len(groups) > 1 && ratio < threshold {
			detail["purity_threshold"] = threshold
			return Fail(ReasonMixedOrAmbiguous, DimensionPurity,
				fmt.Sprintf("资源纯度 %.2f 低于阈值 %.2f，分享里混有多部作品", ratio, threshold), detail)
		}
	}

	return Pass(detail)
}

// unavailable 处理「什么都没拿到」的情况。这是替代隔离区的等效方案：
// 无法证明候选就是订阅要的那部作品时，一律不转存。
func unavailable(m Manifest, opts Options, detail map[string]any) Result {
	result := Fail(ReasonManifestUnavailable, DimensionEvidence,
		"候选没有可验证的文件清单，仅凭链接无法确认身份，不发起转存", detail)
	if opts.AllowManifestUnavailable {
		// 保留逃生阀：只在人工排查时打开，日志仍保留原 reason 便于追溯。
		result.Passed = true
		result.Detail["allow_manifest_unavailable"] = true
	}
	return result
}

// collectItems 把证据展开成逐条可评估的身份。
func collectItems(m Manifest) []item {
	switch m.Kind {
	case EvidenceManifest:
		items := make([]item, 0, len(m.Files))
		for _, f := range m.Files {
			items = append(items, itemFromText(f.Name))
		}
		return items
	case EvidenceMetadata:
		primary := itemFromText(firstNonEmpty(m.Titles))
		// 元数据层显式给出的季集/年份优先于标题解析结果：资源标题往往写成
		// 「XX.S01.1080p」这种形式，正则解析不稳，而服务端字段是可信的。
		if m.Year > 0 {
			primary.year = m.Year
		}
		if m.Season > 0 {
			primary.season = m.Season
			primary.isTV = true
		}
		if m.Episode > 0 {
			primary.episode = m.Episode
			primary.isTV = true
		}
		if m.EndEpisode > 0 {
			primary.endEpisode = m.EndEpisode
			primary.isTV = true
		}
		switch m.MediaType {
		case "movie", "tv":
			primary.isTV = m.MediaType == "tv"
		}
		return []item{primary}
	default:
		return nil
	}
}

// itemFromText 解析一段文本（文件名或资源标题），拿到标题/年份/季集。
func itemFromText(text string) item {
	text = strings.TrimSpace(text)
	if text == "" {
		return item{}
	}
	parsed := rules.NormalizeParsedMedia(rules.ParseFilenameStrict(text))
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		// 解析不出标题时退化为原串，保证至少还有一个可比较的键。
		title = text
	}
	res := item{
		title:    title,
		titleKey: rules.NormalizeTitleKey(title),
		year:     intValue(parsed.Year),
		season:   intValue(parsed.Season),
		episode:  intValue(parsed.Episode),
		isTV:     parsed.Type == "episode",
	}
	// 多集文件（E01-E08）里 rules 只给出起始集，这里从原始文本里补结束集。
	if res.episode > 0 {
		if end := endEpisodeFromText(text); end > res.episode {
			res.endEpisode = end
		}
	}
	return res
}

func firstNonEmpty(values []string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// detectMediaType 判定资源类型。同一个清单里既有电影又有剧集，说明是混拼。
func detectMediaType(items []item, m Manifest) string {
	var hasTV, hasMovie bool
	for _, it := range items {
		if it.isTV || it.season > 0 || it.episode > 0 {
			hasTV = true
		} else if it.titleKey != "" {
			hasMovie = true
		}
	}
	switch {
	case hasTV && hasMovie:
		return "mixed"
	case hasTV:
		return "tv"
	case hasMovie:
		return "movie"
	}
	return strings.TrimSpace(m.MediaType)
}

func expectedTitleKeys(req Request) []string {
	keys := titleKeysFromTexts(append([]string{req.ExpectedTitle}, req.ExpectedAltTitles...))
	if len(keys) == 0 {
		return nil
	}
	// ExpectedTitle 排在最前，调用方取第一个作为展示用标题。
	return keys
}

func titleKeysFromTexts(texts []string) []string {
	seen := make(map[string]struct{}, len(texts))
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		key := itemFromText(t).titleKey
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func titleKeys(items []item) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it.titleKey == "" {
			continue
		}
		if _, ok := seen[it.titleKey]; ok {
			continue
		}
		seen[it.titleKey] = struct{}{}
		out = append(out, it.titleKey)
	}
	return out
}

// titleMatches 判断订阅标题与候选标题是否指向同一部作品。
//
// 归一化口径复用 rules.NormalizeTitleKey（去非字母数字 + 转小写），不另写一套。
// 比较分两级：
//   - 完全相等：直接匹配。
//   - 包含匹配：候选标题里嵌着订阅标题（"流浪地球 蓝光" ⊃ "流浪地球"）。
//
// 包含匹配带一道护栏：如果紧跟在订阅标题之后的内容表明它是另一部作品，视为
// **续集**而不是修饰词，直接判不匹配。否则 "流浪地球2" 会被 "流浪地球" 的订阅
// 认领走，把续集转存成本片 —— 这是订阅追新最容易犯、后果也最直接的错误。
func titleMatches(expected, candidate []string) bool {
	for _, cand := range candidate {
		for _, exp := range expected {
			if cand == exp {
				return true
			}
			if containsTitleKey(cand, exp) {
				return true
			}
		}
	}
	return false
}

// sequelMarkers 是「跟在标题后面即视为另一部作品」的续集/衍生标记，按归一化后的
// 标题键比较（已去非字母数字并转小写）。
var sequelMarkers = []string{"前传", "续集", "重制", "新版", "spinoff", "sequel", "prequel"}

// containsTitleKey 判断 candidate 是否以 expected 为前缀，且前缀之后不是续集标记。
func containsTitleKey(candidate, expected string) bool {
	idx := strings.Index(candidate, expected)
	if idx < 0 {
		return false
	}
	rest := candidate[idx+len(expected):]
	if rest == "" {
		return true
	}
	// 数字续集：「流浪地球2」不是「流浪地球」。
	r, _ := utf8FirstRune(rest)
	if unicode.IsDigit(r) {
		return false
	}
	// 文字续集同理：中文没有统一的编号习惯，只挡数字挡不住「流浪地球前传」这类词，
	// 会让同 IP 的衍生作品被当成本片转存。
	for _, marker := range sequelMarkers {
		if strings.HasPrefix(rest, marker) {
			return false
		}
	}
	return true
}

func yearsOf(items []item) []int {
	out := make([]int, 0, len(items))
	for _, it := range items {
		if it.year > 0 {
			out = append(out, it.year)
		}
	}
	return out
}

func seasonsOf(items []item) []int {
	out := make([]int, 0, len(items))
	for _, it := range items {
		if it.season > 0 {
			out = append(out, it.season)
		}
	}
	return out
}

func episodesOf(items []item) []int {
	out := make([]int, 0, len(items)*2)
	for _, it := range items {
		if it.episode <= 0 {
			continue
		}
		out = append(out, it.episode)
		if it.endEpisode > it.episode {
			out = append(out, it.endEpisode)
		}
	}
	return out
}

func coversEpisode(items []item, ep int) []int {
	var out []int
	for _, it := range items {
		if it.coversEpisode(ep) {
			out = append(out, it.episode)
		}
	}
	return out
}

// purity 计算主标题占比。同一部作品的不同注入点标题（同一个分享里 S01E01 与
// S01E02 常常带着不同的压制组前缀）会归到不同键上，所以先按包含关系合并分组，
// 否则正常剧集会因为压制组标注不一致被误判成混拼。
//
// 返回 ratio（0~1）、主标题键、以及各组标题键 → 文件数。第二个返回值是给日志用的
// 明细，不是判定依据 —— 判定只认 ratio 与 Options.PurityRatio。
func purity(items []item) (ratio float64, dominant string, groups map[string]int) {
	raw := map[string]int{}
	var keys []string
	for _, it := range items {
		if it.titleKey == "" {
			continue
		}
		if _, ok := raw[it.titleKey]; !ok {
			keys = append(keys, it.titleKey)
		}
		raw[it.titleKey]++
	}
	if len(keys) == 0 {
		return 1, "", map[string]int{}
	}
	merged := mergeContainmentGroups(keys)
	groups = make(map[string]int, len(merged))
	total, best := 0, 0
	for groupKey, members := range merged {
		count := 0
		for _, member := range members {
			count += raw[member]
		}
		groups[groupKey] = count
		total += count
		if count > best {
			best = count
			dominant = groupKey
		}
	}
	if total == 0 {
		return 1, dominant, groups
	}
	return float64(best) / float64(total), dominant, groups
}

// mergeContainmentGroups 把互相包含的标题键并成一组，返回 group 代表键 → 成员键。
func mergeContainmentGroups(keys []string) map[string][]string {
	sorted := append([]string(nil), keys...)
	// 长键在前，保证 "silos01ddp51h264ctrlhdsilo" 作为代表吃掉 "silo"。
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i]) != len(sorted[j]) {
			return len(sorted[i]) > len(sorted[j])
		}
		return sorted[i] < sorted[j]
	})
	groups := make(map[string][]string, len(sorted))
	assigned := make(map[string]bool, len(sorted))
	for _, key := range sorted {
		if assigned[key] {
			continue
		}
		members := []string{key}
		assigned[key] = true
		for _, other := range sorted {
			if assigned[other] {
				continue
			}
			if strings.Contains(key, other) {
				members = append(members, other)
				assigned[other] = true
			}
		}
		groups[key] = members
	}
	return groups
}

func sameTMDBID(a, b string) bool {
	normalize := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "tmdb:")
		s = strings.TrimPrefix(s, "TMDB:")
		return strings.TrimSpace(s)
	}
	return normalize(a) != "" && normalize(a) == normalize(b)
}

func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// endEpisodeRe 匹配 "S01E01-E08" / "E01-E08" / "S01E01~08" 形式里的结束集号。
// rules 只给出起始集，结束集要自己从原串里捞，否则「一集覆盖八集」的合集文件
// 会被当成只覆盖第一集。
var endEpisodeRe = regexp.MustCompile(`(?i)(?:S\d{1,2})?E\d{1,3}\s*[-~]\s*(?:E)?(\d{1,3})`)

func endEpisodeFromText(text string) int {
	m := endEpisodeRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func utf8FirstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func distinctInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func containsInt(values []int, target int) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
