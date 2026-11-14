// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package templates 模块 #13：Agent 模板管理（FR-22 §2.9）
// 模板 CRUD + 版本管理 + 系统模板播种；M3 阶段先本地模板（模板市场为后续迭代 §3.3）
package templates

import (
	"macs.local/tower/internal/store"
)

var seedTemplates = []store.AgentTemplate{
	{
		Name: "builtin-executor", Version: "1.0", TemplateType: "executor", AdapterType: "builtin",
		Description: "内置拆解执行器：text_analysis / merge 确定性模式",
		ProtocolDriver: "none",
		InstructionsMD: "接收 dispatch task_def，按 mode 执行并产出 JSON 结果；自检通过后交复核。",
		Capabilities:   `["text_analysis","merge"]`,
		IsSystem:       true,
	},
	{
		Name: "cli-agent-executor", Version: "1.0", TemplateType: "executor", AdapterType: "cli_agent",
		Description: "终端 CLI Agent（Zcode / DeepSeek Harness），argv 直启 + 白名单（6.4.9 CLI Native）",
		ProtocolDriver: "none",
		InstructionsMD: "MACS_ALLOW_CLI_AGENT=1 且命令首 token 命中 MACS_AGENT_ALLOW；{{text}}/{{deps}} 占位替换。",
		Capabilities:   `["cli_agent"]`,
		IsSystem:       true,
	},
	{
		Name: "openai-compat-executor", Version: "1.0", TemplateType: "executor", AdapterType: "openai_compatible",
		Description: "OpenAI Chat Completions 兼容协议驱动（孵化器凭 MACS_OPENAI_* 注入凭据）",
		ProtocolDriver: "openai_compatible",
		DriverVersionPin: "1.0",
		InstructionsMD: "按 base_url+api_key+model 三要素请求；usage 回填 cost_records（6.4.9）。",
		Capabilities:   `["openai_compatible"]`,
		IsSystem:       true,
	},
	{
		Name: "anthropic-executor", Version: "1.0", TemplateType: "executor", AdapterType: "anthropic",
		Description: "Anthropic Messages API 驱动（anthropic-version: 2023-06-01）",
		ProtocolDriver: "anthropic",
		DriverVersionPin: "2023-06-01",
		InstructionsMD: "system+messages 结构；usage 含缓存 token 回填 cost_records（6.4.9）。",
		Capabilities:   `["anthropic"]`,
		IsSystem:       true,
	},
	{
		Name: "independent-reviewer", Version: "1.0", TemplateType: "reviewer", AdapterType: "builtin",
		Description: "独立复核 Worker：重跑原任务并比对指纹（盲点互补 FR-14）",
		ProtocolDriver: "none",
		InstructionsMD: "review 模式：按 review_job 重执行，fingerprint_match 判定 approved/rejected。",
		Capabilities:   `["review"]`,
		IsSystem:       true,
	},
	{
		Name: "merger", Version: "1.0", TemplateType: "merger", AdapterType: "builtin",
		Description: "合并 Worker：多分片结果汇总（concat/structured 策略，FR-15）",
		ProtocolDriver: "none",
		InstructionsMD: "merge 模式：聚合依赖分片，冲突计数上报 merge_result。",
		Capabilities:   `["merge"]`,
		IsSystem:       true,
	},
}

// Seed 幂等播种系统模板
func Seed(st *store.Store) error {
	existing, err := st.ListTemplates(false)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.Name+"@"+t.Version] = true
	}
	for _, t := range seedTemplates {
		if have[t.Name+"@"+t.Version] {
			continue
		}
		t.IsSystem = true
		if _, err := st.UpsertTemplate(t); err != nil {
			return err
		}
	}
	return nil
}
