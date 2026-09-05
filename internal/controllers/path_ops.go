package controllers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"diy-strm/internal/baidupan"
	"diy-strm/internal/models"
	"diy-strm/internal/requests"

	"github.com/gin-gonic/gin"
)

// RenameFile 重命名网盘文件或目录。
func RenameFile(c *gin.Context) {
	var req requests.RenameFileRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if req.SourceType == models.SourceTypeLocal {
		if err := renameLocalFile(req.FileID, req.NewName); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重命名失败：" + err.Error(), Data: nil})
			return
		}
		invalidateNetFileCacheForPath(models.SourceTypeLocal, 0, req.ParentID)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "重命名成功", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if err := renameNetdiskFile(account, req.FileID, req.NewName); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重命名失败：" + err.Error(), Data: nil})
		return
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "重命名成功", Data: nil})
}

// renameLocalFile 本地文件/目录重命名：同目录内 os.Rename
func renameLocalFile(fileID string, newName string) error {
	if strings.TrimSpace(fileID) == "" || filepath.Clean(fileID) == "/" {
		return fmt.Errorf("非法的本地路径")
	}
	if _, err := os.Stat(fileID); err != nil {
		return fmt.Errorf("文件不存在：%s", fileID)
	}
	target := filepath.Join(filepath.Dir(fileID), newName)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("同名文件已存在：%s", newName)
	}
	return os.Rename(fileID, target)
}

// MoveFile 移动网盘文件或目录到目标目录。
func MoveFile(c *gin.Context) {
	var req requests.MoveFileRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if req.SourceType == models.SourceTypeLocal {
		if err := moveLocalFile(req.FileID, req.TargetParentID); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "移动失败：" + err.Error(), Data: nil})
			return
		}
		invalidateNetFileCacheForPath(models.SourceTypeLocal, 0, req.ParentID)
		invalidateNetFileCacheForPath(models.SourceTypeLocal, 0, req.TargetParentID)
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "移动成功", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if err := moveNetdiskFile(account, req.FileID, req.TargetParentID); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "移动失败：" + err.Error(), Data: nil})
		return
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.TargetParentID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "移动成功", Data: nil})
}

// moveLocalFile 本地文件/目录移动：跨目录 os.Rename，目标不允许存在同名项
func moveLocalFile(fileID string, targetParentID string) error {
	if strings.TrimSpace(fileID) == "" || strings.TrimSpace(targetParentID) == "" {
		return fmt.Errorf("非法的本地路径")
	}
	if filepath.Clean(fileID) == "/" || filepath.Clean(targetParentID) == "/" {
		return fmt.Errorf("不允许操作根目录")
	}
	info, err := os.Stat(fileID)
	if err != nil {
		return fmt.Errorf("文件不存在：%s", fileID)
	}
	targetInfo, err := os.Stat(targetParentID)
	if err != nil || !targetInfo.IsDir() {
		return fmt.Errorf("目标目录不存在：%s", targetParentID)
	}
	target := filepath.Join(targetParentID, filepath.Base(filepath.Clean(fileID)))
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("目标目录已存在同名文件：%s", filepath.Base(target))
	}
	if err := os.Rename(fileID, target); err != nil {
		// 同一文件不能作为自身的移动目标（如把目录移进其自身子目录）
		if info.IsDir() && strings.HasPrefix(target, filepath.Clean(fileID)+string(filepath.Separator)) {
			return fmt.Errorf("不能把目录移动到其自身子目录内")
		}
		return err
	}
	return nil
}

// renameNetdiskFile 按账号类型重命名网盘文件或目录。
// fileID 语义与文件列表中的 Id 一致：115/123 为文件 ID，百度/OpenList 为完整路径。
func renameNetdiskFile(account *models.Account, fileID string, newName string) error {
	ctx := context.Background()
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err := client.ReName(ctx, fileID, newName)
		return err
	case models.SourceType123:
		client := account.Get123Client()
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		parentPath := normalizeOpenListPath(path.Dir(normalizeOpenListPath(fileID)))
		if parentPath == "" || parentPath == "." {
			parentPath = "/"
		}
		return client.Rename(parentPath, path.Base(fileID), newName)
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("获取中国移动云盘客户端失败")
		}
		return client.Rename(ctx, fileID, newName)
	case models.SourceTypeGuangYaPan:
		return fmt.Errorf("该网盘暂不支持重命名")
	default:
		return fmt.Errorf("不支持的文件系统")
	}
}

// moveNetdiskFile 按账号类型移动网盘文件或目录到目标目录。
func moveNetdiskFile(account *models.Account, fileID string, targetParentID string) error {
	ctx := context.Background()
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err := client.Move(ctx, []string{fileID}, targetParentID)
		return err
	case models.SourceType123:
		client := account.Get123Client()
		return client.Move(ctx, fileID, targetParentID)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		return client.MoveBatch(ctx, []baidupan.MoveOrCopyItem{{
			Path:    fileID,
			Dest:    targetParentID,
			NewName: path.Base(normalizeOpenListPath(fileID)),
		}})
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		oldPath := normalizeOpenListPath(path.Dir(normalizeOpenListPath(fileID)))
		if oldPath == "" || oldPath == "." {
			oldPath = "/"
		}
		return client.Move(oldPath, normalizeOpenListPath(targetParentID), []string{path.Base(fileID)})
	case models.SourceTypePan139:
		client := account.GetPan139Client()
		if client == nil {
			return fmt.Errorf("获取中国移动云盘客户端失败")
		}
		return client.MoveBatch(ctx, []string{fileID}, targetParentID)
	case models.SourceTypeGuangYaPan:
		return fmt.Errorf("该网盘暂不支持移动")
	default:
		return fmt.Errorf("不支持的文件系统")
	}
}
