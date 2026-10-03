package subtitle

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSRT = "1\r\n00:00:01,000 --> 00:00:03,500\r\n第一条字幕\r\n\r\n" +
	"2\r\n00:00:10,250 --> 00:00:12,000\r\n第二条字幕\r\n换行内容\r\n\r\n" +
	"3\r\n01:02:03,000 --> 01:02:05,000\r\n第三条字幕\r\n"

const sampleASS = `[Script Info]
ScriptType: v4.00+
[V4+ Styles]
Format: Name, Fontname
Style: Default,Arial
[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\pos(10,10)}第一条
Dialogue: 0,0:00:10.25,0:00:12.00,Default,,0,0,0,,第二条
Comment: 0,0:00:20.00,0:00:22.00,Default,,0,0,0,,这是注释不是字幕
`

const sampleVTT = "WEBVTT\n\n00:00:01.000 --> 00:00:03.500\nHello\n\n00:00:10.250 --> 00:00:12.000\nWorld\n"

func TestParseSubtitleContentSRT(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleSRT, FormatSRT)
	if err != nil {
		t.Fatalf("解析 SRT 失败：%v", err)
	}
	if doc.Format != FormatSRT {
		t.Fatalf("格式 = %q，期望 %q", doc.Format, FormatSRT)
	}
	want := []Timing{{1000, 3500}, {10250, 12000}, {3723000, 3725000}}
	if len(doc.Timings) != len(want) {
		t.Fatalf("时间轴条数 = %d，期望 %d", len(doc.Timings), len(want))
	}
	for i, w := range want {
		if doc.Timings[i] != w {
			t.Errorf("第 %d 条 = %+v，期望 %+v", i, doc.Timings[i], w)
		}
	}
}

func TestApplyShiftSRTPreservesText(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleSRT, FormatSRT)
	if err != nil {
		t.Fatalf("解析 SRT 失败：%v", err)
	}
	ApplyShift(doc, 1, 2000)

	if got, want := doc.Timings[0], (Timing{3000, 5500}); got != want {
		t.Fatalf("偏移后第 1 条 = %+v，期望 %+v", got, want)
	}
	out := doc.Serialize()
	for _, must := range []string{
		"1\r\n",
		"第一条字幕",
		"第二条字幕",
		"第三条字幕",
		"换行内容",
		"00:00:03,000 --> 00:00:05,500",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("序列化结果缺少 %q\n---\n%s", must, out)
		}
	}
}

func TestApplyShiftScale(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleSRT, FormatSRT)
	if err != nil {
		t.Fatalf("解析 SRT 失败：%v", err)
	}
	const scale = 23.976 / 25.0
	ApplyShift(doc, scale, 0)

	wantStart := int64(math.Round(1000 * 23.976 / 25.0))
	if doc.Timings[0].Start != wantStart {
		t.Errorf("Start = %d，期望 %d", doc.Timings[0].Start, wantStart)
	}
	if doc.Timings[0].End <= doc.Timings[0].Start {
		t.Errorf("End(%d) 应大于 Start(%d)", doc.Timings[0].End, doc.Timings[0].Start)
	}
}

func TestApplyShiftKeepsEndAfterStart(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleSRT, FormatSRT)
	if err != nil {
		t.Fatalf("解析 SRT 失败：%v", err)
	}
	ApplyShift(doc, 1e-6, 0)
	for i, timing := range doc.Timings {
		if timing.End <= timing.Start {
			t.Errorf("第 %d 条被压成零长度：%+v", i, timing)
		}
	}
}

func TestParseSubtitleContentASS(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleASS, FormatASS)
	if err != nil {
		t.Fatalf("解析 ASS 失败：%v", err)
	}
	if len(doc.Timings) != 2 {
		t.Fatalf("时间轴条数 = %d，期望 2（Comment 行不算字幕）", len(doc.Timings))
	}
	if got, want := doc.Timings[0], (Timing{1000, 3500}); got != want {
		t.Errorf("第 1 条 = %+v，期望 %+v", got, want)
	}
	if !strings.Contains(doc.Serialize(), "[V4+ Styles]") {
		t.Error("序列化结果应保留样式段")
	}
}

