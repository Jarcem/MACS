// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"path/filepath"
	"time"
	"testing"

	"macs.local/tower/internal/store"
)

// M2.6 §6.4.3.1 租约测试：确定性 Watchdog（now 由调用方注入，可精确重放到期边界）
func TestLeaseRecoveryRetryPath(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t-lease", Goal: "g", Status: "running", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	sub := store.Subtask{ID: "s-lease", TaskID: task.ID, Seq: 1, Status: "pending",
		MaxRetries: 3, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{sub}); err != nil {
		t.Fatal(err)
	}
	// 抢占（attempt=1，租约按 timeout_ms=0 → 120s+30s）
	attempt, err := st.ClaimSubtaskLease("s-lease", "inc-1", 0)
	if err != nil || attempt != 1 {
		t.Fatalf("claim: attempt=%d err=%v", attempt, err)
	}
	// 租约未到期：now = claim+10s → 不回收
	got, err := st.RecoverExpiredLeases(time.Now().UTC().Add(10 * time.Second).Format(time.RFC3339))
	if err != nil || len(got) != 0 {
		t.Errorf("未到期不应回收: %v %v", got, err)
	}
	// 到期边界重放：now = claim+200s（> 150s 租约）→ attempt=1 < max=3 → retry 回池
	got, err = st.RecoverExpiredLeases(time.Now().UTC().Add(200 * time.Second).Format(time.RFC3339))
	if err != nil || len(got) != 1 || got[0].Action != "retry" {
		t.Fatalf("到期应 retry 回收: %v %v", got, err)
	}
	sub2, _ := st.GetSubtask("s-lease")
	if sub2.Status != "pending" || sub2.IncubatorID != "" || sub2.Result != "" {
		t.Errorf("retry 回收应清空载体/产物并回池: %+v", sub2)
	}
}

// M2.6 §6.4.3.1：attempt 耗尽 → failed（E01 口径）
func TestLeaseRecoveryExhaustedPath(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t-lease2", Goal: "g", Status: "running", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	sub := store.Subtask{ID: "s-lease2", TaskID: task.ID, Seq: 1, Status: "pending",
		AttemptCount: 3, MaxRetries: 3, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{sub}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimSubtaskLease("s-lease2", "inc-1", 0); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecoverExpiredLeases(time.Now().UTC().Add(200 * time.Second).Format(time.RFC3339))
	if err != nil || len(got) != 1 || got[0].Action != "failed" {
		t.Fatalf("attempt 耗尽应 failed: %v %v", got, err)
	}
	sub2, _ := st.GetSubtask("s-lease2")
	if sub2.Status != "failed" {
		t.Errorf("耗尽应 failed，got %s", sub2.Status)
	}
}

// M2.6 §6.4.3.2 条件终态核销：attempt/incubator 不匹配、已取消 → 拒绝
func TestFinishSubtaskAttemptGuards(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t-fin", Goal: "g", Status: "running", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	sub := store.Subtask{ID: "s-fin", TaskID: task.ID, Seq: 1, Status: "pending", CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{sub}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimSubtaskLease("s-fin", "inc-1", 0); err != nil {
		t.Fatal(err)
	}
	// 错误孵化器
	changed, err := st.FinishSubtaskAttempt("s-fin", "completed", "r", "", "w", "", "", "inc-wrong", 1)
	if err != nil || changed {
		t.Errorf("错误孵化器应拒绝: changed=%v err=%v", changed, err)
	}
	// 错误 attempt
	changed, _ = st.FinishSubtaskAttempt("s-fin", "completed", "r", "", "w", "", "", "inc-1", 2)
	if changed {
		t.Error("旧 attempt 迟到结果应拒绝")
	}
	// 非法终态
	if _, err := st.FinishSubtaskAttempt("s-fin", "review_pending", "r", "", "w", "", "", "inc-1", 1); err == nil {
		t.Error("非法终态应报错")
	}
	// 正确匹配
	changed, err = st.FinishSubtaskAttempt("s-fin", "completed", "r", "", "w", "", "", "inc-1", 1)
	if err != nil || !changed {
		t.Fatalf("匹配应核销: %v %v", changed, err)
	}
	// 已 completed：重复结果拒绝（含旧 attempt 迟到）
	changed, _ = st.FinishSubtaskAttempt("s-fin", "completed", "r2", "", "w2", "", "", "inc-1", 1)
	if changed {
		t.Error("终态后重复结果应拒绝")
	}
}

// M2.6 §6.4.3.3 强制取消：全部在途/待执行强制 cancelled + 在途清单返回 + 幂等
func TestForceCancelSubtasks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t-cancel", Goal: "g", Status: "running", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	subs := []store.Subtask{
		{ID: "s-p", TaskID: task.ID, Seq: 1, Status: "pending", CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt},
		{ID: "s-d", TaskID: task.ID, Seq: 2, Status: "dispatched", IncubatorID: "inc-1", AttemptCount: 2, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt},
		{ID: "s-c", TaskID: task.ID, Seq: 3, Status: "completed", CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt},
	}
	if err := st.InsertTaskWithSubtasks(task, subs); err != nil {
		t.Fatal(err)
	}
	inflight, err := st.ForceCancelSubtasks(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inflight) != 1 || inflight[0].SubtaskID != "s-d" || inflight[0].Attempt != 2 {
		t.Errorf("在途清单应仅 s-d(attempt=2)，got %+v", inflight)
	}
	for _, id := range []string{"s-p", "s-d"} {
		sub, _ := st.GetSubtask(id)
		if sub.Status != "cancelled" {
			t.Errorf("%s 应强制 cancelled，got %s", id, sub.Status)
		}
	}
	if sub, _ := st.GetSubtask("s-c"); sub.Status != "completed" {
		t.Error("已完成子任务不应被取消")
	}
	// 幂等：再次取消在途清单为空（已全部 cancelled）
	inflight2, err := st.ForceCancelSubtasks(task.ID)
	if err != nil || len(inflight2) != 0 {
		t.Errorf("幂等取消在途清单应为空，got %+v err=%v", inflight2, err)
	}
	// M2.6 §6.4.3.2：已取消子任务的迟到结果不得复活（E17）
	changed, err := st.FinishSubtaskAttempt("s-d", "completed", "r", "", "w", "", "", "inc-1", 2)
	if err != nil || changed {
		t.Errorf("取消后迟到结果不得复活（E17），changed=%v err=%v", changed, err)
	}
}
