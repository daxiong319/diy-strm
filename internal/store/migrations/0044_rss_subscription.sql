-- T16 · RSS 订阅源
--
-- 字段照搬 muvyo 的 rss_subscription.py（SQLAlchemy 模型，见
-- /root/dsh/muvyo_latest_recovered/src/app/models/rss_subscription.py）。
-- 与任务书草图不同的地方，以 muvyo 真实模型为准：
--   - 源表**没有** target_tmdb_id / media_type / season / poll_interval_minutes /
--     last_guid / last_title。RSS 源不绑 TMDB 实体，它靠 target_path 直接转存。
--   - 源表**多了** include_regex / exclude_regex / last_status / last_message /
--     poster_url / storage / media_server —— 前两个与 Subscription 同名字段同义。
--   - 历史表**多了** download_url / link / target_path / status / message。
--
-- 去重核心：uq_rss_subscription_history_guid。RSS 条目本身没有稳定 ID，
-- guid 缺失时退到 <link>，再退到 "源ID+标题+发布时间"的合成键 ——
-- 合成键不可靠但总比「同一条目反复入库」好，具体口径见
-- internal/discover/rss/item.go 的 func (Item) DedupKey(int64) string。

CREATE TABLE IF NOT EXISTS rss_subscription_sources (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    rss_url       TEXT    NOT NULL,
    target_path   TEXT    NOT NULL DEFAULT '',
    storage       TEXT    NOT NULL DEFAULT '',
    media_server  TEXT    NOT NULL DEFAULT '',
    poster_url    TEXT    NOT NULL DEFAULT '',
    include_regex TEXT    NOT NULL DEFAULT '',
    exclude_regex TEXT    NOT NULL DEFAULT '',
    -- media_type 与 action 是 litepan 在 muvyo 模型之外补的两个字段。
    -- media_type（movie/tv）参与身份校验：订阅源声明它追的是电影还是剧集，
    --   条目标题解析出的类型与之冲突时按 MEDIA_TYPE_MISMATCH 挡下。
    -- action（transfer/offline）决定产出候选的落地通道：网盘分享走转存，
    --   磁力/ed2k 走离线下载（磁力没有网盘分享可转存）。
    --   muvyo 没有这一列 —— 它的 RSS 走的是订阅执行器，由订阅的规则决定落地方式；
    --   litepan 把这个决定上提到源上，因为 RSS 源不生成 DiscoverySubscription 行。
    media_type    TEXT    NOT NULL DEFAULT 'tv',
    action        TEXT    NOT NULL DEFAULT 'transfer',
    enabled       INTEGER NOT NULL DEFAULT 1,
    last_sync_at  TEXT,
    last_status   TEXT    NOT NULL DEFAULT '',
    last_message  TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_rss_sources_enabled ON rss_subscription_sources (enabled);

CREATE TABLE IF NOT EXISTS rss_subscription_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id    INTEGER NOT NULL,
    source_name  TEXT    NOT NULL DEFAULT '',
    guid         TEXT    NOT NULL,
    title        TEXT    NOT NULL DEFAULT '',
    link         TEXT    NOT NULL DEFAULT '',
    download_url TEXT    NOT NULL DEFAULT '',
    target_path  TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT '',
    message      TEXT    NOT NULL DEFAULT '',
    published_at TEXT,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- 去重的唯一依据。插入走 ON CONFLICT(guid) DO NOTHING，
-- 命中即「这条已经处理过」，是整条去重链路上唯一不能出错的一环。
CREATE UNIQUE INDEX IF NOT EXISTS uq_rss_subscription_history_guid
    ON rss_subscription_history (guid);

CREATE INDEX IF NOT EXISTS idx_rss_history_source ON rss_subscription_history (source_id);
-- 追赶位点要按源取「已处理条目的最新时间」，所以 created_at 也要索引。
CREATE INDEX IF NOT EXISTS idx_rss_history_created ON rss_subscription_history (created_at);