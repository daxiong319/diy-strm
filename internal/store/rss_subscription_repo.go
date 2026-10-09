package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"litepan/internal/domain"
)

// rssSourceRepo / rssHistoryRepo 实现 T16 的 RSS 订阅源与条目历史仓储。
//
// 两条与表结构强相关的口径，代码里到处引用，改动前先读注释：
//
//  1. guid 是**全局唯一**（uq_rss_subscription_history_guid，单列，不带
//     source_id），与 参考实现 一致。InsertOnce 因此走 ON CONFLICT(guid) DO
//     NOTHING 并在冲突后回查既有行，把「已经处理过」表达成正常返回值而不是
//     错误 —— 同步流程里 guid 命中是每轮都在发生的常态，报错会让整个源同步失败。
//
//  2. 历史表**没有软删**。DeleteByID 的语义是「把这个条目重新放回待处理队列」
//     （人工豁免），不是「撤销一次下载」—— 真要撤销得去离线下载那边。UI 上
//     这就是「标记为未处理/重新下载」。
type (
	rssSourceRepo  struct{ db *DB }
	rssHistoryRepo struct{ db *DB }
)

const rssSourceCols = `id, name, rss_url, target_path, storage, media_server, poster_url,
	include_regex, exclude_regex, media_type, action, enabled,
	last_sync_at, last_status, last_message, created_at, updated_at`

const rssHistoryCols = `id, source_id, source_name, guid, title, link, download_url,
	target_path, status, message, published_at, created_at`

// rssNow 统一的时间戳写入格式，SQLite CURRENT_TIMESTAMP 与它等价。
func rssNow() string { return time.Now().UTC().Format(tsLayout) }

// publishedAny 把可空时间域展开成可写入的值：nil 写 NULL 而不是零值，
// 零值会让 published_at 变成 1970 年，UI 上显示成「1970-01-01」。
func publishedAny(t *time.Time) any {
	if t == nil {
		return nil
	}
	return tsValue(*t)
}

// ---------------- 源 ----------------

func (r *rssSourceRepo) List(ctx context.Context, q domain.RSSSourceQuery) ([]domain.RSSSource, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT ` + rssSourceCols + ` FROM rss_subscription_sources WHERE 1=1`)
	if q.Enabled != nil {
		sb.WriteString(` AND enabled = ?`)
		args = append(args, boolToInt(*q.Enabled))
	}
	if kw := strings.TrimSpace(q.Keyword); kw != "" {
		sb.WriteString(` AND (name LIKE ? OR rss_url LIKE ?)`)
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	sb.WriteString(` ORDER BY id ASC`)
	if q.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.Limit)
	}
	rows, err := r.db.read.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.RSSSource, 0, 8)
	for rows.Next() {
		s, err := scanRSSSource(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, s)
	}
	return out, wrapDB(rows.Err())
}

func (r *rssSourceRepo) Get(ctx context.Context, id int64) (domain.RSSSource, bool, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+rssSourceCols+` FROM rss_subscription_sources WHERE id = ?`, id)
	s, err := scanRSSSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RSSSource{}, false, nil
	}
	if err != nil {
		return domain.RSSSource{}, false, wrapDB(err)
	}
	return s, true, nil
}

func (r *rssSourceRepo) GetByURL(ctx context.Context, rssURL string) (domain.RSSSource, bool, error) {
	rssURL = strings.TrimSpace(rssURL)
	if rssURL == "" {
		return domain.RSSSource{}, false, nil
	}
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+rssSourceCols+` FROM rss_subscription_sources WHERE rss_url = ? ORDER BY id ASC LIMIT 1`,
		rssURL)
	s, err := scanRSSSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RSSSource{}, false, nil
	}
	if err != nil {
		return domain.RSSSource{}, false, wrapDB(err)
	}
	return s, true, nil
}

func (r *rssSourceRepo) Upsert(ctx context.Context, p domain.RSSUpsertPayload) (domain.RSSSource, error) {
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	now := rssNow()
	if p.ID > 0 {
		_, err := r.db.write.ExecContext(ctx,
			`UPDATE rss_subscription_sources SET
			   name=?, rss_url=?, target_path=?, storage=?, media_server=?, poster_url=?,
			   include_regex=?, exclude_regex=?, media_type=?, action=?, enabled=?, updated_at=?
			 WHERE id=?`,
			p.Name, p.RssURL, p.TargetPath, p.Storage, p.MediaServer, p.PosterURL,
			p.IncludeRegex, p.ExcludeRegex, p.MediaType, p.Action, boolToInt(enabled), now, p.ID)
		if err != nil {
			return domain.RSSSource{}, wrapDB(err)
		}
		s, found, err := r.Get(ctx, p.ID)
		if err != nil {
			return domain.RSSSource{}, err
		}
		if !found {
			return domain.RSSSource{}, domain.Errf(domain.CodeNotFound)
		}
		return s, nil
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO rss_subscription_sources(
		   name, rss_url, target_path, storage, media_server, poster_url,
		   include_regex, exclude_regex, media_type, action, enabled, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Name, p.RssURL, p.TargetPath, p.Storage, p.MediaServer, p.PosterURL,
		p.IncludeRegex, p.ExcludeRegex, p.MediaType, p.Action, boolToInt(enabled), now, now)
	if err != nil {
		return domain.RSSSource{}, wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.RSSSource{}, wrapDB(err)
	}
	s, found, err := r.Get(ctx, id)
	if err != nil {
		return domain.RSSSource{}, err
	}
	if !found {
		return domain.RSSSource{}, domain.Errf(domain.CodeNotFound)
	}
	return s, nil
}

func (r *rssSourceRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM rss_subscription_sources WHERE id = ?`, id)
	return wrapDB(err)
}

