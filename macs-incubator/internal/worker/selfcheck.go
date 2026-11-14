// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 自检引擎（模块 #23 self-check，FR-13）：验收项逐项核验 + 结构化报告
// done 验收门（6.4.10）：strict 下自检 failed 不得提交复核，塔侧按 E01 重试
package worker

import (
	"encoding/json"
	"time"
)

func startOf(j Job) string { return time.Now().UTC().Format(time.RFC3339) }
func nowStamp() string     { return time.Now().UTC().Format(time.RFC3339) }

// SelfCheck 自检报告（随 execution_result 内嵌上行）
type SelfCheck struct {
	Status  string            `json:"status"` // passed/failed/partial
	Score   float64           `json:"score"`
	Items   []SelfCheckItem   `json:"items"`
	Summary string            `json:"summary"`
}

type SelfCheckItem struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Note   string `json:"note"`
}

// Check 对执行结果做逐项核验（模式感知的最小验收集）
func Check(j Job, out Outcome) SelfCheck {
	items := []SelfCheckItem{
		{Name: "execution_status", Passed: out.Status == "completed", Note: out.Error},
	}
	passed := 0
	for _, it := range items {
		if it.Passed {
			passed++
		}
	}

	if out.Status == "completed" {
		var probe map[string]any
		jsonOK := json.Unmarshal([]byte(out.Result), &probe) == nil && len(probe) > 0
		structured := SelfCheckItem{Name: "result_structured", Passed: jsonOK, Note: "结果应为非空 JSON"}
		items = append(items, structured)
		if structured.Passed {
			passed++
		}
		switch j.Mode {
		case "text_analysis":
			ok := probe["chars"] != nil && probe["non_space"] != nil && probe["top_chars"] != nil
			item := SelfCheckItem{Name: "analysis_fields", Passed: ok, Note: "chars/non_space/top_chars 必填"}
			items = append(items, item)
			if ok {
				passed++
			}
		case "merge":
			ok := probe["merged"] == true && probe["shard_count"] != nil
			item := SelfCheckItem{Name: "merge_fields", Passed: ok, Note: "merged/shard_count 必填"}
			items = append(items, item)
			if ok {
				passed++
			}
		case "cli_agent", "http_agent":
			// 截断信息存在性：保证塔侧可见输出边界
			ok := probe["truncated"] != nil
			items = append(items, SelfCheckItem{Name: "truncation_reported", Passed: ok, Note: "截断标记必填"})
			if ok {
				passed++
			}
		}
	}

	status := "passed"
	switch {
	case passed == len(items):
	case passed == 0:
		status = "failed"
	default:
		status = "partial"
	}
	score := 0.0
	if len(items) > 0 {
		score = float64(passed) / float64(len(items))
	}
	summary := "自检通过"
	if status != "passed" {
		summary = "自检未全通过：" + out.Error
	}
	return SelfCheck{Status: status, Score: score, Items: items, Summary: summary}
}
