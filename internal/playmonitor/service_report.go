package playmonitor

import (
	"context"
	"sort"
	"time"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// 报告生成：读记录 → 汇总 → 出图
//
// 汇总本体是纯函数 BuildReport（见 report.go）。这一层只负责：
//   1. 归一化参数（周期/top/阈值/gap 的非法值落回默认）；
//   2. 取全区间记录（不能分页，见 domain.PlayReportRepository 的注释）；
//   3. 把「插件启用时刻」取出来交给 BuildReport —— 「不补算」就压在这一步；
//   4. 自绘排行图 PNG。
// ─────────────────────────────────────────────────────────────────────────────

// ReportView 一次报告请求的入口参数（API → Service）。
type ReportView struct {
	Period     domain.PlayReportPeriod
	Top        int
	Source     string
	MinSeconds int
	GapMinutes int
	// Now 显式传入，测试可固定时钟；生产传 time.Now()。
	Now time.Time
}

// normalize 归一化请求参数：非法值落回默认值，绝不把非法值透传给汇总。
//
// 归一化放在这一层而不是 BuildReport 里，是为了让 API 回显的
// Period/MinSeconds/GapMinutes 与真正生效的值一致 ——
// 界面上写"阈值 0 秒"而实际按 0 过滤，看起来一样，含义却不同。
func (v ReportView) normalize() ReportView {
	if !v.Period.Valid() {
		v.Period = domain.PlayReportPeriodDefault
	}
	switch v.Top {
	case 5, 10:
	default:
		v.Top = domain.PlayReportTopDefault
	}
	if !domain.PlayReportSourceValid(v.Source) {
		v.Source = domain.PlayReportSourceAuto
	}
	if v.MinSeconds < 0 {
		v.MinSeconds = 0
	}
	if v.GapMinutes <= 0 {
		v.GapMinutes = DefaultReportGapMinutes
	}
	if v.Now.IsZero() {
		v.Now = time.Now()
	}
	return v
}

// Report 生成一份观影报告（含排行图字节）。
func (s *Service) Report(ctx context.Context, v ReportView) (domain.PlayReportResult, []byte, error) {
	v = v.normalize()
	in := reportInput{
		period:     v.Period,
		top:        v.Top,
		minSeconds: v.MinSeconds,
		gapMinutes: v.GapMinutes,
		source:     v.Source,
		now:        v.Now,
		userNames:  map[string]string{},
	}

	// 「统计从启用起累计、不补算」的起点。
	//
	// 启用时刻存在 play_traffic_daily 的哨兵行里（见 EnsureEnabledSince），
	// 不在 settings 里 —— settings 不记录更新时间，拿不到"第一次打开开关"
	// 这个时刻。若哨兵不存在（监控从未启用过），这里传零值，
	// BuildReport 就按标称周期统计；但此时监控没开，本来也不会有记录。
	enabledSince, found, err := s.enabledSince(ctx)
	if err != nil {
		return domain.PlayReportResult{}, nil, err
	}
	if found {
		in.since = enabledSince
	}

	if s.opts.Records != nil {
		// 取全区间记录：必须一次拿全，不能靠 SQL 分页。
		// 分页会把同一次播放（被 gap 合并的那些段）劈成两半、算重次数。
		//
		// AppSource 只在来源明确到某一条链路时才下推：
		// auto 表示"两条链路都算"，而 auto != 表里任何一个 app_source 取值，
		// 直接下推会把所有记录都过滤掉（报告永远是空的）。
		q := domain.PlaybackRecordQuery{
			Since: in.since.Format("2006-01-02"),
			Until: in.now.Format("2006-01-02"),
		}
		if v.Source != "" && v.Source != domain.PlayReportSourceAuto {
			q.AppSource = v.Source
		}
		recs, lerr := s.opts.Records.ListRange(ctx, q)
		if lerr != nil {
			return domain.PlayReportResult{}, nil, lerr
		}
		in.records = recs
	}

	// 用户显示名：app_user_id → 显示名。0028 遗留记录没有 RBAC 用户 ID，
	// 由 BuildReport 退到 "Emby:xxx" 兜底。
	if users := s.userResolver(); users != nil {
		for _, u := range usersFromRecords(in.records) {
			if name, ok := users.DisplayName(u); ok && name != "" {
				in.userNames["app:"+formatInt(u)] = name
			}
		}
	}

	res := BuildReport(in)
	return res, renderReportChart(res), nil
}

// enabledSince 读取插件首次启用时刻；从未启用过时返回零值与 nil 错误。
//
// "从未启用"不是错误：监控默认关闭，此时报告本来就没有数据。
// 但要留一句日志，否则"报告是空的"会变成一个查不出来的谜。
func (s *Service) enabledSince(ctx context.Context) (time.Time, bool, error) {
	if s.opts.Traffic == nil {
		return time.Time{}, false, nil
	}
	t, found, err := s.opts.Traffic.EnabledSince(ctx)
	if err != nil {
		s.logf("播放监控：读取首次启用时刻失败 %v", err)
		return time.Time{}, false, nil
	}
	if !found {
		s.log("播放监控：尚未启用过，报告按标称周期统计（当前不会有记录）")
	}
	return t, found, nil
}

// usersFromRecords 取出记录里出现过的 RBAC 用户 ID（去重）。
func usersFromRecords(recs []domain.PlaybackRecord) []int64 {
	seen := make(map[int64]struct{}, len(recs))
	out := make([]int64, 0, 4)
	for _, rec := range recs {
		if rec.AppUserID <= 0 {
			continue
		}
		if _, ok := seen[rec.AppUserID]; ok {
			continue
		}
		seen[rec.AppUserID] = struct{}{}
		out = append(out, rec.AppUserID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
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
