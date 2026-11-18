// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package acceptance V1.5 结构化验收验证器（设计 §6.2）
// 机器规则失败 → failed；只剩 manual_review → pending；全部通过 → passed。
// Agent 自报 completed / 大模型摘要不能直接改写验收状态。
package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"

	"macs.local/tower/internal/plan"
)

// 验收状态
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusPending = "pending"
)

// ResultView 验收输入视图
type ResultView struct {
	Payload   json.RawMessage // 整个 ResultPayload JSON
	SelfCheck json.RawMessage // self_check 原始 JSON
	Evidence  json.RawMessage // acceptance_evidence 原始 JSON
}

// ValidationResult 验收结论
type ValidationResult struct {
	Status string
	Reason string
}

// Evaluate 执行验收标准列表（内置 json_field_equals / test_report / manual_review）
func Evaluate(criteria []plan.Criterion, view ResultView) ValidationResult {
	machineFailed := []string{}
	manualOnly := true
	for _, c := range criteria {
		switch c.ValidatorKey {
		case "manual_review":
			continue // 人工项：不判失败
		case "json_field_equals":
			if err := validateJSONFieldEquals(c.Params, view.Payload); err != nil {
				machineFailed = append(machineFailed, fmt.Sprintf("json_field_equals: %v", err))
			} else {
				manualOnly = false
			}
		case "test_report":
			if err := validateTestReport(view); err != nil {
				machineFailed = append(machineFailed, fmt.Sprintf("test_report: %v", err))
			} else {
				manualOnly = false
			}
		default:
			machineFailed = append(machineFailed, fmt.Sprintf("unknown validator %q", c.ValidatorKey))
		}
	}
	if len(machineFailed) > 0 {
		return ValidationResult{Status: StatusFailed, Reason: strings.Join(machineFailed, "; ")}
	}
	if manualOnly {
		return ValidationResult{Status: StatusPending, Reason: "manual review required"}
	}
	return ValidationResult{Status: StatusPassed}
}

// validateJSONFieldEquals 参数 {"field":"a.b","value":...}：dotted 路径取值并做 JSON 语义等值比较
func validateJSONFieldEquals(params json.RawMessage, payload json.RawMessage) error {
	var p struct {
		Field string          `json:"field"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(params, &p) != nil || p.Field == "" {
		return fmt.Errorf("params require {field, value}")
	}
	var doc map[string]any
	if json.Unmarshal(payload, &doc) != nil {
		return fmt.Errorf("result payload not a JSON object")
	}
	cur := any(doc)
	for _, seg := range strings.Split(p.Field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return fmt.Errorf("field %q not reachable", p.Field)
		}
		cur, ok = m[seg]
		if !ok {
			return fmt.Errorf("field %q missing", p.Field)
		}
	}
	wantAny, err := json.Marshal(p.Value)
	if err != nil {
		return fmt.Errorf("param value invalid")
	}
	gotAny, _ := json.Marshal(normalizeNumber(cur))
	if string(normalizeJSONBytes(wantAny)) != string(normalizeJSONBytes(gotAny)) {
		return fmt.Errorf("field %q = %s, want %s", p.Field, gotAny, wantAny)
	}
	return nil
}

// normalizeNumber JSON 数字统一 float64 语义
func normalizeNumber(v any) any {
	return v
}

// normalizeJSONBytes 规范化 JSON 字节（键排序由 Marshal 保证确定性；空格去除）
func normalizeJSONBytes(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// validateTestReport 要求 self_check.status == passed（缺自检在调度器层已失败关闭）
func validateTestReport(view ResultView) error {
	if len(view.SelfCheck) == 0 {
		return fmt.Errorf("no self_check attached")
	}
	var sc struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(view.SelfCheck, &sc) != nil {
		return fmt.Errorf("self_check not a JSON object")
	}
	if sc.Status != "passed" {
		return fmt.Errorf("self_check status = %q", sc.Status)
	}
	return nil
}
