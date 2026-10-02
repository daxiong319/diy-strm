// Package playbackrecord 存储 Emby 302 反代的播放记录（对齐老版
// internal/models/emby_playback_record.go 语义）：每次播放重定向成功落一条，
// 供「播放记录」面板分页查询。
//
// 与老版差异：老版表名 emby_playback_records 且用 GORM AutoMigrate 建表；
// 现版沿用主库手写 SQL 迁移（0028_playback_records.sql）建表，读取写在
// read 连接池、写入走 write 连接池，避免与主库写连接争用。
package playbackrecord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 分页约束（与老版 ListEmbyPlaybackRecords 保持一致）。
const (
	DefaultPageSize = 30
	MaxPageSize     = 200
)

// Record 一条播放记录。
type Record struct {
	ID         int64     `json:"id"`
	RuleID     string    `json:"rule_id"`   // 反代规则 ID（老版单实例固定 "1"）
	UserID     string    `json:"user_id"`   // Emby UserId（播放请求 query）
	Client     string    `json:"client"`    // 播放端 Client 标识
	DeviceID   string    `json:"device_id"` // 播放设备 ID
	ItemName   string    `json:"item_name"` // 媒体文件名（STRM path 基名）
	StrmPath   string    `json:"strm_path"` // 完整云盘路径
	Provider   string    `json:"provider"`  // 网盘标识（115/123/guangya/baidu/139/openlist）
	PlaybackAt time.Time `json:"playback_at"`
}

// Service 播放记录读写。零值不可用，必须经 New 构造。
type Service struct {
	write *sql.DB
	read  *sql.DB
}

// New 基于主库读写连接池构造服务。两个句柄都为 nil 时所有方法安全降级
// （记录丢弃、查询返回空），以便在未接主库的测试或裁剪部署中调用方不必判空。
func New(write, read *sql.DB) *Service {
	if read == nil {
		read = write
	}
	return &Service{write: write, read: read}
}

// ErrUnavailable 主库未接入。
var ErrUnavailable = errors.New("播放记录存储不可用")

// Record 落一条播放记录。entry 为 nil 或无有效内容时直接返回；
// PlaybackAt 为零值时补当前时间；RuleID 为空时补 "1"（与老版行为一致）。
//
// 该方法不返回错误：播放链路不能因为记录落库失败而中断，
// 失败原因通过 onError 回调上报（调用方可接日志）。
func (s *Service) Record(ctx context.Context, entry *Record) {
	if entry == nil || s == nil || s.write == nil {
		return
	}
	if entry.PlaybackAt.IsZero() {
		entry.PlaybackAt = time.Now()
	}
	if entry.RuleID == "" {
		entry.RuleID = "1"
	}
	// 全空条目（无用户也无条目名）视为无效事件，不落库制造噪音。
	if entry.UserID == "" && entry.ItemName == "" && entry.StrmPath == "" {
		return
	}
	_, err := s.write.ExecContext(ctx, `
		INSERT INTO playback_records
			(rule_id, user_id, client, device_id, item_name, strm_path, provider, playback_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.RuleID, entry.UserID, entry.Client, entry.DeviceID,
		entry.ItemName, entry.StrmPath, entry.Provider, entry.PlaybackAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		s.reportError(err)
	}
}

// ListQuery 分页查询条件。
type ListQuery struct {
	RuleID   string // 反代规则 ID，空 = 全部
	UserID   string // 用户 ID，空 = 全部
	Keyword  string // 条目名/路径模糊匹配，空 = 全部
	Provider string // 网盘标识，空 = 全部
	Page     int
	PageSize int
}

// ListResult 分页查询结果。
type ListResult struct {
	Items    []Record `json:"items"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}

// List 分页查询播放记录（按播放时间倒序）。
func (s *Service) List(ctx context.Context, q ListQuery) (*ListResult, error) {
	if s == nil || s.read == nil {
		return &ListResult{Items: []Record{}, Page: 1, PageSize: DefaultPageSize}, ErrUnavailable
	}
	q = normalizeQuery(q)

	where, args := buildWhere(q)

	var total int64
	if err := s.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM playback_records"+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("统计播放记录失败: %w", err)
	}

	listArgs := append(append([]any{}, args...), q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, rule_id, user_id, client, device_id, item_name, strm_path, provider, playback_at
		FROM playback_records`+where+`
		ORDER BY playback_at DESC, id DESC
		LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("查询播放记录失败: %w", err)
	}
	defer rows.Close()

	items := make([]Record, 0, q.PageSize)
	for rows.Next() {
		var rec Record
		var playbackAt string
		if err := rows.Scan(&rec.ID, &rec.RuleID, &rec.UserID, &rec.Client, &rec.DeviceID,
			&rec.ItemName, &rec.StrmPath, &rec.Provider, &playbackAt); err != nil {
			return nil, fmt.Errorf("读取播放记录失败: %w", err)
		}
		rec.PlaybackAt = parseTime(playbackAt)
		items = append(items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取播放记录失败: %w", err)
	}
	return &ListResult{Items: items, Total: total, Page: q.Page, PageSize: q.PageSize}, nil
}

// Delete 按 ID 删除一条播放记录。不存在时返回 nil（幂等）。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if s == nil || s.write == nil {
		return ErrUnavailable
	}
	if id <= 0 {
		return fmt.Errorf("播放记录 ID 无效")
	}
	if _, err := s.write.ExecContext(ctx, "DELETE FROM playback_records WHERE id = ?", id); err != nil {
		return fmt.Errorf("删除播放记录失败: %w", err)
	}
	return nil
}

