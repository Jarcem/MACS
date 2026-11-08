// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

import EventSource from 'eventsource';
import { config } from './config.js';

// 管理后台 REST 客户端（主设计附录 B.1，Admin Token 鉴权，只读）
export async function get<T>(path: string): Promise<T> {
  const res = await fetch(`${config.apiBase}${path}`, {
    headers: { Authorization: `Bearer ${config.adminToken}` },
  });
  if (!res.ok) throw new Error(`GET ${path} -> ${res.status}`);
  return (await res.json()) as T;
}

// 列表端点统一解包：契约响应为 { data: [...], page, pageSize }
async function getList<T>(path: string): Promise<T[]> {
  const r = await get<{ data?: T[] } | T[]>(path);
  return Array.isArray(r) ? r : (r?.data ?? []);
}

// 端点与 macs-tower-web 三平面一致（附录 B.1 / UI 设计 V1.1 附录 A）
export const api = {
  incubators: () => getList<Record<string, unknown>>('/api/v1/incubators'),
  tasks: () => getList<Record<string, unknown>>('/api/v1/tasks'),
  dispatchLog: () => getList<Record<string, unknown>>('/api/v1/dispatch-log'),
  platforms: () => getList<Record<string, unknown>>('/api/v1/agent-platforms'),
  adapters: () => getList<Record<string, unknown>>('/api/v1/platform-adapters'),
  telemetryEvents: (query = '?limit=50') =>
    getList<Record<string, unknown>>(`/api/v1/telemetry/events${query}`),
  // 任务执行透视四端点（V3.2，UI 设计 V1.2 §14.8）
  taskDetail: (id: string) => get<Record<string, unknown>>(`/api/v1/tasks/${id}`),
  taskDag: (id: string) =>
    get<{ nodes: DagNode[]; edges: { from: string; to: string }[] }>(`/api/v1/tasks/${id}/dag`),
  taskWorkers: (id: string) => getList<Record<string, unknown>>(`/api/v1/tasks/${id}/workers`),
  taskEvents: (id: string, limit = 20) =>
    getList<Record<string, unknown>>(`/api/v1/tasks/${id}/events?limit=${limit}`),
};

export interface DagNode {
  id: string;
  name: string;
  status: string;
  worker_id?: string | null;
  current_step?: string | null;
  progress_percent?: number | null;
}

// SSE 实时遥测（V3.0，GET /api/v1/telemetry/stream），返回取消订阅函数
export function subscribeTelemetryStream(onEvent: (data: unknown) => void): () => void {
  const es = new EventSource(`${config.apiBase}/api/v1/telemetry/stream`, {
    headers: { Authorization: `Bearer ${config.adminToken}` },
  });
  es.addEventListener('telemetry.update', (ev) => {
    try {
      onEvent(JSON.parse((ev as MessageEvent).data as string));
    } catch {
      /* 忽略非 JSON 心跳帧 */
    }
  });
  return () => es.close();
}
