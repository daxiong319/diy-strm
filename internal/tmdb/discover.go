package tmdb

import (
	"fmt"

	"diy-strm/internal/helpers"
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
