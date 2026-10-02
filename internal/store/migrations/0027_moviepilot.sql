-- MoviePilot 集成：全局配置、促销阶梯、上传任务、识别失败文件、整理历史。
-- 说明：洗版（洗版扫描 wash_scan_items / wash_logs）本次未移植，
-- 仅保留整理流程内联使用的洗版质量比较引擎，故不建对应表。

CREATE TABLE IF NOT EXISTS movie_pilot_configs (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled                 INTEGER NOT NULL DEFAULT 0,
    base_url                TEXT NOT NULL DEFAULT '',
    api_token               TEXT NOT NULL DEFAULT '',
    download_root           TEXT NOT NULL DEFAULT '',
    local_view_root         TEXT NOT NULL DEFAULT '',
    upload_account_id       INTEGER NOT NULL DEFAULT 0,
    upload_root             TEXT NOT NULL DEFAULT '',
    upload_root_id          TEXT NOT NULL DEFAULT '',
    strm_local_dir          TEXT NOT NULL DEFAULT '',
    poll_interval           INTEGER NOT NULL DEFAULT 5,
    notify_enabled          INTEGER NOT NULL DEFAULT 1,
    category_config         TEXT NOT NULL DEFAULT '',
    promotion_order         TEXT NOT NULL DEFAULT 'free,2xfree,normal,half,2xhalf',
    promotion_patience_hours INTEGER NOT NULL DEFAULT 12,
    seed_retention_hours    INTEGER NOT NULL DEFAULT 0,
    qbittorrent_url         TEXT NOT NULL DEFAULT '',
    qbittorrent_user        TEXT NOT NULL DEFAULT '',
    qbittorrent_pass        TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS movie_pilot_promotion_ladders (
    subscribe_id    INTEGER PRIMARY KEY,
    tier            INTEGER NOT NULL DEFAULT 0,
    tier_started_at INTEGER NOT NULL DEFAULT 0,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS movie_pilot_upload_tasks (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    torrent_hash       TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL DEFAULT '',
    media_type         TEXT NOT NULL DEFAULT '',
    tmdb_id            INTEGER NOT NULL DEFAULT 0,
    season             TEXT NOT NULL DEFAULT '',
    local_path         TEXT NOT NULL DEFAULT '',
    remote_path        TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending',
    total_files        INTEGER NOT NULL DEFAULT 0,
    uploaded_files     INTEGER NOT NULL DEFAULT 0,
    total_bytes        INTEGER NOT NULL DEFAULT 0,
    uploaded_bytes     INTEGER NOT NULL DEFAULT 0,
    error              TEXT NOT NULL DEFAULT '',
    empty_source_since TIMESTAMP,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_movie_pilot_upload_tasks_hash ON movie_pilot_upload_tasks(torrent_hash);
CREATE INDEX IF NOT EXISTS idx_movie_pilot_upload_tasks_status ON movie_pilot_upload_tasks(status);

CREATE TABLE IF NOT EXISTS movie_pilot_failed_files (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    INTEGER NOT NULL DEFAULT 0,
    file_name  TEXT NOT NULL DEFAULT '',
    parent_id  TEXT NOT NULL DEFAULT '',
    root_path  TEXT NOT NULL DEFAULT '',
    account_id INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT 'pending',
    media_type TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    tmdb_id    INTEGER NOT NULL DEFAULT 0,
    year       INTEGER NOT NULL DEFAULT 0,
    season     INTEGER NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_movie_pilot_failed_files_task ON movie_pilot_failed_files(task_id);
CREATE INDEX IF NOT EXISTS idx_movie_pilot_failed_files_status ON movie_pilot_failed_files(status);

CREATE TABLE IF NOT EXISTS movie_pilot_organize_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL DEFAULT 0,
    task_id    INTEGER NOT NULL DEFAULT 0,
    file_name  TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    target_path TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    year       INTEGER NOT NULL DEFAULT 0,
    season_num INTEGER NOT NULL DEFAULT 0,
    episode_num INTEGER NOT NULL DEFAULT 0,
    tmdb_id    INTEGER NOT NULL DEFAULT 0,
    status     TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_movie_pilot_organize_history_account ON movie_pilot_organize_history(account_id);
CREATE INDEX IF NOT EXISTS idx_movie_pilot_organize_history_task ON movie_pilot_organize_history(task_id);
