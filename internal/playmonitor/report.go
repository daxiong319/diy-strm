package playmonitor

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"litepan/internal/domain"
)

// ─────────────────────────────────────────────────────────────────────────────
// 观影报告汇总
//
// 这里刻意写成**纯函数**（不碰数据库、不碰时钟、now 显式传入）：
// 报告口径有六条验收（不补算 / 低于阈值过滤 / 等于保留 / gap 拆分 /
// 只有计费中计入上行 / 按用户分组并按条目细分），
// 每条都要能单测。掺进 I/O 就只能靠集成测试间接验，改口径时也没有护栏。
// ─────────────────────────────────────────────────────────────────────────────

// reportInput BuildReport 的输入。
type reportInput struct {
	// records 区间内的全部播放记录（已按时间升序）。
	records []domain.PlaybackRecord
	// period 统计周期（7/14/30 天）。
	period domain.PlayReportPeriod
	// top 取前多少位用户（5 或 10）；<=0 用默认值。
	top int
	// minSeconds「忽略播放时长低于 N 秒」阈值；0 = 不过滤。
	minSeconds int
	// gapMinutes 中断超过多少分钟算新的一次播放；<=0 用默认 30。
	gapMinutes int
	// source 数据来源口径（auto/emby/vyo/plex）。
	source string
	// userNames 用户分组键 → 显示名。
	userNames map[string]string
	// since 统计区间起点。调用方传入 max(周期起点, 插件启用时刻)，
	// 「不补算」的下界就落在这里。
	since time.Time
	// now 当前时刻（显式传入，便于测试"今天"边界）。
	now time.Time
}

