package subtitle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MediaMeta 是检索所需的媒体元数据。
//
// 老版直接传 *models.Media；LitePan 没有统一的媒体模型（媒体信息分散在各服务里），
// 因此改为传值结构体，由调用方从自己的数据源填充。
type MediaMeta struct {
	Title         string
	OriginalTitle string
	Year          int
	Season        int
	Episode       int
	MediaType     string
	TmdbId        int64
	ImdbId        string
	VideoFileName string
}

// Service 是字幕模块的门面：持有配置、来源与任务仓储。
type Service struct {
	mu        sync.RWMutex
	cfg       Config
	providers []Provider
	log       Logger
	tasks     *TaskStore
	reader    ConfigReader
}

// NewService 用配置读取器构造服务并加载一次配置。
// reader 可为 nil（此时用默认配置）。
func NewService(reader ConfigReader, log Logger) *Service {
	if log == nil {
		log = nopLogger{}
	}
	s := &Service{log: log, reader: reader}
	s.Reload()
	return s
}

// SetTaskStore 注入任务仓储（可为 nil）。
func (s *Service) SetTaskStore(store *TaskStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = store
}

// Reload 重新读取配置并重建来源。
// 设置保存后必须调用，否则新填的凭证要等重启才生效。
func (s *Service) Reload() {
	cfg := LoadConfig(s.reader)
	providers, err := buildEnabledProviders(&cfg, s.log)
	if err != nil {
		// 构建期错误（如代理地址非法）只记日志：已成功的来源照常可用。
		s.log.Warnf("部分字幕源构建失败：%v", err)
	}

	s.mu.Lock()
	s.cfg = cfg
	s.providers = providers
	s.mu.Unlock()
}

// Config 返回当前配置快照。
func (s *Service) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Providers 返回当前已启用的来源。
func (s *Service) Providers() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Provider(nil), s.providers...)
}

// FindProvider 按名字找一个已启用的来源。
func (s *Service) FindProvider(name string) Provider {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	for _, p := range s.Providers() {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// ProviderStatuses 汇总各来源的启用/配置/健康状态。
func (s *Service) ProviderStatuses(ctx context.Context) []ProviderStatus {
	cfg := s.Config()
	out := make([]ProviderStatus, 0, 4)
	for _, spec := range providerSpecs(&cfg) {
		status := ProviderStatus{
			Name:        spec.name,
			DisplayName: displayProviderName(spec.name),
			Enabled:     spec.enabled,
			Configured:  !spec.requiresKey || strings.TrimSpace(spec.credential) != "",
		}
		switch {
		case !spec.enabled:
			status.Message = "未启用"
		case spec.requiresKey && status.Configured == false:
			status.Message = "已启用但缺少必需凭证"
		default:
			start := time.Now()
			p := s.FindProvider(spec.name)
			if p == nil {
				status.Message = "来源不可用"
				break
			}
			checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := p.HealthCheck(checkCtx)
			cancel()
			status.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				status.Message = err.Error()
			} else {
				status.Healthy = true
				status.Message = "连接正常"
			}
		}
		out = append(out, status)
	}
	return out
}

// displayProviderName 给出中文来源名（无需构造 provider 实例即可展示）。
func displayProviderName(name string) string {
	switch name {
	case "assrt":
		return "射手网(assrt)"
	case "opensubtitles":
		return "OpenSubtitles"
	case "subhd":
		return "SubHD"
	case "zimuku":
		return "字幕库(zimuku)"
	default:
		return name
	}
}

// ProviderError 记录单个来源的检索失败。
type ProviderError struct {
	Provider string `json:"provider"`
	Message  string `json:"message"`
}

// SearchAll 并发向全部已启用来源检索并统一打分。
// 单个来源失败只记录到 providerErrors，不影响其它来源。
func (s *Service) SearchAll(ctx context.Context, req SearchRequest) ([]ScoredCandidate, []ProviderError) {
	providers := s.Providers()
	if len(providers) == 0 {
		return nil, []ProviderError{{Provider: "", Message: "没有已启用的字幕源，请检查来源开关与凭证"}}
	}

	type result struct {
		name       string
		candidates []Candidate
		err        error
	}
	ch := make(chan result, len(providers))
	for _, p := range providers {
		go func(p Provider) {
			candidates, err := p.Search(ctx, req)
			ch <- result{name: p.Name(), candidates: candidates, err: err}
		}(p)
	}

	var (
		all  []Candidate
		errs []ProviderError
	)
	for i := 0; i < len(providers); i++ {
		r := <-ch
		if r.err != nil {
			errs = append(errs, ProviderError{Provider: r.name, Message: r.err.Error()})
			continue
		}
		all = append(all, r.candidates...)
	}

	cfg := s.Config()
	opts := BuildMatchOptions(req, cfg)
	scored := ScoreCandidates(dedupeCandidates(all), opts)
	return scored, errs
}

// BuildMatchOptions 从检索请求与配置派生打分参数。
func BuildMatchOptions(req SearchRequest, cfg Config) MatchOptions {
	return MatchOptions{
		Title:            req.Title,
		OriginalTitle:    req.OriginalTitle,
		Year:             req.Year,
		Season:           req.Season,
		Episode:          req.Episode,
		MediaType:        req.MediaType,
		VideoFileName:    firstNonEmpty(req.VideoFileName, filepath.Base(req.VideoPath)),
		VideoHash:        req.VideoHash,
		LanguagePriority: splitList(cfg.LanguagePriority),
		FormatPriority:   splitList(cfg.FormatPriority),
		Weights:          DefaultScoreWeights(),
	}
}

// splitList 拆分逗号分隔的优先级列表。
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Search 检索并返回分数不低于 MinMatchScore 的候选。
func (s *Service) Search(ctx context.Context, req SearchRequest) ([]ScoredCandidate, []ProviderError, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return nil, nil, errors.New("字幕模块未启用")
	}
	scored, errs := s.SearchAll(ctx, req)
	if cfg.MinMatchScore > 0 {
		filtered := scored[:0]
		for _, item := range scored {
			if item.Score >= cfg.MinMatchScore {
				filtered = append(filtered, item)
			}
		}
		scored = filtered
	}
	return scored, errs, nil
}

