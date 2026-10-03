package subtitle

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ScoreWeights 是各打分维度的权重。
// 合计 100，发布组/hash 权重最高：它是唯一强信号，"标题像但版本不同"必须永远
// 压不过"版本明确匹配"，否则会下到错版本的字幕。
type ScoreWeights struct {
	Release       float64
	Title         float64
	Language      float64
	Year          float64
	SeasonEpisode float64
	Format        float64
	Popularity    float64
}

// DefaultScoreWeights 返回默认权重（合计 100）。
func DefaultScoreWeights() ScoreWeights {
	return ScoreWeights{
		Release:       40,
		Title:         22,
		Language:      14,
		Year:          8,
		SeasonEpisode: 10,
		Format:        4,
		Popularity:    2,
	}
}

// ScoreBreakdown 是打分明细。
type ScoreBreakdown struct {
	Release       float64 `json:"release"`
	Title         float64 `json:"title"`
	Language      float64 `json:"language"`
	Year          float64 `json:"year"`
	SeasonEpisode float64 `json:"season_episode"`
	Format        float64 `json:"format"`
	Popularity    float64 `json:"popularity"`
	Total         float64 `json:"total"`
}

// ScoredCandidate 是打分后的候选。
type ScoredCandidate struct {
	Candidate Candidate      `json:"candidate"`
	Score     int            `json:"score"`
	Breakdown ScoreBreakdown `json:"breakdown"`
	Reasons   []string       `json:"reasons"`
	Penalties []string       `json:"penalties"`
}

// MatchOptions 是匹配输入。
type MatchOptions struct {
	Title            string
	OriginalTitle    string
	Year             int
	Season           int
	Episode          int
	MediaType        string
	VideoFileName    string
	VideoHash        string
	LanguagePriority []string
	FormatPriority   []string
	Weights          ScoreWeights
}

// releaseTokens 是从文件名/发布名里抽出的版本特征。
type releaseTokens struct {
	normalized  string
	group       string
	versionTags []string
}

// versionKeywords 是版本/编码/音轨类关键词。
var versionKeywords = []string{
	"2160p", "1080p", "720p", "480p",
	"bluray", "blu-ray", "bdrip", "brrip", "webrip", "web-dl", "webdl",
	"hdtv", "dvdrip", "remux",
	"x264", "x265", "h264", "h265", "hevc", "avc", "xvid", "divx",
	"aac", "ac3", "dts", "ddp", "truehd", "flac", "atmos",
	"hdr", "sdr", "dv", "10bit", "8bit",
	"proper", "repack", "extended", "remastered", "imax", "uncut",
}

var releaseSeparator = regexp.MustCompile(`[-_. ]+`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// extractReleaseTokens 归一化发布名并抽取组名与版本标签。
func extractReleaseTokens(name string) releaseTokens {
	var out releaseTokens
	base := name
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	lower := strings.ToLower(base)
	// 字母数字保留，-_. 与空格统一成 '-'
	normalized := releaseSeparator.ReplaceAllString(lower, "-")
	normalized = strings.Trim(normalized, "-")
	out.normalized = normalized

	// 组名：最后一个 '-' 之后，长度 <=24、不是版本关键词、不是纯数字。
	if idx := strings.LastIndex(normalized, "-"); idx >= 0 && idx < len(normalized)-1 {
		candidate := normalized[idx+1:]
		if len(candidate) <= 24 && !containsVersionKeyword(candidate) && !isAllDigits(candidate) {
			out.group = candidate
		}
	}

	seen := map[string]bool{}
	for _, kw := range versionKeywords {
		if strings.Contains(normalized, kw) && !seen[kw] {
			seen[kw] = true
			out.versionTags = append(out.versionTags, kw)
		}
	}
	return out
}

func containsVersionKeyword(s string) bool {
	for _, kw := range versionKeywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ScoreCandidates 给全部候选打分并按分数排序。
func ScoreCandidates(candidates []Candidate, opts MatchOptions) []ScoredCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if opts.Weights == (ScoreWeights{}) {
		opts.Weights = DefaultScoreWeights()
	}
	videoTokens := extractReleaseTokens(firstNonEmpty(opts.VideoFileName, opts.Title))
	out := make([]ScoredCandidate, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, scoreOne(c, opts, videoTokens))
	}
	sortScoredCandidates(out)
	return out
}

