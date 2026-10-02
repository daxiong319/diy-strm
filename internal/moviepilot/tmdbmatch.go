package moviepilot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"litepan/internal/mediaorganize/tmdb"
)

// TMDB 多候选评分匹配常量（标题与年份双主导，年份档位先决）。
//
// 匹配器刻意不把年份作为硬过滤条件：MoviePilot 侧给出的年份常是「连载年」
// （如年番的当前年份），而 TMDB 记录的是首播年，硬过滤会把唯一正确的候选搜空。
const (
	titleScoreExact    = 100 // 归一化后完全相等
	titleScoreContains = 90  // 互为包含
	titleScoreSubseq   = 80  // 有序子序列命中（「遮天」⊂「遮 天」）
	titleAcceptMin     = 60  // 标题可接受阈值（低于直接丢弃候选）

	yearBucketExact   = 30 // 年份精确命中（季年份画像/首播年/发行年）
	yearBucketNear    = 15 // 相近（±1 年）
	yearBucketUnknown = 10 // 候选无年份信息（不扣太多分）
	yearBucketMiss    = 0  // 明确不符（年份先决拒绝，除非标题满分）

	// matchProbeMax TV 候选拉季画像的最大个数（控制 TMDB 请求量）。
	matchProbeMax = 3
)

// tmdbCandidate TMDB 搜索候选。
type tmdbCandidate struct {
	// ID TMDB 作品 ID。
	ID int64
	// Name 结果主名（已本地化）。
	Name string
	// OrigName 原始名。
	OrigName string
	// Year 电影为发行年，剧集为首播年。
	Year int
}

// tmdbSearchResult TMDB 搜索结果的最小投影（新客户端返回原始 JSON，需自行解码）。
type tmdbSearchResult struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	FirstAirDate  string `json:"first_air_date"`
}

// tmdbSearchResponse search/movie 与 search/tv 共用的响应外壳。
type tmdbSearchResponse struct {
	Results []tmdbSearchResult `json:"results"`
}

// searchResultToCandidate 把搜索结果投影为候选：剧集取 first_air_date，电影取 release_date。
func searchResultToCandidate(r tmdbSearchResult, isTV bool) tmdbCandidate {
	if isTV {
		return tmdbCandidate{ID: r.ID, Name: r.Name, OrigName: r.OriginalName, Year: parseYearFrom(r.FirstAirDate)}
	}
	return tmdbCandidate{ID: r.ID, Name: r.Title, OrigName: r.OriginalTitle, Year: parseYearFrom(r.ReleaseDate)}
}

// decodeSearchResults 解码搜索结果原始 JSON。
func decodeSearchResults(raw []json.RawMessage, isTV bool) []tmdbCandidate {
	cands := make([]tmdbCandidate, 0, len(raw))
	for _, item := range raw {
		var r tmdbSearchResult
		if err := json.Unmarshal(item, &r); err != nil || r.ID <= 0 {
			continue
		}
		cands = append(cands, searchResultToCandidate(r, isTV))
	}
	return cands
}

// normalizeForMatch 匹配归一化：去空白与分隔符（空格、点、连字符、下划线、间隔号），统一小写。
func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '.', '-', '_', '·', '　':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isOrderedSubsequence 判断 sub 是否为 s 的有序子序列（逐字符保序出现）。
func isOrderedSubsequence(sub, s string) bool {
	rs := []rune(sub)
	i := 0
	for _, r := range s {
		if i < len(rs) && r == rs[i] {
			i++
		}
	}
	return i == len(rs)
}

// titleMatchScore 标题匹配分（query 与候选均归一化后比较）。
func titleMatchScore(query, cand string) int {
	q, c := normalizeForMatch(query), normalizeForMatch(cand)
	if q == "" || c == "" {
		return 0
	}
	if q == c {
		return titleScoreExact
	}
	if strings.Contains(c, q) || strings.Contains(q, c) {
		return titleScoreContains
	}
	if isOrderedSubsequence(q, c) || isOrderedSubsequence(c, q) {
		return titleScoreSubseq
	}
	return int(lcsRatio(q, c) * 100)
}

// lcsRatio 最长公共子序列长度 / 较短串长度（0~1）。
func lcsRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(ra)+1)
	cur := make([]int, len(ra)+1)
	for _, ch := range rb {
		for i := 1; i <= len(ra); i++ {
			if ch == ra[i-1] {
				cur[i] = prev[i-1] + 1
			} else if cur[i-1] > prev[i] {
				cur[i] = cur[i-1]
			} else {
				cur[i] = prev[i]
			}
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(ra)]) / float64(len(ra))
}

