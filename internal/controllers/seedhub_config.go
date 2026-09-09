package controllers

import (
	"net/http"

	"diy-strm/internal/seedhub"

	"github.com/gin-gonic/gin"
)

// SeedHub 配置端点

// GetSeedhubConfigAPI GET /api/seedhub/config（令牌不回传，只回是否已配置）
func GetSeedhubConfigAPI(c *gin.Context) {
	cfg, ok := seedhub.GetConfig()
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"configured": ok,
		"api_url":    cfg.APIURL,
		"has_token":  cfg.Token != "",
		"hint":       "SeedHub 资源源：网盘分享只读展示，磁力/ED2K 可复制与离线",
	}})
}

// SaveSeedhubConfigAPI POST /api/seedhub/config {api_url, token}
func SaveSeedhubConfigAPI(c *gin.Context) {
	var req struct {
		APIURL string `json:"api_url"`
		Token  string `json:"token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	// 令牌留空 = 保持原值（前端脱敏约定）
	if req.Token == "" {
		if old, ok := seedhub.GetConfig(); ok {
			req.Token = old.Token
		}
	}
	if err := seedhub.SaveConfig(req.APIURL, req.Token); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存 SeedHub 配置失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "SeedHub 配置已保存（令牌本机加密存储）"})
}
