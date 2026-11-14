// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package quality

import (
	"encoding/json"
	"testing"

	"macs.local/tower/internal/store"
)

// §12.1 单元测试：自检报告提取（兼容缺省 = passed，2.x 孵化器不受影响）
func TestSelfCheckFromResult(t *testing.T) {
	def := SelfCheckFromResult(`{"mode":"text_analysis","chars":3}`)
	if def.Status != "passed" {
		t.Errorf("无 self_check 字段应视为 passed（向后兼容），got %s", def.Status)
	}
	withCheck := SelfCheckFromResult(`{"self_check":{"status":"failed","items":[{"name":"a","passed":false}]}}`)
	if withCheck.Status != "failed" || len(withCheck.Items) != 1 {
		t.Errorf("应提取内嵌自检报告，got %+v", withCheck)
	}
}

// §6.4.10 done 验收门：reviewable 判定（确定性模式默认复核；Agent 模式须显式 review）
func TestReviewable(t *testing.T) {
	cases := []struct {
		payload string
		want    bool
	}{
		{`{"mode":"text_analysis"}`, true},
		{`{"mode":"merge"}`, true},
		{`{"mode":"cli_agent"}`, false},           // Agent 模式默认免复核（防重复副作用）
		{`{"mode":"cli_agent","review":true}`, true}, // 显式要求则复核
		{`{"mode":"openai_compatible"}`, false},
		{`{"mode":"openai_compatible","review":true}`, true},
		{`{}`, false},
		{`not-json`, false},
	}
	for i, c := range cases {
		if got := reviewable(store.Subtask{Payload: c.payload}); got != c.want {
			t.Errorf("case %d payload=%s reviewable=%v want %v", i, c.payload, got, c.want)
		}
	}
}

// Q7 抽样比例边界：关闭/0 比例必不抽样
func TestShouldSampleDisabled(t *testing.T) {
	s := &Service{cfg: Config{SpotCheckEnabled: false, SpotCheckRatio: 20}}
	for i := 0; i < 100; i++ {
		if s.ShouldSample() {
			t.Fatal("开关关闭时不得抽样")
		}
	}
	s2 := &Service{cfg: Config{SpotCheckEnabled: true, SpotCheckRatio: 0}}
	if s2.ShouldSample() {
		t.Fatal("比例为 0 时不得抽样")
	}
}

// 复核载荷可被 Worker 端还原（契约对齐：review_job 内嵌原任务 payload）
func TestReviewPayloadShape(t *testing.T) {
	orig := map[string]any{"mode": "text_analysis", "shard": "A", "text": "hello"}
	b, _ := json.Marshal(orig)
	payload, _ := json.Marshal(map[string]any{
		"mode": "review", "review_job": json.RawMessage(b), "review_expect": "sha256:x", "review_round": 0,
	})
	var p struct {
		Mode       string          `json:"mode"`
		ReviewJob  json.RawMessage `json:"review_job"`
		ReviewExp  string          `json:"review_expect"`
		Round      int             `json:"review_round"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Mode != "review" || p.Round != 0 {
		t.Errorf("复核载荷契约破坏: %s", payload)
	}
	var inner map[string]any
	if json.Unmarshal(p.ReviewJob, &inner) != nil || inner["mode"] != "text_analysis" {
		t.Errorf("review_job 应可还原原任务: %s", p.ReviewJob)
	}
}
