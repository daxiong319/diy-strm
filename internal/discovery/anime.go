package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 番剧目录（对应 tgto123 anime：Bangumi 放送日历/搜索 + AniList 双源，
// 可选 TMDB 匹配写入 DiscoverySubjectCache）
// ---------------------------------------------------------------------------

const (
	bangumiBaseURL   = "https://api.bgm.tv"
	anilistGraphQLURL = "https://graphql.anilist.co"
	animeHTTPTimeout = 15 * time.Second
)

var animeHTTPClient = &http.Client{Timeout: animeHTTPTimeout}

// bangumiWeekday Bangumi 星期分组
type bangumiWeekday struct {
	ID   int    `json:"id"`
	En   string `json:"en"`
	CN   string `json:"cn"`
	Items []bangumiSubject `json:"items"`
}

// bangumiSubject Bangumi 条目（calendar / v0 字段子集）
type bangumiSubject struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	NameCN      string `json:"name_cn"`
	Date        string `json:"date"` // 播出日期 YYYY-MM-DD
	AirWeekday  int    `json:"air_weekday"`
	Summary     string `json:"summary"`
	Images      struct {
		Common  string `json:"common"`
		Large   string `json:"large"`
		Medium  string `json:"medium"`
	} `json:"images"`
	Rating struct {
		Score float64 `json:"score"`
	} `json:"rating"`
	Tags []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"tags"`
}

func bangumiHeaders() map[string]string {
	return map[string]string{
		"User-Agent": "diy-strm/discovery (+https://github.com/diy-strm)",
		"Accept":     "application/json",
	}
}

// AnimeCalendarBangumi Bangumi 每周放送日历（GET /calendar）
func AnimeCalendarBangumi(force bool) ([]CalendarDay, error) {
	cacheKey := "anime:bangumi:calendar:v1"
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.([]CalendarDay), nil
		}
	}
	req, err := http.NewRequest(http.MethodGet, bangumiBaseURL+"/calendar", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range bangumiHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := animeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Bangumi 放送日历失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var weekdays []bangumiWeekday
	if err := json.Unmarshal(body, &weekdays); err != nil {
		return nil, fmt.Errorf("解析 Bangumi 放送日历失败：%v", err)
	}
	days := make([]CalendarDay, 0, len(weekdays))
	for _, wd := range weekdays {
		items := make([]Item, 0, len(wd.Items))
		for _, s := range wd.Items {
			item := Item{
				Source:     "bangumi",
				MediaType:  "tv",
				ExternalID: strconv.Itoa(s.ID),
				EntityKey:  normalizeEntityKey("bangumi", "tv", strconv.Itoa(s.ID)),
				Title:      firstNonEmptyStr(s.NameCN, s.Name),
				OriginalTitle: s.Name,
				Overview:   s.Summary,
				VoteAvg:    s.Rating.Score,
				ReleaseDate: s.Date,
			}
			if item.Poster = s.Images.Large; item.Poster == "" {
				item.Poster = s.Images.Common
			}
			for i, tag := range s.Tags {
				if i >= 5 {
					break
				}
				item.Genres = append(item.Genres, tag.Name)
			}
			if len(s.Date) >= 4 {
				item.Year, _ = strconv.Atoi(s.Date[:4])
			}
			items = append(items, item)
		}
		days = append(days, CalendarDay{
			Date:  fmt.Sprintf("weekday-%d", wd.ID),
			Label: firstNonEmptyStr(wd.CN, weekdayCN(wd.ID)),
			Items: items,
		})
	}
	cacheSet(cacheKey, days)
	return days, nil
}

func weekdayCN(id int) string {
	names := map[int]string{1: "周一", 2: "周二", 3: "周三", 4: "周四", 5: "周五", 6: "周六", 0: "周日", 7: "周日"}
	if name, ok := names[id]; ok {
		return name
	}
	return ""
}

// AnimeSearch 番剧搜索（source: bangumi/anilist，默认 bangumi 失败回退 anilist）
func AnimeSearch(ctx context.Context, keyword, source string, page int, force bool) (*PageResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return &PageResult{Items: []Item{}, Page: 1, TotalPages: 1}, nil
	}
	if page <= 0 {
		page = 1
	}
	source = strings.ToLower(strings.TrimSpace(source))
	var (
		result *PageResult
		err    error
	)
	switch source {
	case "anilist":
		result, err = animeSearchAniList(ctx, keyword, page, force)
	case "bangumi", "":
		result, err = animeSearchBangumi(keyword, force)
		if err != nil && source == "" {
			// 默认源失败自动回退 AniList
			result, err = animeSearchAniList(ctx, keyword, page, false)
		}
	default:
		return nil, fmt.Errorf("不支持的番剧数据源：%s", source)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// animeSearchBangumi Bangumi 搜索（POST /v0/search/subjects）
func animeSearchBangumi(keyword string, force bool) (*PageResult, error) {
	cacheKey := "anime:bangumi:search:v1:" + url.QueryEscape(keyword)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	payload := map[string]any{"keyword": keyword}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, bangumiBaseURL+"/v0/search/subjects", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range bangumiHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := animeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Bangumi 搜索失败：%v", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Bangumi 搜索失败：HTTP %d %s", resp.StatusCode, truncateStr(string(rawBody), 200))
	}
	var out struct {
		Data []bangumiSubject `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("解析 Bangumi 搜索失败：%v", err)
	}
	items := make([]Item, 0, len(out.Data))
	for _, s := range out.Data {
		item := Item{
			Source:        "bangumi",
			MediaType:     "tv",
			ExternalID:    strconv.Itoa(s.ID),
			EntityKey:     normalizeEntityKey("bangumi", "tv", strconv.Itoa(s.ID)),
			Title:         firstNonEmptyStr(s.NameCN, s.Name),
			OriginalTitle: s.Name,
			Overview:      s.Summary,
			VoteAvg:       s.Rating.Score,
			ReleaseDate:   s.Date,
		}
		if item.Poster = s.Images.Large; item.Poster == "" {
			item.Poster = s.Images.Common
		}
		for i, tag := range s.Tags {
			if i >= 5 {
				break
			}
			item.Genres = append(item.Genres, tag.Name)
		}
		if len(s.Date) >= 4 {
			item.Year, _ = strconv.Atoi(s.Date[:4])
		}
		items = append(items, item)
	}
	result := &PageResult{Items: items, Page: 1, TotalPages: 1}
	cacheSet(cacheKey, result)
	return result, nil
}

