// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package cmdterm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// marshalHMAC 与 cmdterm sign() 同构：HMAC(secret, JSON(任意结构))
func marshalHMAC(secret string, v any) string {
	b, _ := json.Marshal(v)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

// 签名契约双向测试：塔侧签发的 cmd_exec 必须能被 cmdterm 的 verify() 校验通过；
// cmdterm 上行的 cmd_result 必须能被塔侧 verifyHMAC 校验通过（§6.4.14 A1/A3）
func TestSignatureContract(t *testing.T) {
	secret := "test-secret-x"

	// ── 下行 cmd_exec：塔签发 → cmdterm 校验 ──
	nonce := "nonce-1"
	ts := timeNowMillis()
	execPayload := map[string]any{"command": "whoami", "nonce": nonce, "ts": ts, "terminal_id": "t1"}
	execPayload["signature"] = hmacSign(secret, map[string]any{"command": "whoami", "nonce": nonce, "ts": ts})
	b, _ := json.Marshal(execPayload)

	// cmdterm verify 逻辑（与 macs-cmdterm/cmd/cmdterm verify 同构）：只重算 {command,nonce,ts}
	var p struct {
		Command   string `json:"command"`
		Nonce     string `json:"nonce"`
		Ts        int64  `json:"ts"`
		Signature string `json:"signature"`
	}
	if json.Unmarshal(b, &p) != nil {
		t.Fatal("unmarshal")
	}
	unsigned := struct {
		Command string `json:"command"`
		Nonce   string `json:"nonce"`
		Ts      int64  `json:"ts"`
	}{p.Command, p.Nonce, p.Ts}
	if marshalHMAC(secret, unsigned) != p.Signature {
		t.Errorf("cmd_exec 签名契约断裂：tower-signed=%s cmdterm-recompute=%s", p.Signature, marshalHMAC(secret, unsigned))
	}

	// ── 上行 cmd_result：cmdterm 签名 → 塔校验 ──
	resultPayload := map[string]any{
		"in_reply_to": "msg-1", "ts": timeNowMillis(),
		"nonce": "nonce-2",
		"result": map[string]any{
			"ok": false, "reject": "bad_signature",
		},
	}
	resultPayload["signature"] = hmacSign(secret, resultPayload)
	rb, _ := json.Marshal(resultPayload)

	// 塔侧 verifyHMAC：解析 → 去 signature → 重marshal → HMAC
	if !verifyHMAC(secret, rb) {
		t.Error("cmd_result 签名契约断裂：塔侧无法验证 cmdterm 上行")
	}
	// 篡改检测
	var m map[string]any
	_ = json.Unmarshal(rb, &m)
	m["result"] = map[string]any{"ok": true, "reject": ""}
	tb, _ := json.Marshal(m)
	if verifyHMAC(secret, tb) {
		t.Error("篡改后的 cmd_result 不应通过校验")
	}
}

func timeNowMillis() int64 { return jsonTime() }

// jsonTime 模拟 UnixMilli（>2^32 的整型，验证 float64 往返不影响签名）
func jsonTime() int64 {
	return 1731666844040
}
