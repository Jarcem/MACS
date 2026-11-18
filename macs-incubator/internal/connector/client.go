// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// client.go V1.5 Connector 出站客户端（设计 §2/§6/§8）
//   - 仅出站 WSS 连接，不开放入站端口；带抖动指数退避重连
//   - hello 认证（凭据只发 SHA-256）→ 候选上报 → 心跳
//   - dispatch 校验（本机/原 Agent/原 action/input_digest）→ 下载制品校验 digest
//     → 构造 Worker Job 执行 → 结果先写 outbox 再发送（result_ack 后 MarkSynced）
//   - idempotency_key 去重重复分派；重连对账执行中 attempt
package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"macs.local/incubator/internal/worker"

	"macs.local/tower/agentmsg"
)

const maxMessageSize = 4 << 20 // 单条消息上限（结果/制品引用）

// ErrPermanent 永久性错误（凭据错误/协议不匹配）：重连无意义，客户端退出
var ErrPermanent = errors.New("permanent connector error")

type activeJob struct {
	cancel context.CancelFunc
	env    agentmsg.Envelope
}

type Client struct {
	cfg    Config
	reg    *Registry
	outbox *Outbox
	runner Runner

	writeMu sync.Mutex
	conn    *websocket.Conn

	mu     sync.Mutex
	active map[string]*activeJob // attempt_id → 执行中任务
}

func NewClient(cfg Config, reg *Registry, outbox *Outbox, runner Runner) (*Client, error) {
	if reg == nil {
		reg = NewRegistry(cfg)
	}
	if runner == nil {
		runner = DefaultRunner()
	}
	return &Client{cfg: cfg, reg: reg, outbox: outbox, runner: runner, active: map[string]*activeJob{}}, nil
}

// CredentialHash 认证哈希（明文凭据只在本机；链路只出现 SHA-256）
func CredentialHash(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

// Run 主循环：连接 → 服务 → 断线重连（带抖动指数退避，设计 §8）
func (c *Client) Run(ctx context.Context) error {
	backoff := 1 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrPermanent) {
			log.Printf(`{"level":"error","module":"connector","msg":"permanent failure: %v"}`, err)
			return err
		}
		if err != nil {
			log.Printf(`{"level":"warn","module":"connector","msg":"disconnected: %v"}`, err)
		}
		sleep := backoff + time.Duration(rand.Int63n(int64(backoff/2+1))) // ±抖动
		log.Printf(`{"level":"info","module":"connector","msg":"reconnecting in %s"}`, sleep)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) connectAndServe(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.cfg.TowerURL, &websocket.DialOptions{})
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.cfg.TowerURL, err)
	}
	conn.SetReadLimit(maxMessageSize)
	c.conn = conn
	defer func() {
		c.conn = nil
		conn.Close(websocket.StatusNormalClosure, "client closed")
	}()

	// hello 认证
	if err := c.send(ctx, agentmsg.Envelope{
		MessageType: agentmsg.MsgHello, ProtocolVersion: agentmsg.ProtocolVersion,
		ConnectorID: c.cfg.ConnectorID, CredentialHash: CredentialHash(c.cfg.Credential),
	}); err != nil {
		return err
	}
	reply, err := c.read(ctx)
	if err != nil {
		return err
	}
	switch reply.MessageType {
	case agentmsg.MsgHelloAck:
	case agentmsg.MsgError:
		return fmt.Errorf("%w: gateway rejected hello: %s", ErrPermanent, reply.Payload)
	default:
		return fmt.Errorf("%w: unexpected hello reply %q", ErrPermanent, reply.MessageType)
	}

	// 候选上报（重连只刷新 reported_json + 在线状态，塔侧不覆盖人工身份）
	cands := make([]agentmsg.CandidateAgent, 0, len(c.cfg.Agents))
	for _, a := range c.cfg.Agents {
		cands = append(cands, agentmsg.CandidateAgent{
			LocalAgentKey: a.LocalAgentKey, DisplayName: a.DisplayName,
			Reported: mustJSON(map[string]any{"actions": actionKeys(a.Actions)}),
		})
	}
	if err := c.sendPayload(ctx, agentmsg.MsgAgentReport, cands); err != nil {
		return err
	}

	// 恢复未确认 outbox 事件（塔侧按 attempt_id 幂等核销）
	c.resendPending(ctx)

	// 心跳 + 读循环并发
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go c.heartbeatLoop(hbCtx)

	for {
		env, err := c.read(ctx)
		if err != nil {
			return err
		}
		switch env.MessageType {
		case agentmsg.MsgDispatch:
			go c.handleDispatch(ctx, env)
		case agentmsg.MsgResultAck:
			if env.InReplyTo != "" {
				_ = c.outbox.MarkSynced(env.InReplyTo)
			}
		case agentmsg.MsgReconcileRequest:
			c.handleReconcile(ctx, env)
		case agentmsg.MsgCancel:
			c.handleCancel(env)
		case agentmsg.MsgError:
			log.Printf(`{"level":"warn","module":"connector","msg":"gateway error: %s"}`, env.Payload)
		default:
			log.Printf(`{"level":"warn","module":"connector","msg":"unknown message %q"}`, env.MessageType)
		}
	}
}

