package controllers

import (
	"context"
	"net/http"

	"diy-strm/internal/embyclient-rest-go"
	"diy-strm/internal/models"
	"diy-strm/internal/vlibrary"

	"github.com/gin-gonic/gin"
)

// Emby 虚拟库/榜单合集端点（对齐 tgto123 ranking_virtual_libraries 语义）

// GetVLibrarySettingsAPI GET /api/emby302/virtual-library — 设置读取
func GetVLibrarySettingsAPI(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[*vlibrary.Settings]{Code: Success, Data: vlibrary.GetSettings()})
}

// SaveVLibrarySettingsAPI POST /api/emby302/virtual-library — 设置保存
func SaveVLibrarySettingsAPI(c *gin.Context) {
	var cfg vlibrary.Settings
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error()})
		return
	}
	// 合并保留既有合集 ID 缓存（防止重复创建 Emby 合集）
	existing := vlibrary.GetSettings()
	cfg.Collections = existing.Collections
	if err := vlibrary.SaveSettings(&cfg); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存虚拟库设置失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[*vlibrary.Settings]{Code: Success, Message: "虚拟库设置已保存", Data: vlibrary.GetSettings()})
}

// SyncVLibraryAPI POST /api/emby302/virtual-library/sync — 按榜单创建/更新 Emby 合集
func SyncVLibraryAPI(c *gin.Context) {
	cfg := vlibrary.GetSettings()
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "虚拟库未启用，请先在设置中开启"})
		return
	}
	if models.GlobalEmbyConfig == nil || models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请先在 Emby 设置中配置服务器地址与 API Key"})
		return
	}
	client := embyclientrestgo.NewClient(models.GlobalEmbyConfig.EmbyUrl, models.GlobalEmbyConfig.EmbyApiKey)
	results, err := vlibrary.SyncCollections(context.Background(), client)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "榜单合集同步完成", Data: gin.H{"results": results}})
}
