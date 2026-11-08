-- M1 最小云端 Schema（SQLite/PG 双兼容子集，完整 19 表口径见《数据库存储设计》§2）
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- 模块 #1/#4：孵化器注册与集群状态
CREATE TABLE IF NOT EXISTS incubators (
  id             TEXT PRIMARY KEY,
  name           TEXT UNIQUE NOT NULL,
  status         TEXT NOT NULL DEFAULT 'online',  -- online/offline/draining
  capacity       INTEGER NOT NULL DEFAULT 4,      -- Q4 并发上限
  platforms      TEXT NOT NULL DEFAULT '[]',      -- JSON
  drivers        TEXT NOT NULL DEFAULT '[]',      -- JSON（能力协商 §6.4.13.6）
  listen_port    INTEGER NOT NULL DEFAULT 0,
  last_ip        TEXT NOT NULL DEFAULT '',
  last_heartbeat TEXT,
  registered_at  TEXT NOT NULL
);

-- 模块 #3：调度审计（msg_id 幂等键 UNIQUE）
CREATE TABLE IF NOT EXISTS dispatch_log (
  id            TEXT PRIMARY KEY,
  subtask_id    TEXT NOT NULL,
  incubator_id  TEXT,
  msg_id        TEXT UNIQUE NOT NULL,
  ack_status    TEXT NOT NULL DEFAULT 'pending', -- pending/acked/timeout/driver_mismatch
  dispatched_at TEXT NOT NULL,
  acked_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_dispatch_subtask ON dispatch_log(subtask_id);
