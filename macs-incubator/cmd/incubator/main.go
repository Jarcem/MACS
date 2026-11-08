// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// macs-incubator 孵化器（M1：模块 #15 UDP 通信 + #16 资源探针 + #17 Reporter）
// 流程：注册（带令牌）→ 收 dispatch 回 ACK → 60s 周期 report（Q2）→ 断网离线缓冲 → 恢复后补传
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"macs.local/incubator/internal/probe"
	"macs.local/incubator/internal/store"
	"macs.local/incubator/internal/worker"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type envelope struct {
	MsgID     string          `json:"msg_id"`
	Type      string          `json:"type"`
	Version   string          `json:"version,omitempty"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

func newEnv(msgType string, payload any) envelope {
	b, _ := json.Marshal(payload)
	return envelope{MsgID: newMsgID(), Type: msgType, Version: "3.5", Payload: b}
}

// newMsgID 生成全局唯一 msg_id：塔侧按 msg_id 做 10min 幂等去重，
// 用 UnixNano 在同时刻连发的消息（如 ACK 与紧接的 execution_result）会撞号被静默丢弃
func newMsgID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func main() {
	tower := env("MACS_TOWER_HOST", "127.0.0.1") + ":" + env("MACS_TOWER_PORT", "8470")
	port := env("MACS_INCUBATOR_PORT", "8473")
	name := env("MACS_INCUBATOR_NAME", "dev-incubator")
	token := os.Getenv("MACS_INCUBATOR_TOKEN")
	maxWorkers, _ := strconv.Atoi(env("MACS_MAX_WORKERS", "4"))
	reportEvery := 60 * time.Second // Q2 默认

	st, err := store.Open(env("MACS_INCUBATOR_DB", "incubator.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	if err := st.Migrate(env("MACS_INCUBATOR_MIGRATIONS", "src/db/migrations")); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	towerAddr, _ := net.ResolveUDPAddr("udp4", tower)
	send := func(env envelope) error {
		conn, err := net.DialUDP("udp4", nil, towerAddr)
		if err != nil {
			return err
		}
		defer conn.Close()
		b, _ := json.Marshal(env)
		_, err = conn.Write(b)
		return err
	}

	// ── 注册（6.4.2，令牌鉴权）；收 ACK 取 incubator_id ──
	portNum, _ := strconv.Atoi(port)
	reg := newEnv("register", map[string]any{
		"name": name, "capacity": maxWorkers, "platforms": []string{}, "drivers": []string{},
		"listen_port": portNum, "token": token, "ts": time.Now().UnixMilli(),
	})
	incubatorID := ""
	for attempt := 1; attempt <= 5; attempt++ {
		conn, err := net.DialUDP("udp4", nil, towerAddr)
		if err == nil {
			b, _ := json.Marshal(reg)
			_, _ = conn.Write(b)
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 4096)
			if n, err := conn.Read(buf); err == nil {
				var ack envelope
				if json.Unmarshal(buf[:n], &ack) == nil {
					var p struct {
						IncubatorID string `json:"incubator_id"`
					}
					_ = json.Unmarshal(ack.Payload, &p)
					incubatorID = p.IncubatorID
				}
			}
			conn.Close()
		}
		if incubatorID != "" {
			break
		}
		log.Printf("[incubator] register attempt %d failed, retrying", attempt)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	log.Printf("[incubator] registered id=%s listen=%s", incubatorID, port)

	// ── 模块 #18 spawner：dispatch → 并发受限的 worker 执行 + 结果回传（M2 垂直切片）──
	sem := make(chan struct{}, maxWorkers)
	var workerSeq int64
	execute := func(subtaskID string, taskDef map[string]any) {
		sem <- struct{}{}
		defer func() { <-sem }()
		log.Printf("[incubator] execute start subtask=%s cap=%d", subtaskID, cap(sem))

		// taskDef 直接落到 Job：内置模式与 cli_agent/http_agent 字段共用同一份下发体
		var job worker.Job
		if b, err := json.Marshal(taskDef); err == nil {
			_ = json.Unmarshal(b, &job)
		}
		log.Printf("[incubator] execute run subtask=%s mode=%q shard=%q len=%d deps=%d", subtaskID, job.Mode, job.Shard, len(job.Text), len(job.Deps))
		if job.Mode == "cli_agent" {
			log.Printf("[incubator] execute cli subtask=%s command=%q args=%d cwd=%q", subtaskID, job.Command, len(job.Args), job.Cwd)
		}
		if job.Mode == "http_agent" {
			log.Printf("[incubator] execute http subtask=%s method=%q url=%q", subtaskID, job.Method, job.URL)
		}
		out := worker.Run(job)
		log.Printf("[incubator] execute done subtask=%s status=%s dur=%dms", subtaskID, out.Status, out.DurationMs)

		payload := map[string]any{
			"subtask_id": subtaskID, "incubator_id": incubatorID,
			"worker_id": fmt.Sprintf("%s/%d", name, atomic.AddInt64(&workerSeq, 1)),
			"status":    out.Status, "result": out.Result, "error": out.Error,
			"started_at": out.StartedAt, "finished_at": out.FinishedAt, "duration_ms": out.DurationMs,
		}
		env := newEnv("execution_result", payload)
		log.Printf("[incubator] execution_result send subtask=%s msg_id=%s -> %s", subtaskID, env.MsgID, towerAddr)
		if err := send(env); err != nil {
			b, _ := json.Marshal(payload)
			st.Buffer("execution_result", string(b)) // 断网缓存，Reporter 补传
			log.Printf("[incubator] execution_result buffered: %v", err)
		} else {
			log.Printf("[incubator] execution_result sent subtask=%s", subtaskID)
		}
	}

	// ── 模块 #15：监听调度下行，dispatch 回 ACK ──
	laddr, _ := net.ResolveUDPAddr("udp4", ":"+port)
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	go func() {
		buf := make([]byte, 256*1024)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			var msg envelope
			if json.Unmarshal(buf[:n], &msg) != nil || msg.MsgID == "" {
				continue
			}
			log.Printf("[incubator] recv type=%s msg_id=%s in_reply_to=%s", msg.Type, msg.MsgID, msg.InReplyTo)
			if msg.Type == "ack" {
				continue // ACK 不再回 ACK，避免 ACK 乒乓风暴
			}
			ack := newEnv("ack", map[string]any{"ok": true})
			ack.InReplyTo = msg.MsgID
			b, _ := json.Marshal(ack)
			_, _ = conn.WriteToUDP(b, src)
			if msg.Type == "dispatch" {
				var p struct {
					SubtaskID string         `json:"subtask_id"`
					TaskDef   map[string]any `json:"task_def"`
				}
				if json.Unmarshal(msg.Payload, &p) == nil && p.SubtaskID != "" {
					log.Printf("[incubator] spawn worker subtask=%s", p.SubtaskID)
					go execute(p.SubtaskID, p.TaskDef)
				}
			}
		}
	}()

	// ── 模块 #17 Reporter：周期上报 + 离线缓冲补传 ──
	for range time.Tick(reportEvery) {
		// 先补传缓冲（fail-open：补传失败继续缓存）
		if pending, _ := st.Pending(); len(pending) > 0 {
			for _, p := range pending {
				if err := send(newEnv(p.EventType, json.RawMessage(p.Payload))); err == nil {
					st.MarkSynced(p.ID)
				}
			}
		}
		payload := map[string]any{"incubator_id": incubatorID, "probe": probe.Collect(maxWorkers), "ts": time.Now().UnixMilli()}
		if err := send(newEnv("report", payload)); err != nil {
			b, _ := json.Marshal(payload)
			st.Buffer("report", string(b)) // 断网缓存（§离线缓冲）
			log.Printf("[incubator] report failed, buffered: %v", err)
		}
	}
}
