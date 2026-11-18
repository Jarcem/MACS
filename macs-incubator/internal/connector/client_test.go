// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"macs.local/incubator/internal/worker"
	"macs.local/tower/agentmsg"
)

// ---------- fake tower（协议契约测试的最低限度对端） ----------

type fakeTower struct {
	URL                string
	results            chan agentmsg.Envelope
	reports            chan []agentmsg.CandidateAgent
	writeMu            sync.Mutex
	conn               *websocket.Conn
	credHash           string
	reconcileOnConnect bool
	t                  *testing.T
}

func newFakeTower(t *testing.T, credHash string) *fakeTower {
	ft := &fakeTower{
		t:        t,
		results:  make(chan agentmsg.Envelope, 32),
		reports:  make(chan []agentmsg.CandidateAgent, 8),
		credHash: credHash,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/connect", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		ft.writeMu.Lock()
		ft.conn = conn
		ft.writeMu.Unlock()

		hello := ft.readMsg(r.Context())
		if hello.MessageType != agentmsg.MsgHello || hello.CredentialHash != credHash {
			ft.write(r.Context(), agentmsg.Envelope{MessageType: agentmsg.MsgError, Payload: []byte(`{"error":"auth failed"}`)})
			return
		}
		ft.write(r.Context(), agentmsg.Envelope{MessageType: agentmsg.MsgHelloAck})
		for {
			env := ft.readMsg(r.Context())
			switch env.MessageType {
			case agentmsg.MsgResult, agentmsg.MsgReconcileResult:
				select {
				case ft.results <- env:
				default:
				}
				ft.write(r.Context(), agentmsg.Envelope{MessageType: agentmsg.MsgResultAck, InReplyTo: env.MessageID})
			case agentmsg.MsgAgentReport:
				var c []agentmsg.CandidateAgent
				_ = json.Unmarshal(env.Payload, &c)
				select {
				case ft.reports <- c:
				default:
				}
			case agentmsg.MsgHeartbeat, agentmsg.MsgDispatchAck:
			default:
			}
		}
	})
	srv := httptest.NewServer(mux)
	ft.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect"
	t.Cleanup(srv.Close)
	return ft
}

func (ft *fakeTower) write(ctx context.Context, env agentmsg.Envelope) {
	if env.MessageID == "" {
		env.MessageID = agentmsg.NewID()
	}
	b, _ := json.Marshal(env)
	ft.writeMu.Lock()
	defer ft.writeMu.Unlock()
	if ft.conn != nil {
		_ = ft.conn.Write(ctx, websocket.MessageText, b)
	}
}

func (ft *fakeTower) readMsg(ctx context.Context) agentmsg.Envelope {
	_, data, err := ft.conn.Read(ctx)
	if err != nil {
		return agentmsg.Envelope{}
	}
	env, _ := agentmsg.Unmarshal(data)
	return env
}

func (ft *fakeTower) SendDispatch(env agentmsg.Envelope) {
	ft.write(context.Background(), env)
}

// waitResult 等待一条结果消息
func (ft *fakeTower) waitResult(t *testing.T, timeout time.Duration) agentmsg.Envelope {
	t.Helper()
	select {
	case env := <-ft.results:
		return env
	case <-time.After(timeout):
		t.Fatal("timed out waiting for result message")
		return agentmsg.Envelope{}
	}
}

// ---------- 测试配置/桩执行器 ----------

func testConfig(t *testing.T, url string) Config {
	return Config{
		TowerURL: url, ConnectorID: "c-test", Credential: "secret-1",
		DBPath:         t.TempDir() + "/outbox.db",
		WorkDir:        t.TempDir() + "/work",
		HeartbeatEvery: 1,
		Agents: []LocalAgent{{
			LocalAgentKey: "zcode", DisplayName: "Test Zcode",
			Actions: []LocalActionConfig{{
				ActionKey: "zcode.develop", Adapter: "cli_agent", Command: "echo",
				Args: []string{"{{instruction}}"}, TimeoutMs: 5000,
			}},
		}},
	}
}

type stubRunner struct {
	mu    sync.Mutex
	calls int
	last  worker.Job
	out   worker.Outcome
}

func (r *stubRunner) Run(_ context.Context, job worker.Job) worker.Outcome {
	r.mu.Lock()
	r.calls++
	r.last = job
	out := r.out
	r.mu.Unlock()
	return out
}

func (r *stubRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func successOutcome() worker.Outcome {
	return worker.Outcome{Status: "completed", Result: `{"summary":"did it","self_check":{"status":"passed","findings":[]},"result_ref":"sha256:abc"}`}
}

func startClient(t *testing.T, cfg Config, runner Runner) (*Client, *Outbox) {
	t.Helper()
	ob, err := OpenOutbox(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ob.Close() })
	cl, err := NewClient(cfg, nil, ob, runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = cl.Run(ctx) }()
	return cl, ob
}

