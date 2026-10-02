package moviepilot

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	"litepan/internal/discover/mediaparse"
)

// FileQuality 从文件名解析出的质量快照，洗版比较与命名模板共用。
type FileQuality struct {
	Resolution    int    `json:"resolution"` // 0/480/576/720/1080/1440/2160
	ResTag        string `json:"res_tag"`
	Codec         string `json:"codec"` // h265/h264/av1/mpeg/unknown
	CodecTag      string `json:"codec_tag"`
	VideoFormat   string `json:"video_format"` // bluray/remux/web-dl/webrip/hdtv
	BitDepth      string `json:"bitdepth"`     // 8bit/10bit/12bit
	HDR           string `json:"hdr"`
	AudioTag      string `json:"audio_tag"`
	Channels      int    `json:"channels"`
	Edition       string `json:"edition"`
	Customization string `json:"customization"` // 平台/定制词（Baha/NF/...，命名模板用）
	Group         string `json:"group"`
	Tags          string `json:"tags"` // 质量标签段原样（点分）
}

// Summary 质量摘要，用于日志展示。
func (q *FileQuality) Summary() string {
	if q == nil {
		return ""
	}
	parts := make([]string, 0, 7)
	for _, v := range []string{q.ResTag, q.CodecTag, q.AudioTag, q.VideoFormat, q.BitDepth, q.HDR, q.Group} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " ")
}

// ---- 质量 token 识别表 ----

var (
	qualityResolutionRe = regexp.MustCompile(`(?i)\b(2160p|4k|uhd|1440p|1080p|1080i|720p|576p|540p|480p)\b`)
	qualityCodecRe      = regexp.MustCompile(`(?i)\b(h265|hevc|x265|av1|h264|avc|x264|h\.265|h\.264|mpeg4|xvid|divx|mpeg2|h263|vc1)\b`)
	qualityAudioRe      = regexp.MustCompile(`(?i)\b(atmos|truehd|dts[-_ ]?hd[-_ ]?(?:ma|hr)?|dts[-_ ]?x|dts|eac3|ddp|ac3|dd5\.1|dd2\.0|dolby[-_ ]?digital|7\.1|5\.1|5\.0|2\.0|stereo|mono|aac|flac|lpcm|opus)\b`)
	qualityFormatRe     = regexp.MustCompile(`(?i)\b(bd[-_]?remux|remux|blu[-_ ]?ray|web[-_ ]?dl|web[-_ ]?rip|hdtv|hdr10\+|hdr10|dolby[-_ ]?vision|dv|imax)\b`)
	qualityBitRe        = regexp.MustCompile(`(?i)\b(10bit|12bit|8bit|10[-_]?bit)\b`)
	qualityEditionRe    = regexp.MustCompile(`(?i)\b(uncut|unrated|extended|theatrical|director['’]?s?[-_ ]?cut|remastered)\b`)
	// 组 token 仅纯字母（如 -Ocat / [FRDS]），避免误剥 S01E02 / 2160p 等含数字 token
	groupSuffixRe = regexp.MustCompile(`[-\[\]]([A-Z][A-Za-z]{1,20})$`)
)

// codecRank 编码评分（越高越优）。
func codecRank(c string) int {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "av1", "hevc", "h265", "x265":
		return 3
	case "h264", "avc", "x264":
		return 2
	case "mpeg4", "xvid", "divx", "mpeg2", "h263", "vc1":
		return 1
	default:
		return 0
	}
}

// formatRank 来源格式评分（原盘/remux 高于 web 压制）。
func formatRank(f string) int {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "bd-remux", "remux", "blu-ray", "bluray":
		return 3
	case "web-dl":
		return 2
	case "webrip", "web", "hdtv":
		return 1
	default:
		return 0
	}
}

// audioChannels 由音频标签推断声道数。
func audioChannels(tag string) int {
	t := strings.ToLower(tag)
	switch {
	case strings.Contains(t, "atmos"), strings.Contains(t, "truehd"),
		strings.Contains(t, "dts-hd ma"), strings.Contains(t, "dts-hdma"),
		strings.Contains(t, "dts-x"), strings.Contains(t, "7.1"):
		return 8
	case strings.Contains(t, "5.1"), strings.Contains(t, "ac3"), strings.Contains(t, "ddp"),
		strings.Contains(t, "eac3"), strings.Contains(t, "dolby"):
		return 6
	case strings.Contains(t, "2.0"), strings.Contains(t, "stereo"), strings.Contains(t, "aac"),
		strings.Contains(t, "flac"), strings.Contains(t, "opus"):
		return 2
	case strings.Contains(t, "mono"):
		return 1
	default:
		return 0
	}
}

