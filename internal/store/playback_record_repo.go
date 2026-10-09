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

const playbackRecordCols = `id, rule_id, user_id, app_user_id, client, device_id, item_name, strm_path, provider, playback_at,
	request_type, request_url, original_url, user_agent, client_ip, response_status, response_time,
	storage_slug, storage_type, timestamp, item_scope, app_source, app_client_ip, app_item_name, app_strm_path, watched_seconds`

// 分页约束（与老版列表接口一致）。
const (
	playbackRecordDefaultPageSize = 30
	playbackRecordMaxPageSize     = 200
)

// Insert 落一条播放记录，返回自增 ID。空条目（无用户、无条目名、无路径）
// 视为无效事件直接跳过，避免播放链路抖动时刷出无意义记录。
//
// app_user_id 存 litepan RBAC 侧的用户 ID，0 表示未知；
// user_id 保持 0028 遗留的 TEXT 语义（Emby 侧用户标识）。
func (r *playbackRecordRepo) Insert(ctx context.Context, rec *domain.PlaybackRecord) (int64, error) {
	if rec == nil {
		return 0, nil
	}
	if strings.TrimSpace(rec.EmbyUserID) == "" && strings.TrimSpace(rec.ItemName) == "" && strings.TrimSpace(rec.StrmPath) == "" {
		return 0, nil
	}
	if rec.RuleID == "" {
		rec.RuleID = "1"
	}
	if rec.PlaybackAt == "" {
		rec.PlaybackAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// T11 新增列的默认值：storage_slug 的 115-default 是 参考实现 语义，
	// 这里改用 litepan 的默认存储驱动。
	playbackRecordFillMonitorDefaults(rec)
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO playback_records(rule_id, user_id, app_user_id, client, device_id, item_name, strm_path, provider, playback_at,
			 request_type, request_url, original_url, user_agent, client_ip, response_status, response_time,
			 storage_slug, storage_type, timestamp, item_scope, app_source, app_client_ip, app_item_name, app_strm_path, watched_seconds)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.RuleID, rec.EmbyUserID, rec.AppUserID, rec.Client, rec.DeviceID,
		rec.ItemName, rec.StrmPath, rec.Provider, rec.PlaybackAt,
		rec.RequestType, rec.RequestURL, rec.OriginalURL, rec.UserAgent, rec.ClientIP,
		rec.ResponseStatus, rec.ResponseTime, rec.StorageSlug, rec.StorageType,
		rec.Timestamp, rec.ItemScope, rec.AppSource, rec.AppClientIP, rec.AppItemName,
		rec.AppStrmPath, rec.WatchedSeconds)
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

// T11 播放监控：playback_records 新增列的存储侧常量。
const (
	// PlayTrafficDefaultStorageSlug storage_slug 的默认值。
	//
	// 参考实现 的默认值是 '115-default'（115 盘语义）。litepan 有 11 个存储驱动，
	// 照抄 115-default 会把「未指定存储」的全部记录错标成 115，
	// 所以这里改用 litepan 的默认驱动 'local'。
	PlayTrafficDefaultStorageSlug = "local"
	// PlayTrafficDefaultSource app_source 的默认值：litepan 自身的自动链路。
	PlayTrafficDefaultSource = "auto"
	// PlayTrafficRequestTypeStream request_type：流代理（字节流过自己服务器）。
	PlayTrafficRequestTypeStream = "stream"
	// PlayTrafficRequestTypeRedirect request_type：302（CDN 直连，计 0）。
	PlayTrafficRequestTypeRedirect = "redirect"
)

// playbackRecordFillMonitorDefaults 补齐 T11 新增列的默认值。
//
// storage_slug 的默认值在 参考实现 里是 115 语义的 '115-default'，
// litepan 有 11 个存储驱动，不能照抄；留空时用 litepan 的默认驱动
// （'local'），以空串表示"未指定"会让排行图和历史查询都查不出存储维度。
func playbackRecordFillMonitorDefaults(rec *domain.PlaybackRecord) {
	if strings.TrimSpace(rec.StorageSlug) == "" {
		rec.StorageSlug = PlayTrafficDefaultStorageSlug
	}
	if strings.TrimSpace(rec.Timestamp) == "" {
		rec.Timestamp = rec.PlaybackAt
	}
	if strings.TrimSpace(rec.AppSource) == "" {
		rec.AppSource = PlayTrafficDefaultSource
	}
	// request_type 是三态的存储侧真相：stream=计费中、redirect=CDN 直连。
	if strings.TrimSpace(rec.RequestType) == "" {
		rec.RequestType = PlayTrafficRequestTypeStream
	}
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

// ListRange 按时间倒序取出区间内的**全部**记录，不分页。
// 观影报告汇总用：报告要在内存里把中断超过 gapMinutes 的相邻记录
// 拆成不同的「一次播放」，所以必须一次拿全，不能靠 SQL 分页。
func (r *playbackRecordRepo) ListRange(ctx context.Context, q domain.PlaybackRecordQuery) ([]domain.PlaybackRecord, error) {
	q = normalizePlaybackRecordQuery(q)
	where, args := playbackRecordWhere(q)
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+playbackRecordCols+` FROM playback_records`+where+`
		 ORDER BY playback_at ASC, id ASC`, args...)
	if err != nil {
		return nil, wrapDB(err)
	}
	return r.collect(rows)
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
		if err := rows.Scan(&rec.ID, &rec.RuleID, &rec.EmbyUserID, &rec.AppUserID, &rec.Client, &rec.DeviceID,
			&rec.ItemName, &rec.StrmPath, &rec.Provider, &rec.PlaybackAt,
			&rec.RequestType, &rec.RequestURL, &rec.OriginalURL, &rec.UserAgent, &rec.ClientIP,
			&rec.ResponseStatus, &rec.ResponseTime, &rec.StorageSlug, &rec.StorageType,
			&rec.Timestamp, &rec.ItemScope, &rec.AppSource, &rec.AppClientIP, &rec.AppItemName,
			&rec.AppStrmPath, &rec.WatchedSeconds); err != nil {
			return nil, wrapDB(err)
		}
		// 三态由 request_type 推导，避免库里存两套可能矛盾的标记。
		rec.State = domain.PlayStateFromRequestType(rec.RequestType)
		rec.Metered = rec.State.Metered()
		out = append(out, rec)
	}
	return out, wrapDB(rows.Err())
}

