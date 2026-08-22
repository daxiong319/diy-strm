package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 监控历史（对齐 tgto123 的转存历史：来源标签 + 状态筛选 + 结果搜索 + 分页）
// 覆盖 TG 频道订阅 / 影巢订阅 / TG 机器人三类监控入口的转存审计记录
// ---------------------------------------------------------------------------

// monitorHistorySourceLabel 来源标签（网盘类型 → 展示名）
func monitorHistorySourceLabel(sourceType string) string {
	switch sourceType {
	case string(models.SourceType123):
		return "123云盘"
	case string(models.SourceTypeGuangYaPan):
		return "光鸭云盘"
	case string(models.SourceTypePan139):
		return "移动云盘"
	default:
		return sourceType
	}
}

// ListMonitorHistory GET /api/monitor-history?page=&page_size=&source=&status=&keyword=
func ListMonitorHistory(c *gin.Context) {
	page, _ := strconv.Atoi(strings.TrimSpace(c.Query("page")))
	pageSize, _ := strconv.Atoi(strings.TrimSpace(c.Query("page_size")))
	source := strings.TrimSpace(c.Query("source"))
	status := strings.TrimSpace(c.Query("status"))
	keyword := strings.TrimSpace(c.Query("keyword"))

	records, total, statusOptions, statusCounts, err := models.QueryMonitorTransferRecords(source, status, keyword, page, pageSize)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询监控历史失败：" + err.Error(), Data: nil})
		return
	}

	items := make([]map[string]any, 0, len(records))
	for i := range records {
		r := &records[i]
		entryLabel := r.Entry
		switch r.Entry {
		case "channel":
			entryLabel = "TG 频道"
		case "hive":
			entryLabel = "影巢"
		case "bot":
			entryLabel = "TG 机器人"
		}
		items = append(items, map[string]any{
			"id":              r.ID,
			"source_type":     r.SourceType,
			"source_label":    monitorHistorySourceLabel(r.SourceType),
			"entry":           r.Entry,
			"entry_label":     entryLabel,
			"channel":         r.Channel,
			"message_id":      r.MessageID,
			"message_url":     r.MessageURL,
			"target_url":      r.TargetURL,
			"transfer_status": r.TransferStatus,
			"transfer_time":   r.TransferTime.Format("2006-01-02 15:04:05"),
			"transfer_result": r.TransferResult,
			"title":           r.Title,
			"total":           r.Total,
			"target_dir":      r.TargetDir,
			"subscription_id": r.SubscriptionID,
		})
	}

	// 来源角标（有记录的网盘 + 计数）
	sourceCounts, _ := models.CountMonitorHistoryBySource()
	sources := make([]map[string]any, 0, len(sourceCounts))
	for _, st := range []models.SourceType{models.SourceType123, models.SourceTypeGuangYaPan, models.SourceTypePan139} {
		if cnt, ok := sourceCounts[string(st)]; ok {
			sources = append(sources, map[string]any{"name": string(st), "label": monitorHistorySourceLabel(string(st)), "count": cnt})
		}
	}

	totalPages := 0
	if pageSize > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: map[string]any{
		"items":          items,
		"total":          total,
		"page":           page,
		"page_size":      pageSize,
		"total_pages":    totalPages,
		"sources":        sources,
		"status_options": statusOptions,
		"status_counts":  statusCounts,
		"status_filter":  status,
		"keyword":        keyword,
	}})
}

// DeleteMonitorHistory POST /api/monitor-history/delete  body: {"id":1} 或 {"ids":[1,2,3]}
func DeleteMonitorHistory(c *gin.Context) {
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未指定要删除的记录", Data: nil})
		return
	}
	if err := models.DeleteMonitorTransferRecords(ids); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已删除 " + strconv.Itoa(len(ids)) + " 条记录", Data: nil})
}

// ClearMonitorHistory POST /api/monitor-history/clear  body: {"source":"123","start_date":"2026-01-01","end_date":"2026-02-01","clear_all":false}
func ClearMonitorHistory(c *gin.Context) {
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
	if !req.ClearAll && req.Source == "" && req.StartDate == "" && req.EndDate == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请指定来源或日期范围，或勾选一键清空", Data: nil})
		return
	}
	var startDate, endDate *time.Time
	if s := strings.TrimSpace(req.StartDate); s != "" {
		t, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "起始日期格式应为 YYYY-MM-DD", Data: nil})
			return
		}
		startDate = &t
	}
	if s := strings.TrimSpace(req.EndDate); s != "" {
		t, err := time.ParseInLocation("2006-01-02", s, time.Local)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "结束日期格式应为 YYYY-MM-DD", Data: nil})
			return
		}
		eod := t.Add(24*time.Hour - time.Second)
		endDate = &eod
	}
	source := ""
	if !req.ClearAll {
		source = req.Source
	}
	n, err := models.ClearMonitorTransferRecords(source, startDate, endDate)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清理失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已清理 " + strconv.FormatInt(n, 10) + " 条监控历史", Data: nil})
}
