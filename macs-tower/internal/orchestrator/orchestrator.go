// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package orchestrator 模块 #5/#7：任务编排 + DAG 调度器
// 目标 → 拆解/plan DAG → 依赖满足检测 → 6.4.7 评分选机（top-K 候选）→ 广播/定向下发（§6.4.8）
// → 收 execution_result → 追溯链/成本/质量管线 → E01 失败重试（指数退避）→ Worker TTL 看门狗（6.5）
// → 任务收口：抽查（E05）或直接归档
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"macs.local/tower/internal/archive"
	"macs.local/tower/internal/billing"
	"macs.local/tower/internal/dispatcher"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/quality"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/trace"
)

// ErrBadPlan 计划校验失败（HTTP 层据此返回 400）
var ErrBadPlan = errors.New("invalid plan")

// PlanStep 外部 Agent 接入的执行步骤；mode 全集见 Worker Run
type PlanStep struct {
	Mode      string            `json:"mode"`
	Title     string            `json:"title,omitempty"`
	Text      string            `json:"text,omitempty"`
	Review    bool              `json:"review,omitempty"` // 显式要求独立复核（Agent 模式默认免复核）
	// cli_agent
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	// http_agent
	URL       string            `json:"url,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	// openai_compatible / anthropic（6.4.9 协议驱动；凭据在孵化器侧注入，不下发）
	Model     string            `json:"model,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	System    string            `json:"system,omitempty"`
	MaxTokens int               `json:"max_tokens,omitempty"`

	TimeoutMs int64  `json:"timeout_ms,omitempty"`
	DependsOn []int  `json:"depends_on,omitempty"`
}

// Config 编排器配置（10.3 开关经 env 装配）
type Config struct {
	BroadcastEnabled bool          // udp.broadcast_mode.enabled，默认 true
	SubtaskTTL       time.Duration // Q3 Worker 默认 30 分钟
	MaxRetries       int           // E01 默认 3
	RetryBaseBackoff time.Duration // E01 指数退避基值
	RebalanceEnabled bool          // cluster.rebalance.enabled 默认 true
}

type Orchestrator struct {
	st      *store.Store
	disp    *dispatcher.Dispatcher
	hub     *events.Hub
	tr      *trace.Service
	arch    *archive.Service
	qual    *quality.Service
	bill    *billing.Service
	cfg     Config
	kick    chan struct{}
	mu      sync.Mutex // schedule 串行化（tick + kick 双入口）
	inBatch bool
	// 6.4.11 孵化器级预算：月度成本超限 → 停派 + budget_changed（状态翻转时通知一次）
	budgetSuspended sync.Map // incubator_id → bool
}

func New(st *store.Store, disp *dispatcher.Dispatcher, hub *events.Hub,
	tr *trace.Service, arch *archive.Service, qual *quality.Service, bill *billing.Service,
	cfg Config) *Orchestrator {
	return &Orchestrator{
		st: st, disp: disp, hub: hub, tr: tr, arch: arch, qual: qual, bill: bill,
		cfg: cfg, kick: make(chan struct{}, 1),
	}
}

func (o *Orchestrator) Start() {
	go o.loop()
	go o.watchdog()     // 6.5 Worker 超时清理 + 重试退避回池
	go o.rebalancer()   // 6.4.7 rebalanceTick
	go o.monthlyReset() // 6.5 预算月度重置
}

// Notify 唤醒调度循环（去抖：已有待处理信号则跳过）
func (o *Orchestrator) Notify() {
	select {
	case o.kick <- struct{}{}:
	default:
	}
}

func (o *Orchestrator) loop() {
	tick := time.NewTicker(2 * time.Second) // 周期兜底：重试到期 / 无事件推进
	defer tick.Stop()
	for {
		select {
		case <-o.kick:
			o.schedule()
		case <-tick.C:
			o.schedule()
		}
	}
}

// monthlyReset 6.5 预算月度重置：月滚动清零模板月度累计，
// 清空孵化器超额标记并广播解除（budget_changed exceeded=false）
func (o *Orchestrator) monthlyReset() {
	last := time.Now().UTC().Format("2006-01")
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		cur := time.Now().UTC().Format("2006-01")
		if cur == last {
			continue
		}
		last = cur
		n, err := o.st.ResetTemplateMonthlySpend()
		if err != nil {
			log.Printf(`{"level":"warn","module":"orchestrator","msg":"monthly reset: %v"}`, err)
			continue
		}
		o.budgetSuspended.Range(func(k, v any) bool {
			if v.(bool) {
				o.disp.NoticeBudgetChanged(k.(string), map[string]any{"exceeded": false, "month": cur})
			}
			o.budgetSuspended.Store(k, false)
			return true
		})
		o.hub.Publish("budget_monthly_reset", map[string]any{"month": cur, "templates_reset": n})
		log.Printf("[orch] budget monthly reset: month=%s templates=%d", cur, n)
	}
}

