-- M2 垂直切片：任务 / 子任务（DAG 最小闭环）
-- 任务 = 一次用户目标；子任务 = 拆解后可独立分发的执行单元
CREATE TABLE IF NOT EXISTS tasks (
  id         TEXT PRIMARY KEY,
  goal       TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending', -- pending/running/completed/failed
  progress   INTEGER NOT NULL DEFAULT 0,      -- 0~100，按已完成子任务计
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS subtasks (
  id           TEXT PRIMARY KEY,
  task_id      TEXT NOT NULL,
  seq          INTEGER NOT NULL DEFAULT 0,
  title        TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL DEFAULT '{}',    -- 下发给 worker 的任务定义（JSON）
  depends_on   TEXT NOT NULL DEFAULT '',      -- 逗号分隔前置 subtask_id（DAG 边）
  status       TEXT NOT NULL DEFAULT 'pending', -- pending/dispatched/running/completed/failed
  incubator_id TEXT NOT NULL DEFAULT '',
  worker_id    TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT,
  updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_subtasks_task ON subtasks(task_id);
CREATE INDEX IF NOT EXISTS idx_subtasks_status ON subtasks(status);
