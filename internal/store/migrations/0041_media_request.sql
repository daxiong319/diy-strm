-- T09 · 求片中心（参考实现 移植⑨）。
--
-- 背景：litepan 到 T08 为止，订阅只能由「登录后台的人」手动添加。一家人的典型场景是
-- 奶奶在电视上看到一部剧，想看，但没人会打开管理台 —— 参考实现 的做法是给家人一个
-- 只在手机上打开的「求片站」，她搜一下、点一下，剩下的交给订阅流水线自动完成。
--
-- 本迁移给出四张表，与任务书列出的四张一一对应。参考实现 那边还有一张
-- media_request_round（每次「再求一次」记一行）与一张 media_request_follower，
-- 本期不移植：前者是给「追更到第几轮」这类精细流程用的，本期一条请求就是一次求片；
-- 后者的 tag_sync_* 字段是「给 Emby 打标签失败后重试」用的，本期 litepan 不改 Emby
-- 侧标签（T07 洗版已经明确「Plex 不支持洗版」，标签能力同理不外扩）。
--
-- 语义（原样落地，不做简化）：
--   1. 提交 →（可开审核）→ 通过 → 自动建成资源订阅 → 走**已有**的订阅流水线
--      （T03 身份校验 / T04 账本 / T05 多源自动生效）。不另起一条执行路径。
--   2. 每人每天求片上限、每人同时待审上限，超出给明确错误而不是静默丢弃。
--   3. 入库标签每人最多 20 个、每个最多 100 字。
--   4. 标签挂在「整部剧」上，不按季隔离。
--
-- ⚠️ 本仓的 splitStatements（internal/store/migrate.go:180）是直接按分号切语句的，
-- 不识别注释。所以**本文件任何一行注释里都不能出现分号**，否则那一段会被当成 SQL
-- 切出去并报语法错误。

-- media_requests 求片单。
--
-- 列在任务书列出的那一组之外，本表多了 season / original_title / year /
-- poster_url / subscription_id 五列。理由逐条写在这里而不是留给后来人猜：
--   - season：任务书的列表没写它，但 参考实现 的 MediaRequest 有（season + 0..200），
--     schema 的 SubmitMediaRequest 也有。一部剧「第 2 季还没出」是求片最常见的理由，
--     没有 season 这一列就只能表达「我想要这部剧」这种弱需求。
--   - original_title / year / poster_url：纯展示字段。求片站是手机页面，列表里
--     没有年份和封面就分不清同名的两部片，而这几个字段在搜索结果里已经拿到了，
--     不存下来等于每次展示都再查一次 TMDB。
--   - subscription_id：本单建成的那条资源订阅。存它是为了「这条求片到底有没有接上
--     流水线」这个问题有据可查，也让「已建成 / 没建成」可以显示在审核列表上，
--     而不是只能靠翻订阅表猜。
CREATE TABLE IF NOT EXISTS media_requests (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    -- requester_id 指向 rbac_users.id。0 表示提交者当时是超管（虚拟主体，
    -- 与 T08 的 SuperUserID 哨兵一致）—— 超管在 rbac_users 里没有行。
    requester_id   INTEGER NOT NULL DEFAULT 0,
    requester_name TEXT    NOT NULL DEFAULT '',
    tmdb_id        INTEGER NOT NULL DEFAULT 0,
    title          TEXT    NOT NULL DEFAULT '',
    original_title TEXT    NOT NULL DEFAULT '',
    -- media_type 取 movie / tv。person（人物）不开放求片。
    media_type     TEXT    NOT NULL DEFAULT '',
    -- season 仅 media_type=tv 有意义：0 表示整部剧都要，n 表示点名要第 n 季。
    season         INTEGER NOT NULL DEFAULT 0,
    year           INTEGER NOT NULL DEFAULT 0,
    poster_url     TEXT    NOT NULL DEFAULT '',
    -- status: pending（待审） / approved（已通过） / rejected（已驳回） /
    --          fulfilled（订阅已跑完、东西入库了）
    status         TEXT    NOT NULL DEFAULT 'pending',
    notes          TEXT    NOT NULL DEFAULT '',
    -- tags 是**本单提交时**的标签快照（JSON 字符串数组）。
    -- 与 media_request_tags 分开存，是因为「某次求片当时要什么」和
    -- 「这部剧累计有什么标签」是两个不同的问题：前者是历史，后者是入库用的现值。
    tags           TEXT    NOT NULL DEFAULT '[]',
    created_at     DATETIME,
    reviewed_at    DATETIME,
    -- reviewer_id 同 requester_id：0 = 超管审核。NULL = 还没审。
    reviewer_id    INTEGER,
    reviewer_name  TEXT    NOT NULL DEFAULT '',
    reject_reason  TEXT    NOT NULL DEFAULT '',
    subscription_id INTEGER,
    -- reviewed_note 审核备注（通过时也可以写一句给提交者看的话）。
    reviewed_note  TEXT    NOT NULL DEFAULT ''
);

