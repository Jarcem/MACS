-- V3.5.7：规范化建表（数据库设计 §2.3/§2.4/§2.1 列级口径落地）
-- subtask_dependencies：依赖规范化（与 subtasks.depends_on 逗号列双写，uk 约束防重）
CREATE TABLE IF NOT EXISTS subtask_dependencies (
  subtask_id     TEXT NOT NULL,
  depends_on_id  TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  UNIQUE(subtask_id, depends_on_id)
);
CREATE INDEX IF NOT EXISTS idx_subdep_dep ON subtask_dependencies(depends_on_id);

-- parallel_groups：并行执行组（同层可并行子任务，§2.4）
CREATE TABLE IF NOT EXISTS parallel_groups (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL,
  depth      INTEGER NOT NULL,               -- 依赖层数（同层可并行）
  subtask_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pgroups_task ON parallel_groups(task_id, depth);

-- incubator_resources：资源画像历史快照（§2.1，负载均衡评分与容量趋势依据）
CREATE TABLE IF NOT EXISTS incubator_resources (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  incubator_id  TEXT NOT NULL,
  cpu_cores     INTEGER,
  mem_alloc_mb  INTEGER,
  mem_sys_mb    INTEGER,
  active_workers INTEGER,
  max_workers   INTEGER,
  load_ratio    REAL,
  reported_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_incres_inc ON incubator_resources(incubator_id, reported_at);
