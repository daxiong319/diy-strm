package controllers

import (
	"testing"

	"diy-strm/internal/db"
	"diy-strm/internal/models"
)

// 回归：逐帖留痕写入层防重 —— 同 (订阅, 消息链接) 的跳过/失败只审计一次，
// 防止回溯搜索每轮重放历史帖把监控历史刷屏（影巢/机器人入口行为不变）。
func TestMonitorRecordWriteDedup(t *testing.T) {
	setupControllerTestDB(t, &models.MonitorTransferRecord{})

	url := "https://t.me/chan/100"
	countURL := func(subID uint, u string) int64 {
		var cnt int64
		db.Db.Model(&models.MonitorTransferRecord{}).
			Where("subscription_id = ? AND message_url = ?", subID, u).
			Count(&cnt)
		return cnt
	}

	// 同 (订阅, 消息) 的两次跳过 → 只落 1 条
	recordMonitorSkipped("channel", "123", "chan", "100", url, "https://www.123pan.com/s/aaa", "/tmp", 42, "关键词未命中(帖含资源链接)")
	recordMonitorSkipped("channel", "123", "chan", "100", url, "https://www.123pan.com/s/aaa", "/tmp", 42, "关键词未命中(帖含资源链接)")
	if got := countURL(42, url); got != 1 {
		t.Fatalf("同 (订阅,消息) 重复跳过应只写 1 条，实际 %d", got)
	}

	// 失败记录同样被防重（交叉状态：已有跳过记录的同一消息不再写失败）
	recordMonitorFailed("channel", "123", "chan", "100", url, "https://www.123pan.com/s/aaa", "/tmp", 42, nil)
	if got := countURL(42, url); got != 1 {
		t.Fatalf("同消息失败记录应被防重，实际 %d 条", got)
	}

	// 不同消息 → 新记录
	recordMonitorSkipped("channel", "123", "chan", "101", "https://t.me/chan/101", "", "/tmp", 42, "帖子含 123 秒传暗号(123FSLink/FLCP)，暂不支持自动转存")
	if got := countURL(42, "https://t.me/chan/101"); got != 1 {
		t.Fatalf("不同消息应独立写入，实际 %d 条", got)
	}

	// 不同订阅 → 新记录
	recordMonitorSkipped("channel", "123", "chan", "100", url, "", "/tmp", 43, "去重跳过：该影片/剧集已收录")
	if got := countURL(43, url); got != 1 {
		t.Fatalf("不同订阅应独立写入，实际 %d 条", got)
	}

	// 成功记录不做防重（先跳过后转存成功要完整留痕）
	recordMonitorSuccess("channel", "123", "chan", "102", "https://t.me/chan/102", "https://www.123pan.com/s/bbb", "片名", "/tmp", 3, 42)
	recordMonitorSuccess("channel", "123", "chan", "102", "https://t.me/chan/102", "https://www.123pan.com/s/bbb", "片名", "/tmp", 3, 42)
	if got := countURL(42, "https://t.me/chan/102"); got != 2 {
		t.Fatalf("成功记录不应防重，实际 %d 条", got)
	}

	// 机器人入口（subID=0、空消息链接）不防重
	recordMonitorFailed("bot", "123", "TG机器人", "", "", "text", "/tmp", 0, nil)
	recordMonitorFailed("bot", "123", "TG机器人", "", "", "text", "/tmp", 0, nil)
	if got := countURL(0, ""); got != 2 {
		t.Fatalf("机器人入口（空消息链接）不应防重，实际 %d 条", got)
	}
}
