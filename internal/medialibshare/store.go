package medialibshare

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 时间在库里统一存成 UTC 的 "2006-01-02 15:04:05"，与 internal/mediarequest 一致。
// 全仓约定（见 T09 迁移注释），换一种写法会让同一张库里出现两种时间格式，
// 按字符串比较时后者会静默给出错误结果。
const tsLayout = "2006-01-02 15:04:05"

// VisitorCountWindow 是「同一个访客多久算同一次」的窗口。
//
// 验收第 4 条：同一浏览器 24 小时内多次访问只计 1 次 visitor。
// 这个值取自 参考实现（.so 符号 + 前端 chunk，高置信但未实测），照搬而不是另设一个。
const VisitorCountWindow = 24 * time.Hour

// EventType 是分享行为流水的事件分类。
type EventType string

const (
	// EventOpen 打开分享页。
	EventOpen EventType = "open"
	// EventPlay 实际取流。计 play_count。
	EventPlay EventType = "play"
	// EventVisitor 首次访问（24 小时内去重）。计 visitor_count。
	EventVisitor EventType = "visitor"
)

// ErrNotFound 表示分享不存在、短码不匹配或已被撤销。
//
// 三种情况共用一个错误是有意的：对外不能靠错误差异区分「没有这条分享」
// 和「这条分享被撤了」，否则拿到短码的人能靠报错枚举出哪些短码有效。
var ErrNotFound = errors.New("分享不存在或已被撤销")

// Share 是一条对外分享。
type Share struct {
	ID           string
	Code         string
	AccountID    int64
	FileID       string
	Title        string
	Season       int
	Comment      string
	PasswordHash string
	ExpiresAt    *time.Time
	MaxDevices   int
	ViewCount    int
	PlayCount    int
	VisitorCount int
	CreatedBy    int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	RevokedAt    *time.Time
}

// Expired 判断分享是否已过期（永久分享永不返回 true）。
func (s Share) Expired(now time.Time) bool {
	if s.ExpiresAt == nil {
		return false
	}
	return !now.Before(*s.ExpiresAt)
}

// Revoked 判断分享是否已被撤销。
func (s Share) Revoked() bool { return s.RevokedAt != nil }

// Usable 判断分享此刻能否使用。
func (s Share) Usable(now time.Time) bool { return !s.Revoked() && !s.Expired(now) }

// HasPassword 判断这条分享是否设了访问口令。
func (s Share) HasPassword() bool { return strings.TrimSpace(s.PasswordHash) != "" }