func scoreOne(c Candidate, opts MatchOptions, videoTokens releaseTokens) ScoredCandidate {
	w := opts.Weights
	var (
		b     ScoreBreakdown
		score ScoredCandidate
	)

	releaseScore, releaseReasons, releasePenalties := scoreRelease(c, videoTokens, opts)
	b.Release = releaseScore * w.Release
	score.Reasons = append(score.Reasons, releaseReasons...)
	score.Penalties = append(score.Penalties, releasePenalties...)

	titleScore := scoreTitle(c, opts)
	b.Title = titleScore * w.Title
	switch {
	case titleScore >= 0.8:
		score.Reasons = append(score.Reasons, "标题高度吻合")
	case titleScore < 0.35:
		score.Penalties = append(score.Penalties, "标题相似度低")
	}

	langScore := scoreLanguage(c.Language, opts.LanguagePriority)
	b.Language = langScore * w.Language
	switch {
	case langScore >= 1:
		score.Reasons = append(score.Reasons,
			"语言 "+displayLanguage(c.Language)+" 处于优先级首位")
	case langScore == 0:
		score.Penalties = append(score.Penalties,
			"语言 "+displayLanguage(c.Language)+" 不在优先级列表内")
	}

	yearScore := scoreYear(c.Title, opts.Year)
	b.Year = yearScore * w.Year
	if opts.Year > 0 {
		switch {
		case yearScore == 0:
			score.Penalties = append(score.Penalties, "标题中的年份与影片不符")
		case yearScore >= 1:
			score.Reasons = append(score.Reasons, "年份 "+strconv.Itoa(opts.Year)+" 一致")
		}
	}

	seScore := scoreSeasonEpisode(c.Title, opts)
	b.SeasonEpisode = seScore * w.SeasonEpisode
	if opts.MediaType == "tvshow" {
		switch {
		case seScore >= 1:
			score.Reasons = append(score.Reasons,
				"季集 S"+pad2(opts.Season)+"E"+pad2(opts.Episode)+" 匹配")
		case seScore == 0:
			score.Penalties = append(score.Penalties, "未在标题中识别到对应季集")
		}
	}

	fmtScore := scoreFormat(c.Format, opts.FormatPriority)
	b.Format = fmtScore * w.Format
	if c.Format == FormatSUP {
		score.Penalties = append(score.Penalties, "图形字幕(sup)不支持时间轴校正")
	}

	b.Popularity = scorePopularity(c) * w.Popularity

	total := b.Release + b.Title + b.Language + b.Year + b.SeasonEpisode + b.Format + b.Popularity
	b.Total = math.Round(total*100) / 100
	// 每个候选至少给一条理由：接口契约要求前端始终能解释"为什么它在列表里"，
	// 哪怕是零分候选也该说明"为什么它垫底"。
	if len(score.Reasons) == 0 {
		score.Reasons = append(score.Reasons, "未识别到可用匹配特征，仅按来源召回")
	}
	score.Candidate = c
	score.Breakdown = b
	score.Score = int(math.Round(total))
	return score
}

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

