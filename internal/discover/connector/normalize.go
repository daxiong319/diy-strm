package connector

import (
	"regexp"
	"strconv"
	"strings"

	"litepan/internal/moviepilot"
)

// 正则口径全部对齐 参考实现 的 search connector 实现：
//   - 分享码 ^[0-9A-Za-z]{1,16}$
//   - 磁力    ^magnet:\?(?:[^\s]*&)?xt=urn:btih:(?:[0-9a-f]{40}|[a-z2-7]{32})(?:[^\s]*)?$
var (
	shareCodeRe = regexp.MustCompile(`^[0-9A-Za-z]{1,16}$`)
	magnetRe    = regexp.MustCompile(`^magnet:\?(?:[^\s]*&)?xt=urn:btih:(?:[0-9a-f]{40}|[a-z2-7]{32})(?:[^\s]*)?$`)
	infoHashRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	base32Re    = regexp.MustCompile(`^[a-z2-7]{32}$`)
	ed2kRe      = regexp.MustCompile(`^ed2k://\|file\|`)
	atmosRe     = regexp.MustCompile(`(?i)\batmos\b`)
	dtsRe       = regexp.MustCompile(`(?i)\bdts(?:[-_ ]?hd)?\b`)
	// 分隔符含点号："Dolby.Vision" 在资源名里和 "Dolby Vision" 一样常见，
	// 而 \b 在 "y.V" 这种写法下不会断开，只写 [-_ ]? 会漏掉三分之一的 DV 片源。
	hdrRe     = regexp.MustCompile(`(?i)\b(hdr10\+?|dolby[._ -]?vision|dovi|dv)\b`)
	torrentRe = regexp.MustCompile(`^(?i:magnet:)|\.torrent$`)
)

// ValidShareCode 分享码是否合法（参考实现 口径）。
func ValidShareCode(code string) bool { return shareCodeRe.MatchString(code) }

// ValidMagnet 磁力链接是否合法（参考实现 口径）。
func ValidMagnet(uri string) bool { return magnetRe.MatchString(uri) }

// NormalizeInfoHash 归一化 info hash：接受 40 位 hex 与 32 位 base32，
// 一律转成小写 40 位 hex。base32 用标准 base32 解码。
func NormalizeInfoHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if infoHashRe.MatchString(hash) {
		return hash
	}
	if base32Re.MatchString(hash) {
		return base32ToHex(hash)
	}
	return ""
}

// base32ToHex 把 RFC 4648 base32 解成 40 位小写 hex。
// 32 个字符 × 5 bit = 160 bit = 20 字节 = 40 个十六进制字符，一个字节都不多不少。
func base32ToHex(s string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	var buf, bits uint32
	out := make([]byte, 0, len(s)*5/4)
	for _, c := range s {
		idx := strings.IndexRune(alphabet, c)
		if idx < 0 {
			return ""
		}
		buf = buf<<5 | uint32(idx)
		bits += 5
		for bits >= 8 {
			bits -= 8
			out = append(out, byte(buf>>bits))
		}
	}
	const hexDigits = "0123456789abcdef"
	var sb strings.Builder
	for _, b := range out {
		sb.WriteByte(hexDigits[b>>4])
		sb.WriteByte(hexDigits[b&0x0f])
	}
	return sb.String()
}

func infoHashFromMagnet(uri string) string {
	i := strings.Index(uri, "xt=urn:btih:")
	if i < 0 {
		return ""
	}
	rest := uri[i+len("xt=urn:btih:"):]
	if idx := strings.IndexAny(rest, "&#?"); idx >= 0 {
		rest = rest[:idx]
	}
	return NormalizeInfoHash(rest)
}

// ClassifyKind 从链接形态判定 ItemKind。
func ClassifyKind(text string) ItemKind {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(strings.ToLower(text), "magnet:"):
		return ItemMagnet
	case ed2kRe.MatchString(strings.ToLower(text)):
		return ItemEd2k
	case strings.HasPrefix(text, "http://"), strings.HasPrefix(text, "https://"):
		return ItemDirectLink
	case strings.HasSuffix(strings.ToLower(text), ".torrent"):
		return ItemTorrent
	default:
		// 裸码（含空串）一律当分享码。
		//
		// 空串为什么不是「未知」：RE0 侧分享链接的链接类型字段本来就是空的
		// （见 internal/discover/discovery/subscriptions.go 的 resourceFromHive，
		// 它把空 PanType 的资源直接当成可转存的分享）。把空串判成未知会让
		// 唯一的在跑通道拿不到正确的 Kind。
		return ItemShareLink
	}
}

