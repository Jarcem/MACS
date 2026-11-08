// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Agent 接入适配层（M3）：把本机终端 Agent（Zcode / DeepSeek Harness 等）与 HTTP Agent
// 包装成与 text_analysis 同构的执行模式。
//
// 安全边界：
//   - cli_agent 默认关闭，需孵化器显式设置 MACS_ALLOW_CLI_AGENT=1
//   - 命令首 token 必须命中 MACS_AGENT_ALLOW 白名单（逗号分隔的可执行文件名）
//   - 不经过 shell，参数以 argv 数组传递，避免拼接注入
//   - 输出按 stdout 8KB / stderr 32KB 截断后回传
package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	stdoutLimit = 8 * 1024
	stderrLimit = 32 * 1024
	httpLimit   = 8 * 1024
	defTimeout  = 120 * time.Second
	maxTimeout  = 10 * time.Minute
)

func fail(format string, a ...any) Outcome {
	return Outcome{Status: "failed", Error: fmt.Sprintf(format, a...)}
}

func timeoutOf(ms int64) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return defTimeout
	}
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}

// limitBuffer 边写边截断，同时记录丢弃量
type limitBuffer struct {
	buf   bytes.Buffer
	limit int
	total int
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	l.total += len(p)
	if remain := l.limit - l.buf.Len(); remain > 0 {
		if len(p) > remain {
			l.buf.Write(p[:remain])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitBuffer) String() string  { return l.buf.String() }
func (l *limitBuffer) truncated() bool { return l.total > l.limit }

// allowed CLI 白名单：精确匹配可执行文件名（去扩展名、忽略大小写）
func allowed(command string) bool {
	allow := os.Getenv("MACS_AGENT_ALLOW")
	if strings.TrimSpace(allow) == "" {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(command), filepath.Ext(command)))
	for _, a := range strings.Split(allow, ",") {
		a = strings.ToLower(strings.TrimSpace(a))
		a = strings.TrimSuffix(a, ".exe")
		if a != "" && a == base {
			return true
		}
	}
	return false
}

// expand 占位符替换：{{text}} → 任务目标 / 步骤文本，{{deps}} → 依赖结果摘要
func expand(s string, j Job) string {
	s = strings.ReplaceAll(s, "{{text}}", j.Text)
	s = strings.ReplaceAll(s, "{{deps}}", depsSummary(j.Deps))
	return s
}

func depsSummary(deps []Dep) string {
	var b strings.Builder
	for _, d := range deps {
		if b.Len() > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "%s [%s]:\n%s", d.Title, d.Status, d.Result)
	}
	return b.String()
}

// runCLI 以 argv 方式启动本机 Agent 终端，等待退出并回传退出码与截断输出
func runCLI(j Job) Outcome {
	if os.Getenv("MACS_ALLOW_CLI_AGENT") != "1" {
		return fail("cli_agent disabled on incubator: set MACS_ALLOW_CLI_AGENT=1")
	}
	command := strings.TrimSpace(j.Command)
	if command == "" {
		return fail("cli_agent: command required")
	}
	if !allowed(command) {
		return fail("cli_agent: command %q not in MACS_AGENT_ALLOW", command)
	}

	timeout := timeoutOf(j.TimeoutMs)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := make([]string, 0, len(j.Args))
	for _, a := range j.Args {
		args = append(args, expand(a, j))
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if j.Cwd != "" {
		cmd.Dir = j.Cwd
	}
	stdin := expand(j.Stdin, j)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, stderr := &limitBuffer{limit: stdoutLimit}, &limitBuffer{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	runErr := cmd.Run()
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}

	out := Outcome{}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		out.Status = "failed"
		out.Error = fmt.Sprintf("cli_agent: timeout after %s", timeout)
	case runErr != nil:
		out.Status = "failed"
		out.Error = runErr.Error()
	default:
		out.Status = "completed"
	}
	out.Result = mustJSON(map[string]any{
		"mode": "cli_agent", "command": command, "args": args, "cwd": j.Cwd,
		"exit_code": code, "stdout": stdout.String(), "stderr": stderr.String(),
		"duration_ms": time.Since(start).Milliseconds(),
		"truncated":   map[string]bool{"stdout": stdout.truncated(), "stderr": stderr.truncated()},
	})
	return out
}

// runHTTP 调用本机/局域网 Agent 的 HTTP 接口；2xx 视为完成
func runHTTP(j Job) Outcome {
	raw := strings.TrimSpace(j.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fail("http_agent: valid http(s) url required")
	}
	method := strings.ToUpper(strings.TrimSpace(j.Method))
	if method == "" {
		method = http.MethodPost
	}
	body := expand(j.Body, j)
	req, err := http.NewRequest(method, raw, strings.NewReader(body))
	if err != nil {
		return fail("http_agent: %v", err)
	}
	for k, v := range j.Headers {
		req.Header.Set(k, expand(v, j))
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	timeout := timeoutOf(j.TimeoutMs)
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fail("http_agent: %v", err)
	}
	defer resp.Body.Close()
	rb := &limitBuffer{limit: httpLimit}
	_, _ = io.Copy(rb, resp.Body)

	out := Outcome{}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out.Status = "completed"
	} else {
		out.Status = "failed"
		out.Error = fmt.Sprintf("http_agent: status %d", resp.StatusCode)
	}
	out.Result = mustJSON(map[string]any{
		"mode": "http_agent", "url": raw, "method": method, "status_code": resp.StatusCode,
		"body": rb.String(), "duration_ms": time.Since(start).Milliseconds(),
		"truncated": rb.truncated(),
	})
	return out
}
