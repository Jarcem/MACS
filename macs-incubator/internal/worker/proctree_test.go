// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

//go:build !windows

package worker

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// M2.6 §6.4.3.3：进程树终止——Agent 工具内部 spawn 的子进程随组一并清理
// （bash 父进程 + 其后台子进程同处一个进程组，cancel 触发整组 SIGKILL）
func TestProcessTreeTermination(t *testing.T) {
	cmd := exec.Command("bash", "-c", "sleep 30 & sleep 30 & wait")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithTree(ctx, cmd) }()
	time.Sleep(500 * time.Millisecond) // 让 bash 与子 sleep 全部就绪
	cancel()                           // 触发组终止
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("组终止后未返回")
	}

	// 组内进程应全部消失
	out, err := exec.Command("sh", "-c",
		`ps -axo pid,pgid,command | grep "sleep 30" | grep -v grep | grep -v proc-safety | wc -l | tr -d ' '`).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if n := trimSpace(out); n != "0" {
		t.Errorf("进程组终止后仍有 %s 个 sleep 残留", n)
	}
}

func trimSpace(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// M2.6 §6.4.3.3：正常退出路径不受影响（无超时，进程自然结束）
func TestRunWithTreeNormalExit(t *testing.T) {
	cmd := exec.Command("bash", "-c", "echo ok")
	if err := runWithTree(context.Background(), cmd); err != nil {
		t.Fatalf("正常执行不应报错: %v", err)
	}
}
