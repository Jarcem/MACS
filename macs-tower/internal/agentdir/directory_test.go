// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package agentdir

import (
	"path/filepath"
	"strings"
	"testing"

	"macs.local/tower/internal/store"
)

// V1.5 Task 2：verified 目录只用 verified + enabled action（设计 §4.4）

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedConnector(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.UpsertConnector(store.Connector{ID: id, Name: id + "-dev", CredentialID: "cred-" + id, CredentialHash: "hash-" + id, ProtocolVersion: "1.0"}); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func TestVerifiedCatalogUsesOnlyVerifiedEnabledActions(t *testing.T) {
	st := openStore(t)
	seedConnector(t, st, "c1")
	_, err := st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: "c1", LocalAgentKey: "pending", DisplayName: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Mac Zcode"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(st)
	if _, err := svc.Verify(verified.ID, VerifyPatch{
		DisplayName: "Mac Zcode", PlatformID: "zcode", AdapterType: "cli_agent",
		RoleTags: []string{"developer"}, CapabilityTags: []string{"repo.write", "test.run"},
		EnvironmentTags: []string{"dev"}, ExpectedRevision: 1, Actor: "admin-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertAgentAction(store.AgentAction{ID: "act-disabled", AgentID: verified.ID, ActionKey: "zcode.develop", AdapterType: "cli_agent", ValidatorKeys: []string{"test_report"}, ConfigRef: "zcode", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertAgentAction(store.AgentAction{ID: "act-enabled", AgentID: verified.ID, ActionKey: "zcode.test", AdapterType: "cli_agent", ValidatorKeys: []string{"test_report"}, ConfigRef: "zcode", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	cat, err := svc.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Agents) != 1 || cat.Agents[0].AgentID != verified.ID {
		t.Fatalf("pending agent leaked into catalog: %+v", cat.Agents)
	}
	keys := []string{}
	for _, a := range cat.Agents[0].Actions {
		keys = append(keys, a.ActionKey)
	}
	if !contains(keys, "zcode.test") || contains(keys, "zcode.develop") {
		t.Fatalf("wrong actions: %v", keys)
	}
	// 审计留痕
	audit, err := st.ListIdentityAudit(verified.ID, 10)
	if err != nil || len(audit) == 0 {
		t.Fatalf("audit missing: %+v err=%v", audit, err)
	}
}

// revision 乐观锁：并发标注互相覆盖被拒绝
func TestVerifyRevisionConflict(t *testing.T) {
	st := openStore(t)
	seedConnector(t, st, "c1")
	a, _ := st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Z"})
	svc := NewService(st)
	if _, err := svc.Verify(a.ID, VerifyPatch{DisplayName: "A", PlatformID: "zcode", AdapterType: "cli_agent", ExpectedRevision: 1, Actor: "op1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(a.ID, VerifyPatch{DisplayName: "B", PlatformID: "zcode", AdapterType: "cli_agent", ExpectedRevision: 1, Actor: "op2"}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale revision accepted: %v", err)
	}
	after, _ := st.GetAgentInstance(a.ID)
	if after.DisplayName != "A" {
		t.Fatalf("conflicting patch applied: %s", after.DisplayName)
	}
}

// disable 后退出目录；重复 disable 报错
func TestDisableRemovesFromCatalog(t *testing.T) {
	st := openStore(t)
	seedConnector(t, st, "c1")
	a, _ := st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Z"})
	svc := NewService(st)
	if _, err := svc.Verify(a.ID, VerifyPatch{DisplayName: "A", PlatformID: "zcode", AdapterType: "cli_agent", ExpectedRevision: 1, Actor: "op"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(a.ID, "admin"); err == nil {
		t.Fatal("double disable must fail")
	}
	cat, _ := svc.Catalog()
	if len(cat.Agents) != 0 {
		t.Fatalf("disabled agent still in catalog: %+v", cat.Agents)
	}
}
