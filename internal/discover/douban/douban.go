package douban

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	movieBaseURL = "https://movie.douban.com"
	mobileBaseURL = "https://m.douban.com"
	userAgent     = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
)

// Subject 榜单条目（search_subjects 接口）
type Subject struct {
	Rate      string `json:"rate"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Cover     string `json:"cover"`
	ID        string `json:"id"`
	Subtype   string `json:"subtype"`
	IsPlayable bool   `json:"is_playable"`
}

type searchSubjectsResponse struct {
	Subjects []Subject `json:"subjects"`
}

// CollectionItem 片单条目（rexxar 接口）
type CollectionItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	URL           string `json:"url"`
	Rating        struct {
		Value float64 `json:"value"`
	} `json:"rating"`
	Cover       struct {
		URL string `json:"url"`
	} `json:"cover"`
	ReleaseDate string `json:"release_date"`
	Year        string `json:"year"`
	Info        string `json:"info"`
}

type collectionResponse struct {
	Items []CollectionItem `json:"subject_collection_items"`
}

// Client 豆瓣数据客户端
type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) doGet(rawURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", mobileBaseURL+"/")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("豆瓣接口返回状态码 %d", resp.StatusCode)
	}
	return body, nil
}

// GetSubjects 获取榜单条目（豆瓣 j/search_subjects 接口）
// type: movie 或 tv；tag：热门/最新/豆瓣高分/华语/欧美 等
func (c *Client) GetSubjects(subjectType string, tag string, pageStart int, pageLimit int) ([]Subject, error) {
	if pageLimit <= 0 {
		pageLimit = 30
	}
	if pageLimit > 50 {
		pageLimit = 50
	}
	if subjectType == "" {
		subjectType = "movie"
	}
	if tag == "" {
		tag = "热门"
	}
	params := url.Values{}
	params.Set("type", subjectType)
	params.Set("tag", tag)
	params.Set("sort", "recommend")
	params.Set("page_limit", fmt.Sprintf("%d", pageLimit))
	params.Set("page_start", fmt.Sprintf("%d", pageStart))
	rawURL := fmt.Sprintf("%s/j/search_subjects?%s", movieBaseURL, params.Encode())
	body, err := c.doGet(rawURL)
	if err != nil {
		return nil, err
	}
	var result searchSubjectsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析豆瓣榜单响应失败：%w", err)
	}
	return result.Subjects, nil
}

// RecommendItem 豆瓣推荐目录条目（rexxar recommend 接口）
type RecommendItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Year    string `json:"year"`
	Subtype string `json:"subtype"`
	Info    string `json:"info"`
	Rating  struct {
		Value float64 `json:"value"`
	} `json:"rating"`
	Cover struct {
		URL string `json:"url"`
	} `json:"cover"`
}

type recommendResponse struct {
	Items []RecommendItem `json:"items"`
	Total int             `json:"total"`
}

// GetRecommend 获取豆瓣推荐目录（rexxar recommend 接口，支持分类/地区/形式/排序翻页）
// subjectType: movie/tv；selectedCategories 形如 {"类型":"电影","地区":"美国","形式":"剧集"}
// 的 JSON 串（按需组合，空分类传 {}）；sort: T(推荐)/S(近期热门)/R(高分)
func (c *Client) GetRecommend(subjectType, selectedCategories, sort string, start int, count int) ([]RecommendItem, int, error) {
	if subjectType != "tv" {
		subjectType = "movie"
	}
	if count <= 0 {
		count = 24
	}
	if count > 50 {
		count = 50
	}
	if selectedCategories == "" {
		selectedCategories = "{}"
	}
	if sort == "" {
		sort = "T"
	}
	params := url.Values{}
	params.Set("start", fmt.Sprintf("%d", start))
	params.Set("count", fmt.Sprintf("%d", count))
	params.Set("selected_categories", selectedCategories)
	params.Set("sort", sort)
	
	rawURL := fmt.Sprintf("%s/rexxar/api/v2/%s/recommend?%s", mobileBaseURL, subjectType, params.Encode())
	body, err := c.doGet(rawURL)
	if err != nil {
		return nil, 0, err
	}
	var result recommendResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, 0, fmt.Errorf("解析豆瓣推荐响应失败：%w", err)
	}
	return result.Items, result.Total, nil
}

// GetCollectionItems 获取片单条目（豆瓣 rexxar 接口）
// collection: movie_hot_gaia / tv_hot_gaia / movie_weekly_best / tv_weekly_best 等
func (c *Client) GetCollectionItems(collection string, start int, count int) ([]CollectionItem, error) {
	if collection == "" {
		collection = "movie_hot_gaia"
	}
	if count <= 0 {
		count = 30
	}
	if count > 50 {
		count = 50
	}
	params := url.Values{}
	params.Set("os", "ios")
	params.Set("for_mobile", "1")
	params.Set("start", fmt.Sprintf("%d", start))
	params.Set("count", fmt.Sprintf("%d", count))
	rawURL := fmt.Sprintf("%s/rexxar/api/v2/subject_collection/%s/items?%s", mobileBaseURL, collection, params.Encode())
	body, err := c.doGet(rawURL)
	if err != nil {
		return nil, err
	}
	var result collectionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析豆瓣片单响应失败：%w", err)
	}
	return result.Items, nil
}
