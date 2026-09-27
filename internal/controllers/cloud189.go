package controllers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/casengine"
	"diy-strm/internal/cloud189"
	"diy-strm/internal/db"
	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// 天翼云盘（cloud189）账号接入 + CAS 秒传体系 API
// ---------------------------------------------------------------------------

// Cloud189LoginAPI POST /api/cloud189/login — 账号密码登录（返回需验证码时带 captcha_image）
func Cloud189LoginAPI(c *gin.Context) {
	var req struct {
		Name         string `json:"name"`
		Username     string `json:"username" binding:"required"`
		Password     string `json:"password" binding:"required"`
		ValidateCode string `json:"validate_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	client := cloud189.NewClient(req.Username, req.Password, "")
	res, err := client.LoginByPassword(c.Request.Context(), req.Username, req.Password, req.ValidateCode)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "登录失败：" + err.Error()})
		return
	}
	if res.NeedCaptcha {
		c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "需要验证码", Data: gin.H{
			"need_captcha":  true,
			"captcha_image": res.CaptchaImage,
		}})
		return
	}
	if !res.Success || res.Session == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "登录失败：" + res.Message})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Username
	}
	account := models.Account{
		Name:              name,
		SourceType:        models.SourceTypeCloud189,
		Username:          req.Username,
		Password:          req.Password,
		Token:             res.Session.AccessToken,
		RefreshToken:      res.Session.RefreshToken,
		TokenExpiriesTime: time.Now().Add(6 * 24 * time.Hour).Unix(),
	}
	if err := createAccountIfAbsent(&account); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存账号失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "登录成功", Data: gin.H{"account_id": account.ID, "username": req.Username}})
}

// Cloud189LoginByCookieAPI POST /api/cloud189/login-cookie — SSON Cookie 登录
func Cloud189LoginByCookieAPI(c *gin.Context) {
	var req struct {
		Name   string `json:"name"`
		Cookie string `json:"cookie" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	client := cloud189.NewClient("", "", req.Cookie)
	ctx := c.Request.Context()
	// 触发会话验证（SSON 登录链）
	if _, err := client.GetSessionKey(ctx); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "SSON 登录失败：" + err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "天翼云盘(SSON)"
	}
	account := models.Account{
		Name:       name,
		SourceType: models.SourceTypeCloud189,
		Token:      req.Cookie, // SSON 存 Token 字段
	}
	if err := createAccountIfAbsent(&account); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存账号失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "登录成功", Data: gin.H{"account_id": account.ID}})
}

// Cloud189FilesAPI GET /api/cloud189/files?account_id=&folder_id= — 文件列表
func Cloud189FilesAPI(c *gin.Context) {
	account, ok := mustCloud189Account(c)
	if !ok {
		return
	}
	folderID := c.DefaultQuery("folder_id", "-11")
	files, err := account.GetCloud189Client().ListFiles(c.Request.Context(), folderID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{"items": files}})
}

// ---------------------------------------------------------------------------
// CAS 体系
// ---------------------------------------------------------------------------

// CasRunOnceAPI POST /api/cas/run — 手动触发一轮 CAS 化
func CasRunOnceAPI(c *gin.Context) {
	generated, deleted, skipped, failed, err := casengine.RunOnce(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "CAS 化完成", Data: gin.H{
		"generated": generated, "deleted": deleted, "skipped": skipped, "failed": failed,
	}})
}

// CasRecordsAPI GET /api/cas/records?page=&page_size=&status=&keyword=
func CasRecordsAPI(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	rows, total, err := casengine.ListRecords(page, pageSize, c.Query("status"), c.Query("keyword"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{"items": rows, "total": total}})
}

// CasRestoreTextAPI POST /api/cas/restore — 粘贴 CAS 文本一键秒传恢复
func CasRestoreTextAPI(c *gin.Context) {
	var req struct {
		AccountID      uint   `json:"account_id" binding:"required"`
		TargetFolderID string `json:"target_folder_id"`
		CasContent     string `json:"cas_content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	res, err := casengine.RestoreFromCasText(c.Request.Context(), req.AccountID, req.TargetFolderID, req.CasContent)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "秒传恢复失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "秒传成功", Data: gin.H{"file_id": res.FileID, "file_name": res.FileName, "record_id": res.RecordID}})
}

// CasRestoreRecordAPI POST /api/cas/records/:id/restore — 从记录恢复
func CasRestoreRecordAPI(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var req struct {
		TargetFolderID string `json:"target_folder_id"`
	}
	_ = c.ShouldBindJSON(&req)
	res, err := casengine.RestoreFromRecord(c.Request.Context(), uint(id), req.TargetFolderID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "秒传恢复失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "秒传成功", Data: gin.H{"file_id": res.FileID, "file_name": res.FileName, "record_id": res.RecordID}})
}

// CasExportAPI GET /api/cas/records/:id/export — 导出 .cas 文件下载
func CasExportAPI(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	fileName, content, err := casengine.ExportRecordText(uint(id))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	if !cloud189.IsCasFileName(fileName) {
		fileName += ".cas"
	}
	c.Header("Content-Disposition", "attachment; filename=\""+fileName+"\"")
	c.Data(http.StatusOK, "application/json", []byte(content))
}

// CasDeleteRecordAPI DELETE /api/cas/records/:id
func CasDeleteRecordAPI(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := casengine.DeleteRecord(uint(id)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "已删除"})
}

// CasConfigAPI GET/POST /api/cas/config — CAS 自动化配置
func CasConfigAPI(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		cfg := casengine.GetConfigForAPI()
		c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{"enabled": cfg.Enabled, "age_days": cfg.AgeDays, "write_back_cloud": cfg.WriteBackCloud}})
		return
	}
	var cfg casengine.CasConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	casengine.SaveConfig(cfg)
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "已保存"})
}

func mustCloud189Account(c *gin.Context) (*models.Account, bool) {
	id, _ := strconv.Atoi(c.Query("account_id"))
	if id == 0 {
		var req struct {
			AccountID uint `json:"account_id"`
		}
		_ = c.ShouldBindJSON(&req)
		id = int(req.AccountID)
	}
	account, err := models.GetAccountById(uint(id))
	if err != nil || account == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号不存在"})
		return nil, false
	}
	if account.SourceType != models.SourceTypeCloud189 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号不是天翼云盘"})
		return nil, false
	}
	return account, true
}

// createAccountIfAbsent 创建账号（name 唯一去重）
func createAccountIfAbsent(account *models.Account) error {
	var count int64
	db.Db.Model(&models.Account{}).Where("name = ?", account.Name).Count(&count)
	if count > 0 {
		account.Name = account.Name + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	return db.Db.Create(account).Error
}
