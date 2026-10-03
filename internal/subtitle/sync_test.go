package subtitle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func indexOf(s, sub string) int { return strings.Index(s, sub) }

func TestMergeSegments(t *testing.T) {
	if got := mergeSegments(nil); got != nil {
		t.Errorf("nil 输入应返回 nil，得到 %+v", got)
	}

	got := mergeSegments([]audioSegment{{5000, 6000}, {1000, 2000}})
	if len(got) != 2 || got[0].Start != 1000 || got[1].Start != 5000 {
		t.Errorf("乱序输入应被排序，得到 %+v", got)
	}

	got = mergeSegments([]audioSegment{{1000, 3000}, {2000, 4000}})
	if len(got) != 1 || got[0] != (audioSegment{1000, 4000}) {
		t.Errorf("重叠区间应合并为 {1000,4000}，得到 %+v", got)
	}

	got = mergeSegments([]audioSegment{{1000, 2000}, {2100, 3000}})
	if len(got) != 1 || got[0] != (audioSegment{1000, 3000}) {
		t.Errorf("100ms 间隔应合并，得到 %+v", got)
	}

	got = mergeSegments([]audioSegment{{1000, 2000}, {2500, 3000}})
	if len(got) != 2 {
		t.Errorf("500ms 间隔应保持两段，得到 %+v", got)
	}
}

func TestOverlapWithSegments(t *testing.T) {
	segments := []audioSegment{{1000, 2000}, {3000, 4000}}
	cases := []struct {
		start, end int64
		want       int64
	}{
		{1200, 1800, 600},
		{2200, 2800, 0},
		{1500, 3500, 1000},
		{500, 1500, 500},
		{1500, 1500, 0},
	}
	for _, c := range cases {
		if got := overlapWithSegments(segments, c.start, c.end); got != c.want {
			t.Errorf("overlapWithSegments(%d,%d) = %d，期望 %d", c.start, c.end, got, c.want)
		}
	}
}

func TestScoreOffset(t *testing.T) {
	timings := []Timing{{1000, 3000}, {5000, 7000}}
	speech := []audioSegment{{1000, 3000}, {5000, 7000}}

	aligned := scoreOffset(timings, speech, 0)
	shifted := scoreOffset(timings, speech, 1000)

	if diff := aligned - scoreOffset(timings, speech, 1); diff > 0.01 || diff < -0.01 {
		t.Errorf("偏移 0 与 1 的得分差 = %v，应小于 0.01", diff)
	}
	if shifted >= 0.6 {
		t.Errorf("偏移 1000 的得分 = %v，应小于 0.6（评分函数需要区分度）", shifted)
	}
}

func TestBestOffsetFindsKnownShift(t *testing.T) {
	speech := make([]audioSegment, 20)
	timings := make([]Timing, 20)
	for i := 0; i < 20; i++ {
		start := int64(i*10000 + 1000)
		speech[i] = audioSegment{Start: start, End: start + 3000}
		timings[i] = Timing{Start: start + 4000, End: start + 4000 + 3000}
	}

	offset, confidence := bestOffset(timings, speech, 10000)
	if offset != -4000 {
		t.Errorf("偏移 = %d，期望 -4000", offset)
	}
	if confidence <= 0 {
		t.Errorf("置信度 = %v，应大于 0", confidence)
	}
}

func TestBestOffsetEmptyInput(t *testing.T) {
	if offset, confidence := bestOffset(nil, []audioSegment{{1000, 2000}}, 5000); offset != 0 || confidence != 0 {
		t.Errorf("timings 为空时应返回 (0,0)，得到 (%d,%v)", offset, confidence)
	}
	if offset, confidence := bestOffset([]Timing{{1000, 2000}}, nil, 5000); offset != 0 || confidence != 0 {
		t.Errorf("speech 为空时应返回 (0,0)，得到 (%d,%v)", offset, confidence)
	}
}

func TestSpeechRatio(t *testing.T) {
	if got := speechRatio(nil, 8000); got != 0 {
		t.Errorf("nil 输入 = %v，期望 0", got)
	}
	got := speechRatio([]audioSegment{{0, 2000}, {4000, 8000}}, 8000)
	if got < 0.7 || got > 0.8 {
		t.Errorf("语音占比 = %v，期望约 0.75（6000/8000）", got)
	}
}

