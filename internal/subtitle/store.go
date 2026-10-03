package subtitle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 任务状态。对齐老版 models/subtitle.go 的常量，前端与 API 按这些值过滤。
const (
	TaskStatusPending     = "pending"
	TaskStatusSearching   = "searching"
	TaskStatusMatching    = "matching"
	TaskStatusDownloading = "downloading"
	TaskStatusSyncing     = "syncing"
	TaskStatusDone        = "done"
	TaskStatusFailed      = "failed"
	TaskStatusSkipped     = "skipped"
)

// IsValidTaskStatus 判断状态是否在白名单内（API 层过滤参数校验用）。
func IsValidTaskStatus(status string) bool {
	switch status {
	case TaskStatusPending, TaskStatusSearching, TaskStatusMatching,
		TaskStatusDownloading, TaskStatusSyncing, TaskStatusDone,
		TaskStatusFailed, TaskStatusSkipped:
		return true
	}
	return false
}

// Task 是一条字幕处理记录。
// 对齐老版 models.SubtitleTask：重复处理同一视频时复用同一行覆盖更新，而不是追加新行。
type Task struct {
	ID             uint    `json:"id"`
	MediaID        uint    `json:"media_id"`
	VideoPath      string  `json:"video_path"`
	SubtitlePath   string  `json:"subtitle_path"`
	Status         string  `json:"status"`
	Title          string  `json:"title"`
	Year           int     `json:"year"`
	Season         int     `json:"season"`
	Episode        int     `json:"episode"`
	MediaType      string  `json:"media_type"`
	Provider       string  `json:"provider"`
	CandidateSlug  string  `json:"candidate_slug"`
	MatchScore     int     `json:"match_score"`
	MatchReason    string  `json:"match_reason"`
	CandidateJSON  string  `json:"candidate_json"`
	SyncOffsetMs   int64   `json:"sync_offset_ms"`
	SyncScale      float64 `json:"sync_scale"`
	SyncConfidence float64 `json:"sync_confidence"`
	SyncApplied    bool    `json:"sync_applied"`
	ErrorMessage   string  `json:"error_message"`
	DurationMs     int64   `json:"duration_ms"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

const taskColumns = `id, media_id, video_path, subtitle_path, status, title, year, season, episode,
	media_type, provider, candidate_slug, match_score, match_reason, candidate_json,
	sync_offset_ms, sync_scale, sync_confidence, sync_applied, error_message, duration_ms,
	created_at, updated_at`

// TaskStore 提供字幕任务的持久化。
//
// 老版直接复用 GORM 句柄（models 包暴露 db.Db）；现版 LitePan 的手写表统一走
// store.DB 的读写双池，因此这里接收 *sql.DB 而非 GORM。传 nil 时所有方法退化为
// 内存 no-op（返回零值与错误），使字幕逻辑能在未接数据库的场景下独立单测。
type TaskStore struct {
	write *sql.DB
	read  *sql.DB
}

// NewTaskStore 用写池/读池构造任务仓储。
func NewTaskStore(write, read *sql.DB) *TaskStore {
	if read == nil {
		read = write
	}
	return &TaskStore{write: write, read: read}
}

// ErrStoreUnavailable 表示任务仓储未接入数据库。
var ErrStoreUnavailable = errors.New("字幕任务存储不可用")

// CreateTask 插入一条新任务并回填自增 ID。
func (s *TaskStore) CreateTask(ctx context.Context, task *Task) error {
	if s == nil || s.write == nil || task == nil {
		return ErrStoreUnavailable
	}
	res, err := s.write.ExecContext(ctx,
		`INSERT INTO subtitle_tasks(media_id, video_path, subtitle_path, status, title, year, season, episode,
		 media_type, provider, candidate_slug, match_score, match_reason, candidate_json,
		 sync_offset_ms, sync_scale, sync_confidence, sync_applied, error_message, duration_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.MediaID, task.VideoPath, task.SubtitlePath, task.Status, task.Title, task.Year, task.Season, task.Episode,
		task.MediaType, task.Provider, task.CandidateSlug, task.MatchScore, task.MatchReason, task.CandidateJSON,
		task.SyncOffsetMs, task.SyncScale, task.SyncConfidence, boolToInt(task.SyncApplied), task.ErrorMessage, task.DurationMs)
	if err != nil {
		return fmt.Errorf("创建字幕任务失败：%w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取字幕任务 ID 失败：%w", err)
	}
	task.ID = uint(id)
	return nil
}

