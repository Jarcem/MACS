// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// contract_test.go V1.5 真实契约测试（实施计划 Task 6）
// Zcode 契约测试必须用真实本机 action 配置（MACS_CONNECTOR_CONFIG），mock 不算通过。
// 设置 MACS_RUN_REAL_AGENT_CONTRACT=1 才运行；否则记 BLOCKED（skip）。
package connector

import (
	"context"
	"os"
	"testing"
	"time"

	"macs.local/incubator/internal/worker"
)

func requireRealContractEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("MACS_RUN_REAL_AGENT_CONTRACT") != "1" {
		t.Skip("BLOCKED: MACS_RUN_REAL_AGENT_CONTRACT != 1（真实契约测试需显式开启）")
	}
	if os.Getenv("MACS_CONNECTOR_CONFIG") == "" {
		t.Skip("BLOCKED: MACS_CONNECTOR_CONFIG not set（需要管理员提供的真实本机 action 配置）")
	}
}

// TestRealRegisteredAgentActions 真实本机 action 配置可加载、注册表绑定完整、
// 真实 Runner 可执行（走真实 zcode/部署命令；不注入任何 mock Runner）
func TestRealRegisteredAgentActions(t *testing.T) {
	requireRealContractEnv(t)
	cfg, err := LoadConfig(os.Getenv("MACS_CONNECTOR_CONFIG"))
	if err != nil {
		t.Fatalf("real config load: %v", err)
	}
	reg := NewRegistry(cfg)
	if len(cfg.Agents) == 0 {
		t.Fatal("real config has no agents")
	}
	bound := 0
	for _, ag := range cfg.Agents {
		for _, act := range ag.Actions {
			if _, ok := reg.Lookup(ag.LocalAgentKey, act.ActionKey); !ok {
				t.Fatalf("binding missing: %s/%s", ag.LocalAgentKey, act.ActionKey)
			}
			bound++
		}
	}
	if bound == 0 {
		t.Fatal("real config has no actions")
	}
	// 真实执行冒烟：取第一个动作，以最小指令真实执行（超时 10 分钟）
	ag := cfg.Agents[0]
	act := ag.Actions[0]
	job, err := BuildJob(act, "契约冒烟：输出一行 JSON {\"summary\":\"contract-ok\",\"self_check\":{\"status\":\"passed\"}}", 10*60*1000)
	if err != nil {
		t.Fatalf("build job: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out := worker.RunWithContext(ctx, job)
	if out.Status != "completed" {
		t.Fatalf("real action %s/%s failed: %s (err=%s, result=%s)", ag.LocalAgentKey, act.ActionKey, out.Status, out.Error, truncate(out.Result, 500))
	}
}
