-- 0009_agent_collaboration.sql V1.5 位置无关 Agent 协作（设计 §3 身份数据模型）
-- Connector / AgentInstance / AgentAction 三级身份 + PlanRevision 冻结分派 + ExecutionAttempt + 跨机制品

CREATE TABLE IF NOT EXISTS connectors (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  credential_id TEXT NOT NULL,
  credential_hash TEXT NOT NULL,
  protocol_version TEXT NOT NULL,
  online_status TEXT NOT NULL DEFAULT 'offline',
  health_status TEXT NOT NULL DEFAULT 'unknown',
  load_json TEXT NOT NULL DEFAULT '{}',
  last_seen TEXT NOT NULL DEFAULT '',
  revoked_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_instances (
  id TEXT PRIMARY KEY,
  connector_id TEXT NOT NULL,
  local_agent_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  platform_id TEXT NOT NULL DEFAULT '',
  adapter_type TEXT NOT NULL DEFAULT '',
  role_tags TEXT NOT NULL DEFAULT '[]',
  capability_tags TEXT NOT NULL DEFAULT '[]',
  environment_tags TEXT NOT NULL DEFAULT '[]',
  identity_status TEXT NOT NULL DEFAULT 'pending',
  verified_by TEXT NOT NULL DEFAULT '',
  verified_at TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1,
  online_status TEXT NOT NULL DEFAULT 'offline',
  health_status TEXT NOT NULL DEFAULT 'unknown',
  load_json TEXT NOT NULL DEFAULT '{}',
  reported_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(connector_id, local_agent_key)
);
CREATE INDEX IF NOT EXISTS idx_agent_instances_connector ON agent_instances(connector_id);
CREATE INDEX IF NOT EXISTS idx_agent_instances_status ON agent_instances(identity_status);

CREATE TABLE IF NOT EXISTS agent_actions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  action_key TEXT NOT NULL,
  action_kind TEXT NOT NULL DEFAULT 'execute',
  adapter_type TEXT NOT NULL,
  risk_level TEXT NOT NULL DEFAULT 'normal',
  retry_policy TEXT NOT NULL DEFAULT 'retryable',
  max_attempts INTEGER NOT NULL DEFAULT 3,
  validator_keys TEXT NOT NULL DEFAULT '[]',
  config_ref TEXT NOT NULL,
  secret_refs TEXT NOT NULL DEFAULT '[]',
  enabled INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(agent_id, action_key)
);
CREATE INDEX IF NOT EXISTS idx_agent_actions_agent ON agent_actions(agent_id);

-- 任务级状态（V1.5 扩展：plan_revisions 只承载修订，任务状态/取消在此）
CREATE TABLE IF NOT EXISTS agent_tasks (
  task_id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'running',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plan_revisions (
  task_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  source TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  draft_json TEXT NOT NULL,
  catalog_digest TEXT NOT NULL,
  approved_by TEXT NOT NULL DEFAULT '',
  approved_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(task_id, revision)
);

-- 冻结分派（设计 §3.4）：批准时写入，修改必须新建修订重新审批
CREATE TABLE IF NOT EXISTS task_assignments (
  task_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  plan_revision INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  action_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  approved_by TEXT NOT NULL,
  approved_at TEXT NOT NULL,
  PRIMARY KEY(task_id, step_id, plan_revision)
);

CREATE TABLE IF NOT EXISTS execution_attempts (
  attempt_id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  plan_revision INTEGER NOT NULL,
  attempt_no INTEGER NOT NULL,
  idempotency_key TEXT NOT NULL,
  execution_context_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  action_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  status TEXT NOT NULL,
  acceptance_status TEXT NOT NULL DEFAULT 'pending',
  result_json TEXT NOT NULL DEFAULT '',
  lease_expires_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  UNIQUE(step_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_task ON execution_attempts(task_id);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_status ON execution_attempts(status);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_agent ON execution_attempts(agent_id);

-- 跨机制品（设计 §3.6 / §7）：URI 只允许 s3:// oci:// https://，digest SHA-256
CREATE TABLE IF NOT EXISTS execution_artifacts (
  id TEXT PRIMARY KEY,
  attempt_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  plan_revision INTEGER NOT NULL,
  uri TEXT NOT NULL,
  digest TEXT NOT NULL,
  kind TEXT NOT NULL,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_execution_artifacts_step ON execution_artifacts(task_id, step_id, plan_revision);
CREATE INDEX IF NOT EXISTS idx_execution_artifacts_attempt ON execution_artifacts(attempt_id);

-- 人工标注审计（设计 §10：平台标注、计划批准写审计）
CREATE TABLE IF NOT EXISTS agent_identity_audit (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  patch_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
