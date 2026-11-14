-- M3~M8 云端 Schema（SQLite/PG 双兼容子集）
-- 口径：《数据库存储设计》§2.5 审批 / §2.6 抽查 / §2.7 追溯链 / §2.8 归档 / §2.9 模板
--       §2.10a 统一协议三表 / §2.10b 遥测 / §2.10 成本与预算 / §2.10c 命令终端·AdminToken·通知渠道
-- 迁移必须幂等可重复（8.1）；新增列一律带默认值（10.1 向前兼容）

-- ─────────────────────────────────────────────
-- M2 补充：任务预算 / 子任务质量管线列（附录 B.4 状态全集）
-- ─────────────────────────────────────────────
ALTER TABLE tasks ADD COLUMN title         TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN budget_cents  REAL NOT NULL DEFAULT 0;   -- 6.4.11 任务级预算（0=不限）

ALTER TABLE subtasks ADD COLUMN kind          TEXT NOT NULL DEFAULT 'execute'; -- execute/review/merge
ALTER TABLE subtasks ADD COLUMN review_of     TEXT NOT NULL DEFAULT '';        -- 复核子任务指向原子任务
ALTER TABLE subtasks ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;      -- E01 已执行次数
ALTER TABLE subtasks ADD COLUMN max_retries   INTEGER NOT NULL DEFAULT 3;      -- Q5/E01 重试上限
ALTER TABLE subtasks ADD COLUMN next_retry_at TEXT NOT NULL DEFAULT '';        -- 退避到期时刻
ALTER TABLE subtasks ADD COLUMN review_round  INTEGER NOT NULL DEFAULT 0;      -- E04 复核打回轮次
ALTER TABLE subtasks ADD COLUMN quality_status TEXT NOT NULL DEFAULT '';       -- self_checking/review_pending/reviewing/in_spot_check
ALTER TABLE subtasks ADD COLUMN fingerprint   TEXT NOT NULL DEFAULT '';        -- sha256:<hex>（6.4.4）
ALTER TABLE subtasks ADD COLUMN template_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE subtasks ADD COLUMN required_driver TEXT NOT NULL DEFAULT '';     -- 6.4.13.6 能力协商
CREATE INDEX IF NOT EXISTS idx_subtasks_retry ON subtasks(status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_subtasks_review_of ON subtasks(review_of);

-- ─────────────────────────────────────────────
-- 孵化器资源画像与负载（6.4.7 评分 / rebalanceTick）
-- ─────────────────────────────────────────────
ALTER TABLE incubators ADD COLUMN active_workers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE incubators ADD COLUMN last_load      REAL    NOT NULL DEFAULT 0;   -- 0~1，report 上行
ALTER TABLE incubators ADD COLUMN tags           TEXT    NOT NULL DEFAULT '[]';

-- dispatch_log 按 6.4.8 补列（attempt_count / ack_latency）
ALTER TABLE dispatch_log ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE dispatch_log ADD COLUMN dispatch_mode TEXT NOT NULL DEFAULT 'targeted'; -- targeted/broadcast/notice
ALTER TABLE dispatch_log ADD COLUMN last_sent_at  TEXT NOT NULL DEFAULT '';

-- ─────────────────────────────────────────────
-- §2.5 审批门控（FR-19）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS approvals (
  id            TEXT PRIMARY KEY,
  task_id       TEXT,
  subtask_id    TEXT,
  incubator_id  TEXT,
  approval_type TEXT NOT NULL,              -- acceptance_confirm/start_execution/human_final/subtask_plan/merge_result/budget_exceed/guardrail/danger_op
  status        TEXT NOT NULL DEFAULT 'pending', -- pending/approved/rejected/expired
  requester     TEXT NOT NULL DEFAULT 'system',
  approver      TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  payload       TEXT NOT NULL DEFAULT '{}',
  expires_at    TEXT,
  decision      TEXT,
  decision_note TEXT,
  decided_at    TEXT,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_approvals_task    ON approvals(task_id);
CREATE INDEX IF NOT EXISTS idx_approvals_status  ON approvals(status);
CREATE INDEX IF NOT EXISTS idx_approvals_type    ON approvals(approval_type);

-- ─────────────────────────────────────────────
-- §2.6 抽查核验（FR-16，Q7 默认抽样 20%）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS spot_checks (
  id              TEXT PRIMARY KEY,
  task_id         TEXT NOT NULL,
  subtask_id      TEXT NOT NULL DEFAULT '',
  incubator_id    TEXT NOT NULL DEFAULT '',
  check_scope     TEXT NOT NULL DEFAULT 'task',   -- task/subtask/sample
  status          TEXT NOT NULL DEFAULT 'pending', -- pending/running/passed/failed/retried/skipped
  check_items     TEXT NOT NULL DEFAULT '[]',     -- JSON [{name,passed,note}]
  pass_rate       REAL,
  overall_passed  INTEGER,
  attempt_count   INTEGER NOT NULL DEFAULT 1,
  max_attempts    INTEGER NOT NULL DEFAULT 3,
  next_attempt_at TEXT NOT NULL DEFAULT '',
  last_attempt_at TEXT NOT NULL DEFAULT '',
  note            TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_spot_checks_task   ON spot_checks(task_id);
CREATE INDEX IF NOT EXISTS idx_spot_checks_status ON spot_checks(status);

-- ─────────────────────────────────────────────
-- §2.7 追溯链（FR-17，E06 防篡改指纹链）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS origin_traces (
  id              TEXT PRIMARY KEY,
  task_id         TEXT NOT NULL,
  subtask_id      TEXT NOT NULL DEFAULT '',
  from_kind       TEXT NOT NULL DEFAULT '',
  from_id         TEXT NOT NULL DEFAULT '',
  to_kind         TEXT NOT NULL,
  to_id           TEXT NOT NULL,
  fingerprint     TEXT NOT NULL,
  fingerprint_alg TEXT NOT NULL DEFAULT 'sha256',
  content_summary TEXT NOT NULL DEFAULT '',
  artifact_type   TEXT NOT NULL DEFAULT '',
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  depth           INTEGER NOT NULL DEFAULT 0,
  metadata        TEXT NOT NULL DEFAULT '{}',
  created_at      TEXT NOT NULL,
  UNIQUE(subtask_id, fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_origin_traces_task ON origin_traces(task_id);
CREATE INDEX IF NOT EXISTS idx_origin_traces_fp   ON origin_traces(fingerprint);

-- ─────────────────────────────────────────────
-- §2.8 归档中心（FR-18，云端只存文档不存产物文件）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS archive_documents (
  id             TEXT PRIMARY KEY,
  task_id        TEXT NOT NULL,
  subtask_id     TEXT NOT NULL DEFAULT '',
  incubator_id   TEXT NOT NULL DEFAULT '',
  source         TEXT NOT NULL,                 -- worker/merger/reviewer/tower/human
  doc_type       TEXT NOT NULL,                 -- handoff/review/self_check/merge_summary/final_summary
  title          TEXT NOT NULL,
  content        TEXT NOT NULL,
  format_version TEXT NOT NULL DEFAULT '1.0',
  sections       TEXT NOT NULL DEFAULT '[]',
  word_count     INTEGER NOT NULL DEFAULT 0,
  has_code       INTEGER NOT NULL DEFAULT 0,
  version        INTEGER NOT NULL DEFAULT 1,
  is_latest      INTEGER NOT NULL DEFAULT 1,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_archive_documents_task ON archive_documents(task_id);
CREATE INDEX IF NOT EXISTS idx_archive_documents_type ON archive_documents(doc_type);

-- ─────────────────────────────────────────────
-- §2.9 Agent 模板库（FR-22）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS agent_templates (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  version             TEXT NOT NULL DEFAULT '1.0',
  description         TEXT NOT NULL DEFAULT '',
  template_type       TEXT NOT NULL DEFAULT 'executor',  -- executor/reviewer/planner/merger/custom
  adapter_type        TEXT NOT NULL DEFAULT 'builtin',
  adapter_config      TEXT NOT NULL DEFAULT '{}',
  protocol_driver     TEXT NOT NULL DEFAULT 'none',      -- none/openai_compatible/anthropic（6.4.9）
  driver_version_pin  TEXT NOT NULL DEFAULT '',
  monthly_budget_cents REAL NOT NULL DEFAULT 0,
  spent_monthly_cents REAL NOT NULL DEFAULT 0,
  instructions_md     TEXT NOT NULL DEFAULT '',
  capabilities        TEXT NOT NULL DEFAULT '[]',
  default_timeout_sec INTEGER,
  default_max_retries INTEGER,
  is_system           INTEGER NOT NULL DEFAULT 0,
  is_active           INTEGER NOT NULL DEFAULT 1,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  UNIQUE(name, version)
);

-- ─────────────────────────────────────────────
-- §2.10 成本与预算（FR-21，6.4.11 多级预算 + 塔侧上界）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cost_records (
  id           TEXT PRIMARY KEY,
  incubator_id TEXT NOT NULL DEFAULT '',
  task_id      TEXT,
  subtask_id   TEXT,
  worker_id    TEXT NOT NULL DEFAULT '',
  cost_type    TEXT NOT NULL DEFAULT 'api',   -- api/compute/storage/other
  provider     TEXT NOT NULL DEFAULT '',
  model        TEXT NOT NULL DEFAULT '',
  amount_cents REAL NOT NULL DEFAULT 0,
  token_usage  TEXT,                          -- JSONB 兼容：{prompt,completion,cached}
  duration_sec INTEGER,
  description  TEXT NOT NULL DEFAULT '',
  recorded_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cost_records_task ON cost_records(task_id);
CREATE INDEX IF NOT EXISTS idx_cost_records_inc  ON cost_records(incubator_id);

CREATE TABLE IF NOT EXISTS budget_limits (
  id            TEXT PRIMARY KEY,
  scope_type    TEXT NOT NULL,                -- global/incubator/template/task
  scope_id      TEXT,                         -- global 为 NULL
  amount_cents  REAL NOT NULL DEFAULT 0,      -- 0 = 不限
  warn_at_pct   INTEGER NOT NULL DEFAULT 80,
  enforce_mode  TEXT NOT NULL DEFAULT 'hard', -- hard/soft/off
  limit_version INTEGER NOT NULL DEFAULT 0,   -- 单调递增，孵化器仅应用更大版本（6.4.11）
  issued_at     TEXT NOT NULL,
  UNIQUE(scope_type, scope_id)
);

-- ─────────────────────────────────────────────
-- §2.10b 统一遥测事件（V3.0 §6.4.13.4，高写入日志）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS telemetry_events (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id          TEXT,
  subtask_id       TEXT,
  worker_id        TEXT NOT NULL DEFAULT '',
  platform_adapter TEXT NOT NULL DEFAULT '',
  agent_platform   TEXT NOT NULL DEFAULT '',
  event_type       TEXT NOT NULL,             -- task_start/step_progress/token_usage/completed/failed/guardrail/budget
  payload          TEXT NOT NULL DEFAULT '{}',
  timestamp        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_task ON telemetry_events(task_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_sub  ON telemetry_events(subtask_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_type ON telemetry_events(event_type);

-- 护栏事件（6.4.10：断路器/空闲下岗/唤醒环/有界上下文/done 门）
CREATE TABLE IF NOT EXISTS guardrail_events (
  id             TEXT PRIMARY KEY,
  task_id        TEXT NOT NULL DEFAULT '',
  subtask_id     TEXT NOT NULL DEFAULT '',
  incubator_id   TEXT NOT NULL DEFAULT '',
  guardrail_type TEXT NOT NULL,               -- circuit_breaker/idle_down/wake_loop/bounded_context/done_gate
  action         TEXT NOT NULL DEFAULT '',    -- frozen/destroyed/paused/flagged
  detail         TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_guardrail_events_sub ON guardrail_events(subtask_id);

-- ─────────────────────────────────────────────
-- §2.10a 统一 Agent 平台协议三表（V3.0 §6.4.13.1）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS unified_agent_platforms (
  id             TEXT PRIMARY KEY,
  platform_name  TEXT NOT NULL,
  vendor         TEXT NOT NULL DEFAULT '',
  version        TEXT NOT NULL DEFAULT '1.0',
  protocol_spec  TEXT NOT NULL DEFAULT '',
  is_active      INTEGER NOT NULL DEFAULT 1,
  is_system      INTEGER NOT NULL DEFAULT 0,
  base_url       TEXT NOT NULL DEFAULT '',
  default_driver TEXT NOT NULL DEFAULT '',
  capabilities   TEXT NOT NULL DEFAULT '[]',
  metadata       TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_uap_name ON unified_agent_platforms(platform_name);

CREATE TABLE IF NOT EXISTS protocol_drivers (
  id              TEXT PRIMARY KEY,
  driver_name     TEXT NOT NULL,
  version         TEXT NOT NULL DEFAULT '1.0',
  description     TEXT NOT NULL DEFAULT '',
  protocol_family TEXT NOT NULL DEFAULT '',   -- openai-compatible/anthropic/cli-native
  is_active       INTEGER NOT NULL DEFAULT 1,
  spec_url        TEXT NOT NULL DEFAULT '',
  message_schema  TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  UNIQUE(driver_name, version)
);

CREATE TABLE IF NOT EXISTS platform_adapter_mappings (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  platform_id        TEXT NOT NULL,
  driver_id          TEXT NOT NULL,
  adapter_name       TEXT NOT NULL,
  status             TEXT NOT NULL DEFAULT 'developing', -- developing/ready/deprecated
  compatibility_mode TEXT NOT NULL DEFAULT 'strict',
  supported_features TEXT NOT NULL DEFAULT '{}',
  config_schema      TEXT,
  created_at         TEXT NOT NULL,
  UNIQUE(platform_id, driver_id)
);
CREATE INDEX IF NOT EXISTS idx_pam_platform ON platform_adapter_mappings(platform_id);

-- ─────────────────────────────────────────────
-- §2.10c 命令终端 / Admin Token / 通知渠道（V3.5 §6.4.14~6.4.17）
-- ─────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS command_terminals (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  hostname          TEXT NOT NULL DEFAULT '',
  secret            TEXT NOT NULL,            -- 塔侧持有（签发 cmd_exec / 校验上行 HMAC 必须）；API 仅登记时一次性展示
  secret_prev       TEXT NOT NULL DEFAULT '', -- 轮换 10 分钟双验窗口（§6.4.14 A2）
  secret_prev_until TEXT NOT NULL DEFAULT '',
  allow             TEXT NOT NULL DEFAULT '',  -- 终端白名单摘要
  status            TEXT NOT NULL DEFAULT 'registered', -- registered/online/offline/revoked
  last_ip           TEXT NOT NULL DEFAULT '',
  listen_port       INTEGER NOT NULL DEFAULT 0,
  last_heartbeat    TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS command_audit (
  id          TEXT PRIMARY KEY,
  terminal_id TEXT NOT NULL,
  command     TEXT NOT NULL,
  ok          INTEGER NOT NULL,
  exit_code   INTEGER,
  msg_id      TEXT UNIQUE,                    -- cmd_result 关联键（A3 对账）
  duration_ms INTEGER,
  reject      TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_command_audit_term ON command_audit(terminal_id, created_at);

CREATE TABLE IF NOT EXISTS admin_tokens (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  token_hash TEXT UNIQUE NOT NULL,            -- sha256 hex，仅存哈希（6.4.16）
  scope      TEXT NOT NULL DEFAULT 'read',    -- read/ops/danger
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  rotated_at TEXT
);

CREATE TABLE IF NOT EXISTS notify_channels (
  id        TEXT PRIMARY KEY,
  kind      TEXT NOT NULL,                  -- webhook/wecom/smtp
  config    TEXT NOT NULL DEFAULT '{}',
  min_level TEXT NOT NULL DEFAULT 'warn',
  enabled   INTEGER NOT NULL DEFAULT 1
);
