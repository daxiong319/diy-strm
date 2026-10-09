-- T07 · 洗版管理系统（独立扫描 + 版本槽位 + 提交锁）。
--
-- 背景：internal/moviepilot/ 已经有完整的质量比较引擎
-- （ParseQualityFromName / CompareQuality / DecideWash / QualityCompareTrace），
-- 但那套东西只在 MoviePilot 的整理流程内生效：下载完一个文件、整理到「已整理」
-- 目录时顺手比一次同集旧版好还是差，差就不整理。没有独立的洗版扫描、没有规则配置、
-- 没有判定与提交分离、也没有批量删保护。0027_moviepilot.sql 的注释里
-- 「MoviePilot 洗版扫描表未移植」说的就是这个缺口 —— 引擎有，系统没有。
--
-- 本迁移给出三张表：
--   media_upgrade_scans    一次扫描任务（只判定不执行）
--   media_upgrade_records  每条洗版判定结果（含判定输入快照，供提交阶段复核）
--   media_upgrade_rules    规则集（对应 muvyo 的 upgrade_rules JSON）
--
-- 为什么要「判定」与「提交」分成两张表：
-- 洗版会删用户文件。扫描时的结论基于**当时**看到的旧文件集合，如果从扫描到执行
-- 之间有人改了目录（手动换版本、Emby 重扫、订阅转存写入了同名文件），旧结论就是错的。
-- 所以 records 里存的是判定输入快照（旧文件路径 + 大小 + 修改时间 + 槽位），
-- 执行时重新枚举一次再比对，对不上就整条跳过并标 expired，绝不按过期结论删文件。
--
-- 字段口径：
--   quality_relation  new_wins / new_loses / tie / no_dimension
--                     前三个照搬 muvyo 的 quality_relation 四态；
--                     no_dimension = 无可比维度（两边都没解析出可比的分辨率/编码等），
--                     这种一律不执行（宁可漏洗不误删）。
--   status            pending 待提交 / executing 提交中（锁）/ executed 已执行 /
--                     skipped_limit 触发 max_records_per_series 上限 /
--                     expired 判定已过期 / skipped_new_loses 新版没赢 /
--                     skipped_no_slot 不同槽位 / skipped_no_dimension 无可比维度 /
--                     failed 执行失败 / skipped_no_access 路径不可达
--   snapshot          判定输入快照 JSON：旧文件集合 + 每条的 size/mtime
--   snapshot_hash     快照的 sha256，执行时重算比对，防 TOCTOU
--   loser_action      快照住判定当时生效的动作，执行时按快照执行（避免中途改配置导致行为漂移）
--
-- 与 muvyo 的两处刻意偏差（逆向时 muvyo 叫 min_audio_tracks，这里叫 min_channels）：
--   min_channels 不是 min_audio_tracks —— 本仓解析器从文件名拿到的是**声道数**，
--     不是音轨条数，两者不等价。列名跟着语义走，避免日后按名字误用。
--   loser_action 默认为 keep 而非恒删，理由见变更说明：这是会删用户文件的功能，默认必须安全。
--
-- ⚠️ 本仓的 splitStatements（internal/store/migrate.go:180）是直接按分号切语句的，
-- 不识别注释。所以**本文件任何一行注释里都不能出现分号**，否则那一段会被当成 SQL 切出去
-- 并报语法错误。

CREATE TABLE IF NOT EXISTS media_upgrade_scans (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    source               TEXT    NOT NULL DEFAULT 'local',
    library_root         TEXT    NOT NULL DEFAULT '',
    candidate_roots      TEXT    NOT NULL DEFAULT '',
    rule_id              INTEGER NOT NULL DEFAULT 0,
    status               TEXT    NOT NULL DEFAULT 'pending',
    library_files        INTEGER NOT NULL DEFAULT 0,
    candidate_files      INTEGER NOT NULL DEFAULT 0,
    total_records        INTEGER NOT NULL DEFAULT 0,
    new_wins_count       INTEGER NOT NULL DEFAULT 0,
    skipped_count        INTEGER NOT NULL DEFAULT 0,
    failed_count         INTEGER NOT NULL DEFAULT 0,
    message              TEXT    NOT NULL DEFAULT '',
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at           DATETIME,
    finished_at          DATETIME,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_media_upgrade_scans_created
    ON media_upgrade_scans(created_at);

CREATE TABLE IF NOT EXISTS media_upgrade_records (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id              INTEGER NOT NULL DEFAULT 0,
    rule_id              INTEGER NOT NULL DEFAULT 0,
    series_key           TEXT    NOT NULL DEFAULT '',
    series_title         TEXT    NOT NULL DEFAULT '',
    episode_key          TEXT    NOT NULL DEFAULT '',
    slot_key             TEXT    NOT NULL DEFAULT '',
    new_file_path        TEXT    NOT NULL DEFAULT '',
    new_file_name        TEXT    NOT NULL DEFAULT '',
    new_size             INTEGER NOT NULL DEFAULT 0,
    new_quality          TEXT    NOT NULL DEFAULT '',
    old_files            TEXT    NOT NULL DEFAULT '',
    quality_relation     TEXT    NOT NULL DEFAULT '',
    trace                TEXT    NOT NULL DEFAULT '',
    loser_action         TEXT    NOT NULL DEFAULT 'keep',
    loser_path           TEXT    NOT NULL DEFAULT '',
    snapshot             TEXT    NOT NULL DEFAULT '',
    snapshot_hash        TEXT    NOT NULL DEFAULT '',
    snapshot_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status               TEXT    NOT NULL DEFAULT 'pending',
    delete_failures      TEXT    NOT NULL DEFAULT '',
    message              TEXT    NOT NULL DEFAULT '',
    executed_at          DATETIME,
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_media_upgrade_records_scan
    ON media_upgrade_records(scan_id, status);
CREATE INDEX IF NOT EXISTS idx_media_upgrade_records_series
    ON media_upgrade_records(series_key);
CREATE INDEX IF NOT EXISTS idx_media_upgrade_records_status
    ON media_upgrade_records(status);

CREATE TABLE IF NOT EXISTS media_upgrade_rules (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT    NOT NULL DEFAULT '',
    source                 TEXT    NOT NULL DEFAULT 'local',
    library_root           TEXT    NOT NULL DEFAULT '',
    candidate_roots        TEXT    NOT NULL DEFAULT '',
    min_resolution         INTEGER NOT NULL DEFAULT 0,
    min_channels           INTEGER NOT NULL DEFAULT 0,
    require_subtitle       INTEGER NOT NULL DEFAULT 0,
    max_records_per_series INTEGER NOT NULL DEFAULT 0,
    loser_action           TEXT    NOT NULL DEFAULT 'keep',
    move_dir               TEXT    NOT NULL DEFAULT '',
    group_priority         TEXT    NOT NULL DEFAULT '',
    wash_rules             TEXT    NOT NULL DEFAULT '',
    enabled                INTEGER NOT NULL DEFAULT 1,
    builtin                INTEGER NOT NULL DEFAULT 0,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_media_upgrade_rules_enabled
    ON media_upgrade_rules(enabled, id);
