package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/helpers"
	"diy-strm/internal/mediaparse"
	"diy-strm/internal/models"
	"diy-strm/internal/moviepilot"
)

// ---------------------------------------------------------------------------
// 整理历史任务系统（重整理异步任务，内存态 + TTL 清理）
// ---------------------------------------------------------------------------

// organizeHistoryTaskTTL 任务状态保留时长（与 tgto123 一致 6 小时）
const organizeHistoryTaskTTL = 6 * time.Hour

// organizeHistoryTaskStatus 异步任务状态
type organizeHistoryTaskStatus struct {
	TaskID     string               `json:"task_id"`
	Type       string               `json:"type"` // reorganize
	Status     string               `json:"status"` // running / success / failed
	Message    string               `json:"message"`
	Total      int                  `json:"total"`
	SuccessCnt int                  `json:"success_cnt"`
	FailCnt    int                  `json:"fail_cnt"`
	Failures   []organizeTaskFail   `json:"failures,omitempty"`
	StartedAt  time.Time            `json:"started_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

type organizeTaskFail struct {
	RecordID uint   `json:"record_id"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
}

var (
	organizeTaskMu     sync.Mutex
	organizeTaskStates = map[string]*organizeHistoryTaskStatus{}
	organizeTaskSeq    uint64
)

func newOrganizeTask(taskID, typ string, total int) *organizeHistoryTaskStatus {
	st := &organizeHistoryTaskStatus{
		TaskID:    taskID,
		Type:      typ,
		Status:    "running",
		Total:     total,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	organizeTaskMu.Lock()
	organizeTaskStates[taskID] = st
	organizeTaskMu.Unlock()
	return st
}

func updateOrganizeTask(st *organizeHistoryTaskStatus) {
	st.UpdatedAt = time.Now()
}

func getOrganizeTaskState(taskID string) *organizeHistoryTaskStatus {
	organizeTaskMu.Lock()
	defer organizeTaskMu.Unlock()
	if st, ok := organizeTaskStates[taskID]; ok {
		// 过期清理
		if time.Since(st.UpdatedAt) > organizeHistoryTaskTTL {
			delete(organizeTaskStates, taskID)
			return nil
		}
		return st
	}
	return nil
}

// cleanupExpiredOrganizeTasks 清理过期任务状态（由 /run 接口触发）
func cleanupExpiredOrganizeTasks() {
	organizeTaskMu.Lock()
	defer organizeTaskMu.Unlock()
	now := time.Now()
	for id, st := range organizeTaskStates {
		if now.Sub(st.UpdatedAt) > organizeHistoryTaskTTL {
			delete(organizeTaskStates, id)
		}
	}
}

// ---------------------------------------------------------------------------
// 列表 / 详情
// ---------------------------------------------------------------------------

// ListOrganizeHistory 整理历史列表（分页 + 来源/状态筛选 + 关键词搜索）。
// GET /api/organize-history?page=&page_size=&source=&status=&keyword=
func ListOrganizeHistory(c *gin.Context) {
	page, _ := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(c.Query("page_size")))
	source := strings.TrimSpace(c.Query("source"))
	status := strings.TrimSpace(c.Query("status"))
	keyword := strings.TrimSpace(c.Query("keyword"))

	list, total, err := models.ListOrganizeHistoryRecords(source, status, keyword, page, pageSize)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询整理历史失败：" + err.Error(), Data: nil})
		return
	}
	items := make([]map[string]any, 0, len(list))
	for i := range list {
		items = append(items, organizeRecordView(&list[i]))
	}

	// 来源与状态统计（标签角标）
	sourceCounts, _ := models.CountOrganizeHistoryBySource()
	statusCounts, _ := models.CountOrganizeHistoryByStatus()
	sources := make([]map[string]any, 0, len(sourceCounts))
	for name, cnt := range sourceCounts {
		sources = append(sources, map[string]any{"name": name, "count": cnt})
	}

	totalPages := 0
	if pageSize > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: map[string]any{
		"items":         items,
		"total":         total,
		"page":          page,
		"page_size":     pageSize,
		"total_pages":   totalPages,
		"sources":       sources,
		"status_counts": statusCounts,
		"status_filter": status,
		"keyword":       keyword,
	}})
}

