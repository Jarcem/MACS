// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { api, type Incubator, type DispatchRow, type PlanStep, type Task } from './api';

const statusClass = (s: string) => {
  if (s === 'online' || s === 'acked' || s === 'running') return 'hud-running';
  if (s === 'completed') return 'hud-completed';
  if (s === 'dispatched') return 'hud-checking';
  if (s === 'offline' || s === 'timeout' || s === 'failed') return 'hud-failed';
  return 'hud-pending';
};

// 后端时间为 ISO8601（UTC），展示为本地时间 MM-DD HH:mm:ss
const fmtTime = (s?: string) => {
  if (!s) return '-';
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
};

// 子任务耗时（started_at → finished_at）
const fmtSpan = (s?: string, e?: string) => {
  if (!s || !e) return '-';
  const ms = new Date(e).getTime() - new Date(s).getTime();
  return Number.isNaN(ms) || ms < 0 ? '-' : `${ms} ms`;
};

// 实时事件帧（塔 events.Hub 广播：{event, ts, data}）
export interface StreamEvent {
  event: string;
  ts: string;
  data: Record<string, any>;
}

const TASK_EVENTS = ['task_created', 'subtask_dispatched', 'subtask_finished', 'task_finished'];

function usePoll<T>(fn: () => Promise<T>, ms = 5000) {
  const [data, setData] = useState<T | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const refresh = useCallback(() => {
    fn()
      .then((d) => { setData(d); setErr(null); })
      .catch((e) => setErr(String((e as Error).message ?? e)));
  }, [fn]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, ms);
    return () => clearInterval(t);
  }, [refresh, ms]);

  return { data, err, refresh };
}

// SSE 订阅 /api/v1/telemetry/stream：断线 3s 自动重连，作为轮询之外的实时通道
function useEventStream(onEvent: (e: StreamEvent) => void) {
  const [live, setLive] = useState(false);
  const cb = useRef(onEvent);
  cb.current = onEvent;

  useEffect(() => {
    let es: EventSource | null = null;
    let retry: number | undefined;
    let closed = false;

    const connect = () => {
      es = new EventSource('/api/v1/telemetry/stream');
      es.onopen = () => setLive(true);
      es.onmessage = (m) => {
        try { cb.current(JSON.parse(m.data) as StreamEvent); } catch { /* 忽略非 JSON 帧 */ }
      };
      es.onerror = () => {
        setLive(false);
        es?.close();
        if (!closed) retry = window.setTimeout(connect, 3000);
      };
    };

    connect();
    return () => {
      closed = true;
      es?.close();
      if (retry) window.clearTimeout(retry);
    };
  }, []);

  return live;
}

// SSE 事件摘要（截断 id，展示进度与状态）
const fmtEvent = (e: StreamEvent) => {
  const d = e.data ?? {};
  const status = d.status ?? d.task_status ?? d.subtask?.status;
  return [
    d.task_id && `task ${String(d.task_id).slice(0, 8)}`,
    d.subtask_id && `subtask ${String(d.subtask_id).slice(0, 8)}`,
    d.incubator_id && `node ${String(d.incubator_id).slice(0, 8)}`,
    typeof d.progress === 'number' && `${d.progress}%`,
    status && `status ${status}`,
  ].filter(Boolean).join(' · ') || '-';
};

function Panel({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <section className="hud-panel">
      <h2 className="panel-title">
        {title}
        {note && <span className="panel-note">{note}</span>}
      </h2>
      {children}
    </section>
  );
}

function Empty({ err }: { err?: string | null }) {
  if (err) return <p className="hud-failed" style={{ fontSize: 12 }}>连接失败：{err}</p>;
  return <p className="hint">暂无数据</p>;
}

function ProgressBar({ value, status }: { value: number; status: string }) {
  return (
    <span className={`bar${status === 'failed' ? ' bar-failed' : ''}`}>
      <span className="bar-fill" style={{ width: `${Math.max(0, Math.min(100, value))}%` }} />
    </span>
  );
}

// 子任务 payload 里的执行方式（内置解析 / 汇总 / CLI Agent / HTTP Agent）
const modeOf = (payload?: string) => {
  try {
    const m = JSON.parse(payload ?? '{}')?.mode as string | undefined;
    if (m === 'text_analysis') return '内置';
    if (m === 'merge') return '汇总';
    if (m === 'cli_agent') return 'CLI';
    if (m === 'http_agent') return 'HTTP';
    return m ?? '';
  } catch {
    return '';
  }
};

