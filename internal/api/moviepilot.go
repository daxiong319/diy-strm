package api

import (
	"net/http"

	"litepan/internal/domain"
	"litepan/internal/moviepilot"
)

// moviePilotConfigDTO MoviePilot 设置。
type moviePilotConfigDTO struct {
	ID                     int64  `json:"id"`
	Enabled                bool   `json:"enabled"`
	BaseUrl                string `json:"base_url"`
	ApiToken               string `json:"api_token"`
	DownloadRoot           string `json:"download_root"`
	LocalViewRoot          string `json:"local_view_root"`
	UploadAccountId        int64  `json:"upload_account_id"`
	UploadRoot             string `json:"upload_root"`
	UploadRootId           string `json:"upload_root_id"`
	StrmLocalDir           string `json:"strm_local_dir"`
	PollInterval           int    `json:"poll_interval"`
	NotifyEnabled          bool   `json:"notify_enabled"`
	CategoryConfig         string `json:"category_config"`
	PromotionOrder         string `json:"promotion_order"`
	PromotionPatienceHours int    `json:"promotion_patience_hours"`
	SeedRetentionHours     int    `json:"seed_retention_hours"`
	QbittorrentURL         string `json:"qbittorrent_url"`
	QbittorrentUser        string `json:"qbittorrent_user"`
	QbittorrentPass        string `json:"qbittorrent_pass"`
	CreatedAt              string `json:"created_at,omitempty"`
	UpdatedAt              string `json:"updated_at,omitempty"`
}

// moviePilotConfigInput 设置更新入参（字段均可选，未提供则保持原值）。
type moviePilotConfigInput struct {
	Enabled                *bool   `json:"enabled"`
	BaseUrl                *string `json:"base_url"`
	ApiToken               *string `json:"api_token"`
	DownloadRoot           *string `json:"download_root"`
	LocalViewRoot          *string `json:"local_view_root"`
	UploadAccountId        *int64  `json:"upload_account_id"`
	UploadRoot             *string `json:"upload_root"`
	UploadRootId           *string `json:"upload_root_id"`
	StrmLocalDir           *string `json:"strm_local_dir"`
	PollInterval           *int    `json:"poll_interval"`
	NotifyEnabled          *bool   `json:"notify_enabled"`
	CategoryConfig         *string `json:"category_config"`
	PromotionOrder         *string `json:"promotion_order"`
	PromotionPatienceHours *int    `json:"promotion_patience_hours"`
	SeedRetentionHours     *int    `json:"seed_retention_hours"`
	QbittorrentURL         *string `json:"qbittorrent_url"`
	QbittorrentUser        *string `json:"qbittorrent_user"`
	QbittorrentPass        *string `json:"qbittorrent_pass"`
}

// moviePilotTestInput 连接测试入参：未提供时回退到已保存配置。
type moviePilotTestInput struct {
	BaseUrl  string `json:"base_url"`
	ApiToken string `json:"api_token"`
}

