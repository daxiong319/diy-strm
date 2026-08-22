package controllers

import (
	"fmt"
	"strings"

	"diy-strm/internal/models"
)

// ---------------------------------------------------------------------------
// 监控历史埋点工具（TG 频道订阅 / 影巢订阅 / TG 机器人三类入口共用）
// 与 tgto123 对齐：每条监控转存尝试（成功/失败/跳过）各写一条审计记录
// ---------------------------------------------------------------------------

// buildTGMessageURL 由频道名 + 帖子 ID 构造消息链接
func buildTGMessageURL(channel, postID string) string {
	channel = strings.TrimPrefix(strings.TrimPrefix(channel, "@"), "https://t.me/")
	channel = strings.TrimSuffix(channel, "/")
	if channel == "" || postID == "" {
		return ""
	}
	return "https://t.me/" + channel + "/" + postID
}

// recordMonitorSuccess 记录一次成功转存
func recordMonitorSuccess(entry, sourceType, channel, messageID, messageURL, targetURL, title, targetDir string, total int, subID uint) {
	models.CreateMonitorTransferRecord(&models.MonitorTransferRecord{
		SourceType:     sourceType,
		Entry:          entry,
		Channel:        channel,
		MessageID:      messageID,
		MessageURL:     messageURL,
		TargetURL:      targetURL,
		TransferStatus: models.MonitorStatusSuccess,
		TransferResult: fmt.Sprintf("已转存「%s」共 %d 项到 %s", title, total, targetDir),
		Title:          title,
		Total:          total,
		TargetDir:      targetDir,
		SubscriptionID: subID,
	})
}

// recordMonitorWash 记录一次洗版替换转存
func recordMonitorWash(entry, sourceType, channel, messageID, messageURL, targetURL, title, targetDir string, total int, subID uint, washTarget string) {
	models.CreateMonitorTransferRecord(&models.MonitorTransferRecord{
		SourceType:     sourceType,
		Entry:          entry,
		Channel:        channel,
		MessageID:      messageID,
		MessageURL:     messageURL,
		TargetURL:      targetURL,
		TransferStatus: models.MonitorStatusWash,
		TransferResult: fmt.Sprintf("洗版命中，已转存更高规格「%s」共 %d 项到 %s（目标 %s）", title, total, targetDir, washTarget),
		Title:          title,
		Total:          total,
		TargetDir:      targetDir,
		SubscriptionID: subID,
	})
}

// recordMonitorFailed 记录一次转存失败
func recordMonitorFailed(entry, sourceType, channel, messageID, messageURL, targetURL, targetDir string, subID uint, reason error) {
	msg := ""
	if reason != nil {
		msg = reason.Error()
	}
	models.CreateMonitorTransferRecord(&models.MonitorTransferRecord{
		SourceType:     sourceType,
		Entry:          entry,
		Channel:        channel,
		MessageID:      messageID,
		MessageURL:     messageURL,
		TargetURL:      targetURL,
		TransferStatus: models.MonitorStatusFailed,
		TransferResult: msg,
		TargetDir:      targetDir,
		SubscriptionID: subID,
	})
}

// recordMonitorSkipped 记录一次跳过（去重/规格不达标/网盘类型不支持等）
func recordMonitorSkipped(entry, sourceType, channel, messageID, messageURL, targetURL, targetDir string, subID uint, reason string) {
	models.CreateMonitorTransferRecord(&models.MonitorTransferRecord{
		SourceType:     sourceType,
		Entry:          entry,
		Channel:        channel,
		MessageID:      messageID,
		MessageURL:     messageURL,
		TargetURL:      targetURL,
		TransferStatus: models.MonitorStatusSkipped,
		TransferResult: reason,
		TargetDir:      targetDir,
		SubscriptionID: subID,
	})
}
