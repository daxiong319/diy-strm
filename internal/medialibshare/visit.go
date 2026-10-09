package medialibshare

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// 访客会话的读写。
//
// 这一组方法是验收第 4、6 条的实现位置：
//   - 第 4 条（24 小时去重）在 UpsertVisit + 判定 counted 的逻辑里；
//   - 第 6 条（设备数上限）依赖 CountDevices 只统计「当前有效」的会话。

// FindVisitByVisitor 找这个访客在某条分享下**尚未过期**的会话。
func (s *Store) FindVisitByVisitor(ctx context.Context, shareID, visitorID string, now time.Time) (Visit, bool, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT id, share_id, visitor_id, token_hash, ip_masked, user_agent, counted, created_at, last_seen_at
		 FROM library_share_visits
		 WHERE share_id=? AND visitor_id=? AND last_seen_at > ?
		 ORDER BY last_seen_at DESC LIMIT 1`,
		shareID, visitorID, now.UTC().Add(-VisitorCountWindow).Format(tsLayout))
	v, err := scanVisit(row)
	if err != nil {
		if isNoRows(err) {
			return Visit{}, false, nil
		}
		return Visit{}, false, err
	}
	return v, true, nil
}

// FindVisitByToken 按令牌哈希取会话。这是播放鉴权走的唯一查询路径。
func (s *Store) FindVisitByToken(ctx context.Context, tokenHash string) (Visit, bool, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT id, share_id, visitor_id, token_hash, ip_masked, user_agent, counted, created_at, last_seen_at
		 FROM library_share_visits WHERE token_hash=?`, tokenHash)
	v, err := scanVisit(row)
	if err != nil {
		if isNoRows(err) {
			return Visit{}, false, nil
		}
		return Visit{}, false, err
	}
	return v, true, nil
}