// BuildSearchRequest 把显式字段、视频路径解析、媒体元数据合并成检索请求。
// 优先级：显式字段 > 视频路径解析 > 媒体库元数据。
// "填了就以填的为准"，避免自动解析覆盖手工纠正过的标题。
func BuildSearchRequest(videoPath string, meta MediaMeta) SearchRequest {
	out := SearchRequest{
		Title:         strings.TrimSpace(meta.Title),
		OriginalTitle: strings.TrimSpace(meta.OriginalTitle),
		Year:          meta.Year,
		Season:        meta.Season,
		Episode:       meta.Episode,
		MediaType:     strings.TrimSpace(meta.MediaType),
		TmdbId:        meta.TmdbId,
		ImdbId:        strings.TrimSpace(meta.ImdbId),
		VideoPath:     strings.TrimSpace(videoPath),
	}

	if out.VideoPath != "" {
		parsed := ParseVideoPath(out.VideoPath)
		if out.Title == "" {
			out.Title = parsed.Title
		}
		if out.OriginalTitle == "" {
			out.OriginalTitle = parsed.OriginalTitle
		}
		if out.Year == 0 {
			out.Year = parsed.Year
		}
		if out.Season == 0 {
			out.Season = parsed.Season
		}
		if out.Episode == 0 {
			out.Episode = parsed.Episode
		}
		if out.MediaType == "" {
			out.MediaType = parsed.MediaType
		}
	}

	if out.VideoFileName == "" {
		out.VideoFileName = firstNonEmpty(meta.VideoFileName, filepath.Base(out.VideoPath))
	}
	if out.MediaType == "" {
		out.MediaType = "movie"
	}
	return out
}

// ParsedVideoName 是视频文件名解析结果。
type ParsedVideoName struct {
	Title         string
	OriginalTitle string
	Year          int
	Season        int
	Episode       int
	MediaType     string
	ReleaseGroup  string
}

var (
	titleYearPattern = regexp.MustCompile(`^(.*?)[\s._\-]*[\(\[]?((?:19|20)\d{2})[\)\]]?[\s._\-]*(.*)$`)
	resolutionCut    = regexp.MustCompile(`(?i)[\s._\-]+(?:2160p|1080p|720p|480p|4k|8k)\b.*$`)
	releaseCut       = regexp.MustCompile(`(?i)[\s._\-]+(?:bluray|blu-ray|bdrip|brrip|webrip|web-dl|webdl|hdtv|dvdrip|remux|hdrip)\b.*$`)
)

