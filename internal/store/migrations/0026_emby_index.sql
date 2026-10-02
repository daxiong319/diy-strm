-- Emby 本地索引：把 Emby 媒体条目映射回网盘文件，供其它功能解析「这个 Emby 条目对应哪个网盘文件」。
-- 迁移来源：老版 internal/models/emby_media.go 的四张表。

CREATE TABLE IF NOT EXISTS emby_media_items (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    item_id              TEXT    NOT NULL,
    item_id_int          INTEGER NOT NULL DEFAULT 0,
    server_id            TEXT    NOT NULL DEFAULT '',
    name                 TEXT    NOT NULL DEFAULT '',
    type                 TEXT    NOT NULL DEFAULT '',
    parent_id            TEXT    NOT NULL DEFAULT '',
    series_id            TEXT    NOT NULL DEFAULT '',
    series_name          TEXT    NOT NULL DEFAULT '',
    season_id            TEXT    NOT NULL DEFAULT '',
    season_name          TEXT    NOT NULL DEFAULT '',
    library_id           TEXT    NOT NULL DEFAULT '',
    path                 TEXT    NOT NULL DEFAULT '',
    pick_code            TEXT    NOT NULL DEFAULT '',
    media_source_path    TEXT    NOT NULL DEFAULT '',
    index_number         INTEGER NOT NULL DEFAULT 0,
    parent_index_number  INTEGER NOT NULL DEFAULT 0,
    production_year      INTEGER NOT NULL DEFAULT 0,
    premiere_date        TEXT    NOT NULL DEFAULT '',
    date_created         TEXT    NOT NULL DEFAULT '',
    date_created_time    INTEGER NOT NULL DEFAULT 0,
    date_modified        TEXT    NOT NULL DEFAULT '',
    date_modified_time   INTEGER NOT NULL DEFAULT 0,
    is_folder            INTEGER NOT NULL DEFAULT 0,
    last_seen_sync_run   TEXT    NOT NULL DEFAULT '',
    last_seen_at         INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_emby_media_items_item_id ON emby_media_items(item_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_item_id_int ON emby_media_items(item_id_int);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_library_id ON emby_media_items(library_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_pick_code ON emby_media_items(pick_code);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_series_id ON emby_media_items(series_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_season_id ON emby_media_items(season_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_items_last_seen_sync_run ON emby_media_items(last_seen_sync_run);

-- Emby 条目 ↔ 网盘文件的关联。
-- 现版没有「每文件一行」的 strm 表，这里改为存账号 + 根目录 + 相对路径，
-- 以便通过 file.Service.ResolvePath 反查网盘文件。
CREATE TABLE IF NOT EXISTS emby_media_sync_files (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    emby_item_id   INTEGER NOT NULL DEFAULT 0,
    sync_file_id   INTEGER NOT NULL DEFAULT 0,
    pick_code      TEXT    NOT NULL DEFAULT '',
    sync_path_id   INTEGER NOT NULL DEFAULT 0,
    account_id     INTEGER NOT NULL DEFAULT 0,
    root_id        TEXT    NOT NULL DEFAULT '',
    relative_path  TEXT    NOT NULL DEFAULT '',
    file_name      TEXT    NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_emby_media_sync_files_emby_item_id ON emby_media_sync_files(emby_item_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_sync_files_sync_file_id ON emby_media_sync_files(sync_file_id);
CREATE INDEX IF NOT EXISTS idx_emby_media_sync_files_pick_code ON emby_media_sync_files(pick_code);
CREATE INDEX IF NOT EXISTS idx_emby_media_sync_files_sync_path_id ON emby_media_sync_files(sync_path_id);

-- Emby 媒体库 ↔ STRM 同步任务关联（对应老版 SyncPath）。
CREATE TABLE IF NOT EXISTS emby_library_sync_paths (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id    TEXT    NOT NULL DEFAULT '',
    sync_path_id  INTEGER NOT NULL DEFAULT 0,
    library_name  TEXT    NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_emby_library_sync_paths_unique ON emby_library_sync_paths(library_id, sync_path_id);

CREATE TABLE IF NOT EXISTS emby_libraries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL DEFAULT '',
    library_id    TEXT    NOT NULL DEFAULT '',
    sync_path_id  INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_emby_libraries_library_id ON emby_libraries(library_id);

-- 增量同步游标持久化。按配置 ID 记录 LastSavedCursorAt 与上次同步时间。
CREATE TABLE IF NOT EXISTS emby_sync_state (
    config_id              TEXT    PRIMARY KEY,
    last_saved_cursor_at   INTEGER NOT NULL DEFAULT 0,
    last_sync_time         INTEGER NOT NULL DEFAULT 0,
    last_incremental_at    INTEGER NOT NULL DEFAULT 0,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