// Clear 按条件清空播放记录，返回删除条数。ruleID/userID 均为空时清空全部。
func (s *Service) Clear(ctx context.Context, ruleID, userID string) (int64, error) {
	if s == nil || s.write == nil {
		return 0, ErrUnavailable
	}
	where, args := buildWhere(ListQuery{RuleID: ruleID, UserID: userID})
	res, err := s.write.ExecContext(ctx, "DELETE FROM playback_records"+where, args...)
	if err != nil {
		return 0, fmt.Errorf("清空播放记录失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Stats 概览统计：总条数、最近一次播放时间、去重后的用户/条目数。
type Stats struct {
	Total     int64  `json:"total"`
	LastAt    string `json:"last_at"`
	UserCount int64  `json:"user_count"`
	ItemCount int64  `json:"item_count"`
}

// Stats 返回播放记录概览。
func (s *Service) Stats(ctx context.Context) (*Stats, error) {
	if s == nil || s.read == nil {
		return &Stats{}, ErrUnavailable
	}
	out := &Stats{}
	var lastAt sql.NullString
	if err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(MAX(playback_at), ''),
		       COUNT(DISTINCT user_id),
		       COUNT(DISTINCT item_name)
		FROM playback_records`).Scan(&out.Total, &lastAt, &out.UserCount, &out.ItemCount); err != nil {
		return nil, fmt.Errorf("统计播放记录失败: %w", err)
	}
	if lastAt.Valid {
		out.LastAt = lastAt.String
	}
	return out, nil
}

// —— 错误回调 ——

var (
	errMu   sync.RWMutex
	errHook func(error)
	errSeq  atomic.Int64
)

// SetErrorHook 注册落库失败回调（通常接日志）。传 nil 清除。
func SetErrorHook(fn func(error)) {
	errMu.Lock()
	defer errMu.Unlock()
	errHook = fn
	errSeq.Add(1)
}

func (s *Service) reportError(err error) {
	// 序号快照用于检测回调被并发替换；回调本身不在锁内执行，避免日志卡住写路径。
	errMu.RLock()
	fn := errHook
	seq := errSeq.Load()
	errMu.RUnlock()
	if fn == nil {
		return
	}
	errMu.RLock()
	same := errSeq.Load() == seq
	errMu.RUnlock()
	if same {
		fn(err)
	}
}

// —— 内部工具 ——

func normalizeQuery(q ListQuery) ListQuery {
	q.RuleID = strings.TrimSpace(q.RuleID)
	q.UserID = strings.TrimSpace(q.UserID)
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Provider = strings.TrimSpace(q.Provider)
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > MaxPageSize {
		q.PageSize = DefaultPageSize
	}
	return q
}

func buildWhere(q ListQuery) (string, []any) {
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

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}