// checkIncubatorBudgets 6.4.11 孵化器级预算：当月成本超上界 → 停派 + budget_changed（状态翻转通知一次）
func (o *Orchestrator) checkIncubatorBudgets(incs []store.Incubator) {
	for i := range incs {
		inc := incs[i]
		lim, err := o.st.GetBudgetLimit("incubator", inc.ID)
		if err != nil || lim.AmountCents <= 0 {
			continue // 未设上界 = 不限
		}
		spent, err := o.st.IncubatorMonthlyCents(inc.ID)
		if err != nil {
			continue
		}
		exceeded := spent >= lim.AmountCents && lim.EnforceMode != "off"
		prevAny, had := o.budgetSuspended.Load(inc.ID)
		prev := had && prevAny.(bool)
		if exceeded == prev {
			continue
		}
		o.budgetSuspended.Store(inc.ID, exceeded)
		o.disp.NoticeBudgetChanged(inc.ID, map[string]any{
			"limit_version": lim.LimitVersion, "amount_cents": lim.AmountCents,
			"spent_cents": spent, "exceeded": exceeded,
		})
		o.hub.Publish("budget_exceeded", map[string]any{
			"incubator_id": inc.ID, "spent_cents": spent, "limit_cents": lim.AmountCents, "exceeded": exceeded,
		})
	}
}

// budgetSuspendedFor 孵化器是否因预算暂停承接（选机硬过滤）
func (o *Orchestrator) budgetSuspendedFor(id string) bool {
	v, ok := o.budgetSuspended.Load(id)
	return ok && v.(bool)
}

// watchdog 每分钟：Worker TTL 超时清理（E02）+ 重试到期回池（E01）
func (o *Orchestrator) watchdog() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		// E01：退避到期的失败子任务重新入池
		if n, err := o.st.RequeueRetryable(); err == nil && n > 0 {
			log.Printf("[orch] requeued %d retryable subtasks", n)
		}
		// E02：dispatched/running 超过 TTL → failed（timeout）→ 触发重试
		stale, err := o.st.ListStaleRunning(time.Now().UTC().Add(-o.cfg.SubtaskTTL).Format(time.RFC3339))
		if err != nil {
			continue
		}
		for _, sub := range stale {
			log.Printf("[orch] watchdog ttl timeout subtask=%s", sub.ID)
			o.failSubtask(sub, "worker ttl timeout ("+o.cfg.SubtaskTTL.String()+")")
		}
	}
}

// rebalancer 每 30s（6.4.7 rebalanceTick）：过载孵化器（load>0.85）标记 overloaded；负载回落回 online
func (o *Orchestrator) rebalancer() {
	if !o.cfg.RebalanceEnabled {
		return
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		incs, err := o.st.ListIncubators(false)
		if err != nil {
			continue
		}
		for _, inc := range incs {
			switch {
			case inc.LastLoad > 0.85 && inc.Status == "online":
				o.st.SetIncubatorStatus(inc.ID, "overloaded")
				o.hub.Publish("cluster_event", map[string]any{
					"incubator_id": inc.ID, "event": "overloaded", "load": inc.LastLoad,
				})
			case inc.LastLoad < 0.7 && inc.Status == "overloaded":
				o.st.SetIncubatorStatus(inc.ID, "online")
			}
		}
	}
}

// Submit 内置拆解提交（等价于不带 plan 的 SubmitPlan）
func (o *Orchestrator) Submit(goal string) (store.Task, error) {
	return o.SubmitPlan(TaskRequest{Goal: goal})
}

// TaskRequest POST /api/v1/tasks 请求体（V3.0 扩展：agent_platform / budget / 验收标准 §2.2）
type TaskRequest struct {
	Goal          string     `json:"goal"`
	Title         string     `json:"title,omitempty"`
	Plan          []PlanStep `json:"plan,omitempty"`
	BudgetCents   float64    `json:"budget_cents,omitempty"`
	AgentPlatform string     `json:"agent_platform,omitempty"`
	Priority      string     `json:"priority,omitempty"`
	Acceptance    []string   `json:"acceptance_criteria,omitempty"` // 验收项（AI 拟稿 + 人工确认，§4.2）
	AcceptNote    string     `json:"acceptance_design_note,omitempty"`
	DeadlineAt    string     `json:"deadline_at,omitempty"`
}

