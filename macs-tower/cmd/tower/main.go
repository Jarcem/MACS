// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// macs-tower 调度塔入口
// M1：模块 #1 注册认证 + #2 UDP 网关 + #3 广播调度 + #4 集群管理
// M2：模块 #5/#6/#7 任务编排 + DAG 调度
// M3：模块 #9/#10/#11 审批/追溯/归档 + 质量管线（复核/抽查）
// M5：模块 #12/#13 成本/模板
// M6：模块 #28/#30 统一协议 + 遥测
// M8：模块 #32 塔侧命令终端
package main

import (
	"context"
	"fmt"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"macs.local/tower/internal/approvals"
	"macs.local/tower/internal/archive"
	"macs.local/tower/internal/authz"
	"macs.local/tower/internal/billing"
	"macs.local/tower/internal/cluster"
	"macs.local/tower/internal/cmdterm"
	"macs.local/tower/internal/dispatcher"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/gateway"
	"macs.local/tower/internal/orchestrator"
	"macs.local/tower/internal/quality"
	"macs.local/tower/internal/registry"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/templates"
	"macs.local/tower/internal/trace"
	"macs.local/tower/internal/udp"
	"macs.local/tower/internal/unified"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(os.Getenv(key))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func jlog(level, module, msg string) {
	b, _ := json.Marshal(map[string]string{
		"ts": time.Now().UTC().Format(time.RFC3339), "level": level, "module": module, "msg": msg,
	})
	log.Println(string(b))
}

// apiErr §6.4.15 统一错误响应：{code, message, details}
func apiErr(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "details": map[string]any{}})
}

