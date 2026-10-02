package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"litepan/internal/domain"
)

type moviePilotRepo struct{ db *DB }

// ---- 配置（单行全局） ----

func (r *moviePilotRepo) LoadConfig(ctx context.Context) (*domain.MoviePilotConfig, error) {
	// 单行全局配置：首次读取时若不存在则创建默认行。
	row := r.db.read.QueryRowContext(ctx, `SELECT `+moviePilotConfigCols+` FROM movie_pilot_configs ORDER BY id ASC LIMIT 1`)
	cfg, err := scanMoviePilotConfig(row)
	if err == nil {
		return cfg, nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	defaults := &domain.MoviePilotConfig{PollInterval: 5, NotifyEnabled: true, PromotionOrder: domain.DefaultPromotionOrder, PromotionPatienceHours: 12}
	if saveErr := r.SaveConfig(ctx, defaults); saveErr != nil {
		return nil, saveErr
	}
	return r.LoadConfig(ctx)
}

func (r *moviePilotRepo) SaveConfig(ctx context.Context, cfg *domain.MoviePilotConfig) error {
	if cfg.ID > 0 {
		_, err := r.db.write.ExecContext(ctx,
			`UPDATE movie_pilot_configs SET enabled=?, base_url=?, api_token=?, download_root=?, local_view_root=?,
			 upload_account_id=?, upload_root=?, upload_root_id=?, strm_local_dir=?, poll_interval=?, notify_enabled=?,
			 category_config=?, promotion_order=?, promotion_patience_hours=?, seed_retention_hours=?,
			 qbittorrent_url=?, qbittorrent_user=?, qbittorrent_pass=?, updated_at=CURRENT_TIMESTAMP
			 WHERE id=?`,
			boolToInt(cfg.Enabled), cfg.BaseUrl, cfg.ApiToken, cfg.DownloadRoot, cfg.LocalViewRoot,
			cfg.UploadAccountId, cfg.UploadRoot, cfg.UploadRootId, cfg.StrmLocalDir, cfg.PollInterval, boolToInt(cfg.NotifyEnabled),
			cfg.CategoryConfig, cfg.PromotionOrder, cfg.PromotionPatienceHours, cfg.SeedRetentionHours,
			cfg.QbittorrentURL, cfg.QbittorrentUser, cfg.QbittorrentPass, cfg.ID)
		return wrapDB(err)
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO movie_pilot_configs(enabled, base_url, api_token, download_root, local_view_root,
		 upload_account_id, upload_root, upload_root_id, strm_local_dir, poll_interval, notify_enabled,
		 category_config, promotion_order, promotion_patience_hours, seed_retention_hours,
		 qbittorrent_url, qbittorrent_user, qbittorrent_pass)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		boolToInt(cfg.Enabled), cfg.BaseUrl, cfg.ApiToken, cfg.DownloadRoot, cfg.LocalViewRoot,
		cfg.UploadAccountId, cfg.UploadRoot, cfg.UploadRootId, cfg.StrmLocalDir, cfg.PollInterval, boolToInt(cfg.NotifyEnabled),
		cfg.CategoryConfig, cfg.PromotionOrder, cfg.PromotionPatienceHours, cfg.SeedRetentionHours,
		cfg.QbittorrentURL, cfg.QbittorrentUser, cfg.QbittorrentPass)
	if err != nil {
		return wrapDB(err)
	}
	if id, idErr := res.LastInsertId(); idErr == nil {
		cfg.ID = id
	}
	return nil
}

const moviePilotConfigCols = `id, enabled, base_url, api_token, download_root, local_view_root, upload_account_id,
	upload_root, upload_root_id, strm_local_dir, poll_interval, notify_enabled, category_config,
	promotion_order, promotion_patience_hours, seed_retention_hours, qbittorrent_url, qbittorrent_user,
	qbittorrent_pass, created_at, updated_at`

func scanMoviePilotConfig(row interface{ Scan(...any) error }) (*domain.MoviePilotConfig, error) {
	var (
		c             domain.MoviePilotConfig
		enabled       int
		notifyEnabled int
		createdNull   sql.NullString
		updatedNull   sql.NullString
	)
	err := row.Scan(&c.ID, &enabled, &c.BaseUrl, &c.ApiToken, &c.DownloadRoot, &c.LocalViewRoot, &c.UploadAccountId,
		&c.UploadRoot, &c.UploadRootId, &c.StrmLocalDir, &c.PollInterval, &notifyEnabled, &c.CategoryConfig,
		&c.PromotionOrder, &c.PromotionPatienceHours, &c.SeedRetentionHours, &c.QbittorrentURL, &c.QbittorrentUser,
		&c.QbittorrentPass, &createdNull, &updatedNull)
	if err != nil {
		return nil, wrapDB(err)
	}
	c.Enabled = enabled != 0
	c.NotifyEnabled = notifyEnabled != 0
	c.CreatedAt = parseTS(createdNull)
	c.UpdatedAt = parseTS(updatedNull)
	return &c, nil
}

// ---- 促销阶梯 ----

func (r *moviePilotRepo) GetPromotionLadder(ctx context.Context, subscribeID int64) (*domain.MoviePilotPromotionLadder, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT subscribe_id, tier, tier_started_at, updated_at FROM movie_pilot_promotion_ladders WHERE subscribe_id=?`, subscribeID)
	var (
		l           domain.MoviePilotPromotionLadder
		updatedNull sql.NullString
	)
	if err := row.Scan(&l.SubscribeID, &l.Tier, &l.TierStartedAt, &updatedNull); err != nil {
		return nil, wrapDB(err)
	}
	l.UpdatedAt = parseTS(updatedNull)
	return &l, nil
}

func (r *moviePilotRepo) SavePromotionLadder(ctx context.Context, l *domain.MoviePilotPromotionLadder) error {
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO movie_pilot_promotion_ladders(subscribe_id, tier, tier_started_at, updated_at)
		 VALUES (?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(subscribe_id) DO UPDATE SET tier=excluded.tier, tier_started_at=excluded.tier_started_at, updated_at=CURRENT_TIMESTAMP`,
		l.SubscribeID, l.Tier, l.TierStartedAt)
	return wrapDB(err)
}

