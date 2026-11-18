// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// agent_scheduler.go V1.5 Agent 协作调度器（设计 §5/§6/§8）
// 与 2.x UDP 编排路径完全分离：
//   - 只调度「人工批准」的冻结分派（plan_revision + agent_id + input_digest）
//   - Agent 离线任务保持 pending，不改派（无 top-K、无广播竞速、无 first-ACK-wins）
//   - 结果按 plan_revision + attempt_id 核销；终态（completed/failed/cancelled）不被迟到结果覆盖
//   - 执行状态与 acceptance_status 分离；缺自检/验证器失败不得放行（失败关闭）
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"macs.local/tower/agentmsg"
	"macs.local/tower/internal/acceptance"
	"macs.local/tower/internal/agentdir"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/plan"
	"macs.local/tower/internal/quality"
	"macs.local/tower/internal/store"
)

// AgentDispatcher WSS 网关下发接口（main.go 注入 agentgate.Gateway）
type AgentDispatcher interface {
	DispatchTo(connectorID string, env agentmsg.Envelope) error
	Connected(connectorID string) bool
}

// AgentSchedConfig 调度配置
type AgentSchedConfig struct {
	LeaseTTL    time.Duration // attempt 租约（默认 30m）
	MaxAttempts int           // action 未指定时的兜底
}

// AgentScheduler V1.5 调度器
type AgentScheduler struct {
	st      *store.Store
	gw      AgentDispatcher
	hub     *events.Hub
	cfg     AgentSchedConfig
	kick    chan struct{}
	mu      sync.Mutex // scheduleOnce 串行化
	nowFunc func() time.Time
}

func NewAgentScheduler(st *store.Store, gw AgentDispatcher, hub *events.Hub, cfg AgentSchedConfig) *AgentScheduler {
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 30 * time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	return &AgentScheduler{
		st: st, gw: gw, hub: hub, cfg: cfg,
		kick: make(chan struct{}, 1),
		// 统一 UTC（租约/时间戳走字符串比较，本地时区偏移会破坏字典序）
		nowFunc: func() time.Time { return time.Now().UTC() },
	}
}

// Notify 唤醒调度（结果/批准后推进下游）
func (s *AgentScheduler) Notify() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Start 调度循环 + 租约看门狗
func (s *AgentScheduler) Start() {
	go s.loop()
	go s.leaseWatchdog()
}

func (s *AgentScheduler) loop() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	ctx := context.Background()
	for {
		select {
		case <-s.kick:
		case <-tick.C:
		}
		s.scheduleOnce(ctx)
	}
}

// leaseWatchdog 租约到期回收（设计 §8）：retryable → 重试新 attempt；
// 非重试/耗尽 → failed；状态不明（unknown/non_retryable）→ needs_human 转人工
func (s *AgentScheduler) leaseWatchdog() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		s.runWatchdogOnce()
	}
}

// runWatchdogOnce 单次租约回收（测试可直接触发）
func (s *AgentScheduler) runWatchdogOnce() {
	now := s.nowFunc().UTC().Format(time.RFC3339)
	expired, err := s.st.ListExpiredAttempts(now)
	if err != nil {
		return
	}
	{
		changed := false
		for _, a := range expired {
			s.mu.Lock()
			action, _ := s.st.GetAgentAction(a.ActionID)
			maxAttempts := s.cfg.MaxAttempts
			if action.MaxAttempts > 0 {
				maxAttempts = action.MaxAttempts
			}
			switch {
			case action.RetryPolicy == "non_retryable":
				// non_retryable 状态不明转人工（设计 §8）
				_, _ = s.st.SetExecutionAttemptStatus(a.AttemptID, "needs_human")
			case a.AttemptNo >= maxAttempts:
				if a.Status == "unknown" {
					_, _ = s.st.SetExecutionAttemptStatus(a.AttemptID, "needs_human")
				} else {
					_, _ = s.st.FinishExecutionAttempt(a.AttemptID, "failed", "pending", errJSON("lease expired, attempts exhausted"))
				}
			default:
				// 结果未知（lost）：由 scheduleOnce 创建新 attempt 重试（不立即重试语义）
				_, _ = s.st.SetExecutionAttemptStatus(a.AttemptID, "lost")
			}
			changed = true
			s.hub.Publish("agent_attempt_expired", map[string]any{
				"attempt_id": a.AttemptID, "task_id": a.TaskID, "step_id": a.StepID,
			})
			s.mu.Unlock()
		}
		if changed {
			s.recomputeTaskStatuses()
			s.Notify()
		}
	}
}

func errJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// ---------- 提交 / 批准 ----------

// SubmitDraft 提交 PlanDraft：确定性校验 → 修订落库（draft，待人工批准）
func (s *AgentScheduler) SubmitDraft(d *plan.Draft, source string) (store.PlanRevision, error) {
	if d == nil {
		return store.PlanRevision{}, fmt.Errorf("draft required")
	}
	catalog, err := s.catalog()
	if err != nil {
		return store.PlanRevision{}, err
	}
	if err := plan.Validate(d, catalog); err != nil {
		return store.PlanRevision{}, fmt.Errorf("%w: %v", ErrBadPlan, err)
	}
	draftJSON, err := plan.MarshalDraft(d)
	if err != nil {
		return store.PlanRevision{}, fmt.Errorf("%w: %v", ErrBadPlan, err)
	}
	taskID := "ag-" + store.NewID()
	if err := s.st.InsertAgentTask(store.AgentTask{TaskID: taskID, Title: d.Goal, Goal: d.Goal}); err != nil {
		return store.PlanRevision{}, err
	}
	rev, err := s.st.InsertPlanRevision(store.PlanRevision{
		TaskID: taskID, Source: source, DraftJSON: draftJSON, CatalogDigest: catalog.Digest(),
	})
	if err != nil {
		return store.PlanRevision{}, err
	}
	s.hub.Publish("agent_plan_submitted", map[string]any{
		"task_id": taskID, "revision": rev.Revision, "steps": len(d.Steps),
	})
	return rev, nil
}

// Approve 人工批准：重新校验（目录可能已变化）→ 冻结分派 + input_digest（设计 §5.3）
func (s *AgentScheduler) Approve(taskID string, revision int, approvedBy string) ([]store.TaskAssignment, error) {
	if approvedBy == "" {
		return nil, fmt.Errorf("approved_by required")
	}
	p, err := s.st.GetPlanRevision(taskID, revision)
	if err != nil {
		return nil, err
	}
	if p.Status != "draft" {
		return nil, fmt.Errorf("plan revision %s/%d not in draft state", taskID, revision)
	}
	d, err := plan.DecodeDraft([]byte(p.DraftJSON))
	if err != nil {
		return nil, err
	}
	catalog, err := s.catalog()
	if err != nil {
		return nil, err
	}
	if err := plan.Validate(d, catalog); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadPlan, err)
	}
	assignments := make([]store.TaskAssignment, 0, len(d.Steps))
	for _, step := range d.Steps {
		action, err := s.lookupAction(catalog, step.AgentID, step.ActionKey)
		if err != nil {
			return nil, err
		}
		environment := d.TargetEnvironment
		digest := plan.ComputeInputDigest(revision, step.StepKey, action.ActionID, environment,
			step.Instruction, step.InputRefs)
		assignments = append(assignments, store.TaskAssignment{
			TaskID: taskID, StepID: step.StepKey, PlanRevision: revision,
			AgentID: step.AgentID, ActionID: action.ActionID,
			Environment: environment, InputDigest: digest,
		})
	}
	if err := s.st.ApprovePlanRevision(p, approvedBy, assignments); err != nil {
		return nil, err
	}
	patch, _ := json.Marshal(map[string]any{"task_id": taskID, "revision": revision, "steps": len(assignments)})
	_ = s.st.InsertIdentityAudit("", taskID, approvedBy, "approve_plan", string(patch))
	s.hub.Publish("agent_plan_approved", map[string]any{
		"task_id": taskID, "revision": revision, "steps": len(assignments),
	})
	s.Notify()
	return assignments, nil
}

