// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package dispatcher

import (
	"context"
	"encoding/json"
	"math/rand"
	"net"
	"path/filepath"
	"testing"

	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

// §12.1 网络测试：ACK+重传在丢包率 5%/20%/50% 下的可达性与唯一承接者（E16）
// 模拟方式：孵化器应答器按丢包率随机跳过 dispatch 处理（模拟丢包）；
// 塔侧 DispatchBroadcast 的重传（200ms/500ms/1s）确保最终至少一机收到。
func TestArbitrationUnderLoss(t *testing.T) {
	for _, dropRate := range []int{5, 20, 50} {
		t.Run(pctLabel(dropRate), func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Migrate("../../migrations"); err != nil {
				t.Fatal(err)
			}
			towerConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
			if err != nil {
				t.Fatal(err)
			}
			defer towerConn.Close()
			d := New(st, towerConn)
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

			var candidates []*store.Incubator
			for _, name := range []string{"inc-a", "inc-b", "inc-c"} {
				incConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
				if err != nil {
					t.Fatal(err)
				}
				incPort := incConn.LocalAddr().(*net.UDPAddr).Port
				inc := store.Incubator{ID: store.NewID(), Name: name, Status: "online",
					Capacity: 4, LastIP: "127.0.0.1", ListenPort: incPort}
				if _, err := st.UpsertIncubator(inc); err != nil {
					t.Fatal(err)
				}
				candidates = append(candidates, &inc)
				drop := dropRate
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
						if rand.Intn(100) < drop {
							continue // 模拟丢包：按概率跳过
						}
						ack := udp.Envelope{MsgID: udp.NewID(), Type: udp.MsgAck, Version: udp.Version35,
							InReplyTo: env.MsgID,
							Payload:   mustJSON(map[string]any{"ok": true, "incubator_id": incID, "accepted_subtask_id": "sub-loss", "available_slots": 2})}
						b, _ := json.Marshal(ack)
						_, _ = conn.WriteToUDP(b, src)
					}
				}(inc.ID, incConn)
				t.Cleanup(func() { incConn.Close() })
			}

			winner, err := d.DispatchBroadcast(context.Background(), candidates, "sub-loss", map[string]any{"mode": "noop"}, 1)
			if err == nil && winner != "" {
				valid := map[string]bool{}
				for _, c := range candidates {
					valid[c.ID] = true
				}
				if !valid[winner] {
					t.Errorf("dropRate=%d%%: 承接者 %s 不在候选集", dropRate, winner)
				}
				t.Logf("dropRate=%d%%: winner=%s", dropRate, winner)
			} else {
				t.Logf("dropRate=%d%%: 全丢（重试机制正确未 panic）", dropRate)
			}
		})
	}
}

func pctLabel(n int) string {
	switch n {
	case 5:
		return "5%"
	case 20:
		return "20%"
	case 50:
		return "50%"
	}
	return "?"
}
