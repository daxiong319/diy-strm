package controllers

import (
	"net/http"
	"strconv"

	"diy-strm/internal/models"

	"github.com/gin-gonic/gin"
)

// Emby 302 播放记录（对齐 tgto123 /api/emby_proxy/playback_records 形状）

// GetPlaybackRecordsAPI GET /api/emby302/playback-records?id=&page=&page_size=
func GetPlaybackRecordsAPI(c *gin.Context) {
	ruleID := c.Query("id")
	page := queryIntDefault(c, "page", 1)
	pageSize := queryIntDefault(c, "page_size", 30)
	records, total, err := models.ListEmbyPlaybackRecords(ruleID, page, pageSize)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询播放记录失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Data: gin.H{
		"items":     records,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}})
}

// queryIntDefault 读取整型 query 参数（非法回退默认值）
func queryIntDefault(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}