// SubmitPlan 提交任务：plan 为空时走内置拆解；否则按 plan 建子任务 DAG
func (o *Orchestrator) SubmitPlan(req TaskRequest) (store.Task, error) {
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return store.Task{}, fmt.Errorf("goal required")
	}
	if len(req.Plan) > 0 {
		if err := validatePlan(req.Plan); err != nil {
			return store.Task{}, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		priority = "normal"
	}
	acceptance := "[]"
	if len(req.Acceptance) > 0 {
		if b, err := json.Marshal(req.Acceptance); err == nil {
			acceptance = string(b)
		}
	}
	task := store.Task{
		ID: store.NewID(), Goal: goal, Title: strings.TrimSpace(req.Title),
		Status: "pending", Progress: 0, Priority: priority,
		Acceptance: acceptance, AcceptNote: strings.TrimSpace(req.AcceptNote),
		DeadlineAt: strings.TrimSpace(req.DeadlineAt), BudgetCents: req.BudgetCents,
		CreatedAt: now, UpdatedAt: now,
	}
	var subs []store.Subtask
	if len(req.Plan) > 0 {
		subs = buildPlanSubtasks(task.ID, goal, req.Plan)
	} else {
		subs = decompose(task.ID, goal)
	}
	if err := o.st.InsertTaskWithSubtasks(task, subs); err != nil {
		return store.Task{}, err
	}
	// 追溯链根节点：task → goal
	_ = o.tr.Record(task.ID, "", trace.ChainFingerprint(goal, ""), goal, "goal", int64(len(goal)))
	o.hub.Publish("task_created", task)
	o.Notify()
	return task, nil
}

