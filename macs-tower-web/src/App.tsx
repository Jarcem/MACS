import React, { useEffect, useState } from 'react';
import { api, type Incubator, type DispatchRow, type Task } from './api';

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

function usePoll<T>(fn: () => Promise<T>, ms = 5000) {
  const [data, setData] = useState<T | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    const tick = () => fn()
      .then((d) => alive && (setData(d), setErr(null)))
      .catch((e) => alive && setErr(String(e.message ?? e)));
    tick();
    const t = setInterval(tick, ms);
    return () => { alive = false; clearInterval(t); };
  }, [fn, ms]);
  return { data, err };
}

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

// 任务编排：提交目标 → 后端拆解为两个子任务并行执行 → 汇总进度
function TaskBoard({ tasks, err }: { tasks: Task[]; err?: string | null }) {
  const [goal, setGoal] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const g = goal.trim();
    if (!g || busy) return;
    setBusy(true);
    setMsg(null);
    try {
      await api.createTask(g);
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
        <input
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder="输入目标，例如：梳理 MACS 监控端的关键指标"
          aria-label="任务目标"
        />
        <button type="submit" disabled={busy}>{busy ? '提交中…' : '开始任务'}</button>
      </form>
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

// 单页布局：任务编排 + 集群状态 + 拓扑 + 调度记录，一次看全，不再分页签
export function App() {
  const inc = usePoll(api.incubators);
  const dsp = usePoll(api.dispatchLog);
  const tsk = usePoll(api.tasks, 2000);

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
          <p className="page-sub">运维 / 监控 / 交互 · 单机双 worker 垂直切片 · 2s 自动刷新</p>
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

      <Panel title="任务编排" note="目标 → 拆解 2 个子任务 → 并行执行 → 汇总">
        <TaskBoard tasks={tasks} err={tsk.err} />
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
