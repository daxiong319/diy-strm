-- T12 · 通知场景化与 Webhook 补发
--
-- ⚠️ 本迁移是**新增表**，不改任何既有表结构。
-- 场景订阅（scenes 键）不落这张表，而是存在 notify_channels.config 这个
-- JSON 字符串里 —— 沿用渠道「所有配置项一律进 config」的老惯例，
-- 这样存量渠道没有 scenes 键时 ParseSceneSubscriptions 返回空 = 全订阅，
-- 行为逐条不变（硬约束 6「存量配置必须能加载」）。
--
-- 为什么不给 notify_channels 加 scenes 列：
--   scenes 与 events、webhook 的 url/method/body 同属「渠道配置」，
--   拆成独立列就会出现两份真相来源（config 里一份、列里一份），
--   前端表单改一处后端读另一处的 bug 很难排查。
--
-- 补发队列（outbox）则必须独立成表：它有生命周期状态机、attempts 计数、
-- next_retry_at 排期，这些都不是「渠道配置」而是运行时状态。

CREATE TABLE IF NOT EXISTS notify_retry_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 触发来源场景（upload/strm/...）。存不下就留空串：补发队列要能
    -- 回答「这条是哪个场景失败的」，但事件本身已落 notifications 表，
    -- 这里只是便于按场景排查，不参与过滤。
    event_scene     TEXT NOT NULL DEFAULT '',
    -- 渠道类型：只有 "webhook" 会进这个表（见 notifychannel/outbox.go）。
    -- 单独留一列而不是每次去 JOIN notify_channels，是因为渠道可能被删，
    -- 而「这条重试挂在哪个渠道上」必须留下可查痕迹。
    channel_type    TEXT NOT NULL DEFAULT '',
    -- 渠道名快照：渠道改名/删除后仍能看出这条重试原本发给谁。
    channel_name    TEXT NOT NULL DEFAULT '',
    -- 目标 URL 快照：同上，且渠道配置被改后重试不该改投到新地址
    -- （那是两次不同的发送，不是同一次失败的重试）。
    target_url      TEXT NOT NULL DEFAULT '',
    -- 原始 config JSON 快照，与发送当时完全一致。
    channel_config  TEXT NOT NULL DEFAULT '{}',
    -- 通知标题 / 正文 / 语气，原样保存用于重发。
    title           TEXT NOT NULL DEFAULT '',
    content         TEXT NOT NULL DEFAULT '',
    tone            TEXT NOT NULL DEFAULT '',
    -- 已尝试次数（不含本次待执行的那次）。
    attempts        INTEGER NOT NULL DEFAULT 0,
    -- 下次重试时间；status != 'pending' 时无意义。
    next_retry_at   TIMESTAMP,
    -- 最近一次失败原因，原样存 Send 返回的 error 文案。
    last_error      TEXT NOT NULL DEFAULT '',
    -- pending / sent / failed
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 排期扫描用：worker 每轮只捞 status='pending' 且 next_retry_at <= now 的行。
-- 组合索引把过滤条件和排序列放在一起，避免捞出整表再在 Go 里筛。
CREATE INDEX IF NOT EXISTS idx_notify_retry_pending
    ON notify_retry_queue(status, next_retry_at);

-- 管理台「补发队列」列表按状态倒序展示。
CREATE INDEX IF NOT EXISTS idx_notify_retry_created
    ON notify_retry_queue(created_at DESC);