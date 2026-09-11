package discovery

import (
	"context"
	"fmt"
	"strings"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 热门演员（对齐参考实现 actors：TMDB person/popular + 演员作品 + 人物详情）
// ---------------------------------------------------------------------------

// ActorItem 演员条目（复用 Item 承载，entity_type=person）
type ActorItem = Item

// ActorsPage 热门演员分页
type ActorsPage struct {
	Items      []Item `json:"items"`
	Page       int    `json:"page"`
	TotalPages int    `json:"total_pages"`
	TotalItems int    `json:"total_items"`
}

// Actors 热门演员列表（TMDB person/popular）
func Actors(page int, force bool) (*ActorsPage, error) {
	if page <= 0 {
		page = 1
	}
	cacheKey := fmt.Sprintf("actors:popular:v1:%d", page)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.(*ActorsPage), nil
		}
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()
	resp, err := client.PersonPopular(language, page)
	if err != nil {
		return nil, err
	}
	out := &ActorsPage{
		Items:      personResultsToItems(resp.Results),
		Page:       page,
		TotalPages: clampTotalPages(resp.TotalPages),
		TotalItems: resp.TotalResults,
	}
	cacheSet(cacheKey, out)
	return out, nil
}

// SearchMedia 统一搜索（对齐参考实现 /api/media/search：movie/tv/person）
func SearchMedia(ctx context.Context, q, mediaType string, page int, force bool) (*ActorsPage, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return &ActorsPage{Items: []Item{}, Page: 1, TotalPages: 1}, nil
	}
	if page <= 0 {
		page = 1
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "person":
		cacheKey := fmt.Sprintf("search:person:v1:%s:%d", q, page)
		if !force {
			if cached := cacheGet(cacheKey); cached != nil {
				return cached.(*ActorsPage), nil
			}
		}
		language := models.GlobalScrapeSettings.GetTmdbLanguage()
		client := models.GlobalScrapeSettings.GetTmdbClient()
		resp, err := client.SearchPerson(q, language, page)
		if err != nil {
			return nil, err
		}
		out := &ActorsPage{
			Items:      personResultsToItems(resp.Results),
			Page:       page,
			TotalPages: clampTotalPages(resp.TotalPages),
			TotalItems: resp.TotalResults,
		}
		cacheSet(cacheKey, out)
		return out, nil
	default:
		// movie/tv 搜索（movie 与空值走电影）
		if mediaType != "tv" {
			mediaType = "movie"
		}
		cacheKey := fmt.Sprintf("search:%s:v1:%s:%d", mediaType, q, page)
		if !force {
			if cached := cacheGet(cacheKey); cached != nil {
				return cached.(*ActorsPage), nil
			}
		}
		language := models.GlobalScrapeSettings.GetTmdbLanguage()
		client := models.GlobalScrapeSettings.GetTmdbClient()
		var (
			items []Item
			total int
			pages int
			err   error
		)
		if mediaType == "tv" {
			var resp *tmdbTvSearch
			resp, err = searchTvItems(client, language, q, page)
			if err == nil {
				items, total, pages = resp.items, resp.total, resp.pages
			}
		} else {
			var resp *tmdbMovieSearch
			resp, err = searchMovieItems(client, language, q, page)
			if err == nil {
				items, total, pages = resp.items, resp.total, resp.pages
			}
		}
		if err != nil {
			return nil, err
		}
		out := &ActorsPage{Items: items, Page: page, TotalPages: pages, TotalItems: total}
		cacheSet(cacheKey, out)
		return out, nil
	}
}

type tmdbTvSearch struct {
	items []Item
	total int
	pages int
}

type tmdbMovieSearch struct {
	items []Item
	total int
	pages int
}

func searchTvItems(client *tmdbClient, language, q string, page int) (*tmdbTvSearch, error) {
	resp, err := client.SearchTv(q, 0, language, false)
	if err != nil {
		return nil, err
	}
	return &tmdbTvSearch{items: tmdbTvToItems(resp.Results), total: resp.TotalResults, pages: clampTotalPages(resp.TotalPages)}, nil
}

func searchMovieItems(client *tmdbClient, language, q string, page int) (*tmdbMovieSearch, error) {
	resp, err := client.SearchMovie(q, 0, language, false, false)
	if err != nil {
		return nil, err
	}
	return &tmdbMovieSearch{items: tmdbMovieToItems(resp.Results), total: resp.TotalResults, pages: clampTotalPages(resp.TotalPages)}, nil
}

