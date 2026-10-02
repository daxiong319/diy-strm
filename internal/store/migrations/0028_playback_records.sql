-- 播放记录（对齐老版 internal/models/emby_playback_record.go 的 emby_playback_records）。
-- 老版由 GORM AutoMigrate 建表；现版发现侧的 GORM 表已在同一库共存，但本表是
-- 核心反代链路数据，按主库风格用显式迁移建表，避免 AutoMigrate 与手写迁移混管。

CREATE TABLE IF NOT EXISTS playback_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id     TEXT NOT NULL DEFAULT '1',
    user_id     TEXT NOT NULL DEFAULT '',
    client      TEXT NOT NULL DEFAULT '',
    device_id   TEXT NOT NULL DEFAULT '',
    item_name   TEXT NOT NULL DEFAULT '',
    strm_path   TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL DEFAULT '',
    playback_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_playback_records_playback_at ON playback_records(playback_at);
CREATE INDEX IF NOT EXISTS idx_playback_records_rule_id ON playback_records(rule_id);
CREATE INDEX IF NOT EXISTS idx_playback_records_user_id ON playback_records(user_id);
CREATE INDEX IF NOT EXISTS idx_playback_records_provider ON playback_records(provider);
