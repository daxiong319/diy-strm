package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
	"litepan/internal/playmonitor"
)

// 播放监控面板（计费中 / CDN 直连 / 局域网 三态）与观影报告。
//
// 全部挂在 /api/admin 子树内（见 router.go）—— T10 的 library-share
// 就是在 /admin 之外挂的，前端按 /admin/ 写导致五个操作全 404。
// 这里不重复再挂一份 /play-monitor 免得同一功能两套路径。

// playMonitorSessions 实时播放会话列表 + 顶部流量概览。
// GET /api/admin/play-monitor/sessions
//
// 前端每 2 秒轮询本接口（会话列表要跟得上起停播）；
// traffic 概览搭在同一个响应里，是为了让「今日/本月/累计 + 在播数」
// 跟会话来自同一时刻 —— 分成两个接口会出现
// 「在播 3 个，但顶部写着 2 个」这种自相矛盾的画面。
func (h *Handler) playMonitorSessions(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.playMonitor != nil) {
		return
	}
	q := domain.PlaySessionQuery{
		State: domain.PlayState(strings.TrimSpace(r.URL.Query().Get("state"))),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("user_id")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			q.AppUserID = id
		}
	}
	res, err := h.playMonitor.List(r.Context(), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"items":   res.Sessions,
		"summary": res.Summary,
	})
}

// playMonitorTraffic 流量汇总（今日 / 本月 / 累计 / 按用户排行）。
// GET /api/admin/play-monitor/traffic
//
// 单独一个端点是有意的：这个页面上的数字是"报表"，用户会盯着它核对，
// 不该跟着 2 秒一次的会话轮询一起抖动。前端按 10 秒轮询。
func (h *Handler) playMonitorTraffic(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.playTraffic != nil) {
		return
	}
	ctx := r.Context()
	todayBytes, monthBytes, totalBytes, err := trafficSummary(ctx, h.playTraffic)
	if err != nil {
		writeErr(w, err)
		return
	}
	rank, err := h.playTraffic.RankByUser(ctx, time.Now().Format("2006-01"), 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	enabledSince, found, err := h.playTraffic.EnabledSince(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"today":         todayBytes,
		"month":         monthBytes,
		"total":         totalBytes,
		"month_rank":    rank,
		"enabled_since": enabledSince,
		"has_enabled":   found,
	})
}

// playTrafficClear 清空流量统计。
// POST /api/admin/play-monitor/traffic/clear
//
// ⚠️ 不可撤销：清掉之后所有历史累计与排行都回不来。
// 前端必须二次确认，后端这里不再加确认参数 ——
// 真要在服务端加一道，也拦不住直接调 API 的人。
//
// 注意 Clear **不删「首次启用时刻」标记**：清的是统计数字，
// 不是"从什么时候开始算"这个口径起点。删了它，报告就会把
// 清空之前的历史也算回来，等于清空操作替用户决定了口径。
func (h *Handler) playMonitorTrafficClear(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.playTraffic != nil) {
		return
	}
	n, err := h.playTraffic.Clear(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"cleared": n})
}

// playReport 生成观影报告（默认只回文字与排行，chart=1 时附带排行图）。
// GET /api/admin/play-monitor/report?period=&top=&source=&min_seconds=&gap_minutes=&chart=1
//
// 口径（从启用起累计不补算 / 次数怎么算 / 阈值过滤）由服务端
// 放进响应的 notices 字段，前端原样渲染 —— 不让前端自己复述一遍。
func (h *Handler) playReport(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.playMonitor != nil) {
		return
	}
	q := r.URL.Query()
	view := playmonitor.ReportView{
		Period:     domain.PlayReportPeriod(queryInt(r, "period", int(domain.PlayReportPeriodDefault))),
		Top:        queryInt(r, "top", domain.PlayReportTopDefault),
		Source:     strings.TrimSpace(q.Get("source")),
		MinSeconds: queryInt(r, "min_seconds", 0),
		GapMinutes: queryInt(r, "gap_minutes", playmonitor.DefaultReportGapMinutes),
	}
	res, chart, err := h.playMonitor.Report(r.Context(), view)
	if err != nil {
		writeErr(w, err)
		return
	}
	payload := map[string]any{"report": res}
	// 排行图用 base64 内联，而不是再开一个图片端点：
	// 报告是一次性快照，图和数据必须来自同一次汇总，
	// 分成两个请求会出现"数据是今天的、图是三小时前的"。
	if q.Get("chart") == "1" && len(chart) > 0 {
		payload["chart_png"] = dataURI(chart)
	}
	writeOK(w, payload)
}

