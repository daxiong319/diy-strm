package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/gcid"
	"diy-strm/internal/guangyapan"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notificationmanager"

	"github.com/gin-gonic/gin"
)

// 光鸭 GCID 秒传导出/导入（对齐 tgto123 /api/guangya/gcid-export 语义）：
// 导出=扫描目录生成秒传 JSON + 发 TG 机器人；导入=粘贴 JSON 逐条秒传入目标目录。

// gcidJob 后台任务（导出/导入共用，内存态；单实例部署够用）
type gcidJob struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`   // export / import
	Status    string    `json:"status"` // pending / running / success / failed
	Message   string    `json:"message"`
	Total     int       `json:"total"`
	Done      int       `json:"done"`
	Failed    int       `json:"failed"`
	File      string    `json:"file,omitempty"` // 导出 JSON 文件路径
	CreatedAt time.Time `json:"created_at"`
}

var (
	gcidJobsMu sync.Mutex
	gcidJobs   = map[string]*gcidJob{}
	gcidJobSeq int
)

func newGcidJob(jobType string) *gcidJob {
	gcidJobsMu.Lock()
	defer gcidJobsMu.Unlock()
	gcidJobSeq++
	job := &gcidJob{
		ID:        fmt.Sprintf("%s-%d-%d", jobType, time.Now().Unix(), gcidJobSeq),
		Type:      jobType,
		Status:    "running",
		CreatedAt: time.Now(),
	}
	gcidJobs[job.ID] = job
	// 只保留最近 20 个任务
	if len(gcidJobs) > 20 {
		oldestID := ""
		var oldest time.Time
		for id, j := range gcidJobs {
			if oldest.IsZero() || j.CreatedAt.Before(oldest) {
				oldest = j.CreatedAt
				oldestID = id
			}
		}
		delete(gcidJobs, oldestID)
	}
	return job
}

// GetGuangYaClientByAccountID 按账号 ID 取光鸭客户端
func GetGuangYaClientByAccountID(accountID uint) (*guangyapan.Client, error) {
	account, err := models.GetAccountById(accountID)
	if err != nil {
		return nil, fmt.Errorf("账号不存在（%d）", accountID)
	}
	if account.SourceType != models.SourceTypeGuangYaPan {
		return nil, fmt.Errorf("账号 %s 不是光鸭云盘", account.Name)
	}
	client := account.GetGuangYaPanClient()
	if client == nil {
		return nil, fmt.Errorf("光鸭客户端初始化失败")
	}
	return client, nil
}

// ExportGcidAPI POST /api/guangya/gcid-export {account_id, folder_id, folder_name}
func ExportGcidAPI(c *gin.Context) {
	var req struct {
		AccountID  uint   `json:"account_id"`
		FolderID   string `json:"folder_id"`
		FolderName string `json:"folder_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.FolderID == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：需要 account_id 与 folder_id"})
		return
	}
	client, err := GetGuangYaClientByAccountID(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	job := newGcidJob("export")
	go runGcidExportJob(job, client, req.FolderID, req.FolderName)
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "已进入后台扫描队列，完成后发送到通知渠道", Data: gin.H{"job_id": job.ID}})
}

// runGcidExportJob 扫描 → 写 JSON → TG 通知
func runGcidExportJob(job *gcidJob, client *guangyapan.Client, folderID, folderName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	items, err := gcid.ScanDirForExport(ctx, client, folderID, folderName, 2000)
	gcidJobsMu.Lock()
	job.Total = len(items)
	gcidJobsMu.Unlock()
	if err != nil && len(items) == 0 {
		gcidJobsMu.Lock()
		job.Status = "failed"
		job.Message = "扫描失败：" + err.Error()
		gcidJobsMu.Unlock()
		return
	}
	doc := gcid.GcidExportDoc{
		Version:    1,
		Provider:   "guangya",
		SourceDir:  folderName,
		ExportedAt: time.Now().Unix(),
		Items:      items,
	}
	raw, _ := json.MarshalIndent(doc, "", "  ")
	exportDir := filepath.Join(helpers.ConfigDir, "gcid-exports")
	_ = os.MkdirAll(exportDir, 0755)
	safeName := sanitizeFilename(folderName)
	fileName := fmt.Sprintf("guangya-gcid-%s-%s.json", safeName, time.Now().Format("20060102-150405"))
	filePath := filepath.Join(exportDir, fileName)
	if err := os.WriteFile(filePath, raw, 0644); err != nil {
		job.Status = "failed"
		job.Message = "写入秒传 JSON 失败：" + err.Error()
		return
	}
	gcidJobsMu.Lock()
	job.File = filePath
	job.Status = "success"
	job.Message = fmt.Sprintf("扫描完成：%d 个文件；JSON 已生成（%s）", len(items), fileName)
	gcidJobsMu.Unlock()
	gcid.SaveExportHistory(folderName, len(items), fileName)
	sendGcidNotify(fmt.Sprintf("✅ 光鸭 GCID 秒传 JSON 已生成\n目录：%s\n文件数：%d\n文件：%s", folderName, len(items), fileName))
}

