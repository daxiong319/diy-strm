package controllers

import (
	"net/http"

	"diy-strm/internal/danmu"

	"github.com/gin-gonic/gin"
)

// 弹幕服务配置（Misaka Danmaku 对接，对齐 tgto123 DANMAKU_API_URL/KEY 语义）

// GetDanmuConfigAPI GET /api/danmu/config
func GetDanmuConfigAPI(c *gin.Context) {
	cfg, configured := danmu.GetConfig()
	resp := gin.H{
		"api_url":    cfg.APIURL,
		"api_key":    cfg.APIKey,
		"configured": configured,
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: resp})
}

// SaveDanmuConfigAPI POST /api/danmu/config {api_url, api_key}
func SaveDanmuConfigAPI(c *gin.Context) {
	var req struct {
		APIURL string `json:"api_url"`
		APIKey string `json:"api_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误"})
		return
	}
	if err := danmu.SaveConfig(req.APIURL, req.APIKey); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存弹幕配置失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "弹幕配置已保存；302 播放将自动导入下一集弹幕"})
}
