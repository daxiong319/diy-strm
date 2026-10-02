package embyindex

import (
	"regexp"
	"strconv"
	"strings"
)

// 季目录判定的正则，与老版 internal/helpers/extract.go:623 保持一致。
var (
	// seasonWordRe 匹配 "Season 01" / "season 1" 这类写法。
	seasonWordRe = regexp.MustCompile(`(?i)season\s+0*(\d+)`)
	// seasonShortRe 匹配结尾的 "S01" 这类写法。
	seasonShortRe = regexp.MustCompile(`(?i)s(\d+)$`)
	// tvshowSeasonRe 匹配剧目录里出现的 "S01"（不要求结尾）。
	tvshowSeasonRe = regexp.MustCompile(`(?i)S(\d{1,3})`)
)

// ExtractSeasonNumberFromDirName 从目录名解析季号，解析不出返回 -1。
//
// 老版 helpers.ExtractSeasonsFromSeasonPath 的行为契约：
//   - 空串返回 -1；
//   - 首字符必须是 s 或 S，否则返回 -1（这正是「是否为独立季目录」的判据）；
//   - 先把首字母规范成大写 S；
//   - 先试 `(?i)season\s+0*(\d+)`，再试 `(?i)s(\d+)$`；
//   - 都匹配不上返回 -1。
//
// 返回值 >= 0 表示这是一个独立的季目录，可以被整体删除。
func ExtractSeasonNumberFromDirName(text string) int {
	if text == "" {
		return -1
	}
	first := text[0]
	if first != 's' && first != 'S' {
		return -1
	}
	if first == 's' {
		text = "S" + text[1:]
	}
	if matches := seasonWordRe.FindStringSubmatch(text); matches != nil {
		if number, err := strconv.Atoi(matches[1]); err == nil {
			return number
		}
	}
	if matches := seasonShortRe.FindStringSubmatch(text); matches != nil {
		if number, err := strconv.Atoi(matches[1]); err == nil {
			return number
		}
	}
	return -1
}

// ExtractSeasonFromTvshowDirName 从剧目录名解析季号，解析不出返回 -1。
// 与老版 helpers.ExtractSeasonFromTvshowPath 一致：首字符 s/S，`(?i)S(\d{1,3})`。
func ExtractSeasonFromTvshowDirName(text string) int {
	if text == "" {
		return -1
	}
	first := text[0]
	if first != 's' && first != 'S' {
		return -1
	}
	if first == 's' {
		text = "S" + text[1:]
	}
	if matches := tvshowSeasonRe.FindStringSubmatch(text); matches != nil {
		if number, err := strconv.Atoi(matches[1]); err == nil {
			return number
		}
	}
	return -1
}

// baseNameWithoutExt 去掉文件名扩展名，用于匹配同名元数据文件
// （老版用 strings.TrimSuffix(fileName, filepath.Ext(fileName))）。
func baseNameWithoutExt(fileName string) string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return ""
	}
	index := strings.LastIndex(fileName, ".")
	if index <= 0 {
		return fileName
	}
	return fileName[:index]
}
