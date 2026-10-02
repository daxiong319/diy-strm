-- 批量重命名历史与常用组合（对齐老版 internal/models/rename_record.go 的
-- rename_histories / rename_presets）。老版由 GORM AutoMigrate 建表并按 user_id
-- 隔离；现版 API 只有管理员会话、没有多用户概念，因此 user_id 保留列以兼容
-- 后续扩展，写入固定为 0，查询不再按用户过滤。

CREATE TABLE IF NOT EXISTS rename_histories (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL DEFAULT 0,
    name          TEXT NOT NULL DEFAULT '',
    rules         TEXT NOT NULL DEFAULT '',
    keep_ext      INTEGER NOT NULL DEFAULT 1,
    targets       TEXT NOT NULL DEFAULT '',
    item_count    INTEGER NOT NULL DEFAULT 0,
    change_count  INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_rename_histories_user_id ON rename_histories(user_id);
CREATE INDEX IF NOT EXISTS idx_rename_histories_created_at ON rename_histories(created_at DESC);

CREATE TABLE IF NOT EXISTS rename_presets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL DEFAULT 0,
    name        TEXT NOT NULL DEFAULT '',
    rules       TEXT NOT NULL DEFAULT '',
    keep_ext    INTEGER NOT NULL DEFAULT 1,
    use_count   INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_rename_presets_user_id ON rename_presets(user_id);
