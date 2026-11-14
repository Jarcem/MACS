// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package billing

import (
	"path/filepath"
	"testing"

	"macs.local/tower/internal/approvals"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
)

// §12.1 单元测试：E09 预算门（任务级 100% 硬停 / 80% 软告警 / off 模式只告警不拦截）
func newTestEnv(t *testing.T) (*store.Store, *events.Hub, *approvals.Service, *Service) {
	db := filepath.Join(t.TempDir(), "tower-test.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// 内存迁移：直接执行 DDL（不依赖 migrations 目录）
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE IF NOT EXISTS incubators (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, status TEXT NOT NULL DEFAULT 'online', capacity INTEGER NOT NULL DEFAULT 4, platforms TEXT NOT NULL DEFAULT '[]', drivers TEXT NOT NULL DEFAULT '[]', listen_port INTEGER NOT NULL DEFAULT 0, last_ip TEXT NOT NULL DEFAULT '', last_heartbeat TEXT, registered_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS dispatch_log (id TEXT PRIMARY KEY, subtask_id TEXT NOT NULL, incubator_id TEXT, msg_id TEXT UNIQUE NOT NULL, ack_status TEXT NOT NULL DEFAULT 'pending', dispatched_at TEXT NOT NULL, acked_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, goal TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', progress INTEGER NOT NULL DEFAULT 0, priority TEXT NOT NULL DEFAULT 'normal', acceptance_criteria TEXT NOT NULL DEFAULT '[]', acceptance_design_note TEXT NOT NULL DEFAULT '', deadline_at TEXT, budget_cents REAL NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS subtasks (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, seq INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', depends_on TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', incubator_id TEXT NOT NULL DEFAULT '', worker_id TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'execute', review_of TEXT NOT NULL DEFAULT '', attempt_count INTEGER NOT NULL DEFAULT 0, max_retries INTEGER NOT NULL DEFAULT 3, next_retry_at TEXT NOT NULL DEFAULT '', review_round INTEGER NOT NULL DEFAULT 0, quality_status TEXT NOT NULL DEFAULT '', fingerprint TEXT NOT NULL DEFAULT '', template_id TEXT NOT NULL DEFAULT '', required_driver TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS approvals (id TEXT PRIMARY KEY, task_id TEXT, subtask_id TEXT, incubator_id TEXT, approval_type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', requester TEXT NOT NULL DEFAULT 'system', approver TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', expires_at TEXT, decision TEXT, decision_note TEXT, decided_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS cost_records (id TEXT PRIMARY KEY, incubator_id TEXT NOT NULL DEFAULT '', task_id TEXT, subtask_id TEXT, worker_id TEXT NOT NULL DEFAULT '', cost_type TEXT NOT NULL DEFAULT 'api', provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', amount_cents REAL NOT NULL DEFAULT 0, token_usage TEXT, duration_sec INTEGER, description TEXT NOT NULL DEFAULT '', recorded_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS budget_limits (id TEXT PRIMARY KEY, scope_type TEXT NOT NULL, scope_id TEXT, amount_cents REAL NOT NULL DEFAULT 0, warn_at_pct INTEGER NOT NULL DEFAULT 80, enforce_mode TEXT NOT NULL DEFAULT 'hard', limit_version INTEGER NOT NULL DEFAULT 0, issued_at TEXT NOT NULL, UNIQUE(scope_type, scope_id))`,
		`CREATE TABLE IF NOT EXISTS telemetry_events (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, subtask_id TEXT, worker_id TEXT NOT NULL DEFAULT '', platform_adapter TEXT NOT NULL DEFAULT '', agent_platform TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}', timestamp TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS notify_channels (id TEXT PRIMARY KEY, kind TEXT NOT NULL, config TEXT NOT NULL DEFAULT '{}', min_level TEXT NOT NULL DEFAULT 'warn', enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS subtask_dependencies (subtask_id TEXT NOT NULL, depends_on_id TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(subtask_id, depends_on_id))`,
		`CREATE TABLE IF NOT EXISTS parallel_groups (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, depth INTEGER NOT NULL, subtask_id TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS incubator_resources (id INTEGER PRIMARY KEY AUTOINCREMENT, incubator_id TEXT NOT NULL, cpu_cores INTEGER, mem_alloc_mb INTEGER, mem_sys_mb INTEGER, active_workers INTEGER, max_workers INTEGER, load_ratio REAL, reported_at TEXT NOT NULL)`,
	} {
		if _, err := st.Exec(ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	hub := events.New()
	appr := approvals.New(st, hub)
	return st, hub, appr, New(st, hub, appr)
}

func seedTask(t *testing.T, st *store.Store, budget float64) string {
	t.Helper()
	id := store.NewID()
	now := "2026-01-01T00:00:00Z"
	if err := st.InsertTaskWithSubtasks(store.Task{
		ID: id, Goal: "预算门测试", Status: "pending", BudgetCents: budget,
		CreatedAt: now, UpdatedAt: now,
	}, []store.Subtask{{ID: store.NewID(), TaskID: id, Seq: 1, Status: "pending", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return id
}

func TestBudgetGateBlocksAndResumes(t *testing.T) {
	st, _, appr, bill := newTestEnv(t)
	taskID := seedTask(t, st, 1.0)

	// 无成本 → 放行
	v, err := bill.CheckTaskBudget(taskID)
	if err != nil || v.Exceeded {
		t.Fatalf("无成本不应触发 E09: %+v %v", v, err)
	}

	// 成本 0.8/1.0 → 80% 软告警不拦截
	_ = st.InsertCostRecord(store.CostRecord{ID: "c1", TaskID: taskID, AmountCents: 0.8, RecordedAt: "2026-01-01T00:00:01Z"})
	v, _ = bill.CheckTaskBudget(taskID)
	if v.Exceeded || !v.Warned {
		t.Errorf("0.8/1.0 应软告警不硬停: %+v", v)
	}

	// 成本 1.0/1.0 → 100% 硬停（E09）
	_ = st.InsertCostRecord(store.CostRecord{ID: "c2", TaskID: taskID, AmountCents: 0.2, RecordedAt: "2026-01-01T00:00:02Z"})
	v, _ = bill.CheckTaskBudget(taskID)
	if !v.Exceeded {
		t.Fatalf("1.0/1.0 应触发 E09")
	}
	if err := bill.ExceedE09(taskID, v); err != nil {
		t.Fatalf("ExceedE09: %v", err)
	}
	task, _ := st.GetTask(taskID)
	if task.Status != "blocked" {
		t.Errorf("任务应 blocked，got %s", task.Status)
	}
	pending, _ := st.ListApprovals("pending", 10)
	if len(pending) != 1 || pending[0].ApproveType != "budget_exceed" {
		t.Errorf("应挂一条 budget_exceed 审批，got %+v", pending)
	}

	// 审批通过 → 解除 blocked（OnBudgetDecision 语义）
	if err := st.SetTaskStatus(taskID, "running"); err != nil {
		t.Fatal(err)
	}
	_ = appr
	if task, _ = st.GetTask(taskID); task.Status != "running" {
		t.Errorf("解除后应回到调度，got %s", task.Status)
	}
}

func TestBudgetGateOffModeWarnsOnly(t *testing.T) {
	st, _, _, bill := newTestEnv(t)
	taskID := seedTask(t, st, 1.0)
	_ = st.InsertCostRecord(store.CostRecord{ID: "c1", TaskID: taskID, AmountCents: 5.0, RecordedAt: "2026-01-01T00:00:01Z"})
	// enforce_mode=off → 只记账告警，不拦截（10.3 budget.limit_enforce）
	if err := st.UpsertBudgetLimit(store.BudgetLimit{
		ID: "bl1", ScopeType: "task", ScopeID: taskID, AmountCents: 1, EnforceMode: "off", WarnAtPct: 80, IssuedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	v, err := bill.CheckTaskBudget(taskID)
	if err != nil || v.Exceeded {
		t.Errorf("off 模式不应拦截: %+v %v", v, err)
	}
	if !v.Warned {
		t.Errorf("off 模式仍应软告警: %+v", v)
	}
}

func TestGlobalBudgetGate(t *testing.T) {
	st, _, _, bill := newTestEnv(t)
	if err := st.UpsertBudgetLimit(store.BudgetLimit{
		ID: "bl-g", ScopeType: "global", AmountCents: 10, WarnAtPct: 80, EnforceMode: "hard", IssuedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.InsertCostRecord(store.CostRecord{ID: "c1", TaskID: "t1", AmountCents: 11, RecordedAt: "2026-01-01T00:00:01Z"})
	v, err := bill.CheckGlobalBudget()
	if err != nil || !v.Exceeded {
		t.Errorf("全局 11/10 应硬停: %+v %v", v, err)
	}
}