// ApplyQuality 用 internal/moviepilot.ParseQualityFromName 解析标题里的画质信息，
// 填进 Item 的 Resolution / Codec / HasHDR / HasAtmos / HasDTS。
//
// 刻意复用 moviepilot 那一份而不是另写一套：那个解析器已经过命名模板与洗版比较的
// 验证，另写一套必然出现「订阅里判 2160、整理时命名成 1080」这种对不上的情况。
func ApplyQuality(it *Item) {
	if it == nil {
		return
	}
	text := it.Title
	if text == "" {
		text = it.Remark
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	q := moviepilot.ParseQualityFromName(text)
	if q == nil {
		return
	}
	// 分辨率只在站方标签没给过的时候才填：ApplySpecTags 先跑，
	// 它的判定（盘方对实际文件的记录）比标题里压制组随手写的「2160p」可信。
	if it.Resolution == 0 {
		it.Resolution = q.Resolution
	}
	it.Codec = q.Codec
	switch strings.ToUpper(q.HDR) {
	case "HDR10", "HDR10+", "DOLBY-VISION", "DV", "IMAX":
		it.HasHDR = true
	}
	audio := strings.ToUpper(q.AudioTag)
	it.HasAtmos = strings.Contains(audio, "ATMOS")
	it.HasDTS = strings.Contains(audio, "DTS")
	// moviepilot 的 qualityAudioRe 只取**最左**一个匹配（FindStringSubmatch 不带 All），
	// 于是 "TrueHD.Atmos" / "Atmos.DTS-HD" 里排在左边的那个会盖掉右边的。
	// Dolby Atmos 在 "TrueHD.Atmos" 这种最常见的双标签写法里恰好总在右边，
	// 只看 AudioTag 会把 Atmos 判丢 —— 而订阅的 sdr/atmos 闸门正依赖它。
	// 这里直接在原文上再扫一遍（DTS 同理），不去动 moviepilot 的解析器（那个解析器同时
	// 服务整理侧的命名与洗版比较，改它影响面远超本任务）。
	if !it.HasAtmos && atmosRe.MatchString(text) {
		it.HasAtmos = true
	}
	if !it.HasDTS && dtsRe.MatchString(text) {
		it.HasDTS = true
	}
	// moviepilot 的 qualityFormatRe 只取**最左**一个匹配，而真实发布名里
	// WEB-DL / BluRay 几乎总是排在 HDR 前面（"Movie.2023.2160p.WEB-DL.DV.HDR10"），
	// 于是 q.HDR 恒为空、VideoFormat 拿到了 WEB-DL —— 也就是说这个解析器
	// 实际上几乎识别不出 HDR。
	//
	// 这里的处理与 Atmos/DTS 同理：在原文上直接扫一遍。
	// 不去改 moviepilot 的解析器 —— 它同时服务整理侧的命名与洗版比较，
	// 改它影响面远超本任务；而订阅的 sdr/hdr 闸门正依赖这个判断，
	// 识别不出来就等于闸门形同虚设（sdr 会把所有 HDR 片源都放行）。
	if !it.HasHDR && hdrRe.MatchString(text) {
		it.HasHDR = true
	}
}

// ApplySpecTags 从站方给的规格标签补画质（RE0 的 video_resolution/source 字段）。
//
// 站方标签优先于标题解析：标题写「4K HDR」但站点标签是「1080p」时，
// 以站点为准（那是盘方对实际文件的判定）。
func ApplySpecTags(it *Item, tags []string) {
	if it == nil || len(tags) == 0 {
		return
	}
	for _, tag := range tags {
		res := resolutionFromTag(tag)
		if res > it.Resolution {
			it.Resolution = res
		}
		lower := strings.ToLower(tag)
		switch {
		case strings.Contains(lower, "atmos"):
			it.HasAtmos = true
		case strings.Contains(lower, "dts"):
			it.HasDTS = true
		case strings.Contains(lower, "hdr") || strings.Contains(lower, "dv") || strings.Contains(lower, "dolby vision"):
			it.HasHDR = true
		}
	}
}

// resolutionFromTag 从规格标签取分辨率。RE0 用「1080p」「2160p」「4K」等写法。
func resolutionFromTag(tag string) int {
	lower := strings.ToLower(strings.TrimSpace(tag))
	switch {
	case strings.Contains(lower, "2160") || strings.Contains(lower, "4k") || strings.Contains(lower, "uhd"):
		return 2160
	case strings.Contains(lower, "1440"):
		return 1440
	case strings.Contains(lower, "1080") || strings.Contains(lower, "1080p") || strings.Contains(lower, "1080i"):
		return 1080
	case strings.Contains(lower, "720"):
		return 720
	case strings.Contains(lower, "480"):
		return 480
	default:
		return 0
	}
}

// ParseSizeBytes 解析人类可读体积（"12.5 GB" / "1.2TB" / "800MB" / 纯字节数字）。
// 解析不了返回 0。
func ParseSizeBytes(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	// 纯数字（无单位）一律当「未知」返回 0。
	//
	// 为什么不按字节解释：分享体积字段里出现裸数字时，各家站的单位约定并不统一
	//（有的是字节，有的是 KB，有的是 MB）。猜错会得到几个数量级的偏差，
	// 而体积闸门一旦拿到错值就会把正确的候选筛掉 —— 宁可当作没有体积，
	// 让闸门放行并记一行日志。
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return 0
	}
	// 从第一个数字或小数点开始截断，容忍 "约 3.2 GB" / "大小: 3.2GB" 这类前缀。
	start := strings.IndexAny(raw, "0123456789")
	if start < 0 {
		return 0
	}
	raw = strings.TrimSpace(strings.ReplaceAll(raw[start:], " ", ""))
	upper := strings.ToUpper(raw)
	// 先剥掉 "iB"（GiB/MiB 这类 IEC 写法），它们的倍数与 GB/MB 相同。
	if i := strings.Index(upper, "IB"); i >= 0 {
		upper = upper[:i]
	}
	var multiplier int64
	switch {
	case strings.HasSuffix(upper, "TB"), strings.HasSuffix(upper, "T"):
		multiplier = 1 << 40
	case strings.HasSuffix(upper, "GB"), strings.HasSuffix(upper, "G"):
		multiplier = 1 << 30
	case strings.HasSuffix(upper, "MB"), strings.HasSuffix(upper, "M"):
		multiplier = 1 << 20
	case strings.HasSuffix(upper, "KB"), strings.HasSuffix(upper, "K"):
		multiplier = 1 << 10
	case strings.HasSuffix(upper, "B"):
		multiplier = 1
	default:
		return 0
	}
	for _, suf := range []string{"TB", "GB", "MB", "KB", "T", "G", "M", "K", "B"} {
		if strings.HasSuffix(upper, suf) {
			upper = strings.TrimSuffix(upper, suf)
			break
		}
	}
	f, err := strconv.ParseFloat(upper, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(multiplier))
}
