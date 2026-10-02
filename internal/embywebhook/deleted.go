package embywebhook

import (
	"strings"
	"time"
)

// BuildDeletedContent 渲染删除通知正文，覆盖电影与剧集两种情形。
// 剧集删除时会带上删除的季集区间（由调用方解析后传入）。
func BuildDeletedContent(ev EmbyEvent, seasons map[int][]int) string {
	var builder strings.Builder
	now := time.Now().Format("2006-01-02 15:04:05")

	switch {
	case ev.Item.Type == MediaTypeEpisode || len(seasons) > 0:
		name := strings.TrimSpace(ev.Item.SeriesName)
		if name == "" {
			name = strings.TrimSpace(ev.Item.Name)
		}
		builder.WriteString("电视剧名称：")
		builder.WriteString(name)
		builder.WriteString("\n")
		if rendered := FormatSeasonEpisodes(seasons); rendered != "" {
			builder.WriteString("删除季集：")
			builder.WriteString(rendered)
			builder.WriteString("\n")
		}
		builder.WriteString("⏰ 删除时间：")
		builder.WriteString(now)
	default:
		name := strings.TrimSpace(ev.Item.Name)
		builder.WriteString("电影名称：")
		builder.WriteString(name)
		builder.WriteString("\n")
		builder.WriteString("⏰ 删除时间：")
		builder.WriteString(now)
	}
	return builder.String()
}

// DeletedNotificationTitle 是删除通知的标题。
const DeletedNotificationTitle = "🗑️ Emby 媒体删除通知"
