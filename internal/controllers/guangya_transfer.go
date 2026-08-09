package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"diy-strm/internal/guangyapan"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// ---------- 开发者配置 ----------

type GuangYaDeveloperSettingRequest struct {
	AccountID    uint   `json:"account_id" binding:"required"`
	ClientID     string `json:"client_id" binding:"required"`
	ClientSecret string `json:"client_secret" binding:"required"`
}

// GuangYaDeveloperSetting 保存光鸭开发者凭据
func GuangYaDeveloperSetting(c *gin.Context) {
	var req GuangYaDeveloperSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号不是光鸭云盘账号", Data: nil})
		return
	}
	setting := &models.GuangYaDeveloperSetting{
		AccountID:    req.AccountID,
		ClientID:     strings.TrimSpace(req.ClientID),
		ClientSecret: strings.TrimSpace(req.ClientSecret),
		Enabled:      true,
	}
	if err := models.SaveGuangYaDeveloperSetting(setting); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存开发者配置失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "开发者配置已保存", Data: nil})
}

// GuangYaDeveloperSettingQuery 查询光鸭开发者配置（secret 脱敏）
func GuangYaDeveloperSettingQuery(c *gin.Context) {
	accountID, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：account_id 无效", Data: nil})
		return
	}
	setting, err := models.GetGuangYaDeveloperSetting(uint(accountID))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询开发者配置失败：" + err.Error(), Data: nil})
		return
	}
	if setting == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{"account_id": accountID, "client_id": "", "configured": false}})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: gin.H{
		"account_id":  setting.AccountID,
		"client_id":   setting.ClientID,
		"configured":  true,
		"secret_hint": maskSecret(setting.ClientSecret),
	}})
}

func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return strings.Repeat("*", len(secret))
	}
	return secret[:4] + strings.Repeat("*", len(secret)-8) + secret[len(secret)-4:]
}

// GuangYaDeveloperSettingDelete 删除光鸭开发者配置
func GuangYaDeveloperSettingDelete(c *gin.Context) {
	accountID, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：account_id 无效", Data: nil})
		return
	}
	if err := models.DeleteGuangYaDeveloperSetting(uint(accountID)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除开发者配置失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "开发者配置已删除", Data: nil})
}

// ---------- 接收 TOKEN ----------

type GuangYaReceiverTokenRequest struct {
	AccountID uint   `json:"account_id" binding:"required"`
	TokenID   string `json:"token_id" binding:"required"`
	Remark    string `json:"remark"`
}

// GuangYaReceiverTokensCreate 添加接收 TOKEN
func GuangYaReceiverTokensCreate(c *gin.Context) {
	var req GuangYaReceiverTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	token := &models.GuangYaReceiverToken{
		AccountID: req.AccountID,
		TokenID:   strings.TrimSpace(req.TokenID),
		Remark:    req.Remark,
	}
	if err := models.CreateGuangYaReceiverToken(token); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加接收 TOKEN 失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "接收 TOKEN 已添加", Data: token})
}

// GuangYaReceiverTokensList 列出接收 TOKEN
func GuangYaReceiverTokensList(c *gin.Context) {
	accountID, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：account_id 无效", Data: nil})
		return
	}
	tokens, err := models.GetGuangYaReceiverTokens(uint(accountID))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询接收 TOKEN 失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: tokens})
}

// GuangYaReceiverTokensDelete 删除接收 TOKEN
func GuangYaReceiverTokensDelete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：id 无效", Data: nil})
		return
	}
	if err := models.DeleteGuangYaReceiverToken(uint(id)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除接收 TOKEN 失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "接收 TOKEN 已删除", Data: nil})
}

// ---------- 小号秒传任务 ----------

type GuangYaSmallTransferRequest struct {
	AccountID       uint   `json:"account_id" binding:"required"`
	ReceiverTokenID uint   `json:"receiver_token_id" binding:"required"`
	FileIDs         []string `json:"file_ids" binding:"required,min=1"`
	FileNames       []string `json:"file_names"`
}