func (r *moviePilotRepo) DeletePromotionLadder(ctx context.Context, subscribeID int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM movie_pilot_promotion_ladders WHERE subscribe_id=?`, subscribeID)
	return wrapDB(err)
}

func (r *moviePilotRepo) ListPromotionLadders(ctx context.Context) ([]domain.MoviePilotPromotionLadder, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT subscribe_id, tier, tier_started_at, updated_at FROM movie_pilot_promotion_ladders ORDER BY subscribe_id ASC`)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.MoviePilotPromotionLadder
	for rows.Next() {
		var (
			l           domain.MoviePilotPromotionLadder
			updatedNull sql.NullString
		)
		if err := rows.Scan(&l.SubscribeID, &l.Tier, &l.TierStartedAt, &updatedNull); err != nil {
			return nil, wrapDB(err)
		}
		l.UpdatedAt = parseTS(updatedNull)
		out = append(out, l)
	}
	return out, wrapDB(rows.Err())
}

// ---- 上传任务 ----

const moviePilotUploadTaskCols = `id, torrent_hash, title, media_type, tmdb_id, season, local_path, remote_path,
	status, total_files, uploaded_files, total_bytes, uploaded_bytes, error, empty_source_since, created_at, updated_at`

func (r *moviePilotRepo) CreateUploadTask(ctx context.Context, t *domain.MoviePilotUploadTask) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO movie_pilot_upload_tasks(torrent_hash, title, media_type, tmdb_id, season, local_path, remote_path,
		 status, total_files, uploaded_files, total_bytes, uploaded_bytes, error, empty_source_since)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.TorrentHash, t.Title, t.MediaType, t.TmdbId, t.Season, t.LocalPath, t.RemotePath,
		t.Status, t.TotalFiles, t.UploadedFiles, t.TotalBytes, t.UploadedBytes, t.Error, tsValuePtr(t.EmptySourceSince))
	if err != nil {
		return 0, wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, wrapDB(err)
	}
	t.ID = id
	return id, nil
}

