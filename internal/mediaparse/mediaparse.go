// Package mediaparse 提供媒体文件名解析与整理归类规则（目录整理 / 命名对齐 / MoviePilot 上传整理共用）。
package mediaparse

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ParsedEpisode 文件名解析出的剧集信息。
type ParsedEpisode struct {
	Title   string
	Season  int
	Episode int
}

var (
	// sxxExx 匹配 S01E01 / s01e01 / S1E01 形式。
	sxxExx = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})\s*[ex](\d{1,3})(?:$|[^a-z0-9])`)
	// nxN 匹配 1x01 形式。
	nxN = regexp.MustCompile(`(?:^|[^a-z0-9])(\d{1,2})[xX](\d{1,3})(?:$|[^a-z0-9])`)
	// ep 匹配 EP01 / ep.01 形式。
	ep = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])ep\s*\.?\s*(\d{1,3})(?:$|[^a-z0-9])`)
	// chineseEp 匹配 第01集 / 第1话 形式（无前导边界，避免吞掉标题末位汉字）。
	chineseEp = regexp.MustCompile(`第\s*(\d{1,3})\s*[集話话]`)
	// yearRe 匹配 19xx / 20xx 年份。
	yearRe = regexp.MustCompile(`(19|20)\d{2}`)
	// miscRe 匹配片源/画质等杂讯词。
	miscRe = regexp.MustCompile(`(?i)(1080p|720p|2160p|4k|bluray|blu-ray|web-?dl|webrip|hdr|hdr10|dolby|atmos|ddp?|5\.1|7\.1|ac3|aac|hevc|h\.?26[45]|x26[45]|xvid|chd|chdw|hd|sdr|repack|proper|extended|remux|uhd)`)
)

// VideoExts 整理的视频扩展名（与 STRM 默认视频扩展名一致）。
var VideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".webm": true, ".m4v": true, ".3gp": true, ".ts": true,
}

// IsVideoExt 判断扩展名是否为识别的视频。
func IsVideoExt(name string) bool {
	return VideoExts[strings.ToLower(path.Ext(name))]
}

// ParseEpisode 从文件名解析剧集信息，解析失败返回 false。
func ParseEpisode(fileName string) (ParsedEpisode, bool) {
	ext := path.Ext(fileName)
	stem := strings.TrimSuffix(fileName, ext)

	if m := sxxExx.FindStringSubmatchIndex(stem); m != nil {
		return ParsedEpisode{
			Title:   CleanTitle(stem[:m[0]]),
			Season:  atoiRange(stem, m[2], m[3]),
			Episode: atoiRange(stem, m[4], m[5]),
		}, true
	}
	if m := nxN.FindStringSubmatchIndex(stem); m != nil {
		return ParsedEpisode{
			Title:   CleanTitle(stem[:m[0]]),
			Season:  atoiRange(stem, m[2], m[3]),
			Episode: atoiRange(stem, m[4], m[5]),
		}, true
	}
	if m := ep.FindStringSubmatchIndex(stem); m != nil {
		return ParsedEpisode{
			Title:   CleanTitle(stem[:m[0]]),
			Episode: atoiRange(stem, m[2], m[3]),
		}, true
	}
	if m := chineseEp.FindStringSubmatchIndex(stem); m != nil {
		// 防止把年份等数字结尾误当标题一部分（如 "2024第3集"），要求 "第" 前不是数字。
		if m[0] > 0 && stem[m[0]-1] >= '0' && stem[m[0]-1] <= '9' {
			return ParsedEpisode{}, false
		}
		return ParsedEpisode{
			Title:   CleanTitle(stem[:m[0]]),
			Episode: atoiRange(stem, m[2], m[3]),
		}, true
	}
	return ParsedEpisode{}, false
}

// CleanTitle 清洗剧名：去除季集匹配前的尾部杂讯，并将常见分隔符归一为空格。
func CleanTitle(raw string) string {
	raw = strings.TrimRight(raw, " \t._-[]()·:,;，。；'\"!！?？&")
	repl := strings.NewReplacer(".", " ", "_", " ", "-", " ", "·", " ", "(", " ", ")", " ", "[", " ", "]", " ", "【", " ", "】", " ")
	fields := strings.Fields(repl.Replace(raw))
	return strings.Join(fields, " ")
}

func atoiRange(s string, start, end int) int {
	v, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0
	}
	return v
}

// ParseMedia 解析文件名归类：返回分类、标题、季、集、年份。
// movie：含年份；tv：含季集信息；unknown：其他。
func ParseMedia(name string) (category string, title string, season int, episode int, year int) {
	stem := strings.TrimSuffix(name, path.Ext(name))
	if parsed, ok := ParseEpisode(stem); ok {
		title = CleanTitle(parsed.Title)
		season = parsed.Season
		episode = parsed.Episode
		if season == 0 {
			season = 1
		}
		if m := yearRe.FindString(title); m != "" {
			year, _ = strconv.Atoi(m)
			title = strings.TrimSpace(strings.Replace(title, m, "", 1))
		}
		return "tv", title, season, episode, year
	}
	title = CleanTitle(stem)
	title = miscRe.ReplaceAllString(title, " ")
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	if m := yearRe.FindString(stem); m != "" {
		year, _ = strconv.Atoi(m)
		title = CleanTitle(strings.Replace(stem, m, "", 1))
		title = miscRe.ReplaceAllString(title, " ")
		title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
		return "movie", title, 0, 0, year
	}
	return "unknown", title, 0, 0, 0
}

// BuildTargetRelPath 构建目标相对路径（相对整理根目录）。
// movie → 电影/标题 (年份)；tv → 剧集/标题 (年份)/Season XX；无法归类返回 false。
func BuildTargetRelPath(category string, title string, season int, year int) (string, bool) {
	if category == "movie" {
		if title == "" {
			return "", false
		}
		if year > 0 {
			return fmt.Sprintf("电影/%s (%d)", title, year), true
		}
		return fmt.Sprintf("电影/%s", title), true
	}
	if category == "tv" {
		if title == "" {
			return "", false
		}
		base := fmt.Sprintf("剧集/%s", title)
		if year > 0 {
			base = fmt.Sprintf("剧集/%s (%d)", title, year)
		}
		return fmt.Sprintf("%s/Season %02d", base, season), true
	}
	return "", false
}

// BuildEpisodeNewName 按媒体类型与标题生成规范化文件名，无法生成时返回 false。
// mediaType 为 movie 或 tv；tv 时依赖 parsed 的季集信息。
func BuildEpisodeNewName(parsed ParsedEpisode, mediaTitle string, mediaType string, year int, ext string) (string, bool) {
	title := strings.TrimSpace(mediaTitle)
	if title == "" {
		title = parsed.Title
	}
	if title == "" {
		return "", false
	}
	switch mediaType {
	case "movie":
		if year > 0 {
			return fmt.Sprintf("%s (%d)%s", title, year, ext), true
		}
		return title + ext, true
	default:
		season := parsed.Season
		if season == 0 {
			season = 1
		}
		if parsed.Episode == 0 {
			return "", false
		}
		return fmt.Sprintf("%s S%02dE%02d%s", title, season, parsed.Episode, ext), true
	}
}