// ImportGcidAPI POST /api/guangya/gcid-import {account_id, folder_id, json_text}
func ImportGcidAPI(c *gin.Context) {
	var req struct {
		AccountID uint   `json:"account_id"`
		FolderID  string `json:"folder_id"`
		JSONText  string `json:"json_text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.FolderID == "" || req.JSONText == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：需要 account_id、folder_id 与 json_text"})
		return
	}
	client, err := GetGuangYaClientByAccountID(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	doc, err := parseGcidDoc(req.JSONText)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "解析秒传 JSON 失败：" + err.Error()})
		return
	}
	if len(doc.Items) == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "秒传 JSON 中没有可导入的条目"})
		return
	}
	job := newGcidJob("import")
	job.Total = len(doc.Items)
	go runGcidImportJob(job, client, req.FolderID, doc)
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: fmt.Sprintf("已开始导入 %d 个文件", len(doc.Items)), Data: gin.H{"job_id": job.ID}})
}

// runGcidImportJob 逐条秒传并汇报
func runGcidImportJob(job *gcidJob, client *guangyapan.Client, folderID string, doc *gcid.GcidExportDoc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	var failedList []string
	for _, item := range doc.Items {
		if ctx.Err() != nil {
			break
		}
		if item.GCID == "" || item.FileSize <= 0 || item.FileName == "" {
			job.Failed++
			failedList = append(failedList, item.FileName+"（缺 GCID/大小）")
			continue
		}
		one := gcid.GcidExportDoc{Items: []gcid.GcidItem{item}}
		okN, failN, failMsgs := gcid.ImportFromDoc(ctx, client, &one, folderID)
		gcidJobsMu.Lock()
		job.Done += okN
		job.Failed += failN
		gcidJobsMu.Unlock()
		if failN > 0 && len(failedList) < 20 {
			failedList = append(failedList, failMsgs...)
		}
	}
	gcidJobsMu.Lock()
	if job.Failed > 0 && job.Done == 0 {
		job.Status = "failed"
		job.Message = "全部导入失败：" + joinShort(failedList)
		gcidJobsMu.Unlock()
		return
	}
	job.Status = "success"
	job.Message = fmt.Sprintf("导入完成：成功 %d / 失败 %d", job.Done, job.Failed)
	gcidJobsMu.Unlock()
	if len(failedList) > 0 {
		job.Message += "；" + joinShort(failedList)
	}
	sendGcidNotify(fmt.Sprintf("📦 光鸭 GCID 导入完成\n成功：%d\n失败：%d\n%s", job.Done, job.Failed, joinShort(failedList)))
}

// GcidJobStatusAPI GET /api/guangya/gcid-jobs/:id
func GcidJobStatusAPI(c *gin.Context) {
	snapshot, ok := gcidJobSnapshot(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "任务不存在或已过期"})
		return
	}
	// 不回传服务器绝对路径
	snapshot.File = ""
	c.JSON(http.StatusOK, APIResponse[*gcidJob]{Code: Success, Data: snapshot})
}

// DownloadGcidExportAPI GET /api/guangya/gcid-export/download/:id — 下载导出的 JSON
func DownloadGcidExportAPI(c *gin.Context) {
	gcidJobsMu.Lock()
	job, ok := gcidJobs[c.Param("id")]
	file := ""
	if ok {
		file = job.File
	}
	gcidJobsMu.Unlock()
	if !ok || file == "" {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "导出文件不存在"})
		return
	}
	c.FileAttachment(file, filepath.Base(file))
}

// gcidJobSnapshot 锁内拷贝任务快照（字段读写与后台 goroutine 隔离）
func gcidJobSnapshot(id string) (*gcidJob, bool) {
	gcidJobsMu.Lock()
	defer gcidJobsMu.Unlock()
	job, ok := gcidJobs[id]
	if !ok {
		return nil, false
	}
	clone := *job
	return &clone, true
}

// parseGcidDoc 兼容两种形态：完整文档对象 / 纯条目数组
func parseGcidDoc(text string) (*gcid.GcidExportDoc, error) {
	trimmed := strings.TrimSpace(text)
	var doc gcid.GcidExportDoc
	if err := json.Unmarshal([]byte(trimmed), &doc); err == nil && len(doc.Items) > 0 {
		return &doc, nil
	}
	var items []gcid.GcidItem
	if err := json.Unmarshal([]byte(trimmed), &items); err == nil && len(items) > 0 {
		return &gcid.GcidExportDoc{Version: 1, Provider: "guangya", Items: items}, nil
	}
	return nil, fmt.Errorf("未识别到 items 条目")
}

// sendGcidNotify GCID 任务通知（走已配置的通知渠道，含 TG 机器人）
func sendGcidNotify(content string) {
	if notificationmanager.GlobalEnhancedNotificationManager == nil {
		return
	}
	notif := &models.Notification{
		Type:      models.TransferSuccess,
		Title:     "光鸭 GCID 秒传",
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
		helpers.AppLogger.Errorf("GCID 通知发送失败：%v", err)
	}
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	replaced := make([]rune, 0, len(name))
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			replaced = append(replaced, '_')
			continue
		}
		replaced = append(replaced, r)
	}
	out := string(replaced)
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		out = "dir"
	}
	return out
}

func joinShort(items []string) string {
	if len(items) == 0 {
		return ""
	}
	const max = 5
	if len(items) > max {
		return fmt.Sprintf("%s 等 %d 项", strings.Join(items[:max], "；"), len(items))
	}
	return strings.Join(items, "；")
}
