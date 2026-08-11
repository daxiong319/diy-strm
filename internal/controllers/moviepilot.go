package controllers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/models"
	"diy-strm/internal/moviepilot"
	"diy-strm/internal/requests"
)

// GetMoviePilotConfig 获取 MoviePilot 配置
// @Summary 获取 MoviePilot 配置
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetMoviePilotConfig(c *gin.Context) {
	cfg := models.LoadMoviePilotConfig()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 MoviePilot 配置成功", Data: cfg})
}

// UpdateMoviePilotConfig 更新 MoviePilot 配置
// @Summary 更新 MoviePilot 配置
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateMoviePilotConfig(c *gin.Context) {
	var req requests.UpdateMoviePilotConfigRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	cfg, ok := models.UpdateMoviePilotConfig(&models.MoviePilotConfig{
		Enabled:       req.Enabled,
		BaseUrl:       req.BaseUrl,
		ApiToken:      req.ApiToken,
		DownloadRoot:  req.DownloadRoot,
		LocalViewRoot: req.LocalViewRoot,
		UploadRoot:    req.UploadRoot,
		PollInterval:  req.PollInterval,
		SyncPathId:    req.SyncPathId,
		NotifyEnabled: req.NotifyEnabled,
	})
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新 MoviePilot 配置失败", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "MoviePilot 配置已更新", Data: cfg})
}

// TestMoviePilotConnection 测试 MoviePilot 连接
// @Summary 测试 MoviePilot 连接
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /setting/moviepilot/test [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func TestMoviePilotConnection(c *gin.Context) {
	var req requests.TestMoviePilotConnectionRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	client := moviepilot.NewClient(req.BaseUrl, req.ApiToken)
	ctx, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	if err := client.TestConnection(ctx); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "连接成功", Data: nil})
}

// ListMoviePilotSubscribes 查询 MoviePilot 订阅列表
// @Summary 查询 MoviePilot 订阅列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotSubscribes(c *gin.Context) {
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	subs, err := client.ListSubscribes(c)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取订阅列表成功", Data: subs})
}

// CreateMoviePilotSubscribe 添加订阅
// @Summary 添加 MoviePilot 订阅
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CreateMoviePilotSubscribe(c *gin.Context) {
	var req requests.CreateMoviePilotSubscribeRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	id, err := client.CreateSubscribe(c, &moviepilot.CreateSubscribeRequest{
		Name:         req.Name,
		Year:         req.Year,
		Type:         req.Type,
		TmdbId:       req.TmdbId,
		Season:       req.Season,
		TotalEpisode: req.TotalEpisode,
		SavePath:     req.SavePath,
		Sites:        req.Sites,
	})
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "添加订阅成功", Data: map[string]any{"id": id}})
}

// SearchMoviePilotSubscribe 触发订阅搜索
// @Summary 触发 MoviePilot 订阅搜索
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id/search [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SearchMoviePilotSubscribe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.SearchSubscribe(c, id); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已触发订阅搜索，请稍候查看下载任务", Data: nil})
}

// DeleteMoviePilotSubscribe 删除订阅
// @Summary 删除 MoviePilot 订阅
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id [delete]
// @Security JwtAuth
// @Security ApiKeyAuth
func DeleteMoviePilotSubscribe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.DeleteSubscribe(c, id); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除订阅成功", Data: nil})
}

// UpdateMoviePilotSubscribeStatus 更新订阅状态
// @Summary 更新 MoviePilot 订阅状态
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/subscribes/:id/status [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateMoviePilotSubscribeStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "订阅 ID 无效", Data: nil})
		return
	}
	var req requests.UpdateMoviePilotSubscribeStatusRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	if err := client.UpdateSubscribeStatus(c, id, req.State); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新订阅状态成功", Data: nil})
}

// ListMoviePilotDownloads 查询 MoviePilot 下载任务列表
// @Summary 查询 MoviePilot 下载任务列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/downloads [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotDownloads(c *gin.Context) {
	client, ok := moviePilotClientFromConfig(c)
	if !ok {
		return
	}
	downloads, err := client.ListDownloads(c)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取下载任务成功", Data: downloads})
}

// ListMoviePilotUploadTasks 查询 139 上传任务列表
// @Summary 查询 MoviePilot 上传任务列表
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func ListMoviePilotUploadTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	status := c.DefaultQuery("status", "")
	tasks, total := models.ListMoviePilotUploadTasks(page, pageSize, status)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取上传任务成功", Data: map[string]any{"list": tasks, "total": total}})
}

// RetryMoviePilotUploadTask 重试上传任务
// @Summary 重试 MoviePilot 上传任务
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks/:id/retry [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func RetryMoviePilotUploadTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "任务 ID 无效", Data: nil})
		return
	}
	if !moviepilot.RetryUploadTask(uint(id)) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重试失败（仅失败/已取消任务可重试）", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已加入重试队列", Data: nil})
}

// CancelMoviePilotUploadTask 取消上传任务
// @Summary 取消 MoviePilot 上传任务
// @Tags MoviePilot
// @Success 200 {object} APIResponse[any]
// @Router /moviepilot/upload-tasks/:id/cancel [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CancelMoviePilotUploadTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "任务 ID 无效", Data: nil})
		return
	}
	if !moviepilot.CancelUploadTask(uint(id)) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "取消失败（上传中的任务无法取消）", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已取消", Data: nil})
}

// moviePilotClientFromConfig 从配置创建 MP 客户端；失败时已写响应并返回 false
func moviePilotClientFromConfig(c *gin.Context) (*moviepilot.Client, bool) {
	cfg := models.LoadMoviePilotConfig()
	if cfg.BaseUrl == "" || cfg.ApiToken == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先配置 MoviePilot 地址和 API Token", Data: nil})
		return nil, false
	}
	client := moviepilot.NewClient(cfg.BaseUrl, cfg.ApiToken)
	if client == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "MoviePilot 客户端创建失败", Data: nil})
		return nil, false
	}
	return client, true
}