// ParseQualityFromName 解析文件名中的质量信息（标题/季集段之外的标签部分）。
func ParseQualityFromName(fileName string) *FileQuality {
	q := &FileQuality{}
	stem := strings.TrimSuffix(fileName, path.Ext(fileName))
	tagText := stem
	if m := groupSuffixRe.FindStringSubmatch(stem); m != nil {
		q.Group = m[1]
	}
	if m := qualityResolutionRe.FindStringSubmatch(tagText); m != nil {
		q.ResTag = m[1]
		switch strings.ToLower(m[1]) {
		case "2160p", "4k", "uhd":
			q.Resolution = 2160
		case "1440p":
			q.Resolution = 1440
		case "1080p", "1080i":
			q.Resolution = 1080
		case "720p":
			q.Resolution = 720
		case "576p", "540p":
			q.Resolution = 576
		case "480p":
			q.Resolution = 480
		}
	}
	if m := qualityCodecRe.FindStringSubmatch(tagText); m != nil {
		q.CodecTag = strings.ToUpper(m[1])
		switch strings.ToLower(m[1]) {
		case "hevc", "h265", "x265", "h.265":
			q.Codec = "h265"
		case "av1":
			q.Codec = "av1"
		case "h264", "avc", "x264", "h.264":
			q.Codec = "h264"
		default:
			q.Codec = "mpeg"
		}
	}
	if m := qualityAudioRe.FindStringSubmatch(tagText); m != nil {
		q.AudioTag = strings.ToUpper(m[1])
		q.Channels = audioChannels(m[1])
	}
	if m := qualityFormatRe.FindStringSubmatch(tagText); m != nil {
		f := strings.ToLower(m[1])
		switch f {
		case "hdr10+", "hdr10", "dolby-vision", "dv", "imax":
			q.HDR = strings.ToUpper(m[1])
		default:
			q.VideoFormat = f
		}
	}
	if m := qualityBitRe.FindStringSubmatch(tagText); m != nil {
		q.BitDepth = strings.ToLower(m[1])
	}
	if m := qualityEditionRe.FindStringSubmatch(tagText); m != nil {
		q.Edition = strings.ToLower(m[1])
	}
	// 完整质量标签段（命名模板 tags 字段用）：先解析出标题再剥离标题区间
	_, parsedTitle, _, _, _ := mediaparse.ParseMedia(fileName)
	q.Tags = ExtractQualityTags(fileName, parsedTitle)
	return q
}

// ---- 洗版比较规则 ----

// WashRule 一条比较规则：字段 + 是否「更高更优」。
type WashRule struct {
	Field  string `json:"field"`  // resolution/codec/format/bitdepth/channels/group
	Higher bool   `json:"higher"` // true=分数高者为优
}

// DefaultWashRules 默认比较优先级（逐项比较，先决胜负）。
var DefaultWashRules = []WashRule{
	{Field: "resolution", Higher: true},
	{Field: "codec", Higher: true},
	{Field: "format", Higher: true},
	{Field: "channels", Higher: true},
	{Field: "bitdepth", Higher: true},
	{Field: "group", Higher: true},
}

var washRuleFields = map[string]bool{
	"resolution": true, "codec": true, "format": true, "bitdepth": true, "channels": true, "group": true,
}

// ParseWashRules 解析配置的规则 JSON；空或非法回退默认规则。
func ParseWashRules(raw string) []WashRule {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return DefaultWashRules
	}
	var rules []WashRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil || len(rules) == 0 {
		return DefaultWashRules
	}
	out := make([]WashRule, 0, len(rules))
	for _, r := range rules {
		if washRuleFields[r.Field] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return DefaultWashRules
	}
	return out
}

func fieldValue(r WashRule, q *FileQuality) int {
	if q == nil {
		return 0
	}
	switch r.Field {
	case "resolution":
		return q.Resolution
	case "codec":
		return codecRank(q.Codec)
	case "format":
		return formatRank(q.VideoFormat)
	case "channels":
		return q.Channels
	case "bitdepth":
		switch q.BitDepth {
		case "12bit", "10bit", "10-bit":
			return 2
		case "8bit":
			return 1
		default:
			return 0
		}
	default:
		return 0
	}
}

// groupRank 制作组优先级：位置越靠前越高；不在列表为 0。
func groupRank(group string, priority []string) int {
	if group == "" {
		return 0
	}
	gl := strings.ToLower(group)
	for i, g := range priority {
		if strings.ToLower(strings.TrimSpace(g)) == gl {
			return len(priority) - i
		}
	}
	return 0
}

