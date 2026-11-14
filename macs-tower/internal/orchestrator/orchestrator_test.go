// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package orchestrator

import (
	"encoding/json"
	"testing"

	"macs.local/tower/internal/store"
)

// §12.1 单元测试：依赖满足算法（DAG 调度前置判定）
func TestDepsCompleted(t *testing.T) {
	a := store.Subtask{ID: "a", Status: "completed"}
	b := store.Subtask{ID: "b", Status: "running"}
	byID := map[string]store.Subtask{"a": a, "b": b}

	cases := []struct {
		name string
		sub  store.Subtask
		want bool
	}{
		{"无依赖直接就绪", store.Subtask{DependsOn: ""}, true},
		{"依赖已完成", store.Subtask{DependsOn: "a"}, true},
		{"依赖未完成", store.Subtask{DependsOn: "b"}, false},
		{"混合依赖取未完成", store.Subtask{DependsOn: "a,b"}, false},
		{"依赖缺失视为未满足", store.Subtask{DependsOn: "ghost"}, false},
	}
	for _, c := range cases {
		if got := depsCompleted(c.sub, byID); got != c.want {
			t.Errorf("%s: depsCompleted=%v want %v", c.name, got, c.want)
		}
	}
}

// §12.1 单元测试：E10 依赖环检测（validatePlan 拒绝指向自身/后序步骤的依赖）
func TestValidatePlanRejectsCycles(t *testing.T) {
	if err := validatePlan([]PlanStep{{
		Mode: "text_analysis", DependsOn: []int{0},
	}}); err == nil {
		t.Error("自依赖应拒绝创建（E10）")
	}
	if err := validatePlan([]PlanStep{
		{Mode: "text_analysis"},
		{Mode: "merge", DependsOn: []int{5}},
	}); err == nil {
		t.Error("依赖后序步骤应拒绝（E10）")
	}
	if err := validatePlan([]PlanStep{
		{Mode: "text_analysis"},
		{Mode: "merge", DependsOn: []int{0}},
	}); err != nil {
		t.Errorf("合法 DAG 不应报错: %v", err)
	}
	if err := validatePlan([]PlanStep{{Mode: "bogus"}}); err == nil {
		t.Error("未支持模式应拒绝")
	}
}

// §12.1 单元测试：6.4.7 评分选机（空槽优先 + 负载均衡 + top-K）
func TestSelectIncubatorsScoring(t *testing.T) {
	o := &Orchestrator{}
	incs := []store.Incubator{
		{ID: "busy", Capacity: 4, ActiveWorkers: 4, LastIP: "10.0.0.1", ListenPort: 8473, Drivers: `[]`},
		{ID: "idle-big", Capacity: 8, ActiveWorkers: 1, LastIP: "10.0.0.2", ListenPort: 8473, Drivers: `[]`},
		{ID: "idle-small", Capacity: 2, ActiveWorkers: 0, LastIP: "10.0.0.3", ListenPort: 8473, Drivers: `[]`},
		{ID: "offline-addr", Capacity: 8, LastIP: "", ListenPort: 0, Drivers: `[]`},
	}
	running := map[string]int{"busy": 4}
	sub := store.Subtask{Payload: `{"mode":"text_analysis"}`}
	cands := o.selectIncubators(incs, running, sub, "t1")
	if len(cands) == 0 {
		t.Fatal("应有可用候选")
	}
	if cands[0].ID != "idle-big" {
		t.Errorf("空槽最多者应排首，got %s", cands[0].ID)
	}
	for _, c := range cands {
		if c.ID == "busy" || c.ID == "offline-addr" {
			t.Errorf("无空槽/无地址节点不应入选: %s", c.ID)
		}
	}
}

// §6.4.13.6 能力协商硬过滤：LLM 子任务只派给带驱动的孵化器
func TestSelectIncubatorsDriverFilter(t *testing.T) {
	o := &Orchestrator{}
	incs := []store.Incubator{
		{ID: "has-llm", Capacity: 4, LastIP: "10.0.0.1", ListenPort: 8473, Drivers: `["openai_compatible@1"]`},
		{ID: "no-llm", Capacity: 8, LastIP: "10.0.0.2", ListenPort: 8473, Drivers: `[]`},
	}
	sub := store.Subtask{Payload: `{"mode":"openai_compatible","model":"gpt-x"}`}
	cands := o.selectIncubators(incs, map[string]int{}, sub, "t1")
	if len(cands) != 1 || cands[0].ID != "has-llm" {
		t.Errorf("能力硬过滤应只剩 has-llm，got %v", cands)
	}
}

// buildPlanSubtasks：依赖索引映射为真实子任务 ID + required_driver 标注
func TestBuildPlanSubtasks(t *testing.T) {
	plan := []PlanStep{
		{Mode: "text_analysis"},
		{Mode: "openai_compatible", Model: "gpt-x", DependsOn: []int{0}},
		{Mode: "merge", DependsOn: []int{0, 1}},
	}
	subs := buildPlanSubtasks("task1", "目标", plan)
	if len(subs) != 3 {
		t.Fatalf("应建 3 个子任务，got %d", len(subs))
	}
	if subs[1].DependsOn != subs[0].ID {
		t.Errorf("依赖应映射为真实 ID，got %s want %s", subs[1].DependsOn, subs[0].ID)
	}
	if subs[1].RequiredDriver != "openai_compatible" {
		t.Errorf("LLM 步骤应标注驱动要求，got %q", subs[1].RequiredDriver)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(subs[2].Payload), &payload); err != nil || payload["mode"] != "merge" {
		t.Errorf("payload 应保留 mode: %v", err)
	}
}
