package app

import (
	"context"
	"fmt"
	"strings"

	"litepan/internal/automation"
	"litepan/internal/domain"
	"litepan/internal/playmonitor"
)

// playReportAdapter 把播放监控的观影报告接到自动化触发器上。
//
// 放在 internal/app（装配层）而不是让 automation 直接依赖 playmonitor：
// 播放监控要 import playback（取流链路），反向依赖会把整条播放栈
// 拖进自动化包，也会让 automation 的单测被迫装配播放栈。
// 与 embyIndexRefreshSink 是同一个写法。
type playReportAdapter struct {
	monitor *playmonitor.Service
}

// Generate 实现 automation.PlayReportGenerator。
func (a playReportAdapter) Generate(ctx context.Context, v automation.PlayReportRequest) (automation.PlayReportOutcome, error) {
	if a.monitor == nil {
		return automation.PlayReportOutcome{}, fmt.Errorf("播放监控服务未就绪")
	}
	view := playmonitor.ReportView{
		Period:     domain.PlayReportPeriod(v.Days),
		Top:        v.Top,
		Source:     v.Source,
		MinSeconds: v.MinSeconds,
		GapMinutes: v.GapMinutes,
		Now:        v.Now,
	}
	res, chart, err := a.monitor.Report(ctx, view)
	if err != nil {
		return automation.PlayReportOutcome{}, err
	}
	out := automation.PlayReportOutcome{
		Title: fmt.Sprintf("观影报告 · 近 %d 天", res.Period),
		Body:  playReportBody(res),
		Chart: chart,
	}
	for i, u := range res.Users {
		out.Lines = append(out.Lines, playReportLine(i+1, u))
	}
	return out, nil
}

// playReportBody 生成推送正文。
//
// ⚠️ 正文里必须带上统计口径，否则用户收到一张排行图，
// 只能看到"谁看得最多"，看不到"中断超半小时算一次"这类前提，
// 数字一旦跟他自己的印象对不上，第一反应就是 bug。
func playReportBody(res domain.PlayReportResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("统计区间：%s ~ %s\n", res.Since, res.Until))
	b.WriteString(fmt.Sprintf("合计：播放 %d 次、观看 %s、上行估算 %s\n",
		res.TotalCount, humanDuration(res.TotalWatchedSeconds), humanBytes(res.TotalUploadedBytes)))
	if len(res.Users) == 0 {
		b.WriteString("本期没有播放记录。\n")
	}
	for i, u := range res.Users {
		b.WriteString(playReportLine(i+1, u) + "\n")
	}
	if len(res.Notices) > 0 {
		b.WriteString("\n统计口径：\n")
		for _, n := range res.Notices {
			b.WriteString("· " + n + "\n")
		}
	}
	return b.String()
}

// playReportLine 一行排行。
func playReportLine(rank int, u domain.PlayReport) string {
	name := strings.TrimSpace(u.UserName)
	if name == "" {
		name = "未知用户"
	}
	return fmt.Sprintf("%d. %s · 播放 %d 次 · 观看 %s · 上行估算 %s",
		rank, name, u.Count, humanDuration(u.WatchedSeconds), humanBytes(u.UploadedBytes))
}

// humanDuration 秒 → "1h30m"/"12m"/"40s"。
func humanDuration(secs int) string {
	if secs <= 0 {
		return "0s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// humanBytes 字节 → "1.2 GB"。
//
// 播放监控的数字是**估算值**（码率×时长），却要和真实流量同台比较，
// 所以统一走 1024 进制；文档里出现的 GB 说法也按 1024 折算。
func humanBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024.0
	v := float64(n)
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	idx := 0
	for v >= unit && idx < len(units)-1 {
		v /= unit
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", int64(v), units[idx])
	}
	return fmt.Sprintf("%.1f %s", v, units[idx])
}
