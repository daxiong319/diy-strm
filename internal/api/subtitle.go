package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"litepan/internal/domain"
	"litepan/internal/logx"
	"litepan/internal/subtitle"
)

// 字幕智能处理的 HTTP 层：搜索下载 + 智能匹配 + 时间轴校正。
//
// 设计与 internal/api/mcp.go 一致：
//   - 由主会话在 internal/api/router.go 的 /api 分组内调用 RegisterSubtitleRoutes(r)；
//     本文件不修改 router.go，也不假设自己被挂在哪个前缀下。
//   - 服务实例用 sync.Once 懒构造，避免在禁止改动的接线文件里再插一个依赖字段。
//   - 全部端点都挂 requireAdmin（字幕配置含 API Key/Cookie 等敏感凭证）。
//
// 依赖只取 Handler 已有的 h.settings 与 h.logs：两者都在既有字段里，
// 不需要给 Deps/Handler 增字段，因此与主会话的合并互不冲突。

const (
	// subtitleMaxBody 是请求体上限。检索与下载参数都是小 JSON。
	subtitleMaxBody = 1 << 20
	// subtitleDefaultPageSize / subtitleMaxPageSize 控制任务列表分页。
	subtitleDefaultPageSize = 20
	subtitleMaxPageSize     = 200
)

var (
	// subtitleServiceOnce 保证字幕服务只构造一次。
	subtitleServiceOnce sync.Once
	subtitleServiceInst *subtitle.Service
	// subtitleTaskStoreOnce 保证任务仓储只构造一次。
	subtitleTaskStoreOnce sync.Once
	subtitleTaskStoreInst *subtitle.TaskStore
)

// SubtitleService 返回字幕服务单例。
//
// 任务仓储是可选注入：仓储需要 *sql.DB，而 Handler 上没有可直接用的句柄，
// 因此这里不强求 —— 没有仓储时检索/下载/校正全部照常工作，
// 只是不落任务历史（Service.saveTaskQuietly 对 nil 仓储是安全的）。
func (h *Handler) SubtitleService() *subtitle.Service {
	subtitleServiceOnce.Do(func() {
		// 装配层已注入实例时直接复用：整理流程自动下载字幕用的是同一个实例，
		// 两边共享一份配置快照，避免「管理页改了设置、整理流程还按旧配置跑」。
		if h.subtitleSvc != nil {
			subtitleServiceInst = h.subtitleSvc
			return
		}
		var log *slog.Logger
		if h.logs != nil {
			log = h.logs.For(logx.ModuleSystem)
		}
		subtitleServiceInst = subtitle.NewService(h.settings, subtitle.NewLogger(log))
		if store := h.SubtitleTaskStore(); store != nil {
			subtitleServiceInst.SetTaskStore(store)
		}
	})
	return subtitleServiceInst
}

// SubtitleTaskStore 返回任务仓储；未注入时返回 nil。
//
// Handler 上没有主库句柄，仓储由接线层（internal/app/wire_http.go）通过
// Deps.SubtitleTasks 注入。未注入时检索/下载/校正全部照常工作，
// 只是不落任务历史 —— Service 对 nil 仓储是安全的（saveTaskQuietly）。
func (h *Handler) SubtitleTaskStore() *subtitle.TaskStore {
	subtitleTaskStoreOnce.Do(func() {
		if subtitleTaskStoreInst == nil {
			subtitleTaskStoreInst = h.subtitleTasks
		}
	})
	return subtitleTaskStoreInst
}