// CancelAgentTask 取消任务：未终态尝试全部取消 + 通知 Connector 执行中取消（设计 §4.5）
func (s *AgentScheduler) CancelAgentTask(taskID string) (int, error) {
	task, err := s.st.GetAgentTask(taskID)
	if err != nil {
		return 0, err
	}
	if task.Status == "completed" || task.Status == "cancelled" {
		return 0, fmt.Errorf("task %s already %s", taskID, task.Status)
	}
	n, err := s.st.CancelAttemptsForTask(taskID)
	if err != nil {
		return 0, err
	}
	if err := s.st.SetAgentTaskStatus(taskID, "cancelled"); err != nil {
		return 0, err
	}
	// 通知各 Agent 的 Connector（尽力而为；离线 Connector 重连后按塔侧终态对账）
	rev := s.latestApprovedRevision(taskID)
	if rev > 0 {
		if attempts, err := s.st.ListAttemptsByTask(taskID); err == nil {
			for _, a := range attempts {
				if inst, err := s.st.GetAgentInstance(a.AgentID); err == nil {
					_ = s.gw.DispatchTo(inst.ConnectorID, agentmsg.Envelope{
						MessageType: agentmsg.MsgCancel, TaskID: taskID,
						StepID: a.StepID, AttemptID: a.AttemptID, PlanRevision: strconv.Itoa(rev),
					})
				}
			}
		}
	}
	s.hub.Publish("agent_task_cancelled", map[string]any{"task_id": taskID, "attempts": n})
	return n, nil
}

// ---------- 调度 ----------

func (s *AgentScheduler) scheduleOnce(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.st.ListAgentTasks("running")
	if err != nil {
		return
	}
	for _, task := range tasks {
		s.scheduleTask(ctx, task)
	}
}

// scheduleTask 单任务推进：解析最新批准修订 → 依赖满足的步骤创建 attempt 并定向下发
func (s *AgentScheduler) scheduleTask(ctx context.Context, task store.AgentTask) {
	rev := s.latestApprovedRevision(task.TaskID)
	if rev == 0 {
		return
	}
	p, err := s.st.GetPlanRevision(task.TaskID, rev)
	if err != nil || p.Status != "approved" {
		return
	}
	d, err := plan.DecodeDraft([]byte(p.DraftJSON))
	if err != nil {
		return
	}
	attempts, err := s.st.ListAttemptsByTask(task.TaskID)
	if err != nil {
		return
	}
	latest := latestAttemptByStep(attempts)

	// 步骤状态图：passed 集合供依赖判断
	passed := map[string]bool{}
	for stepKey, a := range latest {
		if a != nil && a.AcceptanceStatus == acceptance.StatusPassed && a.Status == "completed" {
			passed[stepKey] = true
		}
	}
	steps := map[string]plan.Step{}
	for _, st := range d.Steps {
		steps[st.StepKey] = st
	}
	// needs_human 步骤直接阻断调度（转人工）
	for _, st := range d.Steps {
		if a := latest[st.StepKey]; a != nil && a.Status == "needs_human" {
			_ = s.st.SetAgentTaskStatus(task.TaskID, "needs_human")
			return
		}
	}

	for _, st := range d.Steps {
		a := latest[st.StepKey]
		if a != nil {
			// 仅 lost（结果未知）允许重试；进行中/真实失败/取消/转人工均为终态不重试
			if a.Status != "lost" {
				continue
			}
		}
		// 依赖未全部验收通过 → 不调度（设计 §6.3.4：上游失败下游保持 pending）
		if !plan.DepsReady(st, passed) {
			continue
		}
		// 找冻结分派
		assign, err := s.st.GetTaskAssignment(task.TaskID, st.StepKey, rev)
		if err != nil {
			continue
		}
		// Agent 必须 verified；离线等待不改派（设计 §5.2/§8）
		inst, err := s.st.GetAgentInstance(assign.AgentID)
		if err != nil || inst.IdentityStatus != "verified" {
			continue
		}
		// action 必须 enabled（禁用立即阻止尚未开始的分派，设计 §4.5）
		action, err := s.st.GetAgentAction(assign.ActionID)
		if err != nil || !action.Enabled {
			continue
		}
		if !s.gw.Connected(inst.ConnectorID) {
			continue // pending，等待 Agent 上线
		}
		if err := s.dispatchStep(ctx, task.TaskID, rev, st, assign, inst, action); err != nil {
			log.Printf(`{"level":"warn","module":"agent_sched","msg":"dispatch %s/%s: %v"}`, task.TaskID, st.StepKey, err)
		}
	}
}

