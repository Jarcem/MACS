// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// macs-incubator 孵化器（模块 #15/#16/#17/#18 + M3 质量 + M6 协议驱动）
// 流程：注册（带令牌与协议驱动能力 §6.4.13.6）→ 收 dispatch
//   → dispatch_inbox 幂等去重（6.2.4：重复消息直接回 ACK 不重复执行）
//   → 回 ACK（accepted_subtask_id + available_slots，§6.4.8 统一 ACK 报文）
//   → dispatch_commit / worker_rollback → 在跑子任务幂等销毁（E16）
//   → 并发槽受限执行 + 自检报告 → execution_result（指纹 + 遥测）上行
//   → 60s 周期 report（Q2）→ 断网离线缓冲 → 恢复后补传
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
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

// localDrivers 能力协商（§6.4.13.6）：按本机 env 装配 协议驱动@版本 列表
func localDrivers() []string {
	drivers := []string{"cli-native-protocol@1.0"}
	if os.Getenv("MACS_ALLOW_CLI_AGENT") == "1" {
		drivers = append(drivers, "cli_agent@1.0")
	}
	if os.Getenv("MACS_ALLOW_SESSION_AGENT") == "1" {
		drivers = append(drivers, "session_agent@1.0")
	}
	if os.Getenv("MACS_OPENAI_API_KEY") != "" {
		drivers = append(drivers, "openai_compatible@1")
	}
	if os.Getenv("MACS_ANTHROPIC_API_KEY") != "" {
		drivers = append(drivers, "anthropic@2023-06-01")
	}
	return drivers
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

	// ── 注册（6.2.2，令牌鉴权 + 能力协商）；收 ACK 取 incubator_id ──
	portNum, _ := strconv.Atoi(port)
	platforms := []string{"macs-builtin"}
	if os.Getenv("MACS_OPENAI_API_KEY") != "" || os.Getenv("MACS_ANTHROPIC_API_KEY") != "" {
		platforms = append(platforms, "openai-compat-protocol", "anthropic-msg-protocol")
	}
	reg := newEnv("register", map[string]any{
		"name": name, "capacity": maxWorkers, "platforms": platforms, "drivers": localDrivers(),
		"listen_port": portNum, "token": token, "ts": time.Now().UnixMilli(),
	})
	incubatorID := ""
	for attempt := 1; attempt <= 12; attempt++ { // 6.2.2：每 5s 重发，最多 12 次
		conn, err := net.DialUDP("udp4", nil, towerAddr)
		if err == nil {
			b, _ := json.Marshal(reg)
			_, _ = conn.Write(b)
			_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
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
		time.Sleep(5 * time.Second)
	}
	log.Printf("[incubator] registered id=%s listen=%s drivers=%v", incubatorID, port, localDrivers())

	// ── 6.4.11 孵化器级预算：budget_changed 仅当 limit_version 更大时应用；超限拒承接新调度 ──
	var budgetMu sync.Mutex
	budgetSuspended := false
	budgetVersion := int64(0)

	applyBudget := func(payload json.RawMessage) {
		var b struct {
			LimitVersion int64 `json:"limit_version"`
			Exceeded     bool  `json:"exceeded"`
		}
		if json.Unmarshal(payload, &b) != nil {
			return
		}
		budgetMu.Lock()
		defer budgetMu.Unlock()
		if b.LimitVersion < budgetVersion {
			return // 塔侧上界仅收紧不放宽：旧版本忽略（6.4.11）
		}
		budgetVersion = b.LimitVersion
		if budgetSuspended != b.Exceeded {
			budgetSuspended = b.Exceeded
			log.Printf("[incubator] budget_changed v%d exceeded=%v → 拒绝承接新调度", b.LimitVersion, b.Exceeded)
		}
	}

	// ── M2.6 §6.4.3.3 取消注册表：dispatch 登记意图，task_cancelled 按 attempt 中断 ──
	cancels := worker.NewCancelRegistry()

	// ── 模块 #18 spawner：并发槽 + 在跑子任务登记（worker_rollback 幂等销毁 E16）──
	sem := make(chan struct{}, maxWorkers)
	var workerSeq int64
	var execMu sync.Mutex
	active := map[string]context.CancelFunc{} // subtask_id → cancel（rollback 用）

	rollback := func(subtaskID string) {
		execMu.Lock()
		cancel, ok := active[subtaskID]
		execMu.Unlock()
		if ok {
			cancel() // 幂等销毁：取消执行上下文，结果不再回传
			log.Printf("[incubator] worker_rollback subtask=%s destroyed", subtaskID)
		}
	}

	execute := func(subtaskID string, taskDef map[string]any, attempt int) {
		sem <- struct{}{}
		defer func() { <-sem }()

		// M2.6 §6.4.3.3：attempt 被取消墓碑覆盖 → 取消先于调度，禁止孵化
		if !cancels.AllowDispatch(subtaskID, attempt) {
			log.Printf("[incubator] dispatch suppressed subtask=%s attempt=%d (cancelled tombstone)", subtaskID, attempt)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		execMu.Lock()
		if _, exists := active[subtaskID]; exists { // dispatch_inbox 已挡重复，双保险
			execMu.Unlock()
			cancel()
			return
		}
		active[subtaskID] = cancel
		cancels.Register(subtaskID, attempt, cancel)
		execMu.Unlock()
		finish := func() {
			execMu.Lock()
			delete(active, subtaskID)
			cancels.Deregister(subtaskID)
			execMu.Unlock()
			cancel()
		}
		defer finish()

		log.Printf("[incubator] execute start subtask=%s cap=%d", subtaskID, cap(sem))
		var job worker.Job
		if b, err := json.Marshal(taskDef); err == nil {
			_ = json.Unmarshal(b, &job)
		}
		if job.Mode == "cli_agent" {
			log.Printf("[incubator] execute cli subtask=%s command=%q args=%d cwd=%q", subtaskID, job.Command, len(job.Args), job.Cwd)
		}
		if job.Mode == "openai_compatible" || job.Mode == "anthropic" {
			log.Printf("[incubator] execute llm subtask=%s driver=%q model=%q", subtaskID, job.Mode, job.Model)
		}

		// 任务遥测起点（§6.4.13.4 task_start）
		_ = send(newEnv("telemetry_stream", map[string]any{
			"subtask_id": subtaskID, "incubator_id": incubatorID, "event_type": "task_start",
			"payload": map[string]any{"mode": job.Mode},
		}))

		out := worker.RunWithContext(ctx, job)
		if out.Status == "" && ctx.Err() != nil {
			return // 被 worker_rollback / dispatch_commit 销毁：不回传结果
		}
		log.Printf("[incubator] execute done subtask=%s status=%s dur=%dms", subtaskID, out.Status, out.DurationMs)

		// ── 自检报告（模块 #23，done 验收门随结果上行）──
		selfCheck := worker.Check(job, out)
		fp := ""
		if out.Status == "completed" {
			fp = worker.Fingerprint(out.Result)
		}
		payload := map[string]any{
			"subtask_id": subtaskID, "incubator_id": incubatorID,
			"worker_id": fmt.Sprintf("%s/%d", name, atomic.AddInt64(&workerSeq, 1)),
			"status":    out.Status, "result": out.Result, "error": out.Error,
			"started_at": out.StartedAt, "finished_at": out.FinishedAt, "duration_ms": out.DurationMs,
			"fingerprint": fp, "self_check": selfCheck,
			"attempt_count": attempt, // M2.6 §6.4.3.2：结果按 attempt 条件核销
		}
		// 会话载体：session_id 回执 → 塔侧 carrier 亲和（同任务后续子 Worker 在同一 session 孵化）
		if job.Session != nil {
			var sr struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal([]byte(out.Result), &sr) == nil && sr.SessionID != "" {
				payload["session_id"] = sr.SessionID
				payload["worker_id"] = fmt.Sprintf("%s/session:%s", name, sr.SessionID)
			}
		}
		// LLM 模式：usage 从结果 JSON 提取 → 遥测回填 cost_records（6.4.9）
		if job.Mode == "openai_compatible" || job.Mode == "anthropic" {
			var r struct {
				TokenUsage *worker.Usage `json:"token_usage"`
			}
			if json.Unmarshal([]byte(out.Result), &r) == nil && r.TokenUsage != nil {
				payload["telemetry_metrics"] = map[string]any{
					"token_usage": r.TokenUsage, "duration_ms": out.DurationMs,
					"agent_platform": job.Mode, "platform_adapter": job.Mode + "-driver",
				}
			}
		}
		env := newEnv("execution_result", payload)
		if err := send(env); err != nil {
			b, _ := json.Marshal(payload)
			st.Buffer("execution_result", string(b)) // 断网缓存，Reporter 补传
			log.Printf("[incubator] execution_result buffered: %v", err)
		}
	}

	// ── 模块 #15：监听调度下行 ──
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
			log.Printf("[incubator] recv type=%s msg_id=%s", msg.Type, msg.MsgID)
			switch msg.Type {
			case "ack":
				continue // ACK 不再回 ACK，避免乒乓风暴
			case "budget_changed":
				applyBudget(msg.Payload) // 状态更新后落入通用 ACK 流程
			case "dispatch_commit":
				// 6.4.3：广播承接锁定通知 → 停止应答、不孵化；在跑则幂等销毁
				var p struct {
					SubtaskID string `json:"subtask_id"`
				}
				_ = json.Unmarshal(msg.Payload, &p)
				if p.SubtaskID != "" {
					rollback(p.SubtaskID)
				}
				continue
			case "task_cancelled":
				// M2.6 §6.4.3.3：attempt 匹配在跑项 → 中断并抑制回传；不匹配/未运行 → 仅墓碑
				var tc struct {
					SubtaskID    string `json:"subtask_id"`
					AttemptCount int    `json:"attempt_count"`
				}
				_ = json.Unmarshal(msg.Payload, &tc)
				if tc.SubtaskID != "" {
					terminated := cancels.OnTaskCancelled(tc.SubtaskID, tc.AttemptCount)
					log.Printf("[incubator] task_cancelled subtask=%s attempt=%d interrupted=%v",
						tc.SubtaskID, tc.AttemptCount, terminated)
				}
				continue // 已按 notice 口径处理；不落入 dispatch 解析
			case "worker_rollback":
				var p struct {
					SubtaskID string `json:"subtask_id"`
				}
				_ = json.Unmarshal(msg.Payload, &p)
				if p.SubtaskID != "" {
					rollback(p.SubtaskID)
				}
				continue
			case "capability_query":
				// §6.4.13.2：返回已注册平台与协议驱动
				ack := newEnv("ack", map[string]any{
					"ok": true, "incubator_id": incubatorID,
					"platforms": platforms, "drivers": localDrivers(),
				})
				ack.InReplyTo = msg.MsgID
				b, _ := json.Marshal(ack)
				_, _ = conn.WriteToUDP(b, src)
				continue
			}

			// dispatch 处理：dispatch_inbox 幂等去重（6.2.4：重复消息直接回 ACK 不重复执行）
			var p struct {
				SubtaskID    string         `json:"subtask_id"`
				TaskDef      map[string]any `json:"task_def"`
				AttemptCount int            `json:"attempt_count"` // M2.6 §6.4.3.1：随载荷下发，结果原样带回
			}
			if json.Unmarshal(msg.Payload, &p) != nil || p.SubtaskID == "" {
				continue
			}
			if p.AttemptCount <= 0 {
				p.AttemptCount = 1 // 兼容旧塔（未携带 attempt）
			}
			dup, _ := st.RecordDispatch(msg.MsgID, msg.Type, string(msg.Payload))
			ackBody := map[string]any{
				"ok": true, "incubator_id": incubatorID,
				"accepted_subtask_id": p.SubtaskID,
				"available_slots":     maxWorkers - len(active),
			}
			if dup {
				ackBody["duplicate"] = true // 幂等回执：已执行过，仅重发 ACK
			}
			budgetMu.Lock()
			suspended := budgetSuspended
			budgetMu.Unlock()
			if !dup && msg.Type == "dispatch" && suspended {
				// 6.4.11：孵化器预算超限 → 拒绝承接新调度（ACK rejected，塔侧改派他机）
				ackBody["ok"] = false
				ackBody["reason"] = "budget_exceeded"
				ackBody["accepted_subtask_id"] = ""
			}
			ack := newEnv("ack", ackBody)
			ack.InReplyTo = msg.MsgID
			b, _ := json.Marshal(ack)
			_, _ = conn.WriteToUDP(b, src)

			if !dup && msg.Type == "dispatch" && !suspended {
				log.Printf("[incubator] spawn worker subtask=%s attempt=%d", p.SubtaskID, p.AttemptCount)
				go execute(p.SubtaskID, p.TaskDef, p.AttemptCount)
			} else if !dup && msg.Type == "dispatch" && suspended {
				log.Printf("[incubator] dispatch rejected subtask=%s (budget_exceeded)", p.SubtaskID)
			}
		}
	}()

	// ── 模块 #17 Reporter：周期上报 + 离线缓冲补传（6.4.5 指数退避 + Q6 14 天清理）──
	probe.SetActiveProvider(func() int { return len(active) })
	for range time.Tick(reportEvery) {
		// 先补传退避到期的缓冲（fail-open：补传失败按指数退避顺延，不丢数据）
		if due, _ := st.DuePending(); len(due) > 0 {
			for _, p := range due {
				if err := send(newEnv(p.EventType, json.RawMessage(p.Payload))); err == nil {
					st.MarkSynced(p.ID)
				} else {
					st.MarkFailed(p.ID)
				}
			}
		}
		st.PurgeExpired()
		m := probe.Collect(maxWorkers)
		payload := map[string]any{
			"incubator_id": incubatorID, "probe": m, "ts": time.Now().UnixMilli(),
			"capacity": map[string]any{
				"max_workers": maxWorkers, "active_workers": m.ActiveCount,
				"available_slots": maxWorkers - m.ActiveCount,
			},
			"load": m.LoadRatio, // 负载水位（6.4.7 评分依据，probe 已算好）
			"drivers": localDrivers(),
		}
		if err := send(newEnv("report", payload)); err != nil {
			b, _ := json.Marshal(payload)
			st.Buffer("report", string(b)) // 断网缓存（§离线缓冲）
			log.Printf("[incubator] report failed, buffered: %v", err)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
