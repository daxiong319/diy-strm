package moviepilot

import (
	"encoding/json"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
)

// addOrganizeRecord 整理历史埋点统一入口：
// 自动整理 / 上传任务整理 / 手动目录整理 / 确认整理 均通过此函数写入 organize_history_records。
// status: success/failed/skipped/replace；extra 会序列化进 extra_json（account_id/parent_id/task_id 等）。
func addOrganizeRecord(account *models.Account, status, fileID, originalFileName, fileName, sourcePath, targetPath, mediaType, title string, year, season, episode int, tmdbID int64, message, errMsg string, extra map[string]any) {
	if account == nil {
		return
	}
	extraJSON := ""
	if len(extra) > 0 {
		if b, err := json.Marshal(extra); err == nil {
			extraJSON = string(b)
		}
	}
	mediaTypeNorm := ""
	switch strings.ToLower(mediaType) {
	case "movie", "tv":
		mediaTypeNorm = strings.Title(strings.ToLower(mediaType))
	}
	rec := &models.OrganizeHistoryRecord{
		Source:           models.SourceDisplayName(account.SourceType),
		Status:           status,
		EventTime:        time.Now(),
		FileID:           fileID,
		FileName:         fileName,
		OriginalFileName: originalFileName,
		SourcePath:       sourcePath,
		TargetPath:       targetPath,
		Title:            title,
		Year:             year,
		MediaType:        mediaTypeNorm,
		SeasonNum:        season,
		EpisodeNum:       episode,
		TMDBID:           tmdbID,
		Message:          message,
		ErrorMessage:     errMsg,
		ExtraJSON:        extraJSON,
	}
	if err := models.CreateOrganizeHistoryRecord(rec); err != nil {
		helpers.AppLogger.Errorf("写入整理历史失败：%v", err)
	}
}

// recordSuccess 整理成功埋点（replace=true 表示洗版替换）
func recordSuccess(account *models.Account, e organizeEntry, sourcePath, targetPath, mediaType, title string, year, season, episode int, tmdbID int64, newName, message string, extra map[string]any) {
	status := models.OrganizeStatusSuccess
	if extra != nil {
		if ov, ok := extra["replace"].(bool); ok && ov {
			status = models.OrganizeStatusReplace
		}
	}
	addOrganizeRecord(account, status, e.ID, e.Name, newName, sourcePath, targetPath, mediaType, title, year, season, episode, tmdbID, message, "", extra)
}

// recordSkipped 跳过埋点（识别失败/TMDB 未命中/目标已存在保留）
func recordSkipped(account *models.Account, e organizeEntry, sourcePath, mediaType, title string, year, season, episode int, tmdbID int64, message, errMsg string, extra map[string]any) {
	addOrganizeRecord(account, models.OrganizeStatusSkipped, e.ID, e.Name, "", sourcePath, "", mediaType, title, year, season, episode, tmdbID, message, errMsg, extra)
}

// recordFailed 失败埋点
func recordFailed(account *models.Account, e organizeEntry, sourcePath, mediaType, title string, year, season, episode int, tmdbID int64, errMsg string, extra map[string]any) {
	addOrganizeRecord(account, models.OrganizeStatusFailed, e.ID, e.Name, "", sourcePath, "", mediaType, title, year, season, episode, tmdbID, "", errMsg, extra)
}

// baseOrganizeExtra 组装通用扩展信息（account_id/parent_id/task_id）
func baseOrganizeExtra(account *models.Account, parentID string, taskID uint) map[string]any {
	extra := map[string]any{
		"account_id":  account.ID,
		"source_type": string(account.SourceType),
	}
	if parentID != "" {
		extra["parent_id"] = parentID
	}
	if taskID > 0 {
		extra["task_id"] = taskID
	}
	return extra
}
