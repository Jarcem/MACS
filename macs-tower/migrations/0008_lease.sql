-- M2.6 §6.4.3.1：子任务租约（lease_expires_at）与在途租约索引
-- lease_timeout = min(max(subtask.timeout_ms, 120s), 600s) + 30s（claim 时计算写入）
ALTER TABLE subtasks ADD COLUMN lease_expires_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_subtasks_lease ON subtasks(status, lease_expires_at);
