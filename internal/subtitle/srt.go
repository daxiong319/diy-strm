package subtitle

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Timing 是一条字幕的时间区间（毫秒）。
type Timing struct {
	Start int64
	End   int64
}

// SubtitleDoc 是解析后的字幕文档。
//
// 保留原始行结构（RawLines）而不是重新渲染，只改时间行：
// ASS 的样式标签、SRT 的序号、文本内容都必须原样保留，否则会破坏排版。
type SubtitleDoc struct {
	Format     SubtitleFormat
	Entries    []any
	Timings    []Timing
	RawLines   []string
	LineEnding string
}

// ErrUnsupportedSyncFormat 表示该字幕格式无法做时间轴校正。
var ErrUnsupportedSyncFormat = errors.New("该字幕格式不支持时间轴校正（仅支持 srt/ass/ssa/vtt）")

var (
	srtTimePattern       = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})`)
	assTimePattern       = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[.:](\d{1,2})`)
	vttTimePattern       = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})\.(\d{3})`)
	assSingleTimePattern = regexp.MustCompile(`^(\d{1,2}):(\d{2}):(\d{2})[.:](\d{1,2})$`)
)

type srtEntry struct {
	index    int
	timeLine int
}

type assEntry struct {
	line   int
	prefix string
	suffix string
}

type vttEntry struct {
	line   int
	prefix string
	suffix string
}

// ParseSubtitleFile 读文件并解析。
func ParseSubtitleFile(path string) (*SubtitleDoc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取字幕文件失败：%w", err)
	}
	format := DetectFormat(path)
	return ParseSubtitleContent(string(raw), format)
}

// ParseSubtitleContent 解析字幕文本；format 为空时按内容嗅探。
func ParseSubtitleContent(content string, format SubtitleFormat) (*SubtitleDoc, error) {
	if format == "" {
		format = sniffSubtitleFormat(content)
	}
	switch format {
	case FormatSRT:
		return parseSRT(content)
	case FormatASS, FormatSSA:
		return parseASS(content)
	case FormatVTT:
		return parseVTT(content)
	case FormatSUP, "":
		return nil, ErrUnsupportedSyncFormat
	default:
		return nil, ErrUnsupportedSyncFormat
	}
}

// sniffSubtitleFormat 按内容前缀判断格式。
func sniffSubtitleFormat(content string) SubtitleFormat {
	sample := content
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	lower := strings.ToLower(sample)
	if strings.Contains(lower, "[script info]") || strings.Contains(lower, "dialogue:") {
		return FormatASS
	}
	if strings.HasPrefix(strings.TrimSpace(lower), "webvtt") {
		return FormatVTT
	}
	if srtTimePattern.MatchString(sample) {
		return FormatSRT
	}
	return ""
}

// detectLineEnding 判断换行风格，序列化时按原样还原。
func detectLineEnding(content string) string {
	if strings.Contains(content, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// splitLines 统一按 LF 切行。
func splitLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.Split(content, "\n")
}

func parseSRT(content string) (*SubtitleDoc, error) {
	doc := &SubtitleDoc{
		Format:     FormatSRT,
		RawLines:   splitLines(content),
		LineEnding: detectLineEnding(content),
	}
	for i, line := range doc.RawLines {
		matches := srtTimePattern.FindAllStringSubmatch(line, -1)
		if len(matches) < 2 {
			continue
		}
		start := hmsToMs(matches[0][1], matches[0][2], matches[0][3], matches[0][4])
		end := hmsToMs(matches[1][1], matches[1][2], matches[1][3], matches[1][4])
		doc.Entries = append(doc.Entries, srtEntry{index: len(doc.Entries), timeLine: i})
		doc.Timings = append(doc.Timings, Timing{Start: start, End: end})
	}
	if len(doc.Timings) == 0 {
		return nil, errors.New("未在 SRT 文件中解析到任何时间轴")
	}
	return doc, nil
}

func parseASS(content string) (*SubtitleDoc, error) {
	doc := &SubtitleDoc{
		Format:     FormatASS,
		RawLines:   splitLines(content),
		LineEnding: detectLineEnding(content),
	}
	for i, line := range doc.RawLines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "dialogue:") {
			continue
		}
		// 时间戳在逗号切分后的第 2、3 段；用正则直接在整行里找两个时间戳更稳，
		// 因为样式字段数量随版本（v4.00/v4.00+）变化。
		matches := assTimePattern.FindAllStringSubmatch(trimmed, -1)
		if len(matches) < 2 {
			continue
		}
		startIdx := strings.Index(trimmed, matches[0][0])
		endOfFirst := startIdx + len(matches[0][0])
		endIdx := strings.Index(trimmed[endOfFirst:], matches[1][0])
		if startIdx < 0 || endIdx < 0 {
			continue
		}
		endIdx += endOfFirst
		start := hmsToMs(matches[0][1], matches[0][2], matches[0][3], matches[0][4])
		end := hmsToMs(matches[1][1], matches[1][2], matches[1][3], matches[1][4])
		doc.Entries = append(doc.Entries, assEntry{
			line:   i,
			prefix: trimmed[:startIdx],
			suffix: trimmed[endIdx+len(matches[1][0]):],
		})
		doc.Timings = append(doc.Timings, Timing{Start: start, End: end})
	}
	if len(doc.Timings) == 0 {
		return nil, errors.New("未在 ASS/SSA 文件中解析到任何 Dialogue 行")
	}
	return doc, nil
}

func parseVTT(content string) (*SubtitleDoc, error) {
	doc := &SubtitleDoc{
		Format:     FormatVTT,
		RawLines:   splitLines(content),
		LineEnding: detectLineEnding(content),
	}
	for i, line := range doc.RawLines {
		if !strings.Contains(line, "-->") {
			continue
		}
		matches := vttTimePattern.FindAllStringSubmatch(line, -1)
		if len(matches) < 2 {
			continue
		}
		startIdx := strings.Index(line, matches[0][0])
		endOfFirst := startIdx + len(matches[0][0])
		endIdx := strings.Index(line[endOfFirst:], matches[1][0])
		if startIdx < 0 || endIdx < 0 {
			continue
		}
		endIdx += endOfFirst
		start := hmsToMs(matches[0][1], matches[0][2], matches[0][3], matches[0][4])
		end := hmsToMs(matches[1][1], matches[1][2], matches[1][3], matches[1][4])
		doc.Entries = append(doc.Entries, vttEntry{
			line:   i,
			prefix: line[:startIdx],
			suffix: line[endIdx+len(matches[1][0]):],
		})
		doc.Timings = append(doc.Timings, Timing{Start: start, End: end})
	}
	if len(doc.Timings) == 0 {
		return nil, errors.New("未在 WebVTT 文件中解析到任何 cue")
	}
	return doc, nil
}

// hmsToMs 把时/分/秒/小数部分换算成毫秒。
// 小数位数不定：1 位按十分之一秒（×100），2 位按厘秒（×10），3 位是毫秒。
func hmsToMs(h, m, s, frac string) int64 {
	hours := atoi64Safe(h)
	minutes := atoi64Safe(m)
	seconds := atoi64Safe(s)

	var fracMs int64
	switch len(frac) {
	case 1:
		fracMs = atoi64Safe(frac) * 100
	case 2:
		fracMs = atoi64Safe(frac) * 10
	default:
		fracMs = atoi64Safe(frac)
	}
	return ((hours*60+minutes)*60+seconds)*1000 + fracMs
}

func msToSRTTime(ms int64) string {
	h, m, s, milli := splitMs(ms)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, milli)
}

func msToASSTime(ms int64) string {
	h, m, s, milli := splitMs(ms)
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, milli/10)
}

func msToVTTTime(ms int64) string {
	h, m, s, milli := splitMs(ms)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, milli)
}

func splitMs(ms int64) (h, m, s, milli int64) {
	if ms < 0 {
		ms = 0
	}
	milli = ms % 1000
	total := ms / 1000
	s = total % 60
	total /= 60
	m = total % 60
	h = total / 60
	return
}

func atoi64Safe(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func orZero(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// ApplyShift 对全部条目做 t' = t*scale + offsetMs 变换。
// scale<=0 视为 1；start 不为负；end 必须严格大于 start，
// 否则极端缩放会把字幕压成 0 长度而无法显示。
func ApplyShift(doc *SubtitleDoc, scale float64, offsetMs int64) {
	if doc == nil {
		return
	}
	if scale <= 0 {
		scale = 1
	}
	for i := range doc.Timings {
		start := int64(float64(doc.Timings[i].Start)*scale) + offsetMs
		end := int64(float64(doc.Timings[i].End)*scale) + offsetMs
		if start < 0 {
			start = 0
		}
		if end <= start {
			end = start + 1
		}
		doc.Timings[i].Start = start
		doc.Timings[i].End = end
		doc.writeBack(i)
	}
}

// writeBack 把第 i 条的时间写回原始行，只替换时间片段。
func (doc *SubtitleDoc) writeBack(i int) {
	if doc == nil || i < 0 || i >= len(doc.Entries) {
		return
	}
	timing := doc.Timings[i]
	switch entry := doc.Entries[i].(type) {
	case srtEntry:
		if entry.timeLine < 0 || entry.timeLine >= len(doc.RawLines) {
			return
		}
		line := doc.RawLines[entry.timeLine]
		doc.RawLines[entry.timeLine] = replaceNthTimes(line, srtTimePattern, 2,
			msToSRTTime(timing.Start), msToSRTTime(timing.End))
	case assEntry:
		if entry.line < 0 || entry.line >= len(doc.RawLines) {
			return
		}
		doc.RawLines[entry.line] = entry.prefix + msToASSTime(timing.Start) +
			"-->" + msToASSTime(timing.End) + entry.suffix
	case vttEntry:
		if entry.line < 0 || entry.line >= len(doc.RawLines) {
			return
		}
		doc.RawLines[entry.line] = entry.prefix + msToVTTTime(timing.Start) +
			" --> " + msToVTTTime(timing.End) + entry.suffix
	}
}

// replaceNthTimes 把前 n 个匹配依次替换成 replacements（不足则原样保留）。
func replaceNthTimes(line string, pattern *regexp.Regexp, n int, replacements ...string) string {
	idxs := pattern.FindAllStringIndex(line, -1)
	if len(idxs) == 0 || n <= 0 {
		return line
	}
	var b strings.Builder
	cursor := 0
	for i, idx := range idxs {
		if i >= n {
			break
		}
		b.WriteString(line[cursor:idx[0]])
		if i < len(replacements) {
			b.WriteString(replacements[i])
		} else {
			b.WriteString(line[idx[0]:idx[1]])
		}
		cursor = idx[1]
	}
	b.WriteString(line[cursor:])
	return b.String()
}

// Serialize 按原始换行风格拼回文本。
func (doc *SubtitleDoc) Serialize() string {
	if doc == nil {
		return ""
	}
	ending := doc.LineEnding
	if ending == "" {
		ending = "\n"
	}
	return strings.Join(doc.RawLines, ending)
}

// WriteSubtitleFile 原子写字幕文件：先写 .part 再 rename。
func WriteSubtitleFile(doc *SubtitleDoc, destPath string) error {
	if doc == nil {
		return errors.New("字幕文档为空")
	}
	tmp := destPath + ".part"
	if err := os.WriteFile(tmp, []byte(doc.Serialize()), 0o644); err != nil {
		return fmt.Errorf("写入字幕文件失败：%w", err)
	}
	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存字幕文件失败：%w", err)
	}
	return nil
}

// SubtitleDuration 返回字幕最后一条的结束时间。
func (doc *SubtitleDoc) SubtitleDuration() int64 {
	if doc == nil {
		return 0
	}
	var maxEnd int64
	for _, t := range doc.Timings {
		if t.End > maxEnd {
			maxEnd = t.End
		}
	}
	return maxEnd
}

// FormatSubtitleTimestamp 把毫秒格式化成可读的 hh:mm:ss.mmm。
func FormatSubtitleTimestamp(ms int64) string {
	h, m, s, milli := splitMs(ms)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, milli)
}

// parseASSTimestamp 解析单个 ASS 时间戳（形如 0:00:01.00）。
func parseASSTimestamp(raw string) (int64, bool) {
	m := assSingleTimePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 5 {
		return 0, false
	}
	return hmsToMs(m[1], m[2], m[3], m[4]), true
}
