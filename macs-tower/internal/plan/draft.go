// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package plan V1.5 PlanDraft 解码与确定性校验（设计 §5）
// PlanDraft 禁止可执行配置字段（command/args/url/headers/凭据等）；
// instruction 不会被当作 shell 命令执行——执行意图只能经 action 白名单表达。
package plan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Draft PlanDraft（人工/LLM 产出，塔只做确定性校验）
type Draft struct {
	Goal              string `json:"goal"`
	TargetEnvironment string `json:"target_environment,omitempty"`
	Steps             []Step `json:"steps"`
	TimeoutMS         int64  `json:"timeout_ms,omitempty"`
}

// Step 计划步骤（设计 §5.1）
type Step struct {
	StepKey            string      `json:"step_key"`
	Title              string      `json:"title,omitempty"`
	Instruction        string      `json:"instruction"`
	AgentID            string      `json:"agent_id"`
	ActionKey          string      `json:"action_key"`
	StepKind           string      `json:"step_kind,omitempty"` // V1.5 只支持 execute
	DependsOn          []string    `json:"depends_on,omitempty"`
	ExpectedOutputs    []string    `json:"expected_outputs,omitempty"`
	InputRefs          []string    `json:"input_refs,omitempty"` // 声明需要的上游制品类型
	AcceptanceCriteria []Criterion `json:"acceptance_criteria"`
	TimeoutMS          int64       `json:"timeout_ms,omitempty"`
}

// Criterion 验收规则：引用 action 已登记的 validator_key（设计 §5.1）
type Criterion struct {
	ValidatorKey string          `json:"validator_key"`
	Params       json.RawMessage `json:"params,omitempty"`
}

// MaxSteps / DefaultStepTimeout / MaxStepTimeout 步骤限制（设计 §5.2）
const (
	MaxSteps         = 16
	DefaultStepTimeo = 30 * 60 * 1000
	MaxStepTimeout   = 4 * 60 * 60 * 1000
)

// forbiddenFields PlanDraft 禁用字段（设计 §5.1/§10；大小写不敏感）
var forbiddenFields = []string{
	"command", "args", "cwd", "stdin", "url", "method", "headers", "body",
	"password", "passwd", "secret", "token", "authorization", "api_key", "apikey", "bearer",
}

// DecodeDraft 解码草案并拒绝任何禁用字段（递归扫描嵌套结构）
func DecodeDraft(data []byte) (*Draft, error) {
	if bad := findForbiddenKey(data, ""); bad != "" {
		return nil, fmt.Errorf("draft contains forbidden field %q", bad)
	}
	var d Draft
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("draft decode: %w", err)
	}
	return &d, nil
}

// findForbiddenKey 递归扫描 JSON 中的禁用键，返回首个命中键
func findForbiddenKey(data []byte, prefix string) string {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	return scanForbidden(v, prefix)
}

func scanForbidden(v any, prefix string) string {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			lk := strings.ToLower(k)
			for _, bad := range forbiddenFields {
				if lk == bad {
					return fullKey(prefix, k)
				}
			}
			if hit := scanForbidden(sub, fullKey(prefix, k)); hit != "" {
				return hit
			}
		}
	case []any:
		for i, sub := range t {
			if hit := scanForbidden(sub, fmt.Sprintf("%s[%d]", prefix, i)); hit != "" {
				return hit
			}
		}
	}
	return ""
}

func fullKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// NormalizeInstruction 规范化指令（input_digest 输入；空白折叠）
func NormalizeInstruction(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// ComputeInputDigest 冻结分派摘要（设计 §3.4）：
// SHA256(plan_revision | step_id | action_id | environment | normalized_instruction | sorted(input_ref_digests))
func ComputeInputDigest(revision int, stepID, actionID, environment, instruction string, inputRefDigests []string) string {
	refs := append([]string{}, inputRefDigests...)
	sort.Strings(refs)
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s",
		revision, stepID, actionID, environment, NormalizeInstruction(instruction), strings.Join(refs, ","))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ComputeIdempotencyKey 稳定幂等键（设计 §3.5）：SHA256(task_id|step_key|plan_revision)
func ComputeIdempotencyKey(taskID, stepKey string, revision int) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d", taskID, stepKey, revision)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// MarshalDraft 草案序列化（提交/修订存储用）
func MarshalDraft(d *Draft) (string, error) {
	if bad := findForbiddenKey(mustJSON(d), ""); bad != "" {
		return "", fmt.Errorf("draft contains forbidden field %q", bad)
	}
	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