func normalizePlaybackRecordQuery(q domain.PlaybackRecordQuery) domain.PlaybackRecordQuery {
	q.RuleID = strings.TrimSpace(q.RuleID)
	q.UserID = strings.TrimSpace(q.UserID)
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Provider = strings.TrimSpace(q.Provider)
	q.AppSource = strings.TrimSpace(q.AppSource)
	q.Since = strings.TrimSpace(q.Since)
	q.Until = strings.TrimSpace(q.Until)
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
	// app_user_id 是 RBAC 侧的用户 ID，与上面的 Emby 侧标识互不干扰：
	// 两个条件都是"填了就筛"，同时填则取交集。
	if q.AppUserID > 0 {
		conds = append(conds, "app_user_id = ?")
		args = append(args, q.AppUserID)
	}
	if q.Provider != "" {
		conds = append(conds, "provider = ?")
		args = append(args, q.Provider)
	}
	if q.AppSource != "" {
		conds = append(conds, "app_source = ?")
		args = append(args, q.AppSource)
	}
	// 三态筛选用 request_type 落地：redirect→CDN 直连，其余→计费中。
	// 历史记录里判不出局域网（那是实时会话阶段拿客户端 IP 才知道的）。
	if q.State.Valid() {
		if q.State == domain.PlayStateCDN {
			conds = append(conds, "request_type = ?")
			args = append(args, PlayTrafficRequestTypeRedirect)
		} else if q.State == domain.PlayStateMetered {
			conds = append(conds, "request_type != ?")
			args = append(args, PlayTrafficRequestTypeRedirect)
		}
	}
	if q.Since != "" {
		conds = append(conds, "playback_at >= ?")
		args = append(args, q.Since)
	}
	if q.Until != "" {
		conds = append(conds, "playback_at <= ?")
		args = append(args, q.Until)
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
