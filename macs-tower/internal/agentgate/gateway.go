// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package agentgate V1.5 WSS 网关（设计 §2/§10）
// 固定入口 /agent/connect：Connector 仅出站 TLS 连入，握手校验 connector_id +
// credential_hash + protocol_version；连接建立后 connector_id 以连接绑定为准。
// 每个 connector 同时至多一条活跃连接（新连挤掉旧连）。
package agentgate

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"macs.local/tower/agentmsg"
	"macs.local/tower/internal/store"
)

const (
	helloTimeout   = 10 * time.Second // 握手窗口
	readTimeout    = 2 * time.Minute  // 心跳间隔（15s）的兜底上限
	maxMessageSize = 4 << 20          // 4MiB（结果/制品引用上限）
)

// Handler 结果/对账回执处理器（main.go 接线 AgentScheduler，避免循环依赖）
type Handler func(env agentmsg.Envelope)

type connState struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	cancel  context.CancelFunc
}

// Gateway WSS 接入网关
type Gateway struct {
	st        *store.Store
	mu        sync.Mutex
	conns     map[string]*connState // connector_id → 活跃连接
	onMessage Handler               // result / reconcile_result（已绑定校验）
	onEvent   func(kind string, data map[string]any)
}

func New(st *store.Store) *Gateway {
	return &Gateway{st: st, conns: map[string]*connState{}}
}

// SetMessageHandler 注入结果处理器（AgentScheduler.HandleAgentResult）
func (g *Gateway) SetMessageHandler(h Handler) { g.onMessage = h }

// SetEventHandler 注入事件回调（SSE 事件流，可选）
func (g *Gateway) SetEventHandler(h func(kind string, data map[string]any)) { g.onEvent = h }

// HandleConnect GET /agent/connect 升级入口
func (g *Gateway) HandleConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := r.Context()
	conn.SetReadLimit(maxMessageSize)

	// 握手窗口内等待 hello
	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	env, err := g.readEnvelope(hctx, conn)
	hcancel()
	if err != nil || env.MessageType != agentmsg.MsgHello {
		g.reject(ctx, conn, "first message must be hello")
		return
	}
	if env.ProtocolVersion != agentmsg.ProtocolVersion {
		g.reject(ctx, conn, "unsupported protocol version")
		return
	}
	c, err := g.st.GetConnector(env.ConnectorID)
	if err != nil || c.RevokedAt != "" {
		g.reject(ctx, conn, "unknown or revoked connector")
		return
	}
	if subtle.ConstantTimeCompare([]byte(env.CredentialHash), []byte(c.CredentialHash)) != 1 {
		g.reject(ctx, conn, "invalid credential")
		return
	}

	// 认证通过：先注册连接（旧连接挤掉；hello_ack 之后塔即可定向下发），
	// 再回 hello_ack。此后 connector_id 以连接绑定为准。
	connCtx, connCancel := context.WithCancel(ctx)
	st := &connState{conn: conn, cancel: connCancel}
	g.register(env.ConnectorID, st)
	g.st.TouchConnector(env.ConnectorID)
	g.reply(ctx, conn, agentmsg.Envelope{MessageType: agentmsg.MsgHelloAck, ProtocolVersion: agentmsg.ProtocolVersion})
	g.emit("agent_connector_online", map[string]any{"connector_id": env.ConnectorID})
	defer func() {
		connCancel()
		g.unregister(env.ConnectorID, st)
		conn.Close(websocket.StatusNormalClosure, "closed")
	}()

	// 重连对账（设计 §8）：先对账执行中 attempt，不重复执行
	g.reconcile(ctx, env.ConnectorID)

	// 读循环：连接绑定值固定为握手认证的 connectorID（消息体防串号）
	authID := env.ConnectorID
	for {
		rctx, rcancel := context.WithTimeout(connCtx, readTimeout)
		env, err := g.readEnvelope(rctx, conn)
		rcancel()
		if err != nil {
			return
		}
		g.handleInbound(authID, env)
	}
}

