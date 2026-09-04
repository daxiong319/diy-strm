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

// MonitorMediaMeta 监控历史记录的影片关联信息（可选，通过 variadic 传入，
// 不传则留空）。仅影巢订阅 / TG 影视订阅能关联影片，其他入口可不传。
type MonitorMediaMeta struct {
	TMDBID    int64
	MediaType string
	Season    string
	Episode   string
	Title     string // 影片展示名（订阅影片名 / 资源标题），用于补全空 Title
}

// applyMonitorMeta 将可选影片元信息写入记录
func applyMonitorMeta(rec *models.MonitorTransferRecord, meta []MonitorMediaMeta) {
	if len(meta) == 0 {
		return
	}
	m := meta[0]
	rec.TMDBID = m.TMDBID
	rec.MediaType = m.MediaType
	rec.Season = m.Season
	rec.Episode = m.Episode
	if rec.Title == "" && m.Title != "" {
		rec.Title = m.Title
	}
}

// buildTGMessageURL 由频道名 + 帖子 ID 构造消息链接
func buildTGMessageURL(channel, postID string) string {
	channel = strings.TrimPrefix(strings.TrimPrefix(channel, "@"), "https://t.me/")
	channel = strings.TrimSuffix(channel, "/")
	if channel == "" || postID == "" {
		return ""
	}
	return "https://t.me/" + channel + "/" + postID
}

// monitorRecordDuplicated 逐帖留痕防重复：该 (订阅, 消息链接) 已有监控记录则不再写入。
// 回溯搜索每轮会重放整段历史帖，无防重时已收录帖的「去重跳过」「洗版跳过」「转存失败」
// 每 5 分钟轮询都会重复入库刷屏（对齐 tgto123 的 is_message_processed：每消息只审计一次）。
// 影巢入口 messageURL 恒为空、机器人入口 subID=0，均不满足条件，行为不变。
// 同消息首条之后的后续结果（重试失败原因变化、先跳过后转存成功）以执行摘要与成功记录为准。
func monitorRecordDuplicated(subID uint, messageURL string) bool {
	if subID == 0 || strings.TrimSpace(messageURL) == "" {
		return false
	}
	return models.MonitorRecordExists(subID, messageURL)
}

// recordMonitorSuccess 记录一次成功转存
func recordMonitorSuccess(entry, sourceType, channel, messageID, messageURL, targetURL, title, targetDir string, total int, subID uint, meta ...MonitorMediaMeta) {
	rec := &models.MonitorTransferRecord{
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
	}
	applyMonitorMeta(rec, meta)
	models.CreateMonitorTransferRecord(rec)
}

// recordMonitorWash 记录一次洗版替换转存
func recordMonitorWash(entry, sourceType, channel, messageID, messageURL, targetURL, title, targetDir string, total int, subID uint, washTarget string, meta ...MonitorMediaMeta) {
	rec := &models.MonitorTransferRecord{
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
	}
	applyMonitorMeta(rec, meta)
	models.CreateMonitorTransferRecord(rec)
}

// recordMonitorFailed 记录一次转存失败（同 (订阅,消息) 已有记录则不重复写，防止回溯/游标回退重试刷屏）
func recordMonitorFailed(entry, sourceType, channel, messageID, messageURL, targetURL, targetDir string, subID uint, reason error, meta ...MonitorMediaMeta) {
	if monitorRecordDuplicated(subID, messageURL) {
		return
	}
	msg := ""
	if reason != nil {
		msg = reason.Error()
	}
	rec := &models.MonitorTransferRecord{
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
	}
	applyMonitorMeta(rec, meta)
	models.CreateMonitorTransferRecord(rec)
}

// recordMonitorSkipped 记录一次跳过（去重/规格不达标/网盘类型不支持等；同 (订阅,消息) 已有记录则不重复写，防止回溯重放刷屏）
func recordMonitorSkipped(entry, sourceType, channel, messageID, messageURL, targetURL, targetDir string, subID uint, reason string, meta ...MonitorMediaMeta) {
	if monitorRecordDuplicated(subID, messageURL) {
		return
	}
	rec := &models.MonitorTransferRecord{
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
	}
	applyMonitorMeta(rec, meta)
	models.CreateMonitorTransferRecord(rec)
}
