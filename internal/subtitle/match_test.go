package subtitle

import (
	"testing"
)

func TestDefaultScoreWeights(t *testing.T) {
	w := DefaultScoreWeights()
	total := w.Release + w.Title + w.Language + w.Year + w.SeasonEpisode + w.Format + w.Popularity
	if total != 100 {
		t.Fatalf("权重合计 = %v，期望恰好 100", total)
	}
	if !(w.Release > w.Title) {
		t.Errorf("发布组权重(%v) 必须高于标题权重(%v)", w.Release, w.Title)
	}
	if !(w.Release > w.Language) {
		t.Errorf("发布组权重(%v) 必须高于语言权重(%v)", w.Release, w.Language)
	}
}

func containsString(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

func slugsOf(scored []ScoredCandidate) []string {
	out := make([]string, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.Candidate.Slug)
	}
	return out
}

func TestScoreCandidates(t *testing.T) {
	cases := []struct {
		name        string
		candidates  []Candidate
		opts        MatchOptions
		wantTopSlug string
	}{
		{
			name: "hash 命中压倒一切",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "no-hash", Title: "流浪地球.2019.1080p.其它组", Language: "zh-cn", Format: FormatASS},
				{Provider: "assrt", Slug: "hash-hit", Title: "流浪地球 2019", Language: "zh-cn", Format: FormatSRT, HashMatched: true},
			},
			opts: MatchOptions{
				Title:         "流浪地球",
				Year:          2019,
				MediaType:     "movie",
				VideoFileName: "The.Wandering.Earth.2019.1080p.BluRay.x264-GROUP.mkv",
				VideoHash:     "abc123",
			},
			wantTopSlug: "hash-hit",
		},
		{
			name: "同发布组胜出",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "other-group", Title: "Wandering.Earth.2019.1080p.WEB-DL.x264-OTHER", Language: "zh-cn", Format: FormatASS},
				{Provider: "assrt", Slug: "same-group", Title: "Wandering.Earth.2019.1080p.BluRay.x264-GROUP", Language: "zh-cn", Format: FormatASS},
			},
			opts: MatchOptions{
				Title:         "Wandering Earth",
				Year:          2019,
				MediaType:     "movie",
				VideoFileName: "Wandering.Earth.2019.1080p.BluRay.x264-GROUP.mkv",
			},
			wantTopSlug: "same-group",
		},
		{
			name: "集号冲突被淘汰",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "wrong-episode", Title: "Game.of.Thrones.S01E06.1080p", Language: "zh-cn", Format: FormatASS},
				{Provider: "assrt", Slug: "right-episode", Title: "Game.of.Thrones.S01E05.1080p", Language: "zh-cn", Format: FormatASS},
			},
			opts: MatchOptions{
				Title:         "权力的游戏",
				Season:        1,
				Episode:       5,
				MediaType:     "tvshow",
				VideoFileName: "Game.of.Thrones.S01E05.1080p.mkv",
			},
			wantTopSlug: "right-episode",
		},
		{
			name: "语言优先级生效",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "en-sub", Title: "Example Movie 2020", Language: "en", Format: FormatSRT},
				{Provider: "assrt", Slug: "zh-sub", Title: "Example Movie 2020", Language: "zh-cn", Format: FormatSRT},
			},
			opts: MatchOptions{
				Title:            "Example Movie",
				Year:             2020,
				MediaType:        "movie",
				LanguagePriority: []string{"zh-cn", "en"},
			},
			wantTopSlug: "zh-sub",
		},
		{
			name: "格式优先级生效",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "srt-sub", Title: "Example Movie 2020", Language: "zh-cn", Format: FormatSRT},
				{Provider: "assrt", Slug: "ass-sub", Title: "Example Movie 2020", Language: "zh-cn", Format: FormatASS},
			},
			opts: MatchOptions{
				Title:            "Example Movie",
				Year:             2020,
				MediaType:        "movie",
				LanguagePriority: []string{"zh-cn"},
				FormatPriority:   []string{"ass", "srt"},
			},
			wantTopSlug: "ass-sub",
		},
		{
			name: "年份偏差被降权",
			candidates: []Candidate{
				{Provider: "subhd", Slug: "old-dune", Title: "Dune 1984", Language: "en", Format: FormatSRT},
				{Provider: "assrt", Slug: "new-dune", Title: "Dune 2021", Language: "en", Format: FormatSRT},
			},
			opts: MatchOptions{
				Title:     "Dune",
				Year:      2021,
				MediaType: "movie",
			},
			wantTopSlug: "new-dune",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scored := ScoreCandidates(c.candidates, c.opts)
			if len(scored) != len(c.candidates) {
				t.Fatalf("打分结果条数 = %d，期望 %d", len(scored), len(c.candidates))
			}
			if scored[0].Candidate.Slug != c.wantTopSlug {
				t.Errorf("首位 = %q，期望 %q（全部顺序：%v）",
					scored[0].Candidate.Slug, c.wantTopSlug, slugsOf(scored))
			}
			for _, s := range scored {
				if len(s.Reasons) == 0 {
					t.Errorf("候选 %q 没有任何加分理由", s.Candidate.Slug)
				}
				if s.Score < 0 || s.Score > 100 {
					t.Errorf("候选 %q 分数 %d 越界（应在 0..100）", s.Candidate.Slug, s.Score)
				}
			}
		})
	}
}