// GuangYaSmallTransfer 创建小号秒传任务并异步执行
func GuangYaSmallTransfer(c *gin.Context) {
	var req GuangYaSmallTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.FileIDs) > guangyapan.DeveloperMaxItems {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("单次最多秒传 %d 个文件", guangyapan.DeveloperMaxItems), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if account.SourceType != models.SourceTypeGuangYaPan {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号不是光鸭云盘账号", Data: nil})
		return
	}
	setting, err := models.GetGuangYaDeveloperSetting(req.AccountID)
	if err != nil || setting == nil || !setting.Enabled || setting.ClientID == "" || setting.ClientSecret == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先配置光鸭开发者 client_id 和 client_secret", Data: nil})
		return
	}
	receiver, err := models.GetGuangYaReceiverToken(req.ReceiverTokenID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "接收 TOKEN 不存在", Data: nil})
		return
	}
	if receiver.AccountID != req.AccountID {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "接收 TOKEN 不属于该账号", Data: nil})
		return
	}
	if !smallTransferLocks.tryLock(req.AccountID, req.ReceiverTokenID) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号与接收 TOKEN 已有任务在执行中，请稍后再试", Data: nil})
		return
	}
	defer smallTransferLocks.unlock(req.AccountID, req.ReceiverTokenID)

	fileIDsJSON, _ := json.Marshal(req.FileIDs)
	fileNamesJSON, _ := json.Marshal(req.FileNames)
	task := &models.GuangYaTransferTask{
		AccountID:       req.AccountID,
		ReceiverTokenID: req.ReceiverTokenID,
		ReceiverToken:   receiver.TokenID,
		FileIDs:         string(fileIDsJSON),
		FileNames:       string(fileNamesJSON),
		Status:          models.GuangYaTransferStatusRunning,
		TotalCount:      len(req.FileIDs),
		StartedAt:       time.Now(),
	}
	if err := models.CreateGuangYaTransferTask(task); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "创建秒传任务失败：" + err.Error(), Data: nil})
		return
	}
	go runGuangYaSmallTransfer(task.ID, setting.ClientID, setting.ClientSecret)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "小号秒传任务已创建", Data: gin.H{"task_id": task.ID}})
}

// GuangYaSmallTransferList 任务列表
func GuangYaSmallTransferList(c *gin.Context) {
	accountID, err := strconv.ParseUint(c.Query("account_id"), 10, 64)
	if err != nil || accountID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：account_id 无效", Data: nil})
		return
	}
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	tasks, err := models.GetGuangYaTransferTasks(uint(accountID), limit)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询秒传任务失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: tasks})
}

// GuangYaSmallTransferDelete 删除任务记录
func GuangYaSmallTransferDelete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：id 无效", Data: nil})
		return
	}
	task, err := models.GetGuangYaTransferTask(uint(id))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "任务不存在", Data: nil})
		return
	}
	if task.Status == models.GuangYaTransferStatusRunning || task.Status == models.GuangYaTransferStatusAuditing {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "任务执行中，无法删除", Data: nil})
		return
	}
	if err := models.DeleteGuangYaTransferTask(uint(id)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除任务失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "任务已删除", Data: nil})
}

// ---------- 后台执行 ----------

// smallTransferLock 同一账号+接收 TOKEN 的任务互斥
type smallTransferLockMap struct {
	mu    sync.Mutex
	holds map[string]bool
}

var smallTransferLocks = &smallTransferLockMap{holds: make(map[string]bool)}

func (m *smallTransferLockMap) key(accountID, tokenID uint) string {
	return fmt.Sprintf("%d-%d", accountID, tokenID)
}

func (m *smallTransferLockMap) tryLock(accountID, tokenID uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.key(accountID, tokenID)
	if m.holds[key] {
		return false
	}
	m.holds[key] = true
	return true
}

func (m *smallTransferLockMap) unlock(accountID, tokenID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.holds, m.key(accountID, tokenID))
}

