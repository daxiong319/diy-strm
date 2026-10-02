package embywebhook

import (
	"strconv"
	"strings"
)

// ItemDetail 是通知渲染所需的 Emby 条目详情。
// 由 embyproxy 从 Emby REST 接口取回后填充，本包只负责渲染，
// 因此定义在这里而不是依赖 embyproxy，便于测试注入假数据。
type ItemDetail struct {
	ID              string
	Name            string
	Type            string
	Overview        string
	ProductionYear  int
	CommunityRating float64
	Genres          []string
	People          []Person
	ProviderIDs     map[string]string
	ImageTags       map[string]string
	DateCreated     string
	// MediaSources 仅在合并入库场景下用于统计体积。
	MediaSources []MediaSource
	// SeriesName 供剧集条目渲染。
	SeriesName string
	// IndexNumber / ParentIndexNumber 对应集号与季号。
	IndexNumber       int
	ParentIndexNumber int
}

// Person 是 Emby 条目上的一位演职人员。
type Person struct {
	Name string
	Type string
}

// MediaSource 是 Emby 条目的媒体源，只需体积与路径。
type MediaSource struct {
	Path string
	Size int64
}

// ProviderTMDB 是 TMDB 在 Emby ProviderIds 中的键名。
const ProviderTMDB = "Tmdb"

// actorLimit 是主演列表最多展示的人数。
const actorLimit = 5

// ContentInput 是渲染入库通知正文所需的全部输入。
type ContentInput struct {
	Detail ItemDetail
	// ExtraLines 是插在主演之后的附加行（季集区间 / 体积 / 发布组）。
	ExtraLines string
	// IngestedAt 是入库时间；为零值时由调用方传入当前时间。
	IngestedAt string
}

// BuildMediaNotificationContent 渲染入库通知正文。
// 占位符与老版保持一致：🆔 TMDB / ⭐ 评分 / 🎭 类型 / 👤 主演 / ⏰ 入库时间 / 📝 简介。
func BuildMediaNotificationContent(input ContentInput) string {
	detail := input.Detail
	var builder strings.Builder

	builder.WriteString(detail.Name)
	builder.WriteString(" (")
	builder.WriteString(itoa(detail.ProductionYear))
	builder.WriteString(")\n\n")

	if tmdb := strings.TrimSpace(detail.ProviderIDs[ProviderTMDB]); tmdb != "" {
		builder.WriteString("🆔 TMDB：")
		builder.WriteString(tmdb)
		builder.WriteString("\n")
	}

	builder.WriteString("⭐ 评分：")
	builder.WriteString(formatRate(detail.CommunityRating))
	builder.WriteString("\n")

	builder.WriteString("🎭 类型：")
	builder.WriteString(joinOrPlaceholder(detail.Genres, "暂无数据"))
	builder.WriteString("\n")

	builder.WriteString("👤 主演：")
	builder.WriteString(joinOrPlaceholder(actorNames(detail.People), "暂无数据"))
	builder.WriteString("\n")

	if input.ExtraLines != "" {
		builder.WriteString(input.ExtraLines)
	}

	builder.WriteString("⏰ 入库时间：")
	builder.WriteString(input.IngestedAt)
	builder.WriteString("\n\n")

	builder.WriteString("📝 简介\n")
	overview := strings.TrimSpace(detail.Overview)
	if overview == "" {
		overview = "暂无简介"
	}
	builder.WriteString(overview)
	return builder.String()
}

// actorNames 取前 actorLimit 位演员名称。
func actorNames(people []Person) []string {
	names := make([]string, 0, actorLimit)
	for _, person := range people {
		if !strings.EqualFold(person.Type, "Actor") {
			continue
		}
		name := strings.TrimSpace(person.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
		if len(names) >= actorLimit {
			break
		}
	}
	return names
}

// BuildExtraLines 渲染入库通知的附加行（季集区间 / 体积 / 发布组），无内容时返回空串。
func BuildExtraLines(seasonEpisodes string, totalSize int64, releaseGroups string, newCount int) string {
	var builder strings.Builder
	if seasonEpisodes != "" {
		builder.WriteString("📺 入库季集：")
		builder.WriteString(seasonEpisodes)
		if newCount > 0 {
			builder.WriteString("（新增 ")
			builder.WriteString(itoa(newCount))
			builder.WriteString(" 集）")
		}
		builder.WriteString("\n")
	}
	if totalSize > 0 {
		builder.WriteString("💾 体积：")
		builder.WriteString(FormatBytesHuman(totalSize))
		builder.WriteString("\n")
	}
	if releaseGroups != "" {
		builder.WriteString("🏷️ 发布组：")
		builder.WriteString(releaseGroups)
		builder.WriteString("\n")
	}
	return builder.String()
}

// itoa 是 strconv.Itoa 的薄封装，统一正文里的数字渲染方式。
func itoa(value int) string {
	return strconv.Itoa(value)
}