func waitOutboxSynced(t *testing.T, ob *Outbox) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := ob.Pending()
		if err == nil && len(pending) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	pending, _ := ob.Pending()
	t.Fatalf("outbox never synced: %+v", pending)
}

func dispatchEnvelope() agentmsg.Envelope {
	return agentmsg.Envelope{
		MessageType: agentmsg.MsgDispatch, ConnectorID: "c-test",
		AgentID: "agent-1", LocalAgentKey: "zcode",
		TaskID: "task-1", StepID: "s1", PlanRevision: "1",
		ActionID: "act-1", ActionKey: "zcode.develop",
		AttemptID: "att-1", IdempotencyKey: "idem-1",
		Environment: "dev", InputDigest: "sha256:" + strings.Repeat("a", 64),
		Payload: json.RawMessage(`{"instruction":"implement fixture"}`),
	}
}

// ---------- Task 3：客户端主链路 ----------

func TestClientExecutesDispatchAndSyncsOutbox(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	runner := &stubRunner{out: successOutcome()}
	cfg := testConfig(t, ft.URL)
	_, ob := startClient(t, cfg, runner)

	// 候选上报
	select {
	case reps := <-ft.reports:
		if len(reps) != 1 || reps[0].LocalAgentKey != "zcode" {
			t.Fatalf("unexpected report: %+v", reps)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no agent report")
	}

	ft.SendDispatch(dispatchEnvelope())
	env := ft.waitResult(t, 10*time.Second)
	if env.AttemptID != "att-1" || env.IdempotencyKey != "idem-1" || env.InputDigest != dispatchEnvelope().InputDigest {
		t.Fatalf("result envelope missing echo fields: %+v", env)
	}
	var payload agentmsg.ResultPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "completed" || len(payload.SelfCheck) == 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	waitOutboxSynced(t, ob)
	if runner.callCount() != 1 {
		t.Fatalf("runner called %d times", runner.callCount())
	}
}

func TestClientRejectsUnknownAction(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	cfg := testConfig(t, ft.URL)
	startClient(t, cfg, &stubRunner{out: successOutcome()})
	<-ft.reports
	env := dispatchEnvelope()
	env.ActionKey = "zcode.missing"
	ft.SendDispatch(env)
	got := ft.waitResult(t, 10*time.Second)
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "rejected" {
		t.Fatalf("expected rejected, got: %s", got.Payload)
	}
}

// 重复分派（同 idempotency_key）不重复执行，重发既有结果
func TestClientDedupDoesNotRerun(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	runner := &stubRunner{out: successOutcome()}
	cfg := testConfig(t, ft.URL)
	_, ob := startClient(t, cfg, runner)
	<-ft.reports

	ft.SendDispatch(dispatchEnvelope())
	ft.waitResult(t, 10*time.Second)
	waitOutboxSynced(t, ob)

	// 同 idempotency_key 重复分派
	ft.SendDispatch(dispatchEnvelope())
	got := ft.waitResult(t, 10*time.Second)
	if runner.callCount() != 1 {
		t.Fatalf("dedup failed: runner called %d times", runner.callCount())
	}
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "completed" {
		t.Fatalf("resent result unexpected: %s", got.Payload)
	}
}

// 上游制品 digest 不匹配 → 拒绝执行（设计 §6.3.3）
func TestClientInputDigestMismatchFails(t *testing.T) {
	content := "hello"
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, content)
	}))
	defer store.Close()

	ft := newFakeTower(t, CredentialHash("secret-1"))
	cfg := testConfig(t, ft.URL)
	startClient(t, cfg, &stubRunner{out: successOutcome()})
	<-ft.reports

	env := dispatchEnvelope()
	env.InputRefs = []agentmsg.ArtifactRef{{
		URI: store.URL, Digest: "sha256:" + strings.Repeat("0", 64), Kind: "commit", SizeBytes: 5,
	}}
	ft.SendDispatch(env)
	got := ft.waitResult(t, 10*time.Second)
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "failed" {
		t.Fatalf("expected failed on digest mismatch, got: %s", got.Payload)
	}
	if !strings.Contains(payload.Error, "digest mismatch") {
		t.Fatalf("error missing digest mismatch: %s", payload.Error)
	}
}

