// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package agentgate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"macs.local/tower/agentmsg"
	"macs.local/tower/internal/store"
)

// ---------- 测试装置 ----------

func newGateStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tower.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate("../../migrations"); err != nil {
		t.Fatal(err)
	}
	return st
}

func validCredential(t *testing.T, st *store.Store, id, hash string) {
	t.Helper()
	if err := st.UpsertConnector(store.Connector{ID: id, Name: id, CredentialID: id + "-cred", CredentialHash: hash, ProtocolVersion: "1.0"}); err != nil {
		t.Fatal(err)
	}
}

type testGateway struct {
	gw       *Gateway
	srv      *httptest.Server
	URL      string
	st       *store.Store
	messages chan agentmsg.Envelope
}

func newTestGateway(t *testing.T, creds ...[2]string) *testGateway {
	st := newGateStore(t)
	for _, c := range creds {
		validCredential(t, st, c[0], c[1])
	}
	gw := New(st)
	tg := &testGateway{gw: gw, st: st, messages: make(chan agentmsg.Envelope, 32)}
	gw.SetMessageHandler(func(env agentmsg.Envelope) { tg.messages <- env })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent/connect", gw.HandleConnect)
	srv := httptest.NewServer(mux)
	tg.srv = srv
	tg.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect"
	t.Cleanup(srv.Close)
	return tg
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

func send(t *testing.T, conn *websocket.Conn, env agentmsg.Envelope) {
	t.Helper()
	b, err := agentmsg.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, conn *websocket.Conn) agentmsg.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	env, err := agentmsg.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func dialAndHello(t *testing.T, tg *testGateway, connectorID, credHash string) *websocket.Conn {
	t.Helper()
	conn := dial(t, tg.URL)
	send(t, conn, agentmsg.Envelope{
		MessageType: agentmsg.MsgHello, ProtocolVersion: agentmsg.ProtocolVersion,
		ConnectorID: connectorID, CredentialHash: credHash,
	})
	ack := read(t, conn)
	if ack.MessageType != agentmsg.MsgHelloAck {
		t.Fatalf("expected hello_ack, got %+v", ack)
	}
	return conn
}

// ---------- Task 3：凭据与协议校验（设计 §10） ----------

func TestGatewayRejectsInvalidCredentialAndProtocol(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	conn := dial(t, tg.URL)
	send(t, conn, agentmsg.Envelope{MessageType: agentmsg.MsgHello, ProtocolVersion: "2.0", ConnectorID: "c1", CredentialHash: "hash-1"})
	msg := read(t, conn)
	if msg.MessageType != agentmsg.MsgError || !strings.Contains(string(msg.Payload), "protocol") {
		t.Fatalf("expected protocol error: %+v", msg)
	}
	conn2 := dial(t, tg.URL)
	send(t, conn2, agentmsg.Envelope{MessageType: agentmsg.MsgHello, ProtocolVersion: "1.0", ConnectorID: "c1", CredentialHash: "wrong"})
	msg2 := read(t, conn2)
	if msg2.MessageType != agentmsg.MsgError || !strings.Contains(string(msg2.Payload), "credential") {
		t.Fatalf("invalid credential accepted: %+v", msg2)
	}
}

// 未知/吊销 Connector 拒绝
func TestGatewayRejectsUnknownAndRevoked(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	if err := tg.st.RevokeConnector("c1"); err != nil {
		t.Fatal(err)
	}
	conn := dial(t, tg.URL)
	send(t, conn, agentmsg.Envelope{MessageType: agentmsg.MsgHello, ProtocolVersion: "1.0", ConnectorID: "c1", CredentialHash: "hash-1"})
	msg := read(t, conn)
	if msg.MessageType != agentmsg.MsgError || !strings.Contains(string(msg.Payload), "revoked") {
		t.Fatalf("revoked connector accepted: %+v", msg)
	}
	conn2 := dial(t, tg.URL)
	send(t, conn2, agentmsg.Envelope{MessageType: agentmsg.MsgHello, ProtocolVersion: "1.0", ConnectorID: "ghost", CredentialHash: "x"})
	if msg2 := read(t, conn2); msg2.MessageType != agentmsg.MsgError {
		t.Fatalf("unknown connector accepted: %+v", msg2)
	}
}

// 候选上报 + 心跳（心跳只更新健康/负载，不覆盖身份）
func TestGatewayCandidateReportAndHeartbeat(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	conn := dialAndHello(t, tg, "c1", "hash-1")

	send(t, conn, agentmsg.Envelope{MessageType: agentmsg.MsgAgentReport, Payload: mustJSONRaw([]agentmsg.CandidateAgent{{
		LocalAgentKey: "zcode", DisplayName: "Zcode", Reported: json.RawMessage(`{"adapter":"cli_agent"}`),
	}})})
	// 等待落库
	var inst store.AgentInstance
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		inst, _ = tg.st.GetAgentInstanceByKey("c1", "zcode")
		if inst.ID != "" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if inst.ID == "" || inst.IdentityStatus != "pending" {
		t.Fatalf("candidate not created: %+v", inst)
	}
	// 人工标注后（模拟）
	if _, err := tg.st.Exec(`UPDATE agent_instances SET identity_status='verified', platform_id='zcode', revision=2, display_name='Mac Zcode' WHERE id=?`, inst.ID); err != nil {
		t.Fatal(err)
	}

	send(t, conn, agentmsg.Envelope{MessageType: agentmsg.MsgHeartbeat, Payload: mustJSONRaw([]agentmsg.HeartbeatAgent{{
		LocalAgentKey: "zcode", HealthStatus: "healthy", Load: json.RawMessage(`{"active":1}`),
	}})})
	deadline = time.Now().Add(3 * time.Second)
	after := store.AgentInstance{}
	for time.Now().Before(deadline) {
		after, _ = tg.st.GetAgentInstance(inst.ID)
		if after.HealthStatus == "healthy" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if after.HealthStatus != "healthy" || after.OnlineStatus != "online" {
		t.Fatalf("heartbeat not applied: %+v", after)
	}
	if after.DisplayName != "Mac Zcode" || after.Revision != 2 || after.PlatformID != "zcode" {
		t.Fatalf("heartbeat overwrote identity: %+v", after)
	}
}

// 结果转发到 handler（消息体 connector_id 被连接绑定覆盖，防串号）
func TestGatewayResultForwardedWithBoundConnector(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	conn := dialAndHello(t, tg, "c1", "hash-1")
	send(t, conn, agentmsg.Envelope{MessageType: agentmsg.MsgResult, ConnectorID: "SPOOFED",
		AttemptID: "att-1", IdempotencyKey: "k", Payload: json.RawMessage(`{"status":"completed"}`)})
	select {
	case env := <-tg.messages:
		if env.ConnectorID != "c1" {
			t.Fatalf("spoofed connector_id passed: %s", env.ConnectorID)
		}
		if env.AttemptID != "att-1" {
			t.Fatalf("attempt lost: %+v", env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("result not forwarded")
	}
}

// ---------- Task 9：多 Connector 并发连接 ----------

func TestGatewaySupportsMultipleConnectors(t *testing.T) {
	tg := newTestGateway(t, [2]string{"mac", "mac-hash"}, [2]string{"cloud", "cloud-hash"})
	mac := dialAndHello(t, tg, "mac", "mac-hash")
	cloud := dialAndHello(t, tg, "cloud", "cloud-hash")
	if mac == nil || cloud == nil {
		t.Fatal("both connectors must connect")
	}
	if !tg.gw.Connected("mac") || !tg.gw.Connected("cloud") {
		t.Fatal("gateway lost track of connections")
	}
	// 定向下发按 connector 路由
	if err := tg.gw.DispatchTo("mac", agentmsg.Envelope{MessageType: agentmsg.MsgDispatch, AttemptID: "a1"}); err != nil {
		t.Fatalf("dispatch to mac: %v", err)
	}
	env := read(t, mac)
	if env.MessageType != agentmsg.MsgDispatch || env.AttemptID != "a1" {
		t.Fatalf("mac got wrong dispatch: %+v", env)
	}
	// cloud 不应收到 mac 的分派
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := cloud.Read(ctx); err == nil {
		t.Fatal("cloud received mac dispatch")
	}
	if err := tg.gw.DispatchTo("offline-conn", agentmsg.Envelope{}); err == nil {
		t.Fatal("dispatch to offline connector must fail")
	}
}

// 重连挤掉旧连接（单 Connector 单活跃连接）
func TestGatewayReplacesStaleConnection(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	old := dialAndHello(t, tg, "c1", "hash-1")
	newConn := dialAndHello(t, tg, "c1", "hash-1")
	_ = old
	if !tg.gw.Connected("c1") {
		t.Fatal("new connection not registered")
	}
	if err := tg.gw.DispatchTo("c1", agentmsg.Envelope{MessageType: agentmsg.MsgDispatch, AttemptID: "x"}); err != nil {
		t.Fatalf("dispatch to replaced conn: %v", err)
	}
	env := read(t, newConn)
	if env.AttemptID != "x" {
		t.Fatalf("unexpected dispatch: %+v", env)
	}
}

// 重连对账：执行中 attempt 下发 reconcile_request（设计 §8）
func TestGatewayReconcileOnReconnect(t *testing.T) {
	tg := newTestGateway(t, [2]string{"c1", "hash-1"})
	// 预置 verified agent + 执行中 attempt
	validCredential(t, tg.st, "c1", "hash-1")
	inst, err := tg.st.UpsertAgentCandidate(store.AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tg.st.InsertExecutionAttempt(store.ExecutionAttempt{
		AttemptID: "att-r", TaskID: "t1", StepID: "s1", PlanRevision: 1, AttemptNo: 1,
		IdempotencyKey: "k1", AgentID: inst.ID, ActionID: "act", Environment: "dev",
		InputDigest: "d", Status: "dispatched",
	}); err != nil {
		t.Fatal(err)
	}
	conn := dialAndHello(t, tg, "c1", "hash-1")
	env := read(t, conn)
	if env.MessageType != agentmsg.MsgReconcileRequest {
		t.Fatalf("expected reconcile_request, got %+v", env)
	}
	var items []agentmsg.ReconcileAttempt
	if json.Unmarshal(env.Payload, &items) != nil || len(items) != 1 || items[0].AttemptID != "att-r" {
		t.Fatalf("reconcile payload: %s", env.Payload)
	}
}

func mustJSONRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
