// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// deps.go V1.5 步骤依赖解析与上游制品注入（设计 §6.3）
// 上游步骤完成且验收通过后其制品写入 execution_artifacts；
// 下游调度前按 depends_on 收集、按 input_refs 声明的类型过滤，注入执行消息。
package orchestrator

import (
	"macs.local/tower/agentmsg"
	"macs.local/tower/internal/plan"
	"macs.local/tower/internal/store"
)

// collectUpstreamArtifacts 收集步骤声明的上游制品：
//   - 按 depends_on 逐个上游步骤取 execution_artifacts
//   - 按 input_refs 声明的类型过滤（声明为空 → 不注入任何制品）
func collectUpstreamArtifacts(st *store.Store, taskID string, revision int, s plan.Step) []agentmsg.ArtifactRef {
	if len(s.InputRefs) == 0 || len(s.DependsOn) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, k := range s.InputRefs {
		wanted[k] = true
	}
	var out []agentmsg.ArtifactRef
	for _, dep := range s.DependsOn {
		arts, err := st.ListArtifactsForStep(taskID, dep, revision)
		if err != nil {
			continue
		}
		for _, a := range arts {
			if wanted[a.Kind] {
				out = append(out, agentmsg.ArtifactRef{
					URI:       a.URI,
					Digest:    a.Digest,
					Kind:      a.Kind,
					SizeBytes: a.SizeBytes,
				})
			}
		}
	}
	return out
}
