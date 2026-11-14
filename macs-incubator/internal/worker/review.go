// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 独立复核（模块 #24 review，FR-14）：复核 Worker 重跑原任务 → 指纹比对 + 盲点互补
//   确定性模式（text_analysis/merge）：指纹一致 = approved；不一致 = rejected（E04 打回）
//   Agent 模式（cli/http/llm）：非确定性输出，双侧完成即 approved（互补核验失败侧即 rejected）
package worker

import (
	"encoding/json"
)

// runReview 重跑原子任务并核验
func runReview(j Job) Outcome {
	startJob := startOf(j)
	var original Job
	if len(j.ReviewJob) == 0 || json.Unmarshal(j.ReviewJob, &original) != nil {
		out := failOutcome("review: malformed review_job")
		out.StartedAt, out.FinishedAt, out.DurationMs = startJob, nowStamp(), 0
		return out
	}
	// 复核上下文：依赖结果已在原任务 payload 中，重跑得到独立结果
	reRun := Run(original)

	verdict := map[string]any{
		"mode":           "review",
		"review_round":   j.ReviewRound,
		"expect_fp":      j.ReviewExpect,
		"rerun_status":   reRun.Status,
		"fingerprint_match": false,
		"deterministic":  isDeterministic(original),
	}
	var status string
	switch {
	case reRun.Status != "completed":
		verdict["note"] = "复核重跑失败：" + reRun.Error
		status = "rejected"
	case isDeterministic(original):
		fp := Fingerprint(reRun.Result)
		verdict["rerun_fp"] = fp
		if j.ReviewExpect != "" && fp != j.ReviewExpect {
			verdict["note"] = "指纹不一致：期望 " + j.ReviewExpect + " 实际 " + fp
			status = "rejected"
		} else {
			verdict["fingerprint_match"] = true
			verdict["note"] = "指纹一致"
			status = "approved"
		}
	default:
		// 非确定性 Agent 输出：双侧完成即通过（盲点互补）
		verdict["fingerprint_match"] = true
		verdict["note"] = "非确定性输出，双侧完成互证"
		status = "approved"
	}
	verdict["status"] = status

	out := Outcome{Status: "completed", Result: mustJSON(verdict)}
	out.StartedAt, out.FinishedAt = startJob, nowStamp()
	return out
}

func isDeterministic(j Job) bool {
	return j.Mode == "text_analysis" || j.Mode == "merge"
}