// RegisterSubtitleRoutes 注册字幕相关路由。
//
// 由主会话在 internal/api/router.go 的 /api 分组内调用，例如：
// h.RegisterSubtitleRoutes(r)。本函数内部只使用相对路径，
// 不假设自己被挂在哪个前缀下。
func (h *Handler) RegisterSubtitleRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.requireAdmin)

		// 配置与状态
		r.Get("/subtitle/config", h.subtitleConfigGet)
		r.Put("/subtitle/config", h.subtitleConfigUpdate)
		r.Get("/subtitle/providers", h.subtitleProvidersGet)
		r.Post("/subtitle/providers/test", h.subtitleProviderTest)

		// 检索与匹配
		r.Post("/subtitle/search", h.subtitleSearch)
		r.Post("/subtitle/match", h.subtitleMatch)

		// 下载与时间轴校正
		r.Post("/subtitle/download", h.subtitleDownload)
		r.Post("/subtitle/sync", h.subtitleSync)
		r.Post("/subtitle/sync/check", h.subtitleSyncCheck)

		// 任务历史
		r.Get("/subtitle/tasks", h.subtitleTasksList)
		r.Post("/subtitle/tasks/{id}/retry", h.subtitleTaskRetry)
		r.Delete("/subtitle/tasks/{id}", h.subtitleTaskDelete)
	})
}

// ---- 配置与状态 ----

// subtitleConfigGet 返回当前生效的字幕配置（凭证打码）。
func (h *Handler) subtitleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := h.SubtitleService().Config()
	writeOK(w, subtitleConfigPayload(cfg))
}

// subtitleConfigUpdate 部分更新配置（只接受请求里出现的字段），
// 写完后立刻 Reload，让新凭证即时生效。
func (h *Handler) subtitleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled          *bool    `json:"enabled"`
		AssrtEnabled     *bool    `json:"assrt_enabled"`
		AssrtApiKey      *string  `json:"assrt_api_key"`
		OpenSubsEnabled  *bool    `json:"opensubtitles_enabled"`
		OpenSubsApiKey   *string  `json:"opensubtitles_api_key"`
		OpenSubsUser     *string  `json:"opensubtitles_username"`
		OpenSubsPassword *string  `json:"opensubtitles_password"`
		SubhdEnabled     *bool    `json:"subhd_enabled"`
		SubhdCookie      *string  `json:"subhd_cookie"`
		ZimukuEnabled    *bool    `json:"zimuku_enabled"`
		ZimukuCookie     *string  `json:"zimuku_cookie"`
		LanguagePriority *string  `json:"language_priority"`
		FormatPriority   *string  `json:"format_priority"`
		AutoMatch        *bool    `json:"auto_match"`
		AutoDownload     *bool    `json:"auto_download"`
		MinMatchScore    *int     `json:"min_match_score"`
		AutoSync         *bool    `json:"auto_sync"`
		SyncMode         *string  `json:"sync_mode"`
		SyncDryRun       *bool    `json:"sync_dry_run"`
		SyncMinConfid    *float64 `json:"sync_min_confidence"`
		TargetDirPolicy  *string  `json:"target_dir_policy"`
		KeepOriginal     *bool    `json:"keep_original"`
		Overwrite        *bool    `json:"overwrite"`
		Concurrency      *int     `json:"concurrency"`
		TimeoutSeconds   *int     `json:"timeout_seconds"`
	}
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}

	updates := map[string]string{}
	setBool(updates, "subtitle_enabled", req.Enabled)
	setBool(updates, "subtitle_assrt_enabled", req.AssrtEnabled)
	setString(updates, "subtitle_assrt_api_key", req.AssrtApiKey, false)
	setBool(updates, "subtitle_opensubtitles_enabled", req.OpenSubsEnabled)
	setString(updates, "subtitle_opensubtitles_api_key", req.OpenSubsApiKey, false)
	setString(updates, "subtitle_opensubtitles_username", req.OpenSubsUser, false)
	setString(updates, "subtitle_opensubtitles_password", req.OpenSubsPassword, true)
	setBool(updates, "subtitle_subhd_enabled", req.SubhdEnabled)
	setString(updates, "subtitle_subhd_cookie", req.SubhdCookie, false)
	setBool(updates, "subtitle_zimuku_enabled", req.ZimukuEnabled)
	setString(updates, "subtitle_zimuku_cookie", req.ZimukuCookie, false)
	setString(updates, "subtitle_language_priority", req.LanguagePriority, false)
	setString(updates, "subtitle_format_priority", req.FormatPriority, false)
	setBool(updates, "subtitle_auto_match", req.AutoMatch)
	setBool(updates, "subtitle_auto_download", req.AutoDownload)
	setInt(updates, "subtitle_min_match_score", req.MinMatchScore)
	setBool(updates, "subtitle_auto_sync", req.AutoSync)
	setString(updates, "subtitle_sync_mode", req.SyncMode, false)
	setBool(updates, "subtitle_sync_dry_run", req.SyncDryRun)
	setFloat(updates, "subtitle_sync_min_confidence", req.SyncMinConfid)
	setString(updates, "subtitle_target_dir_policy", req.TargetDirPolicy, false)
	setBool(updates, "subtitle_keep_original", req.KeepOriginal)
	setBool(updates, "subtitle_overwrite", req.Overwrite)
	setInt(updates, "subtitle_concurrency", req.Concurrency)
	setInt(updates, "subtitle_timeout_seconds", req.TimeoutSeconds)

	if len(updates) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "没有需要更新的配置项"))
		return
	}
	if err := h.settings.Update(r.Context(), updates); err != nil {
		writeErr(w, err)
		return
	}

	svc := h.SubtitleService()
	svc.Reload()
	h.notifySettingsUpdated(updates)
	writeOK(w, subtitleConfigPayload(svc.Config()))
}