func actionKeys(actions []LocalActionConfig) []string {
	keys := make([]string, 0, len(actions))
	for _, a := range actions {
		keys = append(keys, a.ActionKey)
	}
	return keys
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(c.cfg.HeartbeatEvery) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		activeN := len(c.active)
		c.mu.Unlock()
		agents := make([]agentmsg.HeartbeatAgent, 0, len(c.cfg.Agents))
		for _, a := range c.cfg.Agents {
			agents = append(agents, agentmsg.HeartbeatAgent{
				LocalAgentKey: a.LocalAgentKey, HealthStatus: "healthy",
				Load: mustJSON(map[string]any{"active": activeN}),
			})
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = c.sendPayload(sctx, agentmsg.MsgHeartbeat, agents) // 失败交给读循环断线处理
		cancel()
	}
}

// resendPending 重连后重发未确认事件（结果幂等：塔侧按 attempt_id 终态保护）
func (c *Client) resendPending(ctx context.Context) {
	events, err := c.outbox.Pending()
	if err != nil {
		return
	}
	for _, ev := range events {
		var env agentmsg.Envelope
		if json.Unmarshal(ev.Payload, &env) != nil || env.MessageType != agentmsg.MsgResult {
			continue
		}
		if err := c.send(ctx, env); err != nil {
			return
		}
	}
}

// ---------- dispatch 处理 ----------

func (c *Client) handleDispatch(ctx context.Context, env agentmsg.Envelope) {
	// 送达确认
	_ = c.send(ctx, agentmsg.Envelope{MessageType: agentmsg.MsgDispatchAck, InReplyTo: env.MessageID,
		AttemptID: env.AttemptID, IdempotencyKey: env.IdempotencyKey})

	if env.IdempotencyKey == "" || env.AttemptID == "" || env.InputDigest == "" {
		c.rejectDispatch(ctx, env, "dispatch missing idempotency_key/attempt_id/input_digest")
		return
	}
	// 幂等去重：重复分派不重复执行，重发既有结果
	if _, resultEventID, resultJSON, ok, _ := c.outbox.LookupDispatch(env.IdempotencyKey); ok && resultJSON != "" {
		var prev agentmsg.Envelope
		if json.Unmarshal([]byte(resultJSON), &prev) == nil && prev.MessageType == agentmsg.MsgResult {
			_ = c.send(ctx, prev)
			return
		}
		if resultEventID != "" {
			return // 执行中：忽略重复分派
		}
	}

	// 本机绑定校验：只执行签发给本机、原 Agent、原 action 的任务（设计 §10）
	actionCfg, ok := c.reg.Lookup(env.LocalAgentKey, env.ActionKey)
	if !ok {
		c.rejectDispatch(ctx, env, fmt.Sprintf("action %s/%s not registered locally", env.LocalAgentKey, env.ActionKey))
		return
	}

	// 上游制品下载 + digest 校验（设计 §6.3.3：不匹配拒绝执行）
	inputPaths, err := c.materializeInputs(ctx, env)
	if err != nil {
		c.failDispatch(ctx, env, "input artifact: "+err.Error())
		return
	}

	timeoutMs := dispatchTimeoutMs(env, actionCfg.TimeoutMs)
	job, err := BuildJob(actionCfg, dispatchInstruction(env), timeoutMs)
	if err != nil {
		c.failDispatch(ctx, env, err.Error())
		return
	}
	job = expandInputs(job, inputPaths)

	// 执行（租约超时内）
	jobCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	c.mu.Lock()
	c.active[env.AttemptID] = &activeJob{cancel: cancel, env: env}
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		delete(c.active, env.AttemptID)
		c.mu.Unlock()
	}()

	outcome := c.runner.Run(jobCtx, job)
	if outcome.Status == "" && jobCtx.Err() != nil {
		// ctx 取消（塔侧 cancel / 关闭）：跳过结果回传（幂等销毁语义）
		return
	}

	payload := c.buildResult(env, outcome)
	c.finishDispatch(ctx, env, payload)
}

