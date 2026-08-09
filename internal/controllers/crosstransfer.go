package controllers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

const (
	crossTransferMaxFiles  = 2000
	crossTransferMaxDepth  = 10
	crossTransferTempDir   = "cross-transfer"
	crossTransferMaxItems  = 500
	crossTransferMaxRapid  = 32 * 1024 * 1024 // 百度单分片秒传上限
	crossTransferBaiduCap  = 32 * 1024 * 1024
)

// crossTransferScanFile 扫描到的源文件
type crossTransferScanFile struct {
	SourceFileID string `json:"source_file_id"`
	DownloadID   string `json:"download_id"` // 源文件下载定位 ID（115 pickcode/百度 fs_id/其他文件 ID）
	RelPath      string `json:"rel_path"`
	RelDir       string `json:"rel_dir"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Sha1         string `json:"sha1"`
	Md5          string `json:"md5"`
}

// CrossTransferScanRequest 跨盘秒传扫描请求
type CrossTransferScanRequest struct {
	AccountID uint   `json:"account_id" binding:"required"`
	Path      string `json:"path" binding:"required"`
}

// CrossTransferScan 跨盘秒传扫描：递归扫描源目录并提取文件指纹。
func CrossTransferScan(c *gin.Context) {
	var req CrossTransferScanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}

	var files []crossTransferScanFile
	counter := 0
	truncated := false
	var scan func(ctx context.Context, parentID string, parentPath string, depth int) error
	scan = func(ctx context.Context, parentID string, parentPath string, depth int) error {
		if depth > crossTransferMaxDepth || truncated {
			return nil
		}
		items, err := listAllNetDir(ctx, account, parentID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if counter >= crossTransferMaxFiles {
				truncated = true
				return nil
			}
			fullPath := parentPath + "/" + item.Name
			if parentPath == "" {
				fullPath = item.Name
			}
			if item.IsDirectory {
				if err := scan(ctx, item.Id, fullPath, depth+1); err != nil {
					return err
				}
				continue
			}
			counter++
			file := crossTransferScanFile{
				SourceFileID: item.Id,
				DownloadID:   item.Id,
				RelPath:      fullPath,
				RelDir:       strings.TrimSuffix(parentPath, "/"),
				Name:         item.Name,
				Size:         item.Size,
			}
			switch account.SourceType {
			case models.SourceType115:
				file.Sha1 = item.Sha1
				if item.PickCode != "" {
					file.DownloadID = item.PickCode
				}
			case models.SourceType123:
				file.Md5 = item.Md5
			case models.SourceTypeBaiduPan:
				file.Md5 = item.Md5
				if item.FsId != "" {
					file.DownloadID = item.FsId
				}
			}
			files = append(files, file)
		}
		return nil
	}
	if err := scan(c.Request.Context(), req.Path, "", 1); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "扫描目录失败：" + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "扫描完成", Data: map[string]any{
		"files":     files,
		"total":     len(files),
		"truncated": truncated,
	}})
}

// CrossTransferExecuteRequest 跨盘秒传执行请求
type CrossTransferExecuteRequest struct {
	SourceAccountID uint                          `json:"source_account_id" binding:"required"`
	TargetAccountID uint                          `json:"target_account_id" binding:"required"`
	SourcePath      string                        `json:"source_path" binding:"required"`
	TargetPath      string                        `json:"target_path" binding:"required"`
	Conflict        string                        `json:"conflict"` // skip / rename / overwrite
	Files           []crossTransferExecuteItem    `json:"files" binding:"required"`
}

type crossTransferExecuteItem struct {
	SourceFileID string `json:"source_file_id"`
	DownloadID   string `json:"download_id"`
	RelPath      string `json:"rel_path"`
	RelDir       string `json:"rel_dir"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Sha1         string `json:"sha1"`
	Md5          string `json:"md5"`
}

type crossTransferItemResult struct {
	RelPath string `json:"rel_path"`
	Name    string `json:"name"`
	Mode    string `json:"mode"` // rapid / relay / skip / error
	Success bool   `json:"success"`
	FileID  string `json:"file_id"`
	Error   string `json:"error"`
}

