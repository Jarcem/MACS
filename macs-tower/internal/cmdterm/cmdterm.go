// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package cmdterm 模块 #32 塔侧：命令终端生命周期（V3.5 §6.4.14）
//   A1 注册与心跳：cmd_register/cmd_heartbeat（HMAC 预共享密钥签名），180s 未收判离线
//   A2 密钥管理：每终端独立密钥（禁止全局一把），登记时生成并一次性展示；
//      塔侧保留新旧密钥以支持轮换 10 分钟双验窗口与 cmd_exec 签发
//   A3 审计对账：塔侧 command_audit 与 cmdterm 本地 JSONL 双写，按 cmd_result.msg_id 比对
package cmdterm

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"macs.local/tower/internal/events"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

const staleAfter = 180 * time.Second // §6.4.14：连续 3 个心跳周期（60s）未收判离线

type Service struct {
	st     *store.Store
	hub    *events.Hub
	conn   *net.UDPConn // 复用网关 socket 下发 cmd_exec
	stop   chan struct{}
	issued sync.Map // 已签发未回执的 cmd_exec：msg_id → issuedCmd
}

type issuedCmd struct {
	TerminalID string
	Command    string
	IssuedAt   time.Time
}

func New(st *store.Store, hub *events.Hub, conn *net.UDPConn) *Service {
	s := &Service{st: st, hub: hub, conn: conn, stop: make(chan struct{})}
	go s.sweeper()
	return s
}

func (s *Service) Close() { close(s.stop) }

// sweeper 离线巡检 + 签发-回执对账缺口告警（A1/A3）
func (s *Service) sweeper() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			ts, err := s.st.ListCommandTerminals()
			if err == nil {
				for _, tm := range ts {
					if tm.Status != "online" || tm.LastHeartbeat == "" {
						continue
					}
					if hb, e := time.Parse(time.RFC3339, tm.LastHeartbeat); e == nil && time.Since(hb) > staleAfter {
						_ = s.st.TouchCommandTerminalOffline(tm.ID)
					}
				}
			}
			s.issued.Range(func(k, v any) bool {
				c := v.(issuedCmd)
				if time.Since(c.IssuedAt) > 60*time.Second {
					s.hub.Publish("command_audit_gap", map[string]any{
						"terminal_id": c.TerminalID, "msg_id": k.(string), "command": c.Command,
						"note": "cmd_exec 签发 60s 未收到 cmd_result（A3 对账缺口告警）",
					})
					s.issued.Delete(k)
				}
				return true
			})
		case <-s.stop:
			return
		}
	}
}

// ── A2 登记（一次性展示密钥；仅 danger scope 端点可调）──

type RegisterResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Secret string `json:"secret"` // 仅本次响应返回，之后不再下发
}

func (s *Service) Register(name string) (*RegisterResult, error) {
	if _, err := s.st.GetCommandTerminalByName(name); err == nil {
		return nil, fmt.Errorf("terminal %q already registered", name)
	}
	secret := randomSecret()
	term := store.CommandTerminal{
		ID: store.NewID(), Name: name, Secret: secret,
	}
	if err := s.st.InsertCommandTerminal(term); err != nil {
		return nil, err
	}
	return &RegisterResult{ID: term.ID, Name: name, Secret: secret}, nil
}

// Rotate A2 轮换（新钥生效，旧钥 10 分钟双验窗口）
func (s *Service) Rotate(id string) (*RegisterResult, error) {
	if _, err := s.st.GetCommandTerminal(id); err != nil {
		return nil, err
	}
	secret := randomSecret()
	if err := s.st.RotateCommandSecret(id, secret); err != nil {
		return nil, err
	}
	return &RegisterResult{ID: id, Secret: secret}, nil
}

func randomSecret() string {
	b := make([]byte, 32)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ── A1 上行处理（gateway 路由）─────────────────

// verifyHMAC 校验上行签名：payload 去掉 signature 后按 map 字典序重建（与 cmdterm 签发侧同构）
func verifyHMAC(secret string, payload []byte) bool {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return false
	}
	got, _ := m["signature"].(string)
	if got == "" {
		return false
	}
	delete(m, "signature")
	b, err := json.Marshal(m)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(got)) // ConstantTimeCompare 防时序（模块 #32 安全模型）
}

// verifyTerminal 新旧密钥双验窗口内任一命中即通过（A2 轮换过渡）
func (s *Service) verifyTerminal(p []byte) (store.CommandTerminal, bool) {
	var head struct {
		TerminalID string `json:"terminal_id"`
	}
	if json.Unmarshal(p, &head) != nil || head.TerminalID == "" {
		return store.CommandTerminal{}, false
	}
	tm, err := s.st.GetCommandTerminal(head.TerminalID)
	if err != nil || tm.Status == "revoked" {
		return tm, false
	}
	if verifyHMAC(tm.Secret, p) {
		return tm, true
	}
	if tm.SecretPrev != "" && tm.SecretPrevUntil != "" {
		if until, e := time.Parse(time.RFC3339, tm.SecretPrevUntil); e == nil && time.Now().UTC().Before(until) {
			if verifyHMAC(tm.SecretPrev, p) {
				return tm, true
			}
		}
	}
	return tm, false
}

