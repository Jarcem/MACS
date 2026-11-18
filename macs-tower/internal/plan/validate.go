// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// validate.go V1.5 确定性校验（设计 §5.2）
//   - JSON 结构与字段完整、步骤数/超时限制
//   - DAG 无循环，depends_on 引用存在
//   - 所有 agent_id 已 verified；action 已登记且 enabled；验证器在白名单内
//   - input_refs 声明的制品类型必须能从上游步骤的 expected_outputs 获得
package plan

import (
	"fmt"

	"macs.local/tower/internal/agentdir"
)

// Validate 对草案做确定性校验（目录 = verified Agent + enabled action 快照）
func Validate(d *Draft, catalog agentdir.Catalog) error {
	if d == nil {
		return fmt.Errorf("draft is nil")
	}
	if d.Goal == "" {
		return fmt.Errorf("goal required")
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("steps required")
	}
	if len(d.Steps) > MaxSteps {
		return fmt.Errorf("too many steps: %d > %d", len(d.Steps), MaxSteps)
	}
	if d.TimeoutMS < 0 || d.TimeoutMS > MaxStepTimeout*4 {
		return fmt.Errorf("timeout out of range")
	}
	if d.TargetEnvironment != "" && d.TargetEnvironment == "" {
		return fmt.Errorf("target_environment blank")
	}

	byKey := map[string]*Step{}
	agents := map[string]agentdir.CatalogAgent{}
	for _, a := range catalog.Agents {
		agents[a.AgentID] = a
	}

	for i := range d.Steps {
		s := &d.Steps[i]
		if s.StepKey == "" {
			return fmt.Errorf("step[%d]: step_key required", i)
		}
		if _, dup := byKey[s.StepKey]; dup {
			return fmt.Errorf("duplicate step_key %q", s.StepKey)
		}
		byKey[s.StepKey] = s
	}

	for i := range d.Steps {
		s := &d.Steps[i]
		if s.Instruction == "" {
			return fmt.Errorf("step %q: instruction required", s.StepKey)
		}
		if s.StepKind == "" {
			s.StepKind = "execute"
		}
		if s.StepKind != "execute" {
			return fmt.Errorf("step %q: step_kind %q not supported in V1.5", s.StepKey, s.StepKind)
		}
		if s.TimeoutMS < 0 || s.TimeoutMS > MaxStepTimeout {
			return fmt.Errorf("step %q: timeout out of range", s.StepKey)
		}
		// 依赖引用存在
		seen := map[string]bool{}
		for _, dep := range s.DependsOn {
			if _, ok := byKey[dep]; !ok {
				return fmt.Errorf("step %q: depends_on %q not found", s.StepKey, dep)
			}
			if dep == s.StepKey {
				return fmt.Errorf("step %q: self dependency", s.StepKey)
			}
			if seen[dep] {
				return fmt.Errorf("step %q: duplicate dependency %q", s.StepKey, dep)
			}
			seen[dep] = true
		}
		// Agent verified（目录只含 verified，缺席即未 verified/不存在）
		agent, ok := agents[s.AgentID]
		if !ok {
			return fmt.Errorf("step %q: agent %q not verified or unknown", s.StepKey, s.AgentID)
		}
		// action enabled + 验证器白名单
		var action *agentdir.CatalogAction
		for j := range agent.Actions {
			if agent.Actions[j].ActionKey == s.ActionKey {
				action = &agent.Actions[j]
				break
			}
		}
		if action == nil {
			return fmt.Errorf("step %q: action %q not registered/enabled on agent %q", s.StepKey, s.ActionKey, s.AgentID)
		}
		if len(s.AcceptanceCriteria) == 0 {
			return fmt.Errorf("step %q: acceptance_criteria required", s.StepKey)
		}
		for _, c := range s.AcceptanceCriteria {
			if !contains(action.ValidatorKeys, c.ValidatorKey) {
				return fmt.Errorf("step %q: validator %q not whitelisted on action %q", s.StepKey, c.ValidatorKey, s.ActionKey)
			}
		}
		// input_refs ⊆ 上游 expected_outputs（设计 §5.2）
		upstream := map[string]bool{}
		for _, dep := range s.DependsOn {
			for _, out := range byKey[dep].ExpectedOutputs {
				upstream[out] = true
			}
		}
		for _, ref := range s.InputRefs {
			if !upstream[ref] {
				return fmt.Errorf("step %q: input_ref %q not produced by upstream steps", s.StepKey, ref)
			}
		}
	}

	// DAG 无环（DFS 三色标记）
	color := map[string]int{} // 0 white 1 gray 2 black
	for k := range byKey {
		if err := dfsVisit(k, byKey, color); err != nil {
			return err
		}
	}
	return nil
}

func dfsVisit(key string, byKey map[string]*Step, color map[string]int) error {
	switch color[key] {
	case 1:
		return fmt.Errorf("cyclic dependency detected at step %q", key)
	case 2:
		return nil
	}
	color[key] = 1
	for _, dep := range byKey[key].DependsOn {
		if err := dfsVisit(dep, byKey, color); err != nil {
			return err
		}
	}
	color[key] = 2
	return nil
}

// DepsReady 所有依赖步骤已验收通过
func DepsReady(s Step, passed map[string]bool) bool {
	for _, dep := range s.DependsOn {
		if !passed[dep] {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