-- 「我的求片」列表：某人按时间倒序翻自己提过的。索引列顺序与查询形状一致
-- （先按人筛，再按时间排），否则 SQLite 只能先按时间排序再过滤。
CREATE INDEX IF NOT EXISTS idx_media_requests_requester
    ON media_requests(requester_id, created_at);
-- 审核队列：按状态取待审。这一列单独建索引而不是让它吃
-- idx_media_requests_requester 前导列，是因为待审列表要跨所有提交者查询。
CREATE INDEX IF NOT EXISTS idx_media_requests_status
    ON media_requests(status);
-- 「这部是不是已经有人求过了」：按作品身份查。
CREATE INDEX IF NOT EXISTS idx_media_requests_identity
    ON media_requests(tmdb_id, media_type);

-- 同一部作品**待审期间**只允许一条求片单。
--
-- 为什么是 partial index：参考实现 在 (media_type, tmdb_id, season) 上建了无条件唯一索引，
-- 也就是说这部片一旦有人求过，别人就永远不能再求（驳回之后也不行），家人只能靠
-- 「再求一次」按钮而那条路径本仓还没有。照搬那个约束等于把「驳回」变成终局。
-- 这里改成只约束待审状态：驳回或已完成之后可以再求。
--
-- 为什么**不含 season**：标签刻意挂在整部剧上（见下面 media_request_tags 的说明），
-- 同一剧的第 1 季与第 3 季走的是同一条订阅（entity_key=tmdb:tv:<id>），
-- 让两季各排一条待审单只会让提交者以为「两件事分开处理」，实际只有一条订阅。
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_requests_pending_identity
    ON media_requests(tmdb_id, media_type)
    WHERE status = 'pending';

-- media_request_tags 入库标签（整部剧维度）。
--
-- ⚠️ **这张表刻意没有 season 列。**
--
-- 参考实现 那边 movie / tv 都能标标签，媒体服务器扫到作品就追加，失败会重试
-- （MediaRequestFollower.tag_sync_state 就是干这个的）。任务书点名要照搬的
-- 产品决策是：「**电视剧的标签标在整部剧上，不按季隔离** —— 用户对『第 2 季』的
-- 标签不该只挂在第 2 季」。
--
-- 反过来说，如果这里加了 season，电视剧一个请求就要写 N 行（N=季数），
-- 而「第 2 季入库时才补上标签」这件事必须在代码里额外维护一套对齐逻辑：
-- 季数会变（加播一季）、请求可能只点名某一季、不同步的季还没上映。少一个字段，
-- 少一整类对不齐的 bug —— 入库第 1 季和第 3 季时读到的必然是同一份标签。
--
-- 这一条是刻意的，不是遗漏。
CREATE TABLE IF NOT EXISTS media_request_tags (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    tmdb_id      INTEGER NOT NULL DEFAULT 0,
    media_type   TEXT    NOT NULL DEFAULT '',
    requester_id INTEGER NOT NULL DEFAULT 0,
    tag          TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME
);

