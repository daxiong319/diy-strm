package moviepilot

import (
	"context"
	"encoding/json"
	"strings"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/openai"
)

// IdentifyResult AI 辅助识别的媒体信息
type IdentifyResult struct {
	Category string // movie/tv
	Title    string
	Season   int
	Episode  int
	Year     int
	TmdbId   int64
	// AiQuality AI 识别出的质量快照（分辨率/来源/组名/编码，symedia 契约），
	// 文件名解析缺项时用于补齐洗版比较维度
	AiQuality *FileQuality
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
	if row := models.GetAIParseCacheFull(cacheKey); row != nil {
		aiInfo = &openai.MediaInfoAI{Name: row.ResultName, Year: row.ResultYear}
		// 旧缓存只有名称/年份；新缓存恢复完整结构（季集/质量维度）
		if row.ResultJSON != "" {
			var full openai.MediaInfoAI
			if err := json.Unmarshal([]byte(row.ResultJSON), &full); err == nil && strings.TrimSpace(full.Name) != "" {
				aiInfo = &full
			}
		}
		helpers.AppLogger.Infof("AI 识别缓存命中：%s → %s (%d)", aiInput, aiInfo.Name, aiInfo.Year)
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
		models.SaveAIParseCache(cacheKey, aiInput, aiInfo.Name, aiInfo.Year, models.MarshalAIParseResult(aiInfo))
	}
	// 识别结果结构化日志（对齐 symedia「ChatGPT 辅助识别」输出，便于排查与洗版归因）
	helpers.AppLogger.Infof("AI 识别结果：%s → %s（type=%s year=%d S%02dE%02d %s %s %s %s %s）",
		aiInput, aiInfo.Name, orDash(aiInfo.MediaType), aiInfo.Year, aiInfo.Season, aiInfo.Episode,
		orDash(aiInfo.Resolution), orDash(aiInfo.Source), orDash(aiInfo.VideoCodec), orDash(aiInfo.AudioCodec), orDash(aiInfo.ReleaseGroup))
	// TMDB 校验：按 AI 判定的类型优先（未判定则先电影），另一类型兜底；
	// 再试去空格变体（AI 可能返回「遮 天」）
	verifyNames := []string{aiInfo.Name}
	if noSpace := strings.ReplaceAll(strings.TrimSpace(aiInfo.Name), " ", ""); noSpace != "" && noSpace != strings.TrimSpace(aiInfo.Name) {
		verifyNames = append(verifyNames, noSpace)
	}
	tryMovieFirst := aiInfo.MediaType != "tv"
	for _, verifyName := range verifyNames {
		var order []bool // true=电影 false=剧集
		if tryMovieFirst {
			order = []bool{true, false}
		} else {
			order = []bool{false, true}
		}
		for _, isMovie := range order {
			if res, ok := verifyIdentifyByTmdb(ctx, fileName, verifyName, aiInfo.Year, isMovie); ok {
				applyAiExtras(&res, aiInfo)
				return res, true
			}
		}
	}
	// 校验失败：删缓存（可能是坏缓存），未命中缓存的也无需保留失败结果
	models.DeleteAIParseCache(cacheKey)
	helpers.AppLogger.Warnf("AI 识别结果未通过 TMDB 校验（%s → %s %d）", aiInput, aiInfo.Name, aiInfo.Year)
	return IdentifyResult{}, false
}

// applyAiExtras 把 AI 识别的季集/类型/质量快照合并进校验结果
// （文件名解析缺项时以 AI 为准；文件名已明确解析出的值不覆盖）。
func applyAiExtras(res *IdentifyResult, aiInfo *openai.MediaInfoAI) {
	if res.Category == "tv" {
		if aiInfo.Season > 0 && res.Season <= 1 {
			res.Season = aiInfo.Season
		}
		if aiInfo.Episode > 0 && res.Episode <= 0 {
			res.Episode = aiInfo.Episode
		}
	}
	res.AiQuality = aiQualityFromFile(aiInfo)
}

// orDash 空串显示为「-」（结构化日志用）
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return strings.TrimSpace(s)
}

// aiQualityFromFile AI 识别的质量字段 → 文件质量快照（洗版比较维度）。
// 全部为空时返回 nil（不产生误导性的空快照）。
func aiQualityFromFile(ai *openai.MediaInfoAI) *FileQuality {
	if ai == nil {
		return nil
	}
	q := &FileQuality{}
	if r := strings.ToLower(strings.TrimSpace(ai.Resolution)); r != "" {
		q.ResTag = r
		switch {
		case strings.Contains(r, "2160"), strings.Contains(r, "4k"), strings.Contains(r, "uhd"):
			q.Resolution = 2160
		case strings.Contains(r, "1440"):
			q.Resolution = 1440
		case strings.Contains(r, "1080"):
			q.Resolution = 1080
		case strings.Contains(r, "720"):
			q.Resolution = 720
		case strings.Contains(r, "576"), strings.Contains(r, "540"):
			q.Resolution = 576
		case strings.Contains(r, "480"):
			q.Resolution = 480
		}
	}
	if s := strings.ToLower(strings.TrimSpace(ai.Source)); s != "" {
		q.VideoFormat = s
	}
	if g := strings.TrimSpace(ai.ReleaseGroup); g != "" {
		q.Group = g
	}
	if c := strings.ToLower(strings.TrimSpace(ai.VideoCodec)); c != "" {
		switch {
		case strings.Contains(c, "265"), strings.Contains(c, "hevc"):
			q.Codec = "h265"
			q.CodecTag = "H265"
		case strings.Contains(c, "264"), strings.Contains(c, "avc"):
			q.Codec = "h264"
			q.CodecTag = "H264"
		case strings.Contains(c, "av1"):
			q.Codec = "av1"
			q.CodecTag = "AV1"
		}
	}
	if a := strings.TrimSpace(ai.AudioCodec); a != "" {
		q.AudioTag = strings.ToUpper(a)
		q.Channels = audioChannels(a)
	}
	if q.Resolution == 0 && q.VideoFormat == "" && q.Group == "" && q.Codec == "" && q.AudioTag == "" {
		return nil
	}
	return q
}

// verifyIdentifyByTmdb 用 TMDB 校验 AI 识别的名称与年份，命中则返回规范化媒体信息。
// isMovie=true 时按电影查询，否则按剧集查询（季集从文件名补齐，缺省 1）。
// 校验统一走多候选评分匹配：年份只做辅助评分而非硬过滤——AI 给出的年份常是
// 年番/续季的播出年（如 遮天年番 2026），与 TMDB 首播年（2023）不同，
// 带年过滤会搜空导致 AI 兜底整体失效。
func verifyIdentifyByTmdb(ctx context.Context, fileName, name string, year int, isMovie bool) (IdentifyResult, bool) {
	mediaType := "tv"
	if isMovie {
		mediaType = "movie"
	}
	best, err := matchTmdbCandidates(ctx, name, year, mediaType)
	if err != nil || best == nil || best.ID <= 0 {
		return IdentifyResult{}, false
	}
	officialName := best.Name
	if officialName == "" {
		officialName = best.OrigName
	}
	tmdbYear := best.Year
	res := IdentifyResult{
		Category: "movie",
		Title:    officialName,
		Year:     tmdbYear,
		TmdbId:   best.ID,
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
	helpers.AppLogger.Infof("AI 识别成功：%s → %s（%s，TMDB %d）", fileName, officialName, res.Category, best.ID)
	return res, true
}