// scoreRelease 判断版本匹配程度，是权重最高的维度。
func scoreRelease(c Candidate, videoTokens releaseTokens, opts MatchOptions) (float64, []string, []string) {
	if c.HashMatched {
		return 1, []string{"文件 hash 完全匹配"}, nil
	}
	if opts.VideoHash != "" && c.Extra != nil && c.Extra["moviehash"] == opts.VideoHash {
		return 1, []string{"moviehash 匹配"}, nil
	}

	candTokens := extractReleaseTokens(firstNonEmpty(c.FileName, c.Title))
	if strings.TrimSpace(c.ReleaseGroup) != "" {
		candTokens.group = strings.ToLower(strings.TrimSpace(c.ReleaseGroup))
	}

	if candTokens.normalized != "" && candTokens.normalized == videoTokens.normalized {
		return 1, []string{"发布名完全一致"}, nil
	}
	if candTokens.group != "" && candTokens.group == videoTokens.group {
		return 0.9, []string{"发布组一致（" + candTokens.group + "）"}, nil
	}
	if len(candTokens.versionTags) > 0 && len(videoTokens.versionTags) > 0 {
		overlap := countOverlap(candTokens.versionTags, videoTokens.versionTags)
		ratio := float64(overlap) / float64(len(videoTokens.versionTags))
		if ratio >= 0.6 {
			return 0.35 + 0.4*ratio,
				[]string{"版本特征高度重合（" + strings.Join(candTokens.versionTags, "/") + "）"}, nil
		}
	}
	if candTokens.group != "" && videoTokens.group != "" && candTokens.group != videoTokens.group {
		return 0.15, nil, []string{"发布组不同（视频 " + videoTokens.group + " / 字幕 " + candTokens.group + "）"}
	}
	return 0.3, nil, nil
}

// scoreTitle 取候选标题与影片标题/原始标题的最大相似度。
func scoreTitle(c Candidate, opts MatchOptions) float64 {
	candTitle := cleanCandidateTitle(c.Title)
	if candTitle == "" {
		return 0
	}
	best := titleSimilarity(opts.Title, candTitle)
	if alt := titleSimilarity(opts.OriginalTitle, candTitle); alt > best {
		best = alt
	}
	return best
}

// titleSimilarity 计算两个标题的相似度。
//
// 老版依赖 github.com/mozillazg/go-pinyin 做中文转拼音；LitePan 不允许新增第三方
// 依赖，因此这里改用"汉字结构 + 编辑距离"两段式判定：
// 中文与中文比（编辑距离），中文与拉丁文之间直接判低分——因为拼音与英文拼写
// 本来就不该相似，"流浪地球"对 "Zootopia" 必须落进 [0,0.35]。
func titleSimilarity(a, b string) float64 {
	na := normalizeTitleText(a)
	nb := normalizeTitleText(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		shorter, longer := len(na), len(nb)
		if shorter > longer {
			shorter, longer = longer, shorter
		}
		return 0.75 + 0.25*(float64(shorter)/float64(longer))
	}

	aHan := hasHan(na)
	bHan := hasHan(nb)
	if aHan != bHan {
		// 一个中文一个拉丁：不做拼音桥接，直接给低分。
		return levenshteinRatio(na, nb) * 0.3
	}
	if aHan && bHan {
		return levenshteinRatio(na, nb) * 0.9
	}
	return levenshteinRatio(na, nb) * 0.85
}

// normalizeTitleText 只保留字母数字与汉字并小写，去掉常见发布噪声词。
func normalizeTitleText(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	lower = strings.NewReplacer(
		"2160p", " ", "1080p", " ", "720p", " ",
		"bluray", " ", "blu", " ", "webdl", " ", "web", " ",
		"hdrip", " ", "bd", " ",
	).Replace(lower)

	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// cleanCandidateTitle 去掉候选标题尾部的字幕/压缩包扩展名。
func cleanCandidateTitle(title string) string {
	s := strings.TrimSpace(title)
	for _, ext := range []string{".srt", ".ass", ".ssa", ".sub", ".sup", ".vtt", ".zip", ".rar"} {
		if strings.HasSuffix(strings.ToLower(s), ext) {
			s = s[:len(s)-len(ext)]
			break
		}
	}
	return strings.TrimSpace(s)
}

// levenshteinRatio = 1 - 编辑距离/较长串长度。
func levenshteinRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	maxLen := len(ra)
	if len(rb) > maxLen {
		maxLen = len(rb)
	}
	if maxLen == 0 {
		return 1
	}
	return 1 - float64(levenshtein(ra, rb))/float64(maxLen)
}

