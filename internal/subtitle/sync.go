package subtitle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SyncMode 决定时间轴校正策略。
type SyncMode string

const (
	SyncModeVAD    SyncMode = "vad"
	SyncModeOffset SyncMode = "offset"
	SyncModeScale  SyncMode = "scale"
	SyncModeAuto   SyncMode = "auto"
)

// SyncOptions 控制一次校正。
type SyncOptions struct {
	Mode          SyncMode
	DryRun        bool
	MinConfidence float64
	KeepOriginal  bool
	Timeout       time.Duration
	FpsOverride   float64
}

// SyncResult 是一次校正的结果。
type SyncResult struct {
	Applied     bool     `json:"applied"`
	DryRun      bool     `json:"dry_run"`
	Mode        SyncMode `json:"mode"`
	OffsetMs    int64    `json:"offset_ms"`
	Scale       float64  `json:"scale"`
	Confidence  float64  `json:"confidence"`
	SpeechRatio float64  `json:"speech_ratio"`
	Warnings    []string `json:"warnings,omitempty"`
	Before      []Timing `json:"before,omitempty"`
	After       []Timing `json:"after,omitempty"`
	BackupPath  string   `json:"backup_path,omitempty"`
}

// audioSegment 是音频上的一个区间（毫秒）。
type audioSegment struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// HasFFmpeg 报告系统是否有可用的 ffmpeg。
func HasFFmpeg() (bool, string) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return false, "未找到 ffmpeg，时间轴校正不可用；请安装 ffmpeg 后重试"
	}
	return true, "已找到 ffmpeg：" + path
}

// HasFFprobe 报告系统是否有可用的 ffprobe。
func HasFFprobe() (bool, string) {
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return false, "未找到 ffprobe，时长探测不可用"
	}
	return true, "已找到 ffprobe：" + path
}

