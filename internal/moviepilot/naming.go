package moviepilot

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ---- 质量标签段提取（命名追加用的原始标签） ----

var (
	autoBracketYearRe = regexp.MustCompile(`\([^()]*\d{4}[^()]*\)`)
	autoSxxExxRe      = regexp.MustCompile(`(?i)\bs\d{1,2}\s*[ex]\d{1,3}\b`)
	autoEpRe          = regexp.MustCompile(`(?i)\bep\s*\.?\s*\d{1,3}\b`)
	autoChineseEpRe   = regexp.MustCompile(`第\s*\d{1,3}\s*[集話话]`)
	autoYearRe        = regexp.MustCompile(`(19|20)\d{2}`)
	autoNxNRe         = regexp.MustCompile(`(?:^|[^a-z0-9])\d{1,2}[xX]\d{1,3}(?:$|[^a-z0-9])`)
)

// ExtractQualityTags 从原始文件名中提取质量标签段（标题/年份/季集之外的部分），
// 如 "花开锦绣.S01E01.第1集.2160p.WEB-DL.H.265.60fps-Ocat.mp4" → "2160p.WEB-DL.H.265.60fps-Ocat"。
// 在原始字符串上定位并剥离标题/季集/年份标记，保留质量标签内部的连字符与点号。
func ExtractQualityTags(fileName, title string) string {
	stem := strings.TrimSuffix(fileName, path.Ext(fileName))
	// 1. 剥离标题：按分词顺序在原名中定位标题区间
	if t := strings.TrimSpace(title); t != "" {
		if loc := titleSpanInStem(stem, t); loc[0] >= 0 {
			stem = stem[:loc[0]] + " " + stem[loc[1]:]
		}
	}
	// 2. 剥离结构标记与年份
	stem = autoBracketYearRe.ReplaceAllString(stem, " ")
	stem = autoSxxExxRe.ReplaceAllString(stem, " ")
	stem = autoEpRe.ReplaceAllString(stem, " ")
	stem = autoChineseEpRe.ReplaceAllString(stem, " ")
	stem = autoNxNRe.ReplaceAllString(stem, " ")
	stem = autoYearRe.ReplaceAllString(stem, " ")
	// 3. 归一化分隔符后按点拆分，内部的 . / - 由 join 还原（DDP5.1、WEB-DL 自洽）
	stem = strings.NewReplacer("_", ".", " ", ".").Replace(stem)
	parts := make([]string, 0, 8)
	for _, p := range strings.Split(stem, ".") {
		if p = strings.Trim(p, " ._()-[]"); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ".")
}

// titleSpanInStem 按标题分词顺序定位标题在原名中的区间，找不到返回 [-1,-1]。大小写不敏感。
func titleSpanInStem(stem, title string) [2]int {
	tokens := strings.Fields(normalizeTagToken(title))
	if len(tokens) == 0 {
		return [2]int{-1, -1}
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		quoted = append(quoted, regexp.QuoteMeta(tok))
	}
	re := regexp.MustCompile(`(?i)` + strings.Join(quoted, `[._\-\s]*`))
	if match := re.FindStringSubmatchIndex(stem); match != nil {
		return [2]int{match[0], match[1]}
	}
	// 中文无分隔符标题直接整体匹配
	if pos := strings.Index(strings.ToLower(stem), strings.ToLower(title)); pos >= 0 {
		return [2]int{pos, pos + len(title)}
	}
	return [2]int{-1, -1}
}

func normalizeTagToken(s string) string {
	repl := strings.NewReplacer(".", " ", "_", " ", "-", " ", "·", " ", "(", " ", ")", " ", "[", " ", "]", " ")
	return strings.Join(strings.Fields(repl.Replace(s)), " ")
}

// ---- 整理目标路径与命名 ----

// BuildOrganizeRelDir 由媒体信息构建整理目标相对目录：
// 「{分类}/{标题 (年份) {tmdb=xxx}}」电影，剧集再追加 「/Season NN」。
// 标题为空或 TMDB ID 缺失时返回 false（不整理）。
func BuildOrganizeRelDir(category, title string, year, season int, tmdbID int64, categoryName string) (string, bool) {
	if strings.TrimSpace(title) == "" || tmdbID <= 0 {
		return "", false
	}
	titlePart := fmt.Sprintf("%s (%d) {tmdb=%d}", title, year, tmdbID)
	if year <= 0 {
		titlePart = fmt.Sprintf("%s {tmdb=%d}", title, tmdbID)
	}
	if categoryName == "" {
		categoryName = "未分类"
	}
	base := fmt.Sprintf("%s/%s", categoryName, titlePart)
	if category == "tv" {
		if season <= 0 {
			season = 1
		}
		return fmt.Sprintf("%s/Season %02d", base, season), true
	}
	return base, true
}

// BuildOrganizeNewName 按整理规则生成规范化文件名：
// 剧集「标题.年份.S01E01.第1集.ext」，电影「标题 (年份).ext」。
func BuildOrganizeNewName(category, title string, season, episode, year int, ext string) string {
	if category == "tv" {
		if episode <= 0 {
			episode = 1
		}
		if year > 0 {
			return fmt.Sprintf("%s.%d.S%02dE%02d.第%d集%s", title, year, season, episode, episode, ext)
		}
		return fmt.Sprintf("%s.S%02dE%02d.第%d集%s", title, season, episode, episode, ext)
	}
	if year > 0 {
		return fmt.Sprintf("%s (%d)%s", title, year, ext)
	}
	return title + ext
}

