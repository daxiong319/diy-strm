-- 订阅转存前身份校验（对齐 muvyo 的 identity gate）。
--
-- 背景：订阅追新搜到候选后会直接发起转存，候选若是另一部剧/另一季/另一集，
-- 或是一个混拼了多部作品的分享，错误内容就会进网盘并可能覆盖已有文件。
-- 本表记录每一次身份校验的判定结果，reason_code 与 muvyo 的符号名保持一致，
-- 便于日后对齐两侧日志、按真实行为校准纯度阈值。
--
-- 只记录判定，不参与调度：闸门在发起转存**之前**同步调用，读写本表失败都只
-- 记日志、不影响判定（见 internal/discover/discovery/identity_gate.go）。

CREATE TABLE IF NOT EXISTS discovery_subscription_identity_checks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id  INTEGER NOT NULL DEFAULT 0,
    run_id           INTEGER NOT NULL DEFAULT 0,
    rule_id          INTEGER NOT NULL DEFAULT 0,
    -- 候选标识与订阅身份，方便从日志/记录直接定位到那一条候选。
    item_key         TEXT    NOT NULL DEFAULT '',
    source           TEXT    NOT NULL DEFAULT '',
    provider         TEXT    NOT NULL DEFAULT '',
    slug             TEXT    NOT NULL DEFAULT '',
    tmdb_id          INTEGER NOT NULL DEFAULT 0,
    expected_title   TEXT    NOT NULL DEFAULT '',
    candidate_title  TEXT    NOT NULL DEFAULT '',
    -- 校验结论。
    passed           INTEGER NOT NULL DEFAULT 0,
    reason_code      TEXT    NOT NULL DEFAULT '',
    dimension        TEXT    NOT NULL DEFAULT '',
    evidence_kind    TEXT    NOT NULL DEFAULT '',
    media_type       TEXT    NOT NULL DEFAULT '',
    year             INTEGER NOT NULL DEFAULT 0,
    season           INTEGER NOT NULL DEFAULT 0,
    episode          INTEGER NOT NULL DEFAULT 0,
    purity_ratio     REAL    NOT NULL DEFAULT 0,
    -- Result.Detail 原样 JSON，留作事后排查。
    detail           TEXT    NOT NULL DEFAULT '',
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_disc_identity_sub ON discovery_subscription_identity_checks(subscription_id, id);
CREATE INDEX IF NOT EXISTS idx_disc_identity_reason ON discovery_subscription_identity_checks(reason_code);
CREATE INDEX IF NOT EXISTS idx_disc_identity_run ON discovery_subscription_identity_checks(run_id);
CREATE INDEX IF NOT EXISTS idx_disc_identity_tmdb ON discovery_subscription_identity_checks(tmdb_id);
