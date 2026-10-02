package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"litepan/internal/domain"
)

// playbackRecordRepo 播放记录仓储（对齐老版 models.ListEmbyPlaybackRecords）。
// playback_at 以 RFC3339 文本存储：主库现有时间列统一是文本，且这样能在
// SQL 层直接 ORDER BY 按字典序得到正确的时间倒序。
type playbackRecordRepo struct{ db *DB }

const playbackRecordCols = `id, rule_id, user_id, client, device_id, item_name, strm_path, provider, playback_at`

// 分页约束（与老版列表接口一致）。
const (
	playbackRecordDefaultPageSize = 30
	playbackRecordMaxPageSize     = 200
)

// Insert 落一条播放记录，返回自增 ID。空条目（无用户、无条目名、无路径）
// 视为无效事件直接跳过，避免播放链路抖动时刷出无意义记录。
func (r *playbackRecordRepo) Insert(ctx context.Context, rec *domain.PlaybackRecord) (int64, error) {
	if rec == nil {
		return 0, nil
	}
	if strings.TrimSpace(rec.UserID) == "" && strings.TrimSpace(rec.ItemName) == "" && strings.TrimSpace(rec.StrmPath) == "" {
		return 0, nil
	}
	if rec.RuleID == "" {
		rec.RuleID = "1"
	}
	if rec.PlaybackAt == "" {
		rec.PlaybackAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO playback_records(rule_id, user_id, client, device_id, item_name, strm_path, provider, playback_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		rec.RuleID, rec.UserID, rec.Client, rec.DeviceID,
		rec.ItemName, rec.StrmPath, rec.Provider, rec.PlaybackAt)
	if err != nil {
		return 0, wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, wrapDB(err)
	}
	rec.ID = id
	return id, nil
}

// List 分页查询播放记录，按播放时间倒序（同一时刻按 ID 倒序稳定分页）。
func (r *playbackRecordRepo) List(ctx context.Context, q domain.PlaybackRecordQuery) ([]domain.PlaybackRecord, int64, error) {
	q = normalizePlaybackRecordQuery(q)
	where, args := playbackRecordWhere(q)

	var total int64
	if err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM playback_records`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB(err)
	}

	listArgs := append(append([]any{}, args...), q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+playbackRecordCols+` FROM playback_records`+where+`
		 ORDER BY playback_at DESC, id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, wrapDB(err)
	}
	items, err := r.collect(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Delete 按 ID 删除一条播放记录；不存在时返回 nil（幂等）。
func (r *playbackRecordRepo) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.Errorf(domain.CodeValidation, "播放记录 ID 无效")
	}
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM playback_records WHERE id=?`, id)
	return wrapDB(err)
}

// Clear 按规则/用户清空播放记录，返回删除条数；两者均为空即清空全部。
func (r *playbackRecordRepo) Clear(ctx context.Context, ruleID, userID string) (int64, error) {
	where, args := playbackRecordWhere(domain.PlaybackRecordQuery{RuleID: ruleID, UserID: userID})
	res, err := r.db.write.ExecContext(ctx, `DELETE FROM playback_records`+where, args...)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB(err)
	}
	return n, nil
}

// Stats 返回概览统计：总条数、最近一次播放时间、去重用户数与条目数。
func (r *playbackRecordRepo) Stats(ctx context.Context) (domain.PlaybackRecordStats, error) {
	var (
		out    domain.PlaybackRecordStats
		lastAt sql.NullString
	)
	if err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(playback_at),''), COUNT(DISTINCT user_id), COUNT(DISTINCT item_name)
		 FROM playback_records`).Scan(&out.Total, &lastAt, &out.UserCount, &out.ItemCount); err != nil {
		return domain.PlaybackRecordStats{}, wrapDB(err)
	}
	if lastAt.Valid {
		out.LastAt = lastAt.String
	}
	return out, nil
}

func (r *playbackRecordRepo) collect(rows *sql.Rows) ([]domain.PlaybackRecord, error) {
	defer rows.Close()
	out := make([]domain.PlaybackRecord, 0, 32)
	for rows.Next() {
		var rec domain.PlaybackRecord
		if err := rows.Scan(&rec.ID, &rec.RuleID, &rec.UserID, &rec.Client, &rec.DeviceID,
			&rec.ItemName, &rec.StrmPath, &rec.Provider, &rec.PlaybackAt); err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, rec)
	}
	return out, wrapDB(rows.Err())
}

func normalizePlaybackRecordQuery(q domain.PlaybackRecordQuery) domain.PlaybackRecordQuery {
	q.RuleID = strings.TrimSpace(q.RuleID)
	q.UserID = strings.TrimSpace(q.UserID)
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Provider = strings.TrimSpace(q.Provider)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > playbackRecordMaxPageSize {
		q.PageSize = playbackRecordDefaultPageSize
	}
	return q
}

// playbackRecordWhere 拼接过滤条件。关键词同时匹配条目名与云盘路径
// （老版前端是单框搜索，用户并不会区分这两个字段）。
func playbackRecordWhere(q domain.PlaybackRecordQuery) (string, []any) {
	var conds []string
	var args []any
	if q.RuleID != "" {
		conds = append(conds, "rule_id = ?")
		args = append(args, q.RuleID)
	}
	if q.UserID != "" {
		conds = append(conds, "user_id = ?")
		args = append(args, q.UserID)
	}
	if q.Provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, q.Provider)
	}
	if q.Keyword != "" {
		conds = append(conds, "(item_name LIKE ? OR strm_path LIKE ?)")
		like := "%" + q.Keyword + "%"
		args = append(args, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
