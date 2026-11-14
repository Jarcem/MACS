// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package billing 模块 #12：成本中心（FR-21，6.4.11 多级预算与塔侧上界）
// 预算层级：任务级（tasks.budget_cents）→ 全局上界（budget_limits scope=global）
// 80% 软告警（warn_at_pct）；100% 硬停在派发边界生效：在跑 Worker 允许收尾，不再孵化新 Worker
package billing

import (
	"encoding/json"
	"fmt"

	"macs.local/tower/internal/approvals"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
)

type Service struct {
	st   *store.Store
	hub  *events.Hub
	appr *approvals.Service
}

func New(st *store.Store, hub *events.Hub, appr *approvals.Service) *Service {
	return &Service{st: st, hub: hub, appr: appr}
}

// Record 子任务成本落账（execution_result 遥测回填：token_usage + cost_cents）
func (s *Service) Record(rec store.CostRecord) error {
	if rec.ID == "" {
		rec.ID = store.NewID()
	}
	if err := s.st.InsertCostRecord(rec); err != nil {
		return err
	}
	s.hub.Publish("cost_recorded", map[string]any{
		"task_id": rec.TaskID, "subtask_id": rec.SubtaskID, "amount_cents": rec.AmountCents, "model": rec.Model,
	})
	return nil
}

// BudgetVerdict 派发边界预算判定结果
type BudgetVerdict struct {
	Exceeded bool    // 100% 硬停（E09）
	Warned   bool    // 80% 软告警
	Spent    float64
	Limit    float64
}

// CheckTaskBudget 任务级预算门（在派发边界调用；0 = 不限）
func (s *Service) CheckTaskBudget(taskID string) (BudgetVerdict, error) {
	v := BudgetVerdict{}
	task, err := s.st.GetTask(taskID)
	if err != nil {
		return v, err
	}
	spent, err := s.st.TaskCostCents(taskID)
	if err != nil {
		return v, err
	}
	v.Spent, v.Limit = spent, task.BudgetCents
	if task.BudgetCents <= 0 {
		return v, nil
	}
	// enforce_mode=off → 只记账告警，不拦截（10.3 budget.limit_enforce）
	if lim, err := s.st.GetBudgetLimit("task", taskID); err == nil && lim.EnforceMode == "off" {
		v.Warned = spent >= task.BudgetCents*float64(lim.WarnAtPct)/100
		return v, nil
	}
	if spent >= task.BudgetCents {
		v.Exceeded = true
		return v, nil
	}
	v.Warned = spent >= task.BudgetCents*0.8
	if v.Warned {
		s.warn("task", taskID, spent, task.BudgetCents)
	}
	return v, nil
}

// CheckGlobalBudget 全局/企业上界（budget_limits scope=global，塔侧统一封顶）
func (s *Service) CheckGlobalBudget() (BudgetVerdict, error) {
	v := BudgetVerdict{}
	lim, err := s.st.GetBudgetLimit("global", "")
	if err != nil || lim.AmountCents <= 0 {
		return v, err // 0 = 不限
	}
	spent, err := s.st.GlobalCostCents()
	if err != nil {
		return v, err
	}
	v.Spent, v.Limit = spent, lim.AmountCents
	if lim.EnforceMode == "off" {
		return v, nil
	}
	if spent >= lim.AmountCents {
		v.Exceeded = true
		return v, nil
	}
	v.Warned = spent >= lim.AmountCents*float64(lim.WarnAtPct)/100
	if v.Warned {
		s.warn("global", "", spent, lim.AmountCents)
	}
	return v, nil
}

// warn 80% 软告警：事件 + webhook（只告警不拦截）
func (s *Service) warn(scope, scopeID string, spent, limit float64) {
	s.hub.Publish("budget_warn", map[string]any{
		"scope": scope, "scope_id": scopeID,
		"spent_cents": spent, "limit_cents": limit,
		"pct": int(spent * 100 / limit),
	})
	s.appr.NotifyWebhook("warn", "budget_80pct",
		fmt.Sprintf("%s %s 预算已达 %d%%（%.0f/%.0f 分）", scope, scopeID, int(spent*100/limit), spent, limit))
	_ = s.st.InsertTelemetryEvent(store.TelemetryEvent{
		TaskID:    scopeID,
		EventType: "budget_warn",
		Payload:   mustJSON(map[string]any{"scope": scope, "spent_cents": spent, "limit_cents": limit}),
	})
}

// ExceedE09 预算耗尽（E09）：暂停该任务全部未派发子任务 + budget_exceed 审批
// 已在跑 Worker 允许收尾（硬停在派发边界生效）
func (s *Service) ExceedE09(taskID string, v BudgetVerdict) error {
	_ = s.st.SetTaskStatus(taskID, "blocked")
	s.hub.Publish("budget_exceeded", map[string]any{
		"task_id": taskID, "spent_cents": v.Spent, "limit_cents": v.Limit,
	})
	_, err := s.appr.Raise(approvals.TypeBudgetExceed, taskID, "",
		"预算耗尽，任务暂停", fmt.Sprintf("已消耗 %.0f / 预算 %.0f 分；充值或调额后恢复", v.Spent, v.Limit),
		map[string]any{"spent_cents": v.Spent, "limit_cents": v.Limit}, 0)
	s.appr.NotifyWebhook("error", "budget_exceeded", fmt.Sprintf("task %s 预算耗尽", taskID))
	return err
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