// Visit 是一个访客在一条分享下的一次会话。
type Visit struct {
	ID         int64
	ShareID    string
	VisitorID  string
	TokenHash  string
	IPMasked   string
	UserAgent  string
	Counted    bool
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// Play 是一次实际取流。
type Play struct {
	ID          string
	ShareID     string
	VisitID     int64
	VisitorID   string
	FileID      string
	ItemLabel   string
	Method      string
	IPMasked    string
	WatchedSecs int
	StartedAt   time.Time
	LastSeenAt  time.Time
}

// Stats 是单条分享的统计，供管理端展示。
type Stats struct {
	ShareID       string
	Code          string
	Title         string
	ViewCount     int
	PlayCount     int
	VisitorCount  int
	MaxDevices    int
	ActiveDevices int
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	RecentVisits  []Visit
	RecentPlays   []Play
}

// Store 是分享功能的全部持久化。
//
// 读写分开拿句柄，与 internal/mediarequest.Store 同一个约定：
// SQLite 单写者，写句柄串行、读句柄并发。拿一个句柄当两个用会在写压力下
// 偶发 SQLITE_BUSY，而那种错只在真机上偶尔出现。
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// NewStore 构造 Store。
func NewStore(write, read *sql.DB) *Store { return &Store{write: write, read: read} }

const shareCols = `id, code, code_hash, account_id, file_id, title, season, comment,
	password_hash, expires_at, max_devices, view_count, play_count, visitor_count,
	created_by, created_at, updated_at, revoked_at`

func scanShare(sc interface{ Scan(...any) error }) (Share, error) {
	var s Share
	var expires, revoked sql.NullString
	var created, updated string
	// code_hash 是唯一索引，但这里**不读它**：它只在写入时用来比对，
	// 读出来也没有任何用途，而把它塞进 Share 结构体等于多一处可能漏脱敏的出口。
	if err := sc.Scan(&s.ID, &s.Code, &codeHashSink, &s.AccountID, &s.FileID, &s.Title, &s.Season,
		&s.Comment, &s.PasswordHash, &expires, &s.MaxDevices, &s.ViewCount, &s.PlayCount,
		&s.VisitorCount, &s.CreatedBy, &created, &updated, &revoked); err != nil {
		return Share{}, err
	}
	if t, ok := parseTS(expires); ok {
		s.ExpiresAt = &t
	}
	if t, ok := parseTS(revoked); ok {
		s.RevokedAt = &t
	}
	s.CreatedAt, _ = time.Parse(tsLayout, created)
	s.UpdatedAt, _ = time.Parse(tsLayout, updated)
	return s, nil
}

// codeHashSink 是 scan 时的占位接收者：只占位，不留存。
var codeHashSink string

// parseTS 解析可空的时间列。空串与 NULL 都当作「没有这个时间」。
func parseTS(v sql.NullString) (time.Time, bool) {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(tsLayout, v.String)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func tsOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

// Insert 新建一条分享，返回写库时的完整行（含生成的短码）。
func (s *Store) Insert(ctx context.Context, sh Share) (Share, error) {
	now := sh.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO library_shares
		 (id, code, code_hash, account_id, file_id, title, season, comment, password_hash,
		  expires_at, max_devices, created_by, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sh.ID, sh.Code, HashToken(sh.Code), sh.AccountID, sh.FileID, sh.Title, sh.Season,
		sh.Comment, sh.PasswordHash, tsOrNull(sh.ExpiresAt), sh.MaxDevices, sh.CreatedBy,
		now.UTC().Format(tsLayout), now.UTC().Format(tsLayout))
	if err != nil {
		return Share{}, fmt.Errorf("写入分享失败: %w", err)
	}
	return s.Get(ctx, sh.ID)
}

// Get 按主键取一条分享。
func (s *Store) Get(ctx context.Context, id string) (Share, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+shareCols+` FROM library_shares WHERE id=?`, id)
	return scanShare(row)
}

// GetByCodeHash 按短码哈希取一条分享。
func (s *Store) GetByCodeHash(ctx context.Context, codeHash string) (Share, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+shareCols+` FROM library_shares WHERE code_hash=?`, codeHash)
	return scanShare(row)
}

// List 列出分享，最新的在前。
func (s *Store) List(ctx context.Context, limit int) ([]Share, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+shareCols+` FROM library_shares ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanShares(rows)
}

func scanShares(rows *sql.Rows) ([]Share, error) {
	out := []Share{}
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// UpdateExpiry 改有效期。传 nil 表示改成永久分享。
//
// 写 WHERE revoked_at IS NULL 而不是无条件 UPDATE：一条已撤销的分享不该被
// 改回可用状态（撤销是不可逆的语义，不是能被顺手改回来的字段）。
func (s *Store) UpdateExpiry(ctx context.Context, id string, expiresAt *time.Time, now time.Time) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE library_shares SET expires_at=?, updated_at=? WHERE id=? AND revoked_at IS NULL`,
		tsOrNull(expiresAt), now.UTC().Format(tsLayout), id)
	if err != nil {
		return fmt.Errorf("改分享有效期失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Revoke 撤销一条分享。重复撤销不算错。
func (s *Store) Revoke(ctx context.Context, id string, now time.Time) error {
	res, err := s.write.ExecContext(ctx,
		`UPDATE library_shares SET revoked_at=?, updated_at=? WHERE id=? AND revoked_at IS NULL`,
		now.UTC().Format(tsLayout), now.UTC().Format(tsLayout), id)
	if err != nil {
		return fmt.Errorf("撤销分享失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 已经撤销过：当作成功。上层 delete 接口不该因为「重复点了一次删除」就报错。
		return nil
	}
	return nil
}
