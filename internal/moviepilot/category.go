package moviepilot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/scrape"
	"diy-strm/internal/tmdb"
)

// DefaultCategoryConfigYAML 默认分类策略配置（MoviePilot category.yaml 风格）。
// 配置为空或解析失败时使用该默认值；「已整理」目录名即分类名。
const DefaultCategoryConfigYAML = `# 配置电影的分类策略， 配置为空或者不配置该项则不启用电影分类
movie:
  # 纪录片
  纪录片:
    genre_ids: "99,-10402"
  # 演唱会
  演唱会:
    genre_ids: "10402"
  # 动画电影
  动画电影:
    genre_ids: "16"
  # 华语电影
  华语电影:
    original_language: "zh,cn,bo,za"
  # 日韩电影
  日韩电影:
    original_language: "ja,ko,th"
  # 未配置任何过滤条件时，则按先后顺序不符合上面分类的都会在这个分类下，建议配置在最末尾
  欧美电影:

# 配置电视剧的分类策略， 配置为空或者不配置该项则不启用电视剧分类
tv:
  # 国产动漫
  国产动漫:
    genre_ids: "16"
    origin_country: "CN,TW,HK"
  # 日番动漫
  日番动漫:
    genre_ids: "16"
    origin_country: "JP"
  # 欧美动漫
  欧美动漫:
    genre_ids: "16"
    origin_country: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK"
  # 儿童动漫
  儿童动漫:
    genre_ids: "10762"
  # 其他动漫
  其他动漫:
    genre_ids: "16"
  # 纪录片
  纪录片:
    genre_ids: "99"
  # 综艺节目
  综艺节目:
    genre_ids: "10764,10767"
  # 国产剧集
  国产剧集:
    origin_country: "CN,TW,HK,SG"
  # 日韩剧集
  日韩剧集:
    origin_country: "JP,KP,KR,TH,IN"
  # 欧美剧集
  欧美剧集:
    origin_country: "US,FR,GB,DE,ES,IT,NL,PT,RU,UK,CO"
  # 未分类：未配置任何过滤条件，按先后顺序不符合上面分类的都会在这个分类下
  未分类:
`

// categoryRule 单条分类规则（分类名即网盘目录名）
type categoryRule struct {
	Name          string   // 分类名
	IncludeGenres []int    // 必须包含的 genre_id（正数）
	ExcludeGenres []int    // 必须排除的 genre_id（负数）
	Languages     []string // 匹配 original_language
	Countries     []string // 匹配 origin_country（剧集）
	CatchAll      bool     // 无任何过滤条件（兜底分类，应配置在末尾）
}

type categoryRules struct {
	Movie []categoryRule
	Tv    []categoryRule
}

// parseCategoryRules 解析 MP 风格分类 YAML（保持配置顺序，无条件项为兜底）。
// 配置为空或解析失败时回退默认配置。
func parseCategoryRules(text string) categoryRules {
	if strings.TrimSpace(text) == "" {
		text = DefaultCategoryConfigYAML
	}
	rules := categoryRules{}
	var root yaml.MapSlice
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		helpers.AppLogger.Warnf("MoviePilot 分类策略配置解析失败，使用默认配置：%v", err)
		if err2 := yaml.Unmarshal([]byte(DefaultCategoryConfigYAML), &root); err2 != nil {
			return rules
		}
	}
	for _, kv := range root {
		key, _ := kv.Key.(string)
		if key != "movie" && key != "tv" {
			continue
		}
		items, ok := kv.Value.(yaml.MapSlice)
		if !ok {
			continue
		}
		for _, item := range items {
			name, _ := item.Key.(string)
			rule := categoryRule{Name: name}
			if conds, ok := item.Value.(yaml.MapSlice); ok {
				for _, cond := range conds {
					ck, _ := cond.Key.(string)
					cv, _ := cond.Value.(string)
					switch ck {
					case "genre_ids":
						parseGenreIDs(cv, &rule)
					case "original_language":
						rule.Languages = splitComma(cv)
					case "origin_country":
						rule.Countries = splitComma(cv)
					}
				}
			}
			rule.CatchAll = len(rule.IncludeGenres) == 0 && len(rule.ExcludeGenres) == 0 &&
				len(rule.Languages) == 0 && len(rule.Countries) == 0
			if key == "movie" {
				rules.Movie = append(rules.Movie, rule)
			} else {
				rules.Tv = append(rules.Tv, rule)
			}
		}
	}
	return rules
}

// parseGenreIDs 解析 genre_ids 配置："99,-10402" → 包含 99、排除 10402
func parseGenreIDs(s string, rule *categoryRule) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		if id > 0 {
			rule.IncludeGenres = append(rule.IncludeGenres, id)
		} else if id < 0 {
			rule.ExcludeGenres = append(rule.ExcludeGenres, -id)
		}
	}
}

