package embywebhook

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// newItemCountPattern 匹配 Emby 4.8 批量入库的合并事件标题，例如「将 12 项目添加到 剧名」。
var newItemCountPattern = regexp.MustCompile(`将\s*(\d+)\s*项目添加到`)

// ParseNewItemCountFromTitle 从合并事件标题里解析新增条目数；解析不出返回 0。
func ParseNewItemCountFromTitle(title string) int {
	matches := newItemCountPattern.FindStringSubmatch(title)
	if len(matches) < 2 {
		return 0
	}
	count, err := strconv.Atoi(matches[1])
	if err != nil || count <= 0 {
		return 0
	}
	return count
}

// SummarizeReleaseGroups 从文件名列表里归纳发布组，形如「FRDS×3, CHD」。
// 规则沿用老版：去掉扩展名后取最后一个 "-" 之后的部分作为候选，
// 过滤掉空、过长（>32）和含数字/点的候选项，按出现次数降序、同次数按名称升序，最多保留 3 个。
func SummarizeReleaseGroups(fileNames []string) string {
	counts := map[string]int{}
	for _, name := range fileNames {
		group := releaseGroupFromFileName(name)
		if group == "" {
			continue
		}
		counts[group]++
	}
	if len(counts) == 0 {
		return ""
	}

	groups := make([]string, 0, len(counts))
	for name := range counts {
		groups = append(groups, name)
	}
	sort.Slice(groups, func(i, j int) bool {
		if counts[groups[i]] != counts[groups[j]] {
			return counts[groups[i]] > counts[groups[j]]
		}
		return groups[i] < groups[j]
	})
	if len(groups) > 3 {
		groups = groups[:3]
	}

	parts := make([]string, 0, len(groups))
	for _, name := range groups {
		if counts[name] > 1 {
			parts = append(parts, name+"×"+strconv.Itoa(counts[name]))
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// releaseGroupFromFileName 从单个文件名里取发布组候选，取不到返回空串。
func releaseGroupFromFileName(fileName string) string {
	if fileName == "" {
		return ""
	}
	// 去掉目录部分与扩展名。
	base := fileName
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	idx := strings.LastIndex(base, "-")
	if idx < 0 || idx == len(base)-1 {
		return ""
	}
	candidate := strings.TrimSpace(base[idx+1:])
	if candidate == "" || len(candidate) > 32 {
		return ""
	}
	// 含数字或点的候选多半是分辨率/编码片段，不是发布组。
	if strings.ContainsAny(candidate, ".0123456789") {
		return ""
	}
	return candidate
}

// FormatBytesHuman 把字节数格式化成人类可读的体积，例如 1.50 GB。
func FormatBytesHuman(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	value := float64(size)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == "EB" {
			return strconv.FormatFloat(value, 'f', 2, 64) + " " + unit
		}
	}
	return strconv.FormatInt(size, 10) + " B"
}

// FormatSeasonEpisodes 把「季 → 集列表」渲染成 S01E01-E03, E05; S02E01 形式。
func FormatSeasonEpisodes(seasons map[int][]int) string {
	if len(seasons) == 0 {
		return ""
	}
	seasonNumbers := make([]int, 0, len(seasons))
	for season := range seasons {
		seasonNumbers = append(seasonNumbers, season)
	}
	sort.Ints(seasonNumbers)

	parts := make([]string, 0, len(seasonNumbers))
	for _, season := range seasonNumbers {
		episodes := dedupeInts(seasons[season])
		if len(episodes) == 0 {
			continue
		}
		var builder strings.Builder
		builder.WriteString("S")
		builder.WriteString(strconv.Itoa(season))
		index := 0
		first := true
		for index < len(episodes) {
			end := index
			for end+1 < len(episodes) && episodes[end+1] == episodes[end]+1 {
				end++
			}
			if !first {
				builder.WriteString(", ")
			}
			first = false
			if end > index {
				builder.WriteString("E")
				builder.WriteString(strconv.Itoa(episodes[index]))
				builder.WriteString("-E")
				builder.WriteString(strconv.Itoa(episodes[end]))
			} else {
				builder.WriteString("E")
				builder.WriteString(strconv.Itoa(episodes[index]))
			}
			index = end + 1
		}
		parts = append(parts, builder.String())
	}
	return strings.Join(parts, "; ")
}

// dedupeInts 去重并升序排序。
func dedupeInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

// FormatTicksToTime 把 Emby 的 tick（100 纳秒）转成 HH:MM:SS 或 MM:SS。
func FormatTicksToTime(ticks int64) string {
	totalSeconds := ticks / 10000000
	if totalSeconds < 0 {
		totalSeconds = 0
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return pad2(hours) + ":" + pad2(minutes) + ":" + pad2(seconds)
	}
	return pad2(minutes) + ":" + pad2(seconds)
}

// pad2 把整数格式化成至少两位。
func pad2(value int64) string {
	if value < 10 {
		return "0" + strconv.FormatInt(value, 10)
	}
	return strconv.FormatInt(value, 10)
}

// formatRate 渲染评分：<=0 显示「暂无评分」，否则保留一位小数。
func formatRate(rate float64) string {
	if rate <= 0 {
		return "暂无评分"
	}
	return strconv.FormatFloat(rate, 'f', 1, 64)
}

// joinOrPlaceholder 连接列表，空列表返回占位文案。
func joinOrPlaceholder(values []string, placeholder string) string {
	if len(values) == 0 {
		return placeholder
	}
	return strings.Join(values, ", ")
}
