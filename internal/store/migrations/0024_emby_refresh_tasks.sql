CREATE TABLE IF NOT EXISTS emby_refresh_tasks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_key        TEXT NOT NULL UNIQUE,
    library_id      TEXT NOT NULL DEFAULT '',
    library_name    TEXT NOT NULL DEFAULT '',
    target_type     TEXT NOT NULL DEFAULT 'library',
    item_ids        TEXT NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'pending',
    last_event_at   INTEGER NOT NULL DEFAULT 0,
    refresh_after_at INTEGER NOT NULL DEFAULT 0,
    deadline_at     INTEGER NOT NULL DEFAULT 0,
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_refresh_at INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_emby_refresh_tasks_status ON emby_refresh_tasks(status);
CREATE INDEX IF NOT EXISTS idx_emby_refresh_tasks_library_id ON emby_refresh_tasks(library_id);
CREATE INDEX IF NOT EXISTS idx_emby_refresh_tasks_refresh_after_at ON emby_refresh_tasks(refresh_after_at);
CREATE INDEX IF NOT EXISTS idx_emby_refresh_tasks_deadline_at ON emby_refresh_tasks(deadline_at);
