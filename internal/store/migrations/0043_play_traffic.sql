-- T11 播放监控与观影报告。
--
-- ⚠️ playback_records 这张表**已经存在**（0028_playback_records.sql 建过），
-- 是 Emby 302 反代链路的播放记录。本迁移走 ALTER 补字段而不是重建 ——
-- 重建会把 0028 以来积累的 Emby 播放历史全部清空，而验收第 9 条要求
-- 这些记录能出现在观影报告里。
--
-- ⚠️ 双用户标识（0028 的 user_id 与 RBAC 的 user_id 语义不同）：
--   - user_id TEXT NOT NULL DEFAULT ''  —— Emby 侧的用户标识，来自播放请求的
--     UserId 查询参数，是**字符串**。不能改类型，改了会破坏存量数据。
--   - app_user_id INTEGER NOT NULL DEFAULT 0 —— litepan RBAC（T08）的用户 ID，
--     整数。Emby 用户与 litepan 账号的映射关系由外部配置决定，映射不上就是 0
--     （= 匿名），**不允许拿它替换 user_id**。
--   两列并存：报告按 app_user_id 聚合时用 RBAC 显示名，按 user_id 聚合时用
--   Emby 原名。混淆这两列的典型症状是「所有人的观看记录都算到同一个人头上」。

ALTER TABLE playback_records ADD COLUMN request_type TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN request_url TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN original_url TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN client_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN response_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE playback_records ADD COLUMN response_time REAL NOT NULL DEFAULT 0;
ALTER TABLE playback_records ADD COLUMN storage_slug TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN storage_type TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN timestamp TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN app_user_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE playback_records ADD COLUMN item_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN app_source TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN app_client_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN app_item_name TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN app_strm_path TEXT NOT NULL DEFAULT '';
ALTER TABLE playback_records ADD COLUMN watched_seconds INTEGER NOT NULL DEFAULT 0;

-- app_source 区分这条记录是哪条链路写的。存量行默认为空字符串，
-- 报告侧把空串与 'litepan' 一视同仁（都是 litepan 自己写的），
-- 迁移脚本**不回填**：改了存量行会让「统计从启用起累计」这条口径失效
-- —— 观影报告不补算启用前的记录，回填等于补算。
CREATE INDEX IF NOT EXISTS idx_playback_records_timestamp ON playback_records(timestamp);
CREATE INDEX IF NOT EXISTS idx_playback_records_request_type ON playback_records(request_type);
CREATE INDEX IF NOT EXISTS idx_playback_records_storage_slug ON playback_records(storage_slug);
CREATE INDEX IF NOT EXISTS idx_playback_records_app_user_id ON playback_records(app_user_id);

-- 每日流量桶。⚠️ 唯一键 (day, user_id) 与它自己的注释语义
-- 「用户 × 条目 × 天」**不一致** —— 同一个用户同一天看两部片子只落一行，
-- 两部的流量会被合并。这是 muvyo 原设计（可能是有意的去重，也可能是 bug）。
-- 这里**照搬结构**以保持口径一致，但在代码里标注为待确认点：
-- 若要改成 (day, user_id, item_scope)，需要一次带数据迁移，不能靠改约束。
CREATE TABLE IF NOT EXISTS play_traffic_daily (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    day            TEXT NOT NULL,
    user_id        INTEGER NOT NULL DEFAULT 0,
    user_name      TEXT NOT NULL DEFAULT '',
    uploaded_bytes INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_play_traffic_day_user ON play_traffic_daily(day, user_id);
CREATE INDEX IF NOT EXISTS idx_play_traffic_day ON play_traffic_daily(day);