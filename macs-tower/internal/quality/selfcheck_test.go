package quality_test

import (
	"testing"

	"macs.local/tower/internal/quality"
)

// V1.5 Task 5：缺 self_check 失败关闭（设计 §6.2；与 2.x 兼容路径分离）
func TestMissingSelfCheckFailsClosed(t *testing.T) {
	report := quality.SelfCheckFromResultStrict(`{"status":"completed","summary":"done"}`)
	if report.Status != "failed" {
		t.Fatalf("missing self_check passed: %+v", report)
	}
	report = quality.SelfCheckFromResultStrict(`{"status":"completed","self_check":{"status":"passed","findings":[]}}`)
	if report.Status != "passed" {
		t.Fatalf("structured self_check rejected: %+v", report)
	}
	report = quality.SelfCheckFromResultStrict(`{"status":"completed","self_check":{}}`)
	if report.Status != "failed" {
		t.Fatalf("empty status passed: %+v", report)
	}
	// 非 JSON 结果也失败关闭
	if r := quality.SelfCheckFromResultStrict(`plain text`); r.Status != "failed" {
		t.Fatalf("plain text passed: %+v", r)
	}
	// 2.x 兼容路径语义不变（缺省 passed）
	if r := quality.SelfCheckFromResult(`{"status":"completed"}`); r.Status != "passed" {
		t.Fatalf("legacy semantics changed: %+v", r)
	}
}