// AppendQualityTagsToName 在规范化文件名上追加原始文件的质量标签段，
// 与云盘自动整理命名格式对齐。已含质量标签的末段不重复追加。
func AppendQualityTagsToName(category, title string, season, episode, year int, ext string, tags string) string {
	tags = strings.TrimSpace(tags)
	if tags == "" {
		return BuildOrganizeNewName(category, title, season, episode, year, ext)
	}
	base := strings.TrimSuffix(BuildOrganizeNewName(category, title, season, episode, year, ext), ext)
	if base == "" {
		return base + ext
	}
	// 末段已是质量 token 或纯组名则不重复追加
	lastTok := base
	if idx := strings.LastIndexByte(base, '.'); idx >= 0 {
		lastTok = base[idx+1:]
	}
	if washQualityTokenRe.MatchString(lastTok) || pureGroupTokenRe.MatchString(lastTok) {
		return base + ext
	}
	return base + "." + tags + ext
}

// OrganizeRootPath 由上传根目录推导整理根目录：父目录下的「已整理」。
// 例如 /影视/待整理 → /影视/已整理。
func OrganizeRootPath(uploadRoot string) string {
	p := strings.TrimRight(uploadRoot, "/")
	if idx := strings.LastIndex(p, "/"); idx > 0 {
		return p[:idx] + "/已整理"
	}
	return "已整理"
}

// FailedRootPath 由待整理目录推导默认失败目录：父目录下的「整理失败」。
func FailedRootPath(uploadRoot string) string {
	p := strings.TrimRight(uploadRoot, "/")
	if idx := strings.LastIndex(p, "/"); idx > 0 {
		return p[:idx] + "/整理失败"
	}
	return "整理失败"
}

// ---- 洗版同名匹配 ----

// washQualityTokenRe 质量 token 识别（洗版同名匹配时剥离质量后缀用）。
var washQualityTokenRe = regexp.MustCompile(`(?i)^(2160p|4k|uhd|1440p|1080p|1080i|720p|576p|540p|480p|h265|hevc|x265|av1|h264|avc|x264|h\.265|h\.264|mpeg4|xvid|divx|mpeg2|h263|vc1|atmos|truehd|dts[\w.-]*|eac3|ddp|ac3|dd5\.1|dd2\.0|dolby[\w.-]*|7\.1|5\.1|5\.0|2\.0|stereo|mono|aac|flac|lpcm|opus|bd[\w.-]*|remux|blu[\w.-]*ray|web[\w.-]*(?:dl|rip)?|hdtv|hdr10\+?|dolby[\w.-]*vision|dv|imax|10bit|12bit|8bit|60fps|50fps|30fps|25fps|24fps|uncut|unrated|extended|theatrical|remastered|1080|720|4k)$`)

// pureGroupTokenRe 纯字母组名 token（Ocat / FRDS），不含数字避免误剥 S01E02。
var pureGroupTokenRe = regexp.MustCompile(`^[A-Za-z]{2,20}$`)

// stripQualitySuffix 剥离文件名主干末尾的质量标签 token 序列（调用方已去扩展名）。
// 兼容「质量token-组名」复合 token（如 60fps-Ocat）。
func stripQualitySuffix(stem string) string {
	parts := strings.Split(stem, ".")
	for len(parts) > 0 {
		last := strings.TrimSpace(parts[len(parts)-1])
		if washQualityTokenRe.MatchString(last) || pureGroupTokenRe.MatchString(last) {
			parts = parts[:len(parts)-1]
			continue
		}
		// 复合 token：尾部 -组名 → 去掉组名后重测
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

// WashCoreKey 用于同名匹配的核心键：去扩展名 → 去质量后缀 → 小写。
func WashCoreKey(fileName string) string {
	stem := strings.TrimSuffix(fileName, path.Ext(fileName))
	return strings.ToLower(stripQualitySuffix(stem))
}

var (
	epKeySxxExxRe = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})`)
	epKeyChinese  = regexp.MustCompile(`第\s*(\d{1,3})\s*[集話话]`)
)

// EpisodeKeyOf 提取文件名的剧集键（"S01E05" 规范化），无剧集信息返回空。
func EpisodeKeyOf(name string) string {
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

// IsVideoFile 判断是否为视频文件（比 mediaparse.VideoExts 更宽，含 iso/bdmv 等）。
func IsVideoFile(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".ts", ".m2ts", ".mov", ".wmv", ".flv",
		".rmvb", ".rm", ".m4v", ".webm", ".mpg", ".mpeg", ".iso", ".bdmv":
		return true
	}
	return false
}

// WashEpisodeLabel 从文件名提取「（S01E08）」式集号标签，无则空串。
func WashEpisodeLabel(fileName string) string {
	if ep := EpisodeKeyOf(fileName); ep != "" {
		return "（" + ep + "）"
	}
	return ""
}

// SizeGBText 字节数转「3.21GB」展示文本。
func SizeGBText(n int64) string {
	return fmt.Sprintf("%.2fGB", float64(n)/(1<<30))
}