// SaveTask 按主键覆盖式更新任务。ID 为 0 时退化为插入。
func (s *TaskStore) SaveTask(ctx context.Context, task *Task) error {
	if s == nil || s.write == nil || task == nil {
		return ErrStoreUnavailable
	}
	if task.ID == 0 {
		return s.CreateTask(ctx, task)
	}
	_, err := s.write.ExecContext(ctx,
		`UPDATE subtitle_tasks SET media_id=?, video_path=?, subtitle_path=?, status=?, title=?, year=?,
		 season=?, episode=?, media_type=?, provider=?, candidate_slug=?, match_score=?, match_reason=?,
		 candidate_json=?, sync_offset_ms=?, sync_scale=?, sync_confidence=?, sync_applied=?,
		 error_message=?, duration_ms=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		task.MediaID, task.VideoPath, task.SubtitlePath, task.Status, task.Title, task.Year,
		task.Season, task.Episode, task.MediaType, task.Provider, task.CandidateSlug, task.MatchScore, task.MatchReason,
		task.CandidateJSON, task.SyncOffsetMs, task.SyncScale, task.SyncConfidence, boolToInt(task.SyncApplied),
		task.ErrorMessage, task.DurationMs, task.ID)
	if err != nil {
		return fmt.Errorf("更新字幕任务失败：%w", err)
	}
	return nil
}

// FindTaskByVideoPath 取某视频最近一条任务。
func (s *TaskStore) FindTaskByVideoPath(ctx context.Context, videoPath string) (*Task, bool) {
	if s == nil || s.read == nil {
		return nil, false
	}
	row := s.read.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM subtitle_tasks WHERE video_path = ? ORDER BY id DESC LIMIT 1`, videoPath)
	task, err := scanTask(row)
	if err != nil {
		return nil, false
	}
	return task, true
}

// GetTask 按 ID 取任务。
func (s *TaskStore) GetTask(ctx context.Context, id uint) (*Task, bool) {
	if s == nil || s.read == nil {
		return nil, false
	}
	row := s.read.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM subtitle_tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if err != nil {
		return nil, false
	}
	return task, true
}

// ListTasks 分页列出任务。status 为空表示不过滤。
func (s *TaskStore) ListTasks(ctx context.Context, page, pageSize int, status string) ([]*Task, int64) {
	if s == nil || s.read == nil {
		return nil, 0
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	where := ""
	var args []any
	if status != "" {
		where = " WHERE status = ?"
		args = append(args, status)
	}

	var total int64
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(1) FROM subtitle_tasks`+where, args...).Scan(&total); err != nil {
		return nil, 0
	}

	query := `SELECT ` + taskColumns + ` FROM subtitle_tasks` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.read.QueryContext(ctx, query, append(append([]any{}, args...), pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, total
	}
	defer rows.Close()

	var out []*Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return out, total
		}
		out = append(out, task)
	}
	return out, total
}

// DeleteTask 删除任务，返回是否真的删掉了行。
func (s *TaskStore) DeleteTask(ctx context.Context, id uint) bool {
	if s == nil || s.write == nil {
		return false
	}
	res, err := s.write.ExecContext(ctx, `DELETE FROM subtitle_tasks WHERE id = ?`, id)
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	return err == nil && n > 0
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的公共 Scan 方法。
type scanner interface{ Scan(dest ...any) error }

func scanTask(sc scanner) (*Task, error) {
	var (
		t           Task
		syncApplied int
		createdAt   sql.NullString
		updatedAt   sql.NullString
	)
	if err := sc.Scan(
		&t.ID, &t.MediaID, &t.VideoPath, &t.SubtitlePath, &t.Status, &t.Title, &t.Year, &t.Season, &t.Episode,
		&t.MediaType, &t.Provider, &t.CandidateSlug, &t.MatchScore, &t.MatchReason, &t.CandidateJSON,
		&t.SyncOffsetMs, &t.SyncScale, &t.SyncConfidence, &syncApplied, &t.ErrorMessage, &t.DurationMs,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	t.SyncApplied = syncApplied != 0
	t.CreatedAt = normalizeDBTime(createdAt)
	t.UpdatedAt = normalizeDBTime(updatedAt)
	return &t, nil
}

// normalizeDBTime 把 SQLite 的 CURRENT_TIMESTAMP 文本统一成 RFC3339（UTC）。
// 与 LitePan 其它手写表一致：库里存的是 "2006-01-02 15:04:05"。
func normalizeDBTime(v sql.NullString) string {
	if !v.Valid || v.String == "" {
		return ""
	}
	layouts := []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, v.String); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return v.String
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
