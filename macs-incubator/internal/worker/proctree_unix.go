// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

//go:build !windows

// 进程树终止（M2.6 §6.4.3.3）：Unix 实现——子进程独立进程组（Setpgid），
// 终止时对整组 SIGKILL（Agent 工具内部 spawn 的 git/npm 子进程一并清理）
package worker

import (
	"os/exec"
	"syscall"
)

// setPgid 让子进程成为独立进程组组长（后续可整组终止）
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree 按进程组终止（负 PID = 组内全部进程）
func killTree(pid int) {
	if pid <= 1 {
		return // 防御：绝不 kill init
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
