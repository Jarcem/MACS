-- V3.5.4：6.4.5 离线缓冲指数退避 + Q6 14 天保留
ALTER TABLE telemetry_events_local ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE telemetry_events_local ADD COLUMN next_retry_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_tel_retry ON telemetry_events_local(synced, next_retry_at);
