-- MCP Server 与内置智能助理。
--
-- 配置本体沿用既有 configs(key,value) 键值表（见 internal/settings 与
-- internal/store/config_repo.go），不另建单行配置表：本项目的设置一律走
-- 声明式注册表 + configs 表，MCP 设置没有理由破例，这样也能随备份一起导入导出。
-- 因此这里只需要为「对话历史」建表。
--
-- 对齐老版 internal/models/mcp_chat.go 的 mcp_chat_messages：一条消息一行，
-- 而不是把整段会话塞进一个 JSON 字段 —— 这样长会话可以按 session_id + id
-- 分页，且单条写入失败不会丢掉整段历史。会话列表由聚合查询派生，
-- 不单独建会话表（避免「删了消息却忘了会话行」的不一致）。

CREATE TABLE IF NOT EXISTS mcp_chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL DEFAULT '',
    tool_calls TEXT NOT NULL DEFAULT '[]',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_mcp_chat_session ON mcp_chat_messages(session_id, id);
CREATE INDEX IF NOT EXISTS idx_mcp_chat_updated ON mcp_chat_messages(updated_at DESC);