func (r *rssSourceRepo) MarkSyncResult(ctx context.Context, id int64, status, message string, at time.Time) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE rss_subscription_sources SET last_status=?, last_message=?, last_sync_at=?, updated_at=? WHERE id=?`,
		status, message, tsValue(at), rssNow(), id)
	return wrapDB(err)
}

func (r *rssSourceRepo) ListEnabled(ctx context.Context) ([]domain.RSSSource, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+rssSourceCols+` FROM rss_subscription_sources WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.RSSSource, 0, 8)
	for rows.Next() {
		s, err := scanRSSSource(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, s)
	}
	return out, wrapDB(rows.Err())
}

// ---------------- 历史 ----------------

// InsertOnce 写入历史；guid 命中唯一约束时回查既有行返回（inserted=false）。
func (r *rssHistoryRepo) InsertOnce(ctx context.Context, h domain.RSSHistory) (domain.RSSHistory, bool, error) {
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO rss_subscription_history(
		   source_id, source_name, guid, title, link, download_url,
		   target_path, status, message, published_at, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(guid) DO NOTHING`,
		h.SourceID, h.SourceName, h.Guid, h.Title, h.Link, h.DownloadURL,
		h.TargetPath, h.Status, h.Message, publishedAny(h.PublishedAt), rssNow())
	if err != nil {
		return domain.RSSHistory{}, false, wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.RSSHistory{}, false, wrapDB(err)
	}
	if n > 0 {
		h.ID, _ = res.LastInsertId()
		return h, true, nil
	}
	// 冲突分支：回查那条已存在的记录，让调用方能拿到历史 ID
	// （失败重试与「重新放回队列」都按 guid 定位，不按 id）。
	existing, found, gerr := r.getByGuid(ctx, h.Guid)
	if gerr != nil {
		return domain.RSSHistory{}, false, gerr
	}
	if !found {
		// ON CONFLICT 说插不进去、select 又没有：并发下另一个写者刚删掉了。
		// 当成未插入，调用方下轮会重试，不算错。
		return domain.RSSHistory{}, false, nil
	}
	return existing, false, nil
}

func (r *rssHistoryRepo) getByGuid(ctx context.Context, guid string) (domain.RSSHistory, bool, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+rssHistoryCols+` FROM rss_subscription_history WHERE guid = ?`, guid)
	h, err := scanRSSHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RSSHistory{}, false, nil
	}
	if err != nil {
		return domain.RSSHistory{}, false, wrapDB(err)
	}
	return h, true, nil
}

func (r *rssHistoryRepo) HasGuid(ctx context.Context, guid string) (bool, error) {
	guid = strings.TrimSpace(guid)
	if guid == "" {
		// 空 guid 不可能写进表（列 NOT NULL 但空串可以），这里按「查不到」处理，
		// 让调用方走 Item.DedupKey 的兜底链而不是拿空串去比。
		return false, nil
	}
	var one int
	err := r.db.read.QueryRowContext(ctx,
		`SELECT 1 FROM rss_subscription_history WHERE guid = ? LIMIT 1`, guid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, wrapDB(err)
	}
	return true, nil
}

func (r *rssHistoryRepo) List(ctx context.Context, q domain.RSSHistoryQuery) ([]domain.RSSHistory, error) {
	var (
		sb   strings.Builder
		args []any
	)
	sb.WriteString(`SELECT ` + rssHistoryCols + ` FROM rss_subscription_history WHERE 1=1`)
	if q.SourceID > 0 {
		sb.WriteString(` AND source_id = ?`)
		args = append(args, q.SourceID)
	}
	if st := strings.TrimSpace(q.Status); st != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, st)
	}
	if !q.Since.IsZero() {
		sb.WriteString(` AND created_at >= ?`)
		args = append(args, q.Since.UTC().Format(tsLayout))
	}
	sb.WriteString(` ORDER BY id DESC`)
	if q.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.Limit)
	}
	rows, err := r.db.read.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	out := make([]domain.RSSHistory, 0, 32)
	for rows.Next() {
		h, err := scanRSSHistory(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, h)
	}
	return out, wrapDB(rows.Err())
}

func (r *rssHistoryRepo) DeleteBySource(ctx context.Context, sourceID int64) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`DELETE FROM rss_subscription_history WHERE source_id = ?`, sourceID)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB(err)
	}
	return n, nil
}

func (r *rssHistoryRepo) DeleteByID(ctx context.Context, id int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM rss_subscription_history WHERE id = ?`, id)
	return wrapDB(err)
}

