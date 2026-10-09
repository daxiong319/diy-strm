-- T27 · 一级分类表化（C-3）
--
-- 一张目录白名单表：把「一级/二级/三级分类目录名」登记成可查询的只读目录，
-- 供洗版规则筛选、清理保护等跨模块消费者使用。
--
-- **权威源是 settings.KeyMOClassificationConfig 里的模板 JSON**，本表是它的派生投影，
-- 不是第二份真相：Service.ListActiveCategories 每次读配置后按指纹 upsert 本表，
-- 配置删掉的分类也会从本表删除。理由是分类目录的身份由模板里的 Condition 与
-- Children 决定，模板才是用户编辑的对象；反过来从一张投影表反推模板会把
-- 「投影没同步」变成「用户配的分类凭空消失」。
--
-- 存量库不预置任何行：分类目录在启用分类整理之前根本不存在，
-- 预置两行会让人在界面上看到「库里有的分类」而实际上没有任何规则会用它。

CREATE TABLE IF NOT EXISTS classify_primary_categories (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  -- 层级：1 / 2 / 3。系列目录不入表（它的身份由 series_keywords 决定，名字不唯一）。
  level       INTEGER NOT NULL CHECK (level IN (1, 2, 3)),
  -- 目录名（一级/二级/三级在各自模板里的 Name 原样）。
  name        TEXT    NOT NULL,
  -- 稳定标识：<template>/<父路径>/<本级名>，模板改名会换掉 slug。
  slug        TEXT    NOT NULL,
  -- 所属模板：media / region / genre / custom。
  template    TEXT    NOT NULL,
  -- 一级分类的类型键（movie / tv），二级三级为 NULL。
  primary_key TEXT,
  -- 从模板根到本级的目录名路径，例如 "电影/国产"。
  path        TEXT    NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  -- 模板 JSON 的指纹。配置一变指纹就变，只有指纹不同才重投影，
  -- 避免每次打开设置页都把所有行 UPDATE 一遍把 updated_at 刷成噪声。
  fingerprint TEXT    NOT NULL,
  created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
  updated_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- 同一条分类在同一个指纹下只能有一行。
CREATE UNIQUE INDEX IF NOT EXISTS idx_classify_primary_categories_slug
  ON classify_primary_categories (fingerprint, slug);

-- 消费者最常见的两个查询：按层级取、slug 定位。
CREATE INDEX IF NOT EXISTS idx_classify_primary_categories_level
  ON classify_primary_categories (level);

CREATE INDEX IF NOT EXISTS idx_classify_primary_categories_path
  ON classify_primary_categories (path);

-- 保证 path 的每一段都是单个目录名，不含 "/"。
--
-- ⚠️ 这里刻意**不建触发器**。internal/store/migrate.go 的 splitStatements
-- 按分号切语句且不识别注释，触发器的 BEGIN / END 体一旦被切开就是一条
-- SQL 语法错误（实测报 "incomplete input"），整个 Migrate 失败、服务起不来。
-- path 的合法性改在 Go 侧拦：classifyorganize.validatePathSegment 本来就是
-- 目录名的唯一校验入口，一级二级三级目录名在进模板时已经过它，
-- 投影层再过一次就行，不需要在 SQL 里重复一遍。