func main() {
	// ── 存储（V3.4：SQLite 默认；PG 经 DATABASE_URL 预留）──
	st, err := store.Open(env("MACS_TOWER_DB", "macs-tower.db"))
	if err != nil {
		log.Fatalf("store open: %v", err)
	}
	if err := st.Migrate(env("MACS_MIGRATIONS_DIR", "migrations")); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	// ── UDP 网关（模块 #2）──
	udpPort := env("MACS_TOWER_UDP_PORT", "8470")
	addr, _ := net.ResolveUDPAddr("udp4", ":"+udpPort)
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatalf("udp listen: %v", err)
	}
	gw := gateway.New(conn)
	disp := dispatcher.New(st, conn)
	cm := cluster.New(st)
	reg := registry.New(st, os.Getenv("MACS_REGISTER_TOKEN")) // 空=开发模式
	hub := events.New()

	// ── M3：追溯链 / 归档 / 审批 / 质量 / 成本 ──
	tr := trace.New(st)
	arch := archive.New(st)
	appr := approvals.New(st, hub)
	cfg := orchestrator.Config{
		BroadcastEnabled: envBool("MACS_BROADCAST_ENABLED", true), // 10.3 udp.broadcast_mode.enabled
		SubtaskTTL:       time.Duration(envInt("MACS_SUBTASK_TTL_MIN", 30)) * time.Minute,
		MaxRetries:       envInt("MACS_MAX_RETRIES", 3),
		RetryBaseBackoff: 30 * time.Second,
		RebalanceEnabled: envBool("MACS_REBALANCE_ENABLED", true),
	}
	qualCfg := quality.Config{
		ReviewEnabled:    envBool("MACS_FEATURE_REVIEW", true),
		SpotCheckEnabled: envBool("MACS_FEATURE_SPOT_CHECK", false), // 10.3 默认 false
		SpotCheckRatio:   envInt("MACS_SPOT_CHECK_RATIO", 20),        // Q7 默认 20%
		MaxReviewRounds:  envInt("MACS_MAX_REVIEW_ROUNDS", 3),        // Q5
	}
	bill := billing.New(st, hub, appr)
	var orch *orchestrator.Orchestrator // 声明在前：quality 的 kick 闭包引用编排器
	qual := quality.New(st, hub, tr, arch, appr, qualCfg, func() {
		if orch != nil {
			orch.Notify()
		}
	})
	orch = orchestrator.New(st, disp, hub, tr, arch, qual, bill, cfg)

	// ── M6/M5：统一协议与模板播种（幂等）──
	if err := unified.Seed(st); err != nil {
		log.Fatalf("seed unified protocol: %v", err)
	}
	if err := templates.Seed(st); err != nil {
		log.Fatalf("seed templates: %v", err)
	}

	// ── M8：塔侧命令终端 ──
	ct := cmdterm.New(st, hub, conn)

	// 审批回调接线（状态机副作用唯一入口）
	appr.On(approvals.TypeHumanFinal,
		func(a store.Approval) { orch.OnHumanFinal(a.TaskID, true) },
		func(a store.Approval) { orch.OnHumanFinal(a.TaskID, false) })
	appr.On(approvals.TypeBudgetExceed,
		func(a store.Approval) { orch.OnBudgetDecision(a.TaskID, true) }, nil)
	appr.On(approvals.TypeGuardrail,
		func(a store.Approval) { orch.OnGuardrailDecision(a.SubtaskID, true) },
		func(a store.Approval) { orch.OnGuardrailDecision(a.SubtaskID, false) })
	// 6.4.3 approval_result 下行（审批关联孵化器定向通知）
	appr.SetDownlinkNotifier(func(incubatorID string, payload map[string]any) {
		disp.NotifyIncubator(incubatorID, udp.MsgApprovalResult, payload)
	})
	appr.StartExpireSweeper()

	orch.Start()
	cm.StartSweeper()
	disp.StartJanitor() // 6.5 ACK 兜底：塔重启后卡住的 dispatch 行 E13 标失败、子任务回池
	defer cm.Close()
	defer appr.Close()
	defer ct.Close()

	gw.On(udp.MsgRegister, reg.Handle) // 模块 #1：注册落库
	gw.On(udp.MsgAck, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		disp.HandleAck(e) // 模块 #3：唤醒 ACK 等待（§6.4.8 仲裁）
		return nil, nil
	})
	gw.On(udp.MsgReport, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		// 6.4.3 handleReport：capacity/load 落库供负载均衡评分（6.4.7）
		var p struct {
			IncubatorID string  `json:"incubator_id"`
			Load        float64 `json:"load"`
			Probe       struct {
				NumCPU     int `json:"num_cpu"`
				MemAllocMB int `json:"mem_alloc_mb"`
				MemSysMB   int `json:"mem_sys_mb"`
			} `json:"probe"`
			Capacity struct {
				MaxWorkers    int `json:"max_workers"`
				ActiveWorkers int `json:"active_workers"`
			} `json:"capacity"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.IncubatorID != "" {
			capacity := p.Capacity.MaxWorkers
			if capacity <= 0 {
				if inc, err := st.GetIncubator(p.IncubatorID); err == nil {
					capacity = inc.Capacity
				}
			}
			st.TouchWithLoad(p.IncubatorID, capacity, p.Capacity.ActiveWorkers, p.Load)
			_ = cm.SaveResourceSnapshot(store.IncubatorResource{
				IncubatorID: p.IncubatorID, CpuCores: p.Probe.NumCPU, MemAllocMB: p.Probe.MemAllocMB,
				MemSysMB: p.Probe.MemSysMB, ActiveWorkers: p.Capacity.ActiveWorkers,
				MaxWorkers: capacity, LoadRatio: p.Load, ReportedAt: time.Now().UTC().Format(time.RFC3339),
			})
		}
		return nil, nil
	})
	// M2：worker 执行结果上行 → 子任务终态 → 质量管线 → 任务进度汇总
	gw.On(udp.MsgExecutionResult, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		var r orchestrator.Result
		if err := json.Unmarshal(e.Payload, &r); err != nil || r.SubtaskID == "" {
			return nil, errors.New("malformed execution_result")
		}
		log.Printf("[tower] execution_result subtask=%s status=%s worker=%s", r.SubtaskID, r.Status, r.WorkerID)
		orch.HandleResult(r)
		return nil, nil
	})
	// §6.4.13.2 遥测流 / 平台事件上行 → telemetry_events
	gw.On(udp.MsgTelemetryStream, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		var p struct {
			TaskID    string `json:"task_id"`
			SubtaskID string `json:"subtask_id"`
			WorkerID  string `json:"worker_id"`
			EventType string `json:"event_type"`
			Payload   map[string]any `json:"payload"`
			PlatformAdapter string `json:"platform_adapter"`
			AgentPlatform  string `json:"agent_platform"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.EventType == "" {
			return nil, errors.New("malformed telemetry_stream")
		}
		b, _ := json.Marshal(p.Payload)
		_ = st.InsertTelemetryEvent(store.TelemetryEvent{
			TaskID: p.TaskID, SubtaskID: p.SubtaskID, WorkerID: p.WorkerID,
			PlatformAdapter: p.PlatformAdapter, AgentPlatform: p.AgentPlatform,
			EventType: p.EventType, Payload: string(b),
		})
		hub.Publish("telemetry", map[string]any{
			"task_id": p.TaskID, "subtask_id": p.SubtaskID, "event_type": p.EventType, "payload": p.Payload,
		})
		return nil, nil
	})
	gw.On(udp.MsgPlatformEvent, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		var p struct {
			TaskID    string `json:"task_id"`
			SubtaskID string `json:"subtask_id"`
			WorkerID  string `json:"worker_id"`
			EventType string `json:"event_type"`
			Payload   map[string]any `json:"payload"`
			PlatformAdapter string `json:"platform_adapter"`
			AgentPlatform  string `json:"agent_platform"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.EventType == "" {
			return nil, errors.New("malformed platform_event")
		}
		b, _ := json.Marshal(p.Payload)
		_ = st.InsertTelemetryEvent(store.TelemetryEvent{
			TaskID: p.TaskID, SubtaskID: p.SubtaskID, WorkerID: p.WorkerID,
			PlatformAdapter: p.PlatformAdapter, AgentPlatform: p.AgentPlatform,
			EventType: p.EventType, Payload: string(b),
		})
		hub.Publish("platform_event", map[string]any{
			"task_id": p.TaskID, "subtask_id": p.SubtaskID, "event_type": p.EventType, "payload": p.Payload,
		})
		return nil, nil
	})
	// M8：命令终端上行（A1/A3）
	gw.On(udp.MsgCmdRegister, func(e udp.Envelope, src *net.UDPAddr) ([]byte, error) {
		return ct.HandleCmdRegister(e, src)
	})
	gw.On(udp.MsgCmdHeartbeat, func(e udp.Envelope, src *net.UDPAddr) ([]byte, error) {
		return ct.HandleCmdHeartbeat(e, src)
	})
	gw.On(udp.MsgCmdResult, func(e udp.Envelope, src *net.UDPAddr) ([]byte, error) {
		return ct.HandleCmdResult(e, src)
	})
	go gw.Serve()
	jlog("info", "udp-gateway", "listening :"+udpPort)

	// ── 抽查重试调度（6.5 每分钟）──
	go func() {
		for range time.Tick(60 * time.Second) {
			qual.RunPendingSpotChecks()
		}
	}()

	// ── HTTP 管理面（附录 B.1 + §6.4.16 鉴权）──
	mux := http.NewServeMux()
	az := authz.New(st, os.Getenv("MACS_ADMIN_TOKEN"))
	guard := func(scope string, h http.HandlerFunc) http.HandlerFunc { return az.Middleware(scope, h) }

	wj := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	// 集群（read）
	mux.HandleFunc("GET /api/v1/incubators", func(w http.ResponseWriter, _ *http.Request) {
		list, err := cm.List(false)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})

	// 调度记录（read）
	mux.HandleFunc("GET /api/v1/dispatch-log", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := st.ListDispatchLog(100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": rows})
	})

	// ── 任务（ops 写 / read 读；V3.3 查询参数）──
	mux.HandleFunc("POST /api/v1/tasks", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var req orchestrator.TaskRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Goal) == "" {
			apiErr(w, 400, 66000, "goal required")
			return
		}
		task, err := orch.SubmitPlan(req)
		if err != nil {
			if errors.Is(err, orchestrator.ErrBadPlan) {
				apiErr(w, 400, 66000, err.Error())
				return
			}
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, task)
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		statuses := []string{}
		for _, s := range strings.Split(q.Get("status"), ",") {
			if s = strings.TrimSpace(s); s != "" {
				statuses = append(statuses, s)
			}
		}
		page, _ := strconv.Atoi(q.Get("page"))
		pageSize, _ := strconv.Atoi(q.Get("pageSize"))
		if page <= 0 {
			page = 1
		}
		list, total, err := st.ListTasksFiltered(store.TaskQuery{
			Statuses: statuses, From: q.Get("from"), To: q.Get("to"), Q: q.Get("q"),
			Limit: pageSize, Offset: (page - 1) * max(pageSize, 50), Sort: q.Get("sort"),
		})
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		type taskWithSubs struct {
			store.Task
			Subtasks []store.Subtask `json:"subtasks"`
		}
		out := make([]taskWithSubs, 0, len(list))
		for _, t := range list {
			subs, _ := st.ListSubtasks(t.ID)
			out = append(out, taskWithSubs{Task: t, Subtasks: subs})
		}
		wj(w, map[string]any{"data": out, "page": page, "pageSize": pageSize, "total": total})
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/subtasks", func(w http.ResponseWriter, r *http.Request) {
		subs, err := st.ListSubtasks(r.PathValue("id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": subs})
	})
	// V3.2 任务执行透视（§14.2~14.6 只读端点）
	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, err := st.GetTask(r.PathValue("id"))
		if err != nil {
			apiErr(w, 404, 66200, "task not found")
			return
		}
		cost, _ := st.TaskCostCents(t.ID)
		traces, _ := st.ListOriginTraces(t.ID, 100)
		wj(w, map[string]any{"data": map[string]any{
			"task": t, "cost_cents": cost, "traces": traces,
		}})
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/dag", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		subs, err := st.ListSubtasks(id)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		edges := []map[string]string{}
		for _, s := range subs {
			for _, dep := range strings.Split(s.DependsOn, ",") {
				if dep = strings.TrimSpace(dep); dep != "" {
					edges = append(edges, map[string]string{"from": dep, "to": s.ID})
				}
			}
		}
		wj(w, map[string]any{"data": map[string]any{"subtasks": subs, "dependencies": edges}})
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/workers", func(w http.ResponseWriter, r *http.Request) {
		// V3.2 口径：子 Worker 状态投影由子任务表读取侧合成，不建独立云端 Worker 表
		subs, err := st.ListSubtasks(r.PathValue("id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		out := []map[string]any{}
		for _, s := range subs {
			if s.WorkerID == "" {
				continue
			}
			out = append(out, map[string]any{
				"worker_id": s.WorkerID, "subtask_id": s.ID, "status": s.Status,
				"incubator_id": s.IncubatorID, "started_at": s.StartedAt, "finished_at": s.FinishedAt,
			})
		}
		wj(w, map[string]any{"data": out})
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		evs, err := st.ListTelemetryEvents(r.PathValue("id"), "", "", 200)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].ID > evs[j].ID })
		wj(w, map[string]any{"data": evs})
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		// M2.6 §6.4.3.3 API 契约：200 {id, status:cancelled, changed, cancelled_subtasks, notified_incubators}
		result, err := orch.CancelTask(r.PathValue("id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, result)
	}))

	// ── M3 质量面：审批 / 抽查 / 追溯 / 归档 ──
	mux.HandleFunc("GET /api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListApprovals(r.URL.Query().Get("status"), 100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/approvals/{id}/decision", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Decision string `json:"decision"` // approved/rejected
			Note     string `json:"note"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			req.Decision = ""
		}
		if req.Decision != "approved" && req.Decision != "rejected" {
			apiErr(w, 400, 66000, "decision must be approved|rejected")
			return
		}
		a, changed, err := appr.Decide(r.PathValue("id"), req.Decision, "console", req.Note)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		if !changed {
			apiErr(w, 409, 66300, "approval not pending (already decided or expired)")
			return
		}
		wj(w, a)
	}))
	mux.HandleFunc("GET /api/v1/spot-checks", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListSpotChecks(100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("GET /api/v1/trace", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.URL.Query().Get("task_id")
		if taskID == "" {
			apiErr(w, 400, 66000, "task_id required")
			return
		}
		list, err := st.ListOriginTraces(taskID, 200)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		broken, _ := tr.VerifyTask(taskID)
		wj(w, map[string]any{"data": list, "broken_subtasks": broken})
	})
	mux.HandleFunc("GET /api/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list, err := st.ListArchiveDocs(q.Get("task_id"), q.Get("doc_type"), 100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	// UI 设计 16.2：归档查询走 /api/v1/archives（与 /documents 同一处理器别名）
	mux.HandleFunc("GET /api/v1/archives", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list, err := st.ListArchiveDocs(q.Get("task_id"), q.Get("doc_type"), 100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("GET /api/v1/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		doc, err := st.GetArchiveDoc(r.PathValue("id"))
		if err != nil {
			apiErr(w, 404, 66200, "document not found")
			return
		}
		wj(w, doc)
	})
	mux.HandleFunc("GET /api/v1/guardrails", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListGuardrailEvents(100)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})

	// ── M5 成本（FR-21）──
	mux.HandleFunc("GET /api/v1/costs/summary", func(w http.ResponseWriter, r *http.Request) {
		sums, err := st.SumCosts(r.URL.Query().Get("task_id"), r.URL.Query().Get("incubator_id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		global, _ := st.GlobalCostCents()
		wj(w, map[string]any{"data": sums, "global_cents": global})
	})
	mux.HandleFunc("GET /api/v1/costs/records", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListCostRecords(r.URL.Query().Get("task_id"), 200)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("GET /api/v1/budgets", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListBudgetLimits()
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/budgets", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ScopeType   string  `json:"scope_type"` // global/incubator/template/task
			ScopeID     string  `json:"scope_id"`
			AmountCents float64 `json:"amount_cents"`
			WarnAtPct   int     `json:"warn_at_pct"`
			EnforceMode string  `json:"enforce_mode"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ScopeType == "" {
			apiErr(w, 400, 66000, "scope_type required")
			return
		}
		if req.WarnAtPct <= 0 {
			req.WarnAtPct = 80 // Q8 默认
		}
		if req.EnforceMode == "" {
			req.EnforceMode = "hard"
		}
		var ver int64
		if prev, err := st.GetBudgetLimit(req.ScopeType, req.ScopeID); err == nil {
			ver = prev.LimitVersion
		}
		b := store.BudgetLimit{
			ID: store.NewID(), ScopeType: req.ScopeType, ScopeID: req.ScopeID,
			AmountCents: req.AmountCents, WarnAtPct: req.WarnAtPct,
			EnforceMode: req.EnforceMode, LimitVersion: ver + 1, // 6.4.11：仅收紧（版本单调递增）
		}
		if err := st.UpsertBudgetLimit(b); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		// budget_changed 下行（孵化器仅应用更大 limit_version）
		if req.ScopeType == "incubator" && req.ScopeID != "" {
			disp.NoticeBudgetChanged(req.ScopeID, map[string]any{
				"limit_version": b.LimitVersion, "amount_cents": b.AmountCents,
				"warn_at_pct": b.WarnAtPct, "enforce_mode": b.EnforceMode,
			})
		}
		wj(w, b)
		w.WriteHeader(http.StatusCreated)
	}))

	// ── M5 模板（FR-22）──
	mux.HandleFunc("GET /api/v1/templates", func(w http.ResponseWriter, r *http.Request) {
		activeOnly := r.URL.Query().Get("all") == ""
		list, err := st.ListTemplates(activeOnly)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/templates", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var t store.AgentTemplate
		if json.NewDecoder(r.Body).Decode(&t) != nil || t.Name == "" {
			apiErr(w, 400, 66000, "name required")
			return
		}
		if t.Version == "" {
			t.Version = "1.0"
		}
		id, err := st.UpsertTemplate(t)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		// 6.4.3 template_updated 广播（孵化器按需拉取；本阶段仅通知版本号）
		disp.BroadcastNotice(udp.MsgTemplateUpdated, map[string]any{
			"template_id": id, "name": t.Name, "new_version": t.Version,
		})
		wj(w, map[string]any{"id": id})
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("POST /api/v1/templates/{id}/deactivate", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeactivateTemplate(r.PathValue("id")); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"ok": true})
	}))

	// ── M6 统一协议（模块 #28：三表 REST）──
	mux.HandleFunc("GET /api/v1/agent-platforms", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListPlatforms(r.URL.Query().Get("all") == "")
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/agent-platforms", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var p store.AgentPlatform
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.PlatformName == "" {
			apiErr(w, 400, 66000, "platform_name required")
			return
		}
		id, err := st.UpsertPlatform(p)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"id": id})
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("PATCH /api/v1/agent-platforms/{id}", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IsActive      *bool  `json:"is_active"`
			DefaultDriver string `json:"default_driver"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.IsActive == nil {
			apiErr(w, 400, 66000, "is_active required")
			return
		}
		if err := st.SetPlatformActive(r.PathValue("id"), *req.IsActive, req.DefaultDriver); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("GET /api/v1/protocol-drivers", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListDrivers()
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/protocol-drivers", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var d store.ProtocolDriver
		if json.NewDecoder(r.Body).Decode(&d) != nil || d.DriverName == "" {
			apiErr(w, 400, 66000, "driver_name required")
			return
		}
		if d.Version == "" {
			d.Version = "1.0"
		}
		id, err := st.UpsertDriver(d)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"id": id})
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("GET /api/v1/platform-adapters", func(w http.ResponseWriter, r *http.Request) {
		list, err := st.ListPlatformAdapters(r.URL.Query().Get("platform_id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/platform-adapters", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var a store.PlatformAdapter
		if json.NewDecoder(r.Body).Decode(&a) != nil || a.PlatformID == "" || a.DriverID == "" {
			apiErr(w, 400, 66000, "platform_id and driver_id required")
			return
		}
		if a.Status == "" {
			a.Status = "developing"
		}
		if err := st.UpsertPlatformAdapter(a); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"ok": true})
		w.WriteHeader(http.StatusCreated)
	}))

	// ── 遥测（模块 #30）──
	mux.HandleFunc("GET /api/v1/telemetry/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		list, err := st.ListTelemetryEvents(q.Get("task_id"), q.Get("subtask_id"), q.Get("event_type"), limit)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	// SSE 实时事件（V3.0 §6.4.13）：状态/遥测推送 + 30s 心跳帧
	// §6.4.13.4.1 SSE 帧规范：id/event/data 三行帧 + 15s 心跳 + Last-Event-ID 回放 ≤200
	mux.HandleFunc("GET /api/v1/telemetry/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			apiErr(w, 500, 66500, "stream unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // 反代不缓冲
		fl.Flush()

		// 断线回放：Last-Event-ID 之后最近 ≤200 条 telemetry_events（时间正序）
		if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
			if evs, err := st.ListTelemetryEventsAfterID(lastID, 200); err == nil && len(evs) > 0 {
				for _, e := range evs {
					fmt.Fprintf(w, "id: %d\nevent: telemetry.update\ndata: %s\n\n", e.ID, e.Payload)
				}
				fl.Flush()
			}
		}

		ch, cancel := hub.Subscribe()
		defer cancel()
		seq := hub.Seq() // 实时流 id 基准（断线回放以 telemetry_events 自增 id 为准）
		tk := time.NewTicker(15 * time.Second) // §6.4.13.4.1：15s 心跳
		defer tk.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tk.C:
				_, _ = w.Write([]byte(": heartbeat\n\n"))
				fl.Flush()
			case b, ok := <-ch:
				if !ok {
					return // 慢客户端被 Hub 主动断开（§6.4.13.4.1）；客户端重连回放
				}
				seq++
				// M2.6：SSE 命名事件帧（UI §14.1 / 主设计 6.4.13.3）——
				// web/DSH 统一 addEventListener('telemetry.update')
				fmt.Fprintf(w, "id: %d\nevent: telemetry.update\ndata: ", seq)
				_, _ = w.Write(b)
				_, _ = w.Write([]byte("\n\n"))
				fl.Flush()
			}
		}
	})

	// ── M8 命令终端（模块 #32 塔侧；exec/rotate/register 为 danger scope §6.4.16）──
	mux.HandleFunc("GET /api/v1/command-terminals", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListCommandTerminals()
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/command-terminals", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Name) == "" {
			apiErr(w, 400, 66000, "name required")
			return
		}
		res, err := ct.Register(strings.TrimSpace(req.Name))
		if err != nil {
			apiErr(w, 409, 66300, err.Error())
			return
		}
		az.Refresh()
		wj(w, res) // 密钥一次性展示（§6.4.14 A2）
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("POST /api/v1/command-terminals/{id}/rotate-secret", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		res, err := ct.Rotate(r.PathValue("id"))
		if err != nil {
			apiErr(w, 404, 66200, err.Error())
			return
		}
		wj(w, res)
	}))
	mux.HandleFunc("POST /api/v1/command-terminals/{id}/exec", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Command string `json:"command"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Command) == "" {
			apiErr(w, 400, 66000, "command required")
			return
		}
		out, err := ct.Exec(r.PathValue("id"), strings.TrimSpace(req.Command))
		if err != nil {
			apiErr(w, 409, 66300, err.Error())
			return
		}
		wj(w, out)
	}))
	mux.HandleFunc("GET /api/v1/command-terminals/{id}/audit", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list, err := st.ListCommandAudit(r.PathValue("id"), q.Get("from"), q.Get("to"), 200)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})

	// ── §6.4.16 Admin Token（签发仅 danger）──
	mux.HandleFunc("POST /api/v1/admin-tokens", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name  string `json:"name"`
			Scope string `json:"scope"` // read/ops/danger
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Name == "" {
			apiErr(w, 400, 66000, "name required")
			return
		}
		if req.Scope != authz.ScopeRead && req.Scope != authz.ScopeOps && req.Scope != authz.ScopeDanger {
			apiErr(w, 400, 66000, "scope must be read|ops|danger")
			return
		}
		secret := udp.NewID() + udp.NewID()
		tok := store.AdminToken{
			ID: store.NewID(), Name: req.Name, TokenHash: trace.HashHex(secret), Scope: req.Scope,
		}
		if err := st.InsertAdminToken(tok); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		az.Refresh()
		wj(w, map[string]any{"id": tok.ID, "name": tok.Name, "scope": tok.Scope, "token": secret}) // 一次性展示
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("GET /api/v1/admin-tokens", guard(authz.ScopeDanger, func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListAdminTokens()
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	}))
	mux.HandleFunc("POST /api/v1/incubators/{id}/rotate-token", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		// §6.4.16：孵化器注册令牌吊销/轮换 —— 生成独立令牌（sha256 入库），旧全局令牌仍兼容
		id := r.PathValue("id")
		if _, err := st.GetIncubator(id); err != nil {
			apiErr(w, 404, 66200, "incubator not found")
			return
		}
		token := udp.NewID() + udp.NewID()
		if err := st.SetIncubatorTokenHash(id, trace.HashHex(token)); err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"id": id, "token": token}) // 一次性展示
	}))
	mux.HandleFunc("GET /api/v1/incubators/{id}/rotate-token", guard(authz.ScopeDanger, func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, 405, 66000, "use POST")
	}))

	// ── §6.4.17 C9 通知渠道管理（webhook 单通道先行）──
	mux.HandleFunc("GET /api/v1/notify-channels", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListNotifyChannels()
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": list})
	})
	mux.HandleFunc("POST /api/v1/notify-channels", guard(authz.ScopeDanger, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind     string `json:"kind"`
			Config   string `json:"config"`
			MinLevel string `json:"min_level"`
			Enabled  *bool  `json:"enabled"` // 未传 = 默认启用；显式 false 才停用
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Kind == "" {
			apiErr(w, 400, 66000, "kind required (webhook/wecom/smtp)")
			return
		}
		if req.MinLevel == "" {
			req.MinLevel = "warn"
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		id, err := st.UpsertNotifyChannel(store.NotifyChannel{
			Kind: req.Kind, Config: req.Config, MinLevel: req.MinLevel, Enabled: enabled,
		})
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"id": id})
		w.WriteHeader(http.StatusCreated)
	}))

	// M1 调度测试钩子
	mux.HandleFunc("POST /api/v1/dispatch", guard(authz.ScopeOps, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SubtaskID string         `json:"subtask_id"`
			TaskDef   map[string]any `json:"task_def"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.SubtaskID == "" {
			apiErr(w, 400, 66000, "subtask_id required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		incubatorID, err := disp.Dispatch(ctx, req.SubtaskID, req.TaskDef)
		if err != nil {
			apiErr(w, 409, 66300, err.Error())
			return
		}
		wj(w, map[string]any{"ok": true, "incubator_id": incubatorID})
	}))

	// ── UI 设计 §7.3 配置开关面板：当前值/默认值/开↔关语义/拦截边界（不允许黑盒开关）──
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, _ *http.Request) {
		type switchRow struct {
			Key       string `json:"key"`
			Current   any    `json:"current"`
			Default   any    `json:"default"`
			OnEffect  string `json:"on_effect"`
			OffEffect string `json:"off_effect"`
			Boundary  string `json:"boundary"`
			Source    string `json:"source"`
		}
		rows := []switchRow{
			{"feature.review.enabled", qualCfg.ReviewEnabled, true, "复核 Worker 孵化", "仅自检+抽查", "只影响复核孵化", "MACS_FEATURE_REVIEW"},
			{"feature.spot_check.enabled", qualCfg.SpotCheckEnabled, false, "云端抽查（E06 对账）", "仅自检+复核", "只拦截抽查触发", "MACS_FEATURE_SPOT_CHECK"},
			{"spot_check.ratio", qualCfg.SpotCheckRatio, 20, "抽样比例 %", "-", "Q7 口径", "MACS_SPOT_CHECK_RATIO"},
			{"udp.broadcast_mode.enabled", cfg.BroadcastEnabled, true, "广播 first-ACK-wins", "点名单播", "只影响新调度", "MACS_BROADCAST_ENABLED"},
			{"cluster.rebalance.enabled", cfg.RebalanceEnabled, true, "30s 再平衡（0.85 高水位）", "静态分配", "不迁移在跑子任务", "MACS_REBALANCE_ENABLED"},
			{"worker.ttl", int(cfg.SubtaskTTL.Minutes()), 30, "超时销毁 + E01 重试", "-", "Q3 口径", "MACS_SUBTASK_TTL_MIN"},
			{"worker.max_retries", cfg.MaxRetries, 3, "E01 重试上限", "-", "指数退避 30s×2^n", "MACS_MAX_RETRIES"},
			{"budget.limit_enforce", true, true, "多级预算 + 塔侧上界强制（E09）", "只记账告警", "不影响在跑收尾", "常开（M5）"},
			{"reliability.circuit_breaker", true, true, "连续 5 次失败熔断 60s", "-", "guardrail_events 落账", "常开（V3.5.4）"},
			{"done_acceptance_mode", "strict", "strict", "自检严格门", "advisory 带缺陷提交", "只作用于自检提交边界", "常开（M3）"},
			{"feature.preemptive.enabled", false, false, "高优抢占槽位", "严格 FIFO", "10.3 预留", "未实现（对照表 📋）"},
			{"udp.tcp_fallback.enabled", false, false, "TCP 兜底通道", "仅 UDP+ACK", "10.3 预留", "未实现（对照表 📋）"},
			{"plugins.enabled", false, false, "第三方插件加载", "仅内置适配器", "6.4.12 预留", "未实现（对照表 📋）"},
		}
		wj(w, map[string]any{"data": rows})
	})

	// ── UI 设计 §8.2 枚举字典：Badge 从后端枚举读取，状态着色零散硬编码收敛到此处 ──
	mux.HandleFunc("GET /api/v1/enums", func(w http.ResponseWriter, _ *http.Request) {
		colorOf := map[string]string{ // 8.2 五档语义
			"online": "success", "acked": "success", "completed": "success", "passed": "success", "approved": "success", "ready": "success", "spot_check_passed": "success",
			"running": "active", "reviewing": "active", "in_spot_check": "active",
			"pending": "neutral", "registered": "neutral", "skipped": "neutral",
			"dispatched": "waiting", "review_pending": "waiting", "self_checking": "waiting",
			"overloaded": "warn", "retransmitted": "warn", "partial_failure": "warn", "timeout": "warn",
			"offline": "danger", "failed": "danger", "rejected": "danger", "blocked": "danger", "frozen": "danger", "revoked": "danger", "unreachable": "danger",
		}
		wj(w, map[string]any{
			"task_status":       []string{"pending", "running", "in_spot_check", "completed", "partial_failure", "blocked", "failed", "cancelled"},
			"subtask_status":    []string{"pending", "ready", "dispatched", "running", "self_checking", "review_pending", "reviewing", "frozen", "completed", "failed", "skipped", "cancelled"},
			"incubator_status":  []string{"online", "overloaded", "offline", "unreachable"},
			"approval_status":   []string{"pending", "approved", "rejected", "expired"},
			"spot_check_status": []string{"pending", "running", "passed", "failed", "retried", "skipped"},
			"dispatch_status":   []string{"pending", "sent", "retransmitted", "acked", "driver_mismatch", "failed", "duplicate"},
			"color":             colorOf,
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, http.StatusNotFound, 66200, "resource not found")
	})

	// 抽查收口 → 任务终结回调；复核放行 → 任务重算收口
	qual.OnSpotFinished(orch.OnSpotCheckFinished)
	qual.OnSubtaskFinal(orch.RecomputeAndFinalize)

	httpPort := env("MACS_TOWER_HTTP_PORT", "8471")
	jlog("info", "http-admin", "listening :"+httpPort)
	log.Fatal(http.ListenAndServe(":"+httpPort, mux))
}
