// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package e2e V1.5 真实契约 E2E（设计 §11.2 / 实施计划 Task 6）
// 必须在管理员提供的真实环境运行（MACS_E2E_API / MACS_E2E_ADMIN_TOKEN /
// MACS_E2E_ZCODE_AGENT_ID + 真实本机 action 配置）。mock 通过不算通过：
// 环境缺失时记 BLOCKED（skip），不得用 mock 替代。
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func requireE2EEnvironment(t *testing.T) {
	t.Helper()
	missing := []string{}
	for _, k := range []string{"MACS_E2E_API", "MACS_E2E_ADMIN_TOKEN", "MACS_E2E_ZCODE_AGENT_ID"} {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Skipf("BLOCKED: real E2E environment not configured (missing %v) — mock 不算通过", missing)
	}
}

func apiBase() string { return os.Getenv("MACS_E2E_API") }

func apiCall(t *testing.T, method, path string, body any, out any) {
	t.Helper()
	var buf bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = *bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, apiBase()+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", os.Getenv("MACS_E2E_ADMIN_TOKEN"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s → %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

type e2eTaskView struct {
	TaskID           string `json:"task_id"`
	Status           string `json:"status"`
	AcceptanceStatus string `json:"acceptance_status,omitempty"`
}

// getAgentSnapshot 身份快照（agent_id 不变量：全链路 agent_id 不变）
func getAgentSnapshot(t *testing.T, agentID string) map[string]any {
	t.Helper()
	var wrap struct {
		Data map[string]any `json:"data"`
	}
	apiCall(t, "GET", "/api/v1/agent-catalog", nil, &wrap)
	for _, a := range wrap.Data["agents"].([]any) {
		m := a.(map[string]any)
		if m["agent_id"] == agentID {
			return m
		}
	}
	t.Fatalf("agent %s not in catalog", agentID)
	return nil
}

// createDraft 提交双步骤草案（develop → deploy）
func createDraft(t *testing.T, zcodeID, cloudID, goal string) map[string]any {
	t.Helper()
	draft := map[string]any{
		"goal":               goal,
		"target_environment": "staging",
		"steps": []map[string]any{
			{
				"step_key": "develop", "title": "开发", "instruction": "implement a small fixture change and run tests",
				"agent_id": zcodeID, "action_key": os.Getenv("MACS_E2E_ZCODE_ACTION"),
				"step_kind": "execute", "expected_outputs": []string{"commit"},
				"acceptance_criteria": []map[string]any{{"validator_key": "test_report"}},
			},
			{
				"step_key": "deploy", "title": "部署", "instruction": "deploy the built artifact to staging and verify health",
				"agent_id": cloudID, "action_key": os.Getenv("MACS_E2E_CLOUD_ACTION"),
				"step_kind": "execute", "depends_on": []string{"develop"},
				"input_refs":          []string{"commit"},
				"acceptance_criteria": []map[string]any{{"validator_key": "test_report"}},
			},
		},
	}
	var wrap struct {
		Data map[string]any `json:"data"`
	}
	apiCall(t, "POST", "/api/v1/agent-plans", map[string]any{"draft": draft, "source": "e2e"}, &wrap)
	return wrap.Data
}

// approvePlan 人工批准 → 冻结分派
func approvePlan(t *testing.T, rev map[string]any) map[string]any {
	t.Helper()
	taskID := rev["task_id"].(string)
	revision := int(rev["revision"].(float64))
	var wrap struct {
		Data any `json:"data"`
	}
	apiCall(t, "POST", fmt.Sprintf("/api/v1/agent-plans/%s/revisions/%d/approve", taskID, revision),
		map[string]any{"approved_by": "e2e"}, &wrap)
	return map[string]any{"task_id": taskID, "revision": revision}
}

// waitTaskTerminal 等任务终态（超时报错）
func waitTaskTerminal(t *testing.T, taskID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var wrap struct {
			Data []e2eTaskView `json:"data"`
		}
		apiCall(t, "GET", "/api/v1/agent-tasks?status=completed", nil, &wrap)
		for _, tk := range wrap.Data {
			if tk.TaskID == taskID {
				return tk.Status
			}
		}
		var failed struct {
			Data []e2eTaskView `json:"data"`
		}
		apiCall(t, "GET", "/api/v1/agent-tasks?status=failed", nil, &failed)
		for _, tk := range failed.Data {
			if tk.TaskID == taskID {
				return tk.Status
			}
		}
		var cancelled struct {
			Data []e2eTaskView `json:"data"`
		}
		apiCall(t, "GET", "/api/v1/agent-tasks?status=cancelled", nil, &cancelled)
		for _, tk := range cancelled.Data {
			if tk.TaskID == taskID {
				return tk.Status
			}
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("task %s not terminal within %s", taskID, timeout)
	return ""
}

// getTaskResult 拉取任务尝试（终态 + 验收）
func getTaskResult(t *testing.T, taskID string) []map[string]any {
	t.Helper()
	var wrap struct {
		Data []map[string]any `json:"data"`
	}
	apiCall(t, "GET", "/api/v1/agent-tasks/"+taskID+"/attempts", nil, &wrap)
	return wrap.Data
}

// assertAuditChain 全链路审计可追溯（设计 §11.2）
func assertAuditChain(t *testing.T, taskID string) {
	t.Helper()
	var wrap struct {
		Data []map[string]any `json:"data"`
	}
	apiCall(t, "GET", "/api/v1/agent-identity-audit", nil, &wrap)
	found := false
	for _, e := range wrap.Data {
		if e["task_id"] == taskID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entries for task %s", taskID)
	}
}

// TestRealZcodeDevelopAndArchive 实施计划 Task 6：Mac Zcode 单 Agent 闭环
// （双步草案退化为单 Agent 步骤执行；跨机部分见 cross_machine_test.go）
func TestRealZcodeDevelopAndArchive(t *testing.T) {
	requireE2EEnvironment(t)
	zcodeID := os.Getenv("MACS_E2E_ZCODE_AGENT_ID")
	before := getAgentSnapshot(t, zcodeID)
	rev := createDraft(t, zcodeID, zcodeID, "E2E: implement fixture change and run tests")
	compiled := approvePlan(t, rev)
	status := waitTaskTerminal(t, compiled["task_id"].(string), 30*time.Minute)
	after := getAgentSnapshot(t, zcodeID)
	if before["agent_id"] != after["agent_id"] {
		t.Fatal("agent identity changed")
	}
	if status != "completed" {
		t.Fatalf("task not completed: %s", status)
	}
	attempts := getTaskResult(t, compiled["task_id"].(string))
	if len(attempts) == 0 {
		t.Fatal("no attempts recorded")
	}
	assertAuditChain(t, compiled["task_id"].(string))
}