// playReportPreview 出图端点：直接返回 PNG 字节。
// GET /api/admin/play-monitor/report/chart?...
//
// 与 playReport?chart=1 的 base64 各留一条路：
// 前端 <img src> 直接吃这个端点（浏览器自动带会话），
// 手动下载/分享走 base64 那条。
func (h *Handler) playReportChart(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.playMonitor != nil) {
		return
	}
	q := r.URL.Query()
	view := playmonitor.ReportView{
		Period:     domain.PlayReportPeriod(queryInt(r, "period", int(domain.PlayReportPeriodDefault))),
		Top:        queryInt(r, "top", domain.PlayReportTopDefault),
		Source:     strings.TrimSpace(q.Get("source")),
		MinSeconds: queryInt(r, "min_seconds", 0),
		GapMinutes: queryInt(r, "gap_minutes", playmonitor.DefaultReportGapMinutes),
	}
	_, chart, err := h.playMonitor.Report(r.Context(), view)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(chart) == 0 {
		writeErr(w, domain.Errorf(domain.CodeNotFound, "报告图生成失败"))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(chart)
}

// playMonitorOptions 三态/周期/来源的可选值，供前端渲染下拉。
// GET /api/admin/play-monitor/options
func (h *Handler) playMonitorOptions(w http.ResponseWriter, r *http.Request) {
	periods := make([]map[string]any, 0, 3)
	for _, p := range domain.PlayReportPeriods() {
		periods = append(periods, map[string]any{"value": int(p), "label": strconv.Itoa(int(p)) + " 天"})
	}
	writeOK(w, map[string]any{
		"states": []map[string]string{
			{"value": string(domain.PlayStateMetered), "label": domain.PlayStateMetered.DisplayText()},
			{"value": string(domain.PlayStateCDN), "label": domain.PlayStateCDN.DisplayText()},
			{"value": string(domain.PlayStateLAN), "label": domain.PlayStateLAN.DisplayText()},
		},
		"periods": periods,
		"sources": []map[string]string{
			{"value": domain.PlayReportSourceAuto, "label": "自动"},
			{"value": domain.PlayReportSourceEmby, "label": "Emby"},
			{"value": domain.PlayReportSourceVyo, "label": "Vyo"},
		},
		"tops": []map[string]any{{"value": 5}, {"value": 10}},
		"defaults": map[string]any{
			"period":      int(domain.PlayReportPeriodDefault),
			"top":         domain.PlayReportTopDefault,
			"source":      domain.PlayReportSourceAuto,
			"min_seconds": 0,
			"gap_minutes": playmonitor.DefaultReportGapMinutes,
			"sample_secs": playmonitor.DefaultSampleSeconds,
			"idle_secs":   playmonitor.DefaultIdleSeconds,
		},
	})
}

// trafficSummary 取"今日/本月/累计"三档合计。
func trafficSummary(ctx context.Context, repo domain.PlayTrafficRepository) (int64, int64, int64, error) {
	now := time.Now()
	return repo.TodayMonthTotal(ctx, now.Format("2006-01-02"), now.Format("2006-01"))
}

// dataURI 把 PNG 字节包成 data URI（前端直接 <img src> 用）。
func dataURI(png []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}
