// Package moviepilot 的 TMDB 多候选评分匹配器（借鉴 tgto123 TmdbScraper 设计）：
// 搜索不传年份（避免 first_air_date_year 过滤搜空）→ 逐候选「标题分 + 年份档」双主导
// 评分（年份档位先决）→ TV 候选用 season_years 每季年份画像精确校验。
// 替代 CheckByNameAndYear 的「多结果即拒绝 + 带年过滤」行为。
package moviepilot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/tmdb"
)

// 评分常量（借鉴 tgto123：标题与年份双主导，年份档位先决）
const (
	titleScoreExact    = 100 // 归一化后完全相等
	titleScoreContains = 90  // 互为包含
	titleScoreSubseq   = 80  // 有序子序列命中（「遮天」⊂「遮 天」）
	titleAcceptMin     = 60  // 标题可接受阈值（低于直接丢弃候选）

	yearBucketExact   = 30 // 年份精确命中（季年份画像/首播年/发行年）
	yearBucketNear    = 15 // 相近（±1 年）
	yearBucketUnknown = 10 // 候选无年份信息（不扣太多分）
	yearBucketMiss    = 0  // 明确不符（年份先决拒绝，除非标题满分）

	tvSeasonYearsProbeMax = 3 // TV 候选拉季画像的最大个数（控制 TMDB 请求量）
)

// tmdbCandidate TMDB 搜索候选
type tmdbCandidate struct {
	ID       int64
	Name     string // 结果主名（已本地化）
	OrigName string // 原始名
	Year     int    // 电影：发行年；TV：首播年
}

// normalizeForMatch 匹配归一化：去空白/分隔符（空格 点 连字符 下划线），统一小写
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

// isOrderedSubsequence 判断 sub 是否为 s 的有序子序列（逐字符保序出现）
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

// titleMatchScore 标题匹配分（query 与候选均归一化后比较）
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
	if isOrderedSubsequence(q, c) {
		return titleScoreSubseq
	}
	return int(lcsRatio(q, c) * 100)
}

// lcsRatio 最长公共子序列长度 / 较短串长度（0~1）
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

// parseYearFrom TMDB 日期字符串（2006-01-02）转年份
func parseYearFrom(date string) int {
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil {
			return y
		}
	}
	return 0
}

