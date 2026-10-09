-- T08 · 用户与权限 RBAC（Muvyo 移植②）。
--
-- 背景：internal/adminauth/ 只有「一个管理员账号」的概念（admin_username /
-- admin_password 两个配置项）。一家人共用一个账号，谁改了什么没有 accountability，
-- 也没有「只让家人加订阅、不让他删文件」这种粒度。T09 求片中心、T10 分享页、
-- T18 Telegram Bot 都以「区分谁是谁」为前置。
--
-- 本迁移给出六张表。前五张照 muvyo 的 RBAC 五表（mv_permissions /
-- mv_user_groups / mv_user_group_members / mv_group_permissions /
-- mv_user_permission_overrides），第六张 rbac_users 是 litepan 特有：
-- muvyo 的用户池在其余系统里，litepan 在此之前根本没有用户表。
--
-- 语义（原样落地，不做简化）：
--   1. 组权限矩阵 + 用户级三态覆盖（继承/允许/拒绝）
--   2. 拒绝优先级最高：用户级 deny 压过任何组权限，也压过用户级 allow
--   3. 组级拒绝同样压过用户级 allow
--      —— 因此 rbac_group_permissions 带 effect 列（allow/deny），
--         而不是任务书里写的纯 (group_id, permission_key) 二元组。
--         纯二元组只有「授予」，没有「拒绝」，第 2 条与验收真值表的
--         「组拒绝 + 用户级允许 → 拒绝」就都无法表达。effect 是任务书
--         描述的超集，不改变二元组主键语义。
--   4. 超级管理员绕过一切检查，且不可停用/不可删除/不可改权限
--   5. 加订阅与离线下载始终只有超管能做（见 internal/rbac 的硬编码集合）
--   6. 没有权限的菜单不出现在侧边栏，直接输网址也进不去
--
-- 超管为什么不在 rbac_users 里落行：
--   存量管理员的用户名密码是 admin_username / admin_password 两个配置项，
--   把它搬进 rbac_users 就要迁移密码哈希，迁移失败等于把所有人锁在门外。
--   所以超管是**虚拟主体**（UserID=0 哨兵），身份判定就是
--   「本次会话的用户名等于当前 admin_username」。这样存量部署零迁移即成超管，
--   而且它在数据层面根本不是一行可被启停或删除的记录，「不可停用/删除/改权限」
--   由构造保证而不是靠校验拦。
--
-- ⚠️ 本仓的 splitStatements（internal/store/migrate.go:180）是直接按分号切语句的，
-- 不识别注释。所以**本文件任何一行注释里都不能出现分号**，否则那一段会被当成 SQL 切出去
-- 并报语法错误。

-- rbac_users 受管用户。密码存哈希（pkg/security 的 pbkdf2/scrypt 串），
-- 不存明文。id=1 起是自增，超管的 UserID=0 不在这张表里。
CREATE TABLE IF NOT EXISTS rbac_users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL DEFAULT '',
    username_fold TEXT    NOT NULL DEFAULT '',
    display_name  TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,
    last_login_at DATETIME,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 用户名唯一。SQLite 的 UNIQUE 约束对大小写敏感，而登录名是按大小写不敏感
-- 比较的，所以另外建一个 username_fold 小写列做唯一约束，避免
-- 「Alice」与「alice」两条记录而登录时只认先建的那个。
CREATE UNIQUE INDEX IF NOT EXISTS idx_rbac_users_username
    ON rbac_users(username);
CREATE UNIQUE INDEX IF NOT EXISTS idx_rbac_users_username_fold
    ON rbac_users(username_fold);
CREATE INDEX IF NOT EXISTS idx_rbac_users_enabled
    ON rbac_users(enabled, id);

-- rbac_permissions 权限项字典。内容由 internal/rbac 的目录代码幂等写入，
-- 不在本文件里 INSERT —— 因为「哪些权限超管专属」是硬编码在 Go 里的，
-- 两处都写一份必然漂移。
CREATE TABLE IF NOT EXISTS rbac_permissions (
    key        TEXT    PRIMARY KEY,
    label      TEXT    NOT NULL DEFAULT '',
    category   TEXT    NOT NULL DEFAULT '',
    super_only INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS rbac_user_groups (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL DEFAULT '',
    description TEXT    NOT NULL DEFAULT '',
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_rbac_user_groups_name
    ON rbac_user_groups(name);
CREATE INDEX IF NOT EXISTS idx_rbac_user_groups_builtin
    ON rbac_user_groups(builtin, id);

CREATE TABLE IF NOT EXISTS rbac_group_members (
    group_id   INTEGER NOT NULL DEFAULT 0,
    user_id    INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_rbac_group_members_user
    ON rbac_group_members(user_id, group_id);

CREATE TABLE IF NOT EXISTS rbac_group_permissions (
    group_id       INTEGER NOT NULL DEFAULT 0,
    permission_key TEXT    NOT NULL DEFAULT '',
    effect         TEXT    NOT NULL DEFAULT 'allow',
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, permission_key)
);

CREATE INDEX IF NOT EXISTS idx_rbac_group_permissions_key
    ON rbac_group_permissions(permission_key);

CREATE TABLE IF NOT EXISTS rbac_user_overrides (
    user_id        INTEGER NOT NULL DEFAULT 0,
    permission_key TEXT    NOT NULL DEFAULT '',
    effect         TEXT    NOT NULL DEFAULT '',
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, permission_key)
);

CREATE INDEX IF NOT EXISTS idx_rbac_user_overrides_key
    ON rbac_user_overrides(permission_key);
