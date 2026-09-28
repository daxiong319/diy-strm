package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"diy-strm/internal/casengine"
	"diy-strm/internal/cloud189"
	"diy-strm/internal/db"
	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/pan139"
	"diy-strm/internal/quark"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// CAS 播放自动恢复（对齐 cloud-auto-save-x WorkerPlanePrepareCASRestore 模式）
// 流程：STRM URL(/cas/play/video.mkv?cas_id=N) → 查指纹库
//   → 源文件不存在 → 重放秒传恢复到 .cas 同目录
//   → 拿恢复后文件的 302 直链 → 播放
//   → 延时（默认 2h）删除恢复的文件（.cas 文件保留）
// ---------------------------------------------------------------------------

// GetCasPlayUrl GET /cas/play/*filename — CAS 播放恢复直链
func GetCasPlayUrl(c *gin.Context) {
	// emby302 反代链路中间件会固化 gin 的 queryCache，实时解析 RawQuery
	liveQuery, _ := url.ParseQuery(c.Request.URL.RawQuery)
	casID := strings.TrimSpace(liveQuery.Get("cas_id"))
	if casID == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "cas_id：不能为空", Data: nil})
		return
	}
	id, err := strconv.ParseUint(casID, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "cas_id 无效", Data: nil})
		return
	}

	// 1. 查 CAS 清单记录
	rec, err := casengine.GetRecordByID(uint(id))
	if err != nil || rec == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "CAS 记录不存在", Data: nil})
		return
	}

	// 2. 定位账号
	account, err := models.GetAccountById(rec.AccountID)
	if err != nil || account == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "CAS 记录对应的账号不存在", Data: nil})
		return
	}

	// 3. 解析直链（源存在直接用；不存在先秒传恢复）
	directURL, restored, err := casResolvePlayURL(c.Request.Context(), account, rec)
	if err != nil {
		helpers.AppLogger.Warnf("CAS 播放解析失败：cas_id=%d file=%s 错误：%v", rec.ID, rec.FileName, err)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "CAS 播放失败：" + err.Error(), Data: nil})
		return
	}

	// 4. 恢复成功：登记延时删除任务（.cas 保留）
	if restored {
		go scheduleCASRestoreCleanup(rec)
		helpers.AppLogger.Infof("CAS 播放恢复成功：cas_id=%d file=%s 已登记延时清理", rec.ID, rec.FileName)
	}

	// 5. 302 直链
	helpers.AppLogger.Infof("CAS 播放 302：cas_id=%d file=%s restored=%v", rec.ID, rec.FileName, restored)
	c.Redirect(http.StatusFound, directURL)
}

// casResolvePlayURL 解析 CAS 播放直链。返回 (直链, 是否执行了恢复, 错误)
func casResolvePlayURL(ctx context.Context, account *models.Account, rec *casengine.CasManifestRecord) (string, bool, error) {
	switch account.SourceType {
	case models.SourceTypeCloud189:
		return casResolvePlayCloud189(ctx, account, rec)
	case models.SourceTypePan139:
		return casResolvePlayPan139(ctx, account, rec)
	case models.SourceTypeQuark:
		return casResolvePlayQuark(ctx, account, rec)
	default:
		return "", false, fmt.Errorf("CAS 播放不支持网盘类型：%s", account.SourceType)
	}
}

// casResolvePlayCloud189 天翼：探测源文件 → 在则直链 / 不在则秒传恢复
func casResolvePlayCloud189(ctx context.Context, account *models.Account, rec *casengine.CasManifestRecord) (string, bool, error) {
	client := account.GetCloud189Client()
	if client == nil {
		return "", false, fmt.Errorf("天翼客户端不可用")
	}
	// 探测源文件是否还在原目录
	exists, fileID := cloud189ProbeFile(ctx, client, rec)
	if exists {
		dl, err := client.GetDownloadLink(ctx, fileID, "")
		if err != nil {
			return "", false, err
		}
		return dl, false, nil
	}
	// 秒传恢复到原目录（familyID 传 "0" 个人云）
	newFileID, err := client.RapidUpload(ctx, rec.RemotePath, rec.FileName, rec.FileSize, rec.FileMd5, rec.SliceMd5, "0")
	if err != nil {
		return "", false, fmt.Errorf("秒传恢复失败（指纹可能失效）：%w", err)
	}
	dl, err := client.GetDownloadLink(ctx, newFileID, "")
	if err != nil {
		return "", true, err
	}
	return dl, true, nil
}

