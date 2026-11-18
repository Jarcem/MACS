// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package plan

import (
	"encoding/json"
	"strings"
	"testing"

	"macs.local/tower/internal/agentdir"
)

// V1.5 Task 4：禁用字段拒绝（设计 §5.1/§10）
func TestDraftRejectsExecutableFieldsAndInvalidAssignments(t *testing.T) {
	bad := []byte(`{"goal":"x","steps":[{"step_key":"s1","instruction":"do","agent_id":"a1","action_key":"act","command":"rm -rf /"}]}`)
	_, err := DecodeDraft(bad)
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("command field accepted: %v", err)
	}
	nested := []byte(`{"goal":"x","steps":[{"step_key":"s1","instruction":"do","agent_id":"a1","action_key":"act","headers":{"Authorization":"Bearer x"}}]}`)
	_, err = DecodeDraft(nested)
	if err == nil || (!strings.Contains(err.Error(), "headers") && !strings.Contains(err.Error(), "Authorization")) {
		t.Fatalf("nested forbidden field accepted: %v", err)
	}
	cred := []byte(`{"goal":"x","steps":[{"step_key":"s1","instruction":"do","agent_id":"a1","action_key":"act","api_key":"sk-123"}]}`)
	_, err = DecodeDraft(cred)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("api_key accepted: %v", err)
	}
	good := []byte(`{"goal":"x","steps":[{"step_key":"s1","instruction":"do","agent_id":"a1","action_key":"act","acceptance_criteria":[{"validator_key":"test_report"}]}]}`)
	d, err := DecodeDraft(good)
	if err != nil {
		t.Fatalf("clean draft rejected: %v", err)
	}
	if len(d.Steps) != 1 || d.Steps[0].StepKind != "" {
		t.Fatalf("decode wrong: %+v", d)
	}
}

// V1.5 Task 8：DAG 环与缺失依赖拒绝
func TestValidateRejectsCyclicAndMissingDependencies(t *testing.T) {
	cyclic := Draft{Goal: "g", Steps: []Step{
		{StepKey: "a", Instruction: "i", AgentID: "a1", ActionKey: "act", AcceptanceCriteria: []Criterion{{ValidatorKey: "v"}},
			DependsOn: []string{"b"}},
		{StepKey: "b", Instruction: "i", AgentID: "a1", ActionKey: "act", AcceptanceCriteria: []Criterion{{ValidatorKey: "v"}},
			DependsOn: []string{"a"}},
	}}
	if err := Validate(&cyclic, emptyCatalog()); err == nil {
		t.Fatal("cycle accepted")
	}
	missing := Draft{Goal: "g", Steps: []Step{
		{StepKey: "a", Instruction: "i", AgentID: "a1", ActionKey: "act", AcceptanceCriteria: []Criterion{{ValidatorKey: "v"}},
			DependsOn: []string{"none"}},
	}}
	if err := Validate(&missing, emptyCatalog()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing dependency accepted: %v", err)
	}
}

func emptyCatalog() agentdir.Catalog { return agentdir.Catalog{} }

func testCatalog() agentdir.Catalog {
	return agentdir.Catalog{Agents: []agentdir.CatalogAgent{{
		AgentID: "agent-zcode", PlatformID: "zcode", AdapterType: "cli_agent",
		Actions: []agentdir.CatalogAction{{
			ActionID: "act-develop", ActionKey: "zcode.develop", ValidatorKeys: []string{"test_report", "json_field_equals"},
		}},
	}}}
}

func validDraft() *Draft {
	return &Draft{Goal: "dev", TargetEnvironment: "dev", Steps: []Step{{
		StepKey: "develop", Instruction: "implement fixture",
		AgentID: "agent-zcode", ActionKey: "zcode.develop",
		ExpectedOutputs: []string{"commit"},
		AcceptanceCriteria: []Criterion{{ValidatorKey: "test_report"}},
	}}}
}

// 目录联动：verified+enabled 才能通过校验（设计 §5.2）
func TestValidateAgainstCatalog(t *testing.T) {
	if err := Validate(validDraft(), testCatalog()); err != nil {
		t.Fatalf("valid draft rejected: %v", err)
	}
	// 未 verified Agent（不在目录）→ 拒绝
	d := validDraft()
	d.Steps[0].AgentID = "agent-ghost"
	if err := Validate(d, testCatalog()); err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("unverified agent accepted: %v", err)
	}
	// 未登记/禁用 action → 拒绝
	d = validDraft()
	d.Steps[0].ActionKey = "zcode.deploy"
	if err := Validate(d, testCatalog()); err == nil {
		t.Fatal("unregistered action accepted")
	}
	// 验证器不在白名单 → 拒绝
	d = validDraft()
	d.Steps[0].AcceptanceCriteria = []Criterion{{ValidatorKey: "magic_validator"}}
	if err := Validate(d, testCatalog()); err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("unwhitelisted validator accepted: %v", err)
	}
	// 缺验收标准 → 拒绝
	d = validDraft()
	d.Steps[0].AcceptanceCriteria = nil
	if err := Validate(d, testCatalog()); err == nil {
		t.Fatal("missing acceptance criteria accepted")
	}
}

// input_refs 必须来自上游 expected_outputs（设计 §5.2）
func TestValidateInputRefsAgainstUpstream(t *testing.T) {
	d := validDraft()
	d.Steps = append(d.Steps, Step{
		StepKey: "deploy", Instruction: "deploy it",
		AgentID: "agent-zcode", ActionKey: "zcode.develop",
		DependsOn: []string{"develop"}, InputRefs: []string{"image"},
		AcceptanceCriteria: []Criterion{{ValidatorKey: "test_report"}},
	})
	if err := Validate(d, testCatalog()); err == nil || !strings.Contains(err.Error(), "not produced by upstream") {
		t.Fatalf("unproducible input_ref accepted: %v", err)
	}
	d.Steps[1].InputRefs = []string{"commit"}
	if err := Validate(d, testCatalog()); err != nil {
		t.Fatalf("producible input_ref rejected: %v", err)
	}
}

// input_digest 确定性（设计 §3.4）
func TestInputDigestDeterministic(t *testing.T) {
	base := ComputeInputDigest(3, "s1", "act-1", "dev", "  do   it ", []string{"sha256:b", "sha256:a"})
	again := ComputeInputDigest(3, "s1", "act-1", "dev", "do it", []string{"sha256:a", "sha256:b"})
	if base != again {
		t.Fatalf("digest not deterministic: %s vs %s", base, again)
	}
	if base == ComputeInputDigest(4, "s1", "act-1", "dev", "do it", []string{"sha256:a", "sha256:b"}) {
		t.Fatal("revision not in digest")
	}
	if base == ComputeInputDigest(3, "s1", "act-1", "prod", "do it", []string{"sha256:a", "sha256:b"}) {
		t.Fatal("environment not in digest")
	}
	if !strings.HasPrefix(base, "sha256:") {
		t.Fatalf("digest format: %s", base)
	}
}

// MarshalDraft 双保险：构造时携带禁用字段也拒绝
func TestMarshalDraftRejectsForbidden(t *testing.T) {
	d := validDraft()
	d.Steps[0].Instruction = "do"
	// 通过原始 map 注入禁用字段（绕过结构体）
	raw := map[string]any{
		"goal": "x",
		"steps": []map[string]any{{
			"step_key": "s1", "instruction": "i", "agent_id": "a", "action_key": "k",
			"body": "injected",
		}},
	}
	b, _ := json.Marshal(raw)
	if _, err := DecodeDraft(b); err == nil {
		t.Fatal("map-injected forbidden field accepted")
	}
	_ = d
}
