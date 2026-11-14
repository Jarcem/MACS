// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package unified 模块 #28：统一 Agent 平台协议层（V3.0 §6.4.13）
// 协议驱动注册表 + 平台能力映射 + 消息编解码扩展；内置 openai-compat / anthropic-msg 两族驱动
package unified

import (
	"macs.local/tower/internal/store"
)

// 内置系统平台与驱动（§6.4.13.1 / 6.4.9 能力协商）
var seedPlatforms = []store.AgentPlatform{
	{
		PlatformName: "macs-builtin", Vendor: "jarcem", Version: "1.0",
		ProtocolSpec: "MACS V3.5 信封协议", IsSystem: true,
		DefaultDriver: "cli-native-protocol",
		Capabilities:  `["executor","reviewer","merger"]`,
	},
	{
		PlatformName: "auto-gen", Vendor: "microsoft", Version: "0.2",
		ProtocolSpec: "openai-compat-protocol", IsSystem: true,
		DefaultDriver: "openai-compat-protocol",
		Capabilities:  `["executor","planner"]`,
	},
	{
		PlatformName: "langchain-agents", Vendor: "langchain", Version: "0.1",
		ProtocolSpec: "openai-compat-protocol", IsSystem: true,
		DefaultDriver: "openai-compat-protocol",
		Capabilities:  `["executor","reviewer","planner"]`,
	},
	{
		PlatformName: "crew-ai", Vendor: "crewai", Version: "0.1",
		ProtocolSpec: "openai-compat-protocol", IsSystem: true,
		DefaultDriver: "openai-compat-protocol",
		Capabilities:  `["executor","reviewer"]`,
	},
}

var seedDrivers = []store.ProtocolDriver{
	{
		DriverName: "cli-native-protocol", Version: "1.0", ProtocolFamily: "cli-native",
		Description: "CLI 型适配器（argv 直启），不走 API 能力协商（6.4.9）",
		SpecURL:     "design/MACS技术设计文档.md#649",
	},
	{
		DriverName: "openai-compat-protocol", Version: "1.0", ProtocolFamily: "openai-compatible",
		Description: "OpenAI Chat Completions 及兼容网关（base_url + api_key + model 三要素，SSE 流式，usage 回填 cost_records）",
		SpecURL:     "https://platform.openai.com/docs/api-reference/chat",
	},
	{
		DriverName: "anthropic-msg-protocol", Version: "2023-06-01", ProtocolFamily: "anthropic",
		Description: "Anthropic Messages API（anthropic-version 头协商，system+messages 结构，usage 含缓存 token 回填 cost_records）",
		SpecURL:     "https://docs.anthropic.com/en/api/messages",
	},
}

// Seed 幂等播种系统平台/驱动/映射（仅 is_system 缺失时补插）
func Seed(st *store.Store) error {
	existing, err := st.ListDrivers()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, d := range existing {
		have[d.DriverName+"@"+d.Version] = true
	}
	driverIDs := map[string]string{}
	for _, d := range seedDrivers {
		if have[d.DriverName+"@"+d.Version] {
			list, _ := st.ListDrivers()
			for _, e := range list {
				if e.DriverName == d.DriverName && e.Version == d.Version {
					driverIDs[d.DriverName] = e.ID
				}
			}
			continue
		}
		id, err := st.UpsertDriver(d)
		if err != nil {
			return err
		}
		driverIDs[d.DriverName] = id
	}

	platforms, err := st.ListPlatforms(false)
	if err != nil {
		return err
	}
	haveP := map[string]bool{}
	for _, p := range platforms {
		haveP[p.PlatformName] = true
	}
	for _, p := range seedPlatforms {
		if !haveP[p.PlatformName] {
			p.IsSystem = true
			if _, err := st.UpsertPlatform(p); err != nil {
				return err
			}
		}
	}
	// 平台-驱动映射（§6.4.13.1：auto-gen-to-openai-compat 等 status=ready）
	if err := seedMappings(st, driverIDs); err != nil {
		return err
	}
	return nil
}

func seedMappings(st *store.Store, driverIDs map[string]string) error {
	platforms, _ := st.ListPlatforms(false)
	existing, _ := st.ListPlatformAdapters("")
	pairs := map[string]bool{}
	for _, a := range existing {
		pairs[a.PlatformID+"|"+a.DriverID] = true
	}
	for _, p := range platforms {
		driverID := driverIDs[p.DefaultDriver]
		if driverID == "" || pairs[p.ID+"|"+driverID] {
			continue
		}
		if err := st.UpsertPlatformAdapter(store.PlatformAdapter{
			PlatformID: p.ID, DriverID: driverID,
			AdapterName:       p.PlatformName + "-to-" + p.DefaultDriver,
			Status:            "ready",
			CompatibilityMode: "strict",
			SupportedFeatures: `{"task_dispatch":true,"result_reporting":true,"telemetry":true}`,
		}); err != nil {
			return err
		}
	}
	return nil
}