func (r *moviePilotRepo) UpdateUploadTask(ctx context.Context, t *domain.MoviePilotUploadTask) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE movie_pilot_upload_tasks SET torrent_hash=?, title=?, media_type=?, tmdb_id=?, season=?, local_path=?,
		 remote_path=?, status=?, total_files=?, uploaded_files=?, total_bytes=?, uploaded_bytes=?, error=?,
		 empty_source_since=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		t.TorrentHash, t.Title, t.MediaType, t.TmdbId, t.Season, t.LocalPath, t.RemotePath,
		t.Status, t.TotalFiles, t.UploadedFiles, t.TotalBytes, t.UploadedBytes, t.Error, tsValuePtr(t.EmptySourceSince), t.ID)
	return wrapDB(err)
}

func (r *moviePilotRepo) GetUploadTask(ctx context.Context, id int64) (*domain.MoviePilotUploadTask, error) {
	row := r.db.read.QueryRowContext(ctx, `SELECT `+moviePilotUploadTaskCols+` FROM movie_pilot_upload_tasks WHERE id=?`, id)
	return scanMoviePilotUploadTask(row)
}

func (r *moviePilotRepo) FindUploadTaskByHash(ctx context.Context, hash string) (*domain.MoviePilotUploadTask, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, nil
	}
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+moviePilotUploadTaskCols+` FROM movie_pilot_upload_tasks WHERE torrent_hash=? ORDER BY id DESC LIMIT 1`, hash)
	t, err := scanMoviePilotUploadTask(row)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

func (r *moviePilotRepo) FindUploadTaskByLocalPath(ctx context.Context, localPath, excludeHash string) (*domain.MoviePilotUploadTask, error) {
	if strings.TrimSpace(localPath) == "" {
		return nil, nil
	}
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+moviePilotUploadTaskCols+` FROM movie_pilot_upload_tasks
		 WHERE local_path=? AND torrent_hash<>? ORDER BY id DESC LIMIT 1`, localPath, excludeHash)
	t, err := scanMoviePilotUploadTask(row)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

