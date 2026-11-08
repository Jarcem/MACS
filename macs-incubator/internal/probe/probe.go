// Package probe 模块 #16：资源探针（M1 轻量版：runtime 指标，磁盘/GPU 随 M2 扩展）
package probe

import "runtime"

type Metrics struct {
	NumCPU      int   `json:"num_cpu"`
	Goroutines  int   `json:"goroutines"`
	MemAllocMB  int   `json:"mem_alloc_mb"`
	MemSysMB    int   `json:"mem_sys_mb"`
	MaxWorkers  int   `json:"max_workers"`
	ActiveCount int   `json:"active_workers"` // M2 接并发槽后填充真实值
}

func Collect(maxWorkers int) Metrics {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Metrics{
		NumCPU:     runtime.NumCPU(),
		Goroutines: runtime.NumGoroutine(),
		MemAllocMB: int(m.Alloc / 1024 / 1024),
		MemSysMB:   int(m.Sys / 1024 / 1024),
		MaxWorkers: maxWorkers,
	}
}