// subtitleProvidersGet 返回各来源的启用/配置/健康状态。
func (h *Handler) subtitleProvidersGet(w http.ResponseWriter, r *http.Request) {
	statuses := h.SubtitleService().ProviderStatuses(r.Context())
	writeOK(w, map[string]any{"providers": statuses})
}

// subtitleProviderTest 只测一个来源，避免一次探测打满全部上游。
func (h *Handler) subtitleProviderTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少字幕源名称"))
		return
	}
	svc := h.SubtitleService()
	all := svc.ProviderStatuses(r.Context())
	filtered := make([]subtitle.ProviderStatus, 0, 1)
	for _, s := range all {
		if s.Name == name {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "未知字幕源：%s", name))
		return
	}
	writeOK(w, map[string]any{"providers": filtered})
}

// ---- 检索与匹配 ----

// subtitleSearch 并发检索全部已启用来源并按分数排序返回。
func (h *Handler) subtitleSearch(w http.ResponseWriter, r *http.Request) {
	var req subtitleSearchBody
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}
	searchReq, err := req.toSearchRequest()
	if err != nil {
		writeErr(w, err)
		return
	}

	scored, providerErrors, err := h.SubtitleService().Search(r.Context(), searchReq)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 即使全部来源都失败也要把错误逐条带回去，
	// 否则前端只能显示"没有结果"，无从判断是没匹配还是上游挂了。
	writeOK(w, map[string]any{
		"results":         scored,
		"provider_errors": providerErrors,
		"total":           len(scored),
	})
}

// subtitleMatch 只对显式给定的候选列表打分排序（不联网）。
func (h *Handler) subtitleMatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		subtitleSearchBody
		Candidates []subtitle.Candidate `json:"candidates"`
	}
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}
	if len(req.Candidates) == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "候选列表不能为空"))
		return
	}
	searchReq, err := req.toSearchRequest()
	if err != nil {
		writeErr(w, err)
		return
	}
	opts := subtitle.BuildMatchOptions(searchReq, h.SubtitleService().Config())
	scored := subtitle.ScoreCandidates(req.Candidates, opts)
	writeOK(w, map[string]any{"results": scored, "total": len(scored)})
}

// ---- 下载与时间轴校正 ----

