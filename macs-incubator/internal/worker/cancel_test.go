// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package worker

import "testing"

// M2.6 §6.4.3.3 取消注册表：墓碑/匹配中断/幂等全契约
func TestCancelRegistry(t *testing.T) {
	c := NewCancelRegistry()
	terminated := 0
	c.Register("s-1", 2, func() { terminated++ })

	// attempt 未被墓碑覆盖 → 允许孵化
	if !c.AllowDispatch("s-1", 2) {
		t.Fatal("无墓碑应允许孵化")
	}
	// 取消：attempt 匹配在跑项 → 中断 + 墓碑
	if c.OnTaskCancelled("s-1", 2) != true {
		t.Fatal("attempt 匹配应中断")
	}
	if terminated != 1 {
		t.Fatalf("cancel 应被调用，got %d", terminated)
	}
	// 执行结束：从注册表注销（孵化器 execute defer 语义）
	c.Deregister("s-1")
	// 重复取消：幂等（墓碑取 max，已注销后仅墓碑不中断）
	if c.OnTaskCancelled("s-1", 2) {
		t.Fatal("已注销后仅墓碑，不应再中断")
	}
	if c.OnTaskCancelled("s-1", 1) {
		t.Fatal("旧 attempt 取消只更新墓碑")
	}
	// 取消先于调度到达：attempt <= 墓碑 → 禁止孵化
	if c.AllowDispatch("s-1", 2) {
		t.Error("墓碑覆盖的 attempt 应禁止孵化")
	}
	if !c.AllowDispatch("s-1", 3) {
		t.Error("新 attempt（大于墓碑）应允许孵化")
	}
	// 未运行子任务的取消：仅墓碑
	if c.OnTaskCancelled("s-9", 1) {
		t.Error("未运行取消不应返回 true")
	}
	if c.AllowDispatch("s-9", 1) {
		t.Error("墓碑应阻止孵化")
	}
}