// moviePilotUploadTaskDTO 上传任务。
type moviePilotUploadTaskDTO struct {
	ID               int64  `json:"id"`
	TorrentHash      string `json:"torrent_hash"`
	Title            string `json:"title"`
	MediaType        string `json:"media_type"`
	TmdbId           int64  `json:"tmdb_id"`
	Season           string `json:"season"`
	LocalPath        string `json:"local_path"`
	RemotePath       string `json:"remote_path"`
	Status           string `json:"status"`
	TotalFiles       int    `json:"total_files"`
	UploadedFiles    int    `json:"uploaded_files"`
	TotalBytes       int64  `json:"total_bytes"`
	UploadedBytes    int64  `json:"uploaded_bytes"`
	TotalSizeText    string `json:"total_size_text"`
	Error            string `json:"error,omitempty"`
	EmptySourceSince string `json:"empty_source_since,omitempty"`
	IsRunning        bool   `json:"is_running"`
	CreatedAt        string `json:"created_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

// moviePilotFailedFileDTO 识别失败文件。
type moviePilotFailedFileDTO struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	FileName  string `json:"file_name"`
	RootPath  string `json:"root_path"`
	Status    string `json:"status"`
	MediaType string `json:"media_type"`
	Title     string `json:"title"`
	TmdbId    int64  `json:"tmdb_id"`
	Year      int    `json:"year"`
	Season    int    `json:"season"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// moviePilotResolveFailedInput 失败文件重新整理入参。
type moviePilotResolveFailedInput struct {
	MediaType string `json:"media_type"`
	Title     string `json:"title"`
	Year      int    `json:"year"`
	Season    int    `json:"season"`
	TmdbId    int64  `json:"tmdb_id"`
}

// moviePilotOrganizeHistoryDTO 整理历史。
type moviePilotOrganizeHistoryDTO struct {
	ID         int64  `json:"id"`
	AccountID  int64  `json:"account_id"`
	TaskID     int64  `json:"task_id"`
	FileName   string `json:"file_name"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	MediaType  string `json:"media_type"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	SeasonNum  int    `json:"season_num"`
	EpisodeNum int    `json:"episode_num"`
	TmdbId     int64  `json:"tmdb_id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// moviePilotPageDTO 统一分页响应。
type moviePilotPageDTO[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// toMoviePilotConfigDTO 实体转 DTO。
func toMoviePilotConfigDTO(cfg *domain.MoviePilotConfig) moviePilotConfigDTO {
	if cfg == nil {
		return moviePilotConfigDTO{}
	}
	return moviePilotConfigDTO{
		ID:                     cfg.ID,
		Enabled:                cfg.Enabled,
		BaseUrl:                cfg.BaseUrl,
		ApiToken:               cfg.ApiToken,
		DownloadRoot:           cfg.DownloadRoot,
		LocalViewRoot:          cfg.LocalViewRoot,
		UploadAccountId:        cfg.UploadAccountId,
		UploadRoot:             cfg.UploadRoot,
		UploadRootId:           cfg.UploadRootId,
		StrmLocalDir:           cfg.StrmLocalDir,
		PollInterval:           cfg.PollInterval,
		NotifyEnabled:          cfg.NotifyEnabled,
		CategoryConfig:         cfg.CategoryConfig,
		PromotionOrder:         cfg.PromotionOrder,
		PromotionPatienceHours: cfg.PromotionPatienceHours,
		SeedRetentionHours:     cfg.SeedRetentionHours,
		QbittorrentURL:         cfg.QbittorrentURL,
		QbittorrentUser:        cfg.QbittorrentUser,
		QbittorrentPass:        cfg.QbittorrentPass,
		CreatedAt:              FormatAPITime(cfg.CreatedAt),
		UpdatedAt:              FormatAPITime(cfg.UpdatedAt),
	}
}

// toMoviePilotUploadTaskDTO 实体转 DTO。
func toMoviePilotUploadTaskDTO(t *domain.MoviePilotUploadTask, running bool) moviePilotUploadTaskDTO {
	dto := moviePilotUploadTaskDTO{
		ID:            t.ID,
		TorrentHash:   t.TorrentHash,
		Title:         t.Title,
		MediaType:     t.MediaType,
		TmdbId:        t.TmdbId,
		Season:        t.Season,
		LocalPath:     t.LocalPath,
		RemotePath:    t.RemotePath,
		Status:        t.Status,
		TotalFiles:    t.TotalFiles,
		UploadedFiles: t.UploadedFiles,
		TotalBytes:    t.TotalBytes,
		UploadedBytes: t.UploadedBytes,
		TotalSizeText: moviepilot.SizeGBText(t.TotalBytes),
		Error:         t.Error,
		IsRunning:     running,
		CreatedAt:     FormatAPITime(t.CreatedAt),
		UpdatedAt:     FormatAPITime(t.UpdatedAt),
	}
	if t.EmptySourceSince != nil {
		dto.EmptySourceSince = FormatAPITime(*t.EmptySourceSince)
	}
	return dto
}

// toMoviePilotFailedFileDTO 实体转 DTO。
func toMoviePilotFailedFileDTO(f *domain.MoviePilotFailedFile) moviePilotFailedFileDTO {
	return moviePilotFailedFileDTO{
		ID:        f.ID,
		TaskID:    f.TaskID,
		FileName:  f.FileName,
		RootPath:  f.RootPath,
		Status:    f.Status,
		MediaType: f.MediaType,
		Title:     f.Title,
		TmdbId:    f.TmdbId,
		Year:      f.Year,
		Season:    f.Season,
		Reason:    f.Reason,
		CreatedAt: FormatAPITime(f.CreatedAt),
		UpdatedAt: FormatAPITime(f.UpdatedAt),
	}
}

// getMoviePilotConfig 读取设置。
func (h *Handler) getMoviePilotConfig(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	cfg, err := h.moviePilot.LoadConfig(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toMoviePilotConfigDTO(cfg))
}

// updateMoviePilotConfig 更新设置。
func (h *Handler) updateMoviePilotConfig(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	var in moviePilotConfigInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	ctx := r.Context()
	cfg, err := h.moviePilot.LoadConfig(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.BaseUrl != nil {
		cfg.BaseUrl = *in.BaseUrl
	}
	if in.ApiToken != nil {
		cfg.ApiToken = *in.ApiToken
	}
	if in.DownloadRoot != nil {
		cfg.DownloadRoot = *in.DownloadRoot
	}
	if in.LocalViewRoot != nil {
		cfg.LocalViewRoot = *in.LocalViewRoot
	}
	if in.UploadAccountId != nil {
		cfg.UploadAccountId = *in.UploadAccountId
	}
	if in.UploadRoot != nil {
		cfg.UploadRoot = *in.UploadRoot
	}
	if in.UploadRootId != nil {
		cfg.UploadRootId = *in.UploadRootId
	}
	if in.StrmLocalDir != nil {
		cfg.StrmLocalDir = *in.StrmLocalDir
	}
	if in.PollInterval != nil {
		cfg.PollInterval = *in.PollInterval
	}
	if in.NotifyEnabled != nil {
		cfg.NotifyEnabled = *in.NotifyEnabled
	}
	if in.CategoryConfig != nil {
		cfg.CategoryConfig = *in.CategoryConfig
	}
	if in.PromotionOrder != nil {
		cfg.PromotionOrder = *in.PromotionOrder
	}
	if in.PromotionPatienceHours != nil {
		cfg.PromotionPatienceHours = *in.PromotionPatienceHours
	}
	if in.SeedRetentionHours != nil {
		cfg.SeedRetentionHours = *in.SeedRetentionHours
	}
	if in.QbittorrentURL != nil {
		cfg.QbittorrentURL = *in.QbittorrentURL
	}
	if in.QbittorrentUser != nil {
		cfg.QbittorrentUser = *in.QbittorrentUser
	}
	if in.QbittorrentPass != nil {
		cfg.QbittorrentPass = *in.QbittorrentPass
	}
	if err := h.moviePilot.SaveConfig(ctx, cfg); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, toMoviePilotConfigDTO(cfg))
}

// testMoviePilotConnection 测试连接。
func (h *Handler) testMoviePilotConnection(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	var in moviePilotTestInput
	// 允许空请求体：直接测试已保存配置
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := h.moviePilot.TestConnection(r.Context(), in.BaseUrl, in.ApiToken); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "连接成功"})
}

// listMoviePilotSubscribes 列出订阅。
func (h *Handler) listMoviePilotSubscribes(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	subs, err := h.moviePilot.ListSubscribes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, subs)
}

// createMoviePilotSubscribe 新增订阅。
func (h *Handler) createMoviePilotSubscribe(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	var in moviepilot.CreateSubscribeRequest
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	id, err := h.moviePilot.CreateSubscribe(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// searchMoviePilotSubscribe 触发订阅搜索。
func (h *Handler) searchMoviePilotSubscribe(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.moviePilot.SubscribeSearch(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "已触发搜索"})
}

// deleteMoviePilotSubscribe 删除订阅。
func (h *Handler) deleteMoviePilotSubscribe(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.moviePilot.DeleteSubscribe(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "已删除"})
}

// updateMoviePilotSubscribeStatus 修改订阅状态。
func (h *Handler) updateMoviePilotSubscribeStatus(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		State string `json:"state"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if err := h.moviePilot.UpdateSubscribeStatus(r.Context(), id, in.State); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "已更新"})
}

