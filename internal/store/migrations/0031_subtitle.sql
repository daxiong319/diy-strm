-- 字幕智能处理（搜索下载 + 智能匹配 + 时间轴校正）。
-- 对齐老版 diy-strm 的 internal/models/subtitle.go：
--   * subtitle_tasks   —— 每个视频一条任务记录，重复处理同一视频时复用同一行覆盖更新
--   * 配置项不再单列成 subtitle_configs 表：LitePan 的全局设置统一走
--     internal/settings 声明式注册表（键值存 configs 表），因此字幕的开关、
--     凭证、匹配与校正策略都以设置项形式存在，见 internal/settings/registry.go。
-- 老版用 GORM AutoMigrate 建表；现版走手写迁移，与既有 29 个迁移文件一致，
-- 无需在启动流程里额外挂建表调用。

CREATE TABLE IF NOT EXISTS subtitle_tasks (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id          INTEGER NOT NULL DEFAULT 0,
    video_path        TEXT NOT NULL DEFAULT '',
    subtitle_path     TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',
    title             TEXT NOT NULL DEFAULT '',
    year              INTEGER NOT NULL DEFAULT 0,
    season            INTEGER NOT NULL DEFAULT 0,
    episode           INTEGER NOT NULL DEFAULT 0,
    media_type        TEXT NOT NULL DEFAULT '',
    provider          TEXT NOT NULL DEFAULT '',
    candidate_slug    TEXT NOT NULL DEFAULT '',
    match_score       INTEGER NOT NULL DEFAULT 0,
    match_reason      TEXT NOT NULL DEFAULT '',
    candidate_json    TEXT NOT NULL DEFAULT '',
    sync_offset_ms    INTEGER NOT NULL DEFAULT 0,
    sync_scale        REAL NOT NULL DEFAULT 0,
    sync_confidence   REAL NOT NULL DEFAULT 0,
    sync_applied      INTEGER NOT NULL DEFAULT 0,
    error_message     TEXT NOT NULL DEFAULT '',
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_subtitle_tasks_media_id ON subtitle_tasks(media_id);
CREATE INDEX IF NOT EXISTS idx_subtitle_tasks_video_path ON subtitle_tasks(video_path);
CREATE INDEX IF NOT EXISTS idx_subtitle_tasks_status ON subtitle_tasks(status);
CREATE INDEX IF NOT EXISTS idx_subtitle_tasks_created_at ON subtitle_tasks(created_at DESC);
