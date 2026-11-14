// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package quality 三层质量保障引擎（M3）
//   第一层 自检：Worker 执行产出时随 execution_result 携带 self_check 报告（done 验收门 §6.4.10）
//   第二层 复核：独立 review 子任务，复核 Worker 重跑原任务并比对指纹（FR-14，E04 打回 ≤3 轮）
//   第三层 抽查：任务完成前按 Q7 比例抽样，追溯链对账核验（FR-16，E05 升级路径）
// 状态机唯一口径：主设计 §6.4.6
package quality

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	"macs.local/tower/internal/archive"
	"macs.local/tower/internal/approvals"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/trace"
)

// Config 质量管线开关（10.3：feature.review.enabled=true / feature.spot_check.enabled=false）
type Config struct {
	ReviewEnabled    bool // MACS_FEATURE_REVIEW，默认 true（10.3 feature.review.enabled）
	SpotCheckEnabled bool // MACS_FEATURE_SPOT_CHECK，默认 false
	SpotCheckRatio   int  // Q7 默认 20%
	MaxReviewRounds  int  // Q5 默认 3 轮
	ReviewTTL        time.Duration
}

type Service struct {
	st    *store.Store
	hub   *events.Hub
	trace *trace.Service
	arch  *archive.Service
	appr  *approvals.Service
	cfg   Config
	kick  func() // 唤醒编排器（复核子任务入列后）
	onSpotFinished   func(taskID, status string) // 抽查收口回调（编排器注册）
	onSubtaskFinal   func(taskID string)         // 复核放行后任务重算收口回调（编排器注册）
}

func New(st *store.Store, hub *events.Hub, tr *trace.Service, arch *archive.Service, appr *approvals.Service, cfg Config, kick func()) *Service {
	return &Service{st: st, hub: hub, trace: tr, arch: arch, appr: appr, cfg: cfg, kick: kick}
}

// OnSpotFinished 注册抽查收口回调（编排器据此终结任务）
func (s *Service) OnSpotFinished(fn func(taskID, status string)) { s.onSpotFinished = fn }

// OnSubtaskFinal 注册复核放行回调（编排器重算任务并按需收口）
func (s *Service) OnSubtaskFinal(fn func(taskID string)) { s.onSubtaskFinal = fn }

// ── 自检报告（第一层）─────────────────────────

// SelfCheckReport execution_result 内嵌的自检报告（孵化器产出）
type SelfCheckReport struct {
	Status  string `json:"status"` // passed/failed/partial
	Score   float64
	Items   []CheckItem `json:"items"`
	Summary string      `json:"summary"`
}

type CheckItem struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Note   string `json:"note"`
}

// SelfCheckFromResult 从 execution_result 载荷提取自检报告（缺省视为 passed，兼容 2.x 孵化器）
func SelfCheckFromResult(result string) SelfCheckReport {
	var wrapper struct {
		SelfCheck *SelfCheckReport `json:"self_check"`
	}
	if json.Unmarshal([]byte(result), &wrapper) == nil && wrapper.SelfCheck != nil {
		return *wrapper.SelfCheck
	}
	return SelfCheckReport{Status: "passed"}
}

// ── 复核（第二层，FR-14 / E04）────────────────

// reviewableMode 确定性内置模式可自动复核；Agent 模式须显式要求（防重复副作用）
func reviewable(sub store.Subtask) bool {
	var p struct {
		Mode   string `json:"mode"`
		Review bool   `json:"review"`
	}
	if json.Unmarshal([]byte(sub.Payload), &p) != nil {
		return false
	}
	if p.Review {
		return true
	}
	return p.Mode == "text_analysis" || p.Mode == "merge"
}

// OnExecutionResult 执行结果进入质量管线（orchestrator.HandleResult 调用）
// 返回值：done=false 时子任务处于 review_pending，任务完成判定交给复核回路
func (s *Service) OnExecutionResult(sub store.Subtask) (done bool) {
	if sub.Status != "completed" {
		return true // failed 走重试回路，不进复核
	}
	check := SelfCheckFromResult(sub.Result)

	// done 验收门（§6.4.10）：strict 下自检不通过不得提交复核
	if check.Status == "failed" {
		s.hub.Publish("quality_event", map[string]any{
			"layer": "self_check", "subtask_id": sub.ID, "status": "failed", "summary": check.Summary,
		})
		return true // 由编排器按 E01 重试
	}

	if !s.cfg.ReviewEnabled || !reviewable(sub) {
		return true
	}
	if err := s.spawnReview(sub); err != nil {
		log.Printf(`{"level":"warn","module":"quality","msg":"spawn review: %v"}`, err)
		return true // 复核孵化失败降级为单层自检
	}
	_ = s.st.SetSubtaskStatus(sub.ID, "review_pending", "review_pending")
	s.hub.Publish("quality_event", map[string]any{
		"layer": "review", "subtask_id": sub.ID, "status": "review_pending",
	})
	return false
}

