package controllers

import (
	"context"
	"fmt"
	"net/http"
	"path"

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
	case models.SourceTypeGuangYaPan, models.SourceTypePan139:
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
	case models.SourceTypeGuangYaPan, models.SourceTypePan139:
		return fmt.Errorf("该网盘暂不支持移动")
	default:
		return fmt.Errorf("不支持的文件系统")
	}
}
