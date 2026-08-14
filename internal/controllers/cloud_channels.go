package controllers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/models"
)

// ListCloudChannelsAPI 云盘频道列表（GET /cloud/channels?source_type=xxx）
func ListCloudChannelsAPI(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Query("source_type"))
	if sourceType == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少参数 source_type", Data: nil})
		return
	}
	list, err := models.ListCloudChannels(sourceType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: list})
}

// CreateCloudChannelAPI 添加云盘频道（POST /cloud/channels {source_type, channel}）
// channel 支持 https://t.me/xxx、t.me/xxx、@xxx 三种写法
func CreateCloudChannelAPI(c *gin.Context) {
	var req struct {
		SourceType string `json:"source_type"`
		Channel    string `json:"channel"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	req.SourceType = strings.TrimSpace(req.SourceType)
	req.Channel = strings.TrimSpace(req.Channel)
	if !ensureSourceTypeValid(req.SourceType) {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "不支持的网盘类型：" + req.SourceType, Data: nil})
		return
	}
	if req.Channel == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少频道链接或频道名", Data: nil})
		return
	}
	name := normalizeChannelName(req.Channel)
	if name == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "频道名无效，示例：https://t.me/xxx 或 @xxx", Data: nil})
		return
	}
	ch := models.CloudChannel{SourceType: req.SourceType, Channel: name, Enabled: true}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}
	if err := models.SaveCloudChannel(&ch); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "添加失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "频道已添加", Data: ch})
}

// UpdateCloudChannelAPI 更新频道（PUT /cloud/channels/:id {enabled}）
func UpdateCloudChannelAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的频道 ID", Data: nil})
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	ch, err := models.GetCloudChannel(id)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "频道不存在", Data: nil})
		return
	}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}
	if err := models.SaveCloudChannel(ch); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "频道已更新", Data: nil})
}

// DeleteCloudChannelAPI 删除频道（DELETE /cloud/channels/:id）
func DeleteCloudChannelAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的频道 ID", Data: nil})
		return
	}
	if err := models.DeleteCloudChannel(id); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "频道已删除", Data: nil})
}

// normalizeChannelName 归一化频道名：https://t.me/xxx、t.me/xxx、@xxx → xxx
func normalizeChannelName(name string) string {
	name = strings.TrimSpace(name)
	for _, p := range []string{"https://t.me/s/", "https://t.me/", "http://t.me/s/", "http://t.me/", "t.me/s/", "t.me/", "@"} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimRight(name, "/")
	if strings.ContainsAny(name, " /?#@") {
		return ""
	}
	return name
}