// listMoviePilotDownloads 列出下载任务（含完成度）。
func (h *Handler) listMoviePilotDownloads(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	downloads, err := h.moviePilot.ListDownloads(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, downloads)
}

// listMoviePilotUploadTasks 分页列出上传任务。
func (h *Handler) listMoviePilotUploadTasks(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 20)
	status := r.URL.Query().Get("status")
	tasks, total, err := h.moviePilot.ListUploadTasks(r.Context(), page, pageSize, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]moviePilotUploadTaskDTO, 0, len(tasks))
	for i := range tasks {
		items = append(items, toMoviePilotUploadTaskDTO(&tasks[i], h.moviePilot.UploadTaskStillRunning(tasks[i].ID)))
	}
	writeOK(w, moviePilotPageDTO[moviePilotUploadTaskDTO]{Items: items, Total: total, Page: page, PageSize: pageSize})
}

// retryMoviePilotUploadTask 重试上传任务。
func (h *Handler) retryMoviePilotUploadTask(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	queued, err := h.moviePilot.RetryUploadTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"queued": queued})
}

// cancelMoviePilotUploadTask 取消上传任务。
func (h *Handler) cancelMoviePilotUploadTask(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.moviePilot.CancelUploadTask(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "已取消"})
}

// listMoviePilotFailedFiles 分页列出识别失败文件。
func (h *Handler) listMoviePilotFailedFiles(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 20)
	status := r.URL.Query().Get("status")
	items, total, err := h.moviePilot.ListFailedFiles(r.Context(), page, pageSize, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]moviePilotFailedFileDTO, 0, len(items))
	for i := range items {
		out = append(out, toMoviePilotFailedFileDTO(&items[i]))
	}
	writeOK(w, moviePilotPageDTO[moviePilotFailedFileDTO]{Items: out, Total: total, Page: page, PageSize: pageSize})
}