// casResolvePlayPan139 移动云盘：探测 → 直链 / 秒传恢复（SHA256）
func casResolvePlayPan139(ctx context.Context, account *models.Account, rec *casengine.CasManifestRecord) (string, bool, error) {
	client := account.GetPan139Client()
	defer client.Close()
	// 探测源文件
	exists, fileID := pan139ProbeFile(ctx, client, rec)
	if exists {
		dl, err := client.GetDownloadURL(ctx, fileID)
		if err != nil {
			return "", false, err
		}
		return dl, false, nil
	}
	// 秒传恢复：139 create 接口 contentHash=SHA256（reader nil = 秒传模式）
	newFileID, _, _, err := client.UploadFile(ctx, rec.RemotePath, rec.FileName, rec.FileSize, rec.Sha256, nil, nil)
	if err != nil {
		return "", false, fmt.Errorf("秒传恢复失败（指纹可能失效）：%w", err)
	}
	dl, err := client.GetDownloadURL(ctx, newFileID)
	if err != nil {
		return "", true, err
	}
	return dl, true, nil
}

// casResolvePlayQuark 夸克：探测 → 直链 / 秒传恢复（preHash）
func casResolvePlayQuark(ctx context.Context, account *models.Account, rec *casengine.CasManifestRecord) (string, bool, error) {
	client := account.GetQuarkClient()
	if client == nil {
		return "", false, fmt.Errorf("夸克客户端不可用")
	}
	// 探测源文件
	exists, fileID := quarkProbeFile(ctx, client, rec)
	if exists {
		dl, err := client.GetDownloadURL(ctx, fileID)
		if err != nil {
			return "", false, err
		}
		return dl, false, nil
	}
	// 秒传恢复（夸克 2 步秒传用 preHash）
	newFileID, err := client.RapidUpload(ctx, rec.RemotePath, rec.FileName, rec.FileSize, rec.PreHash)
	if err != nil {
		return "", false, fmt.Errorf("秒传恢复失败（指纹可能失效）：%w", err)
	}
	dl, err := client.GetDownloadURL(ctx, newFileID)
	if err != nil {
		return "", true, err
	}
	return dl, true, nil
}

// cloud189ProbeFile 天翼探测源文件（列父目录找同名/同 ID 文件）
func cloud189ProbeFile(ctx context.Context, client *cloud189.Client, rec *casengine.CasManifestRecord) (bool, string) {
	files, err := client.ListFiles(ctx, rec.RemotePath)
	if err != nil {
		return false, ""
	}
	for _, f := range files {
		if f.ID == rec.RemoteFileID || f.Name == rec.FileName {
			return true, f.ID
		}
	}
	return false, ""
}

// pan139ProbeFile 移动云盘探测源文件
func pan139ProbeFile(ctx context.Context, client *pan139.Client, rec *casengine.CasManifestRecord) (bool, string) {
	files, err := client.GetFiles(ctx, rec.RemotePath)
	if err != nil {
		return false, ""
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if f.FileID == rec.RemoteFileID || f.FileName == rec.FileName {
			return true, f.FileID
		}
	}
	return false, ""
}

// quarkProbeFile 夸克探测源文件
func quarkProbeFile(ctx context.Context, client *quark.Client, rec *casengine.CasManifestRecord) (bool, string) {
	files, err := client.ListFiles(ctx, rec.RemotePath)
	if err != nil {
		return false, ""
	}
	for _, f := range files {
		if f.IsDir {
			continue
		}
		if f.Fid == rec.RemoteFileID || f.Name == rec.FileName {
			return true, f.Fid
		}
	}
	return false, ""
}

// scheduleCASRestoreCleanup 延时清理：CASDelayDeleteHours 小时后删恢复的源文件（.cas 保留）
func scheduleCASRestoreCleanup(rec *casengine.CasManifestRecord) {
	delayHours := 2
	if cfg, err := models.GetAutoOrganizeConfigByAccount(rec.AccountID); err == nil && cfg != nil && cfg.CASDelayDeleteHours > 0 {
		delayHours = cfg.CASDelayDeleteHours
	}
	time.Sleep(time.Duration(delayHours) * time.Hour)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := casengine.DeleteRestoredSource(cleanupCtx, rec.AccountID, rec.RemoteFileID); err != nil {
		helpers.AppLogger.Warnf("CAS 延时清理失败：cas_id=%d file=%s：%v", rec.ID, rec.FileName, err)
	} else {
		helpers.AppLogger.Infof("CAS 延时清理完成：cas_id=%d file=%s 已删源（.cas 保留）", rec.ID, rec.FileName)
		// 记录状态回到 active（源已删、待下次恢复）
		db.Db.Model(&casengine.CasManifestRecord{}).Where("id = ?", rec.ID).Updates(map[string]any{
			"status":     "active",
			"deleted_at": time.Now().Unix(),
		})
	}
}