// dispatchStep 创建 attempt + 定向下发（新 attempt_id；idempotency_key = SHA256(task|step|revision)）
func (s *AgentScheduler) dispatchStep(ctx context.Context, taskID string, rev int, st plan.Step,
	assign store.TaskAssignment, inst store.AgentInstance, action store.AgentAction) error {
	attemptNo, err := s.st.NextAttemptNo(taskID, st.StepKey, rev)
	if err != nil {
		return err
	}
	lease := s.nowFunc().Add(s.cfg.LeaseTTL)
	attempt, err := s.st.InsertExecutionAttempt(store.ExecutionAttempt{
		AttemptID: "att-" + store.NewID(),
		TaskID: taskID, StepID: st.StepKey, PlanRevision: rev, AttemptNo: attemptNo,
		IdempotencyKey: plan.ComputeIdempotencyKey(taskID, st.StepKey, rev),
		AgentID:        assign.AgentID, ActionID: assign.ActionID,
		Environment: assign.Environment, InputDigest: assign.InputDigest,
		Status: "dispatched", LeaseExpiresAt: store.FormatLease(lease),
	})
	if err != nil {
		return err
	}
	// 上游制品注入（设计 §6.3.2）
	inputRefs := collectUpstreamArtifacts(s.st, taskID, rev, st)
	payload, _ := json.Marshal(map[string]any{"instruction": st.Instruction})
	env := agentmsg.Envelope{
		MessageType:    agentmsg.MsgDispatch,
		ConnectorID:    inst.ConnectorID,
		AgentID:        inst.ID,
		LocalAgentKey:  inst.LocalAgentKey,
		TaskID:         taskID,
		StepID:         st.StepKey,
		PlanRevision:   strconv.Itoa(rev),
		ActionID:       action.ID,
		ActionKey:      action.ActionKey,
		AttemptID:      attempt.AttemptID,
		IdempotencyKey: attempt.IdempotencyKey,
		Environment:    assign.Environment,
		InputDigest:    assign.InputDigest,
		LeaseExpiresAt: store.FormatLease(lease),
		Timeout:        stepTimeout(st),
		InputRefs:      inputRefs,
		Payload:        payload,
	}
	if err := s.gw.DispatchTo(inst.ConnectorID, env); err != nil {
		// 下发失败（罕见：Connected 后断开）——结果未知标记 lost，重试由下一轮调度创建
		_, _ = s.st.SetExecutionAttemptStatus(attempt.AttemptID, "lost")
		s.hub.Publish("agent_dispatch_failed", map[string]any{
			"task_id": taskID, "step_id": st.StepKey, "attempt_id": attempt.AttemptID,
		})
		return err
	}
	s.hub.Publish("agent_step_dispatched", map[string]any{
		"task_id": taskID, "step_id": st.StepKey, "attempt_id": attempt.AttemptID,
		"agent_id": inst.ID, "connector_id": inst.ConnectorID, "attempt_no": attemptNo,
	})
	return nil
}

func stepTimeout(st plan.Step) string {
	ms := st.TimeoutMS
	if ms <= 0 {
		ms = plan.DefaultStepTimeo
	}
	return fmt.Sprintf("%dms", ms)
}

// ---------- 结果核销 ----------

// HandleAgentResult 结构化结果核销（设计 §6.2/§8）：
// 校验归属与 digest → 自检失败关闭 → 验收验证器 → 制品落库 → 条件核销 → 推进下游
func (s *AgentScheduler) HandleAgentResult(env agentmsg.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if env.MessageType == agentmsg.MsgReconcileResult {
		s.handleReconcileLocked(env)
		return
	}
	s.handleResultLocked(env)
}