-- 同一作品、同一个人、同一个标签只存一行：重复提交时走 upsert 而不是追加，
-- 免得「简体中文,中字」提交三次就变成三条一样的记录，把 20 个标签的额度吃光。
CREATE UNIQUE INDEX IF NOT EXISTS idx_media_request_tags_identity
    ON media_request_tags(tmdb_id, media_type, requester_id, tag);
-- 入库侧按作品读全部人的标签（Emby 打标签时不区分是谁提的），这是主力查询形状。
CREATE INDEX IF NOT EXISTS idx_media_request_tags_lookup
    ON media_request_tags(tmdb_id, media_type);

-- media_request_rules 求片规则。
--
-- 存在的理由是「每人每天求片上限」这件事本身也要能被调，而调它的入口不应该和
-- 改代码同级别 —— 上限写在配置里改一次要重启，规则表改一行即时生效。
--
-- 一条规则匹配的条件：media_type 为空表示不限类型，为空 applies_to_user_id 表示
-- 不限人。命中多条时取 priority 大的那条（并列取 id 大的），也就是「后建的、
-- 优先级高的覆盖先建的」。
--
-- ⚠️ 这里存的是**上限与自动通过**这类全局性策略，不存「谁负责审」
-- （参考实现 的 assigned_user_id/group_id 指向它的审核工作流，那是另一套排班机制，
-- 本期审核就是管理台里谁看到谁审，见 internal/mediarequest/service.go 的 Review）。
CREATE TABLE IF NOT EXISTS media_request_rules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL DEFAULT '',
    media_type    TEXT    NOT NULL DEFAULT '',
    -- daily_limit 与 pending_limit 的 0 = 不限（不是「一次都不能求」）。
    daily_limit   INTEGER NOT NULL DEFAULT 0,
    pending_limit INTEGER NOT NULL DEFAULT 0,
    -- auto_approve 命中即跳过审核，提交即建订阅。
    auto_approve  INTEGER NOT NULL DEFAULT 0,
    applies_to_user_id INTEGER,
    enabled       INTEGER NOT NULL DEFAULT 1,
    priority      INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 规则命中查询：按 enabled + priority 倒序扫出候选，再在内存里按
-- media_type / applies_to_user_id 过滤。规则条数是个人级别（几十条），
-- 全表扫一次比多维护两个索引便宜，而索引每个都要付写入代价。
CREATE INDEX IF NOT EXISTS idx_media_request_rules_enabled
    ON media_request_rules(enabled, priority);

-- media_request_analytics 求片统计。
--
-- 一天一行 × 一个人 × 一种类型，同一格用 UPSERT 累加。为什么不每次统计都去
-- count(*) 扫 media_requests：待审列表已经要为审核者排一次序了，统计再扫一遍
-- 是白白的第二遍全表。写入只发生在提交与审核这两个低频动作上。
CREATE TABLE IF NOT EXISTS media_request_analytics (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    -- day 是本地日期（YYYY-MM-DD），不是 UTC 日。
    -- 求片发生在晚上 23 点的人，他的「今天」按 UTC 算会落到明天。
    day             TEXT    NOT NULL DEFAULT '',
    requester_id    INTEGER NOT NULL DEFAULT 0,
    media_type      TEXT    NOT NULL DEFAULT '',
    submitted_count INTEGER NOT NULL DEFAULT 0,
    approved_count  INTEGER NOT NULL DEFAULT 0,
    rejected_count  INTEGER NOT NULL DEFAULT 0,
    fulfilled_count INTEGER NOT NULL DEFAULT 0,
    updated_at      DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_request_analytics_slot
    ON media_request_analytics(day, requester_id, media_type);