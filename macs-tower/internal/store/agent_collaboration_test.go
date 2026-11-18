// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// V1.5 Task 1：候选重连不覆盖人工身份 + 心跳隔离（设计 §11.1 前两行）

func openCollaborationStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAgentCandidateReconnectPreservesVerifiedIdentity(t *testing.T) {
	st := openCollaborationStore(t)
	if err := st.UpsertConnector(Connector{ID: "c1", Name: "mac-dev", CredentialID: "cred-1", CredentialHash: "hash", ProtocolVersion: "1.0"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Zcode", ReportedJSON: `{"adapter":"cli_agent"}`})
	if err != nil {
		t.Fatal(err)
	}
	// 人工标注：直接模拟 verified 目录写回（与 agentdir.Verify 等价的效果）
	if _, err := st.Exec(`UPDATE agent_instances SET identity_status='verified', platform_id='zcode', role_tags='["developer"]', revision=3, verified_by='admin-1', display_name='Mac Zcode' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	// 重连后同名候选再次上报，上报名变更
	second, err := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "renamed-by-report", ReportedJSON: `{"adapter":"http_agent"}`})
	if err != nil {
		t.Fatal(err)
	}
	if second.DisplayName != "Mac Zcode" {
		t.Fatalf("display_name overwritten: %s", second.DisplayName)
	}
	if second.IdentityStatus != "verified" || second.PlatformID != "zcode" || second.Revision != 3 {
		t.Fatalf("identity overwritten: %+v", second)
	}
	// 心跳只改健康/负载/在线状态
	if err := st.UpdateHeartbeat(Heartbeat{ConnectorID: "c1", LocalAgentKey: "zcode", HealthStatus: "degraded", LoadJSON: `{"active":2}`, OnlineStatus: "online"}); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetAgentInstance(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.HealthStatus != "degraded" || after.PlatformID != "zcode" || after.Revision != 3 {
		t.Fatalf("heartbeat changed identity: %+v", after)
	}
	if after.OnlineStatus != "online" || after.LoadJSON != `{"active":2}` {
		t.Fatalf("heartbeat status not applied: %+v", after)
	}
}

// 心跳目标未上报过 → 报错（不静默）
func TestHeartbeatUnknownAgentFails(t *testing.T) {
	st := openCollaborationStore(t)
	err := st.UpdateHeartbeat(Heartbeat{ConnectorID: "ghost", LocalAgentKey: "x", HealthStatus: "ok"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// 冻结分派：批准事务 + 不可变修订 + 迟到结果不覆盖取消终态
func TestApproveFreezesAssignmentsAndLateResultGuard(t *testing.T) {
	st := openCollaborationStore(t)
	if err := st.InsertAgentTask(AgentTask{TaskID: "t1", Title: "dev+deploy", Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	p, err := st.InsertPlanRevision(PlanRevision{TaskID: "t1", Source: "manual", DraftJSON: `{"goal":"g"}`, CatalogDigest: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 1 || p.Status != "draft" {
		t.Fatalf("unexpected revision: %+v", p)
	}
	// 重复提交 → revision 递增
	p2, err := st.InsertPlanRevision(PlanRevision{TaskID: "t1", Source: "manual", DraftJSON: `{"goal":"g2"}`, CatalogDigest: "cat"})
	if err != nil || p2.Revision != 2 {
		t.Fatalf("revision should increment: %+v err=%v", p2, err)
	}
	err = st.ApprovePlanRevision(p, "admin-1", []TaskAssignment{{
		TaskID: "t1", StepID: "s1", PlanRevision: 1,
		AgentID: "a1", ActionID: "act1", Environment: "dev", InputDigest: "sha256:aa",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// 重复批准同一修订 → 拒绝（draft 状态门）
	if err := st.ApprovePlanRevision(p, "admin-1", nil); err == nil {
		t.Fatal("double approve must fail")
	}
	// 迟到结果守卫：取消终态不可覆盖
	at := ExecutionAttempt{AttemptID: NewID(), TaskID: "t1", StepID: "s1", PlanRevision: 1, AttemptNo: 1,
		IdempotencyKey: "idem", AgentID: "a1", ActionID: "act1", Environment: "dev", InputDigest: "sha256:aa"}
	got, err := st.InsertExecutionAttempt(at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetExecutionAttemptStatus(got.AttemptID, "dispatched"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CancelAttemptsForTask("t1"); err != nil || n != 1 {
		t.Fatalf("cancel: n=%d err=%v", n, err)
	}
	changed, err := st.FinishExecutionAttempt(got.AttemptID, "completed", "passed", `{}`)
	if err != nil || changed {
		t.Fatalf("late result overwrote cancelled attempt: changed=%v err=%v", changed, err)
	}
	after, _ := st.GetExecutionAttempt(got.AttemptID)
	if after.Status != "cancelled" {
		t.Fatalf("status changed: %s", after.Status)
	}
}

// 跨机制品落库与按步骤收集（Task 7 持久化面）
func TestArtifactPersistAndCollect(t *testing.T) {
	st := openCollaborationStore(t)
	if err := st.InsertArtifact("t1", "s1", 1, StoredArtifact{ID: NewID(), AttemptID: "att1", URI: "https://store/obj", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "commit", SizeBytes: 10}); err != nil {
		t.Fatal(err)
	}
	arts, err := st.ListArtifactsForStep("t1", "s1", 1)
	if err != nil || len(arts) != 1 || arts[0].Kind != "commit" {
		t.Fatalf("artifacts: %+v err=%v", arts, err)
	}
}