// UpsertVisit 在访客已有会话时返回 false（表示复用），没有则建一行并返回 true。
//
// 判定「已有会话」只看 (share_id, visitor_id) 且 last_seen 在 24 小时窗口内 ——
// 与 FindVisitByVisitor 是同一个条件，两者必须一致，否则会出现
// 「FindVisit 说不存在、这里刚建了一行」的自相矛盾。
//
// ⚠️ 曾经这里是纯「先查后插」，在并发下不成立：八个并发首访会各插一行。
// 后果不是「多了几行」这么简单 —— 每个新行 counted=0，
// MarkVisitCounted 于是对同一个访客加了八次 visitor_count，
// 而 CountDevices 数的是 DISTINCT visitor_id 所以设备数还是 1，
// 设备数这一面完全看不出出错了。
//
// 修法：靠 UNIQUE(share_id, visitor_id) 让「每访客一行」成为**数据库保证**，
// 插入撞了就当成复用（用 SQLite 的 upsert 把撞车变成一条语句，不靠事务）。
//
// ⚠️ DO UPDATE 里**不能**动 token_hash：复用时换令牌会让正在播放的页面突然 401，
// 而调用方拿到的是一个新签发的明文令牌（它只在自己 Insert 成功时才是对的）。
// DO UPDATE 只刷新「最近一次见到」和这次的 IP/UA。
//
// ⚠️ DO UPDATE 里**必须**把 counted 复位成「本次访问是否算新访客」：
// 否则同一访客每 24 小时该重计一次的窗口永远转不起来，
// TestVisitorCountedOncePerDay 就一直停在 1（这个回归真的发生过）。
// 复位的判据是 last_seen 是否还落在 24 小时窗口里 ——
// 于是「窗口内复用不计数、跨窗口重开窗口计一次数」两个语义都成立。
func (s *Store) UpsertVisit(ctx context.Context, shareID, visitorID, tokenHash, ipMasked, ua string, now time.Time) (Visit, bool, error) {
	nowTS := now.UTC().Format(tsLayout)
	winTS := now.UTC().Add(-VisitorCountWindow).Format(tsLayout)
	// counted=0 表示「当前窗口还没为这个访客计过数」，
	// 由 MarkVisitCounted 用条件 UPDATE 翻牌（并发下只有一个请求能翻）。
	// 撞上唯一约束说明这个访客已经有行了：如果那行的 last_seen 还在窗口内，
	// 复用（counted 保持原值）；否则是跨窗口回来，把 counted 清零好让这一次重新计一次。
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO library_share_visits(share_id, visitor_id, token_hash, ip_masked, user_agent, counted, created_at, last_seen_at)
		 VALUES (?,?,?,?,?,0,?,?)
		 ON CONFLICT(share_id, visitor_id) DO UPDATE SET
		   ip_masked=excluded.ip_masked,
		   user_agent=excluded.user_agent,
		   counted=CASE WHEN library_share_visits.last_seen_at > ? THEN library_share_visits.counted ELSE 0 END,
		   last_seen_at=excluded.last_seen_at`,
		shareID, visitorID, tokenHash, ipMasked, ua, nowTS, nowTS, winTS)
	if err != nil {
		return Visit{}, false, fmt.Errorf("写入访客会话失败: %w", err)
	}
	// 回读：确定这一行是新建的还是复用的。RowsAffected 分不清「插入」与「更新」，
	// 而这个差别对上层是实质性的（复用不能回传新令牌）。
	//
	// 判据是 created_at 等于**本次写入的值**（nowTS 这个字符串），不是 Equal(now) ——
	// 时间戳落库只保留到秒（tsLayout），而 now 带亚秒，
	// 于是任何带亚秒的时钟都让 Equal 判不相等：created 恒为 false，
	// 新签发的明文令牌被当成「复用」丢掉，回传 has_token=true + token=""，
	// 症状是「换令牌成功但拿不到令牌」，紧接着取流一律 401。
	// 这个坑在 medialibshare 自己的测试里没露出来，因为那边注入的是
	// time.Date(...)，亚秒恒为 0；而生产里 s.clock() 就是 time.Now().UTC()，
	// 同样带纳秒 —— 所以这是真 bug，不是测试造出来的假象。
	v, ok, err := s.FindVisitByVisitor(ctx, shareID, visitorID, now)
	if err != nil {
		return Visit{}, false, err
	}
	if !ok {
		return Visit{}, false, fmt.Errorf("写入访客会话后读不回来")
	}
	return v, v.CreatedAt.UTC().Format(tsLayout) == nowTS, nil
}

// MarkVisitCounted 把这个访客标记为「已计入过一次 visitor」。
//
// 用条件 UPDATE 而不是「先查后写」：两个并发请求会同时查到 counted=0，
// 然后双双写成 1，visitor_count 就会被记两次。
// 条件里带上 counted=0，真正翻牌的只有第一个请求，返回 RowsAffected 供上层判断要不要加计数。
func (s *Store) MarkVisitCounted(ctx context.Context, visitID int64) (bool, error) {
	res, err := s.write.ExecContext(ctx,
		`UPDATE library_share_visits SET counted=1 WHERE id=? AND counted=0`, visitID)
	if err != nil {
		return false, fmt.Errorf("标记访客计数失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BumpShareCounters 给分享的三个计数器各加 delta。delta≤0 时不写。
func (s *Store) BumpShareCounters(ctx context.Context, shareID string, views, plays, visitors int) error {
	if views <= 0 && plays <= 0 && visitors <= 0 {
		return nil
	}
	_, err := s.write.ExecContext(ctx,
		`UPDATE library_shares
		 SET view_count = view_count + ?, play_count = play_count + ?, visitor_count = visitor_count + ?
		 WHERE id=?`,
		max0(views), max0(plays), max0(visitors), shareID)
	if err != nil {
		return fmt.Errorf("更新分享计数失败: %w", err)
	}
	return nil
}

// CountDevices 统计这条分享下「当前仍活跃」的设备数。
//
// 活跃 = 最近 24 小时有动作的会话数。这就是 max_devices 的口径：
// 超过上限说明有太多台不同的设备同时在用，不是「历史上来过多少人」。
// 用 24 小时窗口而不是「行数」是为了让退出的设备自动让位 ——
// 用户不会去手动登出，但一天没再来的设备本就不该占名额。
func (s *Store) CountDevices(ctx context.Context, shareID string, now time.Time) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT visitor_id) FROM library_share_visits
		 WHERE share_id=? AND last_seen_at > ?`,
		shareID, now.UTC().Add(-VisitorCountWindow).Format(tsLayout)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计设备数失败: %w", err)
	}
	return n, nil
}

