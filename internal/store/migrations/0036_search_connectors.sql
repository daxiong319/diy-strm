-- T05 · 搜索连接器接口化 —— 订阅支持多搜索源 + 画质/特效/体积闸门。
--
-- 背景：订阅执行器原来把检索写死成一行 Tgto123SearchResources(...)，
-- 每接一个新源都要改一遍订阅引擎、候选结构、评分、去重。T05 把这层收敛成
-- Connector 接口，本迁移给订阅补上「用哪些源」和「要什么画质」两组列。
--
-- **存量行为不变是硬约束**（任务书验收项 1）。做法：
--   search_sources        默认 'tgto123' —— 与改动前唯一那个源完全一致，
--                         逗号分隔便于加源；用列而不是独立关联表，理由是
--                         litepan 订阅模型本身就是「扁平列 + JSON」（Preferences/
--                         Metadata），关联表要另带 CRUD + 端点 + 前端，更重且无收益。
--   resolution/effect/    画质列默认空串 —— 空串 = 不限制，等价于改动前「不过滤」。
--   min/max_file_size_mb  体积列默认 0 —— 0 = 不限制。
--
-- 语义说明：
--   resolution  是**下限**不是精确匹配（'2160' 表示「至少 4K」，1080p 会被过滤掉）。
--   effect      是**必须具备**的特效；'sdr' 例外，表示「必须不含 HDR/DV」。
--   体积列      两个都 0 时不引入任何体积判定；候选体积未知（源没报 size）
--               时不套用体积闸门，只记日志 —— 多数源不报体积，当硬闸门会饿死订阅。
--
-- ⚠️ 落地方式（为什么 CREATE TABLE 而不是 ALTER TABLE）：
--   discovery_subscriptions 是 GORM AutoMigrate 表，启动顺序是
--   wire_store.go:46 db.Migrate() 先跑、wire_http.go:310 EnsureDiscoverySchema() 后跑。
--   所以**在本迁移里直接 ALTER discovery_subscriptions 会在空库上失败**（此刻表还不存在），
--   SQLite 的 ADD COLUMN 又没有 IF NOT EXISTS，无法幂等。
--   正确做法（本文件采用）：
--     · 这里 CREATE TABLE IF NOT EXISTS，空库时直接建出带新列的完整表；
--     · 旧库时 IF NOT EXISTS 让它空转，随后的 AutoMigrate（DiscoverySubscription 结构体
--       已加同名同类型字段）负责补列 —— GORM AutoMigrate 的既定行为就是对已有表
--       补缺失列、从不删列。
--   两条路径最终收敛到同一张表，新装与升级行为一致。
--
-- ⚠️ 索引为什么不在这里建（踩过的坑）：
--   `CREATE INDEX ... ON discovery_subscriptions(search_sources)` 依赖新列已经存在。
--   空库上确实存在（上面刚建表），但**旧库上不存在** —— 旧库的表是 T05 之前
--   AutoMigrate 建的，没有 search_sources 列，而补列的 AutoMigrate 这时还没跑
--   （它在 wire_http.go，比 wire_store.go 的 Migrate 晚）。
--   于是升级老实例会在启动时直接失败：
--       apply 0036_search_connectors.sql: SQL logic error: no such column: search_sources
--   SQLite 的 DDL 没有「IF 列存在」这种条件语法，绕不开，只能把索引交给
--   AutoMigrate 建（见文件末尾说明）。
--
-- ⚠️ 顺带一个坑：本仓的 splitStatements（internal/store/migrate.go）是直接按分号切
--   语句的，不识别注释。所以**本文件任何一行注释里都不能出现分号**，
--   否则那一段会被当成 SQL 切出去并报语法错误。

CREATE TABLE IF NOT EXISTS discovery_subscriptions (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_key            TEXT    NOT NULL UNIQUE,
    source                TEXT    NOT NULL DEFAULT 'tmdb',
    entity_type           TEXT    NOT NULL DEFAULT 'movie',
    external_id           TEXT    NOT NULL DEFAULT '',
    tmdb_id               INTEGER NOT NULL DEFAULT 0,
    media_type            TEXT    NOT NULL DEFAULT 'movie',
    title                 TEXT    NOT NULL DEFAULT '',
    original_title        TEXT    NOT NULL DEFAULT '',
    poster                TEXT    NOT NULL DEFAULT '',
    target_provider       TEXT    NOT NULL DEFAULT '',
    transfer_mode         TEXT    NOT NULL DEFAULT 'auto',
    enabled               INTEGER NOT NULL DEFAULT 1,
    interval_minutes      INTEGER NOT NULL DEFAULT 0,
    preferences           TEXT    NOT NULL DEFAULT '',
    metadata              TEXT    NOT NULL DEFAULT '',
    status                TEXT    NOT NULL DEFAULT 'pending',
    last_checked_at       DATETIME,
    next_check_at         DATETIME,
    -- ---- T05 新增：搜索源与画质闸门 ----
    search_sources        TEXT    NOT NULL DEFAULT 'tgto123',
    resolution            TEXT    NOT NULL DEFAULT '',
    effect                TEXT    NOT NULL DEFAULT '',
    min_file_size_mb      INTEGER NOT NULL DEFAULT 0,
    max_file_size_mb      INTEGER NOT NULL DEFAULT 0,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 索引不在本文件创建，见文件头「索引为什么不在这里建」的说明。
-- 实际由 discovery.DiscoverySubscription 的 gorm 标签经 AutoMigrate 建出：
--   SearchSources 的 gorm 标签含 index → idx_discovery_subscriptions_search_sources
--   NextCheckAt 的 gorm 标签含 index → idx_discovery_subscriptions_next_check