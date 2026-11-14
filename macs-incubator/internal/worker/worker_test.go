// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

package worker

import (
	"encoding/json"
	"testing"
)

// §12.1：指纹确定性（复核比对前提）
func TestFingerprintDeterministic(t *testing.T) {
	a := analyze("A", "你好 MACS 123")
	b := analyze("A", "你好 MACS 123")
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("同输入指纹应一致")
	}
	if Fingerprint(a) == Fingerprint(analyze("A", "你好 MACS 124")) {
		t.Error("不同输入指纹应不同")
	}
}

// 自检引擎：text_analysis 通过 / 缺文本失败
func TestSelfCheck(t *testing.T) {
	job := Job{Mode: "text_analysis", Text: "abc"}
	out := Run(job)
	sc := Check(job, out)
	if sc.Status != "passed" {
		t.Errorf("合法解析应自检通过，got %+v", sc)
	}
	badOut := Outcome{Status: "failed", Error: "empty text"}
	sc2 := Check(job, badOut)
	if sc2.Status != "failed" {
		t.Errorf("失败执行应自检 failed，got %s", sc2.Status)
	}
}

// 复核模式（FR-14）：确定性任务指纹一致 → approved
func TestReviewApprovedForDeterministic(t *testing.T) {
	orig := Job{Mode: "text_analysis", Text: "审查目标文本", Shard: "A"}
	origOut := Run(orig)
	expect := Fingerprint(origOut.Result)

	origJSON, _ := json.Marshal(orig)
	review := Run(Job{
		Mode: "review", ReviewJob: origJSON, ReviewExpect: expect, ReviewRound: 0,
	})
	if review.Status != "completed" {
		t.Fatalf("复核应完成，got %s (%s)", review.Status, review.Error)
	}
	var verdict struct {
		Status           string `json:"status"`
		FingerprintMatch bool   `json:"fingerprint_match"`
	}
	if json.Unmarshal([]byte(review.Result), &verdict) != nil || verdict.Status != "approved" || !verdict.FingerprintMatch {
		t.Errorf("确定性指纹一致应 approved，got %s", review.Result)
	}
}

// 复核模式：期望指纹与重跑不一致 → rejected（E04 打回）
func TestReviewRejectsMismatch(t *testing.T) {
	orig := Job{Mode: "text_analysis", Text: "另一段文本"}
	origJSON, _ := json.Marshal(orig)
	review := Run(Job{
		Mode: "review", ReviewJob: origJSON,
		ReviewExpect: "sha256:mismatched", ReviewRound: 1,
	})
	var verdict struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(review.Result), &verdict) != nil || verdict.Status != "rejected" {
		t.Errorf("指纹不一致应 rejected，got %s", review.Result)
	}
}

// merge 汇总（FR-15）：分片累加 + 非完成依赖拒绝
func TestMerge(t *testing.T) {
	a := Run(Job{Mode: "text_analysis", Text: "hello 世界", Shard: "A"})
	b := Run(Job{Mode: "text_analysis", Text: "MACS 2026", Shard: "B"})
	deps := []Dep{
		{SubtaskID: "a", Title: "A", Status: "completed", Result: a.Result},
		{SubtaskID: "b", Title: "B", Status: "completed", Result: b.Result},
	}
	out := Run(Job{Mode: "merge", Deps: deps})
	if out.Status != "completed" {
		t.Fatalf("merge 应完成: %s", out.Error)
	}
	var merged struct {
		Merged      bool `json:"merged"`
		ShardCount  int  `json:"shard_count"`
		TotalChars  int  `json:"total_chars"`
	}
	if json.Unmarshal([]byte(out.Result), &merged) != nil || !merged.Merged || merged.ShardCount != 2 {
		t.Errorf("merge 结果字段异常: %s", out.Result)
	}
	// 依赖未完成 → 拒绝合并（质量门）
	deps[1].Status = "failed"
	if out2 := Run(Job{Mode: "merge", Deps: deps}); out2.Status != "failed" {
		t.Error("依赖失败时 merge 应失败")
	}
}