func TestParseSilenceOutput(t *testing.T) {
	output := `[silencedetect @ 0x55] silence_start: 1.5
[silencedetect @ 0x55] silence_end: 3.25 | silence_duration: 1.75
[silencedetect @ 0x55] silence_start: 10
[silencedetect @ 0x55] silence_end: 12.5 | silence_duration: 2.5
`
	speech := parseSilenceOutput(output)
	if len(speech) == 0 {
		t.Fatal("应解析出语音段")
	}
	if speech[0].Start != 0 {
		t.Errorf("首个语音段起点 = %d，期望 0", speech[0].Start)
	}
	for _, s := range speech {
		if overlapWithSegments([]audioSegment{s}, 1500, 3250) > 0 {
			t.Errorf("语音段 %+v 与静音区间 1.5s-3.25s 重叠", s)
		}
	}
}

func TestParseSilenceOutputNoSilence(t *testing.T) {
	if got := parseSilenceOutput("some unrelated ffmpeg output\n"); len(got) != 0 {
		t.Errorf("无关输出应解析出 0 段，得到 %+v", got)
	}
}

func TestParseSilenceOutputUnterminated(t *testing.T) {
	output := "[silencedetect @ 0x55] silence_start: 5.0\n"
	got := parseSilenceOutput(output)
	if len(got) == 0 {
		t.Fatal("未闭合的静音段应仍能产出前置语音段")
	}
	if got[0] != (audioSegment{0, 5000}) {
		t.Errorf("首个语音段 = %+v，期望 {0,5000}", got[0])
	}
}

func TestHasFFmpeg(t *testing.T) {
	_, err := exec.LookPath("ffmpeg")
	available, msg := HasFFmpeg()
	if err != nil {
		if available {
			t.Error("ffmpeg 缺失时 available 必须为 false")
		}
		if msg == "" {
			t.Error("ffmpeg 缺失时提示信息不能为空")
		}
		return
	}
	if !available {
		t.Errorf("ffmpeg 存在时 available 必须为 true，提示：%s", msg)
	}
}

func TestProbeDurationMsMissingFile(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("未安装 ffprobe，跳过")
	}
	if _, err := ProbeDurationMs(t.Context(), filepath.Join(t.TempDir(), "nope.mkv")); err == nil {
		t.Error("探测不存在的文件必须返回错误")
	}
}

func TestSyncSubtitleFFmpegMissing(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		t.Skip("已安装 ffmpeg，跳过缺失分支")
	}
	_, err := SyncSubtitle(t.Context(), "video.mkv", "sub.srt", SyncOptions{})
	if err == nil {
		t.Fatal("ffmpeg 缺失时必须返回错误")
	}
	if !contains(strings.ToLower(err.Error()), "ffmpeg") {
		t.Errorf("错误信息应包含 ffmpeg，得到：%v", err)
	}
}

func TestSyncSubtitleDryRunDoesNotWrite(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("未安装 ffmpeg，跳过")
	}

	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	gen := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-af", "volume=enable='between(t,5,8)':volume=0",
		"-c:a", "aac", video,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("生成测试视频失败，跳过：%v\n%s", err, out)
	}

	subPath := filepath.Join(dir, "clip.srt")
	if err := os.WriteFile(subPath, []byte(sampleSRT), 0o644); err != nil {
		t.Fatalf("写字幕失败：%v", err)
	}
	before, err := os.ReadFile(subPath)
	if err != nil {
		t.Fatalf("读字幕失败：%v", err)
	}

	result, err := SyncSubtitle(t.Context(), video, subPath, SyncOptions{
		Mode:    SyncModeVAD,
		DryRun:  true,
		Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("dry-run 校正失败：%v", err)
	}
	if result == nil {
		t.Fatal("dry-run 应返回结果")
	}
	if result.Applied {
		t.Error("dry-run 不得写入文件，Applied 必须为 false")
	}
	after, err := os.ReadFile(subPath)
	if err != nil {
		t.Fatalf("读回字幕失败：%v", err)
	}
	if string(before) != string(after) {
		t.Error("dry-run 后字幕文件内容发生了变化")
	}
	if len(result.Warnings) == 0 {
		t.Error("dry-run 应至少产出一条警告说明未写回")
	}
}
