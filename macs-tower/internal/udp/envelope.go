// Package udp 是 MACS UDP 消息契约的单一事实源（主设计附录 B.1，V3.5 全集）。
// incubator / cmdterm 复制本文件或 import 本模块，禁止各自另写类型定义。
package udp

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
)

// 信封版本：V3.0 起携带；缺失视为 2.x，未知字段忽略（向后兼容）
const (
	Version2x = "2.x"
	Version30 = "3.0"
	Version34 = "3.4"
	Version35 = "3.5"
)

// 消息类型（附录 B.1 全集）
const (
	// 上行：孵化器/命令终端 → 调度塔
	MsgRegister        = "register"
	MsgReport          = "report"
	MsgFetchRequest    = "fetch_request"
	MsgExecutionResult = "execution_result"
	MsgTelemetryStream = "telemetry_stream"
	MsgPlatformEvent   = "platform_event"
	MsgCmdResult       = "cmd_result"
	MsgCmdRegister     = "cmd_register"   // V3.5 §6.4.14
	MsgCmdHeartbeat    = "cmd_heartbeat"  // V3.5 §6.4.14
	// 下行：调度塔 → 孵化器/命令终端
	MsgDispatch            = "dispatch"
	MsgWorkerSpawn         = "worker_spawn"
	MsgWorkerRollback      = "worker_rollback"
	MsgBudgetChanged       = "budget_changed"
	MsgAck                 = "ack"
	MsgNotice              = "notice"
	MsgConfigUpdate        = "config_update"
	MsgCapabilityQuery     = "capability_query"
	MsgAdapterConfigUpdate = "adapter_config_update"
	MsgCmdExec             = "cmd_exec" // V3.4 模块 #32
)

// TelemetryMetrics 统一遥测指标（§6.4.13.4）
type TelemetryMetrics struct {
	TokenUsage      *TokenUsage `json:"token_usage,omitempty"`
	ProgressPercent *int        `json:"progress_percent,omitempty"`
	CurrentStep     string      `json:"current_step,omitempty"`
	CostCents       *int        `json:"cost_cents,omitempty"`
	DurationMs      *int64      `json:"duration_ms,omitempty"`
}

type TokenUsage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Cached     int `json:"cached,omitempty"`
}

// Envelope UDP 消息信封；Payload 按 Type 二次解析，未知类型不报错（向后兼容）
type Envelope struct {
	MsgID           string          `json:"msg_id"` // uuid-v4，幂等去重键
	Type            string          `json:"type"`
	Version         string          `json:"version,omitempty"`
	ProtocolDriver  string          `json:"protocol_driver,omitempty"`
	PlatformAdapter string          `json:"platform_adapter,omitempty"`
	InReplyTo       string          `json:"in_reply_to,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// NewID 生成 uuid-v4（crypto/rand）
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// New 构造 V3.5 信封并序列化 payload
func New(msgType string, payload any) (Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{MsgID: NewID(), Type: msgType, Version: Version35, Payload: b}, nil
}
