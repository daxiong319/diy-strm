package moviepilot

import (
	"context"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/scrape"
)

// IdentifyResult AI 辅助识别的媒体信息
type IdentifyResult struct {
	Category string // movie/tv
	Title    string
	Season   int
	Episode  int
	Year     int
	TmdbId   int64
}

// IdentifyFileWithAI 对无法正则识别的文件名执行 AI 辅助识别（复用刮削 AI 配置），
// 识别结果通过 TMDB 校验。未启用 AI、AI 调用失败或 TMDB 校验失败时返回 ok=false。
func IdentifyFileWithAI(ctx context.Context, fileName string) (IdentifyResult, bool) {
	settings := models.GlobalScrapeSettings
	if settings.EnableAi == models.AiActionOff {
		return IdentifyResult{}, false
	}
	if err := ctx.Err(); err != nil {
		return IdentifyResult{}, false
	}
	client := settings.GetAiClient()
	if client == nil {
		return IdentifyResult{}, false
	}
	aiInfo, err := client.TakeMoiveName(fileName, settings.GetAiPrompt())
	if err != nil {
		helpers.AppLogger.Warnf("MoviePilot AI 识别文件名失败（%s）：%v", fileName, err)
		return IdentifyResult{}, false
	}
	if aiInfo == nil || strings.TrimSpace(aiInfo.Name) == "" {
		return IdentifyResult{}, false
	}
	// TMDB 校验：优先电影，其次剧集
	if res, ok := verifyIdentifyByTmdb(ctx, fileName, aiInfo.Name, aiInfo.Year, true); ok {
		return res, true
	}
	if res, ok := verifyIdentifyByTmdb(ctx, fileName, aiInfo.Name, aiInfo.Year, false); ok {
		return res, true
	}
	helpers.AppLogger.Warnf("MoviePilot AI 识别结果未通过 TMDB 校验（%s → %s %d）", fileName, aiInfo.Name, aiInfo.Year)
	return IdentifyResult{}, false
}

// verifyIdentifyByTmdb 用 TMDB 校验 AI 识别的名称与年份，命中则返回规范化媒体信息。
// isMovie=true 时按电影查询，否则按剧集查询（季集从文件名补齐，缺省 1）。
func verifyIdentifyByTmdb(ctx context.Context, fileName, name string, year int, isMovie bool) (IdentifyResult, bool) {
	var officialName string
	var tmdbID int64
	var tmdbYear int
	var err error
	if isMovie {
		movieImpl := scrape.NewTmdbMovieImpl(nil, ctx)
		officialName, tmdbID, tmdbYear, err = movieImpl.CheckByNameAndYear(name, year, true)
	} else {
		tvImpl := scrape.NewTmdbTvShowImpl(nil, ctx)
		officialName, tmdbID, tmdbYear, err = tvImpl.CheckByNameAndYear(name, year, true)
	}
	if err != nil || tmdbID <= 0 {
		return IdentifyResult{}, false
	}
	res := IdentifyResult{
		Category: "movie",
		Title:    officialName,
		Year:     tmdbYear,
		TmdbId:   tmdbID,
	}
	if tmdbYear <= 0 && year > 0 {
		res.Year = year
	}
	if !isMovie {
		res.Category = "tv"
		res.Season = 1
		res.Episode = 1
		if parsed, ok := mediaparse.ParseEpisode(fileName); ok {
			res.Season = parsed.Season
			res.Episode = parsed.Episode
		}
	}
	helpers.AppLogger.Infof("MoviePilot AI 识别成功：%s → %s（%s，TMDB %d）", fileName, officialName, res.Category, tmdbID)
	return res, true
}
