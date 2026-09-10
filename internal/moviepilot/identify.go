package moviepilot

import (
	"context"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/openai"
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
	return IdentifyFileWithAIContext(ctx, "", fileName)
}

// IdentifyFileWithAIContext 同 IdentifyFileWithAI，但允许提供目录名作为识别
// 上下文（文件名常不含标题，如「S01E08.2026.2160p.mp4」，标题在目录名里）。
// hintName 非空时 AI 输入为「目录名 + 文件名」，并对去空格变体做 TMDB 校验。
func IdentifyFileWithAIContext(ctx context.Context, hintName, fileName string) (IdentifyResult, bool) {
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
	aiInput := fileName
	hint := strings.TrimSpace(hintName)
	if hint != "" {
		aiInput = hint + " " + fileName
	}
	// AI 识别缓存（借鉴 tgto123 exact 缓存）：相同输入不重复调用 AI；
	// 命中后仍走下方 TMDB 校验，校验失败删除缓存条目避免坏缓存
	cacheKey := models.AICacheKey(hint, fileName)
	var aiInfo *openai.MediaInfoAI
	if name, year, ok := models.GetAIParseCache(cacheKey); ok {
		helpers.AppLogger.Infof("AI 识别缓存命中：%s → %s (%d)", aiInput, name, year)
		aiInfo = &openai.MediaInfoAI{Name: name, Year: year}
	} else {
		res, err := client.TakeMoiveName(aiInput, settings.GetAiPrompt())
		if err != nil {
			helpers.AppLogger.Warnf("AI 识别文件名失败（%s）：%v", aiInput, err)
			return IdentifyResult{}, false
		}
		if res == nil || strings.TrimSpace(res.Name) == "" {
			return IdentifyResult{}, false
		}
		aiInfo = res
		models.SaveAIParseCache(cacheKey, aiInput, aiInfo.Name, aiInfo.Year)
	}
	// TMDB 校验：优先电影，其次剧集；再试去空格变体（AI 可能返回「遮 天」）
	verifyNames := []string{aiInfo.Name}
	if noSpace := strings.ReplaceAll(strings.TrimSpace(aiInfo.Name), " ", ""); noSpace != "" && noSpace != strings.TrimSpace(aiInfo.Name) {
		verifyNames = append(verifyNames, noSpace)
	}
	for _, verifyName := range verifyNames {
		if res, ok := verifyIdentifyByTmdb(ctx, fileName, verifyName, aiInfo.Year, true); ok {
			return res, true
		}
		if res, ok := verifyIdentifyByTmdb(ctx, fileName, verifyName, aiInfo.Year, false); ok {
			return res, true
		}
	}
	// 校验失败：删缓存（可能是坏缓存），未命中缓存的也无需保留失败结果
	models.DeleteAIParseCache(cacheKey)
	helpers.AppLogger.Warnf("AI 识别结果未通过 TMDB 校验（%s → %s %d）", aiInput, aiInfo.Name, aiInfo.Year)
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
	helpers.AppLogger.Infof("AI 识别成功：%s → %s（%s，TMDB %d）", fileName, officialName, res.Category, tmdbID)
	return res, true
}
