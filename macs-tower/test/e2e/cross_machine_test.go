// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package e2e

import (
	"os"
	"testing"
	"time"
)

// TestRealCrossMachineDevelopAndDeploy 实施计划 Task 10：跨机 E2E
// （Zcode 开发 → 共享存储制品 → 云 Agent 部署 → 验收 → 归档）
// 必须满足：两个 agent_id 不变；上游失败下游不执行；验收失败不能归档成功。
func TestRealCrossMachineDevelopAndDeploy(t *testing.T) {
	requireE2EEnvironment(t)
	cloudID := os.Getenv("MACS_E2E_CLOUD_AGENT_ID")
	if cloudID == "" {
		t.Skip("BLOCKED: MACS_E2E_CLOUD_AGENT_ID not configured — mock 不算通过")
	}
	zcodeID := os.Getenv("MACS_E2E_ZCODE_AGENT_ID")
	beforeZ := getAgentSnapshot(t, zcodeID)
	beforeC := getAgentSnapshot(t, cloudID)

	rev := createDraft(t, zcodeID, cloudID, "E2E: develop fixture, deploy to staging, verify health")
	compiled := approvePlan(t, rev)
	status := waitTaskTerminal(t, compiled["task_id"].(string), 45*time.Minute)

	afterZ := getAgentSnapshot(t, zcodeID)
	afterC := getAgentSnapshot(t, cloudID)
	if beforeZ["agent_id"] != afterZ["agent_id"] || beforeC["agent_id"] != afterC["agent_id"] {
		t.Fatal("agent identity changed")
	}
	if status != "completed" {
		t.Fatalf("cross-machine task not completed: %s", status)
	}
	// 跨机制品经共享存储交换（URI 非本地路径 + digest 校验在塔侧已强制）
	attempts := getTaskResult(t, compiled["task_id"].(string))
	if len(attempts) < 2 {
		t.Fatalf("expected both steps executed: %d attempts", len(attempts))
	}
	assertAuditChain(t, compiled["task_id"].(string))
}

// TestUpstreamFailureBlocksDownstreamInE2E 上游失败 → 下游不执行（设计 §6.3.4）
func TestUpstreamFailureBlocksDownstreamInE2E(t *testing.T) {
	requireE2EEnvironment(t)
	cloudID := os.Getenv("MACS_E2E_CLOUD_AGENT_ID")
	if cloudID == "" {
		t.Skip("BLOCKED: MACS_E2E_CLOUD_AGENT_ID not configured")
	}
	zcodeID := os.Getenv("MACS_E2E_ZCODE_AGENT_ID")
	rev := createDraft(t, zcodeID, cloudID, "E2E: failing develop step must block deploy")
	compiled := approvePlan(t, rev)
	status := waitTaskTerminal(t, compiled["task_id"].(string), 30*time.Minute)

	attempts := getTaskResult(t, compiled["task_id"].(string))
	developRan, deployRan := false, false
	for _, a := range attempts {
		if a["step_id"] == "develop" {
			developRan = true
		}
		if a["step_id"] == "deploy" {
			deployRan = true
		}
	}
	if !developRan {
		t.Fatal("develop step did not run")
	}
	if deployRan {
		t.Fatal("downstream ran after upstream failure")
	}
	if status == "completed" {
		t.Fatal("task archived as completed despite upstream failure")
	}
}