// runGuangYaSmallTransfer 执行小号秒传：upload_by_fileid →（需预审则 pre_upload 等待）→ upload_status 轮询
func runGuangYaSmallTransfer(taskID uint, clientID, clientSecret string) {
	client := guangyapan.NewDeveloperClient(clientID, clientSecret)
	ctx := context.Background()
	fail := func(msg string) {
		helpers.AppLogger.Errorf("光鸭小号秒传任务 %d 失败：%s", taskID, msg)
		now := time.Now()
		_ = models.UpdateGuangYaTransferTask(taskID, map[string]interface{}{
			"status":        models.GuangYaTransferStatusFailed,
			"error_message": truncateString(msg, 1000),
			"finished_at":   &now,
		})
	}
	finishSuccess := func(success, skipped, failed int, taskIDStr string) {
		now := time.Now()
		_ = models.UpdateGuangYaTransferTask(taskID, map[string]interface{}{
			"status":        models.GuangYaTransferStatusSuccess,
			"task_id":       taskIDStr,
			"success_count": success,
			"skipped_count": skipped,
			"failed_count":  failed,
			"finished_at":   &now,
		})
		helpers.AppLogger.Infof("光鸭小号秒传任务 %d 完成：成功 %d，跳过 %d，失败 %d", taskID, success, skipped, failed)
	}

	task, err := models.GetGuangYaTransferTask(taskID)
	if err != nil {
		fail("任务不存在：" + err.Error())
		return
	}
	var fileIDs []string
	if err := json.Unmarshal([]byte(task.FileIDs), &fileIDs); err != nil {
		fail("解析文件列表失败：" + err.Error())
		return
	}

	taskIDStr, err := client.UploadByFileID(ctx, task.ReceiverToken, fileIDs)
	if err != nil {
		var apiErr *guangyapan.DeveloperAPIError
		if !asDeveloperAPIError(err, &apiErr) {
			fail(err.Error())
			return
		}
		switch apiErr.Code {
		case guangyapan.DeveloperCodeAlreadySent:
			// 幂等：文件已传过，全部跳过
			finishSuccess(0, len(fileIDs), 0, "")
			return
		case guangyapan.DeveloperCodeNeedPreAudit:
			// 需要预审
			preTaskID, err := client.PreUpload(ctx, task.ReceiverToken, fileIDs)
			if err != nil {
				fail("提交预审失败：" + err.Error())
				return
			}
			_ = models.UpdateGuangYaTransferTask(taskID, map[string]interface{}{
				"status":      models.GuangYaTransferStatusAuditing,
				"pre_task_id": preTaskID,
			})
			preResult := waitPreUploadStatus(client, ctx, preTaskID)
			switch preResult {
			case "passed":
			case "failed":
				fail("文件预审未通过")
				return
			default:
				fail("预审超时（请到光鸭云盘查看预审结果后重试）")
				return
			}
			taskIDStr, err = client.UploadByFileID(ctx, task.ReceiverToken, fileIDs)
			if err != nil {
				var againErr *guangyapan.DeveloperAPIError
				if asDeveloperAPIError(err, &againErr) && againErr.Code == guangyapan.DeveloperCodeAlreadySent {
					finishSuccess(0, len(fileIDs), 0, "")
					return
				}
				fail("预审通过后上传失败：" + err.Error())
				return
			}
		default:
			fail(err.Error())
			return
		}
	}

	_ = models.UpdateGuangYaTransferTask(taskID, map[string]interface{}{"task_id": taskIDStr})

	// 轮询上传状态
	interval := 1500 * time.Millisecond
	for i := 0; i < 400; i++ {
		select {
		case <-ctx.Done():
			fail("任务已取消")
			return
		case <-time.After(interval):
		}
		status, err := client.UploadStatus(ctx, taskIDStr)
		if err != nil {
			var apiErr *guangyapan.DeveloperAPIError
			if asDeveloperAPIError(err, &apiErr) && apiErr.Retryable() {
				continue
			}
			fail("查询上传状态失败：" + err.Error())
			return
		}
		if status.Status == "success" {
			finishSuccess(status.SuccessCount, status.SkippedCount, status.FailedCount, taskIDStr)
			return
		}
		if status.Status == "failed" {
			fail(fmt.Sprintf("上传任务处理失败（成功 %d，跳过 %d，失败 %d）", status.SuccessCount, status.SkippedCount, status.FailedCount))
			return
		}
	}
	fail("查询上传状态超时，请稍后在光鸭云盘中确认结果")
}

// waitPreUploadStatus 轮询预审状态，返回 passed / failed / timeout
func waitPreUploadStatus(client *guangyapan.DeveloperClient, ctx context.Context, preTaskID string) string {
	interval := 3 * time.Second
	for i := 0; i < 1200; i++ {
		select {
		case <-ctx.Done():
			return "timeout"
		case <-time.After(interval):
		}
		status, msg, err := client.PreUploadStatus(ctx, preTaskID)
		if err != nil {
			continue
		}
		switch status {
		case 3:
			helpers.AppLogger.Infof("光鸭预审任务 %s 已通过", preTaskID)
			return "passed"
		case 4:
			helpers.AppLogger.Warnf("光鸭预审任务 %s 未通过：%s", preTaskID, msg)
			return "failed"
		}
	}
	return "timeout"
}

func asDeveloperAPIError(err error, target **guangyapan.DeveloperAPIError) bool {
	if err == nil {
		return false
	}
	apiErr, ok := err.(*guangyapan.DeveloperAPIError)
	if ok {
		*target = apiErr
	}
	return ok
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
