// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package probe 模块 #16：资源探针（runtime 指标 + 并发槽活动数；磁盘/GPU 随后续扩展）
package probe

import "runtime"

// activeProvider 由孵化器主循环注入（并发槽在跑数）
var activeProvider func() int

// SetActiveProvider 注入在跑 Worker 数提供者（6.2.3 heartbeat active_workers）
func SetActiveProvider(fn func() int) { activeProvider = fn }

type Metrics struct {
	NumCPU      int     `json:"num_cpu"`
	Goroutines  int     `json:"goroutines"`
	MemAllocMB  int     `json:"mem_alloc_mb"`
	MemSysMB    int     `json:"mem_sys_mb"`
	MaxWorkers  int     `json:"max_workers"`
	ActiveCount int     `json:"active_workers"`
	LoadRatio   float64 `json:"load_ratio"` // 0~1（6.4.7 负载评分依据）
}

func Collect(maxWorkers int) Metrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	active := 0
	if activeProvider != nil {
		active = activeProvider()
	}
	ratio := 0.0
	if maxWorkers > 0 {
		ratio = float64(active) / float64(maxWorkers)
	}
	return Metrics{
		NumCPU:      runtime.NumCPU(),
		Goroutines:  runtime.NumGoroutine(),
		MemAllocMB:  int(m.Alloc / 1024 / 1024),
		MemSysMB:    int(m.Sys / 1024 / 1024),
		MaxWorkers:  maxWorkers,
		ActiveCount: active,
		LoadRatio:   ratio,
	}
}
