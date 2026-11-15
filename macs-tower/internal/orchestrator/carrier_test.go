// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"path/filepath"
	"testing"

	"macs.local/tower/internal/store"
)

// 会话载体亲和（V3.5.8）：任务已有 carrier → 只派给载体孵化器；
// 载体不可用（离线/熔断/预算暂停）→ 回退全选机（新会话，carrier 随 HandleResult 更新）


// 亲和：任务 carrier 指向 inc-b → 只返回 inc-b（即使 inc-c 空槽更多）
func TestCarrierAffinityPinsIncubator(t *testing.T) {
	st, _, orch := newTestStack(t)
	// 两台孵化器：inc-b（载体）与 inc-c（空槽更多但非载体）
	for _, inc := range []store.Incubator{
		{ID: "inc-b", Name: "inc-b", Status: "online", Capacity: 4, LastIP: "127.0.0.1", ListenPort: 8473, Drivers: `["session_agent@1.0"]`},
		{ID: "inc-c", Name: "inc-c", Status: "online", Capacity: 8, LastIP: "127.0.0.1", ListenPort: 8474, Drivers: `["session_agent@1.0"]`},
	} {
		if _, err := st.UpsertIncubator(inc); err != nil {
			t.Fatal(err)
		}
	}
	task := store.Task{ID: "task-aff", Goal: "会话载体任务", Status: "running",
		CarrierIncubator: "inc-b", CarrierSession: "sess-42"}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{
		{ID: store.NewID(), TaskID: task.ID, Seq: 2, Status: "pending",
			Payload: `{"mode":"session_agent","session":{"driver":"claude"}}`,
			RequiredDriver: "session_agent", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}

	subs, _ := st.ListSubtasks(task.ID)
	incs, _ := st.ListIncubators(true)
	cands := orch.selectIncubators(incs, map[string]int{}, subs[0], task.ID)
	if len(cands) != 1 || cands[0].ID != "inc-b" {
		t.Errorf("亲和应只返回载体 inc-b，got %v", cands)
	}
}

// 载体离线 → 回退常规选机（新会话续接）
func TestCarrierFallbackWhenOffline(t *testing.T) {
	st, _, orch := newTestStack(t)
	st.SetIncubatorStatus("inc-b", "offline") // ListIncubators(true) 过滤离线
	if _, err := st.UpsertIncubator(store.Incubator{
		ID: "inc-c", Name: "inc-c", Status: "online", Capacity: 8,
		LastIP: "127.0.0.1", ListenPort: 8474, Drivers: `["session_agent@1.0"]`,
	}); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "task-fb", Goal: "载体离线回退", Status: "running",
		CarrierIncubator: "inc-b", CarrierSession: "sess-42"}
	if err := st.InsertTaskWithSubtasks(task, []store.Subtask{
		{ID: store.NewID(), TaskID: task.ID, Seq: 1, Status: "pending",
			Payload: `{"mode":"session_agent","session":{"driver":"claude"}}`,
			RequiredDriver: "session_agent", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	subs, _ := st.ListSubtasks(task.ID)
	incs, _ := st.ListIncubators(true)
	cands := orch.selectIncubators(incs, map[string]int{}, subs[0], task.ID)
	if len(cands) == 0 || cands[0].ID == "inc-b" {
		t.Errorf("载体离线应回退他机，got %v", cands)
	}
}

// SetTaskCarrier 幂等：仅首次生效（会话连续性，锁定后不漂移）
func TestSetTaskCarrierIdempotent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	task := store.Task{ID: "t-carrier", Goal: "g", Status: "running", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	if err := st.InsertTaskWithSubtasks(task, nil); err != nil {
		t.Fatal(err)
	}
	_ = st.SetTaskCarrier(task.ID, "inc-a", "sess-1")
	_ = st.SetTaskCarrier(task.ID, "inc-b", "sess-2") // 应被忽略
	got, _ := st.GetTask(task.ID)
	if got.CarrierIncubator != "inc-a" || got.CarrierSession != "sess-1" {
		t.Errorf("carrier 应锁定首次值，got %s/%s", got.CarrierIncubator, got.CarrierSession)
	}
}
