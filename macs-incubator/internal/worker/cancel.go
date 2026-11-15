// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 取消注册表（M2.6 §6.4.3.3 孵化器侧）：
//   executions   subtask_id → {attempt, cancel}   在跑执行登记
//   cancelled    subtask_id → max_cancelled_attempt 取消墓碑
//
// 规则：
//   dispatch 到达    ：attempt <= 墓碑 → 取消先于调度到达，禁止孵化（duplicate/cancelled 回 ACK）
//   task_cancelled 到达：attempt 匹配在跑项 → 中断执行并抑制结果回传；不匹配/未运行 → 仅更新墓碑
//   执行结束          ：注销登记；迟到 cancel 仅更新墓碑，不得影响新 attempt
//   重复取消          ：幂等（墓碑取 max）
package worker

import "sync"

type CancelRegistry struct {
	mu         sync.Mutex
	executions map[string]*cancelEntry
	tombstones map[string]int64
}

type cancelEntry struct {
	attempt int
	cancel  func()
}

func NewCancelRegistry() *CancelRegistry {
	return &CancelRegistry{
		executions: map[string]*cancelEntry{},
		tombstones: map[string]int64{},
	}
}

// AllowDispatch dispatch 到达检查：attempt 未被取消墓碑覆盖才允许孵化
func (c *CancelRegistry) AllowDispatch(subtaskID string, attempt int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int64(attempt) > c.tombstones[subtaskID]
}

// Register 登记在跑执行（attempt 随 dispatch 下发）
func (c *CancelRegistry) Register(subtaskID string, attempt int, cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executions[subtaskID] = &cancelEntry{attempt: attempt, cancel: cancel}
}

// Deregister 执行结束注销
func (c *CancelRegistry) Deregister(subtaskID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.executions, subtaskID)
}

// OnTaskCancelled task_cancelled 到达：
//   attempt 匹配在跑项 → 调用 cancel 中断，返回 true（孵化器抑制结果回传）
//   不匹配/未运行      → 仅更新墓碑，返回 false
// 幂等：重复取消墓碑取 max，重复中断无害
func (c *CancelRegistry) OnTaskCancelled(subtaskID string, attempt int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(attempt) > c.tombstones[subtaskID] {
		c.tombstones[subtaskID] = int64(attempt)
	}
	if e, ok := c.executions[subtaskID]; ok && e.attempt == attempt {
		if e.cancel != nil {
			e.cancel()
		}
		return true // 在跑被中断：孵化器抑制结果回传
	}
	return false // 未运行或 attempt 不匹配：仅墓碑
}