// handleResultLocked 处理 result 信封
func (s *AgentScheduler) handleResultLocked(env agentmsg.Envelope) {
	attempt, err := s.st.GetExecutionAttempt(env.AttemptID)
	if err != nil {
		log.Printf(`{"level":"warn","module":"agent_sched","msg":"unknown attempt %s"}`, env.AttemptID)
		return
	}
	// 归属校验：attempt 与信封的 task/revision 一致（Connector 只核销自己的结果）
	if attempt.TaskID != env.TaskID || attempt.StepID != env.StepID ||
		strconv.Itoa(attempt.PlanRevision) != env.PlanRevision || attempt.AgentID != env.AgentID {
		log.Printf(`{"level":"warn","module":"agent_sched","msg":"result identity mismatch for %s"}`, env.AttemptID)
		return
	}
	var payload agentmsg.ResultPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		payload = agentmsg.ResultPayload{Status: "failed", Error: "unparseable result payload"}
	}
	// input_digest 校验（设计 §6.1）
	if env.InputDigest != "" && env.InputDigest != attempt.InputDigest {
		payload = agentmsg.ResultPayload{Status: "failed", Error: "input_digest mismatch in result"}
	}

	// 结构化结果 JSON（供自检/验收）
	resultJSON, _ := json.Marshal(payload)

	// 执行状态映射：completed → attempt completed；rejected/failed → attempt failed
	execStatus := "failed"
	switch payload.Status {
	case "completed":
		execStatus = "completed"
	case "rejected":
		execStatus = "needs_human" // Connector 拒绝执行（绑定/校验失败）转人工
	}

	// 验收评估（执行状态与验收分离，设计 §6.2）
	acceptanceStatus := acceptance.StatusPending
	reason := ""
	if execStatus == "completed" {
		// 1) 自检失败关闭
		sc := quality.SelfCheckFromResultStrict(string(resultJSON))
		if sc.Status != "passed" {
			acceptanceStatus = acceptance.StatusFailed
			reason = "self_check: " + sc.Status
		} else {
			// 2) 验收验证器（标准来自批准草案）
			if criteria, err := s.stepCriteria(attempt.TaskID, attempt.PlanRevision, attempt.StepID); err == nil {
				res := acceptance.Evaluate(criteria, acceptance.ResultView{
					Payload:   resultJSON,
					SelfCheck: payload.SelfCheck,
					Evidence:  payload.AcceptanceEvidence,
				})
				acceptanceStatus = res.Status
				reason = res.Reason
			} else {
				acceptanceStatus = acceptance.StatusFailed
				reason = "criteria unavailable: " + err.Error()
			}
		}
		// 3) 制品引用校验 + 落库（本地路径拒绝，设计 §7.3）
		if verr := s.recordArtifacts(attempt, payload); verr != nil {
			acceptanceStatus = acceptance.StatusFailed
			reason = "artifact: " + verr.Error()
		}
	}
	if reason != "" {
		if merged, err := mergeReason(resultJSON, reason); err == nil {
			resultJSON = merged
		}
	}

	// 条件核销：终态（cancelled 等）不被迟到结果覆盖（设计 §8）
	changed, err := s.st.FinishExecutionAttempt(attempt.AttemptID, execStatus, acceptanceStatus, string(resultJSON))
	if err != nil {
		log.Printf(`{"level":"error","module":"agent_sched","msg":"finish attempt: %v"}`, err)
		return
	}
	if !changed {
		log.Printf(`{"level":"info","module":"agent_sched","msg":"late result ignored for %s (terminal)"}`, attempt.AttemptID)
		return
	}
	s.hub.Publish("agent_step_result", map[string]any{
		"task_id": attempt.TaskID, "step_id": attempt.StepID,
		"attempt_id": attempt.AttemptID, "status": execStatus, "acceptance": acceptanceStatus,
	})
	s.recomputeTaskStatusesLocked()
	s.Notify()
}