// ParseVideoPath 从视频文件名解析标题、年份与季集。
func ParseVideoPath(videoPath string) ParsedVideoName {
	out := ParsedVideoName{MediaType: "movie"}
	base := filepath.Base(videoPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return out
	}

	if season, episode, found := parseSeasonEpisode(base); found {
		out.Season = season
		out.Episode = episode
		if season > 0 || episode > 0 {
			out.MediaType = "tvshow"
		}
	}

	work := base
	if match := resolutionCut.FindStringIndex(work); match != nil {
		work = work[:match[0]]
	}
	if match := releaseCut.FindStringIndex(work); match != nil {
		work = work[:match[0]]
	}

	// 年份：取最后一个四位数年份，之后的内容（分辨率/编码/组名）丢弃。
	if m := titleYearPattern.FindStringSubmatch(work); len(m) == 4 {
		out.Year = atoiSafe(m[2])
		work = m[1]
	} else if m := yearPattern.FindStringSubmatch(work); len(m) == 2 {
		if idx := strings.Index(work, m[1]); idx > 0 {
			out.Year = atoiSafe(m[1])
			work = work[:idx]
		}
	}

	title := strings.Trim(strings.TrimSpace(work), ".-_ ")
	title = strings.Join(strings.FieldsFunc(title, func(r rune) bool {
		return r == '.' || r == '_'
	}), " ")
	title = strings.TrimSpace(title)
	out.Title = title
	out.OriginalTitle = title

	// 组名若还在尾部则剥掉。
	if idx := strings.LastIndex(title, "-"); idx > 0 && idx < len(title)-1 {
		group := title[idx+1:]
		if len(group) <= 24 && !strings.ContainsAny(group, " /\\") && !containsVersionKeyword(strings.ToLower(group)) {
			out.ReleaseGroup = group
			out.Title = strings.TrimSpace(title[:idx])
			out.OriginalTitle = out.Title
		}
	}
	return out
}

// DownloadRequest 是一次下载的输入。
type DownloadRequest struct {
	Provider     string
	Candidate    Candidate
	VideoPath    string
	SubtitlePath string
	Format       SubtitleFormat
	Overwrite    bool
	AutoSync     bool
}

// Download 下载候选字幕并写入目标路径。
// 校正失败不回滚已下载的字幕：有"未校正但有字幕"远好于什么都没有。
func (s *Service) Download(ctx context.Context, req DownloadRequest) (*Task, error) {
	cfg := s.Config()
	provider := s.FindProvider(req.Provider)
	if provider == nil {
		return nil, fmt.Errorf("字幕源 %s 未启用或凭证不完整", req.Provider)
	}

	destPath := strings.TrimSpace(req.SubtitlePath)
	if destPath == "" {
		if !FileExists(req.VideoPath) {
			return nil, errors.New("视频文件不可访问：" + req.VideoPath)
		}
		format := req.Format
		if format == "" {
			format = DetectFormat(req.Candidate.Title)
		}
		if format == "" {
			format = FormatSRT
		}
		destPath = SubtitleFileNameForVideo(req.VideoPath, format, cfg.TargetDirPolicy)
	}
	if FileExists(destPath) && !req.Overwrite && !cfg.Overwrite {
		return nil, fmt.Errorf("目标字幕已存在：%s（如需覆盖请勾选覆盖）", destPath)
	}

	start := time.Now()
	task := &Task{
		VideoPath:     req.VideoPath,
		SubtitlePath:  destPath,
		Status:        TaskStatusDownloading,
		Title:         req.Candidate.Title,
		Provider:      req.Provider,
		CandidateSlug: req.Candidate.Slug,
	}
	if raw, err := json.Marshal(req.Candidate); err == nil {
		task.CandidateJSON = string(raw)
	} else {
		s.log.Warnf("序列化字幕候选失败：%v", err)
	}
	s.saveTaskQuietly(ctx, task)

	if err := provider.Download(ctx, req.Candidate, destPath); err != nil {
		task.Status = TaskStatusFailed
		task.ErrorMessage = err.Error()
		task.DurationMs = time.Since(start).Milliseconds()
		s.saveTaskQuietly(ctx, task)
		return task, fmt.Errorf("下载字幕失败：%w", err)
	}

	task.Status = TaskStatusDone
	task.SubtitlePath = destPath

	if req.AutoSync || cfg.AutoSync {
		opts := SyncOptionsFromConfig(cfg)
		result, syncErr := s.RunSync(ctx, req.VideoPath, destPath, opts)
		if syncErr != nil {
			task.ErrorMessage = "时间轴校正失败：" + syncErr.Error()
			s.log.Warnf("下载后自动校正字幕失败：%v", syncErr)
		} else {
			applySyncResultToTask(task, result)
		}
	}

	task.DurationMs = time.Since(start).Milliseconds()
	s.saveTaskQuietly(ctx, task)
	return task, nil
}

