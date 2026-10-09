-- T04 · 订阅幂等账本（以媒体库为真值 + 3 小时保护期）。
--
-- 背景：litepan 原来的跨轮去重是**永久去重**（discovery_subscription_items 里
-- status='transferred' 的 scope 集合），用户在网盘删掉一部剧，订阅永远不会拉回来；
-- 洗版换版本同样被挡住。参考实现 的做法是「以媒体库实际有没有为准」：删了就重转
-- （这正是用户想要的），换版本也重转，另有 3 小时保护期防止刚转完就重复动同一批文件。
--
-- 去重的真键：参考实现 用 (storage_slug, sha1)，因为文件 ID 会变（转存后变、跨盘转移后又变），
-- SHA1 才是文件的身份。litepan 的订阅走分享链接转存，**拿不到分享内文件的 SHA1**
-- （tgto123 反代只有 login/search/transfer/authorize 四个端点，转存调用把解锁与
-- 转存压成一次、落盘目录由服务端决定），所以这里落成 content_key：
--
--     sha1:<40位十六进制>   —— 有真实哈希时用（本地文件、CAS 清单等来源）
--     res:<slug>           —— 分享来源，退化为资源级身份
--
-- 唯一索引建在 (storage_slug, content_key) 上而不是任务书写的 (storage_slug, sha1)：
-- 拿不到 sha1 时那一列全是空串，唯一索引会让第二条就插入失败。
-- (storage_slug, sha1) 仍然保留为普通索引，供将来接入真实哈希后回查。
--
-- 时间戳口径：任务书用 organized_at 起算保护期，但 litepan 的整理阶段由 mediaorganize
-- 异步驱动、常常数小时后才跑甚至不跑，用它起算会让保护期形同虚设。
-- 这里用 transfer_requested_at（缺失时回落 candidate_selected_at）起算 —— 语义也更贴切：
-- 「刚转存完，账本立刻生效，但网盘写入有延迟、文件清单可能还没刷新」。
-- organized_at 仍然保留，它是「以媒体库为真值」判定时要用的标记。

CREATE TABLE IF NOT EXISTS discovery_transfer_items (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    idempotency_key        TEXT    NOT NULL UNIQUE,
    storage_slug           TEXT    NOT NULL DEFAULT '',
    content_key            TEXT    NOT NULL DEFAULT '',
    sha1                   TEXT    NOT NULL DEFAULT '',
    media_scope            TEXT    NOT NULL DEFAULT '',
    subscription_id        INTEGER NOT NULL DEFAULT 0,
    rule_id                INTEGER NOT NULL DEFAULT 0,
    title                  TEXT    NOT NULL DEFAULT '',
    state                  TEXT    NOT NULL DEFAULT 'candidate_selected',
    reason_code            TEXT    NOT NULL DEFAULT '',
    candidate_selected_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    transfer_requested_at  DATETIME,
    transfer_confirmed_at  DATETIME,
    organized_at           DATETIME,
    strm_created_at        DATETIME,
    deleted_at             DATETIME,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 去重真键：并发下由唯一索引兜底，重复插入即重复转存。
CREATE UNIQUE INDEX IF NOT EXISTS idx_transfer_items_content
    ON discovery_transfer_items(storage_slug, content_key);
-- 真实哈希来源的回查路径（task 文件指定的联合索引，此处为普通索引，理由见上）。
CREATE INDEX IF NOT EXISTS idx_transfer_items_storage_sha1
    ON discovery_transfer_items(storage_slug, sha1);
-- 「这个订阅的这个范围我判过没有」
CREATE INDEX IF NOT EXISTS idx_transfer_items_sub_scope
    ON discovery_transfer_items(subscription_id, media_scope);
-- 巡检：哪些条目已 organize、哪些还欠 strm
CREATE INDEX IF NOT EXISTS idx_transfer_items_state_organized
    ON discovery_transfer_items(state, organized_at);
