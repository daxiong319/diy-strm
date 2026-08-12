package moviepilot

import (
	"testing"

	"diy-strm/internal/tmdb"
)

func mustParseRules(t *testing.T) categoryRules {
	t.Helper()
	rules := parseCategoryRules("")
	if len(rules.Movie) == 0 || len(rules.Tv) == 0 {
		t.Fatalf("默认分类配置解析为空：movie=%d tv=%d", len(rules.Movie), len(rules.Tv))
	}
	return rules
}

func TestParseCategoryRulesOrderAndCatchAll(t *testing.T) {
	rules := mustParseRules(t)
	// movie：纪录片/演唱会/动画电影/华语电影/日韩电影/欧美电影
	if len(rules.Movie) != 6 {
		t.Fatalf("movie 规则数=%d，期望 6", len(rules.Movie))
	}
	last := rules.Movie[len(rules.Movie)-1]
	if last.Name != "欧美电影" || !last.CatchAll {
		t.Fatalf("movie 兜底应为欧美电影：name=%s catchAll=%v", last.Name, last.CatchAll)
	}
	if rules.Movie[0].Name != "纪录片" {
		t.Fatalf("movie 顺序错误，首个=%s", rules.Movie[0].Name)
	}
	if len(rules.Movie[0].IncludeGenres) != 1 || rules.Movie[0].IncludeGenres[0] != 99 {
		t.Fatalf("纪录片 includeGenres=%v", rules.Movie[0].IncludeGenres)
	}
	if len(rules.Movie[0].ExcludeGenres) != 1 || rules.Movie[0].ExcludeGenres[0] != 10402 {
		t.Fatalf("纪录片 excludeGenres=%v", rules.Movie[0].ExcludeGenres)
	}
	// tv：11 项，兜底未分类
	if len(rules.Tv) != 11 {
		t.Fatalf("tv 规则数=%d，期望 11", len(rules.Tv))
	}
	tvLast := rules.Tv[len(rules.Tv)-1]
	if tvLast.Name != "未分类" || !tvLast.CatchAll {
		t.Fatalf("tv 兜底应为未分类：name=%s catchAll=%v", tvLast.Name, tvLast.CatchAll)
	}
	kr := rules.Tv[8] // 日韩剧集
	if kr.Name != "日韩剧集" {
		t.Fatalf("第 9 项应为日韩剧集：%s", kr.Name)
	}
	if len(kr.Countries) != 5 || kr.Countries[1] != "KP" {
		t.Fatalf("日韩剧集 countries=%v", kr.Countries)
	}
}

func movieDetail(genres []int, lang string, countries []string) *tmdb.MovieDetail {
	d := &tmdb.MovieDetail{}
	for _, g := range genres {
		d.Genres = append(d.Genres, tmdb.Genre{ID: g})
	}
	d.OriginalLanguage = lang
	for _, c := range countries {
		d.ProductionCountries = append(d.ProductionCountries, tmdb.Country{ISO_3166_1: c})
	}
	return d
}

func tvDetail(genres []int, lang string, countries []string) *tmdb.TvDetail {
	d := &tmdb.TvDetail{}
	for _, g := range genres {
		d.Genres = append(d.Genres, tmdb.Genre{ID: g})
	}
	d.OriginalLanguage = lang
	d.OriginCountry = countries
	return d
}

func TestMatchMovieCategory(t *testing.T) {
	rules := mustParseRules(t)
	cases := []struct {
		name string
		d    *tmdb.MovieDetail
		want string
	}{
		{"华语剧情片", movieDetail([]int{18}, "zh", []string{"CN"}), "华语电影"},
		{"日韩恐怖片", movieDetail([]int{27}, "ja", []string{"JP"}), "日韩电影"},
		{"纪录片含音乐排除", movieDetail([]int{99, 18}, "en", []string{"US"}), "纪录片"},
		{"演唱会音乐片", movieDetail([]int{10402}, "en", []string{"US"}), "演唱会"},
		{"动画电影", movieDetail([]int{16, 12}, "en", []string{"US"}), "动画电影"},
		{"纪录片且音乐", movieDetail([]int{99, 10402}, "en", []string{"US"}), "演唱会"},
		{"欧美兜底", movieDetail([]int{28, 12}, "en", []string{"US"}), "欧美电影"},
	}
	for _, c := range cases {
		if got := rules.matchMovie(c.d); got != c.want {
			t.Errorf("%s：got %s want %s", c.name, got, c.want)
		}
	}
}

