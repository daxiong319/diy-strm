-- Emby Webhook 事件去重表：同一事件（event + item id + 播放会话）只处理一次
CREATE TABLE IF NOT EXISTS emby_webhook_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_key   TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_emby_webhook_events_key ON emby_webhook_events(event_key);

CREATE INDEX IF NOT EXISTS idx_emby_webhook_events_created ON emby_webhook_events(created_at);
