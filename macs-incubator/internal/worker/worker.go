// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package worker 孵化器内执行单元：接到 dispatch 后真正干活并产出结果
// 垂直切片阶段实现 text_analysis（对目标片段做真实统计），后续替换为 agent 运行
package worker

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Dep 依赖子任务的执行结果（由塔在分发 merge 子任务时注入）
type Dep struct {
	SubtaskID string `json:"subtask_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Result    string `json:"result"`
}

type Job struct {
	Mode  string
	Shard string
	Text  string
	Deps  []Dep

	// Agent 接入（cli_agent / http_agent）
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd"`
	Stdin     string            `json:"stdin"`
	TimeoutMs int64             `json:"timeout_ms"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
}

type Outcome struct {
	Status     string `json:"status"` // completed/failed
	Result     string `json:"result"`
	Error      string `json:"error"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMs int64  `json:"duration_ms"`
}

// Run 同步执行一个子任务
func Run(j Job) Outcome {
	start := time.Now()
	out := Outcome{StartedAt: start.UTC().Format(time.RFC3339)}

	switch j.Mode {
	case "text_analysis":
		if j.Text == "" {
			out.Status = "failed"
			out.Error = "empty text"
		} else {
			out.Status = "completed"
			out.Result = analyze(j.Shard, j.Text)
		}
	case "cli_agent":
		out.Status, out.Result, out.Error = apply(runCLI(j))
	case "http_agent":
		out.Status, out.Result, out.Error = apply(runHTTP(j))
	case "merge":
		if len(j.Deps) == 0 {
			out.Status = "failed"
			out.Error = "no dependency results"
		} else if merged, err := merge(j.Deps); err != nil {
			out.Status = "failed"
			out.Error = err.Error()
		} else {
			out.Status = "completed"
			out.Result = merged
		}
	default:
		out.Status = "failed"
		out.Error = fmt.Sprintf("unsupported mode: %s", j.Mode)
	}

	out.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	out.DurationMs = time.Since(start).Milliseconds()
	if out.Status == "" {
		out.Status = "completed"
	}
	return out
}

// apply 只取 agent 执行结果的状态字段，计时仍由 Run 统一填写
func apply(o Outcome) (string, string, string) {
	return o.Status, o.Result, o.Error
}

// mustJSON 序列化信封字段，失败时退回错误文本（不影响任务终态）
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

// analyze 目标片段统计：字符数、有效字符数、去重字符数、高频字符前 3
func analyze(shard, text string) string {
	runes := []rune(text)
	freq := map[string]int{}
	nonSpace := 0
	for _, r := range runes {
		if unicode.IsSpace(r) {
			continue
		}
		nonSpace++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			freq[string(r)]++
		}
	}

	type kv struct {
		Char string `json:"char"`
		N    int    `json:"n"`
	}
	pairs := make([]kv, 0, len(freq))
	for k, v := range freq {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].N == pairs[j].N {
			return pairs[i].Char < pairs[j].Char
		}
		return pairs[i].N > pairs[j].N
	})
	top := pairs
	if len(top) > 3 {
		top = top[:3]
	}

	b, _ := json.Marshal(map[string]any{
		"shard":     shard,
		"chars":     len(runes),
		"non_space": nonSpace,
		"unique":    len(freq),
		"top_chars": top,
	})
	return string(b)
}

// merge 汇总依赖分片结果：累加字符/有效字符/去重数，合并高频字符并重新排序
func merge(deps []Dep) (string, error) {
	type topChar struct {
		Char string `json:"char"`
		N    int    `json:"n"`
	}
	type shardStats struct {
		Shard    string    `json:"shard"`
		Chars    int       `json:"chars"`
		NonSpace int       `json:"non_space"`
		Unique   int       `json:"unique"`
		TopChars []topChar `json:"top_chars"`
	}

	shards := make([]shardStats, 0, len(deps))
	freq := map[string]int{}
	totalChars, totalNonSpace, uniqueSum := 0, 0, 0
	skipped := make([]string, 0)
	for _, d := range deps {
		if d.Status != "" && d.Status != "completed" {
			skipped = append(skipped, d.SubtaskID)
			continue
		}
		var s shardStats
		if err := json.Unmarshal([]byte(d.Result), &s); err != nil {
			skipped = append(skipped, d.SubtaskID)
			continue
		}
		shards = append(shards, s)
		totalChars += s.Chars
		totalNonSpace += s.NonSpace
		uniqueSum += s.Unique
		for _, t := range s.TopChars {
			freq[t.Char] += t.N
		}
	}
	if len(skipped) > 0 {
		return "", fmt.Errorf("dependency not completed: %s", strings.Join(skipped, ","))
	}

	pairs := make([]topChar, 0, len(freq))
	for k, v := range freq {
		pairs = append(pairs, topChar{Char: k, N: v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].N == pairs[j].N {
			return pairs[i].Char < pairs[j].Char
		}
		return pairs[i].N > pairs[j].N
	})
	top := pairs
	if len(top) > 3 {
		top = top[:3]
	}

	b, _ := json.Marshal(map[string]any{
		"merged":          true,
		"shard_count":     len(shards),
		"total_chars":     totalChars,
		"total_non_space": totalNonSpace,
		"unique_sum":      uniqueSum,
		"top_chars":       top,
		"shards":          shards,
	})
	return string(b), nil
}
