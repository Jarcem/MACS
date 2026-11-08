-- MACS 孵化器本地库（SQLite）核心表 — 唯一事实源为《数据库存储设计》§3
-- 原则：Worker 运行时状态本地临时、用完即毁；执行状态权威在云端

CREATE TABLE IF NOT EXISTS workers (
  id            TEXT PRIMARY KEY,
  subtask_id    TEXT NOT NULL,
  adapter       TEXT,
  status        TEXT NOT NULL DEFAULT 'spawning', -- 附录 B.4 七态
  ttl_deadline  INTEGER NOT NULL,                 -- epoch ms，Q3 默认 30min
  token_total   INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  destroyed_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_workers_status ON workers(status);

CREATE TABLE IF NOT EXISTS tasks_cache (
  id          TEXT PRIMARY KEY,
  payload     TEXT NOT NULL DEFAULT '{}',  -- JSON：云端下发的任务定义+模板
  synced_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS dispatch_inbox (
  msg_id      TEXT PRIMARY KEY,            -- 幂等去重（6.4.2）
  type        TEXT NOT NULL,
  payload     TEXT NOT NULL DEFAULT '{}',
  received_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- V3.0 §3.17 遥测本地缓冲（断网续传）
CREATE TABLE IF NOT EXISTS telemetry_events_local (
  id          TEXT PRIMARY KEY,
  task_id     TEXT,
  subtask_id  TEXT,
  worker_id   TEXT,
  event_type  TEXT NOT NULL,
  payload     TEXT NOT NULL DEFAULT '{}',
  synced      INTEGER NOT NULL DEFAULT 0,  -- 0 待同步 / 1 已确认
  created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_tel_sync ON telemetry_events_local(synced, created_at);
CREATE INDEX IF NOT EXISTS idx_tel_subtask ON telemetry_events_local(subtask_id);
