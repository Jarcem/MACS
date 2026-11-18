// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"macs.local/tower/agentmsg"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/plan"
	"macs.local/tower/internal/store"
)

// ---------- 测试装置 ----------

type fakeAgentGateway struct {
	mu       sync.Mutex
	messages []agentmsg.Envelope
	online   map[string]bool
}

func newFakeAgentGateway() *fakeAgentGateway {
	return &fakeAgentGateway{online: map[string]bool{}}
}

func (f *fakeAgentGateway) DispatchTo(connectorID string, env agentmsg.Envelope) error {
	f.mu.Lock()
	f.messages = append(f.messages, env)
	f.mu.Unlock()
	return nil
}

func (f *fakeAgentGateway) Connected(connectorID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[connectorID]
}

func (f *fakeAgentGateway) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

func (f *fakeAgentGateway) all() []agentmsg.Envelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentmsg.Envelope{}, f.messages...)
}

func newSchedStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	return st
}

// seedVerifiedAgent 预置 Connector + verified Agent + enabled action（返回 agentID/actionID）
func seedVerifiedAgent(t *testing.T, st *store.Store, connectorID, agentKey, actionKey string) (string, string) {
	t.Helper()
	if err := st.UpsertConnector(store.Connector{ID: connectorID, Name: connectorID, CredentialID: "cr", CredentialHash: "h", ProtocolVersion: "1.0"}); err != nil {
		t.Fatal(err)
	}
	inst, err := st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: connectorID, LocalAgentKey: agentKey, DisplayName: agentKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(`UPDATE agent_instances SET identity_status='verified', platform_id=?, adapter_type='cli_agent', revision=2 WHERE id=?`, agentKey, inst.ID); err != nil {
		t.Fatal(err)
	}
	act, err := st.UpsertAgentAction(store.AgentAction{
		AgentID: inst.ID, ActionKey: actionKey, AdapterType: "cli_agent",
		ValidatorKeys: []string{"test_report", "json_field_equals", "manual_review"},
		ConfigRef:     actionKey, Enabled: true, MaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return inst.ID, act.ID
}

func newScheduler(st *store.Store, gw AgentDispatcher) *AgentScheduler {
	hub := events.New()
	cfg := AgentSchedConfig{LeaseTTL: 500 * time.Millisecond, MaxAttempts: 3}
	return NewAgentScheduler(st, gw, hub, cfg)
}

// twoStepDraft develop → deploy（deploy 声明上游 commit 制品）
func twoStepDraft(agentID, actionKey string) *plan.Draft {
	return &plan.Draft{
		Goal: "develop and deploy", TargetEnvironment: "staging",
		Steps: []plan.Step{
			{
				StepKey: "develop", Instruction: "implement fixture",
				AgentID: agentID, ActionKey: actionKey,
				ExpectedOutputs:    []string{"commit"},
				AcceptanceCriteria: []plan.Criterion{{ValidatorKey: "test_report"}},
			},
			{
				StepKey: "deploy", Instruction: "deploy it",
				AgentID: agentID, ActionKey: actionKey,
				DependsOn: []string{"develop"}, InputRefs: []string{"commit"},
				AcceptanceCriteria: []plan.Criterion{{ValidatorKey: "test_report"}},
			},
		},
	}
}

// submitAndApprove 提交 + 批准，返回 taskID
func submitAndApprove(t *testing.T, s *AgentScheduler, d *plan.Draft) (string, store.PlanRevision) {
	t.Helper()
	rev, err := s.SubmitDraft(d, "test")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := s.Approve(rev.TaskID, rev.Revision, "admin-1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return rev.TaskID, rev
}

func resultEnvelope(t *testing.T, a store.ExecutionAttempt, payload agentmsg.ResultPayload) agentmsg.Envelope {
	t.Helper()
	pb, _ := json.Marshal(payload)
	return agentmsg.Envelope{
		MessageType: agentmsg.MsgResult, ConnectorID: "c1",
		TaskID: a.TaskID, StepID: a.StepID, PlanRevision: itoa(a.PlanRevision),
		AgentID: a.AgentID, AttemptID: a.AttemptID, InputDigest: a.InputDigest,
		Payload: pb,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func passedResult() agentmsg.ResultPayload {
	return agentmsg.ResultPayload{
		Status: "completed", Summary: "did it",
		SelfCheck: json.RawMessage(`{"status":"passed","findings":[]}`),
	}
}

// ---------- Task 4：冻结分派（离线等待，不改派） ----------

func TestStrictDispatchWaitsForFrozenAgentAndDoesNotReassign(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway() // Agent 离线
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	if gw.count() != 0 {
		t.Fatal("offline agent received dispatch")
	}
	// 无 attempt 创建（任务保持 pending）
	attempts, _ := st.ListAttemptsByTask(taskID)
	if len(attempts) != 0 {
		t.Fatalf("attempt created while offline: %+v", attempts)
	}
	// 冻结分派不变
	assign, err := st.GetTaskAssignment(taskID, "develop", 1)
	if err != nil || assign.AgentID != agentID {
		t.Fatalf("assignment changed: %+v err=%v", assign, err)
	}
	// 任务仍 running（等待，不失败）
	task, _ := st.GetAgentTask(taskID)
	if task.Status != "running" {
		t.Fatalf("task status: %s", task.Status)
	}
}

// 在线 Agent：冻结节点的定向下发（携带冻结 digest 与幂等键）
func TestFrozenDispatchToOnlineAgent(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	msgs := gw.all()
	if len(msgs) != 1 || msgs[0].MessageType != agentmsg.MsgDispatch {
		t.Fatalf("expected 1 dispatch: %+v", msgs)
	}
	env := msgs[0]
	assign, _ := st.GetTaskAssignment(taskID, "develop", 1)
	if env.AgentID != agentID || env.ActionID != assign.ActionID || env.InputDigest != assign.InputDigest {
		t.Fatalf("frozen assignment not honored: %+v", env)
	}
	if env.IdempotencyKey == "" || env.AttemptID == "" {
		t.Fatal("idempotency key / attempt id missing")
	}
	attempts, _ := st.ListAttemptsByTask(taskID)
	if len(attempts) != 1 || attempts[0].Status != "dispatched" {
		t.Fatalf("attempt state: %+v", attempts)
	}
}

// 计划批准后重复批准被拒绝（修改必须新建修订）
func TestApproveImmutable(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	s := newScheduler(st, newFakeAgentGateway())
	taskID, rev := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))
	if _, err := s.Approve(taskID, rev.Revision, "admin-1"); err == nil {
		t.Fatal("double approve must fail")
	}
}

// ---------- Task 5：结果核销 / 自检失败关闭 / 验收 ----------

func TestAgentResultAcceptanceGatesDownstream(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	var develop store.ExecutionAttempt
	for _, a := range attempts {
		if a.StepID == "develop" {
			develop = a
		}
	}

	// 自检缺失 → 失败关闭，下游不调度
	s.HandleAgentResult(resultEnvelope(t, develop, agentmsg.ResultPayload{Status: "completed", Summary: "no self check"}))
	got, _ := st.GetExecutionAttempt(develop.AttemptID)
	if got.AcceptanceStatus != "failed" {
		t.Fatalf("missing self_check must fail closed: %+v", got)
	}
	if gw.count() != 1 {
		t.Fatalf("downstream dispatched after failed acceptance: %d", gw.count())
	}
}

func TestAgentResultPassesAndInjectsUpstreamArtifact(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	var develop store.ExecutionAttempt
	for _, a := range attempts {
		if a.StepID == "develop" {
			develop = a
		}
	}

	// 成功结果 + 自检通过 + commit 制品（共享 URI）
	res := passedResult()
	res.ArtifactRefs = []agentmsg.ArtifactRef{{
		URI: "https://127.0.0.1/store/fixture.commit", Digest: "sha256:" + strings.Repeat("a", 64),
		Kind: "commit", SizeBytes: 128,
	}}
	s.HandleAgentResult(resultEnvelope(t, develop, res))

	got, _ := st.GetExecutionAttempt(develop.AttemptID)
	if got.Status != "completed" || got.AcceptanceStatus != "passed" {
		t.Fatalf("develop not passed: %+v", got)
	}
	// 制品落库
	arts, _ := st.ListArtifactsForStep(taskID, "develop", 1)
	if len(arts) != 1 || arts[0].Kind != "commit" {
		t.Fatalf("artifact not persisted: %+v", arts)
	}
	// 下游注入上游制品
	s.scheduleOnce(context.Background())
	msgs := gw.all()
	if len(msgs) != 2 {
		t.Fatalf("downstream not dispatched: %d messages", len(msgs))
	}
	var deployEnv agentmsg.Envelope
	for _, m := range msgs {
		if m.StepID == "deploy" {
			deployEnv = m
		}
	}
	if len(deployEnv.InputRefs) != 1 || deployEnv.InputRefs[0].Kind != "commit" || deployEnv.InputRefs[0].URI != res.ArtifactRefs[0].URI {
		t.Fatalf("upstream artifact not injected: %+v", deployEnv.InputRefs)
	}
}

// Agent 自报 completed 不能改写验收状态（验证器失败 → failed）
func TestValidatorFailureBlocksArchive(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	var develop store.ExecutionAttempt
	for _, a := range attempts {
		if a.StepID == "develop" {
			develop = a
		}
	}
	// 自检声明 passed，但验证器（test_report 要求 self_check.status==passed）通过；
	// 再加一个 json_field_equals 失败的标准 → 验收 failed
	rev := s.latestApprovedRevision(taskID)
	_ = rev
	draft, _ := st.GetPlanRevision(taskID, 1)
	d, _ := decodeDraftHelper(draft.DraftJSON)
	d.Steps[0].AcceptanceCriteria = append(d.Steps[0].AcceptanceCriteria, plan.Criterion{
		ValidatorKey: "json_field_equals", Params: json.RawMessage(`{"field":"summary","value":"expected"}`),
	})
	dj, _ := plan.MarshalDraft(d)
	_, _ = st.Exec(`UPDATE plan_revisions SET draft_json=? WHERE task_id=? AND revision=1`, dj, taskID)

	s.HandleAgentResult(resultEnvelope(t, develop, passedResult()))
	got, _ := st.GetExecutionAttempt(develop.AttemptID)
	if got.AcceptanceStatus != "failed" {
		t.Fatalf("agent self-report overrode acceptance: %+v", got)
	}
	task, _ := st.GetAgentTask(taskID)
	if task.Status == "completed" {
		t.Fatal("task archived despite failed acceptance")
	}
}

// 迟到结果不覆盖取消终态（设计 §8）
func TestCancelledAttemptIgnoresLateResult(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)

	if _, err := s.CancelAgentTask(taskID); err != nil {
		t.Fatal(err)
	}
	task, _ := st.GetAgentTask(taskID)
	if task.Status != "cancelled" {
		t.Fatalf("task status: %s", task.Status)
	}
	// 迟到结果
	s.HandleAgentResult(resultEnvelope(t, attempts[0], passedResult()))
	got, _ := st.GetExecutionAttempt(attempts[0].AttemptID)
	if got.Status != "cancelled" {
		t.Fatalf("late result overwrote cancelled: %s", got.Status)
	}
}

// ---------- Task 8：上游失败下游不调度 ----------

func TestDownstreamNotDispatchedWhenUpstreamFailed(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	var develop store.ExecutionAttempt
	for _, a := range attempts {
		if a.StepID == "develop" {
			develop = a
		}
	}
	// 上游执行失败（无自检 → acceptance failed）
	s.HandleAgentResult(resultEnvelope(t, develop, agentmsg.ResultPayload{
		Status: "failed", Error: "boom",
	}))
	got, _ := st.GetExecutionAttempt(develop.AttemptID)
	if got.Status != "failed" {
		t.Fatalf("develop status: %s", got.Status)
	}
	for i := 0; i < 3; i++ {
		s.scheduleOnce(context.Background())
	}
	if gw.count() != 1 {
		t.Fatalf("downstream dispatched after upstream failure: %d", gw.count())
	}
}

// ---------- 重连对账（设计 §8） ----------

func TestReconcileUnknownDoesNotRerun(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	att := attempts[0]

	// Connector 对账：状态不明
	pb, _ := json.Marshal([]agentmsg.ReconcileOutcome{{AttemptID: att.AttemptID, Status: "unknown",
		Error: "restart mid-run"}})
	s.HandleAgentResult(agentmsg.Envelope{MessageType: agentmsg.MsgReconcileResult, ConnectorID: "c1", Payload: pb})

	got, _ := st.GetExecutionAttempt(att.AttemptID)
	if got.Status != "unknown" {
		t.Fatalf("unknown not recorded: %s", got.Status)
	}
	// 不立即重试
	s.scheduleOnce(context.Background())
	if gw.count() != 1 {
		t.Fatalf("unknown re-dispatched immediately: %d", gw.count())
	}
}

// 对账 finished（带既有结果）→ 正常核销
func TestReconcileFinishedSettlesAttempt(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	att := attempts[0]

	res := passedResult()
	pb, _ := json.Marshal([]agentmsg.ReconcileOutcome{{AttemptID: att.AttemptID, Status: "finished", Result: &res}})
	s.HandleAgentResult(agentmsg.Envelope{MessageType: agentmsg.MsgReconcileResult, ConnectorID: "c1", Payload: pb})

	got, _ := st.GetExecutionAttempt(att.AttemptID)
	if got.Status != "completed" || got.AcceptanceStatus != "passed" {
		t.Fatalf("reconciled result not settled: %+v", got)
	}
}

// 租约到期看门狗：retryable 重试新 attempt；耗尽 → failed
func TestLeaseWatchdogRetriesAndExhausts(t *testing.T) {
	st := newSchedStore(t)
	agentID, _ := seedVerifiedAgent(t, st, "c1", "zcode", "zcode.develop")
	gw := newFakeAgentGateway()
	gw.online["c1"] = true
	s := newScheduler(st, gw)
	taskID, _ := submitAndApprove(t, s, twoStepDraft(agentID, "zcode.develop"))

	s.scheduleOnce(context.Background())
	// 等租约过期（500ms TTL）
	time.Sleep(700 * time.Millisecond)
	// 手动触发看门狗逻辑：过期 attempt 回收（调度器循环里 5s 一跳，测试直接等一次循环太慢，
	// 直接调用回收逻辑）
	expired, _ := st.ListExpiredAttempts(store.FormatLease(time.Now()))
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired attempt, got %d", len(expired))
	}
	s.runWatchdogOnce()
	// 新 attempt 创建（调度下一轮）
	s.scheduleOnce(context.Background())
	attempts, _ := st.ListAttemptsByTask(taskID)
	if len(attempts) != 2 {
		t.Fatalf("expected retry attempt, got %d", len(attempts))
	}
	// 耗尽：连续 3 次到期
	for i := 0; i < 5; i++ {
		time.Sleep(600 * time.Millisecond)
		s.runWatchdogOnce()
		s.scheduleOnce(context.Background())
	}
	attempts, _ = st.ListAttemptsByTask(taskID)
	var latest store.ExecutionAttempt
	for _, a := range attempts {
		if a.StepID == "develop" && a.AttemptNo > latest.AttemptNo {
			latest = a
		}
	}
	if latest.Status != "failed" {
		t.Fatalf("attempts not exhausted to failed: %+v", latest)
	}
	task, _ := st.GetAgentTask(taskID)
	if task.Status != "failed" {
		t.Fatalf("task not failed: %s", task.Status)
	}
}

func decodeDraftHelper(raw string) (*plan.Draft, error) {
	return plan.DecodeDraft([]byte(raw))
}
