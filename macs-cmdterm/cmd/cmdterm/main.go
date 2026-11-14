// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// macs-cmdterm 命令终端（模块 #32，V3.4/V3.5）
// 安全模型：仅执行调度塔 Agent 签发的 cmd_exec
//   HMAC-SHA256 签名（ConstantTimeCompare 防时序）+ nonce/60s 时间窗防重放
//   + 命令白名单 + shell 元字符拒绝；全量审计 JSONL；注册 + 60s 心跳（§6.4.14）
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	port        = env("MACS_CMDTERM_PORT", "8472")
	portNum, _  = strconv.Atoi(port)
	towerAddr   = env("MACS_TOWER_HOST", "127.0.0.1") + ":" + env("MACS_TOWER_PORT", "8470")
	secret      = os.Getenv("MACS_CMDTERM_SECRET") // 缺省拒绝一切指令
	terminalID  = env("MACS_CMDTERM_ID", fmt.Sprintf("%d", time.Now().UnixNano()))
	allowRes    = buildAllow(env("MACS_CMDTERM_ALLOW", "systeminfo,ipconfig,dir,type,hostname,whoami"))
	shellMetaRe = regexp.MustCompile(`[|&;><` + "`" + `$]`)
	nonces      = sync.Map{} // nonce → 过期时刻（防重放）
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func buildAllow(list string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, c := range strings.Split(list, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, regexp.MustCompile(`(?i)^`+regexp.QuoteMeta(c)+`(\s|$)`))
		}
	}
	return out
}

