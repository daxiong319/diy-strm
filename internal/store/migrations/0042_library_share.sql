-- T10 · 免登录分享页（参考实现 移植⑩）。
--
-- 背景：litepan 现有的免鉴权区只覆盖「自己家里用」的路径（/cas/play、/api/strm/play、
-- /api/guanying）。缺的是把某个片发给局域网外的人（同学、同事、远程的家人），
-- 对方没有账号也能看。
--
-- 表结构对着 参考实现 的真实模型落，而不是任务书草图那张两表草图。取舍理由写在这里：
--
--   1. 草图是 library_shares + library_share_events（一张按 event_type 分类的流水）。
--      但草图表达不了验收第 4 条「同一浏览器 24 小时内多次访问只计 1 次 visitor」：
--      要去重就得有一个「这个访客是谁」的实体，流水表只能记事件、记不下「首次」。
--   2. 草图表达不了「令牌按访客签发」：草图的 token_hash 挂在分享上，
--      等于整个分享共用一个令牌，一旦泄露就只能重建整个分享。
--      参考实现 把令牌挂在 library_share_visits 上（每访客一行、token_hash 唯一），
--      泄露一个只废掉一个访客的会话，其它人照常看。
--   3. 因此落 参考实现 的三张表：library_shares / library_share_visits / library_share_plays，
--      第四张 library_share_settings（存 JSON 字典）本期不移植 ——
--      litepan 的配置项走 internal/settings/registry.go，不需要再造一个设置表。
--
-- 密码：library_shares.password_hash 存 bcrypt 风格的哈希，空串表示无口令
--（对齐 参考实现 的默认空值）。litepan 现有的口令哈希实现在 internal/adminauth，
-- 本期直接复用同一个算法，避免两套口令校验。
--
-- ⚠️ 本仓的 splitStatements（internal/store/migrate.go:180）是直接按分号切语句的，
-- 不识别注释。所以**本文件任何一行注释里都不能出现分号**，否则那一段会被当成 SQL
-- 切出去并报语法错误。

-- library_shares 一条对外分享。
--
-- code 是分享链接里的短码（明文，可分享），code_hash 是它的 SHA-256
-- （唯一索引）。两个都存是因为 code_hash 负责唯一性与比对，code 本身没有敏感性：
-- 它就像门牌号，真正的凭证是每访客单独签发的令牌。
-- 为什么不只存 code_hash：生成链接时要把 code 明文交给创建者，
-- 事后要能重新复制同一条链接（用户经常隔几天回来还在复制同一个链接发给别人）。
CREATE TABLE IF NOT EXISTS library_shares (
  id            TEXT    PRIMARY KEY,
  code          TEXT    NOT NULL,
  code_hash     TEXT    NOT NULL UNIQUE,
  account_id    INTEGER NOT NULL DEFAULT 0,
  file_id       TEXT    NOT NULL DEFAULT '',
  title         TEXT    NOT NULL DEFAULT '',
  season        INTEGER NOT NULL DEFAULT -1,
  comment       TEXT    NOT NULL DEFAULT '',
  password_hash TEXT    NOT NULL DEFAULT '',
  expires_at    TEXT,
  max_devices   INTEGER NOT NULL DEFAULT 5,
  view_count    INTEGER NOT NULL DEFAULT 0,
  play_count    INTEGER NOT NULL DEFAULT 0,
  visitor_count INTEGER NOT NULL DEFAULT 0,
  created_by    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL,
  revoked_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_library_shares_created ON library_shares(created_at);
CREATE INDEX IF NOT EXISTS idx_library_shares_revoked ON library_shares(revoked_at);

-- library_share_visits 一个访客在一条分享下的会话。
--
-- 这是「24 小时算 1 次」和「令牌按访客签发」两个语义的落点：
--   - visitor_id：访客标识。不按 IP（同一个公司/学校出口 IP 会把所有人挤成一个人，
--     而家里 Wi-Fi 的动态 IP 又会把同一个人算成很多人）。由访客首次访问时
--     随机生成并放进 localStorage，后端只拿它做去重键。
--   - token_hash：这一次会话的令牌哈希（唯一）。令牌本身只在签发那一次
--     通过响应体交给访客，数据库里没有明文。
--   - counted：这个 24 小时窗口有没有被计过一次 visitor。第 4 条验收就靠它。
--   - ip_masked：IP 脱敏后保存（保留网段，抹掉主机位）。
--     不存完整 IP 是因为这是「外人访问」的记录，比内网日志更敏感。
--     但也不能一个都不存：整条 IPv6 抹成同一个值会让统计完全失真，
--     所以保留 /64 网段。
--
-- ⚠️ (share_id, visitor_id) 是 UNIQUE，不是普通索引：**一个访客在一份分享下
-- 只有一行**。曾经这里允许同一个访客攒出很多行，理由是「24 小时窗口」
-- 看起来需要每 24 小时开一行新会话。真正的问题在并发：
-- 先查后插的写法下八个并发首访会各插一行，每行 counted=0 都去加了一次
-- visitor_count，于是 visitor 计数翻八倍，
-- 而 CountDevices 数的是 DISTINCT visitor_id 所以设备数还是 1，
-- 设备数那一面完全看不出出错了。
-- 改成「每访客一行 + UPSERT」之后，24 小时窗口不再靠「多行」表达，
-- 而是靠 counted 复位表达（见 Store.UpsertVisit 里那个 CASE WHEN），
-- 并发由 SQLite 的 UPSERT 写锁天然串行化。
CREATE TABLE IF NOT EXISTS library_share_visits (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  share_id      TEXT    NOT NULL,
  visitor_id    TEXT    NOT NULL,
  token_hash    TEXT    NOT NULL UNIQUE,
  ip_masked     TEXT    NOT NULL DEFAULT '',
  user_agent    TEXT    NOT NULL DEFAULT '',
  counted       INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL,
  last_seen_at  TEXT    NOT NULL,
  UNIQUE (share_id, visitor_id)
);

CREATE INDEX IF NOT EXISTS idx_library_share_visits_share
  ON library_share_visits(share_id, visitor_id);
CREATE INDEX IF NOT EXISTS idx_library_share_visits_seen
  ON library_share_visits(share_id, last_seen_at);

-- library_share_plays 一次实际的取流。
--
-- 与 visits 分开是因为「打开页面」和「真的开始放」是两个动作：
-- 打开页面的人可能在密码框前就走了。参考实现 也是这么分的。
-- method 留 direct（litepan 目前只有直链/代理两种取流方式，没有第三种）。
CREATE TABLE IF NOT EXISTS library_share_plays (
  id           TEXT    PRIMARY KEY,
  share_id     TEXT    NOT NULL,
  visit_id     INTEGER NOT NULL DEFAULT 0,
  visitor_id   TEXT    NOT NULL DEFAULT '',
  file_id      TEXT    NOT NULL DEFAULT '',
  item_label   TEXT    NOT NULL DEFAULT '',
  method       TEXT    NOT NULL DEFAULT 'direct',
  ip_masked    TEXT    NOT NULL DEFAULT '',
  watched_secs INTEGER NOT NULL DEFAULT 0,
  started_at   TEXT    NOT NULL,
  last_seen_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_library_share_plays_share
  ON library_share_plays(share_id, started_at);
CREATE INDEX IF NOT EXISTS idx_library_share_plays_started
  ON library_share_plays(started_at);