// parseYearFrom TMDB 日期字符串（2006-01-02）转年份，非法返回 0。
func parseYearFrom(date string) int {
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil {
			return y
		}
	}
	return 0
}

// tmdbTVDetail 剧集详情的最小投影（仅取匹配所需的名称/首播年/季列表）。
type tmdbTVDetail struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	FirstAirDate string `json:"first_air_date"`
	Seasons      []struct {
		SeasonNumber int    `json:"season_number"`
		AirDate      string `json:"air_date"`
	} `json:"seasons"`
}

// tmdbMovieDetail 电影详情的最小投影。
type tmdbMovieDetail struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
}

// tvSeasonYearProfile 剧集候选的季年份画像：每季首播年 + 首播年。
// hasProfile=false 表示 TMDB 未提供 seasons（此时只能退回首播年判断）。
func tvSeasonYearProfile(ctx context.Context, client *tmdb.Client, tvID int64, language string) (seasonYears []int, firstAir int, hasProfile bool) {
	raw, err := client.Lookup(ctx, strconv.FormatInt(tvID, 10), "tv")
	if err != nil || len(raw) == 0 {
		return nil, 0, false
	}
	var detail tmdbTVDetail
	if err := json.Unmarshal(raw, &detail); err != nil || detail.ID <= 0 {
		return nil, 0, false
	}
	firstAir = parseYearFrom(detail.FirstAirDate)
	for _, s := range detail.Seasons {
		if s.SeasonNumber <= 0 {
			continue // 跳过特别篇（S0）
		}
		if y := parseYearFrom(s.AirDate); y > 0 {
			seasonYears = append(seasonYears, y)
		}
	}
	return seasonYears, firstAir, len(seasonYears) > 0
}

// yearMatchScore 年份档位评分，返回 (档位分, 是否明确不符)。
func yearMatchScore(wantYear, haveYear int, seasonYears []int, hasProfile bool) (int, bool) {
	if wantYear <= 0 {
		// 无目标年份：纯标题分主导，给中档分
		return yearBucketUnknown, false
	}
	exact := func(y int) bool { return y == wantYear }
	near := func(y int) bool { return y == wantYear-1 || y == wantYear+1 }

	if hasProfile && len(seasonYears) > 0 {
		for _, y := range seasonYears {
			if exact(y) {
				return yearBucketExact, false
			}
		}
		for _, y := range seasonYears {
			if near(y) {
				return yearBucketNear, false
			}
		}
		// 季画像存在但无任何一季命中目标年 → 明确不符
		return yearBucketMiss, true
	}
	if haveYear > 0 {
		if exact(haveYear) {
			return yearBucketExact, false
		}
		if near(haveYear) {
			return yearBucketNear, false
		}
		return yearBucketMiss, true
	}
	return yearBucketUnknown, false
}

// scoredCandidate 带标题分的候选（评分中间态）。
type scoredCandidate struct {
	cand  tmdbCandidate
	score int
}

// sortCandidatesByScore 综合分排序取最高；同分时年份就近者胜、再按 ID 稳定排序。
func sortCandidatesByScore(final []scoredCandidate, wantYear int) {
	sort.SliceStable(final, func(i, j int) bool {
		if final[i].score != final[j].score {
			return final[i].score > final[j].score
		}
		if wantYear > 0 {
			di, dj := yearDistance(final[i].cand.Year, wantYear), yearDistance(final[j].cand.Year, wantYear)
			if di != dj {
				return di < dj
			}
		}
		return final[i].cand.ID < final[j].cand.ID
	})
}

// yearDistance 候选年份与目标年份的距离（候选无年份视为最远）。
func yearDistance(year, want int) int {
	if year <= 0 {
		return 1 << 30
	}
	d := year - want
	if d < 0 {
		d = -d
	}
	return d
}

