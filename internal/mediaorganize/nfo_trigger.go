package mediaorganize

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"litepan/internal/settings"
)

// 刮削落盘的触发侧（T14）。
//
// 挂在 applyPlanRunner 尾部、processSubtitlesForPlan 旁边：
// 那已经是「动作全部执行完」的时点，也是唯一一个「整理已经成功了」
// 已经确定的位置。挂早了会出现「给还没搬过去的文件写 NFO」，
// 挂晚了就会被恢复/重启流程吃掉。

// nfoSettings 汇总了落盘要用到的配置。
type nfoSettings struct {
	Enabled   bool
	Cloud     bool
	AccountID int64
	ParentID  string
}

// loadNFOSettings 读刮削落盘配置。
//
// 目标为网盘时需要一个网盘账号和目标目录：没配全就退回本地，
// 而不是把 NFO 丢进一个不存在的目录。
func (s *Service) loadNFOSettings(ctx context.Context, cfg map[string]any, accountID int64) nfoSettings {
	out := nfoSettings{}
	if s.settings == nil {
		return out
	}
	if !s.settings.Bool(settings.KeyMOScrapeNFOEnabled) {
		return out
	}
	out.Enabled = true
	// 只有 cloud 需要目标目录；其余取值一律按本地处理（而不是报错）——
	// 手改过的配置值不该让整个整理任务拒绝执行。
	out.Cloud = strings.EqualFold(strings.TrimSpace(s.settings.String(settings.KeyMOScrapeNFOTarget)), "cloud")
	out.AccountID = accountID
	if dir := strings.TrimSpace(stringFromCfg(cfg, "target_directory_id")); dir != "" {
		out.ParentID = dir
	}
	if !out.Cloud {
		out.AccountID = 0
		out.ParentID = ""
	}
	return out
}

