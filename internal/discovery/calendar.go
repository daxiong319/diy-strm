package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"diy-strm/internal/hdhive"
	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 追剧日历（对应 tgto123 calendar：影巢 feed 按天分组，Asia/Shanghai 时区）
// ---------------------------------------------------------------------------

// CalendarDay 单日条目
type CalendarDay struct {
	Date  string `json:"date"`            // YYYY-MM-DD（Asia/Shanghai）
	Label string `json:"label"`           // 周几
	Items []Item `json:"items"`
}

var calendarLocation = mustLoadShanghai()

func mustLoadShanghai() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

var weekdayNames = map[time.Weekday]string{
	time.Sunday: "周日", time.Monday: "周一", time.Tuesday: "周二",
	time.Wednesday: "周三", time.Thursday: "周四", time.Friday: "周五",
	time.Saturday: "周六",
}

// calendarKindFilters 日历类型过滤规则：kind → 判定函数
// all=全部 / tv=剧集 / movie=电影 / upcoming=即将播出 / on-air=播出中 / airing-today=今日播出
func calendarKindMatch(kind, mediaType, airDate string) bool {
	now := time.Now().In(calendarLocation)
	today := now.Format("2006-01-02")
	switch strings.ToLower(kind) {
	case "", "all":
		return true
	case "tv":
		return mediaType == "tv"
	case "movie":
		return mediaType == "movie"
	case "upcoming":
		return airDate > today
	case "on-air":
		// 播出中：日期在 30 天前～7 天后窗口内
		t, err := time.ParseInLocation("2006-01-02", airDate[:minLen(airDate, 10)], calendarLocation)
		if err != nil {
			return false
		}
		return !t.Before(now.AddDate(0, 0, -30)) && !t.After(now.AddDate(0, 0, 7))
	case "airing-today":
		return airDate >= today
	default:
		return true
	}
}

func minLen(s string, n int) int {
	if len(s) < n {
		return len(s)
	}
	return n
}

// FeedEpisodePayload 影巢 calendar feed 响应结构（data 字段）
type FeedEpisodePayload struct {
	Episodes []struct {
		ID            int64  `json:"id"`
		TMDBID        int64  `json:"tmdb_id"`
		TVDBID        int64  `json:"tvdb_id"`
		SeriesName    string `json:"series_name"`
		MediaType     string `json:"media_type"`
		SeasonNumber  int    `json:"season_number"`
		EpisodeNumber int    `json:"episode_number"`
		Name          string `json:"name"`
		AirDate       string `json:"air_date"`
		AirTimestamp  int64  `json:"air_timestamp"`
		PosterPath    string `json:"poster_path"`
		VoteAvg       float64 `json:"vote_average"`
		Overview      string `json:"overview"`
	} `json:"episodes"`
	Days []struct {
		Date     string `json:"date"`
		Episodes []int  `json:"episodes"` // episode id 引用
	} `json:"days"`
}

// Calendar 追剧日历。
// days：拉取天数（1-30）；kind：all/tv/movie/upcoming/on-air/airing-today
func Calendar(ctx context.Context, days, kind string, force bool) ([]CalendarDay, error) {
	dayCount := SettingInt(SettingCalendarDays, 7)
	if v := parseIntSafe(days); v > 0 && v <= 30 {
		dayCount = v
	}
	kindKey := strings.ToLower(strings.TrimSpace(kind))
	if kindKey == "" {
		kindKey = SettingString(SettingCalendarKind, "all")
	}
	cacheKey := fmt.Sprintf("calendar:v1:%d:%s", dayCount, kindKey)
	if !force {
		if cached := cacheGet(cacheKey); cached != nil {
			return cached.([]CalendarDay), nil
		}
	}
	episodes, err := fetchCalendarEpisodes(ctx, dayCount)
	if err != nil {
		return nil, err
	}
	days2 := groupByDay(episodes, kindKey)
	cacheSet(cacheKey, days2)
	return days2, nil
}

