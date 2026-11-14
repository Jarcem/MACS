// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { api, type Incubator, type DispatchRow, type PlanStep, type Task, type Approval, type SpotCheck, type ArchiveDoc, type CostSummary, type AgentPlatform, type CommandTerminal } from './api';

const statusClass = (s: string) => {
  if (['online', 'acked', 'running', 'reviewing', 'in_spot_check', 'overloaded'].includes(s)) return 'hud-running';
  if (['completed', 'passed', 'approved', 'ready'].includes(s)) return 'hud-completed';
  if (['dispatched', 'review_pending', 'self_checking', 'spot_check_passed', 'registered'].includes(s)) return 'hud-checking';
  if (['offline', 'timeout', 'failed', 'rejected', 'blocked', 'frozen', 'revoked'].includes(s)) return 'hud-failed';
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

const TASK_EVENTS = ['task_created', 'task_finished', 'task_cancelled', 'subtask_dispatched', 'subtask_finished', 'quality_event', 'approval_created', 'approval_decided', 'spot_check_finished', 'budget_exceeded', 'cost_recorded', 'guardrail_event', 'cmd_result'];

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
  const status = d.status ?? d.task_status ?? d.subtask?.status ?? d.layer;
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

// 子任务 payload 里的执行方式（内置解析 / 汇总 / CLI / HTTP / 协议驱动 / 复核）
const modeOf = (payload?: string, kind?: string) => {
  try {
    const m = JSON.parse(payload ?? '{}')?.mode as string | undefined;
    if (kind === 'review') return '复核';
    const map: Record<string, string> = {
      text_analysis: '内置', merge: '汇总', cli_agent: 'CLI', http_agent: 'HTTP',
      openai_compatible: 'OpenAI', anthropic: 'Anthropic',
    };
    return map[m ?? ''] ?? m ?? '';
  } catch {
    return '';
  }
};

// 任务编排：提交目标 → 内置拆解 / Agent 执行 → 质量管线 → 归档
function TaskBoard({ tasks, err, onChanged }: { tasks: Task[]; err?: string | null; onChanged: () => void }) {
  const [goal, setGoal] = useState('');
  const [mode, setMode] = useState<'builtin' | 'cli' | 'http' | 'llm'>('builtin');
  const [command, setCommand] = useState('');
  const [args, setArgs] = useState('');
  const [cwd, setCwd] = useState('');
  const [url, setUrl] = useState('');
  const [method, setMethod] = useState('POST');
  const [body, setBody] = useState('{"prompt":"{{text}}"}');
  const [model, setModel] = useState('');
  const [prompt, setPrompt] = useState('{{text}}');
  const [budget, setBudget] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const ready = mode === 'builtin' || (mode === 'cli' ? command.trim() !== '' : mode === 'http' ? url.trim() !== '' : true);

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
      } else if (mode === 'llm') {
        // 协议驱动（6.4.9）：凭据由孵化器 env 注入，payload 只带模型与提示词
        plan = [{
          mode: 'openai_compatible',
          model: model.trim() || undefined,
          prompt: prompt.trim() || undefined,
          review: false,
        }];
      }
      await api.createTask(g, plan, budget.trim() ? Number(budget.trim()) : undefined);
      setGoal('');
      onChanged();
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
          <option value="llm">OpenAI 协议驱动</option>
        </select>
        <input
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder="输入目标，例如：审查 src 目录的并发安全"
          aria-label="任务目标"
        />
        <input value={budget} onChange={(e) => setBudget(e.target.value)}
          placeholder="预算(分,可选)" aria-label="任务预算" style={{ maxWidth: 110 }} />
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
      {mode === 'llm' && (
        <div className="task-form task-sub">
          <input value={model} onChange={(e) => setModel(e.target.value)}
            placeholder="模型（留空用孵化器 MACS_OPENAI_MODEL）" aria-label="模型" />
          <input value={prompt} onChange={(e) => setPrompt(e.target.value)}
            placeholder="提示词，可用 {{text}}" aria-label="提示词" />
        </div>
      )}
      {mode === 'cli' && (
        <p className="hint">CLI 模式需孵化器设置 MACS_ALLOW_CLI_AGENT=1；命令以 argv 方式直接启动，不经 shell。</p>
      )}
      {mode === 'llm' && (
        <p className="hint">协议驱动模式（6.4.9）：孵化器需配置 MACS_OPENAI_API_KEY；凭据不上塔、不进 payload。</p>
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
            {t.budget_cents > 0 && <span className="mono-dim">预算 {t.budget_cents} 分</span>}
            <span className="mono-dim">提交 {fmtTime(t.created_at)}</span>
            {!['completed', 'failed', 'cancelled'].includes(t.status) && (
              <button className="hud-btn" onClick={() => api.cancelTask(t.id).then(onChanged).catch((x) => setMsg(String(x)))}>
                取消
              </button>
            )}
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
                  <td>
                    {s.title}
                    {s.review_round > 0 && <span className="mono-dim"> · 复核第{s.review_round}轮</span>}
                    {s.attempt_count > 1 && <span className="mono-dim"> · 重试{s.attempt_count - 1}次</span>}
                  </td>
                  <td className="mono-dim">{modeOf(s.payload, s.kind)}</td>
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

// M3 审批中心（/approvals 交互平面）：pending 审批一键决策
function ApprovalsPanel({ refreshKey, onChanged }: { refreshKey: number; onChanged: () => void }) {
  const { data, err, refresh } = usePoll(() => api.approvals(), 8000);
  useEffect(() => { if (refreshKey > 0) refresh(); }, [refreshKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const approvals: Approval[] = data ?? [];
  const [busy, setBusy] = useState('');
  const decide = async (id: string, decision: 'approved' | 'rejected') => {
    setBusy(id);
    try {
      await api.decideApproval(id, decision);
      refresh();
      onChanged();
    } catch (x) {
      alert(String((x as Error).message ?? x));
    } finally {
      setBusy('');
    }
  };
  const pending = approvals.filter((a) => a.status === 'pending');
  return (
    <Panel title="审批中心" note={`待审 ${pending.length} · 共 ${approvals.length}`}>
      {approvals.length === 0 ? <Empty err={err} /> : (
        <table className="hud-table">
          <thead>
            <tr><th>场景</th><th>标题</th><th>状态</th><th>关联</th><th>创建</th><th>操作</th></tr>
          </thead>
          <tbody>
            {approvals.map((a) => (
              <tr key={a.id}>
                <td className="hud-running">{a.approval_type}</td>
                <td>{a.title}{a.description && <span className="mono-dim"> · {a.description}</span>}</td>
                <td className={statusClass(a.status)}>{a.status}</td>
                <td className="mono-dim">{a.task_id ? `task ${a.task_id.slice(0, 8)}` : '-'}</td>
                <td className="mono-dim">{fmtTime(a.created_at)}</td>
                <td>
                  {a.status === 'pending' ? (
                    <>
                      <button className="hud-btn" disabled={busy === a.id}
                        onClick={() => decide(a.id, 'approved')}>通过</button>
                      <button className="hud-btn hud-btn-danger" disabled={busy === a.id}
                        onClick={() => decide(a.id, 'rejected')}>驳回</button>
                    </>
                  ) : '-'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// M3 质量看板（/monitor/quality）：抽查记录 + 追溯核验状态
function QualityPanel({ refreshKey }: { refreshKey: number }) {
  const { data, err, refresh } = usePoll(() => api.spotChecks(), 10000);
  useEffect(() => { if (refreshKey > 0) refresh(); }, [refreshKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const checks: SpotCheck[] = data ?? [];
  return (
    <Panel title="抽查核验" note={`E06 追溯链对账 · 共 ${checks.length} 次`}>
      {checks.length === 0 ? (
        <p className="hint">未启用或未抽样。设 MACS_FEATURE_SPOT_CHECK=1 开启云端抽查（Q7 默认 20%）。</p>
      ) : (
        <table className="hud-table">
          <thead>
            <tr><th>任务</th><th>范围</th><th>结论</th><th>通过率</th><th>尝试</th><th>说明</th><th>时间</th></tr>
          </thead>
          <tbody>
            {checks.map((c) => (
              <tr key={c.id}>
                <td className="mono-dim">{c.task_id.slice(0, 8)}</td>
                <td>{c.check_scope}</td>
                <td className={statusClass(c.status)}>{c.status}</td>
                <td className="mono-dim">{Math.round(c.pass_rate * 100)}%</td>
                <td className="mono-dim">{c.attempt_count}</td>
                <td className="mono-dim">{c.note}</td>
                <td className="mono-dim">{fmtTime(c.created_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// M3 归档文档库（/archive 交互平面）
function ArchivePanel({ refreshKey }: { refreshKey: number }) {
  const { data, err, refresh } = usePoll(() => api.documents(), 15000);
  useEffect(() => { if (refreshKey > 0) refresh(); }, [refreshKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const docs: ArchiveDoc[] = data ?? [];
  const [openDoc, setOpenDoc] = useState<ArchiveDoc | null>(null);
  const view = async (id: string) => {
    try { setOpenDoc(await api.document(id)); } catch { /* 忽略 */ }
  };
  return (
    <Panel title="归档文档" note={`交接/复核/终审汇总 · 共 ${docs.length} 篇`}>
      {openDoc && (
        <pre className="doc-view">{openDoc.content}</pre>
      )}
      {docs.length === 0 ? <Empty err={err} /> : (
        <table className="hud-table">
          <thead>
            <tr><th>标题</th><th>类型</th><th>来源</th><th>任务</th><th>字数</th><th>时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            {docs.map((d) => (
              <tr key={d.id}>
                <td>{d.title}</td>
                <td className="hud-running">{d.doc_type}</td>
                <td className="mono-dim">{d.source}</td>
                <td className="mono-dim">{d.task_id.slice(0, 8)}</td>
                <td className="mono-dim">{d.word_count}</td>
                <td className="mono-dim">{fmtTime(d.created_at)}</td>
                <td><button className="hud-btn" onClick={() => view(d.id)}>查看</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// M5 成本看板（/monitor/cost）
function CostPanel({ refreshKey }: { refreshKey: number }) {
  const { data, err } = usePoll(() => api.costsSummary(), 15000);
  useEffect(() => { /* SSE 驱动 */ }, [refreshKey]);
  const sums: CostSummary[] = data?.data ?? [];
  const global = data?.global_cents ?? 0;
  return (
    <Panel title="成本中心" note={`全局累计 ${global.toFixed(1)} 分`}>
      {sums.length === 0 ? <Empty err={err} /> : (
        <table className="hud-table">
          <thead>
            <tr><th>维度</th><th>对象</th><th>累计成本（分）</th><th>记录数</th></tr>
          </thead>
          <tbody>
            {sums.map((c, i) => (
              <tr key={`${c.scope}-${c.scope_id}-${i}`}>
                <td>{c.scope}</td>
                <td className="mono-dim">{c.scope_id.slice(0, 8)}</td>
                <td className={c.total_cents > 0 ? 'hud-running' : 'mono-dim'}>{c.total_cents.toFixed(1)}</td>
                <td className="mono-dim">{c.records}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// M8 命令终端（/ops 平面，danger 操作）
function CommandPanel({ refreshKey }: { refreshKey: number }) {
  const { data, err, refresh } = usePoll(() => api.commandTerminals(), 10000);
  useEffect(() => { if (refreshKey > 0) refresh(); }, [refreshKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const terminals: CommandTerminal[] = data ?? [];
  const [name, setName] = useState('');
  const [secret, setSecret] = useState<string | null>(null);
  const [cmd, setCmd] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const register = async () => {
    if (!name.trim() || busy) return;
    setBusy(true);
    try {
      const res = await api.registerCommandTerminal(name.trim());
      setSecret(res.secret); // 一次性展示（§6.4.14 A2）
      setName('');
      refresh();
    } catch (x) {
      setMsg(String((x as Error).message ?? x));
    } finally {
      setBusy(false);
    }
  };

  const exec = async (id: string) => {
    if (!cmd.trim() || busy) return;
    setBusy(true);
    try {
      await api.execCommand(id, cmd.trim());
      setMsg(`指令已签发，等待回执（msg ${Date.now()}）`);
    } catch (x) {
      setMsg(String((x as Error).message ?? x));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel title="命令终端" note="受控服务器 Agent · HMAC 签发 + 白名单 + 审计">
      <div className="task-form task-sub">
        <input value={name} onChange={(e) => setName(e.target.value)}
          placeholder="新终端名称（登记后生成独立密钥，仅展示一次）" aria-label="终端名" />
        <button className="hud-btn" onClick={register} disabled={busy || !name.trim()}>登记终端</button>
        <input value={cmd} onChange={(e) => setCmd(e.target.value)}
          placeholder="待签发指令（须命中终端白名单，如 systeminfo / whoami）" aria-label="指令" />
      </div>
      {secret && (
        <p className="hud-completed" style={{ fontSize: 12 }}>
          新终端密钥（仅此一次，请立即写入目标机 .env 的 MACS_CMDTERM_SECRET）：<code>{secret}</code>
        </p>
      )}
      {msg && <p className="hint">{msg}</p>}
      {terminals.length === 0 ? <Empty err={err} /> : (
        <table className="hud-table">
          <thead>
            <tr><th>名称</th><th>状态</th><th>主机</th><th>地址</th><th>心跳</th><th>签发指令</th></tr>
          </thead>
          <tbody>
            {terminals.map((t) => (
              <tr key={t.id}>
                <td>{t.name}</td>
                <td className={statusClass(t.status)}>{t.status}</td>
                <td className="mono-dim">{t.hostname || '-'}</td>
                <td className="mono-dim">{t.last_ip ? `${t.last_ip}:${t.listen_port}` : '-'}</td>
                <td className="mono-dim">{fmtTime(t.last_heartbeat)}</td>
                <td>
                  <button className="hud-btn" disabled={busy || t.status !== 'online' || !cmd.trim()}
                    onClick={() => exec(t.id)}>签发</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// M6 平台与协议驱动（/ops 平台注册表）
function PlatformsPanel({ refreshKey }: { refreshKey: number }) {
  const { data, err } = usePoll(() => api.agentPlatforms(), 20000);
  useEffect(() => { /* SSE 驱动 */ }, [refreshKey]);
  const platforms: AgentPlatform[] = data ?? [];
  return (
    <Panel title="Agent 平台注册表" note="统一协议三表（6.4.13.1）· 系统内置">
      {platforms.length === 0 ? <Empty err={err} /> : (
        <table className="hud-table">
          <thead>
            <tr><th>平台</th><th>厂商</th><th>版本</th><th>默认驱动</th><th>状态</th><th>能力</th></tr>
          </thead>
          <tbody>
            {platforms.map((p) => (
              <tr key={p.id}>
                <td className="hud-running">{p.platform_name}</td>
                <td className="mono-dim">{p.vendor || '-'}</td>
                <td className="mono-dim">{p.version}</td>
                <td className="mono-dim">{p.default_driver}</td>
                <td className={p.is_active ? 'hud-completed' : 'hud-failed'}>{p.is_active ? 'active' : 'inactive'}</td>
                <td className="mono-dim">{p.capabilities}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

// 事件去重键：重连时塔会回放历史帧，避免重复行
const keyOf = (e: StreamEvent) => `${e.ts}|${e.event}|${e.data?.subtask_id ?? e.data?.id ?? ''}`;

// 单页布局：任务编排 + 审批 + 质量 + 归档 + 成本 + 命令终端 + 平台 + 集群 + 调度记录
export function App() {
  const inc = usePoll(api.incubators, 10000);
  const dsp = usePoll(api.dispatchLog, 10000);
  const [filter, setFilter] = useState('');
  const [q, setQ] = useState('');
  const tsk = usePoll(() => api.tasks({ status: filter || undefined, q: q.trim() || undefined }), 5000);
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [lastAt, setLastAt] = useState('-');
  const [refreshKey, setRefreshKey] = useState(0);
  const bump = useCallback(() => setRefreshKey((k) => k + 1), []);

  const live = useEventStream((ev) => {
    setLastAt(fmtTime(ev.ts));
    setEvents((prev) => [ev, ...prev.filter((x) => keyOf(x) !== keyOf(ev))].slice(0, 12));
    if (TASK_EVENTS.includes(ev.event)) {
      tsk.refresh();
      dsp.refresh();
      if (['quality_event', 'approval_created', 'approval_decided', 'spot_check_finished', 'budget_exceeded', 'cost_recorded', 'guardrail_event', 'cmd_result'].includes(ev.event)) {
        bump();
      }
    }
  });

  const incubators: Incubator[] = inc.data ?? [];
  const dispatch: DispatchRow[] = dsp.data ?? [];
  const tasks: Task[] = tsk.data ?? [];
  const online = incubators.filter((i) => i.Status === 'online').length;
  const running = tasks.filter((t) => ['running', 'pending', 'in_spot_check', 'partial_failure'].includes(t.status)).length;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1 className="page-title">MACS 控制塔</h1>
          <p className="page-sub">
            运维 / 监控 / 交互 三平面 · M3 质量 · M5 成本 · M6 协议 · M8 命令终端 ·
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

      <Panel title="任务编排" note="内置拆解 / CLI / HTTP / OpenAI 协议驱动 → 复核 → 抽查 → 归档">
        <div className="task-form task-sub">
          <select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="状态筛选" style={{ maxWidth: 150 }}>
            <option value="">全部状态</option>
            <option value="pending">pending</option>
            <option value="running">running</option>
            <option value="in_spot_check">in_spot_check</option>
            <option value="completed">completed</option>
            <option value="partial_failure">partial_failure</option>
            <option value="blocked">blocked</option>
            <option value="failed">failed</option>
            <option value="cancelled">cancelled</option>
          </select>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索目标关键词（历史记录 V3.3）" aria-label="搜索" />
        </div>
        <TaskBoard tasks={tasks} err={tsk.err} onChanged={bump} />
      </Panel>

      <ApprovalsPanel refreshKey={refreshKey} onChanged={bump} />
      <QualityPanel refreshKey={refreshKey} />
      <CostPanel refreshKey={refreshKey} />
      <ArchivePanel refreshKey={refreshKey} />

      <Panel title="实时事件" note={live ? 'SSE 已连接 · 事件驱动刷新' : 'SSE 重连中 · 轮询兜底'}>
        {events.length === 0 ? (
          <p className="hint">等待事件。提交任务后会实时出现「任务创建 / 子任务下发 / 质量事件 / 任务完成」。</p>
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
                <th>槽位</th>
                <th>负载</th>
                <th>地址</th>
                <th>最后心跳</th>
              </tr>
            </thead>
            <tbody>
              {incubators.map((i) => (
                <tr key={i.ID}>
                  <td>{i.Name}</td>
                  <td className={statusClass(i.Status)}>{i.Status}</td>
                  <td>{i.Capacity - (i.ActiveWorkers ?? 0)} / {i.Capacity}</td>
                  <td className="mono-dim">{Math.round((i.LastLoad ?? 0) * 100)}%</td>
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

      <CommandPanel refreshKey={refreshKey} />
      <PlatformsPanel refreshKey={refreshKey} />
    </div>
  );
}
