// Package mediarequest 实现「求片中心」（参考实现 移植⑨）。
//
// 这是 litepan 从「单人工具」变成「家庭影音中枢」的入口：家里人不用打开管理台，
// 在手机上登录一个只在手机上打开的求片站，搜一下、点一下，剩下的交给 T03~T05
// 已经建好的订阅流水线。
//
// 文件分工：
//   - store.go   四张表的手写 CRUD（database/sql，理由同 internal/rbac/store.go）
//   - tags.go    入库标签的清洗与上限
//   - service.go 提交 / 审核 / 建订阅 / 规则 / 统计
//   - session.go 求片站自己的会话签名
//   - listener.go 独立监听口的起停（受 mo_media_request_enabled 控制）
//
// ⚠️ 本包**不另起一条订阅执行路径**：审核通过后走的是
// discovery.SaveSubscription，后续检查、转存、入库全部复用既有流水线。
package mediarequest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tsLayout 与 SQLite CURRENT_TIMESTAMP 的文本格式一致（UTC）。
const tsLayout = "2006-01-02 15:04:05"

// Status 求片单状态。
type Status string

const (
	// StatusPending 待审。审核关时不会出现这个状态（提交即 approved）。
	StatusPending Status = "pending"
	// StatusApproved 已通过且已建成订阅。
	StatusApproved Status = "approved"
	// StatusRejected 被驳回。驳回不是终局 —— 可以再求同一部。
	StatusRejected Status = "rejected"
	// StatusFulfilled 订阅已跑完、东西入库了。
	StatusFulfilled Status = "fulfilled"
)

func validStatus(s Status) bool {
	switch s {
	case StatusPending, StatusApproved, StatusRejected, StatusFulfilled:
		return true
	}
	return false
}

// Request 一条求片单。
//
// 注意 RequesterID = 0 表示「提交者是超管」：超管在 rbac_users 里没有行
// （T08 把超管虚拟化了），所以这里沿用同一个哨兵而不是 NULL。
type Request struct {
	ID            int64
	RequesterID   int64
	RequesterName string
	TMDBID        int64
	Title         string
	OriginalTitle string
	MediaType     string
	Season        int
	Year          int
	PosterURL     string
	Status        Status
	Notes         string
	Tags          []string
	CreatedAt     time.Time
	ReviewedAt    time.Time
	ReviewerID    int64
	ReviewerName  string
	RejectReason  string
	ReviewedNote  string
	// SubscriptionID 指向 media_requests 之外那条 discovery_subscriptions.id。
	SubscriptionID int64
}

// Tag 入库标签（整部剧维度，见 migrations/0041_media_request.sql 的说明）。
type Tag struct {
	TMDBID      int64
	MediaType   string
	RequesterID int64
	Tag         string
	CreatedAt   time.Time
}