type envelope struct {
	MsgID     string          `json:"msg_id"`
	Type      string          `json:"type"`
	Version   string          `json:"version,omitempty"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

type cmdPayload struct {
	Command   string `json:"command"`
	Nonce     string `json:"nonce"`
	Ts        int64  `json:"ts"`
	Signature string `json:"signature"`
}

func sign(v any) string {
	b, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

func verify(p cmdPayload) string {
	switch {
	case secret == "":
		return "no_secret"
	case p.Nonce == "" || p.Ts == 0 || p.Signature == "":
		return "malformed"
	case time.Now().UnixMilli()-p.Ts > 60_000 || p.Ts-time.Now().UnixMilli() > 60_000:
		return "stale"
	}
	if _, dup := nonces.LoadOrStore(p.Nonce, time.Now().Add(2*time.Minute).Unix()); dup {
		return "replay"
	}
	unsigned := struct {
		Command string `json:"command"`
		Nonce   string `json:"nonce"`
		Ts      int64  `json:"ts"`
	}{p.Command, p.Nonce, p.Ts}
	if !hmac.Equal([]byte(sign(unsigned)), []byte(p.Signature)) {
		return "bad_signature"
	}
	if shellMetaRe.MatchString(p.Command) {
		return "shell_metachar"
	}
	for _, re := range allowRes {
		if re.MatchString(p.Command) {
			return ""
		}
	}
	return "not_allowed"
}

func audit(line map[string]any) {
	b, _ := json.Marshal(line)
	f, err := os.OpenFile("cmdterm-audit.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
}

var towerUDP *net.UDPAddr

// sendRegister 发送 cmd_register 并等待塔 ACK（A1：塔回 ACK 即上线）；返回是否确认
func sendRegister(hostname string) bool {
	p := map[string]any{"terminal_id": terminalID, "hostname": hostname, "listen_port": portNum}
	p["ts"] = time.Now().UnixMilli()
	p["nonce"] = fmt.Sprintf("%d", time.Now().UnixNano())
	p["signature"] = sign(p)
	env := envelope{MsgID: p["nonce"].(string), Type: "cmd_register", Version: "3.5"}
	env.Payload, _ = json.Marshal(p)
	conn, err := net.DialUDP("udp4", nil, towerUDP)
	if err != nil {
		return false
	}
	defer conn.Close()
	b, _ := json.Marshal(env)
	if _, err := conn.Write(b); err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}
	var ack envelope
	if json.Unmarshal(buf[:n], &ack) != nil || ack.InReplyTo != env.MsgID {
		return false
	}
	var body struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(ack.Payload, &body)
	return body.OK
}

func sendToTower(msgType string, payload any) {
	if secret == "" {
		return // §6.4.14：未配置密钥拒绝上线
	}
	p := payload.(map[string]any)
	p["ts"] = time.Now().UnixMilli()
	p["nonce"] = fmt.Sprintf("%d", time.Now().UnixNano())
	p["signature"] = sign(p)
	env := envelope{MsgID: p["nonce"].(string), Type: msgType, Version: "3.5"}
	env.Payload, _ = json.Marshal(p)
	b, _ := json.Marshal(env)
	conn, err := net.DialUDP("udp4", nil, towerUDP)
	if err == nil {
		conn.Write(b)
		conn.Close()
	}
}

func main() {
	towerUDP, _ = net.ResolveUDPAddr("udp4", towerAddr)
	// 注册（A1）：每 5s 重试直至收到塔 ACK（§6.4.14），防止注册包恰好丢在塔启动窗口
	host, _ := os.Hostname()
	registered := false
	for attempt := 1; attempt <= 12 && !registered; attempt++ {
		if sendRegister(host) {
			registered = true
			break
		}
		log.Printf("[cmdterm] register attempt %d no ack, retrying", attempt)
		time.Sleep(5 * time.Second)
	}
	if registered {
		log.Printf("[cmdterm] registered id=%s listen=:%s", terminalID, port)
	} else {
		log.Printf("[cmdterm] register exhausted (12 attempts); heartbeats continue, exec will target stale port")
	}
	go func() {
		for range time.Tick(60 * time.Second) {
			sendToTower("cmd_heartbeat", map[string]any{"terminal_id": terminalID})
		}
	}()

	addr, _ := net.ResolveUDPAddr("udp4", ":"+port)
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("[cmdterm] id=%s listening :%s tower=%s", terminalID, port, towerAddr)

	// §6.4.17 C7：cmdterm 审计独立保留 90 天——每日检查，超龄归档轮转
	go func() {
		rotate := func() {
			info, err := os.Stat("cmdterm-audit.jsonl")
			if err != nil || time.Since(info.ModTime()) < 90*24*time.Hour {
				return
			}
			archived := fmt.Sprintf("cmdterm-audit-%s.jsonl", time.Now().UTC().Format("20060102"))
			if err := os.Rename("cmdterm-audit.jsonl", archived); err == nil {
				log.Printf("[cmdterm] audit rotated (90d retention): %s", archived)
			}
		}
		rotate()
		for range time.Tick(24 * time.Hour) {
			rotate()
		}
	}()

	buf := make([]byte, 256*1024)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		var msg envelope
		if json.Unmarshal(buf[:n], &msg) != nil || msg.Type != "cmd_exec" {
			continue // 只受理调度指令
		}
		var p cmdPayload
		_ = json.Unmarshal(msg.Payload, &p)
		entry := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339), "command": p.Command}

		var result map[string]any
		if reject := verify(p); reject != "" {
			entry["result"] = "rejected:" + reject
			result = map[string]any{"ok": false, "reject": reject}
		} else {
			start := time.Now()
			// 平台差异：windows 走 cmd /C，unix 走 sh -c；白名单与元字符校验已在 verify 完成
			var out []byte
			var execErr error
			if runtime.GOOS == "windows" {
				out, execErr = exec.Command("cmd", "/C", p.Command).Output()
			} else {
				out, execErr = exec.Command("sh", "-c", p.Command).Output()
			}
			entry["result"] = map[bool]string{true: "error", false: "ok"}[execErr != nil]
			code := 0
			if ee, ok := execErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if execErr != nil {
				code = 1
			}
			s := string(out)
			if len(s) > 64*1024 {
				s = s[:64*1024] // 附录 B.1：回执截断 64KB
			}
			result = map[string]any{"ok": execErr == nil, "exit_code": code, "stdout": s, "duration_ms": time.Since(start).Milliseconds()}
		}
		audit(entry)
		// 回执一律回塔，不信任来源地址（附录 B.1 cmd_result 单播上行）；terminal_id 供塔定位密钥
		go sendToTower("cmd_result", map[string]any{"terminal_id": terminalID, "in_reply_to": msg.MsgID, "result": result})
	}
}
