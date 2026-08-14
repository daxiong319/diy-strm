package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
)

// GetCloudSettings 查询云盘转存目录设置（?source_type=xxx）
func GetCloudSettings(c *gin.Context) {
	sourceType := strings.TrimSpace(c.Query("source_type"))
	if sourceType == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少参数 source_type", Data: nil})
		return
	}
	vals := models.GetCloudSaveDirWithDefault(sourceType, "/")
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: vals})
}

// SetCloudSettingAPI 设置云盘转存目录（POST {source_type, key, value}）
func SetCloudSettingAPI(c *gin.Context) {
	var req struct {
		SourceType string `json:"source_type"`
		Key        string `json:"key"`
		Value      string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.SourceType == "" || req.Key == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少 source_type 或 key", Data: nil})
		return
	}
	if err := models.SetCloudSaveDir(req.SourceType, req.Key, strings.TrimSpace(req.Value)); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "保存失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "保存成功", Data: nil})
}

// ListCloudSubscriptionsAPI 订阅列表（?source_type=xxx 可选）
func ListCloudSubscriptionsAPI(c *gin.Context) {
	subs, err := models.ListCloudSubscriptions(strings.TrimSpace(c.Query("source_type")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "查询失败：" + err.Error(), Data: nil})
		return
	}
	for i := range subs {
		subs[i].OldCount = models.CountSuperseded(subs[i].ID)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: subs})
}

// CreateCloudSubscriptionAPI 新增订阅（POST {source_type, channel, keywords, target_dir, enabled}）
func CreateCloudSubscriptionAPI(c *gin.Context) {
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	body, _ := json.Marshal(raw)
	var req models.CloudSubscription
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	// 布尔开关默认值（未显式传 false 时）
	if _, ok := raw["auto_finish"]; !ok {
		req.AutoFinish = true
	}
	if _, ok := raw["replace_old"]; !ok {
		req.ReplaceOld = true
	}
	if !ensureSourceTypeValid(req.SourceType) {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "不支持的网盘类型：" + req.SourceType, Data: nil})
		return
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" || !strings.HasPrefix(channel, "@") {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "频道名必须以 @ 开头，例如 @dianying", Data: nil})
		return
	}
	req.Channel = channel
	req.Keywords = strings.TrimSpace(req.Keywords)
	if err := models.CreateCloudSubscription(&req); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "创建失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已创建", Data: req})
}

// UpdateCloudSubscriptionAPI 更新订阅（PUT /cloud/subscriptions/:id，仅更新请求中出现的字段）
func UpdateCloudSubscriptionAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	var raw map[string]json.RawMessage
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	body, _ := json.Marshal(raw)
	var req models.CloudSubscription
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	fields := map[string]bool{}
	for k := range raw {
		fields[k] = true
	}
	if err := models.UpdateCloudSubscription(id, &req, fields); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "更新失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已更新", Data: nil})
}

// DeleteCloudSubscriptionAPI 删除订阅（DELETE /cloud/subscriptions/:id）
func DeleteCloudSubscriptionAPI(c *gin.Context) {
	id := strToUint(c.Param("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	if err := models.DeleteCloudSubscription(id); err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "删除失败：" + err.Error(), Data: nil})
		return
	}
	_ = models.DeleteSubscriptionRecords(id)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "订阅已删除", Data: nil})
}

// CleanOldVersionsAPI 清理订阅的旧版本（共存模式下手动删除被洗版替换的文件）
// POST /cloud/subscriptions/clean-old {id}
func CleanOldVersionsAPI(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ID == 0 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "无效的订阅 ID", Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(req.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	recs := models.SupersededRecords(sub.ID)
	if len(recs) == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "没有待清理的旧版本", Data: nil})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	deleted := 0
	var errs []string
	var removedIDs []uint
	for _, r := range recs {
		n, err := deleteOldFilesByTitle(ctx, sub.SourceType, r.TargetDir, r.Title)
		if err != nil {
			errs = append(errs, fmt.Sprintf("「%s」清理失败：%v", r.Title, err))
			continue
		}
		deleted += n
		removedIDs = append(removedIDs, r.ID)
	}
	if len(removedIDs) > 0 {
		if err := models.DeleteTransferRecords(removedIDs); err != nil {
			errs = append(errs, fmt.Sprintf("清理记录失败：%v", err))
		}
	}
	msg := fmt.Sprintf("已清理 %d 个旧版本文件，移除 %d 条旧记录", deleted, len(removedIDs))
	if len(errs) > 0 {
		msg += "；失败：" + strings.Join(errs, "；")
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
}

// PreviewChannelAPI 预览频道最近内容（POST /cloud/subscriptions/preview {channel, limit}）
func PreviewChannelAPI(c *gin.Context) {
	var req struct {
		Channel string `json:"channel"`
		Limit   int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	channel := strings.TrimSpace(req.Channel)
	if channel == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少频道名", Data: nil})
		return
	}
	posts, err := PreviewChannel(channel, req.Limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "抓取失败：" + err.Error(), Data: nil})
		return
	}
	list := make([]gin.H, 0, len(posts))
	for _, p := range posts {
		links := make([]string, 0, len(p.Links))
		for _, l := range p.Links {
			links = append(links, fmt.Sprintf("%s（%s）", l.URL, parseSourceTypeName(l.Type)))
		}
		list = append(list, gin.H{
			"post_id": p.PostID,
			"text":    p.Text,
			"time":    p.Time.Format("2006-01-02 15:04"),
			"links":   links,
		})
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "抓取成功", Data: list})
}

// RunSubscriptionAPI 手动立即执行一条订阅（POST /cloud/subscriptions/run {id}）
func RunSubscriptionAPI(c *gin.Context) {
	var req struct {
		ID uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误：" + err.Error(), Data: nil})
		return
	}
	sub, err := models.GetCloudSubscription(req.ID)
	if err != nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "订阅不存在", Data: nil})
		return
	}
	msg, _ := RunSubscriptionOnce(sub)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: msg, Data: nil})
}

// ListChannelJobsPlaceholder 占位：订阅任务状态列表
func ListChannelJobsPlaceholder(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "查询成功", Data: []gin.H{}})
}

// TestPan123Account 测试 123 账号连通性
func TestPan123Account(c *gin.Context) {
	ctx := c.Request.Context()
	var account models.Account
	if err := db.Db.Where("source_type = ?", models.SourceType123).Order("id asc").First(&account).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未配置 123 云盘账号", Data: nil})
		return
	}
	client := account.Get123Client()
	defer client.Close()
	info, err := client.GetUserInfo(ctx)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "测试失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("连接成功，账号：%s", info.Data.Nickname), Data: nil})
}