// identifyMoviePilotFailedFile 重新识别失败文件。
func (h *Handler) identifyMoviePilotFailedFile(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	media, err := h.moviePilot.IdentifyFailedFile(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, media)
}

// resolveMoviePilotFailedFile 手工指定媒体信息后重新整理。
func (h *Handler) resolveMoviePilotFailedFile(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in moviePilotResolveFailedInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	target, err := h.moviePilot.ResolveFailedFile(r.Context(), id, in.MediaType, in.Title, in.Year, in.Season, in.TmdbId)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"target_dir": target.RelDir,
		"new_name":   target.NewName,
		"title":      target.Media.Title,
		"tmdb_id":    target.Media.TmdbId,
	})
}

// skipMoviePilotFailedFile 忽略失败文件。
func (h *Handler) skipMoviePilotFailedFile(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.moviePilot.SkipFailedFile(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": "已忽略"})
}

// listMoviePilotOrganizeHistory 列出整理历史。
func (h *Handler) listMoviePilotOrganizeHistory(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	limit := queryInt(r, "limit", 50)
	rows, err := h.moviePilot.ListOrganizeHistory(r.Context(), limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]moviePilotOrganizeHistoryDTO, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		out = append(out, moviePilotOrganizeHistoryDTO{
			ID:         row.ID,
			AccountID:  row.AccountID,
			TaskID:     row.TaskID,
			FileName:   row.FileName,
			SourcePath: row.SourcePath,
			TargetPath: row.TargetPath,
			MediaType:  row.MediaType,
			Title:      row.Title,
			Year:       row.Year,
			SeasonNum:  row.SeasonNum,
			EpisodeNum: row.EpisodeNum,
			TmdbId:     row.TmdbId,
			Status:     row.Status,
			Message:    row.Message,
			CreatedAt:  FormatAPITime(row.CreatedAt),
		})
	}
	writeOK(w, out)
}

// moviePilotVersionDisplay 版本号展示文案。
func moviePilotVersionDisplay(v int) string {
	switch v {
	case moviepilot.MajorVersionV3:
		return "v3"
	case moviepilot.MajorVersionV1V2:
		return "v1/v2"
	default:
		return "未知"
	}
}