func (r *moviePilotRepo) ListUploadTasks(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotUploadTask, int64, error) {
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
	if err := r.db.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM movie_pilot_upload_tasks`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB(err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+moviePilotUploadTaskCols+` FROM movie_pilot_upload_tasks`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, wrapDB(err)
	}
	defer rows.Close()
	out, err := scanMoviePilotUploadTasks(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *moviePilotRepo) ListUploadTasksByStatus(ctx context.Context, statuses ...string) ([]domain.MoviePilotUploadTask, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, 0, len(statuses))
	for _, s := range statuses {
		args = append(args, s)
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+moviePilotUploadTaskCols+` FROM movie_pilot_upload_tasks WHERE status IN (`+placeholders+`) ORDER BY id ASC`, args...)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	return scanMoviePilotUploadTasks(rows)
}

func scanMoviePilotUploadTask(row interface{ Scan(...any) error }) (*domain.MoviePilotUploadTask, error) {
	var (
		t               domain.MoviePilotUploadTask
		emptySourceNull sql.NullString
		createdNull     sql.NullString
		updatedNull     sql.NullString
	)
	err := row.Scan(&t.ID, &t.TorrentHash, &t.Title, &t.MediaType, &t.TmdbId, &t.Season, &t.LocalPath, &t.RemotePath,
		&t.Status, &t.TotalFiles, &t.UploadedFiles, &t.TotalBytes, &t.UploadedBytes, &t.Error, &emptySourceNull,
		&createdNull, &updatedNull)
	if err != nil {
		return nil, wrapDB(err)
	}
	t.EmptySourceSince = parseTSPtr(emptySourceNull)
	t.CreatedAt = parseTS(createdNull)
	t.UpdatedAt = parseTS(updatedNull)
	return &t, nil
}

func scanMoviePilotUploadTasks(rows *sql.Rows) ([]domain.MoviePilotUploadTask, error) {
	var out []domain.MoviePilotUploadTask
	for rows.Next() {
		var (
			t               domain.MoviePilotUploadTask
			emptySourceNull sql.NullString
			createdNull     sql.NullString
			updatedNull     sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.TorrentHash, &t.Title, &t.MediaType, &t.TmdbId, &t.Season, &t.LocalPath, &t.RemotePath,
			&t.Status, &t.TotalFiles, &t.UploadedFiles, &t.TotalBytes, &t.UploadedBytes, &t.Error, &emptySourceNull,
			&createdNull, &updatedNull); err != nil {
			return nil, wrapDB(err)
		}
		t.EmptySourceSince = parseTSPtr(emptySourceNull)
		t.CreatedAt = parseTS(createdNull)
		t.UpdatedAt = parseTS(updatedNull)
		out = append(out, t)
	}
	return out, wrapDB(rows.Err())
}

// ---- 识别失败文件 ----

const moviePilotFailedFileCols = `id, task_id, file_name, parent_id, root_path, account_id, status, media_type,
	title, tmdb_id, year, season, reason, created_at, updated_at`

func (r *moviePilotRepo) CreateFailedFile(ctx context.Context, f *domain.MoviePilotFailedFile) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO movie_pilot_failed_files(task_id, file_name, parent_id, root_path, account_id, status,
		 media_type, title, tmdb_id, year, season, reason)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.TaskID, f.FileName, f.ParentID, f.RootPath, f.AccountID, f.Status,
		f.MediaType, f.Title, f.TmdbId, f.Year, f.Season, f.Reason)
	if err != nil {
		return 0, wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, wrapDB(err)
	}
	f.ID = id
	return id, nil
}

func (r *moviePilotRepo) UpdateFailedFile(ctx context.Context, f *domain.MoviePilotFailedFile) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE movie_pilot_failed_files SET task_id=?, file_name=?, parent_id=?, root_path=?, account_id=?, status=?,
		 media_type=?, title=?, tmdb_id=?, year=?, season=?, reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		f.TaskID, f.FileName, f.ParentID, f.RootPath, f.AccountID, f.Status,
		f.MediaType, f.Title, f.TmdbId, f.Year, f.Season, f.Reason, f.ID)
	return wrapDB(err)
}

func (r *moviePilotRepo) GetFailedFile(ctx context.Context, id int64) (*domain.MoviePilotFailedFile, error) {
	row := r.db.read.QueryRowContext(ctx, `SELECT `+moviePilotFailedFileCols+` FROM movie_pilot_failed_files WHERE id=?`, id)
	return scanMoviePilotFailedFile(row)
}

func (r *moviePilotRepo) FindPendingFailedFile(ctx context.Context, taskID int64, fileName string) (*domain.MoviePilotFailedFile, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+moviePilotFailedFileCols+` FROM movie_pilot_failed_files
		 WHERE task_id=? AND file_name=? AND status=? ORDER BY id DESC LIMIT 1`,
		taskID, fileName, domain.MoviePilotFailedPending)
	f, err := scanMoviePilotFailedFile(row)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

func (r *moviePilotRepo) ListFailedFiles(ctx context.Context, page, pageSize int, status string) ([]domain.MoviePilotFailedFile, int64, error) {
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
	if err := r.db.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM movie_pilot_failed_files`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB(err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+moviePilotFailedFileCols+` FROM movie_pilot_failed_files`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.MoviePilotFailedFile
	for rows.Next() {
		var (
			f           domain.MoviePilotFailedFile
			createdNull sql.NullString
			updatedNull sql.NullString
		)
		if err := rows.Scan(&f.ID, &f.TaskID, &f.FileName, &f.ParentID, &f.RootPath, &f.AccountID, &f.Status,
			&f.MediaType, &f.Title, &f.TmdbId, &f.Year, &f.Season, &f.Reason, &createdNull, &updatedNull); err != nil {
			return nil, 0, wrapDB(err)
		}
		f.CreatedAt = parseTS(createdNull)
		f.UpdatedAt = parseTS(updatedNull)
		out = append(out, f)
	}
	return out, total, wrapDB(rows.Err())
}

func scanMoviePilotFailedFile(row interface{ Scan(...any) error }) (*domain.MoviePilotFailedFile, error) {
	var (
		f           domain.MoviePilotFailedFile
		createdNull sql.NullString
		updatedNull sql.NullString
	)
	err := row.Scan(&f.ID, &f.TaskID, &f.FileName, &f.ParentID, &f.RootPath, &f.AccountID, &f.Status,
		&f.MediaType, &f.Title, &f.TmdbId, &f.Year, &f.Season, &f.Reason, &createdNull, &updatedNull)
	if err != nil {
		return nil, wrapDB(err)
	}
	f.CreatedAt = parseTS(createdNull)
	f.UpdatedAt = parseTS(updatedNull)
	return &f, nil
}

// ---- 整理历史 ----

func (r *moviePilotRepo) AddOrganizeHistory(ctx context.Context, h *domain.MoviePilotOrganizeHistory) error {
	_, err := r.db.write.ExecContext(ctx,
		`INSERT INTO movie_pilot_organize_history(account_id, task_id, file_name, source_path, target_path,
		 media_type, title, year, season_num, episode_num, tmdb_id, status, message)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.AccountID, h.TaskID, h.FileName, h.SourcePath, h.TargetPath,
		h.MediaType, h.Title, h.Year, h.SeasonNum, h.EpisodeNum, h.TmdbId, h.Status, h.Message)
	return wrapDB(err)
}

func (r *moviePilotRepo) ListOrganizeHistory(ctx context.Context, limit int) ([]domain.MoviePilotOrganizeHistory, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT id, account_id, task_id, file_name, source_path, target_path, media_type, title, year,
		 season_num, episode_num, tmdb_id, status, message, created_at
		 FROM movie_pilot_organize_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []domain.MoviePilotOrganizeHistory
	for rows.Next() {
		var (
			h           domain.MoviePilotOrganizeHistory
			createdNull sql.NullString
		)
		if err := rows.Scan(&h.ID, &h.AccountID, &h.TaskID, &h.FileName, &h.SourcePath, &h.TargetPath,
			&h.MediaType, &h.Title, &h.Year, &h.SeasonNum, &h.EpisodeNum, &h.TmdbId, &h.Status, &h.Message,
			&createdNull); err != nil {
			return nil, wrapDB(err)
		}
		h.CreatedAt = parseTS(createdNull)
		out = append(out, h)
	}
	return out, wrapDB(rows.Err())
}

// ---- 内部工具 ----

// isNotFound 判断错误是否为"记录不存在"。
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if ae, ok := domain.AsAppError(err); ok {
		return ae.Code == domain.CodeNotFound
	}
	return false
}

// parseTSPtr 解析可空时间戳为 *time.Time，无效返回 nil。
func parseTSPtr(ns sql.NullString) *time.Time {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return nil
	}
	t := parseTS(ns)
	if t.IsZero() {
		return nil
	}
	return &t
}

// tsValuePtr 把 *time.Time 转为可写入的值，nil 或零值写 NULL。
func tsValuePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(tsLayout)
}