// organizeRecordView 记录视图（extra_json 解析为对象）
func organizeRecordView(r *models.OrganizeHistoryRecord) map[string]any {
	var extra map[string]any
	if r.ExtraJSON != "" {
		_ = json.Unmarshal([]byte(r.ExtraJSON), &extra)
	}
	return map[string]any{
		"id":                  r.ID,
		"source":              r.Source,
		"status":              r.Status,
		"event_time":          r.EventTime.Format("2006-01-02 15:04:05"),
		"file_id":             r.FileID,
		"file_name":           r.FileName,
		"original_file_name":  r.OriginalFileName,
		"source_path":         r.SourcePath,
		"original_source_path": r.OriginalSourcePath,
		"target_path":         r.TargetPath,
		"title":               r.Title,
		"year":                r.Year,
		"media_type":          r.MediaType,
		"season_num":          r.SeasonNum,
		"episode_num":         r.EpisodeNum,
		"tmdb_id":             r.TMDBID,
		"message":             r.Message,
		"error_message":       r.ErrorMessage,
		"extra":               extra,
		"created_at":          r.CreatedAt.Format("2006-01-02 15:04:05"),
	}
}

// GetOrganizeHistoryDetail 单条详情。GET /api/organize-history/detail?id=
func GetOrganizeHistoryDetail(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	rec := models.GetOrganizeHistoryRecord(uint(id))
	if rec == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录不存在", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: organizeRecordView(rec)})
}

// ---------------------------------------------------------------------------
// 删除 / 清理
// ---------------------------------------------------------------------------