// handleInbound 入站消息（connectorID 为连接绑定值，覆盖消息体防串号）
func (g *Gateway) handleInbound(connectorID string, env agentmsg.Envelope) {
	env.ConnectorID = connectorID
	switch env.MessageType {
	case agentmsg.MsgHeartbeat:
		g.st.TouchConnector(connectorID)
		var agents []agentmsg.HeartbeatAgent
		if len(env.Payload) > 0 {
			if err := json.Unmarshal(env.Payload, &agents); err != nil {
				return
			}
		}
		for _, a := range agents {
			if err := g.st.UpdateHeartbeat(store.Heartbeat{
				ConnectorID: connectorID, LocalAgentKey: a.LocalAgentKey,
				HealthStatus: a.HealthStatus, LoadJSON: string(a.Load), OnlineStatus: "online",
			}); err != nil {
				log.Printf(`{"level":"warn","module":"agentgate","msg":"heartbeat: %v"}`, err)
			}
		}
	case agentmsg.MsgAgentReport:
		g.st.TouchConnector(connectorID)
		var agents []agentmsg.CandidateAgent
		if len(env.Payload) == 0 || json.Unmarshal(env.Payload, &agents) != nil {
			return
		}
		for _, a := range agents {
			reported := "{}"
			if len(a.Reported) > 0 {
				reported = string(a.Reported)
			}
			up, err := g.st.UpsertAgentCandidate(store.AgentInstance{
				ConnectorID: connectorID, LocalAgentKey: a.LocalAgentKey,
				DisplayName: a.DisplayName, ReportedJSON: reported,
			})
			if err != nil {
				log.Printf(`{"level":"warn","module":"agentgate","msg":"candidate upsert: %v"}`, err)
				continue
			}
			g.emit("agent_candidate", map[string]any{
				"connector_id": connectorID, "agent_id": up.ID,
				"local_agent_key": up.LocalAgentKey, "identity_status": up.IdentityStatus,
			})
		}
	case agentmsg.MsgResult:
		g.st.TouchConnector(connectorID)
		if env.AttemptID == "" {
			return
		}
		if g.onMessage != nil {
			g.onMessage(env)
		}
	case agentmsg.MsgReconcileResult:
		if g.onMessage != nil {
			g.onMessage(env)
		}
	case agentmsg.MsgDispatchAck:
		// 分派送达确认：仅记账，不推进状态（租约在分派时已设置）
	default:
		g.sendToConn(connectorID, agentmsg.Envelope{MessageType: agentmsg.MsgError, InReplyTo: env.MessageID,
			Payload: mustJSON(map[string]string{"error": "unknown message type: " + env.MessageType})})
	}
}

// reconcile 重连对账：查询该 Connector 执行中尝试，批量下发对账请求
func (g *Gateway) reconcile(ctx context.Context, connectorID string) {
	attempts, err := g.st.ListInflightAttemptsByConnector(connectorID)
	if err != nil || len(attempts) == 0 {
		return
	}
	items := make([]agentmsg.ReconcileAttempt, 0, len(attempts))
	for _, a := range attempts {
		items = append(items, agentmsg.ReconcileAttempt{
			AttemptID: a.AttemptID, TaskID: a.TaskID, StepID: a.StepID,
			PlanRevision: strconv.Itoa(a.PlanRevision), IdempotencyKey: a.IdempotencyKey,
		})
	}
	payload, _ := json.Marshal(items)
	_ = g.sendToConn(connectorID, agentmsg.Envelope{
		MessageType: agentmsg.MsgReconcileRequest, ConnectorID: connectorID, Payload: payload,
	})
}

// DispatchTo 定向下发（AgentScheduler 调用）。Connector 离线返回错误（不改派）。
func (g *Gateway) DispatchTo(connectorID string, env agentmsg.Envelope) error {
	return g.sendToConn(connectorID, env)
}

// Connected Connector 是否有活跃连接
func (g *Gateway) Connected(connectorID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.conns[connectorID]
	return ok
}

// CloseAll 优雅退出
func (g *Gateway) CloseAll() {
	g.mu.Lock()
	for _, c := range g.conns {
		c.cancel()
	}
	g.mu.Unlock()
}

// MarkOffline 连接断开后落离线（Connector + 其 Agent，身份不变）
func (g *Gateway) MarkOffline(connectorID string) {
	g.st.MarkConnectorOffline(connectorID)
	g.st.MarkAgentsOfflineConnector(connectorID)
	g.emit("agent_connector_offline", map[string]any{"connector_id": connectorID})
}

// ---------- 内部 ----------

func (g *Gateway) register(connectorID string, st *connState) {
	g.mu.Lock()
	old, had := g.conns[connectorID]
	g.conns[connectorID] = st
	g.mu.Unlock()
	if had && old != st {
		old.cancel() // 挤掉旧连接
	}
}

func (g *Gateway) unregister(connectorID string, st *connState) {
	g.mu.Lock()
	current, ok := g.conns[connectorID]
	if ok && current == st {
		delete(g.conns, connectorID)
	}
	g.mu.Unlock()
	g.MarkOffline(connectorID)
}

func (g *Gateway) sendToConn(connectorID string, env agentmsg.Envelope) error {
	g.mu.Lock()
	st, ok := g.conns[connectorID]
	g.mu.Unlock()
	if !ok {
		return errors.New("connector not connected: " + connectorID)
	}
	if env.MessageID == "" {
		env.MessageID = agentmsg.NewID()
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return st.conn.Write(wctx, websocket.MessageText, b)
}

func (g *Gateway) readEnvelope(ctx context.Context, conn *websocket.Conn) (agentmsg.Envelope, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return agentmsg.Envelope{}, err
	}
	return agentmsg.Unmarshal(data)
}

func (g *Gateway) reject(ctx context.Context, conn *websocket.Conn, msg string) {
	payload, _ := json.Marshal(map[string]string{"error": msg})
	_ = g.writeNow(ctx, conn, agentmsg.Envelope{MessageType: agentmsg.MsgError, Payload: payload})
	conn.Close(websocket.StatusPolicyViolation, msg)
}

func (g *Gateway) reply(ctx context.Context, conn *websocket.Conn, env agentmsg.Envelope) {
	_ = g.writeNow(ctx, conn, env)
}

func (g *Gateway) writeNow(ctx context.Context, conn *websocket.Conn, env agentmsg.Envelope) error {
	if env.MessageID == "" {
		env.MessageID = agentmsg.NewID()
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func (g *Gateway) emit(kind string, data map[string]any) {
	if g.onEvent != nil {
		g.onEvent(kind, data)
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
