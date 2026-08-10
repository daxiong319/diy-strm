package controllers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// Pan139ShareInfoRequest 查询分享链接
type Pan139ShareInfoRequest struct {
	AccountID uint   `json:"account_id" binding:"required"`
	LinkID    string `json:"link_id" binding:"required"`
	Passwd    string `json:"passwd"`
	CaID      string `json:"ca_id"` // 分享内目录 ID（根目录 root）
}

// Pan139ShareSaveRequest 转存分享
type Pan139ShareSaveRequest struct {
	AccountID       uint     `json:"account_id" binding:"required"`
	LinkID          string   `json:"link_id" binding:"required"`
	Passwd          string   `json:"passwd"`
	TargetCatalogID string   `json:"target_catalog_id" binding:"required"`
	FilePaths       []string `json:"file_paths"`   // 文件 path 列表（parentID/fileID）
	DirPaths        []string `json:"dir_paths"`    // 目录 path 列表
	WaitVisible     bool     `json:"wait_visible"` // 是否轮询等待转存完成（默认 false）
}

// Pan139ShareInfo 查询 139 分享链接的根目录/子目录内容
func Pan139ShareInfo(c *gin.Context) {
	var req Pan139ShareInfoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if account.SourceType != models.SourceTypePan139 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号不是中国移动云盘账号", Data: nil})
		return
	}
	client := account.GetPan139Client()
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取中国移动云盘客户端失败", Data: nil})
		return
	}
	caID := strings.TrimSpace(req.CaID)
	if caID == "" {
		caID = "root"
	}
	info, err := client.ListShareDir(c, req.LinkID, req.Passwd, caID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询分享链接失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: info})
}

// Pan139ShareSave 转存分享文件/目录到目标目录
func Pan139ShareSave(c *gin.Context) {
	var req Pan139ShareSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if len(req.FilePaths) == 0 && len(req.DirPaths) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请选择要转存的文件或目录", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	if account.SourceType != models.SourceTypePan139 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该账号不是中国移动云盘账号", Data: nil})
		return
	}
	client := account.GetPan139Client()
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取中国移动云盘客户端失败", Data: nil})
		return
	}
	taskID, err := client.SaveShareFiles(c, req.LinkID, req.Passwd, req.TargetCatalogID, req.FilePaths, req.DirPaths)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "转存失败：" + err.Error(), Data: nil})
		return
	}
	result := gin.H{"task_id": taskID}
	if req.WaitVisible {
		names := make([]string, 0)
		for _, path := range append(append([]string{}, req.FilePaths...), req.DirPaths...) {
			name := path
			if idx := strings.LastIndex(path, "/"); idx >= 0 && idx < len(path)-1 {
				name = path[idx+1:]
			}
			names = append(names, name)
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
		defer cancel()
		visible, missing, waitErr := client.WaitShareFilesVisible(ctx, req.TargetCatalogID, names, 20*time.Second)
		if waitErr != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "转存已提交，但校验完成状态失败：" + waitErr.Error(), Data: result})
			return
		}
		result["visible"] = visible
		result["missing"] = missing
		if len(missing) > 0 {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("转存已提交，已出现 %d/%d 个文件，剩余可能仍在处理中", visible, len(names)), Data: result})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "转存完成", Data: result})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "转存任务已提交", Data: result})
}