// handleReconcileLocked 重连对账回执（设计 §8）
func (s *AgentScheduler) handleReconcileLocked(env agentmsg.Envelope) {
	var outs []agentmsg.ReconcileOutcome
	if json.Unmarshal(env.Payload, &outs) != nil {
		return
	}
	for _, o := range outs {
		attempt, err := s.st.GetExecutionAttempt(o.AttemptID)
		if err != nil {
			continue
		}
		switch o.Status {
		case "running":
			_, _ = s.st.SetExecutionAttemptStatus(attempt.AttemptID, "running")
			lease := s.nowFunc().Add(s.cfg.LeaseTTL)
			_ = s.st.SetExecutionAttemptLease(attempt.AttemptID, lease)
		case "finished":
			if o.Result != nil {
				pb, _ := json.Marshal(o.Result)
				synth := agentmsg.Envelope{
					MessageType: agentmsg.MsgResult, ConnectorID: env.ConnectorID,
					TaskID: attempt.TaskID, StepID: attempt.StepID,
					PlanRevision: strconv.Itoa(attempt.PlanRevision),
					AgentID:      attempt.AgentID, AttemptID: attempt.AttemptID,
					InputDigest: attempt.InputDigest, Payload: pb,
				}
				s.handleResultLocked(synth)
			}
		default: // unknown：状态不明，不立即重试（看门狗按租约处理）
			_, _ = s.st.SetExecutionAttemptStatus(attempt.AttemptID, "unknown")
		}
	}
	s.recomputeTaskStatusesLocked()
	s.Notify()
}

// recordArtifacts 制品引用校验 + 落库（execution_artifacts，设计 §3.6/§7）
func (s *AgentScheduler) recordArtifacts(attempt store.ExecutionAttempt, payload agentmsg.ResultPayload) error {
	for _, ref := range payload.ArtifactRefs {
		if isLocalRef(ref.URI) {
			return fmt.Errorf("local path %q rejected as artifact ref", ref.URI)
		}
		if err := validateSharedRef(ref); err != nil {
			return err
		}
		if err := s.st.InsertArtifact(attempt.TaskID, attempt.StepID, attempt.PlanRevision, store.StoredArtifact{
			ID: store.NewID(), AttemptID: attempt.AttemptID,
			URI: ref.URI, Digest: ref.Digest, Kind: ref.Kind, SizeBytes: ref.SizeBytes,
		}); err != nil {
			return err
		}
	}
	return nil
}

// isLocalRef 本地路径判定（塔侧副本，避免依赖孵化器内部包）
func isLocalRef(uri string) bool {
	u := strings.ToLower(strings.TrimSpace(uri))
	if u == "" || strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, "\\") {
		return true
	}
	if len(u) >= 2 && u[1] == ':' && u[0] >= 'a' && u[0] <= 'z' {
		return true
	}
	_, _, ok := strings.Cut(u, "://")
	return !ok
}

// validateSharedRef 共享引用校验（s3/oci/https + sha256 digest 格式）
func validateSharedRef(ref agentmsg.ArtifactRef) error {
	scheme, _, ok := strings.Cut(strings.ToLower(ref.URI), "://")
	if !ok {
		return fmt.Errorf("artifact uri missing scheme")
	}
	switch scheme {
	case "s3", "oci", "https":
	case "http":
		// 塔侧拒绝非回环 http 引用
		host := ref.URI
		if i := strings.Index(ref.URI, "://"); i >= 0 {
			host = ref.URI[i+3:]
		}
		if j := strings.IndexAny(host, "/:"); j >= 0 {
			host = host[:j]
		}
		if host != "localhost" && host != "127.0.0.1" {
			return fmt.Errorf("artifact scheme %q not allowed", scheme)
		}
	default:
		return fmt.Errorf("artifact scheme %q not allowed", scheme)
	}
	if len(ref.Digest) != len("sha256:")+64 || !strings.HasPrefix(strings.ToLower(ref.Digest), "sha256:") {
		return fmt.Errorf("artifact digest %q invalid", ref.Digest)
	}
	if strings.TrimSpace(ref.Kind) == "" {
		return fmt.Errorf("artifact kind required")
	}
	return nil
}

// ---------- 内部辅助 ----------

func (s *AgentScheduler) catalog() (agentdir.Catalog, error) {
	return agentdir.NewService(s.st).Catalog()
}

func (s *AgentScheduler) lookupAction(cat agentdir.Catalog, agentID, actionKey string) (agentdir.CatalogAction, error) {
	for _, a := range cat.Agents {
		if a.AgentID == agentID {
			for _, act := range a.Actions {
				if act.ActionKey == actionKey {
					return act, nil
				}
			}
		}
	}
	return agentdir.CatalogAction{}, fmt.Errorf("action %q on agent %q not in catalog", actionKey, agentID)
}

