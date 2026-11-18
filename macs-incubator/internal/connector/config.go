// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package connector V1.5 Connector（设计 §2/§6）
// 只发起出站 WSS 连接（不开放入站端口）；本机 action 配置构造 Worker Job；
// 事件先写本地 sync_outbox，塔侧确认后才标记同步；跨机制品经共享存储交换。
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"macs.local/incubator/internal/worker"
)

// StorageConfig 共享存储上传配置（设计 §7：凭据只存在 Connector 本机）
type StorageConfig struct {
	UploadURL     string            `json:"upload_url"`     // 如 https://store.example.com/objects/{{name}}
	UploadHeaders map[string]string `json:"upload_headers"` // 支持 {{env:NAME}} 引用本机环境
}

// LocalActionConfig 本机动作可执行配置（实际 CLI 参数/URL/凭据引用，设计 §10）
type LocalActionConfig struct {
	ActionKey string `json:"action_key"`
	Adapter   string `json:"adapter"` // cli_agent / http_agent / openai_compatible / anthropic / session_agent
	// cli_agent
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
	// http_agent
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	// openai_compatible / anthropic
	Model     string `json:"model,omitempty"`
	System    string `json:"system,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	// session_agent
	Session *worker.SessionSpec `json:"session,omitempty"`

	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// LocalAgent 本机受管 Agent 候选
type LocalAgent struct {
	LocalAgentKey string              `json:"local_agent_key"`
	DisplayName   string              `json:"display_name"`
	Actions       []LocalActionConfig `json:"actions"`
}

// Config Connector 运行配置（JSON 文件，凭据不出本机）
type Config struct {
	TowerURL       string        `json:"tower_url"` // wss://tower.example.com/agent/connect
	ConnectorID    string        `json:"connector_id"`
	Credential     string        `json:"credential"` // 明文只在本机；链路上只出现 SHA-256
	DBPath         string        `json:"db_path"`
	WorkDir        string        `json:"work_dir"`
	Storage        StorageConfig `json:"storage"`
	HeartbeatEvery int           `json:"heartbeat_seconds"` // 默认 15s
	Agents         []LocalAgent  `json:"agents"`
}

// LoadConfig 从 JSON 文件装配（env 展开 {{env:NAME}}）
func LoadConfig(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.TowerURL == "" || cfg.ConnectorID == "" || cfg.Credential == "" {
		return cfg, fmt.Errorf("connector config: tower_url/connector_id/credential required")
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "data/connector-outbox.db"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "data/connector-work"
	}
	if cfg.HeartbeatEvery <= 0 {
		cfg.HeartbeatEvery = 15
	}
	for i := range cfg.Agents {
		for j := range cfg.Agents[i].Actions {
			a := &cfg.Agents[i].Actions[j]
			a.Command = ExpandEnv(a.Command)
			for k, arg := range a.Args {
				a.Args[k] = ExpandEnv(arg)
			}
			a.Cwd = ExpandEnv(a.Cwd)
			a.URL = ExpandEnv(a.URL)
			for k, v := range a.Headers {
				a.Headers[k] = ExpandEnv(v)
			}
			a.Body = ExpandEnv(a.Body)
		}
	}
	cfg.Storage.UploadURL = ExpandEnv(cfg.Storage.UploadURL)
	for k, v := range cfg.Storage.UploadHeaders {
		cfg.Storage.UploadHeaders[k] = ExpandEnv(v)
	}
	return cfg, nil
}

// ExpandEnv 展开 {{env:NAME}}（凭据经环境变量注入，不进配置文件）
func ExpandEnv(s string) string {
	if !strings.Contains(s, "{{env:") {
		return s
	}
	for {
		start := strings.Index(s, "{{env:")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start:], "}}")
		if end < 0 {
			return s
		}
		end += start
		name := s[start+6 : end]
		val := os.Getenv(name)
		s = s[:start] + val + s[end+2:]
	}
}

// ---------- Registry（实施计划接口基线） ----------

// Registry 本机动作注册表：绑定键 = local_agent_key + "/" + action_key
type Registry struct {
	byBinding map[string]LocalActionConfig
}

func NewRegistry(cfg Config) *Registry {
	r := &Registry{byBinding: map[string]LocalActionConfig{}}
	for _, ag := range cfg.Agents {
		for _, a := range ag.Actions {
			r.byBinding[ag.LocalAgentKey+"/"+a.ActionKey] = a
		}
	}
	return r
}

func (r *Registry) Lookup(agentKey, actionKey string) (LocalActionConfig, bool) {
	c, ok := r.byBinding[agentKey+"/"+actionKey]
	return c, ok
}

// Runner 执行器抽象（实施计划接口基线）；默认 worker.RunWithContext
type Runner interface {
	Run(ctx context.Context, job worker.Job) worker.Outcome
}

type runnerFunc func(ctx context.Context, job worker.Job) worker.Outcome

func (f runnerFunc) Run(ctx context.Context, job worker.Job) worker.Outcome { return f(ctx, job) }

// DefaultRunner 生产线执行器
func DefaultRunner() Runner { return runnerFunc(worker.RunWithContext) }

// BuildJob 由本机 action 配置构造 Worker Job。
// {{instruction}} 占位符展开分派指令；instruction 不作为 shell 命令执行（设计 §5.1）。
func BuildJob(c LocalActionConfig, instruction string, timeoutMs int64) (worker.Job, error) {
	j := worker.Job{TimeoutMs: timeoutMs}
	if c.TimeoutMs > 0 && (j.TimeoutMs == 0 || c.TimeoutMs < j.TimeoutMs) {
		j.TimeoutMs = c.TimeoutMs
	}
	switch c.Adapter {
	case "cli_agent":
		j.Mode = "cli_agent"
		j.Command = c.Command
		for _, arg := range c.Args {
			j.Args = append(j.Args, strings.ReplaceAll(arg, "{{instruction}}", instruction))
		}
		j.Cwd = c.Cwd
		j.Stdin = strings.ReplaceAll(c.Stdin, "{{instruction}}", instruction)
		if j.Command == "" {
			return j, fmt.Errorf("cli_agent action: command required")
		}
	case "http_agent":
		j.Mode = "http_agent"
		j.URL = c.URL
		j.Method = c.Method
		if j.Method == "" {
			j.Method = "POST"
		}
		j.Headers = c.Headers
		j.Body = strings.ReplaceAll(c.Body, "{{instruction}}", instruction)
		if j.URL == "" {
			return j, fmt.Errorf("http_agent action: url required")
		}
	case "openai_compatible", "anthropic":
		j.Mode = c.Adapter
		j.Model = c.Model
		j.Prompt = instruction
		j.System = c.System
		j.MaxTokens = c.MaxTokens
		if j.Model == "" {
			return j, fmt.Errorf("%s action: model required", c.Adapter)
		}
	case "session_agent":
		j.Mode = "session_agent"
		j.Text = instruction
		if c.Session == nil || c.Session.Driver == "" {
			return j, fmt.Errorf("session_agent action: session.driver required")
		}
		j.Session = c.Session
		j.Session.Cwd = ExpandEnv(j.Session.Cwd)
		for k, arg := range j.Session.StartArgs {
			j.Session.StartArgs[k] = strings.ReplaceAll(arg, "{{instruction}}", instruction)
		}
	default:
		return j, fmt.Errorf("unsupported adapter: %s", c.Adapter)
	}
	return j, nil
}
