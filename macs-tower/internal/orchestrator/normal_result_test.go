// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"testing"

	"macs.local/tower/internal/store"
)

// M2.6 §6.4.3.2 P0 回归：普通执行结果（非复核）经 FinishSubtaskAttempt 核销，
// 成功必须落 completed 终态（复核发现此前普通成功永远停在 dispatched 的接线缺失）
func TestNormalResultWritesTerminalState(t *testing.T) {
	st, _, orch := newTestStack(t)
	task, err := orch.SubmitPlan(TaskRequest{Goal: "普通结果终态回归"})
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := st.ListSubtasks(task.ID)
	var subA store.Subtask
	for _, s := range subs {
		if s.Kind == "" || s.Kind == "execute" {
			subA = s
			break
		}
	}
	if _, err := st.ClaimSubtaskLease(subA.ID, "inc-1", store.LeaseTimeoutFor("", 0)); err != nil {
		t.Fatal(err)
	}

	result := `{"mode":"text_analysis","chars":5,"non_space":5,"unique":5,"top_chars":[]}`
	orch.HandleResult(Result{
		SubtaskID: subA.ID, IncubatorID: "inc-1", WorkerID: "w", Status: "completed",
		Result: result, AttemptCount: 1,
	})

	got, _ := st.GetSubtask(subA.ID)
	if got.Status == "dispatched" || got.Status == "running" {
		t.Fatalf("P0 回归：普通成功结果未写终态，仍为 %s（复核 ef62c82 指出的接线缺失重现）", got.Status)
	}
	// 成功路径进入质量管线（review_pending 或带 review 质量档），不能是 failed
	if got.Status == "failed" {
		t.Errorf("成功结果不应 failed: %s", got.Error)
	}
}

// 旧孵化器兼容：回执不带 attempt_count（=0）→ 回退 DB attempt 核销
func TestNormalResultLegacyIncubatorNoAttempt(t *testing.T) {
	st, _, orch := newTestStack(t)
	task, _ := orch.SubmitPlan(TaskRequest{Goal: "旧孵化器兼容"})
	subs, _ := st.ListSubtasks(task.ID)
	var subA store.Subtask
	for _, s := range subs {
		if s.Kind == "" || s.Kind == "execute" {
			subA = s
			break
		}
	}
	if _, err := st.ClaimSubtaskLease(subA.ID, "inc-1", store.LeaseTimeoutFor("", 0)); err != nil {
		t.Fatal(err)
	}
	orch.HandleResult(Result{
		SubtaskID: subA.ID, IncubatorID: "inc-1", WorkerID: "w", Status: "completed",
		Result: `{"mode":"text_analysis"}`, AttemptCount: 0, // 旧孵化器不带 attempt
	})
	got, _ := st.GetSubtask(subA.ID)
	if got.Status != "dispatched" && got.Status != "running" && got.Status != "failed" {
		t.Logf("旧孵化器兼容路径：终态 %s（回退 DB attempt 核销成功）", got.Status)
	} else if got.Status == "dispatched" || got.Status == "running" {
		t.Errorf("attempt=0 回退 DB attempt 应完成核销，仍为 %s", got.Status)
	}
}
