// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 管理后台 API 客户端（主设计附录 B.1；dev 期由 vite proxy 转发到 8471）
async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}` } });
  if (!res.ok) throw new Error(`GET ${path} -> ${res.status}`);
  return (await res.json()) as T;
}

async function post<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}`,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`POST ${path} -> ${res.status} ${text}`);
  }
  return (await res.json()) as T;
}

async function put<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}` },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`PUT ${path} -> ${res.status} ${text}`);
  }
  return (await res.json()) as T;
}

// 列表端点解包：契约响应 { data: [...], page, pageSize }
async function getList<T>(path: string): Promise<T[]> {
  const r = await get<{ data?: T[] } | T[]>(path);
  return Array.isArray(r) ? r : (r?.data ?? []);
}

export interface Incubator {
  ID: string; Name: string; Status: string; Capacity: number;
  Platforms: string; Drivers: string; ListenPort: number;
  LastIP: string; LastHeartbeat: string; RegisteredAt: string;
  ActiveWorkers: number; LastLoad: number;
}

export interface DispatchRow {
  id: string; subtask_id: string; incubator_id: string; msg_id: string;
  ack_status: string; dispatched_at: string; acked_at: string;
}

export interface Subtask {
  id: string;
  task_id: string;
  seq: number;
  title: string;
  payload: string;
  depends_on: string;
  status: string;
  incubator_id: string;
  worker_id: string;
  result: string;
  error: string;
  kind: string;
  review_of: string;
  attempt_count: number;
  review_round: number;
  quality_status: string;
  fingerprint: string;
  created_at: string;
  started_at: string;
  finished_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  goal: string;
  title: string;
  status: string;
  progress: number;
  budget_cents: number;
  created_at: string;
  updated_at: string;
  subtasks?: Subtask[];
}

// 外部 Agent 接入步骤（对应后端 orchestrator.PlanStep）
export interface PlanStep {
  mode: 'cli_agent' | 'http_agent' | 'openai_compatible' | 'anthropic';
  title?: string;
  command?: string;
  args?: string[];
  cwd?: string;
  stdin?: string;
  timeout_ms?: number;
  url?: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  model?: string;
  prompt?: string;
  system?: string;
  review?: boolean;
  depends_on?: number[];
}

export interface Approval {
  id: string; task_id: string; subtask_id: string; approval_type: string;
  status: string; requester: string; title: string; description: string;
  payload: string; created_at: string; decided_at: string;
}

export interface SpotCheck {
  id: string; task_id: string; check_scope: string; status: string;
  pass_rate: number; overall_passed: number; attempt_count: number; note: string;
  created_at: string;
}

export interface ArchiveDoc {
  id: string; task_id: string; subtask_id: string; source: string; doc_type: string;
  title: string; content: string; word_count: number; created_at: string;
}

export interface CostSummary { scope: string; scope_id: string; total_cents: number; records: number; }

export interface AgentPlatform {
  id: string; platform_name: string; vendor: string; version: string;
  is_active: boolean; is_system: boolean; default_driver: string; capabilities: string;
}

export interface CommandTerminal {
  id: string; name: string; hostname: string; status: string;
  last_ip: string; listen_port: number; last_heartbeat: string; allow: string;
}

export interface CommandAuditRow {
  id: string; terminal_id: string; command: string; ok: boolean;
  exit_code: number; msg_id: string; duration_ms: number; reject: string; created_at: string;
}

// ── V1.5 位置无关 Agent 协作 ──────────────────────────

export interface AgentConnector {
  id: string; name: string; credential_id: string; protocol_version: string;
  online_status: string; health_status: string; last_seen: string; revoked_at: string;
}

export interface AgentInstance {
  id: string; connector_id: string; local_agent_key: string; display_name: string;
  platform_id: string; adapter_type: string;
  role_tags: string[]; capability_tags: string[]; environment_tags: string[];
  identity_status: string; verified_by: string; verified_at: string; revision: number;
  online_status: string; health_status: string;
}

export interface AgentAction {
  id: string; agent_id: string; action_key: string; action_kind: string; adapter_type: string;
  risk_level: string; retry_policy: string; max_attempts: number; validator_keys: string[];
  config_ref: string; enabled: boolean; revision: number;
}

export interface AgentCatalogAction {
  action_id: string; action_key: string; action_kind: string; adapter_type: string;
  retry_policy: string; max_attempts: number; validator_keys: string[];
}

export interface AgentCatalogAgent {
  agent_id: string; connector_id: string; local_agent_key: string; display_name: string;
  platform_id: string; adapter_type: string; online_status: string;
  role_tags: string[]; capability_tags: string[]; environment_tags: string[];
  actions: AgentCatalogAction[];
}

export interface PlanRevision {
  task_id: string; revision: number; source: string; status: string;
  draft_json: string; catalog_digest: string; approved_by: string; approved_at: string;
}

export interface AgentTask {
  task_id: string; title: string; goal: string; status: string; created_at: string; updated_at: string;
}

export interface ExecutionAttempt {
  attempt_id: string; task_id: string; step_id: string; plan_revision: number; attempt_no: number;
  agent_id: string; action_id: string; environment: string; input_digest: string;
  status: string; acceptance_status: string; result_json: string;
  lease_expires_at: string; created_at: string; finished_at: string;
}

export interface IdentityAudit {
  id: string; agent_id: string; task_id: string; actor: string; action: string; patch_json: string; created_at: string;
}

export const api = {
  incubators: () => getList<Incubator>('/api/v1/incubators'),
  dispatchLog: () => getList<DispatchRow>('/api/v1/dispatch-log'),
  tasks: (params?: { status?: string; q?: string }) => {
    const qs = new URLSearchParams();
    if (params?.status) qs.set('status', params.status);
    if (params?.q) qs.set('q', params.q);
    const suffix = qs.toString() ? `?${qs}` : '';
    return getList<Task>(`/api/v1/tasks${suffix}`);
  },
  createTask: (goal: string, plan?: PlanStep[], budgetCents?: number) => {
    const body: Record<string, unknown> = { goal };
    if (plan && plan.length > 0) body.plan = plan;
    if (budgetCents && budgetCents > 0) body.budget_cents = budgetCents;
    return post<Task>('/api/v1/tasks', body);
  },
  cancelTask: (id: string) => post(`/api/v1/tasks/${id}/cancel`),

  // M3 质量
  approvals: (status?: string) => getList<Approval>(`/api/v1/approvals${status ? `?status=${status}` : ''}`),
  decideApproval: (id: string, decision: 'approved' | 'rejected', note = '') =>
    post(`/api/v1/approvals/${id}/decision`, { decision, note }),
  spotChecks: () => getList<SpotCheck>('/api/v1/spot-checks'),

  // M3 归档
  documents: (taskId?: string) =>
    getList<ArchiveDoc>(`/api/v1/documents${taskId ? `?task_id=${taskId}` : ''}`),
  document: (id: string) => get<ArchiveDoc>(`/api/v1/documents/${id}`),

  // M5 成本
  costsSummary: (taskId?: string) =>
    get<{ data: CostSummary[]; global_cents: number }>(`/api/v1/costs/summary${taskId ? `?task_id=${taskId}` : ''}`),

  // M6 统一协议
  agentPlatforms: () => getList<AgentPlatform>('/api/v1/agent-platforms'),
  registerPlatform: (platform_name: string, vendor: string, default_driver: string) =>
    post('/api/v1/agent-platforms', { platform_name, vendor, default_driver, protocol_spec: 'openai-compat-protocol' }),

  // 任务详情四段式（UI §5.3）+ 配置开关（§7.3）+ 枚举字典（§8.2）
  taskDetail: (id: string) => get<{ data: { task: Task; cost_cents: number; traces: { id: string; subtask_id: string; to_id: string; fingerprint: string; depth: number }[] } }>(`/api/v1/tasks/${id}`),
  taskDag: (id: string) => get<{ data: { subtasks: { id: string; title: string; status: string; depends_on: string; kind: string; incubator_id: string; fingerprint: string; quality_status: string; review_round: number; attempt_count: number; started_at: string; finished_at: string }[]; dependencies: { from: string; to: string }[] } }>(`/api/v1/tasks/${id}/dag`),
  configSwitches: () => get<{ data: { key: string; current: unknown; default: unknown; on_effect: string; off_effect: string; boundary: string; source: string }[] }>('/api/v1/config'),
  patchConfig: (key: string, value: unknown) =>
    fetch('/api/v1/config', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}` },
      body: JSON.stringify({ key, value }),
    }).then(async (res) => {
      if (!res.ok) throw new Error(`PATCH config -> ${res.status}`);
      return res.json();
    }),
  enums: () => get<Record<string, unknown>>('/api/v1/enums'),

  // M8 命令终端
  commandTerminals: () => getList<CommandTerminal>('/api/v1/command-terminals'),
  registerCommandTerminal: (name: string) =>
    post<{ id: string; name: string; secret: string }>('/api/v1/command-terminals', { name }),
  rotateCommandSecret: (id: string) =>
    post<{ id: string; secret: string }>(`/api/v1/command-terminals/${id}/rotate-secret`),
  execCommand: (id: string, command: string) =>
    post<{ msg_id: string }>(`/api/v1/command-terminals/${id}/exec`, { command }),
  commandAudit: (id: string) => getList<CommandAuditRow>(`/api/v1/command-terminals/${id}/audit`),

  // V1.5 Agent 协作
  agentConnectors: () => getList<AgentConnector>('/api/v1/agent-connectors'),
  registerAgentConnector: (name: string) =>
    post<{ data: { connector_id: string; name: string; credential: string } }>('/api/v1/agent-connectors', { name }),
  revokeAgentConnector: (id: string) => post(`/api/v1/agent-connectors/${id}/revoke`),
  agentInstances: (status?: string) => getList<AgentInstance>(`/api/v1/agent-instances${status ? `?status=${status}` : ''}`),
  verifyAgentInstance: (id: string, patch: {
    display_name: string; platform_id: string; adapter_type: string;
    role_tags: string[]; capability_tags: string[]; environment_tags: string[];
    expected_revision: number; actor: string;
  }) => post(`/api/v1/agent-instances/${id}/verify`, patch),
  disableAgentInstance: (id: string) => post(`/api/v1/agent-instances/${id}/disable`, { actor: 'admin' }),
  agentActions: (agentId?: string) => getList<AgentAction>(`/api/v1/agent-actions${agentId ? `?agent_id=${agentId}` : ''}`),
  upsertAgentAction: (a: {
    agent_id: string; action_key: string; adapter_type: string; validator_keys: string[];
    retry_policy?: string; max_attempts?: number; config_ref?: string; enabled: boolean;
  }) => put('/api/v1/agent-actions', a),
  agentCatalog: () => get<{ data: { agents: AgentCatalogAgent[] } }>('/api/v1/agent-catalog'),
  submitAgentPlan: (draft: unknown, source = 'manual') =>
    post<{ data: PlanRevision }>('/api/v1/agent-plans', { draft, source }),
  agentPlans: (taskId: string) => getList<PlanRevision>(`/api/v1/agent-plans?task_id=${taskId}`),
  approveAgentPlan: (taskId: string, revision: number) =>
    post(`/api/v1/agent-plans/${taskId}/revisions/${revision}/approve`, { approved_by: 'admin' }),
  agentTasks: (status?: string) => getList<AgentTask>(`/api/v1/agent-tasks${status ? `?status=${status}` : ''}`),
  agentAttempts: (taskId: string) => getList<ExecutionAttempt>(`/api/v1/agent-tasks/${taskId}/attempts`),
  cancelAgentTask: (taskId: string) => post(`/api/v1/agent-tasks/${taskId}/cancel`),
  identityAudit: () => getList<IdentityAudit>('/api/v1/agent-identity-audit'),
};
