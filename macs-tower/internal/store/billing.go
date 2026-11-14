// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 成本与预算访问层（FR-21，6.4.11 多级预算 + 塔侧上界）
package store

import "time"

// CostRecord 成本记录（金额单位：分）
type CostRecord struct {
	ID          string `json:"id"`
	IncubatorID string `json:"incubator_id"`
	TaskID      string `json:"task_id"`
	SubtaskID   string `json:"subtask_id"`
	WorkerID    string `json:"worker_id"`
	CostType    string `json:"cost_type"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	AmountCents float64 `json:"amount_cents"`
	TokenUsage  string `json:"token_usage"`
	DurationSec int    `json:"duration_sec"`
	Description string `json:"description"`
	RecordedAt  string `json:"recorded_at"`
}

func (s *Store) InsertCostRecord(c CostRecord) error {
	_, err := s.db.Exec(`INSERT INTO cost_records(id,incubator_id,task_id,subtask_id,worker_id,cost_type,provider,model,
		amount_cents,token_usage,duration_sec,description,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.IncubatorID, c.TaskID, c.SubtaskID, c.WorkerID, c.CostType, c.Provider, c.Model,
		c.AmountCents, c.TokenUsage, c.DurationSec, c.Description, now())
	return err
}

func (s *Store) ListCostRecords(taskID string, limit int) ([]CostRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,incubator_id,COALESCE(task_id,''),COALESCE(subtask_id,''),worker_id,cost_type,provider,model,
		amount_cents,COALESCE(token_usage,'{}'),COALESCE(duration_sec,0),description,recorded_at FROM cost_records WHERE 1=1`
	var args []any
	if taskID != "" {
		q += ` AND task_id=?`
		args = append(args, taskID)
	}
	q += ` ORDER BY recorded_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CostRecord{}
	for rows.Next() {
		var c CostRecord
		if err := rows.Scan(&c.ID, &c.IncubatorID, &c.TaskID, &c.SubtaskID, &c.WorkerID, &c.CostType, &c.Provider,
			&c.Model, &c.AmountCents, &c.TokenUsage, &c.DurationSec, &c.Description, &c.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CostSummary 成本聚合（成本看板 §6.5）
type CostSummary struct {
	Scope       string  `json:"scope"`
	ScopeID     string  `json:"scope_id"`
	TotalCents  float64 `json:"total_cents"`
	RecordCount int     `json:"records"`
}

// SumCosts 按 task / incubator / 全局聚合成本
func (s *Store) SumCosts(taskID, incubatorID string) ([]CostSummary, error) {
	dims := []struct {
		scope string
		col   string
		val   string
		filter bool
	}{
		{"task", "COALESCE(task_id,'')", taskID, taskID != ""},
		{"incubator", "COALESCE(incubator_id,'')", incubatorID, incubatorID != ""},
	}
	out := []CostSummary{}
	for _, d := range dims {
		q := `SELECT ` + d.col + ` AS k, COALESCE(SUM(amount_cents),0), COUNT(1) FROM cost_records WHERE ` + d.col + `<>''`
		var args []any
		if d.filter {
			q += ` AND ` + d.col + `=?`
			args = append(args, d.val)
		}
		q += ` GROUP BY k ORDER BY 2 DESC LIMIT 100`
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cs CostSummary
			if err := rows.Scan(&cs.ScopeID, &cs.TotalCents, &cs.RecordCount); err != nil {
				rows.Close()
				return nil, err
			}
			cs.Scope = d.scope
			out = append(out, cs)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaskCostCents 任务累计成本（预算门用）
func (s *Store) TaskCostCents(taskID string) (float64, error) {
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM cost_records WHERE task_id=?`, taskID).Scan(&sum)
	return sum, err
}

// IncubatorMonthlyCents 指定孵化器当月（UTC）累计成本（6.4.11 孵化器级预算门）
func (s *Store) IncubatorMonthlyCents(incubatorID string) (float64, error) {
	monthStart := time.Now().UTC().Format("2006-01") + "-01T00:00:00Z"
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM cost_records
		WHERE incubator_id=? AND recorded_at >= ?`, incubatorID, monthStart).Scan(&sum)
	return sum, err
}

// ResetTemplateMonthlySpend 6.5 预算月度重置：每月滚动清零模板月度累计
func (s *Store) ResetTemplateMonthlySpend() (int64, error) {
	res, err := s.db.Exec(`UPDATE agent_templates SET spent_monthly_cents=0 WHERE spent_monthly_cents<>0`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GlobalCostCents 全局累计成本（企业上界用）
func (s *Store) GlobalCostCents() (float64, error) {
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM cost_records`).Scan(&sum)
	return sum, err
}

// ── 预算上界（6.4.11 budget_limits，塔侧只收紧不放宽）──

type BudgetLimit struct {
	ID           string `json:"id"`
	ScopeType    string `json:"scope_type"`
	ScopeID      string `json:"scope_id"`
	AmountCents  float64 `json:"amount_cents"`
	WarnAtPct    int    `json:"warn_at_pct"`
	EnforceMode  string `json:"enforce_mode"`
	LimitVersion int64  `json:"limit_version"`
	IssuedAt     string `json:"issued_at"`
}

func (s *Store) UpsertBudgetLimit(b BudgetLimit) error {
	_, err := s.db.Exec(`INSERT INTO budget_limits(id,scope_type,scope_id,amount_cents,warn_at_pct,enforce_mode,limit_version,issued_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(scope_type,scope_id) DO UPDATE SET amount_cents=excluded.amount_cents, warn_at_pct=excluded.warn_at_pct,
		enforce_mode=excluded.enforce_mode, limit_version=excluded.limit_version, issued_at=excluded.issued_at`,
		b.ID, b.ScopeType, b.ScopeID, b.AmountCents, b.WarnAtPct, b.EnforceMode, b.LimitVersion, now())
	return err
}

func (s *Store) GetBudgetLimit(scopeType, scopeID string) (BudgetLimit, error) {
	var b BudgetLimit
	err := s.db.QueryRow(`SELECT id,scope_type,COALESCE(scope_id,''),amount_cents,warn_at_pct,enforce_mode,limit_version,issued_at
		FROM budget_limits WHERE scope_type=? AND scope_id IS ?`, scopeType, scopeID).
		Scan(&b.ID, &b.ScopeType, &b.ScopeID, &b.AmountCents, &b.WarnAtPct, &b.EnforceMode, &b.LimitVersion, &b.IssuedAt)
	return b, err
}

func (s *Store) ListBudgetLimits() ([]BudgetLimit, error) {
	rows, err := s.db.Query(`SELECT id,scope_type,COALESCE(scope_id,''),amount_cents,warn_at_pct,enforce_mode,limit_version,issued_at
		FROM budget_limits ORDER BY scope_type, scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BudgetLimit{}
	for rows.Next() {
		var b BudgetLimit
		if err := rows.Scan(&b.ID, &b.ScopeType, &b.ScopeID, &b.AmountCents, &b.WarnAtPct, &b.EnforceMode, &b.LimitVersion, &b.IssuedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
