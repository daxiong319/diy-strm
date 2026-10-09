-- 三级分类 + 系列目录 + 结构化条件字段（T02）。
--
-- 本迁移只做一件事：给 classify_rules 补一个 SQL 侧的建表兜底。
--
-- 这张表历史上由 GORM AutoMigrate（classifyorganize.EnsureClassifySchema，
-- 唯一调用方 internal/app/wire_http.go）创建，不在任何 SQL 迁移里。缺了它，
-- 一台 settings 正常但表没建出来的实例上，分类规则的 YAML 导入会静默失败
-- —— 规则读不到，用户以为导入成功，分类结果却一点没变。症状是「配置不报错
-- 但就是不起作用」，排查成本极高，所以用 IF NOT EXISTS 补齐，字段与
-- ClassifyRule 结构体逐列对齐。
--
-- 三级规则与系列规则**不**建表：它们是「某个模板的第几层目录」和「命中后多挂的一段」，
-- 跟着模板走，存在 settings.KeyMOClassificationConfig 的 JSON 里
-- （三级落在 Rule.Children 的第三层与 Rule.Fields，系列落在 Config.Series）。
-- 换模板就该跟着换，单独立表反而会出现两份真相。
--
-- 分类结果不落库：总纲「本期一律不动」第 2 条，所以本迁移没有任何记录命中结果的表。
--
-- ⚠️ 注释里不能出现分号 —— internal/store/migrate.go 的 splitStatements
-- 按分号切语句且不识别注释。

CREATE TABLE IF NOT EXISTS classify_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type  TEXT    NOT NULL DEFAULT '',
    target_path TEXT    NOT NULL DEFAULT '',
    -- 用户手写的表达式数组（JSON）。结构化字段的落点。
    conditions  TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    position    INTEGER NOT NULL DEFAULT 0,
    remark      TEXT    NOT NULL DEFAULT '',
    created_at  DATETIME,
    updated_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_classify_rules_position ON classify_rules(position, id);