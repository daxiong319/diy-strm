package store

import (
	"context"
	"database/sql"
	"errors"

	"litepan/internal/domain"
)

type embyRefreshTaskRepo struct{ db *DB }

const embyRefreshTaskCols = `id, task_key, library_id, library_name, target_type, item_ids, status,
	last_event_at, refresh_after_at, deadline_at, last_checked_at, last_refresh_at, error, created_at, updated_at`

// UpsertEvent 登记一次刷新事件。存在同 key 任务且仍为 pending 时顺延防抖窗口并合并
// item_ids；若任务处于 refreshing/终态，则重新激活为 pending 并重置窗口。
func (r *embyRefreshTaskRepo) UpsertEvent(ctx context.Context, task *domain.EmbyRefreshTask) (*domain.EmbyRefreshTask, error) {
	if task == nil {
		return nil, nil
	}
	existing, ok, err := r.GetByKey(ctx, task.TaskKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		_, err := r.db.write.ExecContext(ctx,
			`INSERT INTO emby_refresh_tasks(task_key, library_id, library_name, target_type, item_ids, status,
				last_event_at, refresh_after_at, deadline_at, last_checked_at, last_refresh_at, error)
			 VALUES (?,?,?,?,?,?,?,?,?,0,0,'')`,
			task.TaskKey, task.LibraryID, task.LibraryName, task.TargetType, task.ItemIDs,
			task.Status, task.LastEventAt, task.RefreshAfter, task.DeadlineAt)
		if err != nil {
			return nil, wrapDB(err)
		}
		saved, _, err := r.GetByKey(ctx, task.TaskKey)
		return saved, err
	}
	if err := r.applyEvent(ctx, existing, task); err != nil {
		return nil, err
	}
	saved, _, err := r.GetByKey(ctx, task.TaskKey)
	return saved, err
}

// applyEvent 把一次新事件合并进已有任务。
func (r *embyRefreshTaskRepo) applyEvent(ctx context.Context, existing, incoming *domain.EmbyRefreshTask) error {
	mergedLibraryID := existing.LibraryID
	if mergedLibraryID == "" {
		mergedLibraryID = incoming.LibraryID
	}
	mergedLibraryName := existing.LibraryName
	if incoming.LibraryName != "" {
		mergedLibraryName = incoming.LibraryName
	}
	itemIDs := mergeItemIDsJSON(existing.ItemIDs, incoming.ItemIDs)
	// 截止时间锚定首次事件，不随新事件顺延；否则持续到达的事件会让任务永不超时。
	deadlineAt := existing.DeadlineAt
	if deadlineAt == 0 {
		deadlineAt = incoming.DeadlineAt
	}
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE emby_refresh_tasks
		 SET library_id=?, library_name=?, target_type=?, item_ids=?, status=?,
		     last_event_at=?, refresh_after_at=?, deadline_at=?, last_checked_at=0, error='',
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		mergedLibraryID, mergedLibraryName, incoming.TargetType, itemIDs, domain.EmbyRefreshStatusPending,
		incoming.LastEventAt, incoming.RefreshAfter, deadlineAt, existing.ID)
	return wrapDB(err)
}

func (r *embyRefreshTaskRepo) GetByKey(ctx context.Context, taskKey string) (*domain.EmbyRefreshTask, bool, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+embyRefreshTaskCols+` FROM emby_refresh_tasks WHERE task_key=?`, taskKey)
	t, err := scanEmbyRefreshTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, wrapDB(err)
	}
	return t, true, nil
}

func (r *embyRefreshTaskRepo) ListReady(ctx context.Context, now int64, limit int) ([]*domain.EmbyRefreshTask, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+embyRefreshTaskCols+` FROM emby_refresh_tasks
		 WHERE status=? AND refresh_after_at<=? AND (deadline_at=0 OR deadline_at>?)
		 ORDER BY refresh_after_at ASC, id ASC LIMIT ?`,
		domain.EmbyRefreshStatusPending, now, now, limit)
	if err != nil {
		return nil, wrapDB(err)
	}
	return r.collect(rows)
}

func (r *embyRefreshTaskRepo) EarliestDebounce(ctx context.Context, now int64) (int64, bool, error) {
	var at int64
	err := r.db.read.QueryRowContext(ctx,
		`SELECT MIN(refresh_after_at) FROM emby_refresh_tasks
		 WHERE status=? AND refresh_after_at>? AND (deadline_at=0 OR deadline_at>?)`,
		domain.EmbyRefreshStatusPending, now, now).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, wrapDB(err)
	}
	if at == 0 {
		return 0, false, nil
	}
	return at, true, nil
}