// subtitleDownload 下载指定候选并（可选）自动做时间轴校正。
func (h *Handler) subtitleDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider     string                  `json:"provider"`
		Candidate    subtitle.Candidate      `json:"candidate"`
		VideoPath    string                  `json:"video_path"`
		SubtitlePath string                  `json:"subtitle_path"`
		Format       subtitle.SubtitleFormat `json:"format"`
		Overwrite    bool                    `json:"overwrite"`
		AutoSync     *bool                   `json:"auto_sync"`
	}
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.VideoPath) == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少视频路径"))
		return
	}
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = strings.TrimSpace(req.Candidate.Provider)
	}
	if req.Provider == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少字幕源名称"))
		return
	}

	downloadReq := subtitle.DownloadRequest{
		Provider:     req.Provider,
		Candidate:    req.Candidate,
		VideoPath:    req.VideoPath,
		SubtitlePath: req.SubtitlePath,
		Format:       req.Format,
		Overwrite:    req.Overwrite,
	}
	// auto_sync 未传时用（下载请求里的显式值 → 全局配置）这条链路，
	// 显式传 false 表示"本次不要校正"。
	if req.AutoSync != nil {
		downloadReq.AutoSync = *req.AutoSync
	}

	task, err := h.SubtitleService().Download(r.Context(), downloadReq)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"task": task})
}

// subtitleSync 对一个已存在的字幕文件做时间轴校正。
func (h *Handler) subtitleSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VideoPath    string   `json:"video_path"`
		SubtitlePath string   `json:"subtitle_path"`
		Mode         string   `json:"mode"`
		DryRun       *bool    `json:"dry_run"`
		KeepOriginal *bool    `json:"keep_original"`
		MinConfid    *float64 `json:"min_confidence"`
	}
	if !h.decodeSubtitleBody(w, r, &req) {
		return
	}
	videoPath := strings.TrimSpace(req.VideoPath)
	subtitlePath := strings.TrimSpace(req.SubtitlePath)
	if videoPath == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少视频路径"))
		return
	}
	if subtitlePath == "" {
		writeErr(w, domain.Errorf(domain.CodeValidation, "缺少字幕路径"))
		return
	}

	opts := subtitle.SyncOptionsFromConfig(h.SubtitleService().Config())
	if mode := strings.TrimSpace(req.Mode); mode != "" {
		opts.Mode = subtitle.SyncMode(mode)
	}
	if req.DryRun != nil {
		opts.DryRun = *req.DryRun
	}
	if req.KeepOriginal != nil {
		opts.KeepOriginal = *req.KeepOriginal
	}
	if req.MinConfid != nil {
		opts.MinConfidence = *req.MinConfid
	}

	result, err := h.SubtitleService().RunSync(r.Context(), videoPath, subtitlePath, opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"result": result})
}

// subtitleSyncCheck 报告本机是否具备时间轴校正能力（ffmpeg/ffprobe）。
func (h *Handler) subtitleSyncCheck(w http.ResponseWriter, _ *http.Request) {
	ffmpegOK, ffmpegMsg := subtitle.HasFFmpeg()
	ffprobeOK, ffprobeMsg := subtitle.HasFFprobe()
	writeOK(w, map[string]any{
		"available": ffmpegOK,
		"ffmpeg":    map[string]any{"available": ffmpegOK, "message": ffmpegMsg},
		"ffprobe":   map[string]any{"available": ffprobeOK, "message": ffprobeMsg},
	})
}

// ---- 任务历史 ----

// subtitleTasksList 分页返回任务历史。
func (h *Handler) subtitleTasksList(w http.ResponseWriter, r *http.Request) {
	store := h.SubtitleTaskStore()
	if store == nil {
		writeOK(w, map[string]any{
			"tasks": []any{}, "total": 0, "page": 1, "page_size": subtitleDefaultPageSize,
			"message": "任务仓储未接线，暂不记录字幕任务历史",
		})
		return
	}

	page := subtitleIntParam(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := subtitleIntParam(r, "page_size", subtitleDefaultPageSize)
	if pageSize < 1 {
		pageSize = subtitleDefaultPageSize
	}
	if pageSize > subtitleMaxPageSize {
		pageSize = subtitleMaxPageSize
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	tasks, total := store.ListTasks(r.Context(), page, pageSize, status)
	if tasks == nil {
		tasks = []*subtitle.Task{}
	}
	writeOK(w, map[string]any{
		"tasks": tasks, "total": total, "page": page, "page_size": pageSize,
	})
}

// subtitleTaskRetry 重跑一条任务。
func (h *Handler) subtitleTaskRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := subtitleUintParam(w, r, "id")
	if !ok {
		return
	}
	task, err := h.SubtitleService().RetryTask(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"task": task})
}

// subtitleTaskDelete 删除一条任务记录。
func (h *Handler) subtitleTaskDelete(w http.ResponseWriter, r *http.Request) {
	store := h.SubtitleTaskStore()
	if store == nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "任务仓储未接线"))
		return
	}
	id, ok := subtitleUintParam(w, r, "id")
	if !ok {
		return
	}
	if !store.DeleteTask(r.Context(), id) {
		writeErr(w, domain.Errorf(domain.CodeValidation, "字幕任务不存在"))
		return
	}
	writeOK(w, map[string]any{"deleted": true, "id": id})
}

// ---- 请求体与配置载体 ----

// subtitleSearchBody 是检索/匹配共用的输入体。
type subtitleSearchBody struct {
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int      `json:"year"`
	Season        int      `json:"season"`
	Episode       int      `json:"episode"`
	MediaType     string   `json:"media_type"`
	TmdbId        int64    `json:"tmdb_id"`
	ImdbId        string   `json:"imdb_id"`
	Languages     []string `json:"languages"`
	VideoPath     string   `json:"video_path"`
	VideoFileName string   `json:"video_file_name"`
	VideoHash     string   `json:"video_hash"`
	AutoParsePath *bool    `json:"auto_parse_path"`
}

// toSearchRequest 把请求体转成检索请求。
//
// auto_parse_path 默认 true：从视频路径补全标题/年份/季集，
// 但显式传入的字段优先级更高（BuildSearchRequest 里保证）。
func (b subtitleSearchBody) toSearchRequest() (subtitle.SearchRequest, error) {
	title := strings.TrimSpace(b.Title)
	videoPath := strings.TrimSpace(b.VideoPath)
	autoParse := b.AutoParsePath == nil || *b.AutoParsePath

	if title == "" && videoPath == "" {
		return subtitle.SearchRequest{}, domain.Errorf(domain.CodeValidation,
			"标题与视频路径至少提供一个")
	}
	if title == "" && !autoParse {
		return subtitle.SearchRequest{}, domain.Errorf(domain.CodeValidation,
			"关闭路径自动解析时必须提供标题")
	}

	req := subtitle.SearchRequest{
		Title:         title,
		OriginalTitle: strings.TrimSpace(b.OriginalTitle),
		Year:          b.Year,
		Season:        b.Season,
		Episode:       b.Episode,
		MediaType:     strings.TrimSpace(b.MediaType),
		TmdbId:        b.TmdbId,
		ImdbId:        strings.TrimSpace(b.ImdbId),
		Languages:     b.Languages,
		VideoPath:     videoPath,
		VideoFileName: strings.TrimSpace(b.VideoFileName),
		VideoHash:     strings.TrimSpace(b.VideoHash),
	}
	if !autoParse || videoPath == "" {
		if req.MediaType == "" {
			req.MediaType = "movie"
		}
		return req, nil
	}

	parsed := subtitle.BuildSearchRequest(videoPath, subtitle.MediaMeta{})
	parsed.Title = firstNonEmptyString(req.Title, parsed.Title)
	parsed.OriginalTitle = firstNonEmptyString(req.OriginalTitle, parsed.OriginalTitle)
	if req.Year != 0 {
		parsed.Year = req.Year
	}
	if req.Season != 0 {
		parsed.Season = req.Season
	}
	if req.Episode != 0 {
		parsed.Episode = req.Episode
	}
	if req.MediaType != "" {
		parsed.MediaType = req.MediaType
	}
	if req.VideoFileName != "" {
		parsed.VideoFileName = req.VideoFileName
	}
	if req.VideoHash != "" {
		parsed.VideoHash = req.VideoHash
	}
	return parsed, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// subtitleConfigPayload 是返回给前端的配置视图，凭证只回传"是否已配置"。
//
// 绝不下发明文 API Key/Cookie：管理员页面在浏览器里，
// 一旦回传就等于把凭证暴露在浏览器历史、代理日志与前端状态树里。
func subtitleConfigPayload(cfg subtitle.Config) map[string]any {
	return map[string]any{
		"enabled": cfg.Enabled,

		"assrt_enabled":     cfg.AssrtEnabled,
		"assrt_api_key_set": cfg.AssrtAPIKey != "",
		"subhd_enabled":     cfg.SubhdEnabled,
		"subhd_cookie_set":  cfg.SubhdCookie != "",
		"zimuku_enabled":    cfg.ZimukuEnabled,
		"zimuku_cookie_set": cfg.ZimukuCookie != "",
		"opensubtitles": map[string]any{
			"enabled":      cfg.OpenSubtitlesEnabled,
			"api_key_set":  cfg.OpenSubtitlesAPIKey != "",
			"username":     cfg.OpenSubtitlesUsername,
			"password_set": cfg.OpenSubtitlesPassword != "",
			"user_agent":   cfg.OpenSubtitlesUserAgent,
		},

		"language_priority": cfg.LanguagePriority,
		"format_priority":   cfg.FormatPriority,

		"auto_match":      cfg.AutoMatch,
		"auto_download":   cfg.AutoDownload,
		"min_match_score": cfg.MinMatchScore,

		"auto_sync":           cfg.AutoSync,
		"sync_mode":           cfg.SyncMode,
		"sync_dry_run":        cfg.SyncDryRun,
		"sync_min_confidence": cfg.SyncMinConfidence,

		"target_dir_policy": cfg.TargetDirPolicy,
		"keep_original":     cfg.KeepOriginal,
		"overwrite":         cfg.Overwrite,
		"concurrency":       cfg.Concurrency,
		"timeout_seconds":   cfg.TimeoutSeconds,
	}
}

// ---- 小工具 ----

// decodeSubtitleBody 解析并校验 JSON 请求体；失败时已写好响应。
func (h *Handler) decodeSubtitleBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, subtitleMaxBody))
	if err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "读取请求体失败：%v", err))
		return false
	}
	if len(body) == 0 {
		// 空体视为"全部字段未提供"，对纯查询端点友好。
		return true
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeErr(w, domain.Errorf(domain.CodeValidation, "请求体不是合法 JSON：%v", err))
		return false
	}
	return true
}

// notifySettingsUpdated 通知接线层的设置变更钩子（可为 nil）。
func (h *Handler) notifySettingsUpdated(updates map[string]string) {
	if h.onSettingsUpdated != nil {
		h.onSettingsUpdated(updates)
	}
}

func setBool(m map[string]string, key string, v *bool) {
	if v != nil {
		if *v {
			m[key] = "true"
		} else {
			m[key] = "false"
		}
	}
}

func setInt(m map[string]string, key string, v *int) {
	if v != nil {
		m[key] = strconv.Itoa(*v)
	}
}

func setFloat(m map[string]string, key string, v *float64) {
	if v != nil {
		m[key] = strconv.FormatFloat(*v, 'f', -1, 64)
	}
}

// setString 记录字符串字段；mask 为 true 表示这是密码类字段，
// 空串一律跳过（"没填"和"清空"在设置页上无法区分，前者更常见）。
func setString(m map[string]string, key string, v *string, mask bool) {
	if v == nil {
		return
	}
	value := strings.TrimSpace(*v)
	if value == "" && mask {
		return
	}
	m[key] = value
}

func subtitleIntParam(r *http.Request, name string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func subtitleUintParam(w http.ResponseWriter, r *http.Request, name string) (uint, bool) {
	raw := strings.TrimSpace(chi.URLParam(r, name))
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		writeErr(w, domain.Errorf(domain.CodeValidation, "路径参数 %s 不是合法的正整数", name))
		return 0, false
	}
	return uint(v), true
}