// SyncOptionsFromConfig 从配置派生校正选项。
func SyncOptionsFromConfig(cfg Config) SyncOptions {
	return SyncOptions{
		Mode:          SyncMode(cfg.SyncMode),
		DryRun:        cfg.SyncDryRun,
		MinConfidence: cfg.SyncMinConfidence,
		KeepOriginal:  cfg.KeepOriginal,
	}
}

// RunSync 执行一次时间轴校正，并按配置放宽超时。
// 校正要解码整条音轨，按请求超时倍数放宽，避免长片必然超时。
func (s *Service) RunSync(ctx context.Context, videoPath, subtitlePath string, opts SyncOptions) (*SyncResult, error) {
	cfg := s.Config()
	if opts.Mode == "" {
		opts.Mode = SyncModeVAD
	}
	if opts.Timeout <= 0 {
		seconds := cfg.TimeoutSeconds
		if seconds <= 0 {
			seconds = 30
		}
		opts.Timeout = time.Duration(seconds) * 20 * time.Second
	}
	return SyncSubtitle(ctx, videoPath, subtitlePath, opts)
}

// applySyncResultToTask 把校正结果写进任务记录。
func applySyncResultToTask(task *Task, result *SyncResult) {
	if task == nil || result == nil {
		return
	}
	task.SyncOffsetMs = result.OffsetMs
	task.SyncScale = result.Scale
	task.SyncConfidence = result.Confidence
	task.SyncApplied = result.Applied
}

// saveTaskQuietly 保存任务；失败只记日志。
// 任务记录是辅助信息，保存失败不该把"字幕已经下载成功"变成失败响应。
func (s *Service) saveTaskQuietly(ctx context.Context, task *Task) {
	if s.tasks == nil || task == nil {
		return
	}
	var err error
	if task.ID == 0 {
		err = s.tasks.CreateTask(ctx, task)
	} else {
		err = s.tasks.SaveTask(ctx, task)
	}
	if err != nil {
		s.log.Errorf("保存字幕任务失败：%v", err)
	}
}

