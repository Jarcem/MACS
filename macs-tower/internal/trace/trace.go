// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package trace 模块 #10：追溯链服务（FR-17）
// 指纹链算法 6.4.4：无父产物 fp=sha256(content)；有父产物 fp=sha256(parent_fp+content)
// E06 校验：对账时重算指纹，两端不一致即触发告警并冻结相关任务
package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"macs.local/tower/internal/store"
)

// ChainFingerprint 6.4.4 指纹链计算（单一事实源）
func ChainFingerprint(content, parentFingerprint string) string {
	h := sha256.New()
	if parentFingerprint != "" {
		h.Write([]byte(parentFingerprint))
	}
	h.Write([]byte(content))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// HashHex 任意内容的裸 sha256（命令终端密钥哈希等复用）
func HashHex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service { return &Service{st: st} }

// Record 产物落链：task→subtask→result，depth 按 task(0)→subtask(1) 递进
// UNIQUE(subtask_id, fingerprint) 幂等：重复上报不产生重复链节点
func (s *Service) Record(taskID, subtaskID, fingerprint, summary, artifactType string, sizeBytes int64) error {
	depth := 1
	if subtaskID == "" {
		depth = 0
	}
	return s.st.InsertOriginTrace(store.OriginTrace{
		ID:             store.NewID(),
		TaskID:         taskID,
		SubtaskID:      subtaskID,
		FromKind:       "task",
		FromID:         taskID,
		ToKind:         "result",
		ToID:           subtaskID,
		Fingerprint:    fingerprint,
		FingerprintAlg: "sha256",
		ContentSummary: summary,
		ArtifactType:   artifactType,
		SizeBytes:      sizeBytes,
		Depth:          depth,
		Metadata:       fmt.Sprintf(`{"parent":"task_root"}`),
	})
}

// ComputeFor 子任务产物指纹：链父 = 任务根指纹（6.4.4 有父产物 sha256(parent+content)）。
// 并行完成的子任务链父相同（任务根），保证计算与对账确定性一致，不受完成顺序竞态影响。
func (s *Service) ComputeFor(taskID, content string) string {
	root, _ := s.st.GetTaskRootFingerprint(taskID)
	return ChainFingerprint(content, root)
}

// VerifyTask E06 对账：按任务根链重算全部子任务指纹与链上记录比对。
// 复核子任务（kind=review）的输出是对账元数据而非产物，不入链。
// 返回不一致清单；任何不一致 → 相关任务冻结等待人工核查（不自动回滚，防误判）
func (s *Service) VerifyTask(taskID string) ([]string, error) {
	root, _ := s.st.GetTaskRootFingerprint(taskID)
	subs, err := s.st.ListSubtasks(taskID)
	if err != nil {
		return nil, err
	}
	var broken []string
	for _, sub := range subs {
		if sub.Kind == "review" || sub.Result == "" || sub.Status != "completed" {
			continue
		}
		expect := ChainFingerprint(sub.Result, root)
		if sub.Fingerprint != expect {
			broken = append(broken, sub.ID)
		}
	}
	return broken, nil
}