func TestScoreCandidatesSortStability(t *testing.T) {
	candidates := []Candidate{
		{Provider: "subhd", Slug: "b", Title: "Same.Title.2020", Language: "zh-cn", Format: FormatSRT},
		{Provider: "subhd", Slug: "a", Title: "Same.Title.2020", Language: "zh-cn", Format: FormatSRT},
	}
	opts := MatchOptions{Title: "Same Title", Year: 2020, MediaType: "movie"}

	first := ScoreCandidates(candidates, opts)
	second := ScoreCandidates(candidates, opts)

	if first[0].Candidate.Slug != second[0].Candidate.Slug {
		t.Errorf("两次打分顺序不一致：%v vs %v", slugsOf(first), slugsOf(second))
	}
	if first[0].Candidate.Slug != "a" {
		t.Errorf("同分时应按 slug 升序兜底，首位 = %q，期望 \"a\"", first[0].Candidate.Slug)
	}
}

func TestExtractReleaseTokens(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantGroup string
		wantTag   string
	}{
		{
			name:      "标准发布名",
			input:     "The.Wandering.Earth.2019.1080p.BluRay.x264-GROUP.mkv",
			wantGroup: "group",
			wantTag:   "1080p",
		},
		{
			name:      "HDR 发布名",
			input:     "Dune.2021.2160p.WEB-DL.HDR.x265-NTb.mkv",
			wantGroup: "ntb",
			wantTag:   "2160p",
		},
		{
			name:      "无发布组",
			input:     "Some.Movie.2019.720p.mkv",
			wantGroup: "",
			wantTag:   "720p",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tokens := extractReleaseTokens(c.input)
			if tokens.group != c.wantGroup {
				t.Errorf("group = %q，期望 %q", tokens.group, c.wantGroup)
			}
			if !containsString(tokens.versionTags, c.wantTag) {
				t.Errorf("versionTags = %v，应含 %q", tokens.versionTags, c.wantTag)
			}
		})
	}
}

func TestParseSeasonEpisode(t *testing.T) {
	cases := []struct {
		input       string
		wantSeason  int
		wantEpisode int
	}{
		{"Show.S02E10.1080p.mkv", 2, 10},
		{"show.s1e2.mkv", 1, 2},
		{"某某剧 第二季 第10集.mkv", 2, 10},
		{"Some.Movie.2019.mkv", 0, 0},
	}
	for _, c := range cases {
		season, episode, _ := parseSeasonEpisode(c.input)
		if season != c.wantSeason || episode != c.wantEpisode {
			t.Errorf("parseSeasonEpisode(%q) = (%d,%d)，期望 (%d,%d)",
				c.input, season, episode, c.wantSeason, c.wantEpisode)
		}
	}
}

func TestNormalizeLanguage(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"zh", "zh"},
		{"zh-CN", "zh-cn"},
		{"CHS", "zh-cn"},
		{"cht", "zh-tw"},
		{"简体", "zh-cn"},
		{"繁體", "zh-tw"},
		{"english", "en"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeLanguage(c.input); got != c.want {
			t.Errorf("NormalizeLanguage(%q) = %q，期望 %q", c.input, got, c.want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		input string
		want  SubtitleFormat
	}{
		{"movie.zh-cn.srt", FormatSRT},
		{"movie.chs.ass", FormatASS},
		{"movie.ssa", FormatSSA},
		{"movie.sup", FormatSUP},
		{"movie.sub", FormatSUB},
		{"movie.vtt", FormatVTT},
		{"movie.mkv", ""},
	}
	for _, c := range cases {
		if got := DetectFormat(c.input); got != c.want {
			t.Errorf("DetectFormat(%q) = %q，期望 %q", c.input, got, c.want)
		}
	}
}

func TestTitleSimilarity(t *testing.T) {
	cases := []struct {
		a, b     string
		min, max float64
	}{
		{"流浪地球", "流浪地球", 1, 1},
		{"The Wandering Earth", "the.wandering.earth", 0.9, 1},
		{"流浪地球", "流浪地球2", 0.6, 0.99},
		{"流浪地球", "Zootopia", 0, 0.35},
	}
	for _, c := range cases {
		got := titleSimilarity(c.a, c.b)
		if got < c.min || got > c.max {
			t.Errorf("titleSimilarity(%q,%q) = %v，期望落在 [%v,%v]", c.a, c.b, got, c.min, c.max)
		}
	}
}
