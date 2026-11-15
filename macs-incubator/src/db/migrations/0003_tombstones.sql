-- M2.6 §6.4.3.3：取消墓碑持久化（孵化器重启后取消失效防护；24h 保留）
-- 兜底不变量：墓碑丢失时塔侧已 cancelled，迟到结果被条件核销拒绝——墓碑只省掉无意义进程启动
CREATE TABLE IF NOT EXISTS cancel_tombstones (
  subtask_id TEXT PRIMARY KEY,
  max_attempt INTEGER NOT NULL,
  created_at  TEXT NOT NULL
);
