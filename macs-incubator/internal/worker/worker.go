// Package worker 孵化器内执行单元：接到 dispatch 后真正干活并产出结果
// 垂直切片阶段实现 text_analysis（对目标片段做真实统计），后续替换为 agent 运行
package worker

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode"
)

type Job struct {
	Mode  string
	Shard string
	Text  string
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
