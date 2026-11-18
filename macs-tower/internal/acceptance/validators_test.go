// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package acceptance

import (
	"encoding/json"
	"testing"

	"macs.local/tower/internal/plan"
)

func view(payload string, selfCheck string) ResultView {
	return ResultView{Payload: json.RawMessage(payload), SelfCheck: json.RawMessage(selfCheck)}
}

// V1.5 Task 5：机器规则失败 → failed；只剩 manual_review → pending；全部通过 → passed
func TestEvaluateValidators(t *testing.T) {
	payload := `{"status":"completed","summary":"did it","token_usage":{"input":10,"output":20}}`
	// json_field_equals 通过
	res := Evaluate([]plan.Criterion{{ValidatorKey: "json_field_equals",
		Params: json.RawMessage(`{"field":"token_usage.input","value":10}`)}}, view(payload, `{"status":"passed"}`))
	if res.Status != StatusPassed {
		t.Fatalf("expected passed: %+v", res)
	}
	// json_field_equals 失败
	res = Evaluate([]plan.Criterion{{ValidatorKey: "json_field_equals",
		Params: json.RawMessage(`{"field":"summary","value":"other"}`)}}, view(payload, `{"status":"passed"}`))
	if res.Status != StatusFailed || res.Reason == "" {
		t.Fatalf("expected failed: %+v", res)
	}
	// test_report：自检 passed / failed
	res = Evaluate([]plan.Criterion{{ValidatorKey: "test_report"}}, view(payload, `{"status":"passed"}`))
	if res.Status != StatusPassed {
		t.Fatalf("test_report pass expected: %+v", res)
	}
	res = Evaluate([]plan.Criterion{{ValidatorKey: "test_report"}}, view(payload, `{"status":"failed"}`))
	if res.Status != StatusFailed {
		t.Fatalf("test_report fail expected: %+v", res)
	}
	// 只剩 manual_review → pending（转人工）
	res = Evaluate([]plan.Criterion{{ValidatorKey: "manual_review"}}, view(payload, `{"status":"passed"}`))
	if res.Status != StatusPending {
		t.Fatalf("manual_review pending expected: %+v", res)
	}
	// 机器失败优先于 pending
	res = Evaluate([]plan.Criterion{
		{ValidatorKey: "manual_review"},
		{ValidatorKey: "test_report"},
	}, view(payload, `{"status":"failed"}`))
	if res.Status != StatusFailed {
		t.Fatalf("machine failure must win: %+v", res)
	}
	// 未知验证器 → failed（fail-closed）
	res = Evaluate([]plan.Criterion{{ValidatorKey: "hack_validator"}}, view(payload, `{"status":"passed"}`))
	if res.Status != StatusFailed {
		t.Fatalf("unknown validator must fail closed: %+v", res)
	}
}