// SyncSubtitle 按 opts 校正字幕并（非 dry-run 时）写回文件。
//
// 返回 result 为 nil 表示没有产出结果（例如未找到语音段），此时 err 必非 nil；
// result 非 nil 且 Applied=false 表示算出了结果但不该改文件。
func SyncSubtitle(ctx context.Context, videoPath, subtitlePath string, opts SyncOptions) (*SyncResult, error) {
	if ok, msg := HasFFmpeg(); !ok {
		return nil, errors.New(msg)
	}
	if opts.Mode == "" {
		opts.Mode = SyncModeVAD
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	doc, err := ParseSubtitleFile(subtitlePath)
	if err != nil {
		return nil, err
	}
	if len(doc.Timings) == 0 {
		return nil, errors.New("字幕文件没有可校正的时间轴")
	}

	result := &SyncResult{
		DryRun: opts.DryRun,
		Mode:   opts.Mode,
		Scale:  1,
	}
	result.Before = append([]Timing(nil), doc.Timings...)

	switch opts.Mode {
	case SyncModeScale:
		delta, err := computeScaleOffset(ctx, videoPath, doc, opts)
		if err != nil {
			return nil, err
		}
		applyDelta(doc, delta.scale, delta.offsetMs, result, opts)
	case SyncModeOffset:
		delta, err := computeBestOffset(ctx, videoPath, doc)
		if err != nil {
			return nil, err
		}
		applyDelta(doc, 1, delta.offsetMs, result, opts)
	case SyncModeVAD, SyncModeAuto:
		delta, err := computeVADDelta(ctx, videoPath, doc, opts)
		if err != nil {
			return nil, err
		}
		applyDelta(doc, delta.scale, delta.offsetMs, result, opts)
	default:
		return nil, fmt.Errorf("未知的时间轴校正方式：%s", opts.Mode)
	}

	result.After = append([]Timing(nil), doc.Timings...)

	if result.Confidence < opts.MinConfidence {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("置信度 %.2f 低于阈值 %.2f，已保留原字幕", result.Confidence, opts.MinConfidence))
		return result, nil
	}
	if opts.DryRun {
		result.Warnings = append(result.Warnings, "dry-run：仅计算偏移，未写入文件")
		return result, nil
	}
	if result.OffsetMs == 0 && math.Abs(result.Scale-1) < 1e-9 {
		result.Warnings = append(result.Warnings, "无需调整（偏移为 0）")
		return result, nil
	}

	if opts.KeepOriginal {
		backup := subtitlePath + ".bak"
		if err := copyFile(subtitlePath, backup); err == nil {
			result.BackupPath = backup
		} else {
			result.Warnings = append(result.Warnings, "原字幕备份失败："+err.Error())
		}
	}
	if err := WriteSubtitleFile(doc, subtitlePath); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

// syncDelta 是计算出的时间轴变换参数。
type syncDelta struct {
	scale       float64
	offsetMs    int64
	confidence  float64
	speechRatio float64
}

// applyDelta 写入结果字段；scale 非法时回退 1。
func applyDelta(doc *SubtitleDoc, scale float64, offsetMs int64, result *SyncResult, opts SyncOptions) {
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	ApplyShift(doc, scale, offsetMs)
	result.Scale = scale
	result.OffsetMs = offsetMs
}

// computeVADDelta 用语音活动检测对齐：抽取语音段 → 找最佳偏移 → 估算置信度。
func computeVADDelta(ctx context.Context, videoPath string, doc *SubtitleDoc, opts SyncOptions) (syncDelta, error) {
	speech, err := detectSpeechSegments(ctx, videoPath)
	if err != nil {
		return syncDelta{}, err
	}
	if len(speech) == 0 {
		return syncDelta{}, errors.New("未从音轨中检测到语音段，无法校正时间轴")
	}

	ratio := speechRatio(speech, doc.SubtitleDuration())
	span := int64(0)
	for _, s := range speech {
		if s.End > span {
			span = s.End
		}
	}
	// 搜索窗口：语音总时长的 20%，至少 5 秒，最多 60 秒。
	window := span / 5
	if window < 5000 {
		window = 5000
	}
	if window > 60000 {
		window = 60000
	}

	offset, confidence := bestOffset(doc.Timings, speech, window)
	return syncDelta{
		scale:       1,
		offsetMs:    offset,
		confidence:  confidence,
		speechRatio: ratio,
	}, nil
}

// computeBestOffset 只算固定偏移（不拉伸）。
func computeBestOffset(ctx context.Context, videoPath string, doc *SubtitleDoc) (syncDelta, error) {
	speech, err := detectSpeechSegments(ctx, videoPath)
	if err != nil {
		return syncDelta{}, err
	}
	if len(speech) == 0 {
		return syncDelta{}, errors.New("未从音轨中检测到语音段，无法校正时间轴")
	}
	offset, confidence := bestOffset(doc.Timings, speech, 30000)
	return syncDelta{
		scale:       1,
		offsetMs:    offset,
		confidence:  confidence,
		speechRatio: speechRatio(speech, doc.SubtitleDuration()),
	}, nil
}

// computeScaleOffset 用视频/字幕时长比例估算缩放，再在缩放后微调偏移。
func computeScaleOffset(ctx context.Context, videoPath string, doc *SubtitleDoc, opts SyncOptions) (syncDelta, error) {
	subtitleDuration := doc.SubtitleDuration()
	if subtitleDuration <= 0 {
		return syncDelta{}, errors.New("字幕时长为 0，无法计算缩放比例")
	}

	videoDuration, err := ProbeDurationMs(ctx, videoPath)
	if err != nil {
		return syncDelta{}, err
	}
	if videoDuration <= 0 {
		return syncDelta{}, errors.New("视频时长为 0，无法计算缩放比例")
	}

	scale := float64(videoDuration) / float64(subtitleDuration)
	// 离谱的比例（超出 ±10%）说明字幕本来就不是这条视频的，别硬拉。
	if scale < 0.9 || scale > 1.1 {
		return syncDelta{}, fmt.Errorf("视频与字幕时长比例异常（%.4f），不做缩放校正", scale)
	}

	speech, err := detectSpeechSegments(ctx, videoPath)
	if err != nil {
		return syncDelta{}, err
	}
	if len(speech) == 0 {
		return syncDelta{}, errors.New("未从音轨中检测到语音段，无法校正时间轴")
	}

	scaled := make([]Timing, len(doc.Timings))
	for i, t := range doc.Timings {
		scaled[i] = Timing{
			Start: int64(float64(t.Start) * scale),
			End:   int64(float64(t.End) * scale),
		}
	}
	offset, confidence := bestOffset(scaled, speech, 30000)
	return syncDelta{
		scale:       scale,
		offsetMs:    offset,
		confidence:  confidence,
		speechRatio: speechRatio(speech, videoDuration),
	}, nil
}

// detectSpeechSegments 用 ffmpeg silencedetect 反推语音段。
func detectSpeechSegments(ctx context.Context, videoPath string) ([]audioSegment, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats",
		"-i", videoPath,
		"-af", "silencedetect=noise=-30dB:d=0.6",
		"-f", "null", "-",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("音轨分析超时或被取消")
		}
		return nil, fmt.Errorf("分析音轨失败：%w", err)
	}
	return parseSilenceOutput(string(out)), nil
}

var (
	silenceStartPattern    = regexp.MustCompile(`silence_start:\s*(-?\d+(?:\.\d+)?)`)
	silenceEndPattern      = regexp.MustCompile(`silence_end:\s*(-?\d+(?:\.\d+)?)`)
	silenceDurationPattern = regexp.MustCompile(`silence_duration:\s*(-?\d+(?:\.\d+)?)`)
)