// matchTmdbCandidates TMDB 多候选评分匹配（核心入口）：
//  1. 剧集不带年搜索（年份在评分阶段处理）；电影带年与不带年合并去重
//  2. 标题分达阈值的候选（剧集限前 N 个）拉季年份画像做年份精确校验
//  3. 综合分排序取最高；无达标候选返回错误
func matchTmdbCandidates(ctx context.Context, client *tmdb.Client, title string, wantYear int, mediaType string) (*tmdbCandidate, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("标题为空")
	}
	if client == nil {
		return nil, fmt.Errorf("TMDB 客户端未配置")
	}
	isTV := mediaType == "tv"

	cands := make([]tmdbCandidate, 0, 8)
	if isTV {
		// 剧集：不带年搜索（年份交由评分阶段处理）
		raw, err := client.Search(ctx, title, nil, "tv")
		if err != nil {
			return nil, err
		}
		cands = decodeSearchResults(raw, true)
	} else {
		// 电影：带年 + 不带年合并去重（带年提高首位相关度，不带年兜底）
		seen := map[int64]bool{}
		years := []*int{nil}
		if wantYear > 0 {
			y := wantYear
			years = []*int{&y, nil}
		}
		for _, y := range years {
			raw, err := client.Search(ctx, title, y, "movie")
			if err != nil {
				continue
			}
			for _, cand := range decodeSearchResults(raw, false) {
				if cand.ID <= 0 || seen[cand.ID] {
					continue
				}
				seen[cand.ID] = true
				cands = append(cands, cand)
			}
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("TMDB 没有数据")
	}

	scored0 := make([]scoredCandidate, 0, len(cands))
	for _, cand := range cands {
		ts := titleMatchScore(title, cand.Name)
		if ts == 0 {
			ts = titleMatchScore(title, cand.OrigName)
		}
		if ts < titleAcceptMin {
			continue
		}
		scored0 = append(scored0, scoredCandidate{cand: cand, score: ts})
	}
	if len(scored0) == 0 {
		return nil, fmt.Errorf("TMDB 候选均未通过标题校验（%s）", title)
	}
	// 按标题分降序（同分按 ID 稳定排序）
	sort.SliceStable(scored0, func(i, j int) bool {
		if scored0[i].score != scored0[j].score {
			return scored0[i].score > scored0[j].score
		}
		return scored0[i].cand.ID < scored0[j].cand.ID
	})

	final := scored0
	if isTV {
		// 剧集：对标题分达标的候选（限前 N 个）拉季年份画像做年份精确校验
		final = make([]scoredCandidate, 0, len(scored0))
		probe := 0
		for _, s := range scored0 {
			if probe >= matchProbeMax || (len(final) >= 1 && final[0].score >= titleScoreExact+yearBucketExact) {
				break
			}
			seasonYears, firstAir, hasProfile := tvSeasonYearProfile(ctx, client, s.cand.ID, "")
			var bucket int
			var mismatch bool
			if hasProfile {
				bucket, mismatch = yearMatchScore(wantYear, firstAir, seasonYears, true)
			} else {
				bucket, mismatch = yearMatchScore(wantYear, s.cand.Year, nil, false)
			}
			if mismatch && s.score < titleScoreExact {
				continue // 年份明确不符且标题非满分 → 拒绝
			}
			s.score += bucket
			final = append(final, s)
			probe++
		}
	}
	if len(final) == 0 {
		return nil, fmt.Errorf("TMDB 候选年份与 %d 均不符", wantYear)
	}
	sortCandidatesByScore(final, wantYear)
	best := final[0].cand
	return &best, nil
}

// lookupTmdbTitleAndYear 多候选匹配后的正式名称与年份。
// 分别补拉详情；失败时回退候选自带信息。
func lookupTmdbTitleAndYear(ctx context.Context, client *tmdb.Client, cand *tmdbCandidate, mediaType string) (string, int) {
	if client == nil || cand == nil {
		return "", 0
	}
	if mediaType == "tv" {
		if raw, err := client.Lookup(ctx, strconv.FormatInt(cand.ID, 10), "tv"); err == nil && len(raw) > 0 {
			var detail tmdbTVDetail
			if err := json.Unmarshal(raw, &detail); err == nil && detail.ID > 0 {
				name := detail.Name
				if name == "" {
					name = detail.OriginalName
				}
				if name != "" {
					return name, parseYearFrom(detail.FirstAirDate)
				}
			}
		}
		return cand.Name, cand.Year
	}
	if raw, err := client.Lookup(ctx, strconv.FormatInt(cand.ID, 10), "movie"); err == nil && len(raw) > 0 {
		var detail tmdbMovieDetail
		if err := json.Unmarshal(raw, &detail); err == nil && detail.ID > 0 {
			name := detail.Title
			if name == "" {
				name = detail.OriginalTitle
			}
			if name != "" {
				return name, parseYearFrom(detail.ReleaseDate)
			}
		}
	}
	return cand.Name, cand.Year
}