// BuildReport 汇总观影报告。
//
// 六条口径，逐条对应验收：
//
//  1. **只累计不补算**：统计区间起点取 max(周期起点, since)。
//     窗口由调用方算好后传入 —— since 的下界是"插件启用时刻"，
//     存放在 play_traffic_daily 里（见 play_traffic_repo.go 的 EnsureEnabledSince）。
//     停用期间的播放根本没被监控记录到（监控关闭时不开会话、不写记录），
//     「停用期间不记录」由记录链本身天然保证。
//     ⚠️ 唯一绕过的路径是 0028 的 Emby 302 播放记录：那是 T08 之前就有的
//     独立写入链，不受播放监控开关控制 —— 所以遗留记录能进报告，
//     但**绝不**用它回填上行流量（见 EnsureEnabledSince 的注释）。
//  2. **min_seconds 阈值**：watched < minSeconds 丢弃，
//     **== minSeconds 保留**（判定是 `>= N`，对应前端"不计入排行"）。
//  3. **播放次数**：同一用户 + 同一条目的相邻记录，间隔 <= gapMinutes
//     合并成一次，**超过** gapMinutes 才算新的一次。
//     ⇒ 20 分钟中断 = 同一次，35 分钟中断 = 两次。
//  4. **只有计费中计入上行**：`rec.Metered == false`（CDN 直连 / 局域网）
//     时该记录的上行按 0 处理，不进排行。
//  5. **按用户分组**，用户内按条目细分，用户按次数降序取前 top 位。
//  6. **0028 遗留记录能进报告**：用户分组键在 app_user_id 为 0 时
//     退到 Emby 侧标识，播放时刻退回 playback_at 列。
func BuildReport(in reportInput) domain.PlayReportResult {
	if !in.period.Valid() {
		in.period = domain.PlayReportPeriodDefault
	}
	if in.top <= 0 {
		in.top = domain.PlayReportTopDefault
	}
	gap := time.Duration(in.gapMinutes) * time.Minute
	if in.gapMinutes <= 0 {
		gap = time.Duration(domain.PlayCountGapDefault) * time.Minute
	}
	since := in.now.AddDate(0, 0, -in.period.Days())
	if in.since.After(since) {
		since = in.since
	}

	// 按 (用户, 条目) 归拢；组内按时间升序，再用 gap 拆成"一次播放"。
	type playSpan struct {
		start  time.Time
		end    time.Time
		bytes  int64
		secs   int
		reason string
	}
	type group struct {
		userKey    string
		appUserID  int64
		embyUserID string
		userName   string
		itemName   string
		provider   string
		appSource  string
		itemScope  string
		plays      []playSpan
	}
	groups := make(map[string]*group)

	for _, rec := range in.records {
		at := parsePlayTime(rec)
		if at.IsZero() || at.Before(since) || at.After(in.now) {
			// 时刻解析不出来（0028 遗留数据里出现脏值）或落在区间外 → 丢弃。
			continue
		}
		if rec.WatchedSeconds < in.minSeconds {
			// 低于阈值丢弃。**等于阈值不会命中这里**（< 而非 <=）。
			continue
		}
		// 5a. 只有计费中计入上行。
		bytes := rec.UploadedBytes
		if !rec.Metered {
			bytes = 0
		}
		key := userGroupKey(rec) + "\x00" + rec.ItemName
		g, ok := groups[key]
		if !ok {
			g = &group{
				userKey:    userGroupKey(rec),
				appUserID:  rec.AppUserID,
				embyUserID: rec.EmbyUserID,
				userName:   in.userNames[userGroupKey(rec)],
				itemName:   rec.ItemName,
				provider:   rec.Provider,
				appSource:  rec.AppSource,
				itemScope:  rec.ItemScope,
			}
			if g.userName == "" {
				g.userName = displayNameFallback(g.appUserID, g.embyUserID)
			}
			groups[key] = g
		}
		// gap 合并判定：与本组最后一次播放的间隔 > gap 才算新的一次。
		//
		// ⚠️ 这里必须先判「组内还没有任何一次播放」：新建的 group 的
		// plays 是空的，若直接落到下面去改 plays[n-1]，n-1 = -1 会 panic。
		// 上一版就是这个漏判 —— 用例里第一条记录必然踩到。
		n := len(g.plays)
		if n == 0 || at.Sub(g.plays[n-1].end) > gap {
			g.plays = append(g.plays, playSpan{start: at, end: at, bytes: bytes, secs: rec.WatchedSeconds})
			continue
		}
		g.plays[n-1].end = at
		g.plays[n-1].bytes += bytes
		g.plays[n-1].secs += rec.WatchedSeconds
	}

	// 组装：用户 → 条目。
	byUser := make(map[string]*domain.PlayReport)
	for _, g := range groups {
		row := domain.PlayReportRule{
			AppUserID:      g.appUserID,
			EmbyUserID:     g.embyUserID,
			UserName:       g.userName,
			ItemName:       g.itemName,
			ItemScope:      g.itemScope,
			Provider:       g.provider,
			AppSource:      g.appSource,
			Count:          len(g.plays),
			WatchedSeconds: 0,
			UploadedBytes:  0,
			FirstAt:        g.plays[0].start.UTC().Format(time.RFC3339),
			LastAt:         g.plays[0].end.UTC().Format(time.RFC3339),
		}
		for _, p := range g.plays {
			row.WatchedSeconds += p.secs
			row.UploadedBytes += p.bytes
		}
		u, ok := byUser[g.userKey]
		if !ok {
			u = &domain.PlayReport{
				AppUserID:  g.appUserID,
				EmbyUserID: g.embyUserID,
				UserName:   g.userName,
				FirstAt:    row.FirstAt,
				LastAt:     row.LastAt,
				Items:      []domain.PlayReportRule{},
			}
			byUser[g.userKey] = u
		}
		u.Count += row.Count
		u.WatchedSeconds += row.WatchedSeconds
		u.UploadedBytes += row.UploadedBytes
		u.Items = append(u.Items, row)
		if row.FirstAt < u.FirstAt {
			u.FirstAt = row.FirstAt
		}
		if row.LastAt > u.LastAt {
			u.LastAt = row.LastAt
		}
	}

	users := make([]domain.PlayReport, 0, len(byUser))
	for _, u := range byUser {
		// 用户内按次数降序，同次数按观看时长降序，条目名兜底保证稳定序。
		sort.SliceStable(u.Items, func(i, j int) bool {
			if u.Items[i].Count != u.Items[j].Count {
				return u.Items[i].Count > u.Items[j].Count
			}
			if u.Items[i].WatchedSeconds != u.Items[j].WatchedSeconds {
				return u.Items[i].WatchedSeconds > u.Items[j].WatchedSeconds
			}
			return u.Items[i].ItemName < u.Items[j].ItemName
		})
		users = append(users, *u)
	}
	// 用户排序：次数降序 → 时长降序 → 上行降序 → 名字兜底。
	sort.SliceStable(users, func(i, j int) bool {
		if users[i].Count != users[j].Count {
			return users[i].Count > users[j].Count
		}
		if users[i].WatchedSeconds != users[j].WatchedSeconds {
			return users[i].WatchedSeconds > users[j].WatchedSeconds
		}
		if users[i].UploadedBytes != users[j].UploadedBytes {
			return users[i].UploadedBytes > users[j].UploadedBytes
		}
		return users[i].UserName < users[j].UserName
	})

	out := domain.PlayReportResult{
		Since:        since.UTC().Format(time.RFC3339),
		Until:        in.now.UTC().Format(time.RFC3339),
		Period:       in.period.Days(),
		MinSeconds:   in.minSeconds,
		GapMinutes:   in.gapMinutes,
		Top:          in.top,
		EnabledSince: in.since.UTC().Format(time.RFC3339),
		Users:        users,
	}
	if out.GapMinutes <= 0 {
		out.GapMinutes = domain.PlayCountGapDefault
	}
	// 全量合计先算，再按 Top 截断 —— 截断只影响展示，不影响数字。
	for _, u := range users {
		out.TotalCount += u.Count
		out.TotalWatchedSeconds += u.WatchedSeconds
		out.TotalUploadedBytes += u.UploadedBytes
	}
	if len(users) > in.top {
		users = users[:in.top]
	}
	out.Users = users
	out.Notices = reportNotices(in, out)
	return out
}