// spawnReview 孵化复核子任务（幂等：同一原子任务仅一条未完结复核）
func (s *Service) spawnReview(original store.Subtask) error {
	live, err := s.st.ListSubtasksByReviewOf(original.ID)
	if err != nil {
		return err
	}
	for _, r := range live {
		if r.Kind == "review" && (r.Status == "pending" || r.Status == "dispatched" || r.Status == "running") {
			return nil
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var origJob json.RawMessage
	if json.Unmarshal([]byte(original.Payload), &origJob) != nil {
		return fmt.Errorf("original payload malformed")
	}
	payload, _ := json.Marshal(map[string]any{
		"mode":          "review",
		"review_job":    origJob,
		"review_expect": original.Fingerprint,
		"review_round":  original.Round,
	})
	review := store.Subtask{
		ID: store.NewID(), TaskID: original.TaskID, Seq: original.Seq + 1000,
		Title: fmt.Sprintf("复核 · %s（第 %d 轮）", original.Title, original.Round+1),
		Payload: string(payload), Kind: "review", ReviewOf: original.ID,
		MaxRetries: 1, Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.InsertSubtaskStandalone(review); err != nil {
		return err
	}
	s.hub.Publish("subtask_created", map[string]any{
		"task_id": original.TaskID, "subtask_id": review.ID, "kind": "review", "review_of": original.ID,
	})
	if s.kick != nil {
		s.kick()
	}
	return nil
}

// OnReviewResult 复核子任务回传（E04：approved 放行 / rejected 打回重执行）
func (s *Service) OnReviewResult(review store.Subtask, verdict string, note string) {
	original, err := s.st.GetSubtask(review.ReviewOf)
	if err != nil {
		return
	}
	_ = s.arch.ReviewDoc(original.TaskID, review, original, verdict, note)
	s.hub.Publish("quality_event", map[string]any{
		"layer": "review", "subtask_id": original.ID, "review_subtask_id": review.ID,
		"status": verdict, "round": original.Round,
	})

	if verdict == "approved" {
		_ = s.st.SetSubtaskStatus(original.ID, "completed", "review_passed")
		if task, err := s.st.GetTask(original.TaskID); err == nil {
			_ = s.arch.HandoffForSubtask(task, original)
		}
		if updated, err := s.st.RecomputeTask(original.TaskID); err == nil {
			s.hub.Publish("subtask_finished", map[string]any{
				"task_id": original.TaskID, "subtask_id": original.ID,
				"status": "completed", "task_status": updated.Status, "progress": updated.Progress,
			})
			if s.onSubtaskFinal != nil {
				s.onSubtaskFinal(original.TaskID) // 任务收口判定（抽查/归档）
			}
		}
		if s.kick != nil {
			s.kick() // 下游依赖此时满足，触发调度
		}
		return
	}

	// E04：复核不通过 → 打回重执行（≤Q5 3 轮），超轮次 failed + 人工审批
	next := original.Round + 1
	if next <= s.cfg.MaxReviewRounds {
		_ = s.st.SetSubtaskReviewRound(original.ID, next)
		_ = s.st.SetSubtaskRetry(original.ID, 0, original.MaxRetries, "") // 重新计数，由编排器重派
		_ = s.st.SetSubtaskStatus(original.ID, "pending", "")
		s.hub.Publish("quality_event", map[string]any{
			"layer": "review", "subtask_id": original.ID, "status": "review_rejected", "action": "requeued", "round": next,
		})
		if s.kick != nil {
			s.kick()
		}
		return
	}
	_ = s.st.SetSubtaskStatus(original.ID, "failed", "")
	_, _ = s.appr.Raise(approvals.TypeHumanFinal, original.TaskID, original.ID,
		"复核连续打回，需人工终审", fmt.Sprintf("子任务 %s 复核 %d 轮未通过：%s", original.Title, next, note), nil, 0)
	s.hub.Publish("quality_event", map[string]any{
		"layer": "review", "subtask_id": original.ID, "status": "failed", "action": "human_final",
	})
}

// ── 抽查（第三层，FR-16 / E05）────────────────

// ShouldSample Q7 抽样比例判定（任务完成边界调用）
func (s *Service) ShouldSample() bool {
	if !s.cfg.SpotCheckEnabled || s.cfg.SpotCheckRatio <= 0 {
		return false
	}
	return rand.Intn(100) < s.cfg.SpotCheckRatio
}

// RaiseSpotCheck 任务抽查立案（scope=task，核验项=追溯链指纹对账）
func (s *Service) RaiseSpotCheck(taskID string) error {
	checked, _ := s.st.TaskSpotChecked(taskID)
	if checked {
		return nil
	}
	c := store.SpotCheck{
		ID: store.NewID(), TaskID: taskID, CheckScope: "task",
		MaxAttempts: 3,
	}
	if err := s.st.InsertSpotCheck(c); err != nil {
		return err
	}
	s.hub.Publish("spot_check_created", map[string]any{"task_id": taskID, "id": c.ID})
	return nil
}

// RunPendingSpotChecks 抽查重试调度（6.5 每分钟）：对账核验 → passed/failed → E05 升级
func (s *Service) RunPendingSpotChecks() {
	pending, err := s.st.ListPendingSpotChecks()
	if err != nil {
		return
	}
	for _, c := range pending {
		broken, err := s.trace.VerifyTask(c.TaskID)
		passed := err == nil && len(broken) == 0
		items := map[string]any{
			"items": []map[string]any{
				{"name": "trace_chain_verify", "passed": passed,
					"note": fmt.Sprintf("broken=%v err=%v", broken, err)},
			},
		}
		b, _ := json.Marshal(items)
		rate := 0.0
		if passed {
			rate = 1.0
		}
		note := "trace chain verified"
		if !passed {
			note = fmt.Sprintf("E06 fingerprint mismatch: %v", broken)
		}
		if err := s.st.FinishSpotCheck(c.ID, map[bool]string{true: "passed", false: "failed"}[passed],
			string(b), rate, passed, note); err != nil {
			log.Printf(`{"level":"warn","module":"quality","msg":"spot check finish: %v"}`, err)
			continue
		}
		s.hub.Publish("spot_check_finished", map[string]any{
			"id": c.ID, "task_id": c.TaskID, "status": map[bool]string{true: "passed", false: "failed"}[passed],
		})
		if s.onSpotFinished != nil {
			s.onSpotFinished(c.TaskID, map[bool]string{true: "passed", false: "failed"}[passed])
		}
		if !passed {
			// E05：抽查不通过 → 升级 human_final 人工终审；任务冻结等待核查（不自动回滚）
			_, _ = s.appr.Raise(approvals.TypeHumanFinal, c.TaskID, "",
				"抽查不通过，需人工终审", note, map[string]any{"broken_subtasks": broken}, 0)
		}
	}
}

// ── 护栏（6.4.10 唤醒环检测）───────────────────

// FreezeSubtask 唤醒环触发：冻结子任务 + guardrail 审批转人工（E04 状态机 frozen 路径）
func (s *Service) FreezeSubtask(sub store.Subtask, reason string) error {
	_ = s.st.SetSubtaskStatus(sub.ID, "frozen", "frozen")
	_ = s.st.InsertGuardrailEvent(store.GuardrailEvent{
		ID: store.NewID(), TaskID: sub.TaskID, SubtaskID: sub.ID, IncubatorID: sub.IncubatorID,
		Type: "wake_loop", Action: "frozen",
		Detail: fmt.Sprintf(`{"reason":%q,"attempt_count":%d}`, reason, sub.AttemptCount),
	})
	s.hub.Publish("guardrail_event", map[string]any{
		"guardrail_type": "wake_loop", "subtask_id": sub.ID, "action": "frozen", "reason": reason,
	})
	s.appr.NotifyWebhook("warn", "guardrail_wake_loop", sub.Title)
	_, err := s.appr.Raise(approvals.TypeGuardrail, sub.TaskID, sub.ID,
		"护栏冻结：子任务重试环", reason, nil, 0)
	return err
}