// levenshtein 用滚动数组算编辑距离。
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// countOverlap 统计两个切片去重后的交集大小。
func countOverlap(a, b []string) int {
	set := make(map[string]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	seen := map[string]bool{}
	count := 0
	for _, v := range b {
		if set[v] && !seen[v] {
			seen[v] = true
			count++
		}
	}
	return count
}

// scoreLanguage 按优先级列表给语言打分。
// 未知语言给 0.5 中性分，避免网页源被直接压死。
func scoreLanguage(lang string, priority []string) float64 {
	normalized := NormalizeLanguage(lang)
	if normalized == "" {
		return 0.5
	}
	if len(priority) == 0 {
		return 0.7
	}
	for i, want := range priority {
		w := NormalizeLanguage(want)
		if w == "" {
			continue
		}
		if w == normalized {
			return math.Max(0.4, 1-float64(i)*0.15)
		}
		// zh 与 zh-cn/zh-tw 互相认（站点常只标"中文"）。
		if isZhFamily(w) && isZhFamily(normalized) {
			return math.Max(0.4, 0.85-float64(i)*0.15)
		}
	}
	return 0
}

func isZhFamily(lang string) bool {
	return lang == "zh" || lang == "zh-cn" || lang == "zh-tw"
}

var yearPattern = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)

// scoreYear 按年份吻合度打分；无年份信息给中性分。
func scoreYear(candidateTitle string, wantYear int) float64 {
	if wantYear <= 0 {
		return 1
	}
	years := extractYears(candidateTitle)
	if len(years) == 0 {
		return 0.7
	}
	for _, y := range years {
		if y == wantYear {
			return 1
		}
		if y == wantYear-1 || y == wantYear+1 {
			return 0.6
		}
	}
	return 0
}

func extractYears(text string) []int {
	var out []int
	for _, m := range yearPattern.FindAllStringSubmatch(text, -1) {
		if v, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, v)
		}
	}
	return out
}

var (
	seasonEpisodePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`),
		regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`),
		regexp.MustCompile(`(?i)\bE(\d{1,3})\b`),
	}
	// 中文季集可能是汉字数字（"第二季""第10集"），\d 匹配不到"十"，
	// 因此字符类同时收数字与汉字。
	chineseSeasonPattern  = regexp.MustCompile(`第\s*([0-9一二三四五六七八九十]{1,3})\s*季`)
	chineseEpisodePattern = regexp.MustCompile(`第\s*([0-9一二三四五六七八九十]{1,3})\s*集`)
)

// scoreSeasonEpisode 按季集吻合度打分。
// 集号冲突直接给 0（硬冲突）：下错一集比不下更糟。
func scoreSeasonEpisode(candidateTitle string, opts MatchOptions) float64 {
	if opts.MediaType != "tvshow" || opts.Episode <= 0 {
		return 1
	}
	season, episode, found := parseSeasonEpisode(candidateTitle)
	if !found {
		return 0.7
	}
	if episode > 0 && episode != opts.Episode {
		return 0
	}
	if season > 0 && opts.Season > 0 && season != opts.Season {
		return 0
	}
	return 1
}

// parseSeasonEpisode 从文本里解析季集号。
func parseSeasonEpisode(title string) (season, episode int, found bool) {
	if m := seasonEpisodePatterns[0].FindStringSubmatch(title); len(m) == 3 {
		return atoiSafe(m[1]), atoiSafe(m[2]), true
	}
	if m := seasonEpisodePatterns[1].FindStringSubmatch(title); len(m) == 3 {
		return atoiSafe(m[1]), atoiSafe(m[2]), true
	}

	seasonFound := false
	if m := chineseSeasonPattern.FindStringSubmatch(title); len(m) == 2 {
		if v, ok := parseChineseNumber(m[1]); ok {
			season = v
			seasonFound = true
		}
	}
	if m := chineseEpisodePattern.FindStringSubmatch(title); len(m) == 2 {
		if v, ok := parseChineseNumber(m[1]); ok {
			return season, v, true
		}
	}
	if seasonFound {
		return season, 0, true
	}
	if m := seasonEpisodePatterns[2].FindStringSubmatch(title); len(m) == 2 {
		return 0, atoiSafe(m[1]), true
	}
	return 0, 0, false
}