func dispatchTimeoutMs(env agentmsg.Envelope, fallback int64) int64 {
	if env.Timeout != "" {
		if d, err := time.ParseDuration(env.Timeout); err == nil && d > 0 {
			return d.Milliseconds()
		}
	}
	if fallback <= 0 {
		return 30 * 60 * 1000
	}
	return fallback
}

func dispatchInstruction(env agentmsg.Envelope) string {
	var p struct {
		Instruction string `json:"instruction"`
	}
	if len(env.Payload) > 0 && json.Unmarshal(env.Payload, &p) == nil && p.Instruction != "" {
		return p.Instruction
	}
	return ""
}

// materializeInputs 下载 input_refs 到工作目录并校验 digest；返回 kind → 本地文件路径
func (c *Client) materializeInputs(ctx context.Context, env agentmsg.Envelope) (map[string]string, error) {
	if len(env.InputRefs) == 0 {
		return nil, nil
	}
	dir := filepath.Join(c.cfg.WorkDir, env.AttemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for i, ref := range env.InputRefs {
		if err := ValidateArtifactRefInbound(ref); err != nil {
			return nil, err
		}
		data, err := DownloadAndVerify(ctx, ref)
		if err != nil {
			return nil, err
		}
		name := fmt.Sprintf("%s-%d.bin", sanitizeKind(ref.Kind), i)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, err
		}
		paths[ref.Kind] = p
	}
	return paths, nil
}

// ValidateArtifactRefInbound 入站引用校验（禁止本地路径进入跨机 input_refs）
func ValidateArtifactRefInbound(r agentmsg.ArtifactRef) error {
	if IsLocalArtifactPath(r.URI) {
		return fmt.Errorf("local path %q rejected as cross-machine artifact ref", r.URI)
	}
	return ValidateArtifactRef(r)
}

