// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package orchestrator 模块 #5：任务编排（M2 垂直切片）
// 目标 → 拆解为并行子任务 → 按槽位选在线孵化器分发 → 收 execution_result → 汇总进度 → 推进后续子任务
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"macs.local/tower/internal/dispatcher"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
)

type Orchestrator struct {
	st   *store.Store
	disp *dispatcher.Dispatcher
	hub  *events.Hub
	kick chan struct{}
}

func New(st *store.Store, disp *dispatcher.Dispatcher, hub *events.Hub) *Orchestrator {
	return &Orchestrator{st: st, disp: disp, hub: hub, kick: make(chan struct{}, 1)}
}

func (o *Orchestrator) Start() { go o.loop() }

// Notify 唤醒调度循环（去抖：已有待处理信号则跳过）
func (o *Orchestrator) Notify() {
	select {
	case o.kick <- struct{}{}:
	default:
	}
}

func (o *Orchestrator) loop() {
	for range o.kick {
		o.schedule()
	}
}

// Submit 创建任务并立即尝试分发
func (o *Orchestrator) Submit(goal string) (store.Task, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return store.Task{}, fmt.Errorf("goal required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	task := store.Task{
		ID: store.NewID(), Goal: goal, Status: "pending",
		Progress: 0, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.st.InsertTaskWithSubtasks(task, decompose(task.ID, goal)); err != nil {
		return store.Task{}, err
	}
	o.hub.Publish("task_created", task)
	o.Notify()
	return task, nil
}

// HandleResult 处理孵器上行 execution_result（网关调用）
func (o *Orchestrator) HandleResult(r Result) {
	sub, err := o.st.GetSubtask(r.SubtaskID)
	if err != nil {
		return
	}
	status := "completed"
	if r.Status == "failed" {
		status = "failed"
	}
	_ = o.st.FinishSubtask(r.SubtaskID, status, r.Result, r.Error, r.WorkerID, r.StartedAt)

	done, _ := o.st.GetSubtask(r.SubtaskID)
	task, err := o.st.RecomputeTask(sub.TaskID)
	if err == nil {
		o.hub.Publish("subtask_finished", map[string]any{
			"task_id": sub.TaskID, "subtask": done, "task_status": task.Status, "progress": task.Progress,
		})
		if task.Status == "completed" || task.Status == "failed" {
			o.hub.Publish("task_finished", task)
		}
	}
	o.Notify()
}

// Result 孵化器 execution_result 载荷
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
}

// schedule 扫描所有未完成任务，把依赖已满足的 pending 子任务按空槽分发
func (o *Orchestrator) schedule() {
	incs, err := o.st.ListIncubators(true)
	if err != nil || len(incs) == 0 {
		return
	}
	running, _ := o.st.CountRunning()
	slots := make(map[string]int, len(incs))
	for _, i := range incs {
		slots[i.ID] = i.Capacity - running[i.ID]
	}

	tasks, err := o.st.ListTasks(0)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == "completed" || t.Status == "failed" {
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
			target := pick(incs, slots)
			if target == nil {
				return // 本轮无空槽，等下次 Notify
			}
			slots[target.ID]--
			if err := o.st.ClaimSubtask(s.ID, target.ID); err != nil {
				slots[target.ID]++
				log.Printf("[orch] claim failed subtask=%s: %v", s.ID, err)
				continue
			}
			log.Printf("[orch] claim subtask=%s -> incubator=%s (%s:%d)", s.ID, target.ID, target.LastIP, target.ListenPort)
			o.hub.Publish("subtask_dispatched", map[string]any{
				"task_id": s.TaskID, "subtask_id": s.ID, "incubator_id": target.ID,
			})
			go o.dispatch(target, s)
		}
	}
}

func (o *Orchestrator) dispatch(target *store.Incubator, s store.Subtask) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := o.disp.DispatchTo(ctx, target, s.ID, o.taskDef(s)); err != nil {
		log.Printf("[orch] dispatch failed subtask=%s: %v", s.ID, err)
		_ = o.st.FinishSubtask(s.ID, "failed", "", "dispatch: "+err.Error(), "", "")
		if task, e := o.st.RecomputeTask(s.TaskID); e == nil {
			o.hub.Publish("subtask_finished", map[string]any{
				"task_id": s.TaskID, "subtask_id": s.ID, "status": "failed", "task_status": task.Status,
			})
		}
		o.Notify()
	}
}

// taskDef 由子任务 payload 构造下发体；merge 子任务在分发时注入依赖子任务的结果
func (o *Orchestrator) taskDef(s store.Subtask) map[string]any {
	var def map[string]any
	if err := json.Unmarshal([]byte(s.Payload), &def); err != nil || def == nil {
		return map[string]any{"mode": "noop"}
	}
	if def["mode"] != "merge" {
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

// pick 选当前空槽最多的在线孵化器
func pick(incs []store.Incubator, slots map[string]int) *store.Incubator {
	var best *store.Incubator
	for i := range incs {
		if slots[incs[i].ID] <= 0 || incs[i].ListenPort <= 0 || incs[i].LastIP == "" {
			continue
		}
		if best == nil || slots[incs[i].ID] > slots[best.ID] {
			best = &incs[i]
		}
	}
	return best
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
