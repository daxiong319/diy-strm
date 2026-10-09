package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"litepan/internal/domain"
)

// playTrafficRepo 播放流量仓储，对齐 参考实现 的 play_traffic_daily 表。
//
// 表的唯一键是 uq_play_traffic_day_user(day, user_id)，
// 而语义是「用户 × 条目 × 天」。同用户同一天看两部片子只会落一行、
// 流量被合并 —— 这是照搬表结构带来的已知不一致（待确认点），
// 代码侧选择「合并累加、不额外拆行」，与 参考实现 行为一致。
type playTrafficRepo struct{ db *DB }

const playTrafficCols = `day, user_id, user_name, uploaded_bytes`

// playTrafficRealDay 过滤掉「首次启用标记」哨兵行。
//
// 哨兵行 day = '__enabled_since__'，不是合法日期，所以任何按日期
// 区间/前缀的查询天然不会命中它；这里仍然显式排除，是为了在有人
// 改成"不带日期过滤的全表统计"时不会把哨兵行算进来。
const playTrafficRealDay = `day <> '` + domain.PlayTrafficEnabledSinceDay + `'`

// EnsureEnabledSince 首次启用播放监控时写一次标记行；已存在则**不覆盖**。
//
// 这是「统计从启用起累计、之前不补算」这条口径唯一的真相来源。
// 覆盖写会让每次重启都把下界推成今天，累积的历史凭空消失 —— 这是
// 这段代码存在的全部理由，所以用 ON CONFLICT DO NOTHING 而不是 DO UPDATE。
func (r *playTrafficRepo) EnsureEnabledSince(ctx context.Context) error {
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO play_traffic_daily(day, user_id, user_name, uploaded_bytes, updated_at)
		 VALUES (?, 0, '', 0, ?)
		 ON CONFLICT(day, user_id) DO NOTHING`,
		domain.PlayTrafficEnabledSinceDay, time.Now().UTC().Format(time.RFC3339Nano))
	return wrapDB(err)
}

// EnabledSince 读取首次启用时刻；从未启用过时返回 (零值, false, nil)。
func (r *playTrafficRepo) EnabledSince(ctx context.Context) (time.Time, bool, error) {
	var raw string
	err := r.db.read.QueryRowContext(ctx,
		`SELECT updated_at FROM play_traffic_daily WHERE day = ? AND user_id = 0`,
		domain.PlayTrafficEnabledSinceDay).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, wrapDB(err)
	}
	t, perr := time.Parse(time.RFC3339Nano, raw)
	if perr != nil {
		// 标记行损坏不该让整个面板打不开：按"从未启用"处理并记日志由上层决定。
		return time.Time{}, false, wrapDB(perr)
	}
	return t, true, nil
}

// Accumulate 往 (day, user_id) 桶里累加上行字节。
// bytes <= 0 时直接返回：非计费态（CDN 直连 / 局域网）不该在流量表里留痕。
func (r *playTrafficRepo) Accumulate(ctx context.Context, day string, userID int64, userName string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	day = strings.TrimSpace(day)
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO play_traffic_daily(day, user_id, user_name, uploaded_bytes, updated_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(day, user_id) DO UPDATE SET
		   uploaded_bytes = uploaded_bytes + excluded.uploaded_bytes,
		   user_name = CASE WHEN excluded.user_name != '' THEN excluded.user_name ELSE user_name END,
		   updated_at = excluded.updated_at`,
		day, userID, userName, bytes, time.Now().UTC().Format(time.RFC3339Nano))
	return wrapDB(err)
}

// ByUser 列出区间内每用户的上行流量。
// since/until 为 "2006-01-02" 日期文本（含端点），空串表示不限。
func (r *playTrafficRepo) ByUser(ctx context.Context, since, until string) ([]domain.PlayTrafficBucket, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+playTrafficCols+` FROM play_traffic_daily
		 WHERE `+playTrafficRealDay+` AND day >= ? AND day <= ?
		 ORDER BY uploaded_bytes DESC, day DESC`, since, until)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.PlayTrafficBucket, 0, 32)
	for rows.Next() {
		var b domain.PlayTrafficBucket
		if err := rows.Scan(&b.Day, &b.UserID, &b.UserName, &b.UploadedBytes); err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, b)
	}
	return out, wrapDB(rows.Err())
}

// TodayMonthTotal 返回今日 / 本月 / 累计的上行字节。
// month 为 "2006-01" 前缀，用 day LIKE 'YYYY-MM%' 聚合本月。
func (r *playTrafficRepo) TodayMonthTotal(ctx context.Context, day, month string) (int64, int64, int64, error) {
	var today, monthTotal, total int64
	err := r.db.read.QueryRowContext(ctx,
		`SELECT
		   COALESCE(SUM(CASE WHEN day = ? THEN uploaded_bytes ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN day LIKE ? THEN uploaded_bytes ELSE 0 END), 0),
		   COALESCE(SUM(uploaded_bytes), 0)
		 FROM play_traffic_daily WHERE `+playTrafficRealDay, day, month+"%").Scan(&today, &monthTotal, &total)
	return today, monthTotal, total, wrapDB(err)
}

// RankByUser 返回本月外网上行排行（按用户汇总）。
func (r *playTrafficRepo) RankByUser(ctx context.Context, month string, limit int) ([]domain.PlayTrafficRank, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT user_id, COALESCE(NULLIF(user_name,''), CAST(user_id AS TEXT)) AS uname,
		        SUM(uploaded_bytes) AS total
		 FROM play_traffic_daily WHERE `+playTrafficRealDay+` AND day LIKE ?
		 GROUP BY user_id ORDER BY total DESC LIMIT ?`, month+"%", limit)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.PlayTrafficRank, 0, limit)
	for rows.Next() {
		var rank domain.PlayTrafficRank
		if err := rows.Scan(&rank.UserID, &rank.UserName, &rank.UploadedBytes); err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, rank)
	}
	return out, wrapDB(rows.Err())
}

// Clear 清空流量统计，返回删除行数（不可撤销，与 参考实现 一致）。
//
// **保留**首次启用标记行：清掉的是统计数字，不是「从哪天开始算」的口径。
// 如果连它一起删，下次启用会把下界重置成当天，已清空的历史窗口重新张开。
func (r *playTrafficRepo) Clear(ctx context.Context) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`DELETE FROM play_traffic_daily WHERE `+playTrafficRealDay)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}
