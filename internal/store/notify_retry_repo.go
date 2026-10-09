package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"litepan/internal/domain"
)

// notifyRetryRepo 通知补发队列仓储。
//
// 只服务 webhook 渠道（入队判定在 notifychannel/outbox.go），但表本身
// 不做渠道类型约束：将来若要给别的渠道加补发，改的是入队那一处条件，
// 不是这张表的 DDL。
type notifyRetryRepo struct{ db *DB }

const notifyRetryCols = `id, event_scene, channel_type, channel_name, target_url,
	channel_config, title, content, tone, attempts, next_retry_at, last_error, status,
	created_at, updated_at`

// notifyRetryDefaultLimit / MaxLimit 限制管理台列表与排期扫描的取数规模。
const (
	notifyRetryDefaultLimit = 100
	notifyRetryMaxLimit     = 500
)

func notifyRetryLimit(v int) int {
	if v <= 0 {
		return notifyRetryDefaultLimit
	}
	if v > notifyRetryMaxLimit {
		return notifyRetryMaxLimit
	}
	return v
}

// notifyRetryNow 返回与 tsLayout 一致的当前时刻文本。
//
// 全仓的时间列都用 tsLayout 写入（见 util.go 的 tsValue），这里必须跟它一致，
// 否则 created_at 与 next_retry_at 混着两种格式，同一列内逐字符比较的结果
// 完全不可预测。
func notifyRetryNow() string { return time.Now().UTC().Format(tsLayout) }

// Enqueue 新增一条待补发记录。
//
// 显式写 status='pending' 与 next_retry_at：入队时刻就是第一次重试时刻，
// 由调用方按退避表算出 base delay 传进来（仓储不猜退避策略 —— 那是 worker 的事）。
func (r *notifyRetryRepo) Enqueue(ctx context.Context, e domain.NotifyRetryEntry) (int64, error) {
	now := notifyRetryNow()
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO notify_retry_queue
		   (event_scene, channel_type, channel_name, target_url, channel_config,
		    title, content, tone, attempts, next_retry_at, last_error, status,
		    created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(e.EventScene), e.ChannelType, e.ChannelName, e.TargetURL, e.ChannelConfig,
		e.Title, e.Content, e.Tone, e.Attempts, tsValue(e.NextRetryAt), e.LastError,
		string(e.Status), now, now)
	if err != nil {
		return 0, wrapDB(err)
	}
	id, err := res.LastInsertId()
	return id, wrapDB(err)
}

func (r *notifyRetryRepo) scanRetry(row rowScanner) (domain.NotifyRetryEntry, error) {
	var e domain.NotifyRetryEntry
	var scene, status string
	var nextRaw sql.NullString
	err := row.Scan(&e.ID, &scene, &e.ChannelType, &e.ChannelName, &e.TargetURL,
		&e.ChannelConfig, &e.Title, &e.Content, &e.Tone, &e.Attempts, &nextRaw,
		&e.LastError, &status, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return domain.NotifyRetryEntry{}, err
	}
	e.EventScene = domain.NotificationScene(scene)
	e.Status = domain.NotifyRetryStatus(status)
	e.NextRetryAt = parseTS(nextRaw)
	return e, nil
}

// Due 查询到点待重试的记录。
//
// ⚠️ 比较值必须用 tsLayout（"2006-01-02 15:04:05"）而不是 RFC3339Nano：
// tsValue 写进去的是前者，SQLite 对 TEXT 是**逐字符**比较，两者混用时
// 位置 10 上是 ' '(0x20) vs 'T'(0x54)，存储值永远"小于"查询值 ——
// 于是每条记录一入队就被判定为到点，整个退避静默失效。
func (r *notifyRetryRepo) Due(ctx context.Context, now time.Time, limit int) ([]domain.NotifyRetryEntry, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+notifyRetryCols+` FROM notify_retry_queue
		 WHERE status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?
		 ORDER BY next_retry_at ASC, id ASC LIMIT ?`,
		string(domain.NotifyRetryStatusPending), now.UTC().Format(tsLayout), notifyRetryLimit(limit))
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.NotifyRetryEntry, 0, limit)
	for rows.Next() {
		e, err := r.scanRetry(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, e)
	}
	return out, wrapDB(rows.Err())
}

