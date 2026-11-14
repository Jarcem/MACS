// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package approvals 模块 #9：审批中心（FR-19）
// 统一审批模型 + 多场景（human_final / budget_exceed / guardrail / danger_op）+ 超时过期
// 决策结果经 hub 广播（approval_result 语义），由各场景注册的回调驱动状态机（E04/E05/E09）
package approvals

import (
	"encoding/json"
	"log"
	"time"

	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
)

// 场景常量（§2.5 approval_type 全集 + V3.5 danger_op）
const (
	TypeAcceptanceConfirm = "acceptance_confirm"
	TypeStartExecution    = "start_execution"
	TypeHumanFinal        = "human_final"
	TypeSubtaskPlan       = "subtask_plan"
	TypeMergeResult       = "merge_result"
	TypeBudgetExceed      = "budget_exceed"
	TypeGuardrail         = "guardrail"
	TypeDangerOp          = "danger_op"
)

type Service struct {
	st        *store.Store
	hub       *events.Hub
	onApprove map[string]func(a store.Approval) // 场景 → 通过回调
	onReject  map[string]func(a store.Approval) // 场景 → 拒绝回调
	stop      chan struct{}
}

func New(st *store.Store, hub *events.Hub) *Service {
	return &Service{
		st:        st,
		hub:       hub,
		onApprove: map[string]func(store.Approval){},
		onReject:  map[string]func(store.Approval){},
		stop:      make(chan struct{}),
	}
}

// On 场景回调注册（状态机副作用唯一入口，避免循环依赖）
func (s *Service) On(approveType string, approved, rejected func(a store.Approval)) {
	s.onApprove[approveType] = approved
	s.onReject[approveType] = rejected
}

func (s *Service) Close() { close(s.stop) }

// Raise 创建审批（幂等：同实体同类型仅一条 pending）
func (s *Service) Raise(approveType, taskID, subtaskID, title, description string, payload any, ttl time.Duration) (store.Approval, error) {
	exists, err := s.st.PendingApprovalExists(approveType, taskID, subtaskID)
	if err != nil || exists {
		return store.Approval{}, err
	}
	a := store.Approval{
		ID:          store.NewID(),
		TaskID:      taskID,
		SubtaskID:   subtaskID,
		ApproveType: approveType,
		Requester:   "tower",
		Title:       title,
		Description: description,
		ExpiresAt:   "",
	}
	if ttl > 0 {
		a.ExpiresAt = time.Now().UTC().Add(ttl).Format(time.RFC3339)
	}
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			a.Payload = string(b)
		}
	}
	if err := s.st.InsertApproval(a); err != nil {
		return store.Approval{}, err
	}
	s.hub.Publish("approval_created", a)
	s.NotifyWebhook("warn", "approval_created", a.Title)
	return a, nil
}

// Decide 决策（幂等：仅 pending 可决策）；决策后触发场景回调
func (s *Service) Decide(id, decision, approver, note string) (store.Approval, bool, error) {
	a, err := s.st.GetApproval(id)
	if err != nil {
		return store.Approval{}, false, err
	}
	changed, err := s.st.DecideApproval(id, decision, approver, note)
	if err != nil || !changed {
		return a, false, err
	}
	a.Status, a.Approver, a.Decision, a.DecisionNote, a.DecidedAt = decision, approver, decision, note, time.Now().UTC().Format(time.RFC3339)
	s.hub.Publish("approval_decided", map[string]any{
		"id": a.ID, "approval_type": a.ApproveType, "status": decision, "task_id": a.TaskID, "subtask_id": a.SubtaskID,
	})
	switch decision {
	case "approved":
		if cb := s.onApprove[a.ApproveType]; cb != nil {
			cb(a)
		}
	case "rejected":
		if cb := s.onReject[a.ApproveType]; cb != nil {
			cb(a)
		}
	}
	return a, true, nil
}

// ExpireTick 每分钟扫描超时审批（FR-19 超时语义）；expired 触发拒绝回调
func (s *Service) StartExpireSweeper() {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				n, err := s.st.ExpireApprovals()
				if err != nil {
					log.Printf(`{"level":"warn","module":"approvals","msg":"expire sweep: %v"}`, err)
					continue
				}
				if n > 0 {
					pending, _ := s.st.ListApprovals("expired", 20)
					for _, a := range pending {
						if cb := s.onReject[a.ApproveType]; cb != nil {
							cb(a)
						}
					}
				}
			case <-s.stop:
				return
			}
		}
	}()
}

// NotifyWebhook C9 告警出站（M3 先落地 webhook 单通道；SMTP/企微留待 M5 后续）
func (s *Service) NotifyWebhook(level, event, msg string) {
	channels, err := s.st.ListWebhookChannels()
	if err != nil {
		return
	}
	for _, ch := range channels {
		go postWebhook(ch, map[string]any{"level": level, "event": event, "msg": msg, "ts": time.Now().UTC().Format(time.RFC3339)})
	}
}
