package discovery

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 作品详情（对齐参考实现 /api/media/details/<source>/<type>/<id>：
// tmdb movie/tv/person + anilist/bangumi anime + douban）
// ---------------------------------------------------------------------------

// MediaDetails 详情响应（统一 map，字段对齐参考实现详情渲染所需全集）
func MediaDetails(source, entityType, externalID string) (map[string]any, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	entityType = strings.ToLower(strings.TrimSpace(entityType))
	id, err := strconv.ParseInt(strings.TrimSpace(externalID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("非法条目 ID：%s", externalID)
	}
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()

	switch {
	case source == "tmdb" && entityType == "movie":
		return tmdbMovieDetails(client, language, id)
	case source == "tmdb" && entityType == "tv":
		return tmdbTvDetails(client, language, id)
	case source == "tmdb" && entityType == "person":
		return actorProfile(id)
	case source == "anilist" || source == "bangumi":
		return animeDetails(source, externalID)
	case source == "douban":
		return doubanDetails(externalID)
	default:
		return nil, fmt.Errorf("不支持的详情来源：%s/%s", source, entityType)
	}
}

// tmdbMovieDetails 电影详情（含演职员）
func tmdbMovieDetails(client *tmdbClient, language string, id int64) (map[string]any, error) {
	detail, err := client.GetMovieDetail(id, language)
	if err != nil {
		return nil, err
	}
	cast, crew := []map[string]any{}, []map[string]any{}
	if peoples, err := client.GetMoviePepoles(id, language); err == nil {
		for _, c := range peoples.Cast {
			cast = append(cast, personCard(c.ID, c.Name, c.ProfilePath, c.Character, "演员"))
		}
		for _, cr := range peoples.Crew {
			if cr.Job == "Director" || cr.Job == "Screenplay" || cr.Job == "Writer" {
				crew = append(crew, personCard(cr.ID, cr.Name, cr.ProfilePath, cr.Job, cr.Department))
			}
		}
	}
	poster := models.GetTmdbImageUrl(detail.PosterPath)
	backdrop := models.GetTmdbImageUrl(detail.BackdropPath)
	genres := genreNames(detail.Genres)
	return map[string]any{
		"source": "tmdb", "media_type": "movie", "entity_type": "movie",
		"tmdb_id": detail.ID, "external_id": strconv.FormatInt(detail.ID, 10),
		"entity_key": normalizeEntityKey("tmdb", "movie", strconv.FormatInt(detail.ID, 10)),
		"title": detail.Title, "original_title": detail.OriginalTitle,
		"poster": poster, "backdrop": backdrop,
		"overview": detail.Overview, "tagline": detail.Tagline,
		"vote_avg": detail.VoteAverage, "vote_count": detail.VoteCount,
		"year": parseYear(detail.ReleaseDate), "release_date": detail.ReleaseDate,
		"runtime": detail.Runtime, "status": detail.Status,
		"genres": genres, "provider_label": "TMDB",
		"cast": cast, "crew": crew,
	}, nil
}

// tmdbTvDetails 剧集详情（含演职员与季列表）
func tmdbTvDetails(client *tmdbClient, language string, id int64) (map[string]any, error) {
	detail, err := client.GetTvDetail(id, language)
	if err != nil {
		return nil, err
	}
	cast, crew := []map[string]any{}, []map[string]any{}
	if peoples, err := client.GetTvCredits(id, language); err == nil {
		for _, c := range peoples.Cast {
			cast = append(cast, personCard(c.ID, c.Name, c.ProfilePath, c.Character, "演员"))
		}
		for _, cr := range peoples.Crew {
			if cr.Department == "Directing" || cr.Department == "Writing" {
				crew = append(crew, personCard(cr.ID, cr.Name, cr.ProfilePath, cr.Job, cr.Department))
			}
		}
	}
	poster := models.GetTmdbImageUrl(detail.PosterPath)
	backdrop := models.GetTmdbImageUrl(detail.BackdropPath)
	seasons := make([]map[string]any, 0, len(detail.Seasons))
	totalEpisodes := 0
	for _, s := range detail.Seasons {
		if s.SeasonNumber <= 0 {
			continue // 跳过特别篇
		}
		totalEpisodes += s.EpisodeCount
		seasons = append(seasons, map[string]any{
			"season_number": s.SeasonNumber, "name": s.Name,
			"episode_count": s.EpisodeCount, "air_date": s.AirDate,
			"poster": models.GetTmdbImageUrl(s.PosterPath), "overview": s.Overview,
		})
	}
	return map[string]any{
		"source": "tmdb", "media_type": "tv", "entity_type": "tv",
		"tmdb_id": detail.ID, "external_id": strconv.FormatInt(detail.ID, 10),
		"entity_key": normalizeEntityKey("tmdb", "tv", strconv.FormatInt(detail.ID, 10)),
		"title": detail.Name, "original_title": detail.OriginalName,
		"poster": poster, "backdrop": backdrop,
		"overview": detail.Overview, "tagline": detail.Tagline,
		"vote_avg": detail.VoteAverage, "vote_count": detail.VoteCount,
		"year": parseYear(detail.FirstAirDate), "release_date": detail.FirstAirDate,
		"status": detail.Status, "number_of_seasons": detail.NumberOfSeasons,
		"number_of_episodes": totalEpisodes,
		"episode_run_time": detail.EpisodeRunTime,
		"genres":           genreNames(detail.Genres),
		"provider_label":   "TMDB",
		"cast":             cast, "crew": crew, "seasons": seasons,
	}, nil
}

// actorProfile 人物档案（详情 + 作品列表）
func actorProfile(id int64) (map[string]any, error) {
	works, err := ActorWorks(id)
	if err != nil {
		return nil, err
	}
	actor, _ := works["actor"].(map[string]any)
	items, _ := works["items"].([]Item)
	result := map[string]any{
		"source": "tmdb", "media_type": "person", "entity_type": "person",
		"tmdb_id": id, "external_id": strconv.FormatInt(id, 10),
		"entity_key": normalizeEntityKey("tmdb", "person", strconv.FormatInt(id, 10)),
		"provider_label": "TMDB",
		"items":          items,
		"total_count":    works["total_count"],
		"movie_count":    works["movie_count"],
		"tv_count":       works["tv_count"],
	}
	for k, v := range actor {
		result[k] = v
	}
	return result, nil
}

// animeDetails 动漫详情（anilist/bangumi 目录条目 + TMDB 匹配补充）
func animeDetails(source, externalID string) (map[string]any, error) {
	subject, err := SubjectByEntityKey(normalizeEntityKey(source, "tv", externalID))
	if err != nil {
		return nil, fmt.Errorf("未找到该动漫条目（请先在探索页浏览目录）：%s/%s", source, externalID)
	}
	item := Item{}
	if subject.Payload != "" {
		_ = json.Unmarshal([]byte(subject.Payload), &item)
	}
	tmdbID := subject.TMDBID
	if tmdbID <= 0 {
		// 立即尝试一次匹配（详情页兜底）
		if id, score := matchPendingOne(subject); id > 0 {
			tmdbID = id
			_ = score
		}
	}
	result := map[string]any{
		"source": source, "media_type": "tv", "entity_type": "anime",
		"external_id": externalID,
		"entity_key":  normalizeEntityKey(source, "tv", externalID),
		"title":       subject.Title, "original_title": subject.OriginalTitle,
		"poster": subject.Poster, "overview": item.Overview,
		"vote_avg": subject.Rating, "year": parseYear(subject.ReleaseDate),
		"release_date": subject.ReleaseDate,
		"genres":       item.Genres,
		"provider_label": strings.ToUpper(source),
		"tmdb_id":      tmdbID,
	}
	if tmdbID > 0 {
		language := models.GlobalScrapeSettings.GetTmdbLanguage()
		client := models.GlobalScrapeSettings.GetTmdbClient()
		if detail, err := client.GetTvDetail(tmdbID, language); err == nil {
			if detail.BackdropPath != "" {
				result["backdrop"] = models.GetTmdbImageUrl(detail.BackdropPath)
			}
			if tagline := detail.Tagline; tagline != "" {
				result["tagline"] = tagline
			}
			result["number_of_seasons"] = detail.NumberOfSeasons
			result["tmdb_title"] = detail.Name
		}
	}
	return result, nil
}

// matchPendingOne 对单条未匹配条目立即执行一次 TMDB 匹配
func matchPendingOne(subject *DiscoverySubjectCache) (int64, float64) {
	client := models.GlobalScrapeSettings.GetTmdbClient()
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	tmdbID, score := matchSubjectTMDB(client, language, subject.MediaType, subject.Title, subject.OriginalTitle, parseYear(subject.ReleaseDate))
	if tmdbID > 0 {
		now := timeNowPtr()
		_ = dbUpdateSubjectTMDB(subject.ID, tmdbID, score, now)
	}
	return tmdbID, score
}

// doubanDetails 豆瓣条目详情（目录缓存 + TMDB 匹配）
func doubanDetails(externalID string) (map[string]any, error) {
	subject, err := SubjectByEntityKey(normalizeEntityKey("douban", "", externalID))
	if err != nil {
		return nil, fmt.Errorf("未找到该豆瓣条目：%s", externalID)
	}
	raw := map[string]any{}
	if subject.Payload != "" {
		_ = json.Unmarshal([]byte(subject.Payload), &raw)
	}
	tmdbID := subject.TMDBID
	if tmdbID <= 0 {
		if id, _ := matchPendingOne(subject); id > 0 {
			tmdbID = id
		}
	}
	mediaType := subject.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}
	info, _ := raw["info"].(string)
	result := map[string]any{
		"source": "douban", "media_type": mediaType, "entity_type": mediaType,
		"external_id": externalID, "douban_id": externalID,
		"entity_key": normalizeEntityKey("douban", mediaType, externalID),
		"title": subject.Title, "original_title": subject.OriginalTitle,
		"poster": subject.Poster, "vote_avg": subject.Rating,
		"year": parseYear(subject.ReleaseDate), "release_date": subject.ReleaseDate,
		"provider_label": "豆瓣", "info": info, "tmdb_id": tmdbID,
	}
	if tmdbID > 0 {
		language := models.GlobalScrapeSettings.GetTmdbLanguage()
		client := models.GlobalScrapeSettings.GetTmdbClient()
		if mediaType == "tv" {
			if detail, err := client.GetTvDetail(tmdbID, language); err == nil {
				result["backdrop"] = models.GetTmdbImageUrl(detail.BackdropPath)
				result["overview"] = detail.Overview
				result["tagline"] = detail.Tagline
				result["number_of_seasons"] = detail.NumberOfSeasons
				result["vote_count"] = detail.VoteCount
				result["status"] = detail.Status
				result["genres"] = genreNames(detail.Genres)
			}
		} else if detail, err := client.GetMovieDetail(tmdbID, language); err == nil {
			result["backdrop"] = models.GetTmdbImageUrl(detail.BackdropPath)
			result["overview"] = detail.Overview
			result["tagline"] = detail.Tagline
			result["vote_count"] = detail.VoteCount
			result["runtime"] = detail.Runtime
			result["status"] = detail.Status
			result["genres"] = genreNames(detail.Genres)
		}
	}
	return result, nil
}

// personCard 演职员卡片
func personCard(id int64, name, profilePath, character, department string) map[string]any {
	poster := ""
	if profilePath != "" {
		poster = models.GetTmdbImageUrl(profilePath)
	}
	return map[string]any{
		"tmdb_id": id, "name": name, "profile": poster,
		"character": character, "department": department,
		"entity_key": normalizeEntityKey("tmdb", "person", strconv.FormatInt(id, 10)),
	}
}

// genreNames 类型名列表
func genreNames(genres []tmdbGenre) []string {
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		out = append(out, g.Name)
	}
	return out
}