func stringFromCfg(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	raw, ok := cfg[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	return fmt.Sprint(raw)
}

// processNFOMetadata 为整理成功的作品写 NFO 与海报。
//
// 触发条件（缺一不可）：
//   - 装配层注入了落盘器（nil = 刮削落盘未启用）；
//   - 配置打开了 mo_scrape_nfo_enabled；
//   - 动作是真搬运且 Status == "done"。
//
// 单个作品失败只记日志继续下一个：NFO 是元数据，写不进去不该让
// 「文件已经搬到正确位置」这件事在用户眼里变成失败。
func (s *Service) processNFOMetadata(ctx context.Context, taskID string, plan *Plan, cfg map[string]any, accountID int64) {
	if s.nfo == nil || plan == nil || len(plan.Actions) == 0 {
		return
	}
	cfgSettings := s.loadNFOSettings(ctx, cfg, accountID)
	if !cfgSettings.Enabled {
		return
	}
	written := 0
	// 作品级与集级分开去重：同一部剧的第 2 集要落在同一个作品目录里，
	// 如果共用一张表，第 2 集会因为「作品已经写过」被提前跳过，
	// 于是只有第 1 集有 NFO。
	seenWork := map[string]bool{}
	seenEpisode := map[string]bool{}
	seenSeason := map[string]bool{}
	for i := range plan.Actions {
		action := &plan.Actions[i]
		if action.Status != "done" || !isVideoFileName(action.TargetName) {
			continue
		}
		title := strings.TrimSpace(actionTitleFromMeta(action))
		if title == "" {
			continue
		}
		season := mediaSeasonFromAction(action)
		mediaType := mediaTypeFromActionMetadata(action)
		seasonDir, _ := s.targetDirOf(ctx, action, accountID)
		if seasonDir == "" {
			continue
		}
		workDir := seasonDir
		seasonNumber := 0
		if mediaType == MediaTypeTV {
			seasonNumber = season
			if seasonNumber <= 0 {
				continue
			}
			// 剧集的动作目标是「集文件所在目录」，也就是季目录；
			// tvshow.nfo 要写在剧集根目录，所以往上退一层。
			workDir = filepath.Dir(seasonDir)
			if workDir == seasonDir || workDir == "" || workDir == "." || workDir == string(filepath.Separator) {
				workDir = seasonDir
			}
		}
		workKey := fmt.Sprintf("%s|%s|%d", workDir, title, seasonNumber)
		if !seenWork[workKey] {
			seenWork[workKey] = true
			if err := s.writeWorkMetadata(ctx, cfgSettings, accountID, workDir, title, action, mediaType); err != nil {
				s.appendLog(taskID, fmt.Sprintf("[MediaOrganize] 元数据落盘失败: %s (%v)", title, err))
				s.log.Warn("刮削元数据落盘失败", "work", title, "error", err)
				continue
			}
			written++
		}
		if seasonNumber <= 0 {
			continue
		}
		episode := mediaEpisodeFromAction(action)
		episodeKey := ""
		if episode > 0 {
			episodeKey = fmt.Sprintf("%s|S%02dE%02d", seasonDir, seasonNumber, episode)
			if !seenEpisode[episodeKey] {
				seenEpisode[episodeKey] = true
				if err := s.nfo.WriteEpisodeMetadata(ctx, ScrapeEpisodeInput{
					Dir:       seasonDir,
					AccountID: nfoTargetAccount(cfgSettings),
					ParentID:  nfoTargetParent(cfgSettings),
					Title:     episodeTitleFromName(action.TargetName),
					Season:    seasonNumber,
					Episode:   episode,
					TMDBID:    mediaTmdbIDFromAction(action),
					ShowTitle: title,
				}); err != nil {
					s.appendLog(taskID, fmt.Sprintf("[MediaOrganize] 集元数据落盘失败: %s (%v)", title, err))
					s.log.Warn("集元数据落盘失败", "work", title, "episode", episode, "error", err)
				}
			}
		}
		seasonKey := fmt.Sprintf("%s|S%02d", seasonDir, seasonNumber)
		if seenSeason[seasonKey] {
			continue
		}
		seenSeason[seasonKey] = true
		if err := s.nfo.WriteSeasonMetadata(ctx, ScrapeSeasonInput{
			Dir:       seasonDir,
			AccountID: nfoTargetAccount(cfgSettings),
			ParentID:  nfoTargetParent(cfgSettings),
			Title:     title,
			Season:    seasonNumber,
			Premiered: nfoPremiered(mediaYearFromAction(action)),
		}); err != nil {
			s.appendLog(taskID, fmt.Sprintf("[MediaOrganize] 季元数据落盘失败: %s (%v)", title, err))
			s.log.Warn("季元数据落盘失败", "series", title, "season", seasonNumber, "error", err)
		}
	}
	if written > 0 {
		s.appendLog(taskID, fmt.Sprintf("[MediaOrganize] 已写入刮削元数据: %d 部作品", written))
	}
}

// writeWorkMetadata 落一部作品的 NFO 与海报。
//
// 海报来源优先用动作元数据里已经带来的 TMDB 详情（整理时为了命名
// 已经查过一次），没有才回头查一次 —— 一次整理能省掉一次网络往返。
func (s *Service) writeWorkMetadata(ctx context.Context, cfg nfoSettings, accountID int64, dir, title string, action *PlanAction, mediaType string) error {
	detail := tmdbWorkDetail{
		Title:         title,
		OriginalTitle: stringFromMeta(action, "ai_original_title"),
		Year:          mediaYearFromAction(action),
		PosterURL:     stringFromMeta(action, "poster_path"),
		FanartURL:     stringFromMeta(action, "backdrop_path"),
	}
	if detail.PosterURL == "" && s.tmdb != nil {
		if id := mediaTmdbIDFromAction(action); id > 0 {
			if raw, err := s.tmdb.Lookup(ctx, fmt.Sprint(id), mediaType); err == nil {
				if parsed, perr := ParseTMDBWorkDetail(raw); perr == nil {
					if parsed.PosterURL != "" {
						detail.PosterURL = parsed.PosterURL
					}
					if parsed.FanartURL != "" {
						detail.FanartURL = parsed.FanartURL
					}
					if detail.Plot == "" {
						detail.Plot = parsed.Plot
					}
					if detail.OriginalTitle == "" {
						detail.OriginalTitle = parsed.OriginalTitle
					}
				}
			}
		}
	}
	targetAccount, targetParent := nfoTargetAccount(cfg), nfoTargetParent(cfg)
	return s.nfo.WriteWorkMetadata(ctx, ScrapeWorkInput{
		Dir:           dir,
		AccountID:     targetAccount,
		ParentID:      targetParent,
		Title:         detail.Title,
		OriginalTitle: detail.OriginalTitle,
		Year:          detail.Year,
		MediaType:     mediaType,
		TMDBID:        mediaTmdbIDFromAction(action),
		Plot:          detail.Plot,
		PosterURL:     detail.PosterURL,
		FanartURL:     detail.FanartURL,
	})
}

// targetDirOf 算出动作目标所在的本地目录。
//
// 目录已经不在网盘上了也能算：ResolveDirPath 是按路径前缀缓存推出来的，
// 不需要逐层访问驱动。
func (s *Service) targetDirOf(ctx context.Context, action *PlanAction, accountID int64) (string, string) {
	name := strings.TrimSpace(action.TargetName)
	if name == "" {
		name = strings.TrimSpace(action.SourceName)
	}
	if s.files == nil {
		return "", name
	}
	dir, err := s.files.ResolveDirPath(ctx, accountID, action.TargetParentID)
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", name
	}
	return dir, name
}

