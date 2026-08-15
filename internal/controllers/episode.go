package controllers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 剧集号解析：从频道帖子文本提取「季+集」标识，用于按集去重与完结判定。
// 帖子文本示例：……S01 E13 4K……、……S01 E24-E28 4K……、……第 10 集……

var (
	reSeasonEp = regexp.MustCompile(`(?i)S(\d{1,2})\s*E(\d{1,3})(?:\s*[-–—~]\s*E?(\d{1,3}))?`)
	reBareEp   = regexp.MustCompile(`(?i)(?:^|[^A-Za-z])E(\d{1,3})\b`)
	reCnEp     = regexp.MustCompile(`第(\d{1,3})集`)
)

// ParseEpisodeKeys 解析帖子文本中的剧集标识列表（按季+集规范化，如 S01E13）。
// fallbackSeason 用于帖内未带季号时的归属季（订阅指定单季时传入该季号，0 表示未知）。
func ParseEpisodeKeys(text string, fallbackSeason int) []string {
	seen := map[string]bool{}
	add := func(season, ep int) {
		if season > 0 {
			addKey(fmt.Sprintf("S%02dE%02d", season, ep), seen)
			return
		}
		addKey(fmt.Sprintf("E%02d", ep), seen)
	}

	// SxxEyy / SxxEyy-Ezz（含区间展开）
	for _, m := range reSeasonEp.FindAllStringSubmatch(text, -1) {
		s, _ := strconv.Atoi(m[1])
		e1, _ := strconv.Atoi(m[2])
		e2 := e1
		if m[3] != "" {
			e2, _ = strconv.Atoi(m[3])
		}
		if e2 < e1 || e2-e1 > 300 {
			e2 = e1
		}
		for e := e1; e <= e2; e++ {
			add(s, e)
		}
	}

	// 剔除已带季号的片段后，处理独立 Eyy / 第 N 集（归属 fallbackSeason）
	stripped := reSeasonEp.ReplaceAllString(text, " ")
	for _, m := range reBareEp.FindAllStringSubmatch(stripped, -1) {
		if e, err := strconv.Atoi(m[1]); err == nil {
			add(fallbackSeason, e)
		}
	}
	for _, m := range reCnEp.FindAllStringSubmatch(stripped, -1) {
		if e, err := strconv.Atoi(m[1]); err == nil {
			add(fallbackSeason, e)
		}
	}
	return keysOf(seen)
}

func addKey(k string, seen map[string]bool) {
	if k != "" {
		seen[k] = true
	}
}

func keysOf(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// JoinEpisodeKeys 集号列表规范化为存储串（空则返回空串）
func JoinEpisodeKeys(keys []string) string {
	return strings.Join(keys, ",")
}