// reportNotices 统计口径说明，逐条由服务端下发。
//
// ⚠️ 这些文案**必须由服务端下发、前端原样渲染**：口径是照搬 参考实现 的，
// 让前端自己重写一遍就等于凭空多出第二套口径，而两套口径迟早会不一致
// （用户看到的是哪个？）。所以前端只负责显示，不负责解释。
func reportNotices(in reportInput, res domain.PlayReportResult) []string {
	// minText 按阈值分两种写法，避免出现"低于 0 秒"这种读不通的句子。
	minText := "未设置阈值（0 秒），保留全部播放记录"
	if in.minSeconds > 0 {
		minText = "播放时长**低于** " + strconv.Itoa(in.minSeconds) +
			" 秒的记录不进入排行，**刚好等于** " + strconv.Itoa(in.minSeconds) + " 秒的仍然保留"
	}
	gap := in.gapMinutes
	if gap <= 0 {
		gap = domain.PlayCountGapDefault
	}
	return []string{
		"统计从启用观影报告后开始累计，启用前的播放不会补算；停用期间的播放同样不记录。",
		"播放次数 = 关掉播放器重开，或中断超过" + gapText(gap) +
			"算新的一次；不足" + gapText(gap) + "的续播并进上一次。",
		"「忽略播放时长低于 N 秒」：单位为秒；" + minText + "。",
		"上行字节只统计「计费中」的播放（外网播放且字节流经过自己的服务器）；" +
			"CDN 直连与局域网播放都按 0 计。",
		"数字是**估算值**（码率 × 时长，约每 5 秒累计一次），不是精确的上行字节数。",
		"本次实际统计区间：" + shortTime(res.Since) + " 至 " + shortTime(res.Until) +
			"（标称周期 " + strconv.Itoa(in.period.Days()) + " 天）。",
	}
}

// gapText 把 gap 分钟数说成人话。默认门槛恰好是 30 分钟，
// 那正是口径文案里「中断超过半小时算新的一次」的来源 ——
// 用数字「30 分钟」写出来读者要在心里做一次换算，
// 用「半小时」才是任务书里的原话。
func gapText(minutes int) string {
	switch minutes {
	case 30:
		return "半小时"
	case 60:
		return "1 小时"
	case 120:
		return "2 小时"
	}
	return strconv.Itoa(minutes) + " 分钟"
}

// shortTime 把 RFC3339 截到分钟并转本地时区，界面上够用且不显得冗长。
func shortTime(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.Local().Format("2006-01-02 15:04")
}

// displayNameFallback 0028 遗留记录没有 RBAC 用户 ID 时的显示名兜底。
func displayNameFallback(appUserID int64, embyUserID string) string {
	if embyUserID != "" {
		return "Emby:" + embyUserID
	}
	if appUserID > 0 {
		return "用户#" + strconv.FormatInt(appUserID, 10)
	}
	return "未知用户"
}

// parsePlayTime 解析一条记录的播放时刻。
//
// 优先 timestamp（T11 新列），退回 playback_at（0028 遗留列）——
// 0028 遗留记录没有 timestamp，必须靠 playback_at 才能进报告。
func parsePlayTime(rec domain.PlaybackRecord) time.Time {
	for _, raw := range []string{rec.Timestamp, rec.PlaybackAt} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, raw); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// userGroupKey 用户分组键。
//
// app_user_id 优先（RBAC 侧）；为 0（0028 遗留记录、无法对上 RBAC 用户）
// 时退到 "emby:"+Emby 侧标识 —— 让启用前的 Emby 播放记录仍能独立成组
// 出现在报告里，而不是被 app_user_id=0 混成一堆。
func userGroupKey(rec domain.PlaybackRecord) string {
	if rec.AppUserID > 0 {
		return "app:" + strconv.FormatInt(rec.AppUserID, 10)
	}
	if rec.EmbyUserID != "" {
		return "emby:" + rec.EmbyUserID
	}
	return "anonymous"
}
