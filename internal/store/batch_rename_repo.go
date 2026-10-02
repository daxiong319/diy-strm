package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"litepan/internal/domain"
)

type renameRepo struct{ db *DB }

const renameHistoryCols = `id, user_id, name, rules, keep_ext, targets, item_count, change_count, created_at`

const renamePresetCols = `id, user_id, name, rules, keep_ext, use_count, created_at, updated_at`

// CreateHistory 写入一条批量重命名历史。
func (r *renameRepo) CreateHistory(ctx context.Context, history *domain.RenameHistory) error {
	if history == nil {
		return nil
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO rename_histories(user_id, name, rules, keep_ext, targets, item_count, change_count)
		 VALUES (?,?,?,?,?,?,?)`,
		history.UserID, history.Name, string(history.Rules), boolToInt(history.KeepExt),
		string(history.Targets), history.ItemCount, history.ChangeCount)
	if err != nil {
		return wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB(err)
	}
	history.ID = id
	return nil
}

// ListHistories 按 id 倒序返回最近的重命名历史。
func (r *renameRepo) ListHistories(ctx context.Context, userID int64, limit int) ([]*domain.RenameHistory, error) {
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+renameHistoryCols+` FROM rename_histories WHERE user_id=? ORDER BY id DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []*domain.RenameHistory
	for rows.Next() {
		item, err := scanRenameHistory(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, item)
	}
	return out, wrapDB(rows.Err())
}

// GetHistory 读取单条历史，不存在时返回 CodeNotFound。
func (r *renameRepo) GetHistory(ctx context.Context, id, userID int64) (*domain.RenameHistory, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT `+renameHistoryCols+` FROM rename_histories WHERE id=? AND user_id=?`, id, userID)
	item, err := scanRenameHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.Errorf(domain.CodeNotFound, "批量重命名历史不存在")
	}
	if err != nil {
		return nil, wrapDB(err)
	}
	return item, nil
}

// UpdateHistoryTargets 回滚后更新剩余待回滚条目与变更数量。
func (r *renameRepo) UpdateHistoryTargets(ctx context.Context, id int64, targets json.RawMessage, changeCount int) error {
	if len(targets) == 0 {
		targets = json.RawMessage("[]")
	}
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE rename_histories SET targets=?, change_count=? WHERE id=?`,
		string(targets), changeCount, id)
	return wrapDB(err)
}

// DeleteHistory 删除一条历史。
func (r *renameRepo) DeleteHistory(ctx context.Context, id, userID int64) error {
	_, err := r.db.write.ExecContext(ctx,
		`DELETE FROM rename_histories WHERE id=? AND user_id=?`, id, userID)
	return wrapDB(err)
}

// CreatePreset 保存常用组合；同名时覆盖规则、保留使用次数与创建时间。
func (r *renameRepo) CreatePreset(ctx context.Context, preset *domain.RenamePreset) error {
	if preset == nil {
		return nil
	}
	var existingID int64
	err := r.db.read.QueryRowContext(ctx,
		`SELECT id FROM rename_presets WHERE user_id=? AND name=?`, preset.UserID, preset.Name).Scan(&existingID)
	switch {
	case err == nil:
		if _, err := r.db.write.ExecContext(ctx,
			`UPDATE rename_presets SET rules=?, keep_ext=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(preset.Rules), boolToInt(preset.KeepExt), existingID); err != nil {
			return wrapDB(err)
		}
		preset.ID = existingID
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return wrapDB(err)
	}
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO rename_presets(user_id, name, rules, keep_ext, use_count) VALUES (?,?,?,?,0)`,
		preset.UserID, preset.Name, string(preset.Rules), boolToInt(preset.KeepExt))
	if err != nil {
		return wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return wrapDB(err)
	}
	preset.ID = id
	return nil
}

// ListPresets 按 id 倒序返回常用组合。
func (r *renameRepo) ListPresets(ctx context.Context, userID int64) ([]*domain.RenamePreset, error) {
	rows, err := r.db.read.QueryContext(ctx,
		`SELECT `+renamePresetCols+` FROM rename_presets WHERE user_id=? ORDER BY id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []*domain.RenamePreset
	for rows.Next() {
		item, err := scanRenamePreset(rows)
		if err != nil {
			return nil, wrapDB(err)
		}
		out = append(out, item)
	}
	return out, wrapDB(rows.Err())
}

// DeletePreset 删除一条常用组合，未命中时返回 CodeNotFound。
func (r *renameRepo) DeletePreset(ctx context.Context, id, userID int64) error {
	res, err := r.db.write.ExecContext(ctx,
		`DELETE FROM rename_presets WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return wrapDB(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return wrapDB(err)
	}
	if affected == 0 {
		return domain.Errorf(domain.CodeNotFound, "常用组合不存在")
	}
	return nil
}

// IncrementPresetUse 命中相同规则与扩展名设置时累加使用次数。
func (r *renameRepo) IncrementPresetUse(ctx context.Context, userID int64, rules json.RawMessage, keepExt bool) error {
	if len(rules) == 0 {
		return nil
	}
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE rename_presets SET use_count=use_count+1, updated_at=CURRENT_TIMESTAMP
		 WHERE user_id=? AND rules=? AND keep_ext=?`,
		userID, string(rules), boolToInt(keepExt))
	return wrapDB(err)
}

func scanRenameHistory(s interface{ Scan(...any) error }) (*domain.RenameHistory, error) {
	var (
		item       domain.RenameHistory
		rules      string
		targets    string
		keepExt    int
		createdRaw sql.NullString
	)
	if err := s.Scan(&item.ID, &item.UserID, &item.Name, &rules, &keepExt, &targets,
		&item.ItemCount, &item.ChangeCount, &createdRaw); err != nil {
		return nil, err
	}
	item.Rules = json.RawMessage(rules)
	item.Targets = json.RawMessage(targets)
	item.KeepExt = keepExt != 0
	item.CreatedAt = parseTS(createdRaw)
	return &item, nil
}

func scanRenamePreset(s interface{ Scan(...any) error }) (*domain.RenamePreset, error) {
	var (
		item       domain.RenamePreset
		rules      string
		keepExt    int
		createdRaw sql.NullString
		updatedRaw sql.NullString
	)
	if err := s.Scan(&item.ID, &item.UserID, &item.Name, &rules, &keepExt, &item.UseCount,
		&createdRaw, &updatedRaw); err != nil {
		return nil, err
	}
	item.Rules = json.RawMessage(rules)
	item.KeepExt = keepExt != 0
	item.CreatedAt = parseTS(createdRaw)
	item.UpdatedAt = parseTS(updatedRaw)
	return &item, nil
}