// parseSilenceOutput 把 silencedetect 的输出转成语音段。
// 静音段之间即为语音；只有 silence_start 而没有配对 silence_end 的静音段
// （分析被截断/超时）视为"到结尾都静音"，因此它能产出它之前的语音段。
func parseSilenceOutput(output string) []audioSegment {
	var silences []audioSegment
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		m := silenceStartPattern.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		startMs := secondsToMs(m[1])
		endMs := int64(-1)

		// silence_end 可能在同一行，也可能在后续行（ffmpeg 输出格式不稳定）。
		if em := silenceEndPattern.FindStringSubmatch(line); len(em) == 2 {
			endMs = secondsToMs(em[1])
		} else {
			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				if em := silenceEndPattern.FindStringSubmatch(lines[j]); len(em) == 2 {
					endMs = secondsToMs(em[1])
					break
				}
				if dm := silenceDurationPattern.FindStringSubmatch(lines[j]); len(dm) == 2 {
					endMs = startMs + secondsToMs(dm[1])
					break
				}
			}
		}
		if endMs < 0 {
			// 未闭合：把整条音轨的剩余部分都当成静音。
			silences = append(silences, audioSegment{Start: startMs, End: math.MaxInt64 / 4})
			continue
		}
		silences = append(silences, audioSegment{Start: startMs, End: endMs})
	}
	if len(silences) == 0 {
		return nil
	}
	sort.Slice(silences, func(i, j int) bool { return silences[i].Start < silences[j].Start })

	var speech []audioSegment
	cursor := int64(0)
	for _, s := range silences {
		if s.Start > cursor {
			speech = append(speech, audioSegment{Start: cursor, End: s.Start})
		}
		if s.End > cursor {
			cursor = s.End
		}
	}
	// 未闭合的静音段（没有 silence_end）：到末尾都算静音，因此不追加尾段。
	return speech
}

func secondsToMs(raw string) int64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(math.Round(v * 1000))
}

// ProbeDurationMs 用 ffprobe 探测媒体时长。
func ProbeDurationMs(ctx context.Context, path string) (int64, error) {
	if ok, msg := HasFFprobe(); !ok {
		return 0, errors.New(msg)
	}
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("探测媒体时长失败：%w", err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("解析媒体时长失败：%w", err)
	}
	return int64(math.Round(seconds * 1000)), nil
}

// mergeSegments 合并重叠或间隔很小的区间。
func mergeSegments(segments []audioSegment) []audioSegment {
	if len(segments) == 0 {
		return nil
	}
	sorted := append([]audioSegment(nil), segments...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	out := []audioSegment{sorted[0]}
	for _, seg := range sorted[1:] {
		last := &out[len(out)-1]
		// 间隔 <=200ms 视为同一段（检测抖动）。
		if seg.Start <= last.End+200 {
			if seg.End > last.End {
				last.End = seg.End
			}
			continue
		}
		out = append(out, seg)
	}
	return out
}

// overlapWithSegments 返回 [start,end) 与各语音段的重叠总时长。
func overlapWithSegments(segments []audioSegment, start, end int64) int64 {
	if end <= start {
		return 0
	}
	var total int64
	for _, seg := range segments {
		lo := start
		if seg.Start > lo {
			lo = seg.Start
		}
		hi := end
		if seg.End < hi {
			hi = seg.End
		}
		if hi > lo {
			total += hi - lo
		}
	}
	return total
}

// scoreOffset 给某个偏移量打分：字幕落在语音上的比例。
func scoreOffset(timings []Timing, speech []audioSegment, offsetMs int64) float64 {
	if len(timings) == 0 || len(speech) == 0 {
		return 0
	}
	merged := mergeSegments(speech)
	var covered, total int64
	for _, t := range timings {
		start := t.Start + offsetMs
		end := t.End + offsetMs
		if end <= start {
			continue
		}
		if start < 0 {
			start = 0
		}
		span := end - start
		if span <= 0 {
			continue
		}
		total += span
		covered += overlapWithSegments(merged, start, end)
	}
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total)
}

// bestOffset 在 [-window, +window] 内以 100ms 步长找最佳偏移。
// 返回 (偏移毫秒, 置信度)。输入为空时返回 (0,0)。
func bestOffset(timings []Timing, speech []audioSegment, windowMs int64) (int64, float64) {
	if len(timings) == 0 || len(speech) == 0 {
		return 0, 0
	}
	if windowMs <= 0 {
		windowMs = 5000
	}
	const step = 100

	bestScore := -1.0
	best := int64(0)
	for offset := -windowMs; offset <= windowMs; offset += step {
		s := scoreOffset(timings, speech, offset)
		if s > bestScore {
			bestScore = s
			best = offset
		}
	}
	if bestScore <= 0 {
		return 0, 0
	}

	// 置信度：把最佳得分与"零偏移"得分拉开距离，避免字幕本来就对齐时虚报高置信度。
	baseline := scoreOffset(timings, speech, 0)
	confidence := bestScore
	if best != 0 {
		confidence = bestScore - baseline*0.5
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return best, confidence
}

// speechRatio 返回语音段占给定总时长的比例。
func speechRatio(speech []audioSegment, totalMs int64) float64 {
	if len(speech) == 0 || totalMs <= 0 {
		return 0
	}
	merged := mergeSegments(speech)
	var sum int64
	for _, seg := range merged {
		if seg.End > seg.Start {
			sum += seg.End - seg.Start
		}
	}
	ratio := float64(sum) / float64(totalMs)
	if ratio > 1 {
		return 1
	}
	return ratio
}