// personResultsToItems TMDB 人物结果 → 条目
func personResultsToItems(results []tmdbPersonResult) []Item {
	items := make([]Item, 0, len(results))
	for _, p := range results {
		item := Item{
			Source:     "tmdb",
			MediaType:  "person",
			TMDBID:     p.ID,
			ExternalID: fmt.Sprintf("%d", p.ID),
			EntityKey:  normalizeEntityKey("tmdb", "person", fmt.Sprintf("%d", p.ID)),
			Title:      p.Name,
			OriginalTitle: p.OriginalName,
			VoteAvg:    p.Popularity,
		}
		if p.ProfilePath != "" {
			item.Poster = models.GetTmdbImageUrl(p.ProfilePath)
		}
		item.Overview = p.KnownForDepartment
		for i, kf := range p.KnownFor {
			if i >= 3 {
				break
			}
			item.Genres = append(item.Genres, kf.DisplayName())
		}
		items = append(items, item)
	}
	return items
}

func clampTotalPages(p int) int {
	if p <= 0 {
		return 1
	}
	if p > 500 {
		return 500
	}
	return p
}

// ActorWorks 演员作品列表（TMDB person combined_credits，按上映日期与热度排序）
func ActorWorks(actorID int64) (map[string]any, error) {
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()
	detail, err := client.GetPersonDetail(actorID, language)
	if err != nil {
		return nil, err
	}
	credits, err := client.GetPersonCombinedCredits(actorID, language)
	if err != nil {
		return nil, err
	}
	works := make([]Item, 0, len(credits.Cast)+len(credits.Crew))
	movieCount, tvCount := 0, 0
	for _, c := range credits.Cast {
		item := creditToItem(c)
		if item.MediaType == "movie" {
			movieCount++
		} else if item.MediaType == "tv" {
			tvCount++
		}
		works = append(works, item)
	}
	// 幕后作品去重后追加（导演/编剧等有代表性的）
	seen := map[int64]bool{}
	for _, w := range works {
		if w.MediaType == "movie" || w.MediaType == "tv" {
			seen[w.TMDBID] = true
		}
	}
	for _, c := range credits.Crew {
		if c.Department != "Directing" && c.Department != "Writing" {
			continue
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		item := creditToItem(c)
		works = append(works, item)
	}
	sortWorksByDate(works)
	const maxWorks = 60
	if len(works) > maxWorks {
		works = works[:maxWorks]
	}
	poster := ""
	if detail != nil {
		poster = models.GetTmdbImageUrl(detail.ProfilePath)
	}
	return map[string]any{
		"actor": map[string]any{
			"tmdb_id":      detail.ID,
			"title":        detail.Name,
			"original_title": detail.OriginalName,
			"poster":       poster,
			"overview":     detail.Biography,
			"birthday":     detail.Birthday,
			"deathday":     detail.Deathday,
			"place_of_birth": detail.PlaceOfBirth,
			"known_for_department": detail.KnownForDepartment,
			"also_known_as": detail.AlsoKnownAs,
		},
		"items":        works,
		"movie_count":  movieCount,
		"tv_count":     tvCount,
		"total_count":  len(works),
	}, nil
}

// creditToItem 人物作品 → 条目
func creditToItem(c tmdbCreditItem) Item {
	media := c.MediaType
	if media != "movie" && media != "tv" {
		media = "movie"
	}
	item := Item{
		Source:        "tmdb",
		MediaType:     media,
		TMDBID:        c.ID,
		ExternalID:    fmt.Sprintf("%d", c.ID),
		EntityKey:     normalizeEntityKey("tmdb", media, fmt.Sprintf("%d", c.ID)),
		Title:         c.DisplayName(),
		OriginalTitle: c.OriginalName2(),
		Overview:      c.Overview,
		VoteAvg:       c.VoteAverage,
		ReleaseDate:   c.AirDate(),
		EpisodeTitle:  c.Character,
	}
	if len(c.AirDate()) >= 4 {
		item.Year, _ = strconvAtoi(c.AirDate()[:4])
	}
	if c.PosterPath != "" {
		item.Poster = models.GetTmdbImageUrl(c.PosterPath)
	}
	if c.Character != "" {
		item.EpisodeTitle = "饰 " + c.Character
	} else if c.Job != "" {
		item.EpisodeTitle = c.Job
	}
	return item
}

// sortWorksByDate 按上映日期倒序 + 热度
func sortWorksByDate(works []Item) {
	for i := 1; i < len(works); i++ {
		for j := i; j > 0; j-- {
			a, b := works[j-1], works[j]
			aDate, bDate := a.ReleaseDate, b.ReleaseDate
			if aDate < bDate || (aDate == bDate && a.VoteAvg < b.VoteAvg) {
				works[j-1], works[j] = works[j], works[j-1]
				continue
			}
			break
		}
	}
}