// ---------------------------------------------------------------------------
// 天翼/夸克直链 handler（STRM 播放代理，与 /pan139/url 同模式）
// ---------------------------------------------------------------------------

// GetCloud189Url GET /cloud189/url/*filename — 天翼云盘直链
func GetCloud189Url(c *gin.Context) {
	liveQuery, _ := url.ParseQuery(c.Request.URL.RawQuery)
	fileId := strings.TrimSpace(liveQuery.Get("pickcode"))
	if fileId == "" {
		fileId = strings.TrimSpace(liveQuery.Get("fileid"))
	}
	if fileId == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "pickcode：不能为空", Data: nil})
		return
	}
	accID := strings.TrimSpace(liveQuery.Get("account"))
	var account *models.Account
	var err error
	if accID != "" {
		var id uint64
		id, err = strconv.ParseUint(accID, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 无效", Data: nil})
			return
		}
		account, err = models.GetAccountById(uint(id))
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
			return
		}
	} else {
		account = findAccountByType(models.SourceTypeCloud189)
		if account == nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未配置天翼云盘账号", Data: nil})
			return
		}
	}
	client := account.GetCloud189Client()
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "天翼云盘客户端不可用", Data: nil})
		return
	}
	cacheKey := "cloud189url:" + fileId
	if !keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取天翼云盘下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)
	cachedUrl := string(db.Cache.Get(cacheKey))
	if cachedUrl == "" {
		cachedUrl, err = client.GetDownloadLink(c.Request.Context(), fileId, "")
		if err != nil {
			helpers.AppLogger.Warnf("获取天翼云盘下载链接失败：fileId=%s 错误：%v", fileId, err)
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取天翼云盘下载链接失败：" + err.Error(), Data: nil})
			return
		}
		// 天翼直链有效期较短，缓存 10 分钟
		db.Cache.Set(cacheKey, []byte(cachedUrl), 10*60)
	}
	helpers.AppLogger.Infof("302 重定向到天翼云盘下载链接：fileId=%s", fileId)
	c.Redirect(http.StatusFound, cachedUrl)
}

// GetQuarkUrl GET /quark/url/*filename — 夸克网盘直链
func GetQuarkUrl(c *gin.Context) {
	liveQuery, _ := url.ParseQuery(c.Request.URL.RawQuery)
	fileId := strings.TrimSpace(liveQuery.Get("pickcode"))
	if fileId == "" {
		fileId = strings.TrimSpace(liveQuery.Get("fileid"))
	}
	if fileId == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "pickcode：不能为空", Data: nil})
		return
	}
	accID := strings.TrimSpace(liveQuery.Get("account"))
	var account *models.Account
	var err error
	if accID != "" {
		var id uint64
		id, err = strconv.ParseUint(accID, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 无效", Data: nil})
			return
		}
		account, err = models.GetAccountById(uint(id))
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
			return
		}
	} else {
		account = findAccountByType(models.SourceTypeQuark)
		if account == nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未配置夸克网盘账号", Data: nil})
			return
		}
	}
	client := account.GetQuarkClient()
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "夸克网盘客户端不可用", Data: nil})
		return
	}
	cacheKey := "quarkurl:" + fileId
	if !keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取夸克下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)
	cachedUrl := string(db.Cache.Get(cacheKey))
	if cachedUrl == "" {
		cachedUrl, err = client.GetDownloadURL(c.Request.Context(), fileId)
		if err != nil {
			helpers.AppLogger.Warnf("获取夸克下载链接失败：fileId=%s 错误：%v", fileId, err)
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取夸克下载链接失败：" + err.Error(), Data: nil})
			return
		}
		db.Cache.Set(cacheKey, []byte(cachedUrl), 10*60)
	}
	helpers.AppLogger.Infof("302 重定向到夸克下载链接：fileId=%s", fileId)
	c.Redirect(http.StatusFound, cachedUrl)
}
