package store

import (
	"context"
	"database/sql"

	"litepan/internal/domain"
)

type notifyChannelRepo struct{ db *DB }

func (r *notifyChannelRepo) Create(ctx context.Context, c *domain.NotifyChannel) (int64, error) {
	res, err := r.db.write.ExecContext(ctx,
		`INSERT INTO notify_channels(type, name, config, enabled, created_at, updated_at)
		 VALUES (?,?,?,?,COALESCE(NULLIF(?,''),CURRENT_TIMESTAMP),COALESCE(NULLIF(?,''),CURRENT_TIMESTAMP))`,
		c.Type, c.Name, c.Config, boolToInt(c.Enabled), tsValue(c.CreatedAt), tsValue(c.UpdatedAt))
	if err != nil {
		return 0, wrapDB(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, wrapDB(err)
	}
	return id, nil
}

func (r *notifyChannelRepo) Update(ctx context.Context, c *domain.NotifyChannel) error {
	_, err := r.db.write.ExecContext(ctx,
		`UPDATE notify_channels SET type=?, name=?, config=?, enabled=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		c.Type, c.Name, c.Config, boolToInt(c.Enabled), c.ID)
	return wrapDB(err)
}

func (r *notifyChannelRepo) Delete(ctx context.Context, id int64) error {
	_, err := r.db.write.ExecContext(ctx, `DELETE FROM notify_channels WHERE id=?`, id)
	return wrapDB(err)
}

func (r *notifyChannelRepo) Get(ctx context.Context, id int64) (*domain.NotifyChannel, error) {
	row := r.db.read.QueryRowContext(ctx,
		`SELECT id, type, name, config, enabled, created_at, updated_at FROM notify_channels WHERE id=?`, id)
	c, err := scanNotifyChannel(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *notifyChannelRepo) List(ctx context.Context) ([]*domain.NotifyChannel, error) {
	return r.query(ctx, `SELECT id, type, name, config, enabled, created_at, updated_at FROM notify_channels ORDER BY id ASC`)
}

func (r *notifyChannelRepo) ListEnabled(ctx context.Context) ([]*domain.NotifyChannel, error) {
	return r.query(ctx, `SELECT id, type, name, config, enabled, created_at, updated_at FROM notify_channels WHERE enabled=1 ORDER BY id ASC`)
}

func (r *notifyChannelRepo) query(ctx context.Context, stmt string, args ...any) ([]*domain.NotifyChannel, error) {
	rows, err := r.db.read.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, wrapDB(err)
	}
	defer rows.Close()
	var out []*domain.NotifyChannel
	for rows.Next() {
		var (
			c           domain.NotifyChannel
			enabled     int
			createdNull sql.NullString
			updatedNull sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &c.Config, &enabled, &createdNull, &updatedNull); err != nil {
			return nil, wrapDB(err)
		}
		c.Enabled = enabled != 0
		c.CreatedAt = parseTS(createdNull)
		c.UpdatedAt = parseTS(updatedNull)
		out = append(out, &c)
	}
	return out, wrapDB(rows.Err())
}

func scanNotifyChannel(row interface{ Scan(...any) error }) (*domain.NotifyChannel, error) {
	var (
		c           domain.NotifyChannel
		enabled     int
		createdNull sql.NullString
		updatedNull sql.NullString
	)
	err := row.Scan(&c.ID, &c.Type, &c.Name, &c.Config, &enabled, &createdNull, &updatedNull)
	if err != nil {
		return nil, wrapDB(err)
	}
	c.Enabled = enabled != 0
	c.CreatedAt = parseTS(createdNull)
	c.UpdatedAt = parseTS(updatedNull)
	return &c, nil
}
