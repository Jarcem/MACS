// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// AgentCollabPanel V1.5 位置无关 Agent 协作管理面（设计 §4 人工标注 / §5.3 计划批准）
// 五段式：Connector → 候选标注 → 动作登记 → 计划提交/批准 → 任务/尝试
import { useCallback, useEffect, useState } from 'react';
import { api, type AgentAction, type AgentInstance, type AgentTask, type ExecutionAttempt, type IdentityAudit, type PlanRevision } from './api';

const sClass = (s: string): string => {
  if (['online', 'completed', 'passed', 'verified', 'approved'].includes(s)) return 'hud-running';
  if (['running', 'dispatched', 'lost'].includes(s)) return 'hud-checking';
  if (['pending', 'draft'].includes(s)) return 'hud-checking';
  if (['needs_human', 'unknown'].includes(s)) return 'hud-warn';
  if (['offline', 'failed', 'rejected', 'cancelled', 'disabled', 'revoked'].includes(s)) return 'hud-failed';
  return 'hud-checking';
};

function Row({ children }: { children: React.ReactNode }) {
  return <div className="collab-row">{children}</div>;
}

// ── 1. Connector 登记 ──────────────────────────────
function ConnectorsSection({ onChanged }: { onChanged: () => void }) {
  const [rows, setRows] = useState<{ id: string; name: string; credential_id: string; protocol_version: string; online_status: string; last_seen: string; revoked_at: string }[]>([]);
  const [name, setName] = useState('');
  const [issued, setIssued] = useState<{ connector_id: string; credential: string } | null>(null);
  const [err, setErr] = useState('');

  const load = useCallback(async () => {
    try { setRows(await api.agentConnectors()); } catch { /* 静默 */ }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const register = async () => {
    if (!name.trim()) return;
    try {
      const r = await api.registerAgentConnector(name.trim());
      setIssued({ connector_id: r.data.connector_id, credential: r.data.credential });
      setName('');
      onChanged();
    } catch (e) { setErr(String(e)); }
  };

  return (
    <div>
      <h3 className="collab-h3">① Connector 登记 <span className="panel-note">凭据仅此一次返回 · 塔只存 SHA-256</span></h3>
      <Row>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="名称，如 mac-dev / cloud-deploy" style={{ maxWidth: 220 }} />
        <button className="hud-btn" onClick={() => void register()}>登记</button>
        {err && <span className="hud-failed">{err}</span>}
      </Row>
      {issued && (
        <div className="collab-issued">
          <div>connector_id: <code>{issued.connector_id}</code></div>
          <div>credential: <code>{issued.credential}</code>（写入 Connector 本机配置，不再展示）</div>
        </div>
      )}
      <table className="hud-table">
        <thead><tr><th>ID</th><th>名称</th><th>协议</th><th>状态</th><th>最近心跳</th><th></th></tr></thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id}>
              <td className="mono-dim">{c.id}</td>
              <td>{c.name}</td>
              <td>{c.protocol_version}</td>
              <td className={c.revoked_at ? 'hud-failed' : sClass(c.online_status)}>
                {c.revoked_at ? 'revoked' : c.online_status}
              </td>
              <td className="mono-dim">{c.last_seen || '-'}</td>
              <td>
                {!c.revoked_at && (
                  <button className="hud-btn" onClick={async () => { await api.revokeAgentConnector(c.id); onChanged(); }}>吊销</button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="hint">暂无 Connector。孵化器侧以 MACS_CONNECTOR_CONFIG 启动 connector 模式后自动上报。</p>}
    </div>
  );
}

// ── 2. 候选人工标注（设计 §4）──────────────────────
function InstancesSection({ onChanged }: { onChanged: () => void }) {
  const [rows, setRows] = useState<AgentInstance[]>([]);
  const [actions, setActions] = useState<Record<string, AgentAction[]>>({});
  const [editing, setEditing] = useState<AgentInstance | null>(null);
  const [display, setDisplay] = useState('');
  const [platform, setPlatform] = useState('');
  const [roles, setRoles] = useState('');
  const [caps, setCaps] = useState('');
  const [envs, setEnvs] = useState('');
  const [actKey, setActKey] = useState('');
  const [err, setErr] = useState('');

  const load = useCallback(async () => {
    try {
      const all = await api.agentInstances();
      setRows(all);
      const am: Record<string, AgentAction[]> = {};
      for (const a of all) {
        am[a.id] = await api.agentActions(a.id);
      }
      setActions(am);
    } catch { /* 静默 */ }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const startEdit = (a: AgentInstance) => {
    setEditing(a);
    setDisplay(a.display_name);
    setPlatform(a.platform_id);
    setRoles(a.role_tags.join(','));
    setCaps(a.capability_tags.join(','));
    setEnvs(a.environment_tags.join(','));
    setActKey('');
    setErr('');
  };

  const verify = async () => {
    if (!editing) return;
    try {
      await api.verifyAgentInstance(editing.id, {
        display_name: display, platform_id: platform, adapter_type: editing.adapter_type || 'cli_agent',
        role_tags: roles.split(',').map((x) => x.trim()).filter(Boolean),
        capability_tags: caps.split(',').map((x) => x.trim()).filter(Boolean),
        environment_tags: envs.split(',').map((x) => x.trim()).filter(Boolean),
        expected_revision: editing.revision, actor: 'admin',
      });
      // 登记动作（可选）
      if (actKey.trim()) {
        await api.upsertAgentAction({
          agent_id: editing.id, action_key: actKey.trim(), adapter_type: platform === 'zcode' ? 'cli_agent' : 'cli_agent',
          validator_keys: ['test_report', 'manual_review'], enabled: true, config_ref: actKey.trim(),
        });
      }
      setEditing(null);
      onChanged();
      void load();
    } catch (e) { setErr(String(e)); }
  };

  const split = (t: string[]) => (t.length ? t.join(', ') : '-');

  return (
    <div>
      <h3 className="collab-h3">② 身份标注 <span className="panel-note">pending → verified（乐观锁）；未 verified 不进规划</span></h3>
      <table className="hud-table">
        <thead><tr><th>Agent</th><th>Connector</th><th>平台</th><th>身份</th><th>标签</th><th>动作</th><th></th></tr></thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id}>
              <td>{a.display_name}<span className="mono-dim"> · {a.local_agent_key}</span></td>
              <td className="mono-dim">{a.connector_id}</td>
              <td>{a.platform_id || '-'}<span className="mono-dim"> / {a.adapter_type || '-'}</span></td>
              <td className={sClass(a.identity_status)}>
                {a.identity_status}<span className="mono-dim"> r{a.revision}</span>
              </td>
              <td className="mono-dim">{split(a.role_tags)} / {split(a.capability_tags)} / {split(a.environment_tags)}</td>
              <td className="mono-dim">
                {(actions[a.id] ?? []).map((x) => `${x.action_key}${x.enabled ? '' : '(停用)'}`).join('、') || '-'}
              </td>
              <td>
                {a.identity_status !== 'disabled' && (
                  <>
                    <button className="hud-btn" onClick={() => startEdit(a)}>{a.identity_status === 'verified' ? '改注' : '标注'}</button>
                    <button className="hud-btn" onClick={async () => { await api.disableAgentInstance(a.id); onChanged(); void load(); }}>禁用</button>
                  </>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="hint">暂无候选。Connector 上线后自动上报本机 Agent 候选。</p>}
      {editing && (
        <div className="collab-edit">
          <Row><span className="panel-note">名称</span><input value={display} onChange={(e) => setDisplay(e.target.value)} /></Row>
          <Row><span className="panel-note">平台</span><input value={platform} onChange={(e) => setPlatform(e.target.value)} placeholder="zcode / openclaw …" /></Row>
          <Row><span className="panel-note">角色</span><input value={roles} onChange={(e) => setRoles(e.target.value)} placeholder="developer" /></Row>
          <Row><span className="panel-note">能力</span><input value={caps} onChange={(e) => setCaps(e.target.value)} placeholder="repo.write, test.run" /></Row>
          <Row><span className="panel-note">环境</span><input value={envs} onChange={(e) => setEnvs(e.target.value)} placeholder="dev, staging" /></Row>
          <Row><span className="panel-note">登记动作</span><input value={actKey} onChange={(e) => setActKey(e.target.value)} placeholder="如 zcode.develop（可空）" /></Row>
          {err && <p className="hud-failed">{err}</p>}
          <Row>
            <button className="hud-btn hud-btn-active" onClick={() => void verify()}>确认 verified</button>
            <button className="hud-btn" onClick={() => setEditing(null)}>取消</button>
          </Row>
        </div>
      )}
    </div>
  );
}

// ── 3. 计划提交 / 批准（设计 §5.3）──────────────────
function PlansSection({ onChanged }: { onChanged: () => void }) {
  const [goal, setGoal] = useState('');
  const [catalog, setCatalog] = useState<{ agent_id: string; display_name: string; platform_id: string; actions: { action_key: string }[]; environment_tags: string[] }[]>([]);
  const [agentSel, setAgentSel] = useState('');
  const [actionSel, setActionSel] = useState('');
  const [instr, setInstr] = useState('');
  const [revs, setRevs] = useState<PlanRevision[]>([]);
  const [err, setErr] = useState('');

  const load = useCallback(async () => {
    try {
      const cat = await api.agentCatalog();
      setCatalog(cat.data.agents);
      if (!agentSel && cat.data.agents.length > 0) {
        setAgentSel(cat.data.agents[0].agent_id);
        setActionSel(cat.data.agents[0].actions[0]?.action_key ?? '');
      }
    } catch { /* 静默 */ }
  }, [agentSel]);
  useEffect(() => { void load(); }, [load]);

  const submit = async () => {
    if (!goal.trim() || !agentSel || !actionSel || !instr.trim()) { setErr('goal/agent/action/instruction 必填'); return; }
    const draft = {
      goal: goal.trim(), target_environment: 'dev',
      steps: [{
        step_key: 'step-1', instruction: instr.trim(), agent_id: agentSel, action_key: actionSel,
        step_kind: 'execute', expected_outputs: ['result'],
        acceptance_criteria: [{ validator_key: 'test_report' }],
      }],
    };
    try {
      const r = await api.submitAgentPlan(draft);
      setErr('');
      setGoal(''); setInstr('');
      const list = await api.agentPlans(r.data.task_id);
      setRevs(list);
      onChanged();
    } catch (e) { setErr(String(e)); }
  };

  const approve = async (taskId: string, revision: number) => {
    try { await api.approveAgentPlan(taskId, revision); onChanged(); } catch (e) { setErr(String(e)); }
  };

  const agentById = (id: string) => catalog.find((a) => a.agent_id === id);

  return (
    <div>
      <h3 className="collab-h3">③ 计划提交与批准 <span className="panel-note">批准后冻结分派（agent/action/input_digest），修改须新修订</span></h3>
      <div className="collab-edit">
        <Row><span className="panel-note">目标</span><input value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="一句话目标" /></Row>
        <Row>
          <span className="panel-note">Agent</span>
          <select value={agentSel} onChange={(e) => { setAgentSel(e.target.value); const a = agentById(e.target.value); setActionSel(a?.actions[0]?.action_key ?? ''); }}>
            {catalog.map((a) => <option key={a.agent_id} value={a.agent_id}>{a.display_name}（{a.platform_id}）</option>)}
          </select>
          <span className="panel-note">动作</span>
          <select value={actionSel} onChange={(e) => setActionSel(e.target.value)}>
            {agentById(agentSel)?.actions.map((x) => <option key={x.action_key} value={x.action_key}>{x.action_key}</option>)}
          </select>
        </Row>
        <Row><span className="panel-note">指令</span><input value={instr} onChange={(e) => setInstr(e.target.value)} placeholder="instruction（不作为 shell 执行）" /></Row>
        {err && <p className="hud-failed">{err}</p>}
        <Row><button className="hud-btn" onClick={() => void submit()}>提交草案（draft）</button></Row>
      </div>
      {revs.length > 0 && (
        <table className="hud-table">
          <thead><tr><th>修订</th><th>来源</th><th>状态</th><th>批准人</th><th></th></tr></thead>
          <tbody>
            {revs.map((r) => (
              <tr key={`${r.task_id}-${r.revision}`}>
                <td>r{r.revision} <span className="mono-dim">{r.task_id.slice(0, 12)}…</span></td>
                <td>{r.source}</td>
                <td className={sClass(r.status)}>{r.status}</td>
                <td className="mono-dim">{r.approved_by || '-'}</td>
                <td>
                  {r.status === 'draft' && (
                    <button className="hud-btn hud-btn-active" onClick={() => void approve(r.task_id, r.revision)}>批准</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ── 4. 任务与尝试 ──────────────────────────────────
function TasksSection({ refreshKey, onChanged }: { refreshKey: number; onChanged: () => void }) {
  const [tasks, setTasks] = useState<AgentTask[]>([]);
  const [attempts, setAttempts] = useState<ExecutionAttempt[]>([]);
  const [sel, setSel] = useState('');
  const [audit, setAudit] = useState<IdentityAudit[]>([]);

  const load = useCallback(async () => {
    try {
      setTasks(await api.agentTasks());
      setAudit(await api.identityAudit());
    } catch { /* 静默 */ }
  }, []);
  useEffect(() => { void load(); }, [load, refreshKey]);
  useEffect(() => {
    if (!sel) { setAttempts([]); return; }
    void api.agentAttempts(sel).then(setAttempts).catch(() => {});
  }, [sel, refreshKey]);

  return (
    <div>
      <h3 className="collab-h3">④ 任务与尝试 <span className="panel-note">执行状态与 acceptance_status 分离 · 缺自检不放行</span></h3>
      <table className="hud-table">
        <thead><tr><th>任务</th><th>目标</th><th>状态</th><th>更新</th><th></th></tr></thead>
        <tbody>
          {tasks.map((t) => (
            <tr key={t.task_id} onClick={() => setSel(t.task_id)} style={{ cursor: 'pointer' }} className={sel === t.task_id ? 'collab-sel' : ''}>
              <td className="mono-dim">{t.task_id.slice(0, 14)}…</td>
              <td>{t.title}</td>
              <td className={sClass(t.status)}>{t.status}</td>
              <td className="mono-dim">{t.updated_at}</td>
              <td>
                {['running', 'needs_human'].includes(t.status) && (
                  <button className="hud-btn" onClick={async (e) => { e.stopPropagation(); await api.cancelAgentTask(t.task_id); onChanged(); void load(); }}>取消</button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {tasks.length === 0 && <p className="hint">暂无 V1.5 任务。</p>}
      {attempts.length > 0 && (
        <table className="hud-table">
          <thead><tr><th>步骤</th><th>attempt</th><th>执行</th><th>验收</th><th>租约</th></tr></thead>
          <tbody>
            {attempts.map((a) => (
              <tr key={a.attempt_id}>
                <td>{a.step_id} <span className="mono-dim">r{a.plan_revision}</span></td>
                <td className="mono-dim">#{a.attempt_no} {a.attempt_id.slice(0, 8)}…</td>
                <td className={sClass(a.status)}>{a.status}</td>
                <td className={sClass(a.acceptance_status)}>{a.acceptance_status}</td>
                <td className="mono-dim">{a.lease_expires_at || '-'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {audit.length > 0 && (
        <details className="collab-audit">
          <summary className="panel-note">身份/批准审计（近 {audit.length} 条）</summary>
          <table className="hud-table">
            <thead><tr><th>时间</th><th>动作</th><th>对象</th><th>操作人</th></tr></thead>
            <tbody>
              {audit.slice(0, 20).map((e) => (
                <tr key={e.id}>
                  <td className="mono-dim">{e.created_at}</td>
                  <td>{e.action}</td>
                  <td className="mono-dim">{e.agent_id ? `agent ${e.agent_id.slice(0, 8)}…` : `task ${e.task_id.slice(0, 10)}…`}</td>
                  <td>{e.actor}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </div>
  );
}

// ── 面板组装 ───────────────────────────────────────
export function AgentCollabPanel({ refreshKey, compact }: { refreshKey?: number; compact?: boolean }) {
  const [key, setKey] = useState(0);
  const bump = useCallback(() => setKey((k) => k + 1), []);
  return (
    <div className="collab-panel" style={compact ? {} : { padding: 16 }}>
      <p className="hint">
        V1.5 位置无关 Agent 协作：WSS（/agent/connect）是唯一跨网络控制通道。
        Connector 出站连入 → 上报候选 → 人工标注 verified → 提交计划 → 人工批准（冻结分派）→ 定向下发 → 结构化结果 → 验收（失败关闭）。
      </p>
      <ConnectorsSection onChanged={bump} />
      <InstancesSection onChanged={bump} />
      <PlansSection onChanged={bump} />
      <TasksSection refreshKey={(refreshKey ?? 0) + key} onChanged={bump} />
    </div>
  );
}
