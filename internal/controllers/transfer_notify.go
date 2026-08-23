package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"diy-strm/internal/helpers"
	"diy-strm/internal/models"
	"diy-strm/internal/notificationmanager"
)

// transferNotifTitle 通知用资源标题：优先订阅 TMDB 标题，否则回退帖子文本（截断）
func transferNotifTitle(sub *models.CloudSubscription, fallback string) string {
	if sub.TMDBTitle != "" {
		return sub.TMDBTitle
	}
	t := strings.TrimSpace(fallback)
	if len([]rune(t)) > 60 {
		r := []rune(t)
		return string(r[:60]) + "..."
	}
	return t
}

// sendTransferSuccessNotification 发送转存成功通知（TG 频道订阅 / 影巢资源订阅共用）
// extra：附加信息（如 "剧集：S01E26"、"洗版更新"），为空时省略
func sendTransferSuccessNotification(sourceType, title, targetDir string, total int, extra string) {
	content := fmt.Sprintf("资源：%s\n转存：%d 项\n目标目录：%s\n来源：%s", title, total, targetDir, parseSourceTypeName(sourceType))
	if extra != "" {
		content += "\n" + extra
	}
	notif := &models.Notification{
		Type:      models.TransferSuccess,
		Title:     "✅ 转存成功",
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送转存成功通知失败：%v", err)
		}
	}
}

// sendTransferFailedNotification 发送转存失败通知（TG 频道订阅 / 影巢资源订阅共用）
func sendTransferFailedNotification(sourceType, title, targetDir, reason string) {
	content := fmt.Sprintf("资源：%s\n目标目录：%s\n来源：%s\n原因：%s", title, targetDir, parseSourceTypeName(sourceType), reason)
	notif := &models.Notification{
		Type:      models.TransferFailed,
		Title:     "❌ 转存失败",
		Content:   content,
		Timestamp: time.Now(),
		Priority:  models.NormalPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(context.Background(), notif); err != nil {
			helpers.AppLogger.Errorf("发送转存失败通知失败：%v", err)
		}
	}
}