// anilistMedia AniList Media 字段
type anilistMedia struct {
	ID          int    `json:"id"`
	Title       struct {
		Romaji  string `json:"romaji"`
		English string `json:"english"`
		Native  string `json:"native"`
	} `json:"title"`
	CoverImage struct {
		Large string `json:"large"`
		ExtraLarge string `json:"extraLarge"`
	} `json:"coverImage"`
	AverageScore int `json:"averageScore"`
	Description  string `json:"description"`
	Genres       []string `json:"genres"`
	StartDate    struct {
		Year  int `json:"year"`
		Month int `json:"month"`
		Day   int `json:"day"`
	} `json:"startDate"`
	NextAiringEpisode *struct {
		Episode           int   `json:"episode"`
		AiringAt          int64 `json:"airingAt"`
		TimeUntilAiring   int64 `json:"timeUntilAiring"`
	} `json:"nextAiringEpisode"`
}

func anilistDate(m anilistMedia) string {
	if m.StartDate.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", m.StartDate.Year, m.StartDate.Month, m.StartDate.Day)
}

// animeSearchAniList AniList GraphQL 搜索
func animeSearchAniList(ctx context.Context, keyword string, page int, force bool) (*PageResult, error) {
	cacheKey := fmt.Sprintf("anime:anilist:search:v1:%s:%d", url.QueryEscape(keyword), page)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*PageResult), nil
		}
	}
	query := `
query ($page: Int, $search: String) {
  Page(page: $page, perPage: 24) {
    pageInfo { currentPage lastPage }
    media(type: ANIME, sort: SEARCH_MATCH, search: $search) {
      id
      title { romaji english native }
      coverImage { large extraLarge }
      averageScore
      description(asHtml: false)
      genres
      startDate { year month day }
      nextAiringEpisode { episode airingAt }
    }
  }
}`
	payload := map[string]any{
		"query": query,
		"variables": map[string]any{"page": page, "search": keyword},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anilistGraphQLURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := animeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 AniList 失败：%v", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Data struct {
			Page struct {
				PageInfo struct {
					CurrentPage int `json:"currentPage"`
					LastPage    int `json:"lastPage"`
				} `json:"pageInfo"`
				Media []anilistMedia `json:"media"`
			} `json:"Page"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rawBody, &out); err != nil {
		return nil, fmt.Errorf("解析 AniList 响应失败：%v", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("AniList 查询失败：%s", out.Errors[0].Message)
	}
	items := make([]Item, 0, len(out.Data.Page.Media))
	for _, m := range out.Data.Page.Media {
		vote := 0.0
		if m.AverageScore > 0 {
			vote = float64(m.AverageScore) / 10.0
		}
		desc := strings.TrimSpace(m.Description)
		item := Item{
			Source:        "anilist",
			MediaType:     "tv",
			ExternalID:    strconv.Itoa(m.ID),
			EntityKey:     normalizeEntityKey("anilist", "tv", strconv.Itoa(m.ID)),
			Title:         firstNonEmptyStr(m.Title.English, m.Title.Romaji, m.Title.Native),
			OriginalTitle: firstNonEmptyStr(m.Title.Romaji, m.Title.Native),
			Poster:        firstNonEmptyStr(m.CoverImage.ExtraLarge, m.CoverImage.Large),
			Overview:      desc,
			VoteAvg:       vote,
			ReleaseDate:   anilistDate(m),
			Genres:        m.Genres,
		}
		if len(item.ReleaseDate) >= 4 {
			item.Year, _ = strconv.Atoi(item.ReleaseDate[:4])
		}
		items = append(items, item)
	}
	totalPages := out.Data.Page.PageInfo.LastPage
	if totalPages <= 0 {
		totalPages = page
	}
	result := &PageResult{Items: items, Page: page, TotalPages: totalPages}
	cacheSet(cacheKey, result)
	return result, nil
}

// ---------------------------------------------------------------------------
// TMDB 匹配（番剧条目 → TMDB tv），结果写入 discovery_subject_cache
// ---------------------------------------------------------------------------

// MatchAnimeTMDB 将番剧条目与 TMDB 剧集匹配并落库。
// 返回匹配到的 TMDB ID（未匹配返回 0）
func MatchAnimeTMDB(entityKey string) (int64, error) {
	subject, err := SubjectByEntityKey(entityKey)
	if err != nil {
		return 0, fmt.Errorf("条目不存在：%s", entityKey)
	}
	if subject.TMDBID > 0 {
		return subject.TMDBID, nil
	}
	title := subject.TMDBTitle
	if title == "" {
		title = subject.Title
	}
	year := 0
	if len(subject.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(subject.ReleaseDate[:4])
	}
	client := models.GlobalScrapeSettings.GetTmdbClient()
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	resp, err := client.SearchTv(title, year, language, true)
	if err != nil {
		return 0, fmt.Errorf("TMDB 搜索失败：%v", err)
	}
	if len(resp.Results) == 0 {
		return 0, fmt.Errorf("TMDB 未找到匹配剧集：%s", title)
	}
	best := resp.Results[0]
	tmdbID := best.ID
	tmdbTitle := best.Name
	if err := UpdateSubjectCacheTMDB(entityKey, tmdbID, tmdbTitle, 1.0); err != nil {
		return tmdbID, fmt.Errorf("保存匹配结果失败：%v", err)
	}
	return tmdbID, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
