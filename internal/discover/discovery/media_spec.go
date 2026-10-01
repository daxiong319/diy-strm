package discovery

import (
	"log"
	"regexp"
	"strconv"
	"strings"
)

// 媒体规格解析与洗版判定。
// 优先级链：分辨率 > 来源 > 编码 > 特效 > 体积。
// 采用加权总分便于"洗版目标达标"判定：
//
//	score = 分辨率*10000 + 来源*1000 + 编码*100 + 特效*10 + min(体积GB, 100)

// MediaSpec 资源规格
type MediaSpec struct {
	Resolution int     // 0未知 1=720p 2=1080p 3=2160p(4K)
	Source     int     // 0未知 1=HDTV 2=WEBRip 3=WEB-DL 4=BluRay 5=REMUX
	Codec      int     // 0未知 1=H264 2=H265
	Effect     int     // 0未知 1=SDR 2=HDR 3=Dolby Vision
	SizeGB     float64 // 体积（GB），解析不到为 0
}

var (
	specResRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)2160p|4k\b|2160`), 3},
		{regexp.MustCompile(`(?i)1080p|1080i|full\s?hd|1920x1080`), 2},
		{regexp.MustCompile(`(?i)720p|1280x720|half\s?hd`), 1},
	}
	specSrcRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)remux|uhd\.bluray`), 5},
		{regexp.MustCompile(`(?i)bluray|blu-ray|bdrip|bd\.?iso|蓝光|原盘`), 4},
		{regexp.MustCompile(`(?i)web-dl|webdl|web\.dl|dl\.web`), 3},
		{regexp.MustCompile(`(?i)webrip|web-rip`), 2},
		{regexp.MustCompile(`(?i)dvdrip|dvd|tvrip|hdtv`), 1},
	}
	specCodecRe = []struct {
		re  *regexp.Regexp
		val int
	}{
		{regexp.MustCompile(`(?i)h265|hevc|x265|\bhv1\b`), 2},
		{regexp.MustCompile(`(?i)h264|avc|x264`), 1},
	}
	specDvRe   = regexp.MustCompile(`(?i)dolby[\s.]?vision|杜比视界|\bdv\b`)
	specHdrRe  = regexp.MustCompile(`(?i)hdr10\+|hdr10|hdr`)
	specSizeRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(gb|g|tb|t)\b`)
)

// ParseMediaSpec 从帖子文本（含资源标题行）解析资源规格
func ParseMediaSpec(text string) MediaSpec {
	var s MediaSpec
	for _, r := range specResRe {
		if r.re.MatchString(text) {
			s.Resolution = r.val
			break
		}
	}
	for _, r := range specSrcRe {
		if r.re.MatchString(text) {
			s.Source = r.val
			break
		}
	}
	for _, r := range specCodecRe {
		if r.re.MatchString(text) {
			s.Codec = r.val
			break
		}
	}
	if specDvRe.MatchString(text) {
		s.Effect = 3
	} else if specHdrRe.MatchString(text) {
		s.Effect = 2
	} else {
		s.Effect = 1
	}
	for _, loc := range specSizeRe.FindAllStringSubmatchIndex(text, -1) {
		numStart := loc[2]
		if numStart > 0 {
			prev := text[numStart-1]
			if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') {
				continue // 数字紧贴字母（如 HDR10.1TB），非体积
			}
		}
		if v, err := strconv.ParseFloat(text[loc[2]:loc[3]], 64); err == nil {
			switch strings.ToLower(text[loc[4]:loc[5]]) {
			case "t", "tb":
				s.SizeGB = v * 1024
			default:
				s.SizeGB = v
			}
			break
		}
	}
	return s
}

// Score 规格加权总分（体积封顶 100，避免体积主导）
func (s MediaSpec) Score() int {
	size := s.SizeGB
	if size > 100 {
		size = 100
	}
	return s.Resolution*10000 + s.Source*1000 + s.Codec*100 + s.Effect*10 + int(size)
}

// BetterThan 新规格是否优于旧规格
func (s MediaSpec) BetterThan(o MediaSpec) bool {
	return s.Score() > o.Score()
}

// WashTargetScore 洗版目标的达标分数阈值（低于该分数视为"未达标"可继续洗版）
func WashTargetScore(target string) int {
	switch strings.TrimSpace(strings.ToLower(target)) {
	case "1080p":
		return 2 * 10000
	case "4k":
		return 3 * 10000
	case "4k_remux":
		return 3*10000 + 5*1000
	}
	return 0
}

// WashTargetReached 旧版本规格是否已达洗版目标（达标则不再升级）。
// target 为空 = 无限制（视为未达标，允许继续升级）；非法值告警并按未达标处理，
// 防止拼写错误导致 WashTargetScore 得 0 → Score()>=0 恒真 → 洗版静默失效。
func WashTargetReached(target string, oldScore int) bool {
	t := strings.TrimSpace(strings.ToLower(target))
	if t == "" {
		return false
	}
	threshold := WashTargetScore(t)
	if threshold <= 0 {
		log.Printf("[discovery] 洗版目标 %q 无法识别（支持 1080p/4k/4k_remux），按未达标处理", target)
		return false
	}
	return oldScore >= threshold
}

// NameMatchesTitle 文件名与旧版本标题匹配（精确或带规格后缀前缀）
func NameMatchesTitle(name, title string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	t := strings.ToLower(strings.TrimSpace(title))
	if n == "" || t == "" {
		return false
	}
	if n == t {
		return true
	}
	if strings.HasPrefix(n, t+".") || strings.HasPrefix(t, n+".") {
		return true
	}
	return false
}

// ToMediaSpec 将转存记录的规格字段还原为 MediaSpec（洗版基线）
func (r *DiscoveryTransferRecord) ToMediaSpec() MediaSpec {
	return MediaSpec{
		Resolution: r.Resolution,
		Source:     r.Source,
		Codec:      r.Codec,
		Effect:     r.Effect,
		SizeGB:     r.SizeGB,
	}
}
