// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package agentmsg V1.5 WSS 信封协议（设计 §6.1/§6.2）
// 塔与 Connector 共享的单一事实源：macs-incubator 经 replace 依赖本包。
// WSS 是唯一跨网络 Agent 控制通道（设计 §2），旧 UDP 不作为降级路径。
package agentmsg

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// ProtocolVersion 当前接入协议版本；网关拒绝其他版本
const ProtocolVersion = "1.0"

// 消息类型全集
const (
	MsgHello            = "hello"              // Connector → Tower：认证 + 协议版本
	MsgHelloAck         = "hello_ack"          // Tower → Connector：认证通过
	MsgError            = "error"              // 双向：协议/校验错误
	MsgHeartbeat        = "heartbeat"          // Connector → Tower：健康/负载/在线
	MsgAgentReport      = "agent_report"       // Connector → Tower：本地 Agent 候选上报
	MsgDispatch         = "dispatch"           // Tower → Connector：冻结分派执行
	MsgDispatchAck      = "dispatch_ack"       // Connector → Tower：分派已接收
	MsgResult           = "result"             // Connector → Tower：结构化结果
	MsgResultAck        = "result_ack"         // Tower → Connector：结果已落库（outbox MarkSynced 依据）
	MsgReconcileRequest = "reconcile_request"  // Tower → Connector：重连对账（设计 §8）
	MsgReconcileResult  = "reconcile_result"   // Connector → Tower：对账回执
	MsgCancel           = "cancel"             // Tower → Connector：任务取消（执行中按取消处理）
)

// ArtifactRef 跨机制品引用（URI 只允许 s3:// oci:// https://，设计 §7）
type ArtifactRef struct {
	URI       string `json:"uri"`
	Digest    string `json:"digest"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
}

// Envelope 统一消息信封。认证后 connector_id 以连接绑定为准，
// 消息体中的 connector_id 仅作一致性校验（防止串号）。
type Envelope struct {
	MessageID       string          `json:"message_id"`
	MessageType     string          `json:"message_type"`
	ProtocolVersion string          `json:"protocol_version,omitempty"`
	InReplyTo       string          `json:"in_reply_to,omitempty"`
	ConnectorID     string          `json:"connector_id,omitempty"`
	CredentialHash  string          `json:"credential_hash,omitempty"` // 仅 hello 携带
	AgentID         string          `json:"agent_id,omitempty"`
	LocalAgentKey   string          `json:"local_agent_key,omitempty"`
	TaskID          string          `json:"task_id,omitempty"`
	StepID          string          `json:"step_id,omitempty"`
	PlanRevision    string          `json:"plan_revision,omitempty"`
	ActionID        string          `json:"action_id,omitempty"`
	ActionKey       string          `json:"action_key,omitempty"`
	AttemptID       string          `json:"attempt_id,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	Environment     string          `json:"environment,omitempty"`
	InputDigest     string          `json:"input_digest,omitempty"`
	LeaseExpiresAt  string          `json:"lease_expires_at,omitempty"`
	Timeout         string          `json:"timeout,omitempty"`
	InputRefs       []ArtifactRef   `json:"input_refs,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// ResultPayload 结构化结果（设计 §6.2）。status 是执行适配器状态，不等于验收通过；
// 缺 self_check 时控制塔按 failed 关闭（Task 5 失败关闭语义）。
type ResultPayload struct {
	Status             string          `json:"status"`
	Summary            string          `json:"summary,omitempty"`
	ResultRef          string          `json:"result_ref,omitempty"`
	ArtifactRefs       []ArtifactRef   `json:"artifact_refs,omitempty"`
	SelfCheck          json.RawMessage `json:"self_check,omitempty"`
	AcceptanceEvidence json.RawMessage `json:"acceptance_evidence,omitempty"`
	TokenUsage         struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
	} `json:"token_usage"`
	Error string `json:"error,omitempty"`
}

// HeartbeatAgent 心跳中的单个 Agent 健康（只更新健康/负载/在线，设计 §4.3）
type HeartbeatAgent struct {
	LocalAgentKey string          `json:"local_agent_key"`
	HealthStatus  string          `json:"health_status"`
	Load          json.RawMessage `json:"load,omitempty"`
}

// CandidateAgent 候选上报（重连只刷新 reported_json + 在线状态）
type CandidateAgent struct {
	LocalAgentKey string          `json:"local_agent_key"`
	DisplayName   string          `json:"display_name"`
	Reported      json.RawMessage `json:"reported,omitempty"`
}

// ReconcileAttempt 塔侧对账请求项
type ReconcileAttempt struct {
	AttemptID      string `json:"attempt_id"`
	TaskID         string `json:"task_id,omitempty"`
	StepID         string `json:"step_id,omitempty"`
	PlanRevision   string `json:"plan_revision,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// ReconcileOutcome Connector 对账回执项：running（执行中）/ finished（已完成带结果）/ unknown（状态不明）
type ReconcileOutcome struct {
	AttemptID string          `json:"attempt_id"`
	Status    string          `json:"status"` // running / finished / unknown
	Result    *ResultPayload  `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// NewID 生成消息/事件 ID（16 字节随机 hex）
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Marshal 信封编码（发送侧统一入口）
func Marshal(e Envelope) ([]byte, error) {
	if e.MessageID == "" {
		e.MessageID = NewID()
	}
	return json.Marshal(e)
}

// Unmarshal 信封解码
func Unmarshal(b []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(b, &e)
	return e, err
}