// CompareQuality 洗版质量比较：newQ 相对 oldQ。
// 返回 1=新更优，-1=新更差，0=持平。
func CompareQuality(newQ, oldQ *FileQuality, groupPriority []string, rules []WashRule) int {
	if newQ == nil || oldQ == nil {
		// 无法解析旧质量时按可覆盖处理，避免阻塞整理
		return 1
	}
	if len(rules) == 0 {
		rules = DefaultWashRules
	}
	for _, r := range rules {
		var nv, ov int
		if r.Field == "group" {
			nv = groupRank(newQ.Group, groupPriority)
			ov = groupRank(oldQ.Group, groupPriority)
		} else {
			nv = fieldValue(r, newQ)
			ov = fieldValue(r, oldQ)
		}
		if nv == ov {
			continue
		}
		if r.Higher {
			if nv > ov {
				return 1
			}
			return -1
		}
		if nv < ov {
			return 1
		}
		return -1
	}
	return 0
}

// ---- 洗版对比明细 ----

var washFieldLabels = map[string]string{
	"resolution": "分辨率", "codec": "编码", "format": "来源",
	"channels": "声道", "bitdepth": "色深", "group": "组名",
}

// qualityFieldDisplay 规则字段的可读值（如 2160p / H265 / WEB-DL / 5.1 / Ocat）。
func qualityFieldDisplay(field string, q *FileQuality) string {
	if q == nil {
		return "?"
	}
	switch field {
	case "resolution":
		if q.ResTag != "" {
			return strings.ToLower(q.ResTag)
		}
		if q.Resolution > 0 {
			return fmt.Sprintf("%dp", q.Resolution)
		}
		return "未知"
	case "codec":
		if q.CodecTag != "" {
			return q.CodecTag
		}
		if q.Codec != "" {
			return strings.ToUpper(q.Codec)
		}
		return "未知"
	case "format":
		if q.VideoFormat != "" {
			return strings.ToUpper(q.VideoFormat)
		}
		return "未知"
	case "channels":
		if q.AudioTag != "" {
			return q.AudioTag
		}
		if q.Channels > 0 {
			return fmt.Sprintf("%dch", q.Channels)
		}
		return "未知"
	case "bitdepth":
		if q.BitDepth != "" {
			return strings.ToUpper(q.BitDepth)
		}
		return "未知"
	case "group":
		if q.Group != "" {
			return q.Group
		}
		return "无组"
	}
	return "?"
}

// QualityCompareTrace 洗版逐项对比描述：按规则顺序输出「字段 新值/旧值 关系」，
// 在决出胜负的字段处标注（新优/新差），后面的项不再列出。
func QualityCompareTrace(newQ, oldQ *FileQuality, groupPriority []string, rules []WashRule) string {
	if newQ == nil || oldQ == nil {
		return "任一侧质量不可解析"
	}
	if len(rules) == 0 {
		rules = DefaultWashRules
	}
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		var nv, ov int
		if r.Field == "group" {
			nv = groupRank(newQ.Group, groupPriority)
			ov = groupRank(oldQ.Group, groupPriority)
		} else {
			nv = fieldValue(r, newQ)
			ov = fieldValue(r, oldQ)
		}
		label := washFieldLabels[r.Field]
		if label == "" {
			label = r.Field
		}
		nd, od := qualityFieldDisplay(r.Field, newQ), qualityFieldDisplay(r.Field, oldQ)
		if nv == ov {
			if nd == od {
				parts = append(parts, fmt.Sprintf("%s %s=%s", label, nd, od))
			} else {
				parts = append(parts, fmt.Sprintf("%s %s/%s 同档", label, nd, od))
			}
			continue
		}
		better := (r.Higher && nv > ov) || (!r.Higher && nv < ov)
		if better {
			parts = append(parts, fmt.Sprintf("%s %s>%s（新优）", label, nd, od))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s<%s（新差）", label, nd, od))
		}
		return strings.Join(parts, "；")
	}
	if len(parts) == 0 {
		return "逐项持平"
	}
	return strings.Join(parts, "；") + "；逐项持平"
}

// MergeQualityHints 文件名解析缺项时用外部识别结果补齐（不覆盖已解析出的值）。
func MergeQualityHints(q, hint *FileQuality) {
	if q == nil || hint == nil {
		return
	}
	if q.Resolution == 0 {
		q.Resolution = hint.Resolution
	}
	if q.ResTag == "" {
		q.ResTag = hint.ResTag
	}
	if q.Codec == "" {
		q.Codec = hint.Codec
	}
	if q.CodecTag == "" {
		q.CodecTag = hint.CodecTag
	}
	if q.VideoFormat == "" {
		q.VideoFormat = hint.VideoFormat
	}
	if q.BitDepth == "" {
		q.BitDepth = hint.BitDepth
	}
	if q.HDR == "" {
		q.HDR = hint.HDR
	}
	if q.AudioTag == "" {
		q.AudioTag = hint.AudioTag
	}
	if q.Channels == 0 {
		q.Channels = hint.Channels
	}
	if q.Edition == "" {
		q.Edition = hint.Edition
	}
	if q.Group == "" {
		q.Group = hint.Group
	}
	if q.Tags == "" {
		q.Tags = hint.Tags
	}
}
