-- 巡检快照：扫描结果与预览内容的落库。
-- 存在的理由只有一个 —— 让「预览」成为执行的唯一入口。
-- 执行接口只接受 snapshot_id + finding_id，没有快照就没有可执行的修复动作。
-- 注意：本文件里的注释不得出现分号（迁移按分号切语句且不识别注释）。

CREATE TABLE IF NOT EXISTS inspection_snapshots (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_key TEXT    NOT NULL UNIQUE,
    finding_ids  TEXT    NOT NULL,
    payload      TEXT    NOT NULL,
    scanned_at   INTEGER NOT NULL,
    consumed_at  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_inspection_snapshots_created
    ON inspection_snapshots(created_at);