// DeleteOrganizeHistory 删除记录（单条/批量）。POST /api/organize-history/delete
func DeleteOrganizeHistory(c *gin.Context) {
	var req struct {
		ID  uint   `json:"id"`
		IDs []uint `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	ids := req.IDs
	if req.ID > 0 {
		ids = append(ids, req.ID)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择要删除的记录", Data: nil})
		return
	}
	deleted := models.DeleteOrganizeHistoryRecords(ids)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已删除 %d 条记录", deleted), Data: map[string]any{"deleted": deleted}})
}

// ClearOrganizeHistory 按来源 + 日期范围清理。POST /api/organize-history/clear
func ClearOrganizeHistory(c *gin.Context) {
	var req struct {
		Source    string `json:"source"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		ClearAll  bool   `json:"clear_all"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.ClearAll {
		req.Source = ""
		req.StartDate = ""
		req.EndDate = ""
	}
	if req.Source == "" && req.StartDate == "" && req.EndDate == "" && !req.ClearAll {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请指定清理范围（来源或日期）", Data: nil})
		return
	}
	deleted, err := models.ClearOrganizeHistoryRecords(req.Source, req.StartDate, req.EndDate)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清理失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已清理 %d 条记录", deleted), Data: map[string]any{"deleted": deleted}})
}

// ---------------------------------------------------------------------------
// 手动触发整理
// ---------------------------------------------------------------------------

// RunOrganizeHistory 手动触发整理（复用自动整理引擎，可指定账号）。
// POST /api/organize-history/run
func RunOrganizeHistory(c *gin.Context) {
	RunAutoOrganizeNow(c)
}

// ---------------------------------------------------------------------------
// 重新整理（手动指定 TMDB，异步任务）
// ---------------------------------------------------------------------------

// ReorganizeOrganizeHistory 手动指定 TMDB 后重新整理（单条/批量）。
// 批量模式仅支持 TV（每条记录使用各自的季/集）。POST /api/organize-history/reorganize
func ReorganizeOrganizeHistory(c *gin.Context) {
	var req struct {
		RecordID uint   `json:"record_id"`
		IDs      []uint `json:"ids"`
		TMDBID   int64  `json:"tmdb_id"`
		Title    string `json:"title"`
		Year     int    `json:"year"`
		MediaType string `json:"media_type"` // movie / tv
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	ids := req.IDs
	if req.RecordID > 0 {
		ids = append(ids, req.RecordID)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择要重新整理的记录", Data: nil})
		return
	}
	if req.TMDBID <= 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请填写 TMDB ID", Data: nil})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请填写媒体标题", Data: nil})
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	if mediaType != "tv" && mediaType != "movie" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择媒体类型（电影/剧集）", Data: nil})
		return
	}

	// 加载记录并校验
	records := make([]*models.OrganizeHistoryRecord, 0, len(ids))
	accountID := uint(0)
	for _, id := range ids {
		rec := models.GetOrganizeHistoryRecord(id)
		if rec == nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("记录 #%d 不存在", id), Data: nil})
			return
		}
		// 批量模式仅支持 TV（与 tgto123 一致）
		if len(ids) > 1 && !strings.EqualFold(rec.MediaType, "TV") {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("批量重整理仅支持剧集记录（#%d 为 %s）", rec.ID, rec.MediaType), Data: nil})
			return
		}
		extra := map[string]any{}
		_ = json.Unmarshal([]byte(rec.ExtraJSON), &extra)
		recAccountID := uint(0)
		if v, ok := extra["account_id"].(float64); ok {
			recAccountID = uint(v)
		}
		if accountID == 0 {
			accountID = recAccountID
		} else if accountID != recAccountID {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "所选记录属于不同网盘账号，请分开重整理", Data: nil})
			return
		}
		records = append(records, rec)
	}
	if accountID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录缺少账号信息，无法重整理（请手动整理）", Data: nil})
		return
	}
	account, err := models.GetAccountById(accountID)
	if err != nil || account == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "记录对应的网盘账号不存在", Data: nil})
		return
	}

	// 启动异步任务
	cleanupExpiredOrganizeTasks()
	taskSeq := atomic.AddUint64(&organizeTaskSeq, 1)
	taskID := fmt.Sprintf("reorganize_%d_%d", time.Now().Unix(), taskSeq)
	st := newOrganizeTask(taskID, "reorganize", len(records))

	runAuthBackgroundTask(func() {
		defer func() {
			if r := recover(); r != nil {
				helpers.AppLogger.Errorf("重整理任务 %s panic：%v", taskID, r)
				st.Status = "failed"
				st.Message = fmt.Sprintf("任务异常中断：%v", r)
				updateOrganizeTask(st)
			}
		}()
		for _, rec := range records {
			media := &moviepilot.IdentifyResult{
				Category: mediaType,
				Title:    strings.TrimSpace(req.Title),
				Year:     req.Year,
				TmdbId:   req.TMDBID,
			}
			// 季/集优先用记录自身（批量 TV 场景）；单条可手动指定年份
			if rec.SeasonNum > 0 {
				media.Season = rec.SeasonNum
			}
			if rec.EpisodeNum > 0 {
				media.Episode = rec.EpisodeNum
			}
			if media.Category == "movie" {
				media.Season = 0
				media.Episode = 0
			} else if media.Season <= 0 {
				media.Season = 1
			}
			extra := map[string]any{}
			_ = json.Unmarshal([]byte(rec.ExtraJSON), &extra)
			parentID := ""
			if v, ok := extra["parent_id"].(string); ok {
				parentID = v
			}
			_, rErr := moviepilot.ReorganizeFile(context.Background(), account, rec.FileID, parentID, rec.OriginalFileName, media)
			if rErr != nil {
				st.FailCnt++
				st.Failures = append(st.Failures, organizeTaskFail{RecordID: rec.ID, Name: rec.OriginalFileName, Reason: rErr.Error()})
				helpers.AppLogger.Errorf("重整理记录 #%d（%s）失败：%v", rec.ID, rec.OriginalFileName, rErr)
			} else {
				st.SuccessCnt++
				helpers.AppLogger.Infof("重整理记录 #%d（%s）成功 → TMDB %d", rec.ID, rec.OriginalFileName, req.TMDBID)
			}
			updateOrganizeTask(st)
		}
		st.Status = "success"
		if st.FailCnt > 0 && st.SuccessCnt == 0 {
			st.Status = "failed"
		}
		st.Message = fmt.Sprintf("重整理完成：成功 %d / 失败 %d", st.SuccessCnt, st.FailCnt)
		updateOrganizeTask(st)
	})

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已提交重整理任务", Data: map[string]any{
		"task_id": taskID,
		"total":   len(records),
		"queued":  true,
	}})
}

// OrganizeHistoryTaskStatus 查询异步任务状态（前端轮询）。
// GET /api/organize-history/task-status?task_id=
func OrganizeHistoryTaskStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Query("task_id"))
	if taskID == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "缺少 task_id", Data: nil})
		return
	}
	st := getOrganizeTaskState(taskID)
	if st == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "任务不存在或已过期", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: st})
}

// ---------------------------------------------------------------------------
// 识别测试（重整理前验证文件名 → 标题 → TMDB 候选）
// ---------------------------------------------------------------------------

// RecognizeTestOrganize 识别测试：输入文件名，解析媒体信息并给出 TMDB 候选列表。
// POST /api/organize-history/recognize-test
func RecognizeTestOrganize(c *gin.Context) {
	var req struct {
		FileName  string `json:"file_name" binding:"required"`
		MediaType string `json:"media_type"` // 可选，指定 movie/tv 缩小范围
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	category, title, season, episode, year := mediaparse.ParseMedia(req.FileName)
	if title == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无法从文件名解析出标题", Data: nil})
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	client := models.GlobalScrapeSettings.GetTmdbClient()
	lang := models.GlobalScrapeSettings.GetTmdbLanguage()

	type candidate struct {
		Title      string `json:"title"`
		Original   string `json:"original_title"`
		Year       int    `json:"year"`
		TMDBID     int64  `json:"tmdb_id"`
		MediaType  string `json:"media_type"`
		PosterPath string `json:"poster_path,omitempty"`
	}
	candidates := make([]candidate, 0, 10)
	if mediaType == "" || mediaType == "movie" {
		if resp, err := client.SearchMovie(title, year, lang, true, true); err == nil && resp != nil {
			for _, m := range resp.Results {
				y := yearFromTMDBDate(m.ReleaseDate)
				candidates = append(candidates, candidate{Title: m.Title, Original: m.OriginalTitle, Year: y, TMDBID: m.ID, MediaType: "movie", PosterPath: m.PosterPath})
			}
		}
	}
	if mediaType == "" || mediaType == "tv" {
		if resp, err := client.SearchTv(title, year, lang, true); err == nil && resp != nil {
			for _, t := range resp.Results {
				y := yearFromTMDBDate(t.FirstAirDate)
				candidates = append(candidates, candidate{Title: t.Name, Original: t.OriginalName, Year: y, TMDBID: t.ID, MediaType: "tv", PosterPath: t.PosterPath})
			}
		}
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: map[string]any{
		"file_name":  req.FileName,
		"category":   category,
		"title":      title,
		"year":       year,
		"season":     season,
		"episode":    episode,
		"candidates": candidates,
	}})
}

// yearFromTMDBDate TMDB 日期字符串取年份（"2026-05-01" → 2026）
func yearFromTMDBDate(dateStr string) int {
	if len(dateStr) >= 4 {
		if y, err := strconv.Atoi(dateStr[:4]); err == nil && y > 1900 && y < 3000 {
			return y
		}
	}
	return 0
}
