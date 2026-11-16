// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package plugins

import (
	"encoding/json"
	"runtime"
	"os"
	"path/filepath"
	"testing"
)

// §6.4.12 插件化适配器加载测试

func writePlugin(t *testing.T, dir, name string, manifest Manifest, createExe bool) {
	pdir := filepath.Join(dir, name)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if createExe {
		exeDir := filepath.Join(pdir, filepath.Dir(manifest.Executable))
		os.MkdirAll(exeDir, 0o755)
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pdir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if createExe {
		exe := filepath.Join(pdir, manifest.Executable)
		// 拷贝 sleep 作为可执行文件（跨平台测试用）
		src := "/bin/sleep"
		if _, err := os.Stat(src); err != nil {
			src = "/usr/bin/sleep"
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Skipf("sleep not found: %v", err)
		}
		if err := os.WriteFile(exe, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	m := Manifest{Name: "x", Version: "1.0", Type: "adapter", Executable: "./bin/x", Capabilities: []string{"adapter"}}
	if err := m.Validate(); err != nil {
		t.Errorf("valid manifest should pass: %v", err)
	}
	m2 := Manifest{Version: "1.0", Type: "adapter", Executable: "./x", Capabilities: []string{"adapter"}}
	if m2.Validate() == nil {
		t.Error("missing name should fail")
	}
	m3 := Manifest{Name: "x", Version: "1.0", Type: "bogus", Executable: "./x", Capabilities: []string{"adapter"}}
	if m3.Validate() == nil {
		t.Error("invalid type should fail")
	}
	m4 := Manifest{Name: "x", Version: "1.0", Type: "adapter", Executable: "./x", Capabilities: []string{}}
	if m4.Validate() == nil {
		t.Error("empty capabilities should fail")
	}
}

func TestCapabilityGating(t *testing.T) {
	m := &Manager{plugins: map[string]*Plugin{
		"adapter-plugin": {Manifest: Manifest{
			Name: "adapter-plugin", Capabilities: []string{"adapter"},
		}, State: StateRunning},
	}}
	if !m.HasCapability("adapter-plugin", "adapter") {
		t.Error("declared capability should pass")
	}
	if m.HasCapability("adapter-plugin", "webhook") {
		t.Error("undeclared capability should be rejected (越权拒绝)")
	}
	if m.HasCapability("nonexistent", "adapter") {
		t.Error("nonexistent plugin should fail")
	}
}

func TestScanFindsPlugins(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "my-adapter", Manifest{
		Name: "my-adapter", Version: "1.0", Type: "adapter",
		Executable: "./bin/agent", Capabilities: []string{"adapter"},
	}, false) // 不创建可执行文件（仅验证扫描）
	m := NewManager(dir, false)
	manifests, err := m.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 1 || manifests[0].Name != "my-adapter" {
		t.Errorf("应发现 1 个插件，got %v", manifests)
	}
	if m.Enabled() {
		t.Error("门禁默认应关闭")
	}
}

func TestDisabledGateRejectsStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sleep not available on Windows (test portability)")
	}
	dir := t.TempDir()
	writePlugin(t, dir, "test-plug", Manifest{
		Name: "test-plug", Version: "1.0", Type: "adapter",
		Executable: "./bin/agent", Capabilities: []string{"adapter"},
	}, true)
	m := NewManager(dir, false) // 门禁关闭
	m.Scan()
	if err := m.Start("test-plug"); err == nil {
		t.Error("plugins.enabled 关闭时应拒绝启动")
	}
}

func TestEnabledGateStartsPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sleep not available on Windows (test portability)")
	}
	dir := t.TempDir()
	writePlugin(t, dir, "ok-plug", Manifest{
		Name: "ok-plug", Version: "1.0", Type: "tool",
		Executable: "./macs-plug", Capabilities: []string{"tool"},
	}, false)
	// 拷贝 sleep 作为可执行文件
	exe := filepath.Join(dir, "ok-plug", "macs-plug")
	src := "/bin/sleep"
	if _, err := os.Stat(src); err != nil {
		src = "/usr/bin/sleep"
	}
	data, _ := os.ReadFile(src)
	if err := os.WriteFile(exe, data, 0o755); err != nil {
		t.Skipf("sleep copy failed: %v", err)
	}

	m := NewManager(dir, true) // 门禁开启
	m.Scan()
	if err := m.Start("ok-plug"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 状态应为 running
	status := m.Status()
	if len(status) != 1 || status[0]["state"] != string(StateRunning) {
		t.Errorf("插件应 running，got %+v", status)
	}
	m.Close()
}
