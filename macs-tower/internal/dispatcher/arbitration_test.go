// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package dispatcher

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

// §12.1 网络测试（回环真实 UDP）：广播 first-ACK-wins 并发仲裁——
// 多候选同时应答时必须产生唯一承接者（E16 并发承接正确性）。
// ACK 泵：towerConn 收到的 ACK 实时喂给 HandleAck，复现生产网关路径。

func TestBroadcastArbitrationSingleWinner(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	towerConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("tower listen: %v", err)
	}
	defer towerConn.Close()

	d := New(st, towerConn)

	// ACK 泵：复现网关路径（收到 ACK → HandleAck 仲裁）
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := towerConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var env udp.Envelope
			if json.Unmarshal(buf[:n], &env) == nil && env.Type == udp.MsgAck {
				d.HandleAck(env)
			}
		}
	}()

	// 三个候选孵化器：注册 + ACK 竞速应答器（无延迟并发应答）
	names := []string{"inc-a", "inc-b", "inc-c"}
	var candidates []*store.Incubator
	for _, name := range names {
		incConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("%s listen: %v", name, err)
		}
		incPort := incConn.LocalAddr().(*net.UDPAddr).Port
		inc := store.Incubator{ID: store.NewID(), Name: name, Status: "online",
			Capacity: 4, LastIP: "127.0.0.1", ListenPort: incPort}
		if _, err := st.UpsertIncubator(inc); err != nil {
			t.Fatalf("register: %v", err)
		}
		candidates = append(candidates, &inc)
		// 竞速应答器：收到 dispatch 立即回 ACK（最大化并发窗口）；
		// incubator_id 必须用 UUID（仲裁表按候选集 UUID 键控，防冒领）
		go func(incID string, conn *net.UDPConn) {
			buf := make([]byte, 65536)
			for {
				n, src, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				var env udp.Envelope
				if json.Unmarshal(buf[:n], &env) != nil || env.Type != udp.MsgDispatch {
					continue
				}
				ack := udp.Envelope{
					MsgID: udp.NewID(), Type: udp.MsgAck, Version: udp.Version35,
					InReplyTo: env.MsgID,
					Payload: mustJSON(map[string]any{
						"ok": true, "incubator_id": incID,
						"accepted_subtask_id": "sub-arb", "available_slots": 2,
					}),
				}
				b, _ := json.Marshal(ack)
				_, _ = conn.WriteToUDP(b, src)
			}
		}(inc.ID, incConn)
		t.Cleanup(func() { incConn.Close() })
	}

	// 广播下发：first-ACK-wins 必须产生唯一承接者
	winner, err := d.DispatchBroadcast(context.Background(), candidates, "sub-arb", map[string]any{"mode": "noop"}, 1)
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if winner == "" {
		t.Fatal("无承接者")
	}
	valid := map[string]bool{}
	for _, c := range candidates {
		valid[c.ID] = true
	}
	if !valid[winner] {
		t.Errorf("承接者 %s 不在候选集", winner)
	}
	// dispatch_log：该次广播应已锁定 acked（唯一承接）
	rows, _ := st.ListDispatchLog(10)
	acked := 0
	for _, r := range rows {
		t.Logf("dispatch_log: subtask=%s status=%s", r.SubtaskID, r.AckStatus)
		if r.SubtaskID == "sub-arb" && r.AckStatus == "acked" {
			acked++
		}
	}
	if acked != 1 {
		t.Errorf("dispatch_log 应恰有一条 acked，got %d", acked)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
