package tmdb

import (
	"fmt"
	"strings"

	"diy-strm/internal/helpers"

	"resty.dev/v3"
)

// GetMovieList 获取电影榜单列表（popular/top_rated/now_playing/upcoming）
// https://api.themoviedb.org/3/movie/{category}
func (c *Client) GetMovieList(category string, language string, page int) (*SearchMovieResponse, error) {
	if category == "" {
		category = "popular"
	}
	respResult := SearchMovieResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest(fmt.Sprintf("/movie/%s", category), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取电影榜单失败（%s）：%+v", category, err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取电影榜单失败（%s）：%s", category, resp.String())
		return nil, fmt.Errorf("获取电影榜单失败：%s", resp.String())
	}
	return &respResult, nil
}

// GetTrendingMovies 获取热门趋势电影列表
// https://api.themoviedb.org/3/trending/movie/{time_window}
func (c *Client) GetTrendingMovies(timeWindow string, language string, page int) (*SearchMovieResponse, error) {
	if timeWindow != "week" {
		timeWindow = "day"
	}
	respResult := SearchMovieResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest(fmt.Sprintf("/trending/movie/%s", timeWindow), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取趋势电影失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取趋势电影失败：%s", resp.String())
		return nil, fmt.Errorf("获取趋势电影失败：%s", resp.String())
	}
	return &respResult, nil
}

// GetTvList 获取剧集榜单列表（popular/top_rated/on_the_air/airing_today）
// https://api.themoviedb.org/3/tv/{category}
func (c *Client) GetTvList(category string, language string, page int) (*SearchTvResponse, error) {
	if category == "" {
		category = "popular"
	}
	respResult := SearchTvResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest(fmt.Sprintf("/tv/%s", category), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取剧集榜单失败（%s）：%+v", category, err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取剧集榜单失败（%s）：%s", category, resp.String())
		return nil, fmt.Errorf("获取剧集榜单失败：%s", resp.String())
	}
	return &respResult, nil
}

// GetTrendingTv 获取热门趋势剧集列表
// https://api.themoviedb.org/3/trending/tv/{time_window}
func (c *Client) GetTrendingTv(timeWindow string, language string, page int) (*SearchTvResponse, error) {
	if timeWindow != "week" {
		timeWindow = "day"
	}
	respResult := SearchTvResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	resp, err := c.doRequest(fmt.Sprintf("/trending/tv/%s", timeWindow), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取趋势剧集失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取趋势剧集失败：%s", resp.String())
		return nil, fmt.Errorf("获取趋势剧集失败：%s", resp.String())
	}
	return &respResult, nil
}

// DiscoverQuery 影视探索多条件筛选参数（对应 TMDB /discover 接口）
type DiscoverQuery struct {
	Genre        string // 类型 ID（TMDB genre id），空为全部
	Year         string // 年份：电影按 primary_release_year，剧集按 first_air_date_year
	Region       string // 地区 ISO 3166-1（如 US/CN/JP）
	SortBy       string // latest/rating/popular，默认 popular
	VoteMin      string // 最低评分（vote_average.gte）
}

// DiscoverMovies 多条件筛选电影
// https://api.themoviedb.org/3/discover/movie
func (c *Client) DiscoverMovies(q DiscoverQuery, language string, page int) (*SearchMovieResponse, error) {
	sortKey := discoverSortBy(q.SortBy, "popularity.desc")
	respResult := SearchMovieResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	applyDiscoverParams(req, q, sortKey, "primary_release_year", "primary_release_date.desc")
	resp, err := c.doRequest("/discover/movie", req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("筛选电影失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("筛选电影失败：%s", resp.String())
		return nil, fmt.Errorf("筛选电影失败：%s", resp.String())
	}
	return &respResult, nil
}

// DiscoverTvs 多条件筛选剧集
// https://api.themoviedb.org/3/discover/tv
func (c *Client) DiscoverTvs(q DiscoverQuery, language string, page int) (*SearchTvResponse, error) {
	sortKey := discoverSortBy(q.SortBy, "popularity.desc")
	if sortKey == "primary_release_date.desc" {
		sortKey = "first_air_date.desc"
	}
	respResult := SearchTvResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	if page > 0 {
		req.SetQueryParam("page", fmt.Sprintf("%d", page))
	}
	if language != "" {
		req.SetQueryParam("language", language)
	}
	applyDiscoverParams(req, q, sortKey, "first_air_date_year", "first_air_date.desc")
	resp, err := c.doRequest("/discover/tv", req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("筛选剧集失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("筛选剧集失败：%s", resp.String())
		return nil, fmt.Errorf("筛选剧集失败：%s", resp.String())
	}
	return &respResult, nil
}

// discoverSortBy 归一化排序键（latest/rating/popular → TMDB sort_by）
func discoverSortBy(sort, defaultSort string) string {
	switch strings.TrimSpace(strings.ToLower(sort)) {
	case "latest":
		return "primary_release_date.desc"
	case "rating":
		return "vote_average.desc"
	default:
		return defaultSort
	}
}

// applyDiscoverParams 应用公共筛选参数
func applyDiscoverParams(req *resty.Request, q DiscoverQuery, sortKey, yearParam, latestSort string) {
	if q.Genre != "" {
		req.SetQueryParam("with_genres", q.Genre)
	}
	if q.Year != "" {
		req.SetQueryParam(yearParam, q.Year)
	}
	if q.Region != "" {
		req.SetQueryParam("region", strings.ToUpper(q.Region))
	}
	if q.VoteMin != "" {
		req.SetQueryParam("vote_average.gte", q.VoteMin)
		req.SetQueryParam("vote_count.gte", "50")
	}
	if q.SortBy == "rating" && q.VoteMin == "" {
		// 按评分排序时要求最低投票数，避免冷门高分刷榜
		req.SetQueryParam("vote_count.gte", "50")
	}
	req.SetQueryParam("sort_by", sortKey)
	req.SetQueryParam("include_adult", "false")
}
