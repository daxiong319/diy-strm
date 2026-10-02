package moviepilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"litepan/internal/discover/mediaparse"
	"litepan/internal/domain"
)

// ListFailedFiles 分页查询失败文件。
func (s *Service) ListFailedFiles(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotFailedFile, int64, error) {
	if s.repo == nil {
		return nil, 0, nil
	}
	return s.repo.ListFailedFiles(ctx, page, pageSize, status)
}

// ResolveFailedFile 手工指定媒体信息后重新整理一条失败文件：
// 该文件会走正常的识别结果覆盖 → 规划 → 上传流程。
// mediaType 为空时按 movie 处理；title 必填；tmdbID 必填（无法按 ID 定位则整理无从下手）。
func (s *Service) ResolveFailedFile(ctx context.Context, id int64, mediaType, title string, year, season int, tmdbID int64) (*OrganizeTarget, error) {
	if s.repo == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	rec, err := s.repo.GetFailedFile(ctx, id)
	if err != nil {
		return nil, domain.Wrap(domain.CodeNotFound, err)
	}
	if rec.Status == domain.MoviePilotFailedResolved {
		return nil, domain.Errorf(domain.CodeValidation, "该文件已整理完成")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, domain.Errorf(domain.CodeValidation, "请填写媒体标题")
	}
	if tmdbID <= 0 {
		return nil, domain.Errorf(domain.CodeValidation, "请填写 TMDB ID")
	}
	if mediaType == "" {
		mediaType = "movie"
	}
	if mediaType != "movie" && mediaType != "tv" {
		mediaType = "movie"
	}
	srcPath := filepath.Join(rec.RootPath, rec.FileName)
	if !pathExists(srcPath) {
		return nil, domain.Errorf(domain.CodeNotFound, "本地已找不到文件 %s（可能已被移动或删除）", rec.FileName)
	}
	media := &IdentifyResult{
		Category: mediaType,
		Title:    title,
		Year:     year,
		Season:   season,
		Episode:  1,
		TmdbId:   tmdbID,
	}
	// 文件名里的季集信息比手工填写更精确，优先采用
	if parsed, ok := mediaparse.ParseEpisode(rec.FileName); ok {
		if parsed.Season > 0 {
			media.Season = parsed.Season
		}
		if parsed.Episode > 0 {
			media.Episode = parsed.Episode
		}
	}
	if media.Category == "tv" && media.Season <= 0 {
		media.Season = 1
	}
	official, tmdbYear := s.lookupTmdbByID(ctx, tmdbID, media.Category)
	if official != "" {
		media.Title = official
	}
	if tmdbYear > 0 {
		media.Year = tmdbYear
	}
	relDir, ok := BuildOrganizeRelDir(media.Category, media.Title, media.Year, media.Season, media.TmdbId, s.categoryName())
	if !ok {
		return nil, domain.Errorf(domain.CodeValidation, "媒体信息不完整，无法构建目标目录")
	}
	ext := filepath.Ext(rec.FileName)
	baseName := BuildOrganizeNewName(media.Category, media.Title, media.Season, media.Episode, media.Year, ext)
	tags := ExtractQualityTags(rec.FileName, media.Title)
	newName := AppendQualityTagsToName(media.Category, media.Title, media.Season, media.Episode, media.Year, ext, tags)
	if strings.TrimSpace(newName) == "" || newName == ext {
		newName = baseName
	}
	return &OrganizeTarget{
		SourcePath: srcPath,
		RelDir:     relDir,
		NewName:    newName,
		Media:      media,
		Quality:    ParseQualityFromName(rec.FileName),
	}, nil
}

// SkipFailedFile 忽略一条失败文件（不再自动重试，保留记录供查阅）。
func (s *Service) SkipFailedFile(ctx context.Context, id int64) error {
	return s.transitionFailedFile(ctx, id, domain.MoviePilotFailedSkipped, "")
}

// MarkFailedFileResolved 标记失败文件已整理完成。
func (s *Service) MarkFailedFileResolved(ctx context.Context, id int64) error {
	return s.transitionFailedFile(ctx, id, domain.MoviePilotFailedResolved, "")
}

// ReopenFailedFile 把已忽略/已完成之外的失败文件重新置为待处理（供「重新识别」入口使用）。
func (s *Service) ReopenFailedFile(ctx context.Context, id int64) error {
	return s.transitionFailedFile(ctx, id, domain.MoviePilotFailedPending, "")
}

// transitionFailedFile 更新失败文件状态（可选覆盖原因）。
func (s *Service) transitionFailedFile(ctx context.Context, id int64, status, reason string) error {
	if s.repo == nil {
		return domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	rec, err := s.repo.GetFailedFile(ctx, id)
	if err != nil {
		return domain.Wrap(domain.CodeNotFound, err)
	}
	rec.Status = status
	if strings.TrimSpace(reason) != "" {
		rec.Reason = reason
	}
	if err := s.repo.UpdateFailedFile(ctx, rec); err != nil {
		return domain.Wrap(domain.CodeInternal, err)
	}
	return nil
}

// IdentifyFailedFile 对失败文件重新做一次识别（不落盘、不上传），
// 返回识别结果供前端展示或确认后再整理。
func (s *Service) IdentifyFailedFile(ctx context.Context, id int64) (*IdentifyResult, error) {
	if s.repo == nil {
		return nil, domain.Errorf(domain.CodeNotImplement, "MoviePilot 服务未配置")
	}
	rec, err := s.repo.GetFailedFile(ctx, id)
	if err != nil {
		return nil, domain.Wrap(domain.CodeNotFound, err)
	}
	srcPath := filepath.Join(rec.RootPath, rec.FileName)
	if !pathExists(srcPath) {
		return nil, domain.Errorf(domain.CodeNotFound, "本地已找不到文件 %s（可能已被移动或删除）", rec.FileName)
	}
	cfg, cfgErr := s.repo.LoadConfig(ctx)
	var client *Client
	if cfgErr == nil && cfg != nil {
		client = NewClient(cfg.BaseUrl, cfg.ApiToken)
	}
	media, ok := s.IdentifyLocalFile(ctx, client, rec.FileName, filepath.Base(rec.RootPath))
	if !ok || media == nil {
		return nil, domain.Errorf(domain.CodeValidation, "文件名无法识别")
	}
	// 识别成功即回填记录，减少人工重复劳动
	rec.MediaType = media.Category
	rec.Title = media.Title
	rec.TmdbId = media.TmdbId
	rec.Year = media.Year
	rec.Season = media.Season
	if uErr := s.repo.UpdateFailedFile(ctx, rec); uErr != nil {
		s.log.Warn("回填失败文件识别结果失败", "id", id, "err", uErr)
	}
	return media, nil
}

// failedFileSourcePath 组合失败文件的本地绝对路径。
func failedFileSourcePath(rec *domain.MoviePilotFailedFile) string {
	if rec == nil {
		return ""
	}
	return filepath.Join(rec.RootPath, rec.FileName)
}

// fileExists 判断本地文件是否存在且为普通文件。
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
