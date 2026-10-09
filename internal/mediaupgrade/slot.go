package mediaupgrade

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"litepan/internal/moviepilot"
)

// Slot 是一个「版本槽位」，照搬 参考实现 VersionGrouper 的口径：
// 分辨率 / 编码 / 制作组 / 音轨 / 字幕 / 容器，六个维度全同才算同一个槽位。
//
// 为什么必须有槽位：不做槽位分组的话，一次扫描会把「用户 1080p 的全集」
// 和「刚下载的一个 2160p」判成同一集的新旧版本，然后建议删掉 1080p。
// 那不是洗版，那是把整个片库升格。槽位分组把「同档位内的版本迭代」
// （2160p H265 → 2160p HEVC、同分辨率下换更大的原盘）从「跨档位换清晰度」里分开了，
// 只对前者判胜负，后者一律两个都保留。
type Slot struct {
	Resolution string // 2160p / 1080p / ?（解析不出）
	Codec      string // h265 / h264 / av1 / mpeg / ?
	Group      string // 制作组（小写），空 = ?
	Audio      string // 声道数，? = 解析不出
	Subtitle   string // 有/无（文件名推断，见 hasSubtitleToken）
	Container  string // mkv / mp4 / ts ...
}

// slotUnknown 是解析不出的维度占位符。
//
// 刻意让它成为一个真实取值而不是空串：两个都解析不出的文件会落进同一个槽位，
// 从而进入比较流程，然后被 RelationNoDimension 拦下（宁可漏洗不误删）。
// 如果解析不出就当各自不同槽位，反而会让「无信息」变成「无限跳过」，掩盖问题。
const slotUnknown = "?"

// Key 返回槽位的规范化键，存进 record.slot_key。
func (s Slot) Key() string {
	return strings.Join([]string{
		"res=" + s.Resolution,
		"codec=" + s.Codec,
		"group=" + s.Group,
		"audio=" + s.Audio,
		"sub=" + s.Subtitle,
		"ext=" + s.Container,
	}, "|")
}

// Label 返回给前端展示的中文摘要。
func (s Slot) Label() string {
	parts := make([]string, 0, 6)
	for _, v := range []string{s.Resolution, s.Codec, s.Group, s.Audio, s.Subtitle, s.Container} {
		if v != "" && v != slotUnknown {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "未知槽位"
	}
	return strings.Join(parts, " ")
}

// SlotOf 由文件名与已解析质量算出槽位。
func SlotOf(fileName string, q *moviepilot.FileQuality) Slot {
	slot := Slot{
		Resolution: slotUnknown,
		Codec:      slotUnknown,
		Group:      slotUnknown,
		Audio:      slotUnknown,
		Subtitle:   hasSubtitleToken(fileName),
		Container:  slotUnknown,
	}
	if ext := strings.TrimPrefix(strings.ToLower(path.Ext(fileName)), "."); ext != "" {
		slot.Container = ext
	}
	if q == nil {
		return slot
	}
	if q.Resolution > 0 {
		slot.Resolution = strconv.Itoa(q.Resolution) + "p"
	}
	if c := strings.ToLower(strings.TrimSpace(q.Codec)); c != "" {
		slot.Codec = c
	}
	if g := strings.ToLower(strings.TrimSpace(q.Group)); g != "" {
		slot.Group = g
	}
	if q.Channels > 0 {
		slot.Audio = strconv.Itoa(q.Channels) + "ch"
	}
	return slot
}

// subtitleTokenRe 字幕标记识别。
//
// ⚠️ 这是**推测值**：参考实现 的槽位里的「字幕」来自媒体服务器的媒体探针（ffprobe 轨道数），
// 本仓对本地文件没有 ffprobe，只能从文件名猜，所以词表故意保守 ——
// 只认中文明确标记与英文地区码，不认 "sub"（会误命中 "subscribed"）也不认 "gb"（会误命中体积串）。
// 结果是带外置字幕的同名文件会被判成「无字幕」，两者槽位不同 ⇒ 两个都保留。这是安全的一侧。
var subtitleTokenRe = regexp.MustCompile(
	`(?i)(字幕|中字|中英|双语|简繁|简体|繁體|繁體中配|chs|cht|big5|zh-?cn|zh-?tw|mandarin)`)

// subtitleTokenSplitRe 词元分隔符（与 release 组 tag 的切分口径一致）。
var subtitleTokenSplitRe = regexp.MustCompile(`[.\s_\-()\[\]]+`)

// hasSubtitleToken 按文件名推断是否带字幕，返回 "sub" / "nosub"。
func hasSubtitleToken(fileName string) string {
	if subtitleTokenRe.MatchString(fileName) {
		return "sub"
	}
	// 复合 token（如 "60fps-Ocat"）里的匹配也算，按分隔符切词元逐个看，
	// 避免 "A-Sub-C.mkv" 这种用分隔符拼接的写法漏判。
	for _, tok := range subtitleTokenSplitRe.Split(fileName, -1) {
		if tok != "" && subtitleTokenRe.MatchString(tok) {
			return "sub"
		}
	}
	return "nosub"
}