// fetchCalendarEpisodes 拉取影巢日历 feed；通道不可用时回退 TMDB airing_today 兜底
func fetchCalendarEpisodes(ctx context.Context, days int) ([]Item, error) {
	resp, err := feedExecute(ctx, func(fc hdhive.FeedClient) (*hdhive.OAuthAPIResponse, error) {
		return fc.GetCalendar(ctx, days)
	})
	if err == nil && (resp.Success || len(resp.Data) > 0) {
		var payload FeedEpisodePayload
		if json.Unmarshal(resp.Data, &payload) == nil && len(payload.Episodes) > 0 {
			items := make([]Item, 0, len(payload.Episodes))
			for _, ep := range payload.Episodes {
				media := strings.ToLower(ep.MediaType)
				if media != "movie" {
					media = "tv"
				}
				airDate := ep.AirDate
				if airDate == "" && ep.AirTimestamp > 0 {
					airDate = time.Unix(ep.AirTimestamp, 0).In(calendarLocation).Format("2006-01-02 15:04")
				}
				item := Item{
					Source:        "hdhive",
					MediaType:     media,
					TMDBID:        ep.TMDBID,
					ExternalID:    fmt.Sprintf("%d:%d:%d", ep.TMDBID, ep.SeasonNumber, ep.EpisodeNumber),
					EntityKey:     normalizeEntityKey("tmdb", media, fmt.Sprintf("%d", ep.TMDBID)),
					Title:         firstNonEmptyStr(ep.SeriesName, ep.Name),
					Poster:        absoluteTMDBImage(ep.PosterPath),
					Overview:      ep.Overview,
					VoteAvg:       ep.VoteAvg,
					AirDate:       airDate,
					SeasonNumber:  ep.SeasonNumber,
					EpisodeNumber: ep.EpisodeNumber,
				}
				if ep.Name != "" && ep.SeriesName != "" && ep.Name != ep.SeriesName {
					item.EpisodeTitle = ep.Name
				}
				items = append(items, item)
			}
			return items, nil
		}
	}
	// 影巢通道不可用 → 回退 TMDB（本周播出 + 今日播出）
	return tmdbAiringFallback(days)
}

// tmdbAiringFallback TMDB 播出表兜底（on_the_air 本周 + airing_today 今日）
func tmdbAiringFallback(days int) ([]Item, error) {
	language := models.GlobalScrapeSettings.GetTmdbLanguage()
	client := models.GlobalScrapeSettings.GetTmdbClient()
	var items []Item
	respTv, err := client.GetTvList("on_the_air", language, 1)
	if err == nil {
		for _, v := range tmdbTvToItems(respTv.Results) {
			v.Source = "tmdb"
			v.AirDate = v.ReleaseDate
			items = append(items, v)
		}
	}
	respToday, err := client.GetTvList("airing_today", language, 1)
	if err == nil {
		for _, v := range tmdbTvToItems(respToday.Results) {
			v.Source = "tmdb"
			v.AirDate = v.ReleaseDate
			items = append(items, v)
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("追剧日历数据源不可用（影巢通道未授权且 TMDB 播出表无数据）")
	}
	_ = days
	return items, nil
}

// groupByDay 按天分组（Asia/Shanghai），只保留 days 窗口内的条目并排序
func groupByDay(episodes []Item, kind string) []CalendarDay {
	now := time.Now().In(calendarLocation)
	start := now.AddDate(0, 0, -1) // 含昨天补漏
	end := now.AddDate(0, 0, 14)
	byDay := map[string][]Item{}
	for _, ep := range episodes {
		date := ep.AirDate
		if date == "" {
			continue
		}
		if len(date) > 10 {
			date = date[:10]
		}
		t, err := time.ParseInLocation("2006-01-02", date, calendarLocation)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		if !calendarKindMatch(kind, ep.MediaType, date) {
			continue
		}
		byDay[date] = append(byDay[date], ep)
	}
	dates := make([]string, 0, len(byDay))
	for d := range byDay {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	out := make([]CalendarDay, 0, len(dates))
	for _, d := range dates {
		list := byDay[d]
		sort.Slice(list, func(i, j int) bool { return list[i].AirDate < list[j].AirDate })
		t, _ := time.ParseInLocation("2006-01-02", d, calendarLocation)
		out = append(out, CalendarDay{
			Date:  d,
			Label: weekdayNames[t.Weekday()],
			Items: list,
		})
	}
	return out
}

// parseIntSafe 安全解析整数
func parseIntSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
