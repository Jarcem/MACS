// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

//go:build windows

// 进程树终止（M2.6 §6.4.3.3）：Windows 实现——taskkill /T /F 按进程树强制终止
package worker

import (
	"os/exec"
	"strconv"
)

// setPgid Windows 无进程组语义（taskkill /T 自行遍历进程树），保持空实现
func setPgid(cmd *exec.Cmd) {}

// killTree 按进程树强制终止（/T 包含子进程，/F 强制）
func killTree(pid int) {
	if pid <= 1 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
