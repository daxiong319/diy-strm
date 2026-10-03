package store

import (
	"context"
	"database/sql"
	"strings"

	"litepan/internal/domain"
)

// ---- 降级兜底（次数触发自动补找）----
//
// 语义严格对齐参考实现 mp_fallback_* 文案：
//   搜索侧 —— 以「影片+季」为粒度**累计**无结果次数（跨轮累加，不要求连续）；
//             一次正常完成的搜索若候选为 0 则 +1，一旦候选 > 0 立即清零。
//   订阅侧 —— 以「影片+季」为粒度统计**连续**无进展轮数；
//             一轮正常完成但未补到任何资源则 +1，有任何新收录立即清零。
// 两侧以 trigger（search / subscription）分别落到唯一键 (media_key, trigger)，
// 因此同一个影片+季的搜索计数与订阅计数互不影响。

const moviePilotFallbackCols = `id, media_key, trigger, media_type, tmdb_id, title, season,
	search_count, subscription_count, progress, status, action, download_episodes, external_id,
	message, created_at, updated_at`

func (r *moviePilotRepo) GetFallback(ctx context.Context, mediaKey, trigger string) (*domain.MoviePilotFallback, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+moviePilotFallbackCols+` FROM moviepilot_fallbacks WHERE media_key=? AND trigger=?`, mediaKey, trigger)
	return scanMoviePilotFallback(row)
}

func (r *moviePilotRepo) SaveFallback(ctx context.Context, rec *domain.MoviePilotFallback) error {
	if rec == nil {
		return domain.Errorf(domain.CodeValidation, "MoviePilot 兜底记录不能为空")
	}
	if rec.ID > 0 {
		_, err := r.db.write.ExecContext(ctx,
			`UPDATE moviepilot_fallbacks SET media_key=?, trigger=?, media_type=?, tmdb_id=?, title=?, season=?,
			 search_count=?, subscription_count=?, progress=?, status=?, action=?, download_episodes=?,
			 external_id=?, message=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			rec.MediaKey, rec.Trigger, rec.MediaType, rec.TmdbId, rec.Title, rec.Season,
			rec.SearchCount, rec.SubscriptionCount, rec.Progress, rec.Status, rec.Action, rec.DownloadEpisodes,
			rec.ExternalID, rec.Message, rec.ID)
		return wrapDB(err)
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO moviepilot_fallbacks(media_key, trigger, media_type, tmdb_id, title, season,
		 search_count, subscription_count, progress, status, action, download_episodes, external_id, message)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(media_key, trigger) DO UPDATE SET
			media_type=excluded.media_type, tmdb_id=excluded.tmdb_id, title=excluded.title, season=excluded.season,
			search_count=excluded.search_count, subscription_count=excluded.subscription_count,
			progress=excluded.progress, status=excluded.status, action=excluded.action,
			download_episodes=excluded.download_episodes, external_id=excluded.external_id, message=excluded.message,
			updated_at=CURRENT_TIMESTAMP`,
		rec.MediaKey, rec.Trigger, rec.MediaType, rec.TmdbId, rec.Title, rec.Season,
		rec.SearchCount, rec.SubscriptionCount, rec.Progress, rec.Status, rec.Action, rec.DownloadEpisodes,
		rec.ExternalID, rec.Message)
	if err != nil {
		return wrapDB(err)
	}
	// ON CONFLICT 时 LastInsertId 不可靠：回读一次拿稳定主键；回读失败不视为写入失败。
	row := r.db.read.QueryRowContext(ctx,
		`SELECT id FROM moviepilot_fallbacks WHERE media_key=? AND trigger=?`, rec.MediaKey, rec.Trigger)
	if scanErr := row.Scan(&rec.ID); scanErr == nil {
		return nil
	}
	if id, idErr := res.LastInsertId(); idErr == nil {
		rec.ID = id
	}
	return nil
}

func (r *moviePilotRepo) ListFallbacks(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotFallback, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	where := ""
	args := []any{}
	if strings.TrimSpace(status) != "" {
		where = " WHERE status=?"
		args = append(args, status)
	}
	var total int64
	if err := r.db.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM moviepilot_fallbacks`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB(err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+moviePilotFallbackCols+` FROM moviepilot_fallbacks`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, wrapDB(err)
	}
	defer rows.Close()
	out, err := scanMoviePilotFallbacks(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *moviePilotRepo) CountActiveFallbacks(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM moviepilot_fallbacks WHERE status IN (?,?)`,
		domain.MoviePilotFallbackPending, domain.MoviePilotFallbackRunning).Scan(&n)
	if err != nil {
		return 0, wrapDB(err)
	}
	return n, nil
}

// HasPendingTransferTasks 是否存在「待入库」任务：
//   - movie_pilot_upload_tasks 尚未完成（pending/uploading）；
//   - upload_tasks 尚未完成（pending/running）。
//
// 表尚未建立（首启）时按「无待入库」返回，避免兜底因历史表缺失整体失效。
func (r *moviePilotRepo) HasPendingTransferTasks(ctx context.Context) (bool, error) {
	var n int64
	err := r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM movie_pilot_upload_tasks WHERE status IN (?,?)`,
		domain.MoviePilotUploadPending, domain.MoviePilotUploadUploading).Scan(&n)
	if err != nil {
		if isMissingTable(err) {
			return false, nil
		}
		return false, wrapDB(err)
	}
	if n > 0 {
		return true, nil
	}
	err = r.db.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM upload_tasks WHERE status IN ('pending','running')`).Scan(&n)
	if err != nil {
		if isMissingTable(err) {
			return false, nil
		}
		return false, wrapDB(err)
	}
	return n > 0, nil
}

func scanMoviePilotFallback(row interface{ Scan(...any) error }) (*domain.MoviePilotFallback, error) {
	var (
		f           domain.MoviePilotFallback
		createdNull sql.NullString
		updatedNull sql.NullString
	)
	err := row.Scan(&f.ID, &f.MediaKey, &f.Trigger, &f.MediaType, &f.TmdbId, &f.Title, &f.Season,
		&f.SearchCount, &f.SubscriptionCount, &f.Progress, &f.Status, &f.Action, &f.DownloadEpisodes,
		&f.ExternalID, &f.Message, &createdNull, &updatedNull)
	if err != nil {
		return nil, wrapDB(err)
	}
	f.CreatedAt = parseTS(createdNull)
	f.UpdatedAt = parseTS(updatedNull)
	return &f, nil
}

func scanMoviePilotFallbacks(rows *sql.Rows) ([]domain.MoviePilotFallback, error) {
	var out []domain.MoviePilotFallback
	for rows.Next() {
		var (
			f           domain.MoviePilotFallback
			createdNull sql.NullString
			updatedNull sql.NullString
		)
		if err := rows.Scan(&f.ID, &f.MediaKey, &f.Trigger, &f.MediaType, &f.TmdbId, &f.Title, &f.Season,
			&f.SearchCount, &f.SubscriptionCount, &f.Progress, &f.Status, &f.Action, &f.DownloadEpisodes,
			&f.ExternalID, &f.Message, &createdNull, &updatedNull); err != nil {
			return nil, wrapDB(err)
		}
		f.CreatedAt = parseTS(createdNull)
		f.UpdatedAt = parseTS(updatedNull)
		out = append(out, f)
	}
	return out, wrapDB(rows.Err())
}

// isMissingTable 判断错误是否为「表不存在」。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table")
}