func (s *AgentScheduler) stepCriteria(taskID string, revision int, stepID string) ([]plan.Criterion, error) {
	p, err := s.st.GetPlanRevision(taskID, revision)
	if err != nil {
		return nil, err
	}
	d, err := plan.DecodeDraft([]byte(p.DraftJSON))
	if err != nil {
		return nil, err
	}
	for _, st := range d.Steps {
		if st.StepKey == stepID {
			return st.AcceptanceCriteria, nil
		}
	}
	return nil, fmt.Errorf("step %q not in revision", stepID)
}

func (s *AgentScheduler) latestApprovedRevision(taskID string) int {
	revs, err := s.st.ListPlanRevisions(taskID)
	if err != nil {
		return 0
	}
	latest := 0
	for _, r := range revs {
		if r.Status == "approved" && r.Revision > latest {
			latest = r.Revision
		}
	}
	return latest
}

// recomputeTaskStatuses 任务级收口（加锁入口）
func (s *AgentScheduler) recomputeTaskStatuses() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeTaskStatusesLocked()
}

// recomputeTaskStatusesLocked 任务级收口（全部通过 → completed；有 needs_human → needs_human；
// 有 failed 终态且无法再推进 → failed）。调用方必须已持有 s.mu。
func (s *AgentScheduler) recomputeTaskStatusesLocked() {
	tasks, err := s.st.ListAgentTasks("running")
	if err != nil {
		return
	}
	for _, task := range tasks {
		rev := s.latestApprovedRevision(task.TaskID)
		if rev == 0 {
			continue
		}
		assignments, err := s.st.ListTaskAssignments(task.TaskID, rev)
		if err != nil || len(assignments) == 0 {
			continue
		}
		attempts, _ := s.st.ListAttemptsByTask(task.TaskID)
		latest := latestAttemptByStep(attempts)
		allPassed, anyNeedsHuman, anyFailed := true, false, false
		for _, as := range assignments {
			a := latest[as.StepID]
			if a == nil {
				allPassed = false
				continue
			}
			if a.AcceptanceStatus == acceptance.StatusPassed && a.Status == "completed" {
				continue
			}
			allPassed = false
			switch a.Status {
			case "needs_human":
				anyNeedsHuman = true
			case "failed":
				action, _ := s.st.GetAgentAction(a.ActionID)
				maxAttempts := s.cfg.MaxAttempts
				if action.MaxAttempts > 0 {
					maxAttempts = action.MaxAttempts
				}
				if a.AttemptNo >= maxAttempts || action.RetryPolicy == "non_retryable" {
					anyFailed = true
				}
			}
		}
		switch {
		case allPassed:
			_ = s.st.SetAgentTaskStatus(task.TaskID, "completed")
			s.hub.Publish("agent_task_completed", map[string]any{"task_id": task.TaskID})
		case anyNeedsHuman:
			_ = s.st.SetAgentTaskStatus(task.TaskID, "needs_human")
		case anyFailed:
			_ = s.st.SetAgentTaskStatus(task.TaskID, "failed")
			s.hub.Publish("agent_task_failed", map[string]any{"task_id": task.TaskID})
		}
	}
}

// latestAttemptByStep 步骤 → 最新 attempt（attempt_no 最大）
func latestAttemptByStep(attempts []store.ExecutionAttempt) map[string]*store.ExecutionAttempt {
	out := map[string]*store.ExecutionAttempt{}
	for i := range attempts {
		a := &attempts[i]
		if cur, ok := out[a.StepID]; !ok || a.AttemptNo > cur.AttemptNo {
			out[a.StepID] = a
		}
	}
	return out
}

func mergeReason(resultJSON json.RawMessage, reason string) (json.RawMessage, error) {
	var m map[string]any
	if json.Unmarshal(resultJSON, &m) != nil {
		return resultJSON, fmt.Errorf("not object")
	}
	m["acceptance_reason"] = reason
	return json.Marshal(m)
}