func TestParseSubtitleContentVTT(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleVTT, FormatVTT)
	if err != nil {
		t.Fatalf("解析 VTT 失败：%v", err)
	}
	if len(doc.Timings) != 2 {
		t.Fatalf("时间轴条数 = %d，期望 2", len(doc.Timings))
	}
	if got, want := doc.Timings[1], (Timing{10250, 12000}); got != want {
		t.Errorf("第 2 条 = %+v，期望 %+v", got, want)
	}
}

func TestSniffSubtitleFormat(t *testing.T) {
	cases := []struct {
		content string
		want    SubtitleFormat
	}{
		{sampleSRT, FormatSRT},
		{sampleASS, FormatASS},
		{sampleVTT, FormatVTT},
	}
	for i, c := range cases {
		if got := sniffSubtitleFormat(c.content); got != c.want {
			t.Errorf("第 %d 例嗅探 = %q，期望 %q", i, got, c.want)
		}
	}
}

func TestHmsToMs(t *testing.T) {
	cases := []struct {
		h, m, s, frac string
		want          int64
	}{
		{"00", "00", "01", "500", 1500},
		{"0", "00", "01", "50", 1500},
		{"0", "00", "01", "500", 1500},
		{"0", "00", "01", "5", 1500},
		{"01", "02", "03", "000", 3723000},
		{"25", "00", "00", "000", 90000000},
	}
	for _, c := range cases {
		if got := hmsToMs(c.h, c.m, c.s, c.frac); got != c.want {
			t.Errorf("hmsToMs(%q,%q,%q,%q) = %d，期望 %d", c.h, c.m, c.s, c.frac, got, c.want)
		}
	}
}

func TestMsToSRTTimeRoundTrip(t *testing.T) {
	values := []int64{0, 1, 999, 1000, 59999, 3600000, 3723000, 90000000}
	for _, ms := range values {
		line := msToSRTTime(ms) + " --> " + msToSRTTime(ms+1000)
		m := srtTimePattern.FindStringSubmatch(line)
		if len(m) != 5 {
			t.Fatalf("ms=%d 生成的 %q 未被时间正则命中", ms, line)
		}
		if got := hmsToMs(m[1], m[2], m[3], m[4]); got != ms {
			t.Errorf("ms=%d 往返后 = %d", ms, got)
		}
	}
}

func TestParseSubtitleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(path, []byte(sampleSRT), 0o644); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	doc, err := ParseSubtitleFile(path)
	if err != nil {
		t.Fatalf("解析文件失败：%v", err)
	}
	if doc.Format != FormatSRT {
		t.Errorf("格式 = %q，期望 %q", doc.Format, FormatSRT)
	}
	if len(doc.Timings) != 3 {
		t.Errorf("时间轴条数 = %d，期望 3", len(doc.Timings))
	}
}

func TestWriteSubtitleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(path, []byte(sampleSRT), 0o644); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	doc, err := ParseSubtitleFile(path)
	if err != nil {
		t.Fatalf("解析文件失败：%v", err)
	}
	ApplyShift(doc, 1, 1000)
	if err := WriteSubtitleFile(doc, path); err != nil {
		t.Fatalf("写字幕文件失败：%v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回文件失败：%v", err)
	}
	if !strings.Contains(string(raw), "00:00:02,000") {
		t.Errorf("写回内容缺少偏移后的时间\n%s", raw)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("临时文件 .part 应已被清理")
	}
}

func TestSubtitleDuration(t *testing.T) {
	doc, err := ParseSubtitleContent(sampleSRT, FormatSRT)
	if err != nil {
		t.Fatalf("解析 SRT 失败：%v", err)
	}
	if got := doc.SubtitleDuration(); got != 3725000 {
		t.Errorf("时长 = %d，期望 3725000", got)
	}
}

func TestSubtitleFileNameForVideo(t *testing.T) {
	video := "/media/Movie.2020/Movie.2020.1080p.mkv"
	if got, want := SubtitleFileNameForVideo(video, FormatASS, "same"), "/media/Movie.2020/Movie.2020.1080p.ass"; got != want {
		t.Errorf("same 策略 = %q，期望 %q", got, want)
	}
	if got, want := SubtitleFileNameForVideo(video, FormatSRT, "subtitle"), "/media/Movie.2020/subtitle/Movie.2020.1080p.srt"; got != want {
		t.Errorf("subtitle 策略 = %q，期望 %q", got, want)
	}
}
