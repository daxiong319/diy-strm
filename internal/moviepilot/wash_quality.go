package moviepilot

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"diy-strm/internal/mediaparse"
)

// FileQuality 从文件名解析出的质量快照（借鉴 mediavault MediaUpgradeRecord 的质量维度）
type FileQuality struct {
	Resolution int    `json:"resolution"` // 0/480/720/1080/2160
	ResTag     string `json:"res_tag"`
	Codec      string `json:"codec"` // h265/h264/av1/mpeg/unknown
	CodecTag   string `json:"codec_tag"`
	VideoFormat string `json:"video_format"` // bluray/remux/web-dl/webrip/hdtv
	BitDepth   string `json:"bitdepth"`     // 8bit/10bit/12bit
	HDR        string `json:"hdr"`
	AudioTag   string `json:"audio_tag"`
	Channels   int    `json:"channels"`
	Edition    string `json:"edition"`
	Customization string `json:"customization"` // 平台/定制词（Baha/NF/...，命名模板用）
	Group      string `json:"group"`
	Tags       string `json:"tags"` // 质量标签段原样（点分）
}

func (q *FileQuality) Summary() string {
	parts := []string{}
	if q.ResTag != "" {
		parts = append(parts, q.ResTag)
	}
	if q.CodecTag != "" {
		parts = append(parts, q.CodecTag)
	}
	if q.AudioTag != "" {
		parts = append(parts, q.AudioTag)
	}
	if q.VideoFormat != "" {
		parts = append(parts, q.VideoFormat)
	}
	if q.BitDepth != "" {
		parts = append(parts, q.BitDepth)
	}
	if q.HDR != "" {
		parts = append(parts, q.HDR)
	}
	if q.Group != "" {
		parts = append(parts, q.Group)
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
	qualityFpsRe        = regexp.MustCompile(`(?i)\b(120fps|60fps|50fps|30fps|25fps|24fps)\b`)
	qualityEditionRe    = regexp.MustCompile(`(?i)\b(uncut|unrated|extended|theatrical|director['’]?s?[-_ ]?cut|remastered)\b`)
	// 组 token 仅纯字母（如 -Ocat / [FRDS]，避免误剥 S01E02 / 2160p 等含数字 token）
	groupSuffixRe       = regexp.MustCompile(`[-\[\]]([A-Z][A-Za-z]{1,20})$`)
)

// codecRank 编码评分（越高越优，参考 mediavault preferred_codecs=hevc,h265,av1）
func codecRank(c string) int {
	switch strings.ToLower(c) {
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

// formatRank 视频来源格式评分（原盘/remux 高于 web 压制）
func formatRank(f string) int {
	switch strings.ToLower(f) {
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

// audioChannels 由音频标签推断声道数
func audioChannels(tag string) int {
	t := strings.ToLower(tag)
	switch {
	case strings.Contains(t, "atmos"), strings.Contains(t, "truehd"), strings.Contains(t, "dts-hd ma"), strings.Contains(t, "dts-hdma"), strings.Contains(t, "dts-x"), strings.Contains(t, "7.1"):
		return 8
	case strings.Contains(t, "5.1"), strings.Contains(t, "ac3"), strings.Contains(t, "ddp"), strings.Contains(t, "eac3"), strings.Contains(t, "dolby"):
		return 6
	case strings.Contains(t, "2.0"), strings.Contains(t, "stereo"), strings.Contains(t, "aac"), strings.Contains(t, "flac"), strings.Contains(t, "opus"):
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
	// 组：尾部 -Group / [Group] 提取（不剥 ISO RIFF，仅提取展示与评分用）
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
	// 完整质量标签段（供命名模板 tags 字段）：先由文件名解析出标题再剥离标题区间
	_, parsedTitle, _, _, _ := mediaparse.ParseMedia(fileName)
	q.Tags = extractQualityTags(fileName, parsedTitle)
	return q
}

// ---- 洗版比较规则（P1-3，借鉴 mediavault upgrade_rules）----

// WashRule 一条比较规则：字段 + 是否「更高更优」
type WashRule struct {
	Field  string `json:"field"`  // resolution/codec/format/bitdepth/channels/group
	Higher bool   `json:"higher"` // true=分数高者为优（默认全 true）
}

// DefaultWashRules 默认比较优先级（逐项比较，先决胜负；与 mediavault「按优先级逐项比较」一致）
var DefaultWashRules = []WashRule{
	{Field: "resolution", Higher: true},
	{Field: "codec", Higher: true},
	{Field: "format", Higher: true},
	{Field: "channels", Higher: true},
	{Field: "bitdepth", Higher: true},
	{Field: "group", Higher: true},
}

// ParseWashRules 解析配置的规则 JSON；空或非法回退默认
func ParseWashRules(raw string) []WashRule {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return DefaultWashRules
	}
	var rules []WashRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil || len(rules) == 0 {
		return DefaultWashRules
	}
	// 校验字段名，非法字段剔除
	valid := map[string]bool{"resolution": true, "codec": true, "format": true, "bitdepth": true, "channels": true, "group": true}
	out := make([]WashRule, 0, len(rules))
	for _, r := range rules {
		if valid[r.Field] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return DefaultWashRules
	}
	return out
}

func fieldValue(r WashRule, q *FileQuality) int {
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
	case "group":
		return 0 // 非列表组为 0，由 groupRank 动态计算
	default:
		return 0
	}
}

// groupRank 制作组优先级：位置越靠前越高；不在列表=0
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
// 返回 1=新更优，-1=新更差，0=持平。按规则逐项比较（借鉴 mediavault 综合评分逐项比较）。
func CompareQuality(newQ, oldQ *FileQuality, groupPriority []string, rules []WashRule) int {
	if newQ == nil || oldQ == nil {
		return 1 // 无法解析旧质量时按可覆盖处理，避免阻塞整理
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

// ---- 同名匹配（P0-1：忽略扩展名/质量标签后缀）----

var washQualityTokenRe = regexp.MustCompile(`(?i)^(2160p|4k|uhd|1440p|1080p|1080i|720p|576p|540p|480p|h265|hevc|x265|av1|h264|avc|x264|h\.265|h\.264|mpeg4|xvid|divx|mpeg2|h263|vc1|atmos|truehd|dts[\w.-]*|eac3|ddp|ac3|dd5\.1|dd2\.0|dolby[\w.-]*|7\.1|5\.1|5\.0|2\.0|stereo|mono|aac|flac|lpcm|opus|bd[\w.-]*|remux|blu[\w.-]*ray|web[\w.-]*(?:dl|rip)?|hdtv|hdr10\+?|dolby[\w.-]*vision|dv|imax|10bit|12bit|8bit|60fps|50fps|30fps|25fps|24fps|uncut|unrated|extended|theatrical|remastered|1080|720|4k)$`)

// stripQualitySuffix 剥离文件名主干末尾的质量标签 token 序列（调用方已去扩展名），
// 返回剥离后剩余部分。多 part / cd 标记（part1/cd1）不属于质量 token，保留。
// 兼容 diy-strm 标准输出中「质量token-组名」复合 token（如 60fps-Ocat / H.265-Ocat）：
// 先尝试剥掉尾部 -组名 再重测剩余部分是否为质量 token。
func stripQualitySuffix(stem string) string {
	parts := strings.Split(stem, ".")
	for len(parts) > 0 {
		last := strings.TrimSpace(parts[len(parts)-1])
		if washQualityTokenRe.MatchString(last) || pureGroupTokenRe.MatchString(last) {
			parts = parts[:len(parts)-1]
			continue
		}
		// 复合 token：尾部 -组名 → 去掉组名后重测（如 60fps-Ocat → 60fps）
		if idx := strings.LastIndexByte(last, '-'); idx > 0 {
			rest := last[:idx]
			if pureGroupTokenRe.MatchString(last[idx+1:]) && washQualityTokenRe.MatchString(rest) {
				parts = parts[:len(parts)-1]
				continue
			}
		}
		break
	}
	return strings.Join(parts, ".")
}

// pureGroupTokenRe 纯字母组名 token（Ocat / FRDS），不含数字避免误剥 S01E02
var pureGroupTokenRe = regexp.MustCompile(`^[A-Za-z]{2,20}$`)

// washCoreKey 计算用于同名匹配的核心键：去扩展名 → 去质量后缀 → 小写。
// 两侧核心键一致的旧文件即为「同片同名」洗版对象（忽略扩展名与质量差异）。
func washCoreKey(fileName string) string {
	stem := strings.TrimSuffix(fileName, path.Ext(fileName))
	return strings.ToLower(stripQualitySuffix(stem))
}

// findWashTargets 在目标目录条目中查找与新文件「同名」的旧视频文件（忽略扩展名/质量后缀）。
// 返回值元素为目录条目下标。tv 场景额外放宽：规范命名 SxxExx 与旧文件季集一致即视为同名。
func findWashTargets(newName string, newQ *FileQuality, entries []organizeEntry) []int {
	newKey := washCoreKey(newName)
	newEp := episodeKeyOf(newName)
	var targets []int
	for i := range entries {
		e := &entries[i]
		if e.IsDir || !isVideoFile(e.Name) {
			continue
		}
		if washCoreKey(e.Name) == newKey {
			targets = append(targets, i)
			continue
		}
		// 电视扩展：同集号即同名（适合旧版本命名不规范场景）
		if newEp != "" {
			oldEp := episodeKeyOf(e.Name)
			if oldEp != "" && oldEp == newEp {
				targets = append(targets, i)
			}
		}
	}
	return targets
}

var (
	epKeySxxExxRe = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	epKeyChinese  = regexp.MustCompile(`第\s*(\d{1,3})\s*[集話话]`)
)

// episodeKeyOf 提取文件名的剧集键（"S01E05" 规范化），无剧集信息返回空
func episodeKeyOf(name string) string {
	if m := epKeySxxExxRe.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		return fmt.Sprintf("S%02dE%02d", s, e)
	}
	if m := epKeyChinese.FindStringSubmatch(name); m != nil {
		e, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("S00E%02d", e)
	}
	return ""
}

func isVideoFile(name string) bool {
	return mediaIsVideoExt(name)
}

func mediaIsVideoExt(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".mp4", ".mkv", ".avi", ".ts", ".m2ts", ".mov", ".wmv", ".flv", ".rmvb", ".rm", ".m4v", ".webm", ".mpg", ".mpeg", ".iso", ".bdmv":
		return true
	}
	return false
}