-- MoviePilot 降级兜底（次数触发自动补找）：
--   1. movie_pilot_configs 追加 mp_fallback_* 配置列；
--   2. 新建 moviepilot_fallbacks 记录表（按「影片+季+触发来源」聚合计数）。
--
-- 语义严格对齐参考实现 ResourceSearchConfig 的 mp_fallback_* 文案：
--   mp_fallback_enabled               次数触发自动补找总开关（只控制搜索次数与订阅轮次规则）
--   mp_fallback_search_enabled        资源搜索无结果时启用（报错和取消不计数）
--   mp_fallback_search_threshold      同一影片或同一季累计无结果次数（默认 3）
--   mp_fallback_subscription_enabled  本地订阅无进展时启用（有待入库任务时不触发）
--   mp_fallback_subscription_threshold 同一影片或同一季连续无进展轮数（默认 3）
--   动作 subscribe / download（默认）/ download_then_subscribe。

ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_search_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_search_threshold INTEGER NOT NULL DEFAULT 3;
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_search_action TEXT NOT NULL DEFAULT 'download';
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_subscription_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_subscription_threshold INTEGER NOT NULL DEFAULT 3;
ALTER TABLE movie_pilot_configs ADD COLUMN mp_fallback_subscription_action TEXT NOT NULL DEFAULT 'download';

CREATE TABLE IF NOT EXISTS moviepilot_fallbacks (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    media_key          TEXT NOT NULL DEFAULT '',
    trigger            TEXT NOT NULL DEFAULT '',
    media_type         TEXT NOT NULL DEFAULT '',
    tmdb_id            INTEGER NOT NULL DEFAULT 0,
    title              TEXT NOT NULL DEFAULT '',
    season             INTEGER NOT NULL DEFAULT 0,
    search_count       INTEGER NOT NULL DEFAULT 0,
    subscription_count INTEGER NOT NULL DEFAULT 0,
    progress           INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'pending',
    action             TEXT NOT NULL DEFAULT '',
    download_episodes  TEXT NOT NULL DEFAULT '',
    external_id        TEXT NOT NULL DEFAULT '',
    message            TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 「影片+季+触发来源」唯一：搜索计数与订阅计数各自独立累计，互不干扰。
CREATE UNIQUE INDEX IF NOT EXISTS idx_moviepilot_fallbacks_media_trigger ON moviepilot_fallbacks(media_key, trigger);
CREATE INDEX IF NOT EXISTS idx_moviepilot_fallbacks_status ON moviepilot_fallbacks(status);
CREATE INDEX IF NOT EXISTS idx_moviepilot_fallbacks_tmdb ON moviepilot_fallbacks(tmdb_id);
