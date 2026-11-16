// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package plugins 模块 #29：插件化适配器加载（§6.4.12）
// 第三方适配器/连接器以插件形式接入孵化器，与内核进程隔离：
//   独立子进程 + JSON-RPC (stdio) 通信；崩溃不影响主进程，计入 guardrail_events
//   manifest 声明能力（adapter / webhook / cron / tool），越权调用被能力门控拒绝
//   安装后默认 paused，需显式启用；本地路径支持热重载（构建产物变更后 ≤1s 重启）
//   plugins.enabled 门禁（§10.3 默认关）：关闭时仅内置适配器
package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ── 插件清单（manifest）──────────────────────

// Manifest 插件清单（plugins/<name>/manifest.json）
type Manifest struct {
	Name         string   `json:"name"`         // 插件名（目录名必须一致）
	Version      string   `json:"version"`      // 版本
	Type         string   `json:"type"`         // adapter / webhook / cron / tool
	Executable   string   `json:"executable"`   // 相对插件目录的可执行文件路径
	Capabilities []string `json:"capabilities"` // 声明的能力（adapter 等）
	Description  string   `json:"description,omitempty"`
}

// Validate 校验清单必填字段
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("name required")
	}
	if m.Version == "" {
		return fmt.Errorf("version required")
	}
	switch m.Type {
	case "adapter", "webhook", "cron", "tool":
	default:
		return fmt.Errorf("invalid type %q (adapter|webhook|cron|tool)", m.Type)
	}
	if m.Executable == "" {
		return fmt.Errorf("executable required")
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("capabilities required (at least one)")
	}
	return nil
}

// hasCapability 能力门控：插件声明了指定能力才允许调用
func (m *Manifest) hasCapability(cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ── 插件状态 ────────────────────────────────

// State 插件运行状态
type State string

const (
	StatePaused  State = "paused"   // 安装后默认
	StateRunning State = "running"
	StateCrashed State = "crashed"
	StateStopped State = "stopped"
)

// Plugin 一个已加载的插件实例
type Plugin struct {
	Manifest  Manifest
	Dir       string
	State     State
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	startedAt time.Time
	restarts  int
}

// ── 管理器 ──────────────────────────────────

// Manager 插件管理器：扫描 → 加载 → 监控 → 能力门控
type Manager struct {
	mu       sync.Mutex
	dir      string              // 插件根目录（每个子目录一个插件）
	enabled  bool                // plugins.enabled 门禁（§10.3 默认关）
	plugins  map[string]*Plugin  // name → plugin
	lastScan map[string]time.Time // 热重载：构建产物变更检测（mtime）
	stop     chan struct{}
}

// NewManager 创建插件管理器
//   dir: 插件根目录（默认 MACS_PLUGINS_DIR，孵化器工作目录下 plugins/）
//   enabled: plugins.enabled 门禁（MACS_PLUGINS_ENABLED）
func NewManager(dir string, enabled bool) *Manager {
	return &Manager{
		dir:      dir,
		enabled:  enabled,
		plugins:  map[string]*Plugin{},
		lastScan: map[string]time.Time{},
		stop:     make(chan struct{}),
	}
}

// Enabled 插件系统是否启用（§10.3 plugins.enabled）
func (m *Manager) Enabled() bool { return m.enabled }

// Scan 扫描插件目录，加载/更新 manifest；热重载检测可执行文件 mtime 变更
// 门禁关闭时仅扫描不启动（§10.3 关语义：仅内置适配器）
func (m *Manager) Scan() ([]Manifest, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 插件目录不存在 = 无插件
		}
		return nil, err
	}
	var manifests []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(m.dir, e.Name(), "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // 非插件目录
		}
		var mf Manifest
		if json.Unmarshal(data, &mf) != nil || mf.Validate() != nil {
			continue // 清单无效
		}
		if mf.Name != e.Name() {
			continue // manifest.name 必须与目录名一致
		}
		manifests = append(manifests, mf)

		m.mu.Lock()
		existing, ok := m.plugins[mf.Name]
		if !ok {
			m.plugins[mf.Name] = &Plugin{Manifest: mf, Dir: filepath.Join(m.dir, mf.Name), State: StatePaused}
		} else if existing.State != StateRunning {
			existing.Manifest = mf // 清单热更新
		}
		// 热重载：可执行文件 mtime 变更 → 重启插件进程
		if ok && existing.State == StateRunning {
			exePath := filepath.Join(existing.Dir, mf.Executable)
			if info, err := os.Stat(exePath); err == nil {
				if mt, ok2 := m.lastScan[exePath]; ok2 && info.ModTime().After(mt) {
					m.restart(mf.Name) // ≤1s 热重载
				}
				m.lastScan[exePath] = info.ModTime()
			}
		}
		m.mu.Unlock()
	}
	return manifests, nil
}