// CrossTransferExecute 跨盘秒传执行：目标网盘按指纹尝试秒传，未命中则入队中转上传。
func CrossTransferExecute(c *gin.Context) {
	var req CrossTransferExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	sourceAccount, err := models.GetAccountById(req.SourceAccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取源账号失败：" + err.Error(), Data: nil})
		return
	}
	targetAccount, err := models.GetAccountById(req.TargetAccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取目标账号失败：" + err.Error(), Data: nil})
		return
	}
	if len(req.Files) > crossTransferMaxItems {
		req.Files = req.Files[:crossTransferMaxItems]
	}
	switch targetAccount.SourceType {
	case models.SourceTypePan139:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该网盘暂不支持秒传或中转上传", Data: nil})
		return
	}

	ctx := context.Background()
	dirCache := map[string]string{}
	results := make([]crossTransferItemResult, 0, len(req.Files))
	rapidCount := 0
	relayCount := 0
	skipCount := 0
	failCount := 0

	for _, file := range req.Files {
		result := crossTransferItemResult{
			RelPath: file.RelPath,
			Name:    file.Name,
		}
		// 1. 确保目标目录存在
		targetDirID, err := ensureOrganizeDir(ctx, targetAccount, req.TargetPath, "", file.RelDir, dirCache)
		if err != nil {
			result.Mode = "error"
			result.Error = "创建目标目录失败：" + err.Error()
			failCount++
			results = append(results, result)
			continue
		}
		// 2. 按目标网盘类型尝试秒传
		reuse, fileID, rapidErr := tryCrossTransferRapid(ctx, targetAccount, targetDirID, file, req.Conflict)
		if rapidErr != "" {
			result.Mode = "error"
			result.Error = rapidErr
			failCount++
			results = append(results, result)
			continue
		}
		if reuse {
			result.Mode = "rapid"
			result.Success = true
			result.FileID = fileID
			rapidCount++
			results = append(results, result)
			continue
		}
		// 3. 未命中，入队中转上传
		if err := enqueueCrossTransferRelay(req, sourceAccount, targetAccount, targetDirID, file); err != nil {
			result.Mode = "error"
			result.Error = "创建中转上传任务失败：" + err.Error()
			failCount++
			results = append(results, result)
			continue
		}
		result.Mode = "relay"
		relayCount++
		results = append(results, result)
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("跨盘传输完成：秒传 %d 个，中转 %d 个，失败 %d 个", rapidCount, relayCount, failCount), Data: map[string]any{
		"results": results,
		"rapid":   rapidCount,
		"relay":   relayCount,
		"skipped": skipCount,
		"failed":  failCount,
	}})
}

// tryCrossTransferRapid 按目标网盘类型尝试指纹秒传。
func tryCrossTransferRapid(ctx context.Context, targetAccount *models.Account, targetDirID string, file crossTransferExecuteItem, conflict string) (reuse bool, fileID string, errMsg string) {
	switch targetAccount.SourceType {
	case models.SourceType115:
		if file.Sha1 == "" {
			return false, "", ""
		}
		client := targetAccount.Get115Client()
		if client == nil {
			return false, "", "目标账号 115 客户端不存在"
		}
		ok, id, err := client.RapidUploadBySHA1(ctx, targetDirID, file.Name, file.Size, file.Sha1)
		if err != nil {
			return false, "", "115 秒传失败：" + err.Error()
		}
		return ok, id, ""
	case models.SourceType123:
		if file.Md5 == "" {
			return false, "", ""
		}
		client := targetAccount.Get123Client()
		if client == nil {
			return false, "", "目标账号 123 云盘客户端不存在"
		}
		duplicate := 2
		if conflict == "rename" {
			duplicate = 1
		}
		ok, id, err := client.RapidUploadByHash(ctx, targetDirID, file.Name, file.Size, file.Md5, duplicate)
		if err != nil {
			return false, "", "123 云盘秒传失败：" + err.Error()
		}
		return ok, id, ""
	case models.SourceTypeBaiduPan:
		if file.Md5 == "" || file.Size > crossTransferBaiduCap {
			return false, "", ""
		}
		client := targetAccount.GetBaiDuPanClient()
		if client == nil {
			return false, "", "目标账号百度网盘客户端不存在"
		}
		remotePath := strings.TrimRight(strings.TrimSpace(targetDirID), "/") + "/" + file.Name
		ok, id, err := client.RapidUploadByMD5(ctx, remotePath, file.Size, file.Md5)
		if err != nil {
			return false, "", "百度网盘秒传失败：" + err.Error()
		}
		return ok, id, ""
	case models.SourceTypeOpenList:
		return false, "", ""
	case models.SourceTypeGuangYaPan:
		// 光鸭秒传需要本地文件计算 GCID，直接入队中转，上传阶段内部自动尝试秒传
		return false, "", ""
	default:
		return false, "", ""
	}
}

// enqueueCrossTransferRelay 创建跨盘中转上传任务：源网盘下载 → 目标网盘上传。
func enqueueCrossTransferRelay(req CrossTransferExecuteRequest, sourceAccount *models.Account, targetAccount *models.Account, targetDirID string, file crossTransferExecuteItem) error {
	task := models.DbUploadTask{
		Source:            models.UploadSourceCrossTransfer,
		AccountId:         targetAccount.ID,
		SourceType:        targetAccount.SourceType,
		SourceAccountId:   sourceAccount.ID,
		SourceFileId:      file.DownloadID,
		RemoteFileId:      targetDirID,
		RemotePathId:      targetDirID,
		RelativePath:      file.RelDir,
		FileName:          file.Name,
		FileSize:          file.Size,
		Status:            models.UploadStatusPending,
		UploadedBytes:     0,
		LocalFullPath:     "",
	}
	if err := models.CreateUploadTask(&task); err != nil {
		return err
	}
	tempDir := filepath.Join(os.TempDir(), crossTransferTempDir)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return fmt.Errorf("创建临时目录失败：%w", err)
	}
	localPath := filepath.Join(tempDir, fmt.Sprintf("%d-%s", task.ID, file.Name))
	if err := models.UpdateUploadTaskLocalPath(task.ID, localPath); err != nil {
		return err
	}
	task.LocalFullPath = localPath
	return nil
}