// getMoviePilotVersion 探测 MoviePilot 主版本（GET /moviepilot/version）。
//
// 探测失败不报错：返回 major_version=0 且 degraded=true，调用方据此回退 v1/v2 行为。
// 这样「MoviePilot 没起」不会让设置页报错，只提示降级。
func (h *Handler) getMoviePilotVersion(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	v, err := h.moviePilot.MajorVersion(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"major_version": v,
		"display":       moviePilotVersionDisplay(v),
		"degraded":      v == moviepilot.MajorVersionUnknown,
	})
}

// moviePilotFallbackDTO 降级兜底记录（观测用）。
type moviePilotFallbackDTO struct {
	ID                int64  `json:"id"`
	MediaKey          string `json:"media_key"`
	Trigger           string `json:"trigger"`
	MediaType         string `json:"media_type"`
	TmdbId            int64  `json:"tmdb_id"`
	Title             string `json:"title"`
	Season            int    `json:"season"`
	SearchCount       int    `json:"search_count"`
	SubscriptionCount int    `json:"subscription_count"`
	Progress          int    `json:"progress"`
	Status            string `json:"status"`
	Action            string `json:"action"`
	DownloadEpisodes  string `json:"download_episodes"`
	ExternalID        string `json:"external_id"`
	Message           string `json:"message"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// toMoviePilotFallbackDTO 领域模型 → 观测 DTO。
func toMoviePilotFallbackDTO(rec domain.MoviePilotFallback) moviePilotFallbackDTO {
	return moviePilotFallbackDTO{
		ID:                rec.ID,
		MediaKey:          rec.MediaKey,
		Trigger:           rec.Trigger,
		MediaType:         rec.MediaType,
		TmdbId:            rec.TmdbId,
		Title:             rec.Title,
		Season:            rec.Season,
		SearchCount:       rec.SearchCount,
		SubscriptionCount: rec.SubscriptionCount,
		Progress:          rec.Progress,
		Status:            rec.Status,
		Action:            rec.Action,
		DownloadEpisodes:  rec.DownloadEpisodes,
		ExternalID:        rec.ExternalID,
		Message:           rec.Message,
		CreatedAt:         rec.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:         rec.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// moviePilotFallbackActionDTO 可选动作（前端下拉选项）。
type moviePilotFallbackActionDTO struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// listMoviePilotFallbacks 列出降级兜底记录（GET /moviepilot/fallbacks）。
func (h *Handler) listMoviePilotFallbacks(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 20)
	status := r.URL.Query().Get("status")
	items, total, err := h.moviePilot.ListFallbacks(r.Context(), page, pageSize, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	list := make([]moviePilotFallbackDTO, 0, len(items))
	for _, it := range items {
		list = append(list, toMoviePilotFallbackDTO(it))
	}
	pages := 0
	if pageSize > 0 {
		pages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	writeOK(w, map[string]any{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"pages":     pages,
	})
}

// getMoviePilotFallbackSummary 降级兜底概览（GET /moviepilot/fallbacks/summary）。
//
// 与源实现一致：即使未配置 MoviePilot 也正常返回，由 configured 字段提示前端；
// 阈值/动作在未显式配置时报默认值（3 / download）。
func (h *Handler) getMoviePilotFallbackSummary(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.moviePilot != nil) {
		return
	}
	sum, err := h.moviePilot.FallbackSummary(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"configured":             sum.Configured,
		"enabled":                sum.Enabled,
		"search_enabled":         sum.SearchEnabled,
		"search_threshold":       sum.SearchThreshold,
		"search_action":          sum.SearchAction,
		"subscription_enabled":   sum.SubscriptionEnabled,
		"subscription_threshold": sum.SubscriptionThreshold,
		"subscription_action":    sum.SubscriptionAction,
		"active_count":           sum.ActiveCount,
		"actions": []moviePilotFallbackActionDTO{
			{Value: domain.MoviePilotFallbackActionSubscribe, Label: "仅添加 MoviePilot 订阅"},
			{Value: domain.MoviePilotFallbackActionDownload, Label: "仅让 MoviePilot 搜索下载"},
			{Value: domain.MoviePilotFallbackActionDownloadThenSubscribe, Label: "先搜索下载，无资源再订阅"},
		},
	})
}