// tvSeasonYearProfile TV 候选的季年份画像：season_years（每季首播年）+ 首播年
// 返回 (seasonYears, firstAirYear, hasProfile)；TMDB 未提供 seasons 时 hasProfile=false
func tvSeasonYearProfile(ctx context.Context, client *tmdb.Client, tvID int64, language string) (seasonYears []int, firstAir int, hasProfile bool) {
	detail, err := client.GetTvDetail(tvID, language)
	if err != nil || detail == nil || detail.SearchTv.ID <= 0 {
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

// yearMatchScore 年份档位评分（tvSeasonYears 仅 TV 提供时参与精确判断）
// 返回 (档位分, 明确不符)
func yearMatchScore(wantYear int, haveYear int, seasonYears []int, hasProfile bool) (int, bool) {
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

// matchTmdbCandidates TMDB 多候选评分匹配（核心入口）：
//  1. TV 不带年搜索（movie 带年与不带年合并去重）
//  2. 标题分 ≥ 阈值的候选拉 TV 季画像做年份精确校验（限前 N 个）
//  3. 综合分排序取最高；无达标候选返回错误
//
// 返回 (最佳候选, 错误)
func matchTmdbCandidates(ctx context.Context, title string, wantYear int, mediaType string) (*tmdbCandidate, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("标题为空")
	}
	client := models.GlobalScrapeSettings.GetTmdbClient()
	if client == nil {
		return nil, fmt.Errorf("TMDB 客户端未配置")
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	isTV := mediaType == "tv"

	cands := make([]tmdbCandidate, 0, 8)
	if isTV {
		// TV：不带年搜索（tgto123 策略：年份在评分阶段处理）
		resp, err := client.SearchTv(title, 0, language, true)
		if err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			cands = append(cands, tmdbCandidate{
				ID:       r.ID,
				Name:     r.Name,
				OrigName: r.OriginalName,
				Year:     parseYearFrom(r.FirstAirDate),
			})
		}
	} else {
		// 电影：带年 + 不带年合并去重（带年提高首位相关度，不带年兜底）
		seen := map[int64]bool{}
		for _, y := range []int{wantYear, 0} {
			resp, err := client.SearchMovie(title, y, language, true, false)
			if err != nil {
				continue
			}
			for _, r := range resp.Results {
				if !seen[r.ID] {
					seen[r.ID] = true
					cands = append(cands, tmdbCandidate{
						ID:       r.ID,
						Name:     r.Title,
						OrigName: r.OriginalTitle,
						Year:     parseYearFrom(r.ReleaseDate),
					})
				}
			}
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("TMDB 没有数据")
	}

	type scored struct {
		cand  tmdbCandidate
		score int
	}
	scored0 := make([]scored, 0, len(cands))
	for _, cand := range cands {
		ts := titleMatchScore(title, cand.Name)
		if ts == 0 {
			ts = titleMatchScore(title, cand.OrigName)
		}
		if ts < titleAcceptMin {
			continue
		}
		scored0 = append(scored0, scored{cand: cand, score: ts})
	}
	// 别名兜底（借鉴 tgto123 _fetch_candidate_alias_titles）：
	// 全部候选标题未达标时，拉前几个候选的 TMDB 别名（alternative titles）
	// 再做标题匹配（别名常含更贴合本地化/罗马音的写法），命中则纳入评分
	if len(scored0) == 0 {
		probe := 0
		for _, cand := range cands {
			if probe >= tvSeasonYearsProbeMax {
				break
			}
			probe++
			var altNames []string
			var aErr error
			if isTV {
				altNames, aErr = client.GetTvAlternativeTitles(cand.ID)
			} else {
				altNames, aErr = client.GetMovieAlternativeTitles(cand.ID)
			}
			if aErr != nil || len(altNames) == 0 {
				continue
			}
			for _, alt := range altNames {
				ts := titleMatchScore(title, alt)
				if ts < titleAcceptMin {
					continue
				}
				scored0 = append(scored0, scored{cand: cand, score: ts})
				helpers.AppLogger.Infof("TMDB 别名命中：%s ← %s（候选 %s）", title, alt, cand.Name)
				break
			}
			if len(scored0) > 0 {
				break
			}
		}
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

	var final []scored
	if isTV {
		// TV：对标题分达标的候选（限前 N 个）拉季年份画像做年份精确校验
		probe := 0
		for _, s := range scored0 {
			if probe >= tvSeasonYearsProbeMax || len(final) >= 1 && final[0].score >= titleScoreExact+yearBucketExact {
				break
			}
			seasonYears, firstAir, hasProfile := tvSeasonYearProfile(ctx, client, s.cand.ID, language)
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
	} else {
		final = scored0
	}
	if len(final) == 0 {
		return nil, fmt.Errorf("TMDB 候选年份与 %d 均不符", wantYear)
	}
	// 综合分排序取最高
	sort.SliceStable(final, func(i, j int) bool {
		if final[i].score != final[j].score {
			return final[i].score > final[j].score
		}
		return final[i].cand.ID < final[j].cand.ID
	})
	best := final[0].cand
	helpers.AppLogger.Infof("TMDB 多候选匹配：%s (%d) → %s (%d)（综合分 %d / %d 候选）",
		title, wantYear, best.Name, best.ID, final[0].score, len(scored0))
	return &best, nil
}

// lookupTmdbTitleAndYear 多候选匹配后的正式名称与年份（供调用方校验归一化）。
// 电影/剧集分别补拉详情；失败时回退候选自带信息。
func lookupTmdbTitleAndYear(ctx context.Context, cand *tmdbCandidate, mediaType string, language string) (string, int) {
	client := models.GlobalScrapeSettings.GetTmdbClient()
	if client == nil {
		return cand.Name, cand.Year
	}
	if mediaType == "tv" {
		if detail, err := client.GetTvDetail(cand.ID, language); err == nil && detail != nil && detail.SearchTv.ID > 0 {
			name := detail.SearchTv.Name
			if name == "" {
				name = detail.SearchTv.OriginalName
			}
			return name, parseYearFrom(detail.FirstAirDate)
		}
	} else if detail, err := client.GetMovieDetail(cand.ID, language); err == nil && detail != nil && detail.SearchMovie.ID > 0 {
		name := detail.SearchMovie.Title
		if name == "" {
			name = detail.SearchMovie.OriginalTitle
		}
		return name, parseYearFrom(detail.SearchMovie.ReleaseDate)
	}
	return cand.Name, cand.Year
}
