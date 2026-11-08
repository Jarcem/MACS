// macs-tower 调度塔入口（M1：模块 #1 注册认证 + #2 UDP 网关 + #3 广播调度 + #4 集群管理）
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"macs.local/tower/internal/cluster"
	"macs.local/tower/internal/dispatcher"
	"macs.local/tower/internal/events"
	"macs.local/tower/internal/gateway"
	"macs.local/tower/internal/orchestrator"
	"macs.local/tower/internal/registry"
	"macs.local/tower/internal/store"
	"macs.local/tower/internal/udp"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func jlog(level, module, msg string) {
	b, _ := json.Marshal(map[string]string{
		"ts": time.Now().UTC().Format(time.RFC3339), "level": level, "module": module, "msg": msg,
	})
	log.Println(string(b))
}

type errBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func apiErr(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errBody{Code: code, Message: msg})
}

func main() {
	// ── 存储（V3.4：SQLite 默认；PG 切换随 M2 落地）──
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
	orch := orchestrator.New(st, disp, hub) // 模块 #5：任务编排（M2）
	orch.Start()
	cm.StartSweeper()
	defer cm.Close()

	gw.On(udp.MsgRegister, reg.Handle) // 模块 #1：注册落库
	gw.On(udp.MsgAck, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		disp.HandleAck(e) // 模块 #3：唤醒 ACK 等待
		return nil, nil
	})
	gw.On(udp.MsgReport, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		var p struct {
			IncubatorID string `json:"incubator_id"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		cm.Touch(p.IncubatorID) // 模块 #4：心跳刷新
		return nil, nil
	})
	// M2：worker 执行结果上行 → 子任务终态 → 任务进度汇总
	gw.On(udp.MsgExecutionResult, func(e udp.Envelope, _ *net.UDPAddr) ([]byte, error) {
		var r orchestrator.Result
		if err := json.Unmarshal(e.Payload, &r); err != nil || r.SubtaskID == "" {
			return nil, errors.New("malformed execution_result")
		}
		log.Printf("[tower] execution_result subtask=%s status=%s worker=%s", r.SubtaskID, r.Status, r.WorkerID)
		orch.HandleResult(r)
		return nil, nil
	})
	go gw.Serve()
	jlog("info", "udp-gateway", "listening :"+udpPort)

	// ── HTTP 管理面 ──
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/v1/incubators", func(w http.ResponseWriter, _ *http.Request) {
		list, err := cm.List(false)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
	})

	// ── 监控端只读端点（附录 B.1；M1 阶段空表先返回空集，M2 接任务表后填实）──
	wj := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/v1/dispatch-log", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := st.ListDispatchLog(50)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": rows})
	})
	// ── 任务编排（M2 垂直切片）：提交目标 → 拆解 → 分发 → 汇总 ──
	mux.HandleFunc("POST /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Goal string `json:"goal"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Goal) == "" {
			apiErr(w, 400, 66000, "goal required")
			return
		}
		task, err := orch.Submit(req.Goal)
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(task)
	})
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, _ *http.Request) {
		list, err := st.ListTasks(50)
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
		wj(w, map[string]any{"data": out, "page": 1, "pageSize": 50})
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/subtasks", func(w http.ResponseWriter, r *http.Request) {
		subs, err := st.ListSubtasks(r.PathValue("id"))
		if err != nil {
			apiErr(w, 500, 66500, err.Error())
			return
		}
		wj(w, map[string]any{"data": subs})
	})
	mux.HandleFunc("GET /api/v1/agent-platforms", func(w http.ResponseWriter, _ *http.Request) {
		wj(w, map[string]any{"data": []any{}})
	})
	mux.HandleFunc("GET /api/v1/platform-adapters", func(w http.ResponseWriter, _ *http.Request) {
		wj(w, map[string]any{"data": []any{}})
	})
	mux.HandleFunc("GET /api/v1/telemetry/events", func(w http.ResponseWriter, _ *http.Request) {
		wj(w, map[string]any{"data": []any{}})
	})
	// SSE 实时事件（V3.0 §6.4.13）：任务/子任务状态变化实时推送 + 30s 心跳帧
	mux.HandleFunc("GET /api/v1/telemetry/stream", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			apiErr(w, 500, 66500, "stream unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fl.Flush()

		ch, cancel := hub.Subscribe()
		defer cancel()
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tk.C:
				_, _ = w.Write([]byte(": heartbeat\n\n"))
				fl.Flush()
			case b := <-ch:
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(b)
				_, _ = w.Write([]byte("\n\n"))
				fl.Flush()
			}
		}
	})
	// M1 调度测试钩子（正式任务编排随 M2 任务表接入）
	mux.HandleFunc("POST /api/v1/dispatch", func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "incubator_id": incubatorID})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, http.StatusNotFound, 66200, "resource not found")
	})

	httpPort := env("MACS_TOWER_HTTP_PORT", "8471")
	jlog("info", "http-admin", "listening :"+httpPort)
	log.Fatal(http.ListenAndServe(":"+httpPort, mux))
}
