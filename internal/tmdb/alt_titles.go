package tmdb

import "fmt"

// AltTitleItem 别名条目（TMDB alternative_titles）
type AltTitleItem struct {
	Iso3166 string `json:"iso_3166_1"`
	Title   string `json:"title"`
	Type    int    `json:"type,omitempty"`
}

// GetTvAlternativeTitles 剧集别名列表
// https://api.themoviedb.org/3/tv/{id}/alternative_titles
func (c *Client) GetTvAlternativeTitles(tvID int64) ([]string, error) {
	var resp struct {
		Results []AltTitleItem `json:"results"`
	}
	req := c.resty.R().SetMethod("GET").SetResult(&resp)
	r, err := c.doRequest(fmt.Sprintf("/tv/%d/alternative_titles", tvID), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		return nil, err
	}
	if !r.IsStatusSuccess() {
		return nil, fmt.Errorf("获取剧集别名失败：%s", r.String())
	}
	names := make([]string, 0, len(resp.Results))
	for _, t := range resp.Results {
		if t.Title != "" {
			names = append(names, t.Title)
		}
	}
	return names, nil
}

// GetMovieAlternativeTitles 电影别名列表
// https://api.themoviedb.org/3/movie/{id}/alternative_titles
func (c *Client) GetMovieAlternativeTitles(movieID int64) ([]string, error) {
	var resp struct {
		Titles []AltTitleItem `json:"titles"`
	}
	req := c.resty.R().SetMethod("GET").SetResult(&resp)
	r, err := c.doRequest(fmt.Sprintf("/movie/%d/alternative_titles", movieID), req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		return nil, err
	}
	if !r.IsStatusSuccess() {
		return nil, fmt.Errorf("获取电影别名失败：%s", r.String())
	}
	names := make([]string, 0, len(resp.Titles))
	for _, t := range resp.Titles {
		if t.Title != "" {
			names = append(names, t.Title)
		}
	}
	return names, nil
}
