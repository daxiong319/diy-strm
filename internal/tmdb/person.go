package tmdb

import (
	"fmt"

	"diy-strm/internal/helpers"
)

// PersonSearchResult 人物搜索/热门结果条目
type PersonSearchResult struct {
	Adult              bool        `json:"adult"`
	ID                 int64       `json:"id"`
	Name               string      `json:"name"`
	OriginalName       string      `json:"original_name"`
	MediaType          string      `json:"media_type"`
	Popularity         float64     `json:"popularity"`
	Gender             int         `json:"gender"`
	KnownForDepartment string      `json:"known_for_department"`
	ProfilePath        string      `json:"profile_path"`
	KnownFor           []KnownForMedia `json:"known_for"`
}

// KnownForMedia 人物「代表作」（movie/tv 摘要）
type KnownForMedia struct {
	ID            int64   `json:"id"`
	MediaType     string  `json:"media_type"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	PosterPath    string  `json:"poster_path"`
	VoteAverage   float64 `json:"vote_average"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
}

// DisplayName 代表作标题
func (k KnownForMedia) DisplayName() string {
	if k.Title != "" {
		return k.Title
	}
	return k.Name
}

// PersonSearchResponse 人物搜索响应（person/search + person/popular 共用）
type PersonSearchResponse struct {
	Page         int                 `json:"page"`
	Results      []PersonSearchResult `json:"results"`
	TotalResults int                 `json:"total_results"`
	TotalPages   int                 `json:"total_pages"`
}

// SearchPerson 人物搜索（GET /search/person）
func (c *Client) SearchPerson(query string, language string, page int) (*PersonSearchResponse, error) {
	if page <= 0 {
		page = 1
	}
	respResult := PersonSearchResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult).
		SetQueryParam("query", query).
		SetQueryParam("language", language).
		SetQueryParam("page", fmt.Sprintf("%d", page))
	resp, err := c.request("/search/person", req)
	if err != nil {
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("搜索人物失败：%s", resp.String())
		return nil, fmt.Errorf("搜索人物失败：%s", resp.String())
	}
	return &respResult, nil
}

// PersonPopular 热门人物（GET /person/popular）
func (c *Client) PersonPopular(language string, page int) (*PersonSearchResponse, error) {
	if page <= 0 {
		page = 1
	}
	respResult := PersonSearchResponse{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult).
		SetQueryParam("language", language).
		SetQueryParam("page", fmt.Sprintf("%d", page))
	resp, err := c.request("/person/popular", req)
	if err != nil {
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取热门人物失败：%s", resp.String())
		return nil, fmt.Errorf("获取热门人物失败：%s", resp.String())
	}
	return &respResult, nil
}

// PersonDetail 人物详情
type PersonDetail struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	OriginalName       string  `json:"original_name"`
	Biography          string  `json:"biography"`
	Birthday           string  `json:"birthday"`
	Deathday           string  `json:"deathday"`
	PlaceOfBirth       string  `json:"place_of_birth"`
	KnownForDepartment string  `json:"known_for_department"`
	ProfilePath        string  `json:"profile_path"`
	Popularity         float64 `json:"popularity"`
	Gender             int     `json:"gender"`
	AlsoKnownAs        []string `json:"also_known_as"`
}

// GetPersonDetail 人物详情（GET /person/{id}）
func (c *Client) GetPersonDetail(personID int64, language string) (*PersonDetail, error) {
	respResult := PersonDetail{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult).
		SetQueryParam("language", language)
	resp, err := c.request(fmt.Sprintf("/person/%d", personID), req)
	if err != nil {
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取人物详情失败：%s", resp.String())
		return nil, fmt.Errorf("获取人物详情失败：%s", resp.String())
	}
	return &respResult, nil
}

// PersonCreditItem 人物作品条目（combined_credits cast/crew 合并子集）
type PersonCreditItem struct {
	ID            int64   `json:"id"`
	MediaType     string  `json:"media_type"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	Overview      string  `json:"overview"`
	VoteAverage   float64 `json:"vote_average"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	Popularity    float64 `json:"popularity"`
	Character     string  `json:"character"`
	Department    string  `json:"department"`
	Job           string  `json:"job"`
}

// AirDate 上映/首播日期（优先首播日）
func (p PersonCreditItem) AirDate() string {
	if p.ReleaseDate != "" {
		return p.ReleaseDate
	}
	return p.FirstAirDate
}

// DisplayName 标题（优先影视名）
func (p PersonCreditItem) DisplayName() string {
	if p.Title != "" {
		return p.Title
	}
	return p.Name
}

// OriginalName2 原始标题
func (p PersonCreditItem) OriginalName2() string {
	if p.OriginalTitle != "" {
		return p.OriginalTitle
	}
	return p.OriginalName
}

// PersonCombinedCredits 人物合并作品（GET /person/{id}/combined_credits）
type PersonCombinedCredits struct {
	ID   int64              `json:"id"`
	Cast []PersonCreditItem `json:"cast"`
	Crew []PersonCreditItem `json:"crew"`
}

// GetPersonCombinedCredits 人物合并作品列表
func (c *Client) GetPersonCombinedCredits(personID int64, language string) (*PersonCombinedCredits, error) {
	respResult := PersonCombinedCredits{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult).
		SetQueryParam("language", language)
	resp, err := c.request(fmt.Sprintf("/person/%d/combined_credits", personID), req)
	if err != nil {
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取人物作品失败：%s", resp.String())
		return nil, fmt.Errorf("获取人物作品失败：%s", resp.String())
	}
	return &respResult, nil
}