// parseChineseNumber 解析 一~九十 这类中文数字（够用于季集号）。
func parseChineseNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	// 阿拉伯数字直接走 Atoi（"第10集" 这类混写）。
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v, true
	}
	digits := map[rune]int{
		'一': 1, '二': 2, '三': 3, '四': 4, '五': 5,
		'六': 6, '七': 7, '八': 8, '九': 9,
	}
	runes := []rune(s)
	// 十、十N、N十、N十M
	if len(runes) == 1 && runes[0] == '十' {
		return 10, true
	}
	if len(runes) == 2 {
		if runes[0] == '十' {
			if d, ok := digits[runes[1]]; ok {
				return 10 + d, true
			}
		}
		if runes[1] == '十' {
			if d, ok := digits[runes[0]]; ok {
				return d * 10, true
			}
		}
	}
	if len(runes) == 3 && runes[1] == '十' {
		tens, ok1 := digits[runes[0]]
		ones, ok2 := digits[runes[2]]
		if ok1 && ok2 {
			return tens*10 + ones, true
		}
	}
	if len(runes) == 1 {
		if d, ok := digits[runes[0]]; ok {
			return d, true
		}
	}
	return 0, false
}

// scoreFormat 按格式优先级打分；列表为空时用内置偏好。
func scoreFormat(format SubtitleFormat, priority []string) float64 {
	if format == "" {
		return 0.5
	}
	if len(priority) == 0 {
		switch format {
		case FormatASS:
			return 1
		case FormatSRT:
			return 0.9
		case FormatSSA:
			return 0.8
		case FormatVTT:
			return 0.7
		case FormatSUB:
			return 0.5
		case FormatSUP:
			return 0.2
		}
		return 0.5
	}
	for i, want := range priority {
		if SubtitleFormat(strings.ToLower(strings.TrimSpace(want))) == format {
			return math.Max(0.2, 1-float64(i)*0.15)
		}
	}
	return 0.2
}

// scorePopularity 把评分与下载量合成热度分。
func scorePopularity(c Candidate) float64 {
	rating := math.Min(c.Rating/10, 1) * 0.5
	downloads := 0.0
	if c.DownloadCount > 0 {
		downloads = math.Min(math.Log10(float64(c.DownloadCount))/4, 1)
	}
	return rating + downloads*0.5
}

// displayLanguage 把内部语言值翻成可读名称。
func displayLanguage(lang string) string {
	switch NormalizeLanguage(lang) {
	case "zh-cn":
		return "简体中文"
	case "zh-tw":
		return "繁体中文"
	case "zh":
		return "中文"
	case "en":
		return "英文"
	case "ja":
		return "日文"
	case "ko":
		return "韩文"
	default:
		return "未知"
	}
}

// sortScoredCandidates 稳定排序：分数降序 → hash 命中 → 下载量 → 评分 → slug 升序。
func sortScoredCandidates(items []ScoredCandidate) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Candidate.HashMatched != b.Candidate.HashMatched {
			return a.Candidate.HashMatched
		}
		if a.Candidate.DownloadCount != b.Candidate.DownloadCount {
			return a.Candidate.DownloadCount > b.Candidate.DownloadCount
		}
		if a.Candidate.Rating != b.Candidate.Rating {
			return a.Candidate.Rating > b.Candidate.Rating
		}
		return a.Candidate.Slug < b.Candidate.Slug
	})
}

// atoiSafe 宽松解析整数；明显不合理的值（>1000000）返回 0。
func atoiSafe(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || v > 1_000_000 {
		return 0
	}
	return v
}
