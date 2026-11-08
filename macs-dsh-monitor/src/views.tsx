// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

import React, { useEffect, useState } from 'react';
import { Box, Text } from 'ink';
import { api, get, subscribeTelemetryStream } from './api.js';
import { config } from './config.js';
import { bar } from './components.js';

// 通用轮询 Hook：按 refreshMs 周期拉取，错误降级为提示不中断界面
function usePolling<T>(fetcher: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const next = await fetcher();
        if (alive) {
          setData(next);
          setError(null);
        }
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      }
    };
    void tick();
    const timer = setInterval(tick, config.refreshMs);
    return () => {
      alive = false;
      clearInterval(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return { data, error };
}

// 通用 JSON 表格渲染：取首行键为列，单元格截断防换行错位
function JsonTable({ rows, max = 15 }: { rows: unknown[] | null; max?: number }) {
  if (!rows) return <Text dimColor>加载中…</Text>;
  if (!Array.isArray(rows) || rows.length === 0) return <Text dimColor>暂无数据</Text>;
  const sliced = rows.slice(0, max) as Record<string, unknown>[];
  const cols = Object.keys(sliced[0] ?? {});
  const cell = (v: unknown) => {
    const s = v === null || v === undefined ? '-' : String(v);
    return s.length > 18 ? s.slice(0, 17) + '…' : s;
  };
  return (
    <Box flexDirection="column">
      <Text bold color="cyan">{cols.map((c) => c.padEnd(20)).join('')}</Text>
      {sliced.map((r, i) => (
        <Text key={i}>{cols.map((c) => cell(r[c]).padEnd(20)).join('')}</Text>
      ))}
      {rows.length > max && <Text dimColor>… 其余 {rows.length - max} 行省略</Text>}
    </Box>
  );
}

function ViewShell({ title, error, children }: { title: string; error: string | null; children: React.ReactNode }) {
  return (
    <Box flexDirection="column">
      <Text bold color="cyan">{title}</Text>
      {error ? <Text color="red">连接失败：{error}</Text> : children}
    </Box>
  );
}

// 监控平面 · 集群拓扑（/monitor/topology 同内容）
export function ClusterView() {
  const { data, error } = usePolling(api.incubators);
  return <ViewShell title="集群拓扑（孵化器 / 资源画像）" error={error}><JsonTable rows={data} /></ViewShell>;
}

// 监控平面 · 调度监控（/monitor/dispatch 同内容）
export function DispatchView() {
  const { data, error } = usePolling(api.dispatchLog);
  return <ViewShell title="调度监控（下发 + ACK）" error={error}><JsonTable rows={data} /></ViewShell>;
}

// 交互平面 · 任务工作台（/tasks 同内容，终端侧只读）
export function TasksView() {
  const { data, error } = usePolling(api.tasks);
  return <ViewShell title="任务列表" error={error}><JsonTable rows={data} /></ViewShell>;
}

// 运维平面 · 统一 Agent 平台管理（/ops/agent-platforms 同内容，终端侧只读）
export function PlatformsView() {
  const { data, error } = usePolling(api.platforms);
  return <ViewShell title="统一 Agent 平台" error={error}><JsonTable rows={data} /></ViewShell>;
}

// 监控平面 · 适配器状态监控（/monitor/adapters 同内容）
export function AdaptersView() {
  const { data, error } = usePolling(api.adapters);
  return <ViewShell title="适配器状态" error={error}><JsonTable rows={data} /></ViewShell>;
}

// 监控平面 · 遥测数据可视化（/monitor/telemetry 同内容：SSE 实时流 + 历史查询）
export function TelemetryView() {
  const history = usePolling(api.telemetryEvents);
  const [live, setLive] = useState<unknown[]>([]);
  useEffect(() => {
    return subscribeTelemetryStream((data) => {
      setLive((prev) => [data, ...prev].slice(0, 10));
    });
  }, []);
  return (
    <Box flexDirection="column">
      <ViewShell title="遥测 · 实时流（SSE，最近 10 条）" error={null}>
        <JsonTable rows={live} max={10} />
      </ViewShell>
      <ViewShell title="遥测 · 历史（最近 50 条）" error={history.error}>
        <JsonTable rows={history.data} max={10} />
      </ViewShell>
    </Box>
  );
}

// 交互平面 · 历史任务记录（V1.4 §16.2，/tasks/history 同内容：近 50 条倒序 + 进度条列）
export function HistoryView() {
  const { data, error } = usePolling(() =>
    get<unknown[]>('/api/v1/tasks?pageSize=50&sort=updated_at%20DESC'),
  );
  const rows = (Array.isArray(data) ? data : []).map((t) => {
    const r = t as Record<string, unknown>;
    const pct = Number(r.progress_percent ?? 0);
    return {
      id: String(r.id ?? '').slice(0, 8),
      title: String(r.title ?? '').slice(0, 18),
      status: String(r.archived ? `${r.status}·归档` : r.status),
      进度: `${bar(pct, 10)} ${pct}%`,
      耗时: r.duration_sec != null ? `${Math.round(Number(r.duration_sec) / 60)}m` : '-',
      cost: r.cost_cents != null ? `¥${(Number(r.cost_cents) / 100).toFixed(2)}` : '-',
      完成: r.finished_at ? String(r.finished_at).slice(0, 16).replace('T', ' ') : '-',
    };
  });
  return <ViewShell title="历史任务记录（近 50 条，updated_at DESC）" error={error}><JsonTable rows={rows} max={15} /></ViewShell>;
}

// 监控平面 · 运行总览（/overview 同内容：孵化器在线 + 任务统计聚合）
export function OverviewView() {
  const incubators = usePolling(api.incubators);
  const tasks = usePolling(api.tasks);
  const online = Array.isArray(incubators.data)
    ? incubators.data.filter((i) => (i as Record<string, unknown>).status === 'online').length
    : 0;
  return (
    <Box flexDirection="column">
      <ViewShell title={`运行总览 · 孵化器在线 ${online}/${incubators.data?.length ?? '-'} · 任务 ${tasks.data?.length ?? '-'} 个`} error={incubators.error ?? tasks.error}>
        <JsonTable rows={tasks.data} max={8} />
      </ViewShell>
    </Box>
  );
}
