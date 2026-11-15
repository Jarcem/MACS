// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 会话载体 Worker（§6.4.9 CLI Native 扩展）：把 zcode / trae / DeepSeek Harness /
// claude code / codex 等终端 Agent 作为持久载体——同一任务的子 Worker 在工具的
// 同一 session 内连续孵化（上下文累积、多轮协作），而非一次性调用。
//
// 驱动形态：
//   claude 预设 —— claude -p <prompt> --output-format json（首启）/
//                 claude -p <prompt> --resume <session> --output-format json（续会话）
//   generic    —— 显式 start_args / resume_args 模板（{{text}}/{{deps}}/{{session}} 占位），
//                 覆盖 zcode / dsh / trae 等一切具备「首启 + 会话续传」CLI 的工具
//
// 会话 id 权威在塔侧（tasks.carrier_session 随任务亲和下发），孵化器只负责执行与回执。
package worker

import (
	"context"
	"os"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// SessionSpec 会话载体任务规格（plan 步骤 session 字段；塔侧亲和下发 resume）
type SessionSpec struct {
	Driver        string   `json:"driver"`                 // claude / generic
	Resume        string   `json:"resume,omitempty"`       // 已有会话 id（塔侧下发；空 = 新会话）
	Command       string   `json:"command,omitempty"`      // generic 必填；claude 预设默认 claude
	StartArgs     []string `json:"start_args,omitempty"`   // generic：首启参数模板
	ResumeArgs    []string `json:"resume_args,omitempty"`  // generic：续会话参数模板（{{session}} 占位）
	Output        string   `json:"output,omitempty"`       // json / text（默认 text）
	SessionIDPath string   `json:"session_id_path,omitempty"` // JSON 提取路径（默认 session_id）
	ResultPath    string   `json:"result_path,omitempty"`     // JSON 提取路径（默认 result）
	Cwd           string   `json:"cwd,omitempty"`             // 工作目录（Agent 在此仓库内工作）
	TimeoutMs     int64    `json:"timeout_ms,omitempty"`      // 会话内单步超时（默认 30min，上限 120min）
}

const (
	sessionOutputLimit  = 256 * 1024        // 会话输出截断（JSON 解析需更大余量）
	sessionDefTimeout   = 30 * time.Minute  // 会话单步默认超时
	sessionMaxTimeout   = 120 * time.Minute // 会话单步上限
)

// sessionTimeoutOf 会话单步超时（与 cli_agent 口径独立：编码任务允许更长时间）
func sessionTimeoutOf(ms int64) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return sessionDefTimeout
	}
	if d > sessionMaxTimeout {
		return sessionMaxTimeout
	}
	return d
}

// sessionArgsOf 按驱动装配 argv（claude 预设 / generic 模板），展开占位符
func sessionArgsOf(j Job, sp SessionSpec, resume bool) (command string, args []string) {
	prompt := promptOf(j)
	switch sp.Driver {
	case "claude":
		command = "claude"
		if resume && sp.Resume != "" {
			args = []string{"-p", prompt, "--resume", sp.Resume, "--output-format", "json"}
		} else {
			args = []string{"-p", prompt, "--output-format", "json"}
		}
		return command, args
	default: // generic：zcode / dsh / trae 等显式模板
		command = strings.TrimSpace(sp.Command)
		tpl := sp.StartArgs
		if resume && sp.Resume != "" && len(sp.ResumeArgs) > 0 {
			tpl = sp.ResumeArgs
		}
		for _, a := range tpl {
			a = expand(a, j)
			a = strings.ReplaceAll(a, "{{session}}", sp.Resume)
			args = append(args, a)
		}
		return command, args
	}
}

// jsonPath 按 dot 路径提取 JSON 字段（支持一层 map 嵌套：result.content 等）
func jsonPath(m map[string]any, path string) (string, bool) {
	cur := m
	parts := strings.Split(path, ".")
	for i, k := range parts {
		v, ok := cur[k]
		if !ok {
			return "", false
		}
		if i == len(parts)-1 {
			switch tv := v.(type) {
			case string:
				return tv, true
			default:
				b, _ := json.Marshal(v)
				return string(b), true
			}
		}
		next, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		cur = next
	}
	return "", false
}

// runSession 会话载体执行：新会话（首启）或续会话（塔侧下发 resume）。
// 输出 JSON 时提取 session_id / result；工具无会话能力时每次独立执行（诚实降级）。
func runSession(ctx context.Context, j Job) Outcome {
	if os.Getenv("MACS_ALLOW_SESSION_AGENT") != "1" {
		out := failOutcome("session_agent disabled on incubator: set MACS_ALLOW_SESSION_AGENT=1")
		out.StartedAt, out.FinishedAt = nowStamp(), nowStamp()
		return out
	}
	if strings.TrimSpace(j.Text) == "" && strings.TrimSpace(j.Session.Resume) == "" {
		out := failOutcome("session_agent: empty prompt (text required)")
		out.StartedAt, out.FinishedAt = nowStamp(), nowStamp()
		return out
	}
	sp := *j.Session
	command, args := sessionArgsOf(j, sp, sp.Resume != "")
	if command == "" {
		out := failOutcome("session_agent: command required (driver=generic)")
		out.StartedAt, out.FinishedAt = nowStamp(), nowStamp()
		return out
	}

	runCtx, cancel := context.WithTimeout(ctx, sessionTimeoutOf(sp.TimeoutMs))
	defer cancel()
	cmd := execCommand(runCtx, command, args...)
	if sp.Cwd != "" {
		cmd.Dir = sp.Cwd
	}
	stdout, stderr := &limitBuffer{limit: sessionOutputLimit}, &limitBuffer{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	runErr := runWithTree(runCtx, cmd) // 进程树终止：claude 内部 spawn 的 git/npm 一并清理（M2.6）
	if ctx.Err() != nil {
		return Outcome{} // 回滚
	}

	resumed := sp.Resume != ""
	out := Outcome{Status: "completed"}
	toolOut := stdout.String()
	summary := map[string]any{
		"mode": "session_agent", "driver": sp.Driver, "command": command,
		"resumed": resumed, "duration_ms": time.Since(start).Milliseconds(),
		"truncated": stdout.truncated(),
	}
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		out.Status = "failed"
		out.Error = "session_agent: step timeout"
	case runErr != nil:
		// CLI 非零退出：输出仍回传（部分 Agent 用非零码表达任务内失败，内容有价值）
		out.Status = "failed"
		out.Error = runErr.Error()
	}

	payload := map[string]any{}
	if strings.EqualFold(sp.Output, "json") {
		var m map[string]any
		if err := json.Unmarshal([]byte(toolOut), &m); err == nil {
			sid, _ := jsonPath(m, orDefault(sp.SessionIDPath, "session_id"))
			res, _ := jsonPath(m, orDefault(sp.ResultPath, "result"))
			if sid != "" {
				payload["session_id"] = sid
				summary["session_id"] = sid
			}
			payload["result"] = res
			summary["result_preview"] = preview(res, 200)
		} else {
			summary["json_parse_error"] = true
			payload["result"] = toolOut
		}
	} else {
		payload["result"] = toolOut
	}
	summary["stderr"] = stderr.String()
	payload["session_agent"] = summary
	payload["stdout"] = toolOut
	out.Result = mustJSON(payload)

	out.FinishedAt = nowStamp()
	out.DurationMs = time.Since(start).Milliseconds()
	return out
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
