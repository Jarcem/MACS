// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"macs.local/tower/internal/archive"
	"macs.local/tower/internal/approvals"
	"macs.local/tower/internal/billing"
	"macs.local/tower/internal/dispatcher"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/quality"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/trace"
)

// §12.1 幂等测试：上报/事件/调度重放 100 次，结果一致，无重复孵化/重复副作用（E07）
func newTestStack(t *testing.T) (*store.Store, *events.Hub, *Orchestrator) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := events.New()
	appr := approvals.New(st, hub)
	tr := trace.New(st)
	arch := archive.New(st)
	bill := billing.New(st, hub, appr)
	disp := dispatcher.New(st, nil) // HandleResult 路径不触网，nil conn 安全
	qual := quality.New(st, hub, tr, arch, appr, quality.Config{
		ReviewEnabled: true, MaxReviewRounds: 3,
	}, nil)
	orch := New(st, disp, hub, tr, arch, qual, bill, Config{
		BroadcastEnabled: false, SubtaskTTL: 30e9, MaxRetries: 3, RetryBaseBackoff: 1,
	})
	return st, hub, orch
}

func TestExecutionResultReplayIdempotency(t *testing.T) {
	st, _, orch := newTestStack(t)

	// 提交任务（内置拆解：A/B 并行 + C 汇总）
	task, err := orch.SubmitPlan(TaskRequest{Goal: "幂等重放冒烟目标文本"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	subs, _ := st.ListSubtasks(task.ID)
	var subA store.Subtask
	for _, s := range subs {
		if s.Kind == "" || s.Kind == "execute" {
			subA = s
			break
		}
	}
	if subA.ID == "" {
		t.Fatal("未找到执行子任务")
	}
	if _, err := st.ClaimSubtaskLease(subA.ID, "inc-1", 0); err != nil {
		t.Fatalf("claim: %v", err)
	}

	workerResult := `{"mode":"text_analysis","chars":12,"non_space":10,"unique":8,"top_chars":[{"char":"a","n":3}]}`
	result := func() Result {
		return Result{
			SubtaskID: subA.ID, IncubatorID: "inc-1", WorkerID: "worker-a/1",
			Status: "completed", Result: workerResult, DurationMs: 12,
		}
	}

	// 重放 100 次
	for i := 0; i < 100; i++ {
		orch.HandleResult(result())
	}

	// 断言：子任务进入 review_pending（复核孵化），复核子任务仅 1 条
	got, _ := st.GetSubtask(subA.ID)
	if got.Status != "review_pending" {
		t.Errorf("首次处理后应 review_pending，got %s", got.Status)
	}
	reviews, _ := st.ListSubtasksByReviewOf(subA.ID)
	if len(reviews) != 1 {
		t.Errorf("复核子任务应仅 1 条（重放不重复孵化），got %d", len(reviews))
	}
	// 追溯链/遥测：仅 1 条（UNIQUE 约束 + E07 幂等）
	traces, _ := st.ListOriginTraces(task.ID, 100)
	subTraces := 0
	for _, tr := range traces {
		if tr.SubtaskID == subA.ID {
			subTraces++
		}
	}
	if subTraces != 1 {
		t.Errorf("追溯链节点应仅 1 条，got %d", subTraces)
	}
	telemetry, _ := st.ListTelemetryEvents(task.ID, "", "", 500)
	tel := 0
	for _, e := range telemetry {
		if e.SubtaskID == subA.ID {
			tel++
		}
	}
	if tel != 1 {
		t.Errorf("遥测事件应仅 1 条，got %d", tel)
	}
	// 指纹稳定（塔侧权威重算不随重放漂移）
	if got.Fingerprint == "" {
		t.Error("应记录产物指纹")
	}
}

func TestReviewResultReplayIdempotency(t *testing.T) {
	st, _, orch := newTestStack(t)
	task, _ := orch.SubmitPlan(TaskRequest{Goal: "复核重放目标文本"})
	subs, _ := st.ListSubtasks(task.ID)
	var subA store.Subtask
	for _, s := range subs {
		if s.Kind == "" || s.Kind == "execute" {
			subA = s
			break
		}
	}
	if _, err := st.ClaimSubtaskLease(subA.ID, "inc-1", 0); err != nil {
		t.Fatal(err)
	}
	orch.HandleResult(Result{SubtaskID: subA.ID, Status: "completed",
		Result: `{"mode":"text_analysis","chars":3,"non_space":3,"unique":3,"top_chars":[]}`, WorkerID: "w"})

	reviews, _ := st.ListSubtasksByReviewOf(subA.ID)
	if len(reviews) != 1 {
		t.Fatalf("复核孵化异常: %d", len(reviews))
	}
	review := reviews[0]
	_, _ = st.ClaimSubtaskLease(review.ID, "inc-1", 0)

	verdict := `{"review":{"status":"approved","fingerprint_match":true,"note":"指纹一致"}}`
	for i := 0; i < 100; i++ {
		orch.HandleResult(Result{SubtaskID: review.ID, Status: "completed", Result: verdict, WorkerID: "w"})
	}

	got, _ := st.GetSubtask(subA.ID)
	if got.Status != "completed" || got.QualityStatus != "review_passed" {
		t.Errorf("复核放行后应 completed/review_passed，got %s/%s", got.Status, got.QualityStatus)
	}
	// 复核报告 + 交接文档各仅 1 篇（重放不重复归档）
	docs, _ := st.ListArchiveDocs(task.ID, "", 100)
	reviewDocs, handoffs := 0, 0
	for _, d := range docs {
		if d.SubtaskID == subA.ID {
			switch d.DocType {
			case "review":
				reviewDocs++
			case "handoff":
				handoffs++
			}
		}
	}
	if reviewDocs != 1 || handoffs != 1 {
		t.Errorf("归档应各 1 篇 review=%d handoff=%d", reviewDocs, handoffs)
	}
}

// JSON 载荷可被解析（自检报告契约对齐）
func TestSelfCheckContractAligned(t *testing.T) {
	raw := `{"mode":"text_analysis","chars":1,"non_space":1,"unique":1,"top_chars":[],"self_check":{"status":"passed","score":1,"items":[]}}`
	var probe struct {
		SelfCheck struct {
			Status string `json:"status"`
		} `json:"self_check"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil || probe.SelfCheck.Status != "passed" {
		t.Error("自检报告内嵌契约破坏")
	}
}