// Start 启动指定插件（paused → running：spawn 子进程 + JSON-RPC stdio）
// 门禁关闭时拒绝启动（§10.3）
func (m *Manager) Start(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return fmt.Errorf("plugins.enabled is off (set MACS_PLUGINS_ENABLED=1)")
	}
	p, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	if p.State == StateRunning {
		return nil // 已在跑
	}
	return m.start(p)
}

// start 内部启动（调用方持锁）
func (m *Manager) start(p *Plugin) error {
	exePath := filepath.Join(p.Dir, p.Manifest.Executable)
	if _, err := os.Stat(exePath); err != nil {
		return fmt.Errorf("executable not found: %s", exePath)
	}
	cmd := exec.Command(exePath)
	cmd.Dir = p.Dir
	// JSON-RPC stdio 通信：后续增强（当前先保证子进程隔离启动/停止/监控）
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		p.State = StateCrashed
		return fmt.Errorf("start: %w", err)
	}
	p.cmd = cmd
	p.stdin, p.stdout = stdin, stdout
	p.State = StateRunning
	p.startedAt = time.Now()
	m.lastScan[exePath] = time.Now()

	// 崩溃监控：子进程退出 → 计入 guardrail_events（孵化器侧），不影响主进程
	name := p.Manifest.Name
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if p2, ok := m.plugins[name]; ok && p2.State == StateRunning {
			p2.State = StateCrashed
		}
		m.mu.Unlock()
	}()
	return nil
}

// restart 热重载（stop → start）
func (m *Manager) restart(name string) {
	p, ok := m.plugins[name]
	if !ok {
		return
	}
	stopPlugin(p)
	p.State = StateStopped
	m.start(p)
}

// Stop 停止指定插件
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q not found", name)
	}
	if p.State != StateRunning {
		return nil
	}
	stopPlugin(p)
	p.State = StateStopped
	return nil
}

// stopPlugin 统一停止：先关 stdio 管道（否则 Wait 阻塞等拷贝）再 Kill；
// 进程回收由崩溃监控 goroutine 的 cmd.Wait() 独占承担（避免双 Wait 竞争）
func stopPlugin(p *Plugin) {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// HasCapability 能力门控：插件声明了指定能力才允许调用（§6.4.12 越权拒绝）
func (m *Manager) HasCapability(name, cap string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plugins[name]
	return ok && p.Manifest.hasCapability(cap)
}

// Status 返回全部插件状态快照
func (m *Manager) Status() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, p := range m.plugins {
		out = append(out, map[string]any{
			"name":         p.Manifest.Name,
			"version":      p.Manifest.Version,
			"type":         p.Manifest.Type,
			"state":        string(p.State),
			"capabilities": p.Manifest.Capabilities,
			"description":  p.Manifest.Description,
			"restarts":     p.restarts,
		})
	}
	return out
}

// Close 停止全部插件
func (m *Manager) Close() {
	close(m.stop)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.plugins {
		if p.State == StateRunning {
			stopPlugin(p)
		}
		p.State = StateStopped
	}
}