func sanitizeKind(kind string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(kind) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// expandInputs 将 {{input:KIND}} 占位符替换为已下载制品路径
func expandInputs(job worker.Job, inputs map[string]string) worker.Job {
	if len(inputs) == 0 {
		return job
	}
	expand := func(s string) string {
		for kind, p := range inputs {
			s = strings.ReplaceAll(s, "{{input:"+kind+"}}", p)
		}
		return s
	}
	for i, arg := range job.Args {
		job.Args[i] = expand(arg)
	}
	job.Stdin = expand(job.Stdin)
	job.Body = expand(job.Body)
	for k, v := range job.Headers {
		job.Headers[k] = expand(v)
	}
	return job
}

// buildResult 由 Worker Outcome 组装结构化结果（设计 §6.2）：
// 结果为 JSON 且含 self_check 时继承结构化字段；纯文本回退为 summary。
// 制品声明为本地路径时必须上传共享存储（失败 → attempt failed，不得以本地路径替代）。
func (c *Client) buildResult(env agentmsg.Envelope, outcome worker.Outcome) agentmsg.ResultPayload {
	var res agentmsg.ResultPayload
	res.Status = outcome.Status
	res.Error = outcome.Error

	var structured struct {
		Summary            string                 `json:"summary"`
		ResultRef          string                 `json:"result_ref"`
		ArtifactRefs       []agentmsg.ArtifactRef `json:"artifact_refs"`
		SelfCheck          json.RawMessage        `json:"self_check"`
		AcceptanceEvidence json.RawMessage        `json:"acceptance_evidence"`
		TokenUsage         *struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"token_usage"`
	}
	if len(outcome.Result) > 0 && json.Unmarshal([]byte(outcome.Result), &structured) == nil &&
		(structured.SelfCheck != nil || structured.ArtifactRefs != nil || structured.ResultRef != "" || structured.TokenUsage != nil) {
		res.Summary = structured.Summary
		res.ResultRef = structured.ResultRef
		res.SelfCheck = structured.SelfCheck
		res.AcceptanceEvidence = structured.AcceptanceEvidence
		if structured.TokenUsage != nil {
			res.TokenUsage.Input = structured.TokenUsage.Input
			res.TokenUsage.Output = structured.TokenUsage.Output
		}
	} else {
		res.Summary = truncate(outcome.Result, 2000)
	}

	// 制品发布（设计 §7.1）
	for _, ref := range structured.ArtifactRefs {
		if !IsLocalArtifactPath(ref.URI) {
			if err := ValidateArtifactRef(ref); err != nil {
				res.Status = "failed"
				res.Error = "artifact ref invalid: " + err.Error()
				return res
			}
			res.ArtifactRefs = append(res.ArtifactRefs, ref)
			continue
		}
		data, err := readWithin(c.cfg.WorkDir, ref.URI)
		if err != nil {
			res.Status = "failed"
			res.Error = "artifact publish: " + err.Error()
			return res
		}
		name := fmt.Sprintf("%s-%s", env.AttemptID, sanitizeKind(ref.Kind))
		up, err := UploadArtifact(context.Background(), data, ref.Kind, c.cfg.Storage, name)
		if err != nil {
			res.Status = "failed"
			res.Error = "artifact upload failed: " + err.Error()
			return res
		}
		res.ArtifactRefs = append(res.ArtifactRefs, up)
	}
	return res
}

// readWithin 只允许读 WorkDir 内的制品文件（防越权读取本机任意文件）
func readWithin(workDir, path string) ([]byte, error) {
	rel, err := filepath.Rel(filepath.Clean(workDir), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("artifact path %q outside work dir", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", path, err)
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// rejectDispatch 绑定/参数校验失败：结果 rejected（塔侧按 failed 关闭，不重试）
func (c *Client) rejectDispatch(ctx context.Context, env agentmsg.Envelope, msg string) {
	payload := agentmsg.ResultPayload{Status: "rejected", Error: msg}
	c.sendResultPayload(ctx, env, payload)
}

// failDispatch 执行前置失败（制品/构造）：attempt failed
func (c *Client) failDispatch(ctx context.Context, env agentmsg.Envelope, msg string) {
	payload := agentmsg.ResultPayload{Status: "failed", Error: msg}
	c.sendResultPayload(ctx, env, payload)
}

func (c *Client) finishDispatch(ctx context.Context, env agentmsg.Envelope, payload agentmsg.ResultPayload) {
	c.sendResultPayload(ctx, env, payload)
}

func (c *Client) sendResultPayload(ctx context.Context, env agentmsg.Envelope, payload agentmsg.ResultPayload) {
	if payload.ArtifactRefs == nil {
		payload.ArtifactRefs = []agentmsg.ArtifactRef{}
	}
	pb, _ := json.Marshal(payload)
	resultEnv := agentmsg.Envelope{
		MessageType:    agentmsg.MsgResult,
		ConnectorID:    c.cfg.ConnectorID,
		AgentID:        env.AgentID,
		LocalAgentKey:  env.LocalAgentKey,
		TaskID:         env.TaskID,
		StepID:         env.StepID,
		PlanRevision:   env.PlanRevision,
		ActionID:       env.ActionID,
		ActionKey:      env.ActionKey,
		AttemptID:      env.AttemptID,
		IdempotencyKey: env.IdempotencyKey,
		Environment:    env.Environment,
		InputDigest:    env.InputDigest,
		Payload:        pb,
	}
	// 先落 outbox（崩溃安全），再发送；result_ack 后 MarkSynced
	// message_id 与 outbox event_id 一致，result_ack（in_reply_to=message_id）才能对上行
	eventID := agentmsg.NewID()
	resultEnv.MessageID = eventID
	rb, _ := json.Marshal(resultEnv)
	if env.IdempotencyKey != "" {
		if _, _, prev, ok, _ := c.outbox.LookupDispatch(env.IdempotencyKey); ok && prev == "" {
			_ = c.outbox.RecordDispatch(env.IdempotencyKey, env.AttemptID, eventID, string(rb))
		} else if ok {
			_ = c.outbox.UpdateDispatchResult(env.IdempotencyKey, eventID, string(rb))
		} else {
			_ = c.outbox.RecordDispatch(env.IdempotencyKey, env.AttemptID, eventID, string(rb))
		}
	}
	if err := c.outbox.Append(OutboxEvent{
		EventID: eventID, ConnectorID: c.cfg.ConnectorID,
		EventType: agentmsg.MsgResult, Payload: rb,
	}); err != nil {
		log.Printf(`{"level":"error","module":"connector","msg":"outbox append: %v"}`, err)
	}
	if err := c.send(ctx, resultEnv); err != nil {
		log.Printf(`{"level":"warn","module":"connector","msg":"result send (kept in outbox %s): %v"}`, eventID, err)
	}
}

// ---------- 对账 / 取消 ----------

// handleReconcile 重连对账（设计 §8）：执行中 → running；有承接记录 → finished+结果；否则 unknown
func (c *Client) handleReconcile(ctx context.Context, env agentmsg.Envelope) {
	var attempts []agentmsg.ReconcileAttempt
	if len(env.Payload) == 0 || json.Unmarshal(env.Payload, &attempts) != nil {
		return
	}
	out := make([]agentmsg.ReconcileOutcome, 0, len(attempts))
	for _, a := range attempts {
		c.mu.Lock()
		_, running := c.active[a.AttemptID]
		c.mu.Unlock()
		if running {
			out = append(out, agentmsg.ReconcileOutcome{AttemptID: a.AttemptID, Status: "running"})
			continue
		}
		if a.IdempotencyKey != "" {
			_, _, resultJSON, ok, _ := c.outbox.LookupDispatch(a.IdempotencyKey)
			if ok && resultJSON != "" {
				var prev agentmsg.Envelope
				if json.Unmarshal([]byte(resultJSON), &prev) == nil && prev.Payload != nil {
					out = append(out, agentmsg.ReconcileOutcome{AttemptID: a.AttemptID, Status: "finished", Result: reconcileResult(prev)})
				}
				continue
			}
		}
		out = append(out, agentmsg.ReconcileOutcome{AttemptID: a.AttemptID, Status: "unknown",
			Error: "no local record (connector restart mid-run)"})
	}
	pb, _ := json.Marshal(out)
	_ = c.send(ctx, agentmsg.Envelope{MessageType: agentmsg.MsgReconcileResult, InReplyTo: env.MessageID, Payload: pb})
}

func reconcileResult(env agentmsg.Envelope) *agentmsg.ResultPayload {
	var p agentmsg.ResultPayload
	if json.Unmarshal(env.Payload, &p) == nil {
		return &p
	}
	return nil
}

// handleCancel 取消（设计 §4.5：执行中任务按取消处理）
func (c *Client) handleCancel(env agentmsg.Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, job := range c.active {
		if env.AttemptID != "" && env.AttemptID == id ||
			(env.AttemptID == "" && env.TaskID != "" && job.env.TaskID == env.TaskID) {
			job.cancel()
			delete(c.active, id)
		}
	}
}

// ---------- 底层收发 ----------

func (c *Client) send(ctx context.Context, env agentmsg.Envelope) error {
	if c.conn == nil {
		return errors.New("not connected")
	}
	if env.MessageID == "" {
		env.MessageID = agentmsg.NewID()
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.conn.Write(wctx, websocket.MessageText, b)
}

func (c *Client) sendPayload(ctx context.Context, msgType string, payload any) error {
	return c.send(ctx, agentmsg.Envelope{
		MessageType: msgType, ConnectorID: c.cfg.ConnectorID, Payload: mustJSON(payload),
	})
}

func (c *Client) read(ctx context.Context) (agentmsg.Envelope, error) {
	if c.conn == nil {
		return agentmsg.Envelope{}, errors.New("not connected")
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, data, err := c.conn.Read(rctx)
	if err != nil {
		return agentmsg.Envelope{}, err
	}
	return agentmsg.Unmarshal(data)
}