// HandleCmdRegister cmd_register 上行（§6.4.14 A1）
func (s *Service) HandleCmdRegister(env udp.Envelope, src *net.UDPAddr) ([]byte, error) {
	tm, ok := s.verifyTerminal(env.Payload)
	if !ok {
		return nil, errors.New("cmd_register: signature verify failed")
	}
	var p struct {
		TerminalID string `json:"terminal_id"`
		Hostname   string `json:"hostname"`
		ListenPort int    `json:"listen_port"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	listenPort := p.ListenPort
	if listenPort <= 0 {
		listenPort = src.Port // 兼容未上报端口的旧终端（临时源端口）
	}
	s.st.TouchCommandTerminal(tm.ID, src.IP.String(), listenPort, p.Hostname)
	s.hub.Publish("cmd_terminal_online", map[string]any{"terminal_id": tm.ID, "name": tm.Name})
	return []byte(`{"ok":true}`), nil
}

// HandleCmdHeartbeat cmd_heartbeat 上行
func (s *Service) HandleCmdHeartbeat(env udp.Envelope, src *net.UDPAddr) ([]byte, error) {
	tm, ok := s.verifyTerminal(env.Payload)
	if !ok {
		return nil, errors.New("cmd_heartbeat: signature verify failed")
	}
	s.st.TouchCommandHeartbeat(tm.ID, src.IP.String())
	return []byte(`{"ok":true}`), nil
}

// HandleCmdResult cmd_result 上行 → command_audit 落库（A3 双写）
func (s *Service) HandleCmdResult(env udp.Envelope, src *net.UDPAddr) ([]byte, error) {
	tm, ok := s.verifyTerminal(env.Payload)
	if !ok {
		return nil, errors.New("cmd_result: signature verify failed")
	}
	var p struct {
		InReplyTo string `json:"in_reply_to"`
		Result    struct {
			OK         bool   `json:"ok"`
			ExitCode   int    `json:"exit_code"`
			Stdout     string `json:"stdout"`
			DurationMs int64  `json:"duration_ms"`
			Reject     string `json:"reject"`
		} `json:"result"`
	}
	if json.Unmarshal(env.Payload, &p) != nil {
		return nil, errors.New("cmd_result: malformed")
	}
	cmd := ""
	if v, found := s.issued.LoadAndDelete(p.InReplyTo); found {
		cmd = v.(issuedCmd).Command
	}
	_ = s.st.InsertCommandAudit(store.CommandAuditRow{
		ID: store.NewID(), TerminalID: tm.ID, Command: cmd,
		OK: p.Result.OK, ExitCode: p.Result.ExitCode, MsgID: p.InReplyTo,
		DurationMs: p.Result.DurationMs, Reject: p.Result.Reject,
	})
	s.st.TouchCommandHeartbeat(tm.ID, src.IP.String())
	s.hub.Publish("cmd_result", map[string]any{
		"terminal_id": tm.ID, "command": cmd, "ok": p.Result.OK, "exit_code": p.Result.ExitCode,
	})
	return []byte(`{"ok":true}`), nil
}

// ── cmd_exec 签发（POST /api/v1/command-terminals/:id/exec，scope=danger）──

type ExecOutcome struct {
	MsgID      string `json:"msg_id"`
	TerminalID string `json:"terminal_id"`
	Command    string `json:"command"`
}

// Exec 签发服务器指令：HMAC-SHA256 + nonce + 60s 时间窗（cmdterm 侧防重放校验）
func (s *Service) Exec(terminalID, command string) (*ExecOutcome, error) {
	tm, err := s.st.GetCommandTerminal(terminalID)
	if err != nil {
		return nil, err
	}
	if tm.Status != "online" {
		return nil, fmt.Errorf("terminal %s not online (status=%s)", tm.Name, tm.Status)
	}
	if tm.LastIP == "" || tm.ListenPort <= 0 {
		return nil, errors.New("terminal address unknown")
	}
	dest := &net.UDPAddr{IP: net.ParseIP(tm.LastIP), Port: tm.ListenPort}

	nonce := udp.NewID()
	// 签名体与 cmdterm verify() 契约严格对齐：{command, nonce, ts}（terminal_id 仅作路由，不入签）
	payload := map[string]any{
		"command": command, "nonce": nonce, "ts": time.Now().UnixMilli(), "terminal_id": tm.ID,
	}
	sig := hmacSign(tm.Secret, map[string]any{"command": command, "nonce": nonce, "ts": payload["ts"]})
	payload["signature"] = sig

	env, err := udp.New(udp.MsgCmdExec, payload)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(env)
	if _, err := s.conn.WriteToUDP(b, dest); err != nil {
		return nil, err
	}
	s.issued.Store(env.MsgID, issuedCmd{TerminalID: tm.ID, Command: command, IssuedAt: time.Now()})

	s.hub.Publish("cmd_exec_issued", map[string]any{
		"terminal_id": tm.ID, "msg_id": env.MsgID, "command": command,
	})
	return &ExecOutcome{MsgID: env.MsgID, TerminalID: tm.ID, Command: command}, nil
}

// hmacSign 与 cmdterm 同构：签名 = HMAC(secret, JSON(payload 不含 signature))
func hmacSign(secret string, payload map[string]any) string {
	b, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
