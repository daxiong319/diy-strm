package automation

import (
	"context"
	"strings"
	"time"

	"litepan/internal/domain"
)

// 观影报告触发器：到点生成排行图，并把结论推给用户。
//
// 它是**触发器**而不是动作：动作是"这条规则跑到这儿做一步"，
// 而观影报告是"到点了"就发生的事。参数（周期/Top/来源/阈值/gap）
// 都挂在触发配置上，推送本身仍由既有的 notify 动作负责 ——
// 于是"报告要不要发、发到哪"和"什么时候生成"两件事解耦，
// 用户不勾 notify 动作就只生成不推送，不会莫名收到消息。

// PlayReportGenerator 生成一份观影报告。
//
// 由 internal/playmonitor.Service 实现；未装配（监控服务为 nil）时
// 观影报告触发器不可用，规则跑起来会明确报"服务未就绪"，
// 而不是静默成功 —— 静默成功会让用户以为报告发出去了。
type PlayReportGenerator interface {
	// Generate 生成报告。Now 显式传入，调度器用自己的时钟口径。
	Generate(ctx context.Context, v PlayReportRequest) (PlayReportOutcome, error)
}

// PlayReportRequest 报告生成参数。
//
// 与 playmonitor.ReportView 字段一一对应，但**不复用那个类型**：
// internal/playmonitor 要 import internal/domain 与 internal/playback，
// 让 internal/automation 反向依赖会把播放链路拖进自动化包，
// 也会让本包的测试被迫装配整个播放栈。
type PlayReportRequest struct {
	// Days 统计周期天数，只接受 7/14/30；非法值落默认。
	Days int
	// Top 排行条数，只接受 5/10；非法值落默认。
	Top int
	// Source 数据来源：auto/emby/vyo/plex。
	Source string
	// MinSeconds 忽略播放时长低于 N 秒的记录（低于过滤、等于保留）。
	MinSeconds int
	// GapMinutes 中断超过多少分钟算新的一次播放。
	GapMinutes int
	// Now 显式时钟。
	Now time.Time
}

// PlayReportOutcome 一次报告生成的结果。
type PlayReportOutcome struct {
	// Title 通知标题，例如「观影报告 · 近 7 天」。
	Title string
	// Body 通知正文（纯文本；通知渠道当前没有图片消息类型，
	// 排行图只作为站内附件与前端展示，正文里给出同样的排行文字）。
	Body string
	// Lines 排行行，便于测试断言。
	Lines []string
	// Chart PNG 字节；可能为 nil（绘图失败不算报告失败）。
	Chart []byte
}

// SetPlayReportGenerator 注入报告生成器。
//
// 用 setter 而非 Options 字段，与 SetNotifier 同理：播放监控服务在
// wire_services.go 里创建，晚于 automation.New，注入顺序不由构造函数决定。
func (s *Service) SetPlayReportGenerator(g PlayReportGenerator) {
	if s != nil {
		s.playReport = g
	}
}

// runPlayReport 生成并推送观影报告。
//
// 返回值里的 success 只表示**报告是否生成成功**，不表示是否已推送出去
// —— 推送由规则里的 notify 动作决定，这里不该替它表态，
// 否则规则里没配 notify 时，用户会看到一条"已推送"却是自己发的提示。
func (s *Service) runPlayReport(ctx context.Context, cfg map[string]any) map[string]any {
	if s == nil || s.playReport == nil {
		return map[string]any{"status": "failed", "success": false, "message": "播放监控服务未就绪"}
	}
	req := playReportRequestFromConfig(cfg)
	out, err := s.playReport.Generate(ctx, req)
	if err != nil {
		s.log.Warn("生成观影报告失败", "err", err)
		return map[string]any{"status": "failed", "success": false, "message": "观影报告生成失败：" + err.Error()}
	}
	if strings.TrimSpace(out.Title) == "" {
		out.Title = "观影报告"
	}
	message := out.Title + " 已生成"
	if len(out.Lines) == 0 {
		message += "，本期没有播放记录"
	} else {
		message += "，共 " + itoa(len(out.Lines)) + " 位用户"
	}
	return map[string]any{
		"status":  "success",
		"success": true,
		"message": message,
		"data": map[string]any{
			"title": out.Title,
			"body":  out.Body,
			"lines": out.Lines,
			// chart 走 data.chart_bytes 的 base64 由 API 层做；
			// 这里只带一个"有没有图"的布尔，省得把二进制塞进运行记录。
			"has_chart": len(out.Chart) > 0,
		},
	}
}

// playReportRequestFromConfig 从触发配置里读报告参数。
//
// 参数全部走"读不到就落默认"，不报错：观影报告的三个口径
// （周期、条数、阈值）都有合理默认，用户只填了推送时间也能跑。
// 真正必填的只有时间与周期 —— 那些在 normalizeInput 里已经校验过了。
func playReportRequestFromConfig(cfg map[string]any) PlayReportRequest {
	req := PlayReportRequest{
		Days:       anyInt(cfg["days"]),
		Top:        anyInt(cfg["top"]),
		Source:     strings.TrimSpace(anyString(cfg["source"])),
		MinSeconds: anyInt(cfg["min_seconds"]),
		GapMinutes: anyInt(cfg["gap_minutes"]),
		Now:        time.Now(),
	}
	if req.Days <= 0 {
		req.Days = int(domain.PlayReportPeriodDefault)
	}
	if req.Top <= 0 {
		req.Top = domain.PlayReportTopDefault
	}
	if req.MinSeconds < 0 {
		req.MinSeconds = 0
	}
	if req.GapMinutes <= 0 {
		req.GapMinutes = DefaultPlayReportGapMinutes
	}
	if !domain.PlayReportSourceValid(req.Source) {
		req.Source = domain.PlayReportSourceAuto
	}
	return req
}

// DefaultPlayReportGapMinutes 播放次数的「中断超多久算新一次」默认分钟数。
const DefaultPlayReportGapMinutes = 30

// itoa 是极小的整数转字符串，避免为一处 strconv 拖一个 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