// 本地路径作为 input_refs → 拒绝（设计 §7.3）
func TestClientRejectsLocalPathInputRef(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	cfg := testConfig(t, ft.URL)
	startClient(t, cfg, &stubRunner{out: successOutcome()})
	<-ft.reports

	env := dispatchEnvelope()
	env.InputRefs = []agentmsg.ArtifactRef{{
		URI: "/Users/mac/build.zip", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "image",
	}}
	ft.SendDispatch(env)
	got := ft.waitResult(t, 10*time.Second)
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "failed" {
		t.Fatalf("expected failed on local path, got: %s", got.Payload)
	}
}

// 结果声明的本地制品必须上传共享存储（设计 §7.1；上传失败 attempt failed）
func TestClientPublishesLocalArtifacts(t *testing.T) {
	uploaded := map[string][]byte{}
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		uploaded[r.URL.Path] = b
		w.WriteHeader(http.StatusOK)
	}))
	defer store.Close()

	ft := newFakeTower(t, CredentialHash("secret-1"))
	cfg := testConfig(t, ft.URL)
	cfg.Storage.UploadURL = store.URL + "/objects/{{name}}"
	runner := &stubRunner{}
	workFile := cfg.WorkDir + "/out/report.json"
	_ = os.MkdirAll(cfg.WorkDir+"/out", 0o755)
	_ = os.WriteFile(workFile, []byte(`{"report":true}`), 0o644)
	runner.out = worker.Outcome{Status: "completed", Result: `{"summary":"built","self_check":{"status":"passed"},"artifact_refs":[{"uri":"` + workFile + `","digest":"sha256:x","kind":"test_report"}]}`}
	startClient(t, cfg, runner)
	<-ft.reports

	ft.SendDispatch(dispatchEnvelope())
	got := ft.waitResult(t, 10*time.Second)
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "completed" {
		t.Fatalf("publish failed: %s", got.Payload)
	}
	if len(payload.ArtifactRefs) != 1 || !strings.HasPrefix(payload.ArtifactRefs[0].URI, "http") {
		t.Fatalf("artifact not replaced with shared uri: %+v", payload.ArtifactRefs)
	}
	if payload.ArtifactRefs[0].Digest != sha256Digest([]byte(`{"report":true}`)) {
		t.Fatalf("digest wrong: %s", payload.ArtifactRefs[0].Digest)
	}
	if len(uploaded) != 1 {
		t.Fatalf("upload did not happen: %v", uploaded)
	}
}

// WorkDir 之外的本地制品 → failed（防越权读取）
func TestClientRejectsArtifactOutsideWorkDir(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	cfg := testConfig(t, ft.URL)
	runner := &stubRunner{}
	runner.out = worker.Outcome{Status: "completed", Result: `{"summary":"x","self_check":{"status":"passed"},"artifact_refs":[{"uri":"/etc/hostname","digest":"sha256:x","kind":"log"}]}`}
	startClient(t, cfg, runner)
	<-ft.reports
	ft.SendDispatch(dispatchEnvelope())
	got := ft.waitResult(t, 10*time.Second)
	var payload agentmsg.ResultPayload
	if json.Unmarshal(got.Payload, &payload) != nil || payload.Status != "failed" {
		t.Fatalf("expected failed, got: %s", got.Payload)
	}
}

// 重连对账：塔发 reconcile_request，客户端回 finished + 既有结果
func TestClientReconcileFinishedAfterRestart(t *testing.T) {
	ft := newFakeTower(t, CredentialHash("secret-1"))
	runner := &stubRunner{out: successOutcome()}
	cfg := testConfig(t, ft.URL)
	_, _ = startClient(t, cfg, runner)
	<-ft.reports
	ft.SendDispatch(dispatchEnvelope())
	ft.waitResult(t, 10*time.Second)

	// 模拟塔侧重连对账请求（客户端并未执行中该 attempt）
	env := agentmsg.Envelope{MessageType: agentmsg.MsgReconcileRequest, Payload: mustJSONRaw([]agentmsg.ReconcileAttempt{{
		AttemptID: "att-1", TaskID: "task-1", StepID: "s1", PlanRevision: "1", IdempotencyKey: "idem-1",
	}})}
	ft.SendDispatch(env)
	got := ft.waitResult(t, 10*time.Second)
	if got.MessageType != agentmsg.MsgReconcileResult {
		t.Fatalf("expected reconcile_result, got %q", got.MessageType)
	}
	var outs []agentmsg.ReconcileOutcome
	if json.Unmarshal(got.Payload, &outs) != nil || len(outs) != 1 {
		t.Fatalf("unexpected reconcile payload: %s", got.Payload)
	}
	if outs[0].Status != "finished" || outs[0].Result == nil || outs[0].Result.Status != "completed" {
		t.Fatalf("reconcile outcome unexpected: %+v", outs[0])
	}
}

func mustJSONRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
