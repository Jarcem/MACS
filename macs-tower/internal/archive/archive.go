// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package archive 模块 #11：归档中心（FR-18）
// Worker 完成即生成 handoff 交接文档；任务完成生成 final_summary；云端只存文档与元数据（硬约束：不存产物文件）
package archive

import (
	"fmt"
	"strings"
	"time"

	"macs.local/tower/internal/store"
)

type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service { return &Service{st: st} }

func wordCount(s string) int { return len(strings.Fields(s)) }

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// HandoffForSubtask 子任务交接文档（Markdown 标准模板：设计方案/流程/结果/待办）
func (s *Service) HandoffForSubtask(task store.Task, sub store.Subtask) error {
	title := fmt.Sprintf("交接文档 · %s", sub.Title)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "> 任务目标：%s\n\n", task.Goal)
	b.WriteString("## 一、执行概要\n\n")
	fmt.Fprintf(&b, "| 项 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| 子任务 | %s |\n", sub.ID)
	fmt.Fprintf(&b, "| 执行节点 | %s |\n", sub.IncubatorID)
	fmt.Fprintf(&b, "| Worker | %s |\n", sub.WorkerID)
	fmt.Fprintf(&b, "| 状态 | %s |\n", sub.Status)
	fmt.Fprintf(&b, "| 开始 → 结束 | %s → %s |\n", sub.StartedAt, sub.FinishedAt)
	fmt.Fprintf(&b, "| 产物指纹 | `%s` |\n\n", sub.Fingerprint)
	b.WriteString("## 二、执行结果\n\n```json\n")
	b.WriteString(sub.Result)
	b.WriteString("\n```\n\n")
	if sub.Error != "" {
		fmt.Fprintf(&b, "## 异常记录\n\n%s\n\n", sub.Error)
	}
	b.WriteString("## 三、待办与交接\n\n- [ ] 下游子任务可引用本结果（`{{deps}}`）\n- [ ] 抽查通过后随任务归档\n")

	doc := store.ArchiveDoc{
		ID:          store.NewID(),
		TaskID:      task.ID,
		SubtaskID:   sub.ID,
		IncubatorID: sub.IncubatorID,
		Source:      "worker",
		DocType:     "handoff",
		Title:       title,
		Content:     b.String(),
		WordCount:   wordCount(b.String()),
		HasCode:     true,
		Version:     1,
	}
	return s.st.InsertArchiveDoc(doc)
}

// FinalSummary 任务终审汇总（归档中心检索入口）
func (s *Service) FinalSummary(task store.Task, subs []store.Subtask, spotPassed string, costCents float64) error {
	title := fmt.Sprintf("任务终审汇总 · %s", firstLine(task.Goal))
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- 目标：%s\n", task.Goal)
	fmt.Fprintf(&b, "- 状态：%s（进度 %d%%）\n", task.Status, task.Progress)
	fmt.Fprintf(&b, "- 创建：%s · 更新：%s\n", task.CreatedAt, task.UpdatedAt)
	if spotPassed != "" {
		fmt.Fprintf(&b, "- 抽查：%s\n", spotPassed)
	}
	fmt.Fprintf(&b, "- 成本：%.2f 分\n\n", costCents)

	b.WriteString("## 子任务清单\n\n| # | 子任务 | 状态 | 节点 | worker | 指纹 |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, sub := range subs {
		fp := sub.Fingerprint
		if len(fp) > 18 {
			fp = fp[:18] + "…"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | `%s` |\n", sub.Seq, sub.Title, sub.Status, sub.IncubatorID, sub.WorkerID, fp)
	}

	b.WriteString("\n## 交接文档索引\n\n")
	for _, sub := range subs {
		if sub.Status == "completed" {
			fmt.Fprintf(&b, "- handoff · %s\n", sub.Title)
		}
	}

	doc := store.ArchiveDoc{
		ID:        store.NewID(),
		TaskID:    task.ID,
		Source:    "tower",
		DocType:   "final_summary",
		Title:     title,
		Content:   b.String(),
		WordCount: wordCount(b.String()),
		Version:   1,
	}
	return s.st.InsertArchiveDoc(doc)
}

// ReviewDoc 复核报告归档（E04 留痕）
func (s *Service) ReviewDoc(taskID string, review store.Subtask, original store.Subtask, verdict, note string) error {
	title := fmt.Sprintf("复核报告 · %s", original.Title)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- 原子任务：%s（fp `%s`）\n", original.ID, original.Fingerprint)
	fmt.Fprintf(&b, "- 复核子任务：%s（worker %s）\n", review.ID, review.WorkerID)
	fmt.Fprintf(&b, "- 结论：%s\n", verdict)
	fmt.Fprintf(&b, "- 说明：%s\n\n", note)
	b.WriteString("## 复核输出\n\n```json\n" + review.Result + "\n```\n")

	doc := store.ArchiveDoc{
		ID:        store.NewID(),
		TaskID:    taskID,
		SubtaskID: original.ID,
		Source:    "reviewer",
		DocType:   "review",
		Title:     title,
		Content:   b.String(),
		WordCount: wordCount(b.String()),
		Version:   1,
	}
	return s.st.InsertArchiveDoc(doc)
}

var _ = time.Now // 占位：时间统一走 store.now()
