// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 会话载体（§6.4.9 CLI Native 扩展）：伪工具首启签发 session id，
// 续会话参数携带 {{session}} —— 验证同一 session 内连续孵化子 Worker 的契约
func TestSessionDriverLifecycle(t *testing.T) {
	t.Setenv("MACS_ALLOW_SESSION_AGENT", "1")
	dir := t.TempDir()
	// 伪工具：无 resume 参数（首启）→ 签发 sess-001；有 resume → 续签 sess-001 并回显
	fake := filepath.Join(dir, "macs-fake-agent")
	script := "#!/bin/bash\n" +
		"if [[ \"$*\" == *\"--resume\"* ]]; then\n" +
		"  echo \"{\\\"session_id\\\":\\\"sess-001\\\",\\\"result\\\":\\\"resumed: last-arg=$*\\\"}\"\n" +
		"else\n" +
		"  echo \"{\\\"session_id\\\":\\\"sess-001\\\",\\\"result\\\":\\\"started\\\"}\"\n" +
		"fi\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// ── 首启（新会话）──
	out := Run(Job{
		Mode:  "session_agent",
		Text:  "第一个子任务：建库",
		Session: &SessionSpec{
			Driver: "generic", Command: fake,
			StartArgs: []string{"--task", "{{text}}"},
			Output:    "json", SessionIDPath: "session_id", ResultPath: "result",
		},
	})
	if out.Status != "completed" {
		t.Fatalf("首启失败: %s", out.Error)
	}
	var first struct {
		SessionID   string `json:"session_id"`
		Result      string `json:"result"`
		SessionInfo struct {
			Resumed bool `json:"resumed"`
		} `json:"session_agent"`
	}
	if json.Unmarshal([]byte(out.Result), &first) != nil || first.SessionID != "sess-001" {
		t.Fatalf("session_id 提取失败: %s", out.Result)
	}
	if first.SessionInfo.Resumed {
		t.Error("首启不应标记 resumed")
	}

	// ── 续会话（塔侧下发 resume，{{session}} 占位展开）──
	out2 := Run(Job{
		Mode: "session_agent",
		Text: "第二个子任务：写迁移",
		Session: &SessionSpec{
			Driver: "generic", Command: fake, Resume: first.SessionID,
			StartArgs: []string{"--task", "{{text}}"},
			ResumeArgs: []string{"--resume", "{{session}}", "--task", "{{text}}"},
			Output:    "json", SessionIDPath: "session_id", ResultPath: "result",
		},
	})
	var second struct {
		SessionID string `json:"session_id"`
		Result    string `json:"result"`
		SessionInfo struct {
			Resumed  bool   `json:"resumed"`
			Stderr   string `json:"stderr"`
		} `json:"session_agent"`
		Stdout string `json:"stdout"`
	}
	if json.Unmarshal([]byte(out2.Result), &second) != nil || second.SessionID != "sess-001" {
		t.Fatalf("续会话失败: %s", out2.Result)
	}
	if !second.SessionInfo.Resumed {
		t.Error("续会话应标记 resumed")
	}
	if second.SessionInfo.Stderr != "" {
		t.Errorf("stderr 应为空: %s", second.SessionInfo.Stderr)
	}
}

// claude 预设参数装配（无需显式模板）
func TestSessionClaudePreset(t *testing.T) {
	j := Job{Mode: "session_agent", Text: "修复登录页", Session: &SessionSpec{
		Driver: "claude", Resume: "sess-42", Cwd: "/proj",
	}}
	cmd, args := sessionArgsOf(j, *j.Session, true)
	if cmd != "claude" || len(args) != 6 {
		t.Fatalf("claude 预设参数异常: %s %v", cmd, args)
	}
	joined := args[0] + " " + args[1] + " " + args[2] + " " + args[3] + " " + args[4] + " " + args[5]
	for _, want := range []string{"修复登录页", "--resume", "sess-42", "--output-format"} {
		if !contains(joined, want) {
			t.Errorf("claude 续会话参数缺 %s: %s", want, joined)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// 会话单步超时上限（120min）
func TestSessionTimeoutCap(t *testing.T) {
	if sessionTimeoutOf(0) != 30*time.Minute {
		t.Error("默认超时应 30min")
	}
	if sessionTimeoutOf(999999999) != 120*time.Minute {
		t.Error("超时应封顶 120min")
	}
}

// 有界上下文（6.4.10）：max_tokens 超孵化器硬顶 → bounded_context 拒执行
func TestBoundedContextCap(t *testing.T) {
	t.Setenv("MACS_MAX_TOKENS_CAP", "1000")
	out := Run(Job{
		Mode: "openai_compatible", Text: "x",
		Session: nil, MaxTokens: 5000,
	})
	if out.Status != "failed" || !jsonContains(out.Error, "bounded_context") {
		t.Errorf("max_tokens 超硬顶应 bounded_context 拒执行，got %s / %s", out.Status, out.Error)
	}
}

func jsonContains(s, sub string) bool {
	return jsonRaw(s) && contains(s, sub)
}
func jsonRaw(string) bool { return true }