func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// matchGenreIDs genreIDs 包含任一 include 且不含任一 exclude
func matchGenreIDs(genreIDs, include, exclude []int) bool {
	if len(include) > 0 && !intersectInts(genreIDs, include) {
		return false
	}
	if len(exclude) > 0 && intersectInts(genreIDs, exclude) {
		return false
	}
	return true
}

func intersectInts(a, b []int) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// matchMovie 按配置顺序匹配电影分类（TMDB detail 字段）
func (r categoryRules) matchMovie(d *tmdb.MovieDetail) string {
	genres := make([]int, 0, len(d.Genres))
	for _, g := range d.Genres {
		genres = append(genres, g.ID)
	}
	countries := make([]string, 0, len(d.ProductionCountries))
	for _, c := range d.ProductionCountries {
		countries = append(countries, c.ISO_3166_1)
	}
	for _, rule := range r.Movie {
		if rule.matches(genres, d.OriginalLanguage, countries) {
			return rule.Name
		}
	}
	return "未分类"
}

// matchTv 按配置顺序匹配剧集分类（TMDB detail 字段）
func (r categoryRules) matchTv(d *tmdb.TvDetail) string {
	genres := make([]int, 0, len(d.Genres))
	for _, g := range d.Genres {
		genres = append(genres, g.ID)
	}
	for _, rule := range r.Tv {
		if rule.matches(genres, d.OriginalLanguage, d.OriginCountry) {
			return rule.Name
		}
	}
	return "未分类"
}

func (rule *categoryRule) matches(genreIDs []int, language string, countries []string) bool {
	if !matchGenreIDs(genreIDs, rule.IncludeGenres, rule.ExcludeGenres) {
		return false
	}
	if len(rule.Languages) > 0 && !containsStr(rule.Languages, language) {
		return false
	}
	if len(rule.Countries) > 0 {
		found := false
		for _, c := range countries {
			if containsStr(rule.Countries, c) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// getCategoryRules 读取当前 MoviePilot 配置中的分类策略
func getCategoryRules() categoryRules {
	return parseCategoryRules(models.MoviePilotConfigGlobal.CategoryConfig)
}

// lookupTmdbMedia 对已解析媒体执行 TMDB 校验并确定分类：
// 返回 TMDB 官方标题（当前语言）、TMDB ID、TMDB 年份、分类名。
// TMDB 校验失败返回错误（由调用方按无法识别处理）。
func lookupTmdbMedia(ctx context.Context, media *IdentifyResult) (officialTitle string, tmdbID int64, tmdbYear int, categoryName string, err error) {
	isMovie := media.Category != "tv"
	var checkName string
	var checkID int64
	var checkYear int
	if isMovie {
		movieImpl := scrape.NewTmdbMovieImpl(nil, ctx)
		checkName, checkID, checkYear, err = movieImpl.CheckByNameAndYear(media.Title, media.Year, true)
	} else {
		tvImpl := scrape.NewTmdbTvShowImpl(nil, ctx)
		checkName, checkID, checkYear, err = tvImpl.CheckByNameAndYear(media.Title, media.Year, true)
	}
	if err != nil || checkID <= 0 {
		if err == nil {
			err = fmt.Errorf("TMDB 校验失败")
		}
		return "", 0, 0, "", err
	}
	client := models.GlobalScrapeSettings.GetTmdbClient()
	rules := getCategoryRules()
	if isMovie {
		detail, dErr := client.GetMovieDetail(checkID, models.GlobalScrapeSettings.GetTmdbLanguage())
		if dErr != nil {
			helpers.AppLogger.Warnf("MoviePilot 获取 TMDB 电影详情失败（%d）：%v", checkID, dErr)
			categoryName = fallbackCategory(rules.Movie)
		} else {
			categoryName = rules.matchMovie(detail)
		}
	} else {
		detail, dErr := client.GetTvDetail(checkID, models.GlobalScrapeSettings.GetTmdbLanguage())
		if dErr != nil {
			helpers.AppLogger.Warnf("MoviePilot 获取 TMDB 剧集详情失败（%d）：%v", checkID, dErr)
			categoryName = fallbackCategory(rules.Tv)
		} else {
			categoryName = rules.matchTv(detail)
		}
	}
	return checkName, checkID, checkYear, categoryName, nil
}

// fallbackCategory 无配置时兜底分类名
func fallbackCategory(list []categoryRule) string {
	for _, r := range list {
		if r.CatchAll {
			return r.Name
		}
	}
	return "未分类"
}

// organizeRootPath 由上传根目录推导整理根目录：父目录下的「已整理」
// 例如 /影视/待整理 → 影视/已整理
func organizeRootPath(uploadRoot string) string {
	p := strings.TrimRight(uploadRoot, "/")
	idx := strings.LastIndex(p, "/")
	if idx > 0 {
		return p[:idx] + "/已整理"
	}
	return "已整理"
}