// Result 孵化器 execution_result 载荷（V3.0 遥测扩展 + 自检报告内嵌）
type Result struct {
	SubtaskID   string `json:"subtask_id"`
	IncubatorID string `json:"incubator_id"`
	WorkerID    string `json:"worker_id"`
	Status      string `json:"status"`
	Result      string `json:"result"`
	Error       string `json:"error"`
	DurationMs  int64  `json:"duration_ms"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	Fingerprint string `json:"fingerprint"`
	// §6.4.13.2 遥测扩展
	Telemetry *udpTelemetry `json:"telemetry_metrics,omitempty"`
}

type udpTelemetry struct {
	TokenUsage *struct {
		Prompt     int `json:"prompt"`
		Completion int `json:"completion"`
		Cached     int `json:"cached"`
	} `json:"token_usage,omitempty"`
	CostCents    float64 `json:"cost_cents"`
	CurrentStep  string  `json:"current_step"`
	PlatformAdapter string `json:"platform_adapter"`
	AgentPlatform   string `json:"agent_platform"`
}

// HandleResult 处理孵化器上行 execution_result（网关调用；幂等：终态重复回传直接吸收）
func (o *Orchestrator) HandleResult(r Result) {
	sub, err := o.st.GetSubtask(r.SubtaskID)
	if err != nil {
		return
	}

	// ── 复核子任务回传 → E04 回路（幂等：非在途回传一律吸收，防广播双跑/重放重复归档）──
	if sub.Kind == "review" {
		if sub.Status != "pending" && sub.Status != "dispatched" && sub.Status != "running" {
			return // E07：终态复核重复回传不重复生效
		}
		verdict, note := parseReviewVerdict(r, sub)
		status := "completed"
		if verdict != "approved" {
			status = "failed"
		}
		_ = o.st.FinishSubtask(sub.ID, status, r.Result, r.Error, r.WorkerID, r.StartedAt, r.Fingerprint)
		o.qual.OnReviewResult(sub, verdict, note)
		o.Notify()
		return
	}

	// ── 普通执行结果 ──
	// 幂等（E07）：仅受理在途子任务的回传；终态/completed-after-review/补传重放直接吸收，
	// 否则重放会重复落 cost_records / telemetry_events / 追溯链副作用
	if sub.Status != "dispatched" && sub.Status != "running" {
		return
	}

	status := "completed"
	if r.Status != "completed" {
		status = "failed"
	}

	// 追溯链指纹（6.4.4）：塔侧权威重算（链父 = 任务根，与 E06 对账口径一致）
	fp := ""
	if status == "completed" {
		fp = o.tr.ComputeFor(sub.TaskID, r.Result)
	}
	_ = o.st.FinishSubtask(sub.ID, status, r.Result, r.Error, r.WorkerID, r.StartedAt, fp)
	if status == "completed" {
		summary := fmt.Sprintf("%.80s", r.Result)
		_ = o.tr.Record(sub.TaskID, sub.ID, fp, summary, "result", int64(len(r.Result)))
	}

	// 成本与遥测（FR-21 / §6.4.13.4）
	o.recordCost(sub, r)

	if status == "failed" {
		o.hub.Publish("subtask_failed", map[string]any{
			"task_id": sub.TaskID, "subtask_id": sub.ID, "error": r.Error,
		})
		o.Notify()
		return
	}

	// ── 质量管线（M3）：自检门 → 复核孵化（review_pending 时不推进任务收口）──
	sub.Status = status // 终态已落库，质量管线按最新状态判定
	done := o.qual.OnExecutionResult(sub)
	doneSub, _ := o.st.GetSubtask(sub.ID)
	task, err := o.st.RecomputeTask(sub.TaskID)
	if err == nil {
		o.hub.Publish("subtask_finished", map[string]any{
			"task_id": sub.TaskID, "subtask": doneSub, "task_status": task.Status,
			"progress": task.Progress, "quality": doneSub.QualityStatus,
		})
	}
	if done {
		o.maybeFinalize(task)
	}
	o.Notify()
}

// parseReviewVerdict 解析复核输出 → approved/rejected
func parseReviewVerdict(r Result, review store.Subtask) (string, string) {
	var p struct {
		Review struct {
			Status           string `json:"status"`
			FingerprintMatch bool   `json:"fingerprint_match"`
			Note             string `json:"note"`
		} `json:"review"`
	}
	if json.Unmarshal([]byte(r.Result), &p) == nil && p.Review.Status != "" {
		if p.Review.Status == "approved" {
			return "approved", p.Review.Note
		}
		return "rejected", p.Review.Note
	}
	if r.Status == "completed" {
		return "approved", ""
	}
	return "rejected", r.Error
}

// recordCost execution_result 遥测 → cost_records + telemetry_events
func (o *Orchestrator) recordCost(sub store.Subtask, r Result) {
	ev := store.TelemetryEvent{
		TaskID: sub.TaskID, SubtaskID: sub.ID, WorkerID: r.WorkerID,
		EventType: map[bool]string{true: "completed", false: "failed"}[r.Status == "completed"],
	}
	if r.Telemetry != nil {
		ev.PlatformAdapter = r.Telemetry.PlatformAdapter
		ev.AgentPlatform = r.Telemetry.AgentPlatform
		ev.Payload = mustJSON(map[string]any{
			"token_usage": r.Telemetry.TokenUsage, "cost_cents": r.Telemetry.CostCents,
			"duration_ms": r.DurationMs, "current_step": r.Telemetry.CurrentStep,
		})
	} else {
		ev.Payload = mustJSON(map[string]any{"duration_ms": r.DurationMs})
	}
	_ = o.st.InsertTelemetryEvent(ev)

	if r.Telemetry == nil {
		return
	}
	usage := ""
	if r.Telemetry.TokenUsage != nil {
		usage = mustJSON(r.Telemetry.TokenUsage)
	}
	_ = o.bill.Record(store.CostRecord{
		ID: store.NewID(), IncubatorID: r.IncubatorID, TaskID: sub.TaskID, SubtaskID: sub.ID,
		WorkerID: r.WorkerID, CostType: "api", Provider: r.Telemetry.AgentPlatform,
		Model: "", AmountCents: r.Telemetry.CostCents, TokenUsage: usage,
		DurationSec: int(r.DurationMs / 1000), Description: "execution telemetry",
	})
}

// maybeFinalize 任务收口：全部子任务完成 → 抽查立案（E05）或直接归档
func (o *Orchestrator) maybeFinalize(task store.Task) {
	if task.Status != "completed" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	cur, _ := o.st.GetTask(task.ID)
	if cur.Status != "completed" {
		return
	}
	if o.qual.ShouldSample() {
		if err := o.qual.RaiseSpotCheck(task.ID); err == nil {
			_ = o.st.SetTaskStatus(task.ID, "in_spot_check")
			o.hub.Publish("task_in_spot_check", map[string]any{"task_id": task.ID})
			return // 抽查通过/失败由 quality 回调收口
		}
	}
	o.finalizeTask(task.ID, "completed")
}

// finalizeTask 终态落库 + 归档（FR-18）
func (o *Orchestrator) finalizeTask(taskID, status string) {
	_ = o.st.SetTaskStatus(taskID, status)
	task, err := o.st.GetTask(taskID)
	if err != nil {
		return
	}
	subs, _ := o.st.ListSubtasks(taskID)
	cost, _ := o.st.TaskCostCents(taskID)
	spotNote := ""
	if checks, _ := o.st.ListSpotChecks(1); len(checks) > 0 {
		spotNote = checks[0].Status
	}
	_ = o.arch.FinalSummary(task, subs, spotNote, cost)
	o.hub.Publish("task_finished", task)
}

// OnSpotCheckFinished 抽查收口回调（quality 注册）
func (o *Orchestrator) OnSpotCheckFinished(taskID, status string) {
	task, _ := o.st.GetTask(taskID)
	if task.Status != "in_spot_check" {
		return
	}
	switch status {
	case "passed":
		o.finalizeTask(taskID, "completed")
	case "failed":
		// E05 已升级 human_final 审批；任务保持冻结等待人工
		_ = o.st.SetTaskStatus(taskID, "blocked")
	}
}

// RecomputeAndFinalize 复核放行后的任务重算与收口（quality OnSubtaskFinal 回调）
func (o *Orchestrator) RecomputeAndFinalize(taskID string) {
	if task, err := o.st.RecomputeTask(taskID); err == nil {
		o.maybeFinalize(task)
	}
	o.Notify()
}

// OnHumanFinal 人工终审回调（E04 复核耗尽 / E05 抽查不通过升级）：approved 放行 / rejected 终止
func (o *Orchestrator) OnHumanFinal(taskID string, approved bool) {
	task, err := o.st.GetTask(taskID)
	if err != nil {
		return
	}
	if task.Status != "blocked" && task.Status != "in_spot_check" {
		return
	}
	if approved {
		o.finalizeTask(taskID, "completed")
		return
	}
	_ = o.st.SetTaskStatus(taskID, "failed")
	o.hub.Publish("task_finished", task)
}

// OnBudgetDecision E09 预算审批回调：approved → 解除 blocked 回到调度
func (o *Orchestrator) OnBudgetDecision(taskID string, approved bool) {
	if !approved {
		return
	}
	if task, err := o.st.GetTask(taskID); err == nil && task.Status == "blocked" {
		_ = o.st.SetTaskStatus(taskID, "running")
		o.Notify()
	}
}

// OnGuardrailDecision 护栏审批回调（6.4.6 frozen 路径）：approved → 解冻重排；rejected → failed
func (o *Orchestrator) OnGuardrailDecision(subtaskID string, approved bool) {
	sub, err := o.st.GetSubtask(subtaskID)
	if err != nil || sub.Status != "frozen" {
		return
	}
	if approved {
		_ = o.st.SetSubtaskRetry(subtaskID, 0, sub.MaxRetries, "") // 重置重试额度
		_ = o.st.SetSubtaskStatus(subtaskID, "pending", "")
		o.Notify()
		return
	}
	_ = o.st.SetSubtaskStatus(subtaskID, "failed", "")
	if task, e := o.st.RecomputeTask(sub.TaskID); e == nil {
		o.hub.Publish("subtask_finished", map[string]any{
			"task_id": sub.TaskID, "subtask_id": subtaskID, "status": "failed", "task_status": task.Status,
		})
	}
	o.Notify()
}

// CancelTask 取消任务（6.4.3 task_cancelled 语义）：未派发子任务置 cancelled，任务终态 cancelled
func (o *Orchestrator) CancelTask(taskID string) error {
	task, err := o.st.GetTask(taskID)
	if err != nil {
		return err
	}
	if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
		return fmt.Errorf("task %s already %s", taskID, task.Status)
	}
	subs, _ := o.st.ListSubtasks(taskID)
	for _, s := range subs {
		switch s.Status {
		case "pending", "ready", "frozen":
			_ = o.st.SetSubtaskStatus(s.ID, "cancelled", "")
		case "dispatched", "running", "review_pending", "reviewing", "in_spot_check":
			// 在途子任务按 fail-open 收尾（10.1），不强制中断
		}
	}
	_ = o.st.SetTaskStatus(taskID, "cancelled")
	o.hub.Publish("task_cancelled", map[string]any{"task_id": taskID})
	// 6.4.3 下行接线：task_cancelled 通知涉及孵化器；在跑子任务凭 worker_rollback 幂等回滚
	notif := map[string]any{"task_id": taskID, "goal": task.Goal}
	sent := map[string]bool{}
	for _, s := range subs {
		if (s.Status == "dispatched" || s.Status == "running") && s.IncubatorID != "" {
			o.disp.NotifyIncubator(s.IncubatorID, "worker_rollback", map[string]any{"subtask_id": s.ID})
		}
		if s.IncubatorID != "" && !sent[s.IncubatorID] {
			o.disp.NotifyIncubator(s.IncubatorID, "task_cancelled", notif)
			sent[s.IncubatorID] = true
		}
	}
	return nil
}

// ── 调度主循环（6.4.7 selectIncubator + 6.4.3 dispatchToIncubators）──

func (o *Orchestrator) schedule() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inBatch {
		return
	}
	o.inBatch = true
	defer func() { o.inBatch = false }()

	incs, err := o.st.ListIncubators(true)
	if err != nil {
		return
	}
	running, _ := o.st.CountRunning()

	o.checkIncubatorBudgets(incs)
	tasks, err := o.st.ListTasks(0)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == "completed" || t.Status == "failed" || t.Status == "cancelled" ||
			t.Status == "blocked" || t.Status == "in_spot_check" {
			continue
		}
		// 预算门（E09）：派发边界硬停，在跑 Worker 收尾不受影响
		if v, err := o.bill.CheckTaskBudget(t.ID); err == nil && v.Exceeded {
			_ = o.bill.ExceedE09(t.ID, v)
			continue
		}
		if v, err := o.bill.CheckGlobalBudget(); err == nil && v.Exceeded {
			_ = o.apprRaiseGlobalBudget(v)
			continue
		}

		subs, err := o.st.ListSubtasks(t.ID)
		if err != nil {
			continue
		}
		byID := make(map[string]store.Subtask, len(subs))
		for _, s := range subs {
			byID[s.ID] = s
		}
		for _, s := range subs {
			if s.Status != "pending" || !depsCompleted(s, byID) {
				continue
			}
			// 唤醒环护栏（6.4.10）：重试额度耗尽 → 冻结 + guardrail 审批
			if s.AttemptCount >= s.MaxRetries && s.MaxRetries > 0 {
				if err := o.qual.FreezeSubtask(s, fmt.Sprintf("重试 %d 次未通过（E01 耗尽）", s.AttemptCount)); err != nil {
					log.Printf("[orch] freeze failed subtask=%s: %v", s.ID, err)
				}
				continue
			}
			cands := o.selectIncubators(incs, running, s, t.ID)
			if len(cands) == 0 {
				return // 本轮无可用槽位，等下次 Notify
			}
			if err := o.st.ClaimSubtask(s.ID, cands[0].ID); err != nil {
				log.Printf("[orch] claim failed subtask=%s: %v", s.ID, err)
				continue
			}
			running[cands[0].ID]++
			o.hub.Publish("subtask_dispatched", map[string]any{
				"task_id": s.TaskID, "subtask_id": s.ID, "incubator_id": cands[0].ID,
			})
			go o.dispatch(cands, s)
		}
	}
}

// apprRaiseGlobalBudget 全局预算耗尽（少见路径，直接广播事件；审批按 task 维度挂）
func (o *Orchestrator) apprRaiseGlobalBudget(v billing.BudgetVerdict) error {
	o.hub.Publish("budget_global_exceeded", map[string]any{
		"spent_cents": v.Spent, "limit_cents": v.Limit,
	})
	return nil
}

// dispatch 下发：广播模式（>1 候选）first-ACK-wins；失败/改派 → 依次尝试下一候选
func (o *Orchestrator) dispatch(candidates []*store.Incubator, s store.Subtask) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	def := o.taskDef(s)
	var err error
	if o.cfg.BroadcastEnabled && len(candidates) > 1 {
		_, err = o.disp.DispatchBroadcast(ctx, candidates, s.ID, def)
	} else {
		for _, c := range candidates {
			_, err = o.disp.DispatchTo(ctx, c, s.ID, def)
			if err == nil || errors.Is(err, dispatcher.ErrDriverMismatch) {
				if err == nil {
					break
				}
				continue // driver_mismatch → 改派下一候选（§6.4.13.6）
			}
			break
		}
	}
	if err != nil {
		log.Printf("[orch] dispatch failed subtask=%s: %v", s.ID, err)
		sub, _ := o.st.GetSubtask(s.ID)
		o.failSubtask(sub, "dispatch: "+err.Error())
	}
}

// failSubtask E01 失败登记：退避到 next_retry_at；额度耗尽留给护栏冻结判定
func (o *Orchestrator) failSubtask(sub store.Subtask, reason string) {
	if sub.ID == "" {
		return
	}
	_ = o.st.FinishSubtask(sub.ID, "failed", sub.Result, reason, sub.WorkerID, sub.StartedAt, sub.Fingerprint)
	maxRetries := sub.MaxRetries
	if maxRetries <= 0 {
		maxRetries = o.cfg.MaxRetries
	}
	backoff := o.cfg.RetryBaseBackoff * time.Duration(math.Pow(2, float64(sub.AttemptCount)))
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	_ = o.st.SetSubtaskRetry(sub.ID, sub.AttemptCount, maxRetries, time.Now().UTC().Add(backoff).Format(time.RFC3339))
	if task, e := o.st.RecomputeTask(sub.TaskID); e == nil {
		o.hub.Publish("subtask_finished", map[string]any{
			"task_id": sub.TaskID, "subtask_id": sub.ID, "status": "failed",
			"task_status": task.Status, "error": reason,
		})
	}
	o.Notify()
}

// selectIncubators 6.4.7 评分选机（top-K 候选，默认 K=3）
//   score = 0.45*idle_score + 0.35*load_score + template_bonus(0) + spread_penalty
func (o *Orchestrator) selectIncubators(incs []store.Incubator, running map[string]int, s store.Subtask, taskID string) []*store.Incubator {
	driver := requiredDriverOf(s)
	type scored struct {
		inc   *store.Incubator
		score float64
	}
	var out []scored
	for i := range incs {
		inc := &incs[i]
		if inc.ID == s.IncubatorID && s.Status == "pending" && s.AttemptCount == 0 {
			// 首次派发无粘连；重试由评分自然分散
			_ = inc
		}
		slots := inc.Capacity - running[inc.ID]
		if slots <= 0 || inc.ListenPort <= 0 || inc.LastIP == "" {
			continue
		}
		// 硬过滤：协议驱动能力（§6.4.13.6）+ 断路器（6.4.10）+ 孵化器级预算（6.4.11）
		if driver != "" && !hasDriver(inc.Drivers, driver) {
			continue
		}
		if o.disp.CircuitOpen(inc.ID) {
			continue
		}
		if o.budgetSuspendedFor(inc.ID) {
			continue
		}
		idleScore := float64(slots) / float64(max(1, inc.Capacity))
		loadScore := 1.0 - clamp(inc.LastLoad, 0, 1)
		spread := -0.15 * float64(running[inc.ID]) // 同任务并行子任务分散（简化为全局在跑数）
		score := 0.45*idleScore + 0.35*loadScore + spread
		out = append(out, scored{inc: inc, score: score})
	}
	// 评分降序取前 K=3
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].score > out[i].score {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > 3 {
		out = out[:3]
	}
	cands := make([]*store.Incubator, 0, len(out))
	for _, c := range out {
		cands = append(cands, c.inc)
	}
	return cands
}

// requiredDriverOf 子任务 → 协议驱动要求（6.4.13.6）
func requiredDriverOf(s store.Subtask) string {
	if s.RequiredDriver != "" {
		return s.RequiredDriver
	}
	var p struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal([]byte(s.Payload), &p) != nil {
		return ""
	}
	switch p.Mode {
	case "openai_compatible":
		return "openai_compatible"
	case "anthropic":
		return "anthropic"
	}
	return ""
}

func hasDriver(driversJSON, driver string) bool {
	var list []string
	if json.Unmarshal([]byte(driversJSON), &list) != nil {
		return false
	}
	for _, d := range list {
		if strings.HasPrefix(d, driver) {
			return true
		}
	}
	return false
}

// taskDef 由子任务 payload 构造下发体；有依赖的子任务在分发时注入依赖结果
func (o *Orchestrator) taskDef(s store.Subtask) map[string]any {
	var def map[string]any
	if err := json.Unmarshal([]byte(s.Payload), &def); err != nil || def == nil {
		return map[string]any{"mode": "noop"}
	}
	if s.DependsOn == "" {
		return def
	}
	deps := make([]map[string]any, 0, 2)
	for _, id := range strings.Split(s.DependsOn, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		d, err := o.st.GetSubtask(id)
		if err != nil {
			continue
		}
		deps = append(deps, map[string]any{
			"subtask_id": d.ID, "title": d.Title, "status": d.Status, "result": d.Result,
		})
	}
	def["deps"] = deps
	return def
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func depsCompleted(s store.Subtask, byID map[string]store.Subtask) bool {
	if s.DependsOn == "" {
		return true
	}
	for _, id := range strings.Split(s.DependsOn, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if d, ok := byID[id]; !ok || d.Status != "completed" {
			return false
		}
	}
	return true
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// validatePlan 校验计划：模式可执行、CLI 有命令、HTTP 有合法 URL、依赖只指向更早步骤
func validatePlan(plan []PlanStep) error {
	if len(plan) > 20 {
		return fmt.Errorf("%w: too many steps (max 20)", ErrBadPlan)
	}
	for i := range plan {
		plan[i].Mode = strings.TrimSpace(plan[i].Mode)
		switch plan[i].Mode {
		case "cli_agent":
			if strings.TrimSpace(plan[i].Command) == "" {
				return fmt.Errorf("%w: plan[%d] command required", ErrBadPlan, i)
			}
		case "http_agent":
			u, err := url.Parse(strings.TrimSpace(plan[i].URL))
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("%w: plan[%d] valid http(s) url required", ErrBadPlan, i)
			}
		case "text_analysis", "merge", "openai_compatible", "anthropic":
			// 内置模式与协议驱动模式（6.4.9）：凭据由孵化器注入，不下发
		default:
			return fmt.Errorf("%w: plan[%d] unsupported mode %q", ErrBadPlan, i, plan[i].Mode)
		}
		for _, d := range plan[i].DependsOn {
			if d < 0 || d >= i {
				return fmt.Errorf("%w: plan[%d] depends_on must reference an earlier step", ErrBadPlan, i)
			}
		}
	}
	return nil
}

// buildPlanSubtasks 把 plan 落成子任务：按序建 ID，依赖用索引映射为真实子任务 ID
func buildPlanSubtasks(taskID, goal string, plan []PlanStep) []store.Subtask {
	now := time.Now().UTC().Format(time.RFC3339)
	ids := make([]string, len(plan))
	for i := range ids {
		ids[i] = store.NewID()
	}
	subs := make([]store.Subtask, 0, len(plan))
	for i, step := range plan {
		if strings.TrimSpace(step.Text) == "" {
			step.Text = goal // 未显式指定时，占位符 {{text}} 用任务目标
		}
		title := strings.TrimSpace(step.Title)
		if title == "" {
			title = fmt.Sprintf("步骤 %d · %s", i+1, modeLabel(step.Mode))
		}
		deps := make([]string, 0, len(step.DependsOn))
		for _, d := range step.DependsOn {
			deps = append(deps, ids[d])
		}
		payload, _ := json.Marshal(step)
		subs = append(subs, store.Subtask{
			ID: ids[i], TaskID: taskID, Seq: i + 1, Title: title,
			Payload: string(payload), DependsOn: strings.Join(deps, ","),
			RequiredDriver: driverForMode(step.Mode),
			Status:         "pending", CreatedAt: now, UpdatedAt: now,
		})
	}
	return subs
}

// driverForMode mode → 孵化器能力协商键（§6.4.13.6）
func driverForMode(mode string) string {
	switch mode {
	case "openai_compatible":
		return "openai_compatible"
	case "anthropic":
		return "anthropic"
	}
	return ""
}

func modeLabel(mode string) string {
	switch mode {
	case "cli_agent":
		return "终端 CLI Agent"
	case "http_agent":
		return "HTTP Agent"
	case "openai_compatible":
		return "OpenAI 协议驱动"
	case "anthropic":
		return "Anthropic 协议驱动"
	case "text_analysis":
		return "内置解析"
	case "merge":
		return "结果汇总"
	default:
		return mode
	}
}

// decompose 最小拆解：目标按字符对半切 → 两个并行解析子任务 → 一个依赖前两者的汇总子任务
// （接入 LLM 语义拆解后替换此处，DAG 结构与调度逻辑不变）
func decompose(taskID, goal string) []store.Subtask {
	runes := []rune(goal)
	mid := len(runes) / 2
	if mid == 0 {
		mid = len(runes)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	aID, bID, cID := store.NewID(), store.NewID(), store.NewID()
	payloadA, _ := json.Marshal(map[string]any{
		"mode": "text_analysis", "shard": "A", "text": string(runes[:mid]),
	})
	payloadB, _ := json.Marshal(map[string]any{
		"mode": "text_analysis", "shard": "B", "text": string(runes[mid:]),
	})
	payloadC, _ := json.Marshal(map[string]any{"mode": "merge"})

	return []store.Subtask{
		{
			ID: aID, TaskID: taskID, Seq: 1, Title: "子任务 A · 目标片段解析",
			Payload: string(payloadA), Status: "pending", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: bID, TaskID: taskID, Seq: 2, Title: "子任务 B · 目标片段解析",
			Payload: string(payloadB), Status: "pending", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: cID, TaskID: taskID, Seq: 3, Title: "子任务 C · 分段结果汇总",
			Payload: string(payloadC), DependsOn: aID + "," + bID, Status: "pending",
			CreatedAt: now, UpdatedAt: now,
		},
	}
}