// LatestProcessedAt 返回该源最近一次写入历史的时刻，作为轮询位点。
func (r *rssHistoryRepo) LatestProcessedAt(ctx context.Context, sourceID int64) (time.Time, bool, error) {
	var raw string
	err := r.db.read.QueryRowContext(ctx,
		`SELECT created_at FROM rss_subscription_history WHERE source_id = ? ORDER BY id DESC LIMIT 1`,
		sourceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, wrapDB(err)
	}
	t := parseTS(sql.NullString{String: raw, Valid: raw != ""})
	if t.IsZero() {
		// 记录存在但时间列损坏：不返回 found=true，否则位点会被判成公元 1 年，
		// 下一轮就把整个 feed 历史全部当新条目重放一遍。
		return time.Time{}, false, nil
	}
	return t, true, nil
}

// ---------------- 扫描 ----------------

type scanner interface{ Scan(dest ...any) error }

func scanRSSSource(row scanner) (domain.RSSSource, error) {
	var (
		s                          domain.RSSSource
		enabled                    int
		lastSync, created, updated sql.NullString
	)
	err := row.Scan(&s.ID, &s.Name, &s.RssURL, &s.TargetPath, &s.Storage, &s.MediaServer, &s.PosterURL,
		&s.IncludeRegex, &s.ExcludeRegex, &s.MediaType, &s.Action, &enabled,
		&lastSync, &s.LastStatus, &s.LastMessage, &created, &updated)
	if err != nil {
		return domain.RSSSource{}, err
	}
	s.Enabled = enabled != 0
	if t := parseTS(lastSync); !t.IsZero() {
		tt := t
		s.LastSyncAt = &tt
	}
	s.CreatedAt = parseTS(created)
	s.UpdatedAt = parseTS(updated)
	return s, nil
}

func scanRSSHistory(row scanner) (domain.RSSHistory, error) {
	var (
		h                  domain.RSSHistory
		published, created sql.NullString
	)
	err := row.Scan(&h.ID, &h.SourceID, &h.SourceName, &h.Guid, &h.Title, &h.Link, &h.DownloadURL,
		&h.TargetPath, &h.Status, &h.Message, &published, &created)
	if err != nil {
		return domain.RSSHistory{}, err
	}
	if t := parseTS(published); !t.IsZero() {
		tt := t
		h.PublishedAt = &tt
	}
	h.CreatedAt = parseTS(created)
	return h, nil
}