// Rule 求片规则：上限与自动通过这类策略。
//
// 0 在 daily_limit / pending_limit 上表示「不限」，不是「一次都不能求」——
// 这两个字段的 0 语义如果按字面理解，管理员新建一条默认规则会把所有人求片的路堵死，
// 而配置里的默认值是「不限」。
type Rule struct {
	ID              int64
	Name            string
	MediaType       string
	DailyLimit      int
	PendingLimit    int
	AutoApprove     bool
	AppliesToUserID int64
	Enabled         bool
	Priority        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// AnalyticsRow 求片统计（一天 × 一个人 × 一种类型一行）。
type AnalyticsRow struct {
	Day            string
	RequesterID    int64
	MediaType      string
	SubmittedCount int
	ApprovedCount  int
	RejectedCount  int
	FulfilledCount int
}

// Store 四张表的手写 CRUD。
//
// 用 database/sql 而不是 GORM，与 internal/rbac/store.go 同理由：这几张表有
// partial unique index、UPSERT 累加统计和「按 status 取一段」这些形状，
// 交给 ORM 只能绕。写句柄缺失时构造一个只读 Store（装配顺序出问题时能看清是什么问题）。
type Store struct {
	write *sql.DB
	read  *sql.DB
}

// NewStore 构造 Store。read 为 nil 时复用 write。
func NewStore(write, read *sql.DB) *Store {
	if read == nil {
		read = write
	}
	return &Store{write: write, read: read}
}

const requestColumns = `id, requester_id, requester_name, tmdb_id, title, original_title,
	media_type, season, year, poster_url, status, notes, tags, created_at,
	reviewed_at, reviewer_id, reviewer_name, reject_reason, reviewed_note, subscription_id`

func scanRequest(rows interface{ Scan(...any) error }) (*Request, error) {
	var (
		r          Request
		status     string
		tagsRaw    string
		createdAt  sql.NullString
		reviewedAt sql.NullString
		reviewerID sql.NullInt64
		subID      sql.NullInt64
	)
	if err := rows.Scan(&r.ID, &r.RequesterID, &r.RequesterName, &r.TMDBID, &r.Title, &r.OriginalTitle,
		&r.MediaType, &r.Season, &r.Year, &r.PosterURL, &status, &r.Notes, &tagsRaw, &createdAt,
		&reviewedAt, &reviewerID, &r.ReviewerName, &r.RejectReason, &r.ReviewedNote, &subID); err != nil {
		return nil, err
	}
	r.Status = Status(status)
	r.Tags = decodeTags(tagsRaw)
	r.CreatedAt = parseTS(createdAt)
	r.ReviewedAt = parseTS(reviewedAt)
	r.ReviewerID = reviewerID.Int64
	r.SubscriptionID = subID.Int64
	return &r, nil
}

// Insert 写入一条求片单，返回新行 id。
func (s *Store) Insert(ctx context.Context, r *Request) (int64, error) {
	if s.write == nil {
		return 0, errors.New("求片存储未配置写句柄")
	}
	created := r.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	res, err := s.write.ExecContext(ctx, `INSERT INTO media_requests(
		requester_id, requester_name, tmdb_id, title, original_title, media_type, season,
		year, poster_url, status, notes, tags, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RequesterID, r.RequesterName, r.TMDBID, r.Title, r.OriginalTitle, r.MediaType, r.Season,
		r.Year, r.PosterURL, string(r.Status), r.Notes, encodeTags(r.Tags), tsValue(created))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateReview 写审核结论。
//
// 只改审核相关列，不碰请求本身 —— 提交者不能在审核时改掉自己求的是什么，
// 这条边界用「更新语句里不出现 title/tmdb_id/media_type」来保证。
func (s *Store) UpdateReview(ctx context.Context, r *Request) error {
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	var reviewerID any
	if r.ReviewerID > 0 || r.ReviewerName != "" {
		reviewerID = r.ReviewerID
	}
	res, err := s.write.ExecContext(ctx,
		`UPDATE media_requests SET status=?, reviewed_at=?, reviewer_id=?, reviewer_name=?,
		 reject_reason=?, reviewed_note=?, subscription_id=?
		 WHERE id=? AND status='pending'`,
		string(r.Status), tsValue(r.ReviewedAt), reviewerID, r.ReviewerName,
		r.RejectReason, r.ReviewedNote, nullInt(r.SubscriptionID), r.ID)
	if err != nil {
		return err
	}
	// RowsAffected 为 0 有两种可能：这一行不存在，或者已经被别人审过了。
	// 两者都不能报「成功」—— 覆盖掉同事的审核结论比报错糟糕得多。
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotPending
	}
	return nil
}

// MarkFulfilled 把已通过的求片标记成「东西入库了」。
//
// 订阅跑完之后由对账器调用（见 service.go 的 Reconcile）。它是**对账**不是驱动：
// 订阅本身照常按自己的周期跑，不因为多了一行求片就多跑一次。
func (s *Store) MarkFulfilled(ctx context.Context, id, subscriptionID int64) error {
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	res, err := s.write.ExecContext(ctx,
		`UPDATE media_requests SET status=?, subscription_id=? WHERE id=? AND status='approved'`,
		string(StatusFulfilled), nullInt(subscriptionID), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotApproved
	}
	return nil
}

// AttachSubscriptionID 单独回写 subscription_id。
//
// 与 UpdateReview 分开是因为补建订阅（Reconcile）发生在**审核结论已经落库之后**：
// 那时 status 已经不是 pending 了，走 UpdateReview 会被 `AND status='pending'`
// 挡下来，于是订阅建成了却关联不上，求片单永远停在「已通过但没订阅」。
func (s *Store) AttachSubscriptionID(ctx context.Context, id, subscriptionID int64) error {
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	res, err := s.write.ExecContext(ctx,
		`UPDATE media_requests SET subscription_id=? WHERE id=? AND subscription_id IS NULL`,
		nullInt(subscriptionID), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 已经关联过了：不算错（并发对账时两个 worker 可能同时捡到同一条），
		// 但也不该静默 —— 返回一个调用方能识别的信号。
		return ErrAlreadyLinked
	}
	return nil
}

// ListApprovedWithSubscription 取「已通过且已关联订阅」的单（对账入库用）。
func (s *Store) ListApprovedWithSubscription(ctx context.Context, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests
		 WHERE status='approved' AND subscription_id IS NOT NULL
		 ORDER BY reviewed_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// ListByStatus 按状态取求片单，最新的在前。
func (s *Store) ListByStatus(ctx context.Context, status Status, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if !validStatus(status) {
		return nil, errors.New("不支持的状态：" + string(status))
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests WHERE status=?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, string(status), limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// ListAll 取全部求片单，最新的在前。
//
// 管理台历史页用。走参数化查询而不是拼串：status 是空还是具体值由
// validStatus 决定，**不让调用方把任意串带进 WHERE**。
func (s *Store) ListAll(ctx context.Context, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests
		 ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// Get 取一条求片单。
func (s *Store) Get(ctx context.Context, id int64) (*Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	row := s.read.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM media_requests WHERE id=?`, id)
	return scanRequest(row)
}

// ListPending 取待审队列，按创建时间正序（先到先审）。
func (s *Store) ListPending(ctx context.Context, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests WHERE status='pending'
		 ORDER BY created_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// ListMine 取某人自己提过的求片，按时间倒序。
func (s *Store) ListMine(ctx context.Context, requesterID int64, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests WHERE requester_id=?
		 ORDER BY created_at DESC, id DESC LIMIT ?`, requesterID, limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// ListApprovedWithoutSubscription 取「已通过但还没接上订阅」的单。
//
// 这是对账器要的那一格：审核时建订阅失败（没配保存目录、网络抖动）不该让这条求片
// 永远卡在没有订阅的状态里。
func (s *Store) ListApprovedWithoutSubscription(ctx context.Context, limit int) ([]Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests
		 WHERE status='approved' AND subscription_id IS NULL
		 ORDER BY reviewed_at ASC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collectRequests(rows)
}

// CountSince 数某人从 since 起提交了多少条（每天上限用）。
//
// 走 idx_media_requests_requester(requester_id, created_at)：
// 先按人筛再按时间筛，正好是这个索引的形状。
func (s *Store) CountSince(ctx context.Context, requesterID int64, since time.Time) (int, error) {
	if s.read == nil {
		return 0, errors.New("求片存储未配置读句柄")
	}
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_requests WHERE requester_id=? AND created_at >= ?`,
		requesterID, tsValue(since)).Scan(&n)
	return n, err
}

// CountPendingByRequester 数某人当前有几条待审（同时待审上限用）。
func (s *Store) CountPendingByRequester(ctx context.Context, requesterID int64) (int, error) {
	if s.read == nil {
		return 0, errors.New("求片存储未配置读句柄")
	}
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_requests WHERE requester_id=? AND status='pending'`,
		requesterID).Scan(&n)
	return n, err
}

// PendingByIdentity 取某作品当前的待审单（判重提示用；不存在返回 nil,nil）。
func (s *Store) PendingByIdentity(ctx context.Context, tmdbID int64, mediaType string) (*Request, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	row := s.read.QueryRowContext(ctx,
		`SELECT `+requestColumns+` FROM media_requests
		 WHERE tmdb_id=? AND media_type=? AND status='pending' LIMIT 1`, tmdbID, mediaType)
	r, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ---------------------------------------------------------------------------
// 标签
// ---------------------------------------------------------------------------

// UpsertTags 把一个人对一部作品的标签写成给定的集合。
//
// 语义是「替换」而不是「追加」：用户在手机上看到一串标签勾选后提交，
// 界面上显示的最终结果必须等于提交的这串。追加的话用户永远删不掉一个标签 ——
// 没有删除入口的标签集合，20 个的额度用完就等于永久锁死这部作品。
// 所以缺失的旧标签走 DELETE，多出来的新标签走 INSERT OR IGNORE。
func (s *Store) UpsertTags(ctx context.Context, tmdbID int64, mediaType string, requesterID int64, tags []string) error {
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM media_request_tags WHERE tmdb_id=? AND media_type=? AND requester_id=?`,
		tmdbID, mediaType, requesterID); err != nil {
		return err
	}
	now := tsValue(time.Now().UTC())
	for _, tag := range tags {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO media_request_tags(tmdb_id, media_type, requester_id, tag, created_at)
			 VALUES(?,?,?,?,?)`, tmdbID, mediaType, requesterID, tag, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTags 取某作品全部人的标签（入库侧读这个）。
func (s *Store) ListTags(ctx context.Context, tmdbID int64, mediaType string) ([]Tag, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT tmdb_id, media_type, requester_id, tag, created_at FROM media_request_tags
		 WHERE tmdb_id=? AND media_type=? ORDER BY id ASC`, tmdbID, mediaType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		var created sql.NullString
		if err := rows.Scan(&t.TMDBID, &t.MediaType, &t.RequesterID, &t.Tag, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTS(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTagsOfUser 取某个人对某作品的标签。
func (s *Store) ListTagsOfUser(ctx context.Context, tmdbID int64, mediaType string, requesterID int64) ([]string, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT tag FROM media_request_tags
		 WHERE tmdb_id=? AND media_type=? AND requester_id=? ORDER BY id ASC`,
		tmdbID, mediaType, requesterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		out = append(out, tag)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// 规则
// ---------------------------------------------------------------------------

// ListRules 取规则。enabledOnly 时只取启用的。
//
// 刻意不分页也不排序之外加过滤：调用方（service.go 的 resolveRule）在内存里按
// media_type / applies_to_user_id 挑命中的那一条，规则是几十条的个人级别数据。
func (s *Store) ListRules(ctx context.Context, enabledOnly bool) ([]Rule, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	q := `SELECT id, name, media_type, daily_limit, pending_limit, auto_approve,
		applies_to_user_id, enabled, priority, created_at, updated_at FROM media_request_rules`
	if enabledOnly {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY priority DESC, id DESC`
	rows, err := s.read.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var (
			r       Rule
			applies sql.NullInt64
			created sql.NullString
			updated sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.MediaType, &r.DailyLimit, &r.PendingLimit, &r.AutoApprove,
			&applies, &r.Enabled, &r.Priority, &created, &updated); err != nil {
			return nil, err
		}
		r.AppliesToUserID = applies.Int64
		r.CreatedAt = parseTS(created)
		r.UpdatedAt = parseTS(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRule 新建或更新一条规则，返回 id。
func (s *Store) SaveRule(ctx context.Context, r *Rule) (int64, error) {
	if s.write == nil {
		return 0, errors.New("求片存储未配置写句柄")
	}
	var applies any
	if r.AppliesToUserID > 0 {
		applies = r.AppliesToUserID
	}
	if r.ID > 0 {
		_, err := s.write.ExecContext(ctx,
			`UPDATE media_request_rules SET name=?, media_type=?, daily_limit=?, pending_limit=?,
			 auto_approve=?, applies_to_user_id=?, enabled=?, priority=?, updated_at=CURRENT_TIMESTAMP
			 WHERE id=?`,
			r.Name, r.MediaType, r.DailyLimit, r.PendingLimit, boolInt(r.AutoApprove), applies,
			boolInt(r.Enabled), r.Priority, r.ID)
		return r.ID, err
	}
	res, err := s.write.ExecContext(ctx,
		`INSERT INTO media_request_rules(name, media_type, daily_limit, pending_limit,
		 auto_approve, applies_to_user_id, enabled, priority)
		 VALUES(?,?,?,?,?,?,?,?)`,
		r.Name, r.MediaType, r.DailyLimit, r.PendingLimit, boolInt(r.AutoApprove), applies,
		boolInt(r.Enabled), r.Priority)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteRule 删一条规则。
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	res, err := s.write.ExecContext(ctx, `DELETE FROM media_request_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// 统计
// ---------------------------------------------------------------------------

// bump 把某个计数字段 +1（UPSERT 累加）。
//
// 字段名由调用方从白名单里选，不接受任意 SQL 片段 —— 这个函数拼的是查询串，
// 让外部字符串进来就是注入口。
func (s *Store) bump(ctx context.Context, day string, requesterID int64, mediaType, column string, delta int) error {
	switch column {
	case "submitted_count", "approved_count", "rejected_count", "fulfilled_count":
	default:
		return fmt.Errorf("未知的统计列 %q", column)
	}
	if s.write == nil {
		return errors.New("求片存储未配置写句柄")
	}
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO media_request_analytics(day, requester_id, media_type, `+column+`, updated_at)
		 VALUES(?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(day, requester_id, media_type)
		 DO UPDATE SET `+column+`=`+column+`+excluded.`+column+`, updated_at=CURRENT_TIMESTAMP`,
		day, requesterID, mediaType, delta)
	return err
}

// ListAnalytics 取 [from, to] 区间内的统计（day 为本地日期 YYYY-MM-DD）。
func (s *Store) ListAnalytics(ctx context.Context, from, to string, limit int) ([]AnalyticsRow, error) {
	if s.read == nil {
		return nil, errors.New("求片存储未配置读句柄")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT day, requester_id, media_type, submitted_count, approved_count,
		 rejected_count, fulfilled_count FROM media_request_analytics
		 WHERE day >= ? AND day <= ? ORDER BY day DESC, requester_id ASC LIMIT ?`,
		from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AnalyticsRow{}
	for rows.Next() {
		var a AnalyticsRow
		if err := rows.Scan(&a.Day, &a.RequesterID, &a.MediaType, &a.SubmittedCount,
			&a.ApprovedCount, &a.RejectedCount, &a.FulfilledCount); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------

func collectRequests(rows *sql.Rows) ([]Request, error) {
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	buf, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(buf)
}

func decodeTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func parseTS(ns sql.NullString) time.Time {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return time.Time{}
	}
	raw := strings.TrimSpace(ns.String)
	for _, layout := range []string{
		tsLayout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

func tsValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(tsLayout)
}

func nullInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