func TestMatchTvCategory(t *testing.T) {
	rules := mustParseRules(t)
	cases := []struct {
		name string
		d    *tmdb.TvDetail
		want string
	}{
		{"国产剧集", tvDetail([]int{18}, "zh", []string{"CN"}), "国产剧集"},
		{"日番动漫", tvDetail([]int{16}, "ja", []string{"JP"}), "日番动漫"},
		{"国产动漫", tvDetail([]int{16}, "zh", []string{"CN"}), "国产动漫"},
		{"韩剧", tvDetail([]int{18}, "ko", []string{"KR"}), "日韩剧集"},
		{"泰剧", tvDetail([]int{18}, "th", []string{"TH"}), "日韩剧集"},
		{"美剧", tvDetail([]int{18, 10765}, "en", []string{"US"}), "欧美剧集"},
		{"综艺", tvDetail([]int{10764}, "zh", []string{"CN"}), "综艺节目"},
		{"纪录片", tvDetail([]int{99}, "en", []string{"GB"}), "纪录片"},
		{"其他动漫", tvDetail([]int{16}, "es", []string{"ES"}), "欧美动漫"},
		{"未分类兜底", tvDetail([]int{18}, "hi", []string{"IN"}), "日韩剧集"},
		{"纯兜底", tvDetail([]int{10770}, "ar", []string{"SA"}), "未分类"},
	}
	for _, c := range cases {
		if got := rules.matchTv(c.d); got != c.want {
			t.Errorf("%s：got %s want %s", c.name, got, c.want)
		}
	}
}

func TestOrganizeRootPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"影视/待整理", "影视/已整理"},
		{"待整理", "已整理"},
		{"/影视/待整理", "/影视/已整理"},
	}
	for _, c := range cases {
		if got := organizeRootPath(c.in); got != c.want {
			t.Errorf("organizeRootPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestBuildOrganizeRelDir(t *testing.T) {
	relDir, ok := buildOrganizeRelDir("tv", "我的荒糖恋爱", 2026, 1, 291496, "日韩剧集")
	if !ok || relDir != "日韩剧集/我的荒糖恋爱 (2026) {tmdb=291496}/Season 01" {
		t.Fatalf("tv relDir=%q ok=%v", relDir, ok)
	}
	relDir, ok = buildOrganizeRelDir("movie", "星河入梦", 2026, 0, 1359005, "华语电影")
	if !ok || relDir != "华语电影/星河入梦 (2026) {tmdb=1359005}" {
		t.Fatalf("movie relDir=%q ok=%v", relDir, ok)
	}
	if _, ok = buildOrganizeRelDir("tv", "", 2026, 1, 0, "未分类"); ok {
		t.Fatal("空标题/空 tmdb 应返回 false")
	}
}

func TestBuildOrganizeNewName(t *testing.T) {
	if got := buildOrganizeNewName("tv", "我的荒糖恋爱", 1, 11, 2026, ".mkv"); got != "我的荒糖恋爱.2026.S01E11.第11集.mkv" {
		t.Fatalf("tv name=%s", got)
	}
	if got := buildOrganizeNewName("movie", "星河入梦", 0, 0, 2026, ".mkv"); got != "星河入梦 (2026).mkv" {
		t.Fatalf("movie name=%s", got)
	}
}

func TestParseCategoryConfigBadYAML(t *testing.T) {
	rules := parseCategoryRules("not: [valid")
	if len(rules.Movie) == 0 || len(rules.Tv) == 0 {
		t.Fatal("坏配置应回退默认配置")
	}
}
