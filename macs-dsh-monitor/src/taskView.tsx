import React, { useEffect, useState } from 'react';
import { Box, Text } from 'ink';
import { api, DagNode, get } from './api.js';
import { config } from './config.js';
import { bar, taskProgress } from './components.js';

// 任务执行透视（UI 设计 V1.2 第十四章六块内容的终端实现）
// 数据口径与 web 端 /tasks/:id/blueprint 完全一致，只读

const GLYPH: Record<string, string> = {
  running: '●', completed: '✓', failed: '✗', frozen: '❄',
  pending: '○', self_checking: '◐', reviewing: '◐', dispatched: '◌',
  skipped: '⊘', merging: '◑', accepted: '✓',
};
const glyph = (s: string) => GLYPH[s] ?? '○';
const short = (id?: string | null) => (id ? id.slice(0, 8) : '-');

function usePolling<T>(fetcher: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const next = await fetcher();
        if (alive) { setData(next); setError(null); }
      } catch (err) {
        if (alive) setError(err instanceof Error ? err.message : String(err));
      }
    };
    void tick();
    const t = setInterval(tick, config.refreshMs);
    return () => { alive = false; clearInterval(t); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return { data, error };
}

// 拓扑分层（Kahn）：环或缺失依赖降级为末层，保证节点不丢（§14.3 硬约束）
function topoLevels(nodes: DagNode[], edges: { from: string; to: string }[]): DagNode[][] {
  const indeg = new Map<string, number>(nodes.map((n) => [n.id, 0]));
  edges.forEach((e) => indeg.has(e.to) && indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const levels: DagNode[][] = [];
  let frontier = nodes.filter((n) => (indeg.get(n.id) ?? 0) === 0);
  const placed = new Set<string>();
  while (frontier.length > 0) {
    levels.push(frontier);
    frontier.forEach((n) => placed.add(n.id));
    const next: DagNode[] = [];
    edges.forEach((e) => {
      if (!placed.has(e.from)) return;
      const d = (indeg.get(e.to) ?? 0) - 1;
      indeg.set(e.to, d);
      if (d === 0 && !placed.has(e.to)) { const n = byId.get(e.to); if (n) next.push(n); }
    });
    frontier = next;
  }
  const rest = nodes.filter((n) => !placed.has(n.id));
  if (rest.length > 0) levels.push(rest);
  return levels;
}

function Blueprint({ nodes, edges }: { nodes: DagNode[]; edges: { from: string; to: string }[] }) {
  const levels = topoLevels(nodes, edges);
  return (
    <Box flexDirection="column">
      {levels.map((lv, i) => (
        <Box key={i} flexDirection="column">
          {i > 0 && <Text dimColor>{'  │'}</Text>}
          <Text>
            {lv.map((n) => `[${glyph(n.status)} ${n.name}]`).join(' ─▶ ')}
          </Text>
        </Box>
      ))}
    </Box>
  );
}

export function TaskBlueprintView() {
  const [taskId, setTaskId] = useState<string | null>(process.env.MACS_TASK_ID ?? null);
  useEffect(() => {
    if (taskId) return;
    api.tasks().then((ts) => {
      const first = Array.isArray(ts) ? (ts[0] as Record<string, unknown> | undefined) : undefined;
      if (first?.id) setTaskId(String(first.id));
    }).catch(() => undefined);
  }, [taskId]);

  const detail = usePolling(() => get<Record<string, unknown>>(`/api/v1/tasks/${taskId}`), [taskId]);
  const dag = usePolling(() => api.taskDag(taskId!), [taskId]);
  const workers = usePolling(() => api.taskWorkers(taskId!), [taskId]);
  const events = usePolling(() => api.taskEvents(taskId!), [taskId]);

  if (!taskId) return <Text dimColor>未发现任务（可设 MACS_TASK_ID 指定）</Text>;
  const d = detail.data;
  const items = Array.isArray(d?.acceptance_items) ? (d!.acceptance_items as { label: string; passed: boolean | null }[]) : [];
  const passed = items.filter((i) => i.passed === true).length;
  const nodes = dag.data?.nodes ?? [];
  const active = nodes.filter((n) => ['running', 'self_checking', 'reviewing'].includes(n.status));
  const pct = taskProgress(nodes); // 加权合成（V1.4 §16.1）
  const hasFine = nodes.some((n) => n.progress_percent != null);

  return (
    <Box flexDirection="column">
      {/* ① 目标面板（含实时进度条 §16.1） */}
      <Text bold color="cyan">目标 {glyph(String(d?.status ?? 'pending'))} {String(d?.title ?? taskId)} · 验收 {passed}/{items.length}</Text>
      <Text bold color={pct >= 100 ? 'green' : 'cyan'}>[{bar(pct)}] {pct}%{hasFine ? '' : '（按完成数）'}</Text>
      {items.map((it, i) => (
        <Text key={i} dimColor={it.passed === null}>  {it.passed === true ? '✓' : it.passed === false ? '✗' : '○'} {it.label}</Text>
      ))}
      {/* ②⑥ 过程拆解 + 实时蓝图（同一份 DAG + 状态合成） */}
      <Text bold color="cyan" >实时蓝图</Text>
      {dag.data ? <Blueprint nodes={nodes} edges={dag.data.edges} /> : <Text dimColor>加载中…</Text>}
      {/* ③ 当前执行看板 */}
      <Text bold color="cyan">执行中（{active.length}）</Text>
      {active.length === 0 && <Text dimColor>  无进行中子任务</Text>}
      {active.map((n) => (
        <Text key={n.id}>  {glyph(n.status)} {n.name} · worker {short(n.worker_id)} · {n.current_step ?? '-'} · {n.progress_percent ?? 0}%</Text>
      ))}
      {/* ④ 子 Worker 状况 */}
      <Text bold color="cyan">子 Worker（{workers.data?.length ?? '-'}）</Text>
      {(workers.data ?? []).slice(0, 8).map((w) => {
        const r = w as Record<string, unknown>;
        return <Text key={String(r.worker_id)}>  {glyph(String(r.status))} {short(String(r.worker_id))} · {String(r.agent_platform ?? r.platform_adapter ?? '-')} · ttl {String(r.ttl_remaining_sec ?? '-')}s · tokens {String(r.token_total ?? '-')}</Text>;
      })}
      {/* ⑤ Agent 间汇报记录 */}
      <Text bold color="cyan">汇报记录</Text>
      {(events.data ?? []).slice(0, 8).map((e, i) => {
        const r = e as Record<string, unknown>;
        const highlight = ['review_rejected', 'guardrail', 'escalation'].some((k) => String(r.event_type).includes(k));
        return (
          <Text key={i} color={highlight ? 'yellow' : undefined}>
            {'  '}{String(r.event_type)} {short(String(r.from_worker ?? r.worker_id ?? '-'))} → {String(r.to_target ?? 'tower')} · {String(r.summary ?? '').slice(0, 60)}
          </Text>
        );
      })}
      {(detail.error ?? dag.error) && <Text color="red">{detail.error ?? dag.error}</Text>}
    </Box>
  );
}