// 任务编排：提交目标 → 内置拆解，或交给本机 CLI / HTTP Agent 执行 → 汇总进度
function TaskBoard({ tasks, err }: { tasks: Task[]; err?: string | null }) {
  const [goal, setGoal] = useState('');
  const [mode, setMode] = useState<'builtin' | 'cli' | 'http'>('builtin');
  const [command, setCommand] = useState('');
  const [args, setArgs] = useState('');
  const [cwd, setCwd] = useState('');
  const [url, setUrl] = useState('');
  const [method, setMethod] = useState('POST');
  const [body, setBody] = useState('{"prompt":"{{text}}"}');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const ready = mode === 'builtin' || (mode === 'cli' ? command.trim() !== '' : url.trim() !== '');

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const g = goal.trim();
    if (!g || busy || !ready) return;
    setBusy(true);
    setMsg(null);
    try {
      let plan: PlanStep[] | undefined;
      if (mode === 'cli') {
        plan = [{
          mode: 'cli_agent',
          command: command.trim(),
          args: args.trim() ? args.trim().split(/\s+/) : [],
          cwd: cwd.trim() || undefined,
        }];
      } else if (mode === 'http') {
        plan = [{
          mode: 'http_agent',
          url: url.trim(),
          method: method.trim() || 'POST',
          body: body.trim() || undefined,
        }];
      }
      await api.createTask(g, plan);
      setGoal('');
    } catch (x) {
      setMsg(String((x as Error).message ?? x));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <form className="task-form" onSubmit={submit}>
        <select value={mode} onChange={(e) => setMode(e.target.value as typeof mode)} aria-label="执行方式">
          <option value="builtin">内置拆解</option>
          <option value="cli">终端 CLI Agent</option>
          <option value="http">HTTP Agent</option>
        </select>
        <input
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder="输入目标，例如：审查 src 目录的并发安全"
          aria-label="任务目标"
        />
        <button type="submit" disabled={busy || !ready}>{busy ? '提交中…' : '开始任务'}</button>
      </form>

      {mode === 'cli' && (
        <div className="task-form task-sub">
          <input value={command} onChange={(e) => setCommand(e.target.value)}
            placeholder="命令（如 zcode / dsh），须在孵化器 MACS_AGENT_ALLOW 白名单" aria-label="命令" />
          <input value={args} onChange={(e) => setArgs(e.target.value)}
            placeholder="参数，空格分隔；可用 {{text}}" aria-label="参数" />
          <input value={cwd} onChange={(e) => setCwd(e.target.value)}
            placeholder="工作目录（可选）" aria-label="工作目录" />
        </div>
      )}
      {mode === 'http' && (
        <div className="task-form task-sub">
          <input value={url} onChange={(e) => setUrl(e.target.value)}
            placeholder="http://127.0.0.1:9000/run" aria-label="Agent URL" />
          <input value={method} onChange={(e) => setMethod(e.target.value)}
            placeholder="POST" aria-label="请求方法" style={{ maxWidth: 96 }} />
          <input value={body} onChange={(e) => setBody(e.target.value)}
            placeholder='请求体，可用 {{text}} / {{deps}}' aria-label="请求体" />
        </div>
      )}
      {mode === 'cli' && (
        <p className="hint">CLI 模式需孵化器设置 MACS_ALLOW_CLI_AGENT=1；命令以 argv 方式直接启动，不经 shell。</p>
      )}
      {msg && <p className="hud-failed" style={{ fontSize: 12 }}>{msg}</p>}
      {tasks.length === 0 ? (
        <p className="hint">暂无任务。提交后这里显示拆解出的子任务、执行节点与实时进度。</p>
      ) : tasks.map((t) => (
        <div className="task" key={t.id}>
          <div className="task-head">
            <span className="task-goal">{t.goal}</span>
            <span className={statusClass(t.status)}>{t.status}</span>
            <ProgressBar value={t.progress} status={t.status} />
            <span className="mono-dim">{t.progress}%</span>
            <span className="mono-dim">提交 {fmtTime(t.created_at)}</span>
          </div>
          <table className="hud-table">
            <thead>
              <tr>
                <th>子任务</th>
                <th>方式</th>
                <th>状态</th>
                <th>执行节点</th>
                <th>worker</th>
                <th>耗时</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {(t.subtasks ?? []).map((s) => (
                <tr key={s.id}>
                  <td>{s.title}</td>
                  <td className="mono-dim">{modeOf(s.payload)}</td>
                  <td className={statusClass(s.status)}>{s.status}</td>
                  <td className="mono-dim">{s.incubator_id ? s.incubator_id.slice(0, 8) : '-'}</td>
                  <td className="mono-dim">{s.worker_id || '-'}</td>
                  <td className="mono-dim">{fmtSpan(s.started_at, s.finished_at)}</td>
                  <td className="mono-dim">
                    {s.error ? <span className="hud-failed">{s.error}</span>
                      : s.result ? `${s.result.slice(0, 72)}${s.result.length > 72 ? '…' : ''}` : '-'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
      {err && <p className="hud-failed" style={{ fontSize: 12 }}>任务接口异常：{err}</p>}
    </>
  );
}

// 事件去重键：重连时塔会回放历史帧，避免重复行
const keyOf = (e: StreamEvent) => `${e.ts}|${e.event}|${e.data?.subtask_id ?? e.data?.id ?? ''}`;

// 单页布局：任务编排 + 集群状态 + 拓扑 + 调度记录，一次看全，不再分页签
export function App() {
  const inc = usePoll(api.incubators, 10000);
  const dsp = usePoll(api.dispatchLog, 10000);
  const tsk = usePoll(api.tasks, 5000);
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [lastAt, setLastAt] = useState('-');

  const live = useEventStream((ev) => {
    setLastAt(fmtTime(ev.ts));
    setEvents((prev) => [ev, ...prev.filter((x) => keyOf(x) !== keyOf(ev))].slice(0, 8));
    if (TASK_EVENTS.includes(ev.event)) {
      tsk.refresh();
      dsp.refresh();
    }
  });

  const incubators: Incubator[] = inc.data ?? [];
  const dispatch: DispatchRow[] = dsp.data ?? [];
  const tasks: Task[] = tsk.data ?? [];
  const online = incubators.filter((i) => i.Status === 'online').length;
  const running = tasks.filter((t) => t.status === 'running' || t.status === 'pending').length;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">MACS 控制塔</h1>
          <p className="page-sub">
            运维 / 监控 / 交互 · 单机双 worker 垂直切片 ·
            <span className={live ? 'hud-running' : 'hud-checking'}>
              {live ? ' SSE 实时' : ' SSE 重连中'}
            </span>
            <span className="mono-dim"> · 最近事件 {lastAt}</span>
          </p>
        </div>
        <div className="kpis">
          <div className="kpi">
            <div className="kpi-label">孵化器在线</div>
            <div className={`kpi-value ${online === incubators.length && online > 0 ? 'hud-running' : 'hud-checking'}`}>
              {online} / {incubators.length}
            </div>
          </div>
          <div className="kpi">
            <div className="kpi-label">进行中任务</div>
            <div className="kpi-value hud-checking">{running}</div>
          </div>
          <div className="kpi">
            <div className="kpi-label">任务总数</div>
            <div className="kpi-value hud-completed">{tasks.length}</div>
          </div>
          <div className="kpi">
            <div className="kpi-label">调度记录</div>
            <div className="kpi-value hud-completed">{dispatch.length}</div>
          </div>
        </div>
      </header>

      {(inc.err || dsp.err || tsk.err) && (
        <p className="hud-failed" style={{ fontSize: 12 }}>
          连接异常：{inc.err ?? dsp.err ?? tsk.err}
        </p>
      )}

      <Panel title="任务编排" note="内置拆解 / 终端 CLI Agent / HTTP Agent → 依赖门控执行">
        <TaskBoard tasks={tasks} err={tsk.err} />
      </Panel>

      <Panel title="实时事件" note={live ? 'SSE 已连接 · 事件驱动刷新' : 'SSE 重连中 · 轮询兜底'}>
        {events.length === 0 ? (
          <p className="hint">等待事件。提交任务后会实时出现「任务创建 / 子任务下发 / 子任务完成 / 任务完成」。</p>
        ) : (
          <table className="hud-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>事件</th>
                <th>详情</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e, i) => (
                <tr key={`${e.ts}-${i}`}>
                  <td className="mono-dim">{fmtTime(e.ts)}</td>
                  <td className="hud-running">{e.event}</td>
                  <td className="mono-dim">{fmtEvent(e)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>

      <Panel title="集群状态" note={`${incubators.length} 台`}>
        {incubators.length === 0 ? <Empty err={inc.err} /> : (
          <table className="hud-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>并发上限</th>
                <th>地址</th>
                <th>最后心跳</th>
              </tr>
            </thead>
            <tbody>
              {incubators.map((i) => (
                <tr key={i.ID}>
                  <td>{i.Name}</td>
                  <td className={statusClass(i.Status)}>{i.Status}</td>
                  <td>{i.Capacity}</td>
                  <td>{i.LastIP}:{i.ListenPort}</td>
                  <td className="mono-dim">{fmtTime(i.LastHeartbeat)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>

      <Panel title="集群拓扑" note="tower :8470 (UDP) / :8471 (HTTP)">
        {incubators.length === 0 ? <Empty err={inc.err} /> : (
          <div className="topology">
            <div className="tower-node">◈ 调度塔 tower</div>
            {incubators.map((i) => (
              <div key={i.ID} className="incubator-row">
                <span className="incubator-edge">└─▶</span>
                <div className="incubator-node">
                  <span className={statusClass(i.Status)}>● {i.Status}</span>
                  {' '}<strong>{i.Name}</strong>
                  <span className="mono-dim"> · {i.LastIP}:{i.ListenPort} · 槽位 {i.Capacity} · 心跳 {fmtTime(i.LastHeartbeat)}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </Panel>

      <Panel title="调度记录" note={`近 ${dispatch.length} 条`}>
        {dispatch.length === 0 ? <Empty err={dsp.err} /> : (
          <table className="hud-table">
            <thead>
              <tr>
                <th>子任务</th>
                <th>ACK 状态</th>
                <th>下发时间</th>
                <th>应答时间</th>
                <th>msg_id</th>
              </tr>
            </thead>
            <tbody>
              {dispatch.map((r) => (
                <tr key={r.id}>
                  <td>{r.subtask_id}</td>
                  <td className={statusClass(r.ack_status)}>{r.ack_status}</td>
                  <td className="mono-dim">{fmtTime(r.dispatched_at)}</td>
                  <td className="mono-dim">{fmtTime(r.acked_at)}</td>
                  <td className="mono-dim">{r.msg_id.slice(0, 8)}…</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}