// UpdateResult 记录一次重试结果。
func (r *notifyRetryRepo) UpdateResult(ctx context.Context, id int64, attempts int, nextRetryAt time.Time, lastErr string, status domain.NotifyRetryStatus) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE notify_retry_queue
		    SET attempts = ?, next_retry_at = ?, last_error = ?, status = ?, updated_at = ?
		  WHERE id = ?`,
		attempts, tsValue(nextRetryAt), lastErr, string(status),
		notifyRetryNow(), id)
	return wrapDB(err)
}

// List 分页列出。总数单独 COUNT 一份，游标分页用不上但管理台要显示总数。
func (r *notifyRetryRepo) List(ctx context.Context, q domain.NotifyRetryQuery) ([]domain.NotifyRetryEntry, int64, error) {
	where := ""
	var args []any
	if st := strings.TrimSpace(string(q.Status)); st != "" {
		where = ` WHERE status = ?`
		args = append(args, st)
	}
	var total int64
	if err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notify_retry_queue`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB(err)
	}
	limit := notifyRetryLimit(q.Limit)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+notifyRetryCols+` FROM notify_retry_queue`+where+`
		 ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(append([]any(nil), args...), limit, offset)...)
	if err != nil {
		return nil, 0, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.NotifyRetryEntry, 0, limit)
	for rows.Next() {
		e, err := r.scanRetry(rows)
		if err != nil {
			return nil, 0, wrapDB(err)
		}
		out = append(out, e)
	}
	return out, total, wrapDB(rows.Err())
}

func (r *notifyRetryRepo) Get(ctx context.Context, id int64) (domain.NotifyRetryEntry, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+notifyRetryCols+` FROM notify_retry_queue WHERE id = ?`, id)
	e, err := r.scanRetry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotifyRetryEntry{}, domain.Errorf(domain.CodeNotFound, "补发记录不存在: %d", id)
	}
	if err != nil {
		return domain.NotifyRetryEntry{}, wrapDB(err)
	}
	return e, nil
}

// Redrive 把 failed 记录重新排回 pending。
//
// ⚠️ 刻意只在 failed 状态上生效：若允许改 pending，会把正在退避等待中的
// 记录 attempts 归零，等于免费插队重试，退避上限形同虚设。
func (r *notifyRetryRepo) Redrive(ctx context.Context, id int64, now time.Time) error {
	res, err := r.db.write.ExecContext(ctx,
		`UPDATE notify_retry_queue
		    SET status = ?, attempts = 0, next_retry_at = ?, last_error = '', updated_at = ?
		  WHERE id = ? AND status = ?`,
		string(domain.NotifyRetryStatusPending), tsValue(now),
		notifyRetryNow(), id,
		string(domain.NotifyRetryStatusFailed))
	if err != nil {
		return wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapDB(err)
	}
	if n == 0 {
		// 区分「不存在」和「状态不对」，管理台点重投时提示才有意义。
		if _, gerr := r.Get(ctx, id); gerr != nil {
			return gerr
		}
		return domain.Errorf(domain.CodeValidation, "补发记录当前不是失败状态，无需重投")
	}
	return nil
}

// Clear 按状态清理；空状态 = 清全部。
func (r *notifyRetryRepo) Clear(ctx context.Context, status domain.NotifyRetryStatus) (int64, error) {
	q := `DELETE FROM notify_retry_queue`
	var args []any
	if st := strings.TrimSpace(string(status)); st != "" {
		q += ` WHERE status = ?`
		args = append(args, st)
	}
	res, err := r.db.write.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}

// Counts 返回各状态条数。map 里没有的状态补 0，方便前端直接读。
func (r *notifyRetryRepo) Counts(ctx context.Context) (map[domain.NotifyRetryStatus]int64, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM notify_retry_queue GROUP BY status`)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := map[domain.NotifyRetryStatus]int64{
		domain.NotifyRetryStatusPending: 0,
		domain.NotifyRetryStatusSent:    0,
		domain.NotifyRetryStatusFailed:  0,
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, wrapDB(err)
		}
		out[domain.NotifyRetryStatus(st)] = n
	}
	return out, wrapDB(rows.Err())
}