func actionTitleFromMeta(action *PlanAction) string {
	if action == nil {
		return ""
	}
	if title := strings.TrimSpace(stringFromMeta(action, "title")); title != "" {
		return title
	}
	name := strings.TrimSpace(action.TargetName)
	if name == "" {
		return ""
	}
	stem := strings.TrimSuffix(name, path.Ext(name))
	// 「完美世界 (2019)」这类命名：去掉年份后缀再交给上层判断。
	if idx := strings.LastIndex(stem, " ("); idx > 0 && strings.HasSuffix(stem, ")") {
		if year := strings.Trim(stem[idx+2:], "()"); isYear(year) {
			return strings.TrimSpace(stem[:idx])
		}
	}
	return stem
}

func isYear(text string) bool {
	if len(text) != 4 {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func stringFromMeta(action *PlanAction, key string) string {
	if action == nil || action.Metadata == nil {
		return ""
	}
	raw, ok := action.Metadata[key]
	if !ok || raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

// mediaTypeFromActionMetadata 读动作元数据里的媒体形态。
//
// planner 写的是 media_kind（tv/movie），字幕侧历史上读的是 media_type；
// 这里两个都认，且以 planner 实际写的那个为准 —— 读到空就默认电影。
func mediaTypeFromActionMetadata(action *PlanAction) string {
	if kind := strings.TrimSpace(stringFromMeta(action, "media_kind")); kind != "" {
		switch strings.ToLower(kind) {
		case "tv", "tvshow", "series":
			return MediaTypeTV
		default:
			return MediaTypeMovie
		}
	}
	return normalizeMediaType(mediaTypeFromAction(action))
}

// nfoTargetAccount / nfoTargetParent 决定落盘走本地还是网盘。
//
// 本地时返回 0/空 —— 落盘器据此直接写本地目录，不去碰任何网盘驱动。
func nfoTargetAccount(cfg nfoSettings) int64 {
	if !cfg.Cloud {
		return 0
	}
	return cfg.AccountID
}

func nfoTargetParent(cfg nfoSettings) string {
	if !cfg.Cloud {
		return ""
	}
	return cfg.ParentID
}

// nfoPremiered 把年份转成季级 NFO 用的 premiered 字符串。
func nfoPremiered(year int) string {
	if year <= 0 || year > 9999 {
		return ""
	}
	return fmt.Sprintf("%04d", year)
}

// episodeTitleFromName 从整理后的文件名里取集标题。
//
// 整理命名为「S01E02 标题」这种形态，取第一个空格之后的部分即可；
// 命名里没有标题就返回空 —— 那时只写集号，不编一个假的标题。
func episodeTitleFromName(name string) string {
	stem := strings.TrimSuffix(strings.TrimSpace(name), path.Ext(strings.TrimSpace(name)))
	idx := strings.Index(stem, " ")
	if idx <= 0 || idx+1 >= len(stem) {
		return ""
	}
	return strings.TrimSpace(stem[idx+1:])
}
