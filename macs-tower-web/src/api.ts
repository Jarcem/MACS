// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 管理后台 API 客户端（主设计附录 B.1；dev 期由 vite proxy 转发到 8471）
async function get<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}` } });
  if (!res.ok) throw new Error(`GET ${path} -> ${res.status}`);
  return (await res.json()) as T;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${localStorage.getItem('macs_token') ?? ''}`,
    },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`POST ${path} -> ${res.status} ${text}`);
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
  created_at: string;
  started_at: string;
  finished_at: string;
  updated_at: string;
}

export interface Task {
  id: string;
  goal: string;
  status: string;
  progress: number;
  created_at: string;
  updated_at: string;
  subtasks?: Subtask[];
}

// 外部 Agent 接入步骤（对应后端 orchestrator.PlanStep）
export interface PlanStep {
  mode: 'cli_agent' | 'http_agent';
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
  depends_on?: number[];
}

export const api = {
  incubators: () => getList<Incubator>('/api/v1/incubators'),
  dispatchLog: () => getList<DispatchRow>('/api/v1/dispatch-log'),
  tasks: () => getList<Task>('/api/v1/tasks'),
  createTask: (goal: string, plan?: PlanStep[]) =>
    post<Task>('/api/v1/tasks', plan && plan.length > 0 ? { goal, plan } : { goal }),
};
