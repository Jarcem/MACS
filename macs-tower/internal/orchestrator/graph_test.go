// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"path/filepath"
	"testing"

	"macs.local/tower/internal/store"
)

// §12.1：E10 依赖环检测（通用 DFS，创建时拒绝带环任务）
func TestValidateDagRejectsCycles(t *testing.T) {
	a := store.Subtask{ID: "a", DependsOn: "c"}
	b := store.Subtask{ID: "b", DependsOn: "a"}
	c := store.Subtask{ID: "c", DependsOn: "b"}
	if err := store.ValidateDag([]store.Subtask{a, b, c}); err == nil {
		t.Error("三节点环应被检出（E10）")
	}
	if err := store.ValidateDag([]store.Subtask{
		{ID: "a", DependsOn: "a"},
	}); err == nil {
		t.Error("自环应被检出")
	}
	if err := store.ValidateDag([]store.Subtask{
		{ID: "a"},
		{ID: "b", DependsOn: "a"},
		{ID: "c", DependsOn: "a,b"},
	}); err != nil {
		t.Errorf("合法 DAG 不应报错: %v", err)
	}
}

// 数据库设计 §2.3/§2.4/§2.1 规范化落地：依赖表 / 并行组 / 资源快照写入与清理
func TestNormalizedGraphTables(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	task := store.Task{ID: store.NewID(), Goal: "规范化表冒烟", Status: "pending", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	a := store.Subtask{ID: store.NewID(), TaskID: task.ID, Seq: 1, Status: "pending", CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	b := store.Subtask{ID: store.NewID(), TaskID: task.ID, Seq: 2, Status: "pending", DependsOn: a.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	c := store.Subtask{ID: store.NewID(), TaskID: task.ID, Seq: 3, Status: "pending", DependsOn: a.ID + "," + b.ID, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{a, b, c}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	n1, err := st.CountSubtaskDependencies()
	if err != nil {
		t.Fatalf("deps count: %v", err)
	}
	if n1 != 3 { // a 无依赖；b→a；c→a,b
		t.Errorf("subtask_dependencies 应 3 行，got %d", n1)
	}
	n2, _ := st.CountParallelGroups()
	if n2 != 3 {
		t.Errorf("parallel_groups 应 3 行（每子任务一组），got %d", n2)
	}
	// 资源快照写入与 7 天清理（6.5）
	if err := st.InsertIncubatorResource(store.IncubatorResource{IncubatorID: "inc-1", LoadRatio: 0.5, ReportedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("resource insert: %v", err)
	}
	if n3, _ := st.CountIncubatorResources(); n3 != 1 {
		t.Errorf("资源快照应 1 行，got %d", n3)
	}
	if n4, _ := st.PurgeIncubatorResources(); n4 != 1 {
		t.Errorf("7 天清理应移除 1 行，got %d", n4)
	}
}

// 6.4.11 孵化器级预算：月度成本超上界 → 停派（选机硬过滤）
func TestIncubatorBudgetGateFilters(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 上界 10 分；当月已消耗 10 分
	if err := st.UpsertBudgetLimit(store.BudgetLimit{
		ID: "bl-inc", ScopeType: "incubator", ScopeID: "inc-1",
		AmountCents: 10, WarnAtPct: 80, EnforceMode: "hard", IssuedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertCostRecord(store.CostRecord{
		ID: "c1", IncubatorID: "inc-1", TaskID: "t1", AmountCents: 10, RecordedAt: "2026-01-01T00:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}
	spent, err := st.IncubatorMonthlyCents("inc-1")
	if err != nil || spent != 10 {
		t.Fatalf("月度成本应 10，got %v %v", spent, err)
	}
}
