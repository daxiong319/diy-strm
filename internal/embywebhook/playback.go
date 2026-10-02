package embywebhook

import (
	"strconv"
	"strings"
)

// PlaybackEvent 是播放类 Webhook 事件。
// types.go 的 EmbyEvent 只覆盖入库/删除所需的字段，播放事件额外带
// User / Session / PlaybackInfo 三段，所以单独建模。
type PlaybackEvent struct {
	Event        string           `json:"Event"`
	User         EmbyUser         `json:"User"`
	Item         EmbyItem         `json:"Item"`
	Session      EmbySession      `json:"Session"`
	PlaybackInfo EmbyPlaybackInfo `json:"PlaybackInfo"`
}

// playbackOverviewLimit 是简介截断长度（按 rune 计）。
const playbackOverviewLimit = 100

// BuildPlaybackContent 渲染播放通知正文。
// overview 为 true 时附加简介（最多 100 字），progress 为 true 时输出播放进度或总时长。
func BuildPlaybackContent(ev PlaybackEvent, overview bool, progress bool) string {
	var builder strings.Builder

	builder.WriteString("用户：")
	builder.WriteString(strings.TrimSpace(ev.User.Name))
	builder.WriteString("\n")

	device := strings.TrimSpace(ev.Session.DeviceName)
	client := strings.TrimSpace(ev.Session.Client)
	builder.WriteString("设备：")
	builder.WriteString(device)
	if client != "" {
		builder.WriteString(" (")
		builder.WriteString(client)
		builder.WriteString(")")
	}
	builder.WriteString("\n")

	if ev.Item.Type == MediaTypeEpisode {
		builder.WriteString("电视剧：")
		builder.WriteString(strings.TrimSpace(ev.Item.SeriesName))
		builder.WriteString("\n")
		builder.WriteString("季集：")
		builder.WriteString(FormatSeasonEpisode(ev.Item.ParentIndexNumber, ev.Item.IndexNumber))
		builder.WriteString("\n")
	}

	if progress {
		positionTicks := ev.PlaybackInfo.PositionTicks
		runtimeTicks := runtimeTicksOf(ev)
		switch {
		case positionTicks > 0 && runtimeTicks > 0:
			builder.WriteString("播放进度：")
			builder.WriteString(FormatTicksToTime(positionTicks))
			builder.WriteString(" / ")
			builder.WriteString(FormatTicksToTime(runtimeTicks))
			builder.WriteString(" (")
			builder.WriteString(percent(positionTicks, runtimeTicks))
			builder.WriteString("%)\n")
		case runtimeTicks > 0:
			builder.WriteString("时长：")
			builder.WriteString(FormatTicksToTime(runtimeTicks))
			builder.WriteString("\n")
		}
	}

	if overview {
		intro := truncateRunes(strings.TrimSpace(ev.Item.Overview), playbackOverviewLimit)
		builder.WriteString("简介：")
		builder.WriteString(intro)
		builder.WriteString("\n")
	}

	return builder.String()
}

// runtimeTicksOf 取条目总时长：优先 Item.RunTimeTicks，回退到 PlaybackInfo.RunTimeTicks。
func runtimeTicksOf(ev PlaybackEvent) int64 {
	if ev.Item.RunTimeTicks > 0 {
		return ev.Item.RunTimeTicks
	}
	return ev.PlaybackInfo.RunTimeTicks
}

// FormatSeasonEpisode 渲染 S%02dE%02d；季集都取不到时返回空串。
func FormatSeasonEpisode(season, episode int) string {
	if season == 0 && episode == 0 {
		return ""
	}
	return "S" + pad2(int64(season)) + "E" + pad2(int64(episode))
}

// FormatPlaybackDuration 把毫秒时长渲染成「N 小时 M 分钟」这类中文文案。
func FormatPlaybackDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "0 秒"
	}
	totalSeconds := milliseconds / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return itoa64(hours) + " 小时 " + itoa64(minutes) + " 分钟"
	}
	if minutes > 0 {
		return itoa64(minutes) + " 分钟"
	}
	return itoa64(seconds) + " 秒"
}

// PlaybackEventEmoji 返回播放事件的表情。
func PlaybackEventEmoji(event string) string {
	switch event {
	case EventPlaybackStart:
		return "📺"
	case EventPlaybackPause:
		return "⏸️"
	case EventPlaybackStop:
		return "⏹️"
	default:
		return "📺"
	}
}

// PlaybackEventName 返回播放事件的中文名。
func PlaybackEventName(event string) string {
	switch event {
	case EventPlaybackStart:
		return "播放开始"
	case EventPlaybackPause:
		return "播放暂停"
	case EventPlaybackStop:
		return "播放停止"
	default:
		return "播放事件"
	}
}

// MediaTypeName 返回媒体类型的中文名。
func MediaTypeName(mediaType string) string {
	switch mediaType {
	case MediaTypeMovie:
		return "电影"
	case MediaTypeEpisode, MediaTypeSeries, MediaTypeSeason:
		return "剧集"
	default:
		return "媒体"
	}
}

// percent 计算播放百分比，四舍五入到整数。
func percent(position, runtime int64) string {
	if runtime <= 0 {
		return "0"
	}
	value := float64(position) / float64(runtime) * 100
	return itoa64(int64(value + 0.5))
}

// truncateRunes 按 rune 截断，超出时补省略号。
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// itoa64 是 strconv.FormatInt 的薄封装，统一数字渲染方式。
func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