// Claim 是 CAS 抢占：只有仍处于 pending 的任务才能被本次扫描认领。
func (r *embyRefreshTaskRepo) Claim(ctx context.Context, id int64) (bool, error) {
	res, err := r.db.write.ExecContext(ctx,
		`UPDATE emby_refresh_tasks SET status=?, last_checked_at=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND status=?`,
		domain.EmbyRefreshStatusRefreshing, nowUnix(), id, domain.EmbyRefreshStatusPending)
	if err != nil {
		return false, wrapDB(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, wrapDB(err)
	}
	return n == 1, nil
}

func (r *embyRefreshTaskRepo) Complete(ctx context.Context, id int64, status, errMessage string, refreshedAt int64) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE emby_refresh_tasks SET status=?, error=?, last_refresh_at=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		status, errMessage, refreshedAt, id)
	return wrapDB(err)
}

func (r *embyRefreshTaskRepo) CancelExpired(ctx context.Context, now int64) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`UPDATE emby_refresh_tasks SET status=?, error=?, updated_at=CURRENT_TIMESTAMP
		 WHERE status=? AND deadline_at>0 AND deadline_at<=?`,
		domain.EmbyRefreshStatusCancelled, "等待超过最长刷新时间，已放弃", domain.EmbyRefreshStatusPending, now)
	if err != nil {
		return 0, wrapDB(err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB(err)
}

// ListReadyItems 返回已到点的 pending item 级任务，用于按媒体库合并。
func (r *embyRefreshTaskRepo) ListReadyItems(ctx context.Context, now int64, limit int) ([]*domain.EmbyRefreshTask, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+embyRefreshTaskCols+` FROM emby_refresh_tasks
		 WHERE status=? AND target_type=? AND refresh_after_at<=? AND (deadline_at=0 OR deadline_at>?)
		 ORDER BY library_id ASC, id ASC LIMIT ?`,
		domain.EmbyRefreshStatusPending, domain.EmbyRefreshTargetItem, now, now, limit)
	if err != nil {
		return nil, wrapDB(err)
	}
	return r.collect(rows)
}

// AbsorbItemsIntoLibrary 在一个事务内合并条目并取消被吸收的 item 任务。
func (r *embyRefreshTaskRepo) AbsorbItemsIntoLibrary(ctx context.Context, libraryTask *domain.EmbyRefreshTask, itemTasks []*domain.EmbyRefreshTask, now int64) (int, error) {
	if libraryTask == nil || len(itemTasks) == 0 {
		return 0, nil
	}
	tx, err := r.db.write.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapDB(err)
	}
	defer func() { _ = tx.Rollback() }()

	merged := libraryTask.ItemIDs
	absorbed := 0
	for _, item := range itemTasks {
		if item == nil || item.ID == 0 {
			continue
		}
		res, execErr := tx.ExecContext(ctx,
			`UPDATE emby_refresh_tasks SET status=?, error=?, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND status=? AND target_type=?`,
			domain.EmbyRefreshStatusCancelled,
			"已由媒体库刷新任务覆盖",
			item.ID, domain.EmbyRefreshStatusPending, domain.EmbyRefreshTargetItem)
		if execErr != nil {
			return 0, wrapDB(execErr)
		}
		n, execErr := res.RowsAffected()
		if execErr != nil {
			return 0, wrapDB(execErr)
		}
		// 只有真正被本次抢占的 item 任务才并入 item_ids，避免并入已被其它路径处理的任务。
		if n != 1 {
			continue
		}
		merged = mergeItemIDsJSON(merged, item.ItemIDs)
		absorbed++
	}
	if absorbed == 0 {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE emby_refresh_tasks SET item_ids=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		merged, libraryTask.ID); err != nil {
		return 0, wrapDB(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, wrapDB(err)
	}
	return absorbed, nil
}

func (r *embyRefreshTaskRepo) collect(rows *sql.Rows) ([]*domain.EmbyRefreshTask, error) {
	defer rows.Close()
	var out []*domain.EmbyRefreshTask
	for rows.Next() {
		t, err := scanEmbyRefreshTask(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, t)
	}
	return out, wrapDB(rows.Err())
}

func scanEmbyRefreshTask(s interface{ Scan(...any) error }) (*domain.EmbyRefreshTask, error) {
	var (
		t           domain.EmbyRefreshTask
		createdNull sql.NullString
		updatedNull sql.NullString
	)
	if err := s.Scan(&t.ID, &t.TaskKey, &t.LibraryID, &t.LibraryName, &t.TargetType, &t.ItemIDs,
		&t.Status, &t.LastEventAt, &t.RefreshAfter, &t.DeadlineAt, &t.LastCheckedAt,
		&t.LastRefreshAt, &t.Error, &createdNull, &updatedNull); err != nil {
		return nil, err
	}
	t.CreatedAt = parseTS(createdNull)
	t.UpdatedAt = parseTS(updatedNull)
	return &t, nil
}