// ProcessVideo 是整理流程的入口：为单个视频自动检索、挑选并下载字幕。
// force 为 true 时即使已有字幕也重新处理。
func (s *Service) ProcessVideo(ctx context.Context, videoPath string, meta MediaMeta, force bool) (*Task, error) {
	cfg := s.Config()
	if !cfg.Enabled {
		return nil, errors.New("字幕模块未启用")
	}
	if !FileExists(videoPath) {
		return nil, errors.New("视频文件不存在：" + videoPath)
	}

	if existing := FindSubtitleForVideo(videoPath, cfg.TargetDirPolicy); existing != "" && !force {
		return &Task{
			VideoPath:    videoPath,
			SubtitlePath: existing,
			Status:       TaskStatusSkipped,
			ErrorMessage: "已存在同名字幕，跳过",
		}, nil
	}

	searchReq := BuildSearchRequest(videoPath, meta)
	searchReq.VideoFileName = filepath.Base(videoPath)
	if hash, err := ComputeOpenSubtitlesHash(videoPath); err == nil {
		searchReq.VideoHash = hash
	}

	start := time.Now()
	task := &Task{
		MediaID:   0,
		VideoPath: videoPath,
		Status:    TaskStatusSearching,
		Title:     searchReq.Title,
		Year:      searchReq.Year,
		Season:    searchReq.Season,
		Episode:   searchReq.Episode,
		MediaType: searchReq.MediaType,
	}
	if prev, ok := s.findTask(ctx, videoPath); ok && prev.ID != 0 {
		task.ID = prev.ID
	}
	s.saveTaskQuietly(ctx, task)

	scored, providerErrors := s.SearchAll(ctx, searchReq)
	if len(scored) == 0 {
		task.Status = TaskStatusFailed
		task.DurationMs = time.Since(start).Milliseconds()
		if len(providerErrors) > 0 {
			msgs := make([]string, 0, len(providerErrors))
			for _, pe := range providerErrors {
				msgs = append(msgs, pe.Provider+": "+pe.Message)
			}
			task.ErrorMessage = "所有字幕源都没有结果：" + strings.Join(msgs, "; ")
		} else {
			task.ErrorMessage = "没有找到匹配的字幕"
		}
		s.saveTaskQuietly(ctx, task)
		return task, errors.New(task.ErrorMessage)
	}

	best := scored[0]
	task.Status = TaskStatusMatching
	task.Provider = best.Candidate.Provider
	task.CandidateSlug = best.Candidate.Slug
	task.MatchScore = best.Score
	task.MatchReason = strings.Join(best.Reasons, "; ")
	if raw, err := json.Marshal(best.Candidate); err == nil {
		task.CandidateJSON = string(raw)
	}
	s.saveTaskQuietly(ctx, task)

	if !cfg.AutoDownload {
		task.Status = TaskStatusSkipped
		task.ErrorMessage = "已匹配但未开启自动下载"
		task.DurationMs = time.Since(start).Milliseconds()
		s.saveTaskQuietly(ctx, task)
		return task, nil
	}
	if cfg.MinMatchScore > 0 && best.Score < cfg.MinMatchScore {
		task.Status = TaskStatusSkipped
		task.ErrorMessage = fmt.Sprintf("最高分 %d 低于阈值 %d，已跳过", best.Score, cfg.MinMatchScore)
		task.DurationMs = time.Since(start).Milliseconds()
		s.saveTaskQuietly(ctx, task)
		return task, nil
	}

	format := best.Candidate.Format
	if format == "" {
		format = FormatSRT
	}
	destPath := SubtitleFileNameForVideo(videoPath, format, cfg.TargetDirPolicy)
	downloaded, err := s.Download(ctx, DownloadRequest{
		Provider:     best.Candidate.Provider,
		Candidate:    best.Candidate,
		VideoPath:    videoPath,
		SubtitlePath: destPath,
		Format:       format,
		Overwrite:    cfg.Overwrite,
	})
	if err != nil {
		return downloaded, err
	}
	// 保留检索阶段的打分信息（Download 会重建 task）。
	downloaded.MatchScore = best.Score
	downloaded.MatchReason = task.MatchReason
	downloaded.Title = searchReq.Title
	downloaded.Year = searchReq.Year
	downloaded.Season = searchReq.Season
	downloaded.Episode = searchReq.Episode
	downloaded.MediaType = searchReq.MediaType
	s.saveTaskQuietly(ctx, downloaded)
	return downloaded, nil
}

// findTask 取某视频最近一条任务。
func (s *Service) findTask(ctx context.Context, videoPath string) (*Task, bool) {
	if s.tasks == nil {
		return nil, false
	}
	return s.tasks.FindTaskByVideoPath(ctx, videoPath)
}

// RetryTask 按任务记录重跑一次下载。
func (s *Service) RetryTask(ctx context.Context, id uint) (*Task, error) {
	if s.tasks == nil {
		return nil, ErrStoreUnavailable
	}
	task, ok := s.tasks.GetTask(ctx, id)
	if !ok {
		return nil, errors.New("字幕任务不存在")
	}
	var candidate Candidate
	if task.CandidateJSON != "" {
		if err := json.Unmarshal([]byte(task.CandidateJSON), &candidate); err != nil {
			s.log.Warnf("解析字幕任务候选快照失败：%v", err)
		}
	}
	if candidate.Slug == "" {
		// 没有候选快照时退回完整流程。
		return s.ProcessVideo(ctx, task.VideoPath, MediaMeta{
			Title:     task.Title,
			Year:      task.Year,
			Season:    task.Season,
			Episode:   task.Episode,
			MediaType: task.MediaType,
		}, true)
	}

	cfg := s.Config()
	format := candidate.Format
	if format == "" {
		format = FormatSRT
	}
	destPath := strings.TrimSpace(task.SubtitlePath)
	if destPath == "" {
		destPath = SubtitleFileNameForVideo(task.VideoPath, format, cfg.TargetDirPolicy)
	}
	updated, err := s.Download(ctx, DownloadRequest{
		Provider:     task.Provider,
		Candidate:    candidate,
		VideoPath:    task.VideoPath,
		SubtitlePath: destPath,
		Format:       format,
		Overwrite:    true,
	})
	if err != nil {
		return updated, err
	}
	if updated != nil {
		updated.ID = task.ID
		s.saveTaskQuietly(ctx, updated)
	}
	return updated, nil
}

// 确保 path/filepath 与 os 被使用（部分辅助函数可能被裁剪）。
var (
	_ = os.Stat
	_ = strconv.Itoa
)