// ActiveDevicesByShare 一次查出多条分享各自的有效设备数。
//
// 存在的理由只有一个：让列表页能显示「几台设备在用」。
// 逐条 CountDevices 是 N+1 —— 分享数量级是几十条，在 SQLite 上不至于卡，
// 但那会把「打开列表」的成本和「访问的人多」绑在一起，
// 而且前端一旦把这个数字做进表格，N+1 就再也删不掉了（以后想删得改前端）。
// 这里多写一个函数，省掉的是以后一整次改表结构的返工。
func (s *Store) ActiveDevicesByShare(ctx context.Context, shareIDs []string, now time.Time) (map[string]int, error) {
	out := make(map[string]int, len(shareIDs))
	if len(shareIDs) == 0 {
		return out, nil
	}
	// SQLite 的变量上限是 999（老版本）。分享列表超过 900 条时截断，
	// 多出来的部分显示 0 设备 —— 宁可少显示，也不能让整个列表接口报错。
	const maxIDs = 900
	if len(shareIDs) > maxIDs {
		shareIDs = shareIDs[:maxIDs]
	}
	placeholders := make([]string, 0, len(shareIDs))
	args := make([]any, 0, len(shareIDs)+1)
	args = append(args, now.UTC().Add(-VisitorCountWindow).Format(tsLayout))
	for _, id := range shareIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return out, nil
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT share_id, COUNT(DISTINCT visitor_id) FROM library_share_visits
		 WHERE last_seen_at > ? AND share_id IN (`+strings.Join(placeholders, ",")+`)
		 GROUP BY share_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("统计设备数失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// InsertPlay 记一次取流。
func (s *Store) InsertPlay(ctx context.Context, p Play) error {
	started := p.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO library_share_plays(id, share_id, visit_id, visitor_id, file_id, item_label, method, ip_masked, started_at, last_seen_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.ShareID, p.VisitID, p.VisitorID, p.FileID, p.ItemLabel, p.Method, p.IPMasked,
		started.UTC().Format(tsLayout), started.UTC().Format(tsLayout))
	if err != nil {
		return fmt.Errorf("记录取流失败: %w", err)
	}
	return nil
}

func scanVisit(sc interface{ Scan(...any) error }) (Visit, error) {
	var v Visit
	var counted int
	var created, seen string
	if err := sc.Scan(&v.ID, &v.ShareID, &v.VisitorID, &v.TokenHash, &v.IPMasked, &v.UserAgent,
		&counted, &created, &seen); err != nil {
		return Visit{}, err
	}
	v.Counted = counted != 0
	v.CreatedAt, _ = time.Parse(tsLayout, created)
	v.LastSeenAt, _ = time.Parse(tsLayout, seen)
	return v, nil
}

// Stats 组装单条分享的统计。
func (s *Store) Stats(ctx context.Context, shareID string, now time.Time, recent int) (Stats, error) {
	sh, err := s.Get(ctx, shareID)
	if err != nil {
		return Stats{}, err
	}
	devices, err := s.CountDevices(ctx, shareID, now)
	if err != nil {
		return Stats{}, err
	}
	st := Stats{
		ShareID: sh.ID, Code: sh.Code, Title: sh.Title,
		ViewCount: sh.ViewCount, PlayCount: sh.PlayCount, VisitorCount: sh.VisitorCount,
		MaxDevices: sh.MaxDevices, ActiveDevices: devices,
		ExpiresAt: sh.ExpiresAt, CreatedAt: sh.CreatedAt,
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT id, share_id, visitor_id, token_hash, ip_masked, user_agent, counted, created_at, last_seen_at
		 FROM library_share_visits WHERE share_id=? ORDER BY last_seen_at DESC LIMIT ?`, shareID, recent)
	if err != nil {
		return Stats{}, fmt.Errorf("读访客流水失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanVisit(rows)
		if err != nil {
			return Stats{}, err
		}
		st.RecentVisits = append(st.RecentVisits, v)
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}

	rows2, err := s.read.QueryContext(ctx,
		`SELECT id, share_id, visit_id, visitor_id, file_id, item_label, method, ip_masked, started_at, last_seen_at
		 FROM library_share_plays WHERE share_id=? ORDER BY started_at DESC LIMIT ?`, shareID, recent)
	if err != nil {
		return Stats{}, fmt.Errorf("读取流流水失败: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var p Play
		var started, seen string
		if err := rows2.Scan(&p.ID, &p.ShareID, &p.VisitID, &p.VisitorID, &p.FileID,
			&p.ItemLabel, &p.Method, &p.IPMasked, &started, &seen); err != nil {
			return Stats{}, err
		}
		p.StartedAt, _ = time.Parse(tsLayout, started)
		p.LastSeenAt, _ = time.Parse(tsLayout, seen)
		st.RecentPlays = append(st.RecentPlays, p)
	}
	return st, rows2.Err()
}

func isNoRows(err error) bool { return err == sql.ErrNoRows }

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// maskLabel 收敛 item_label 的长度，避免有人把整段 UA 塞进这一列。
func maskLabel(s string) string {
	s = strings.TrimSpace(s)
	const max = 120
	if len(s) > max {
		return s[:max]
	}
	return s
}
