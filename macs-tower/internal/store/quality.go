// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 质量与归档访问层（M3）：审批 §2.5 / 抽查 §2.6 / 追溯链 §2.7 / 归档 §2.8 / 遥测 §2.10b / 护栏
package store

import (
	"database/sql"
	"time"
)

// ── 审批（FR-19）──────────────────────────────

type Approval struct {
	ID          string `json:"id"`
	TaskID      string `json:"task_id"`
	SubtaskID   string `json:"subtask_id"`
	IncubatorID string `json:"incubator_id"`
	ApproveType string `json:"approval_type"`
	Status      string `json:"status"`
	Requester   string `json:"requester"`
	Approver    string `json:"approver"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Payload     string `json:"payload"`
	ExpiresAt   string `json:"expires_at"`
	Decision    string `json:"decision"`
	DecisionNote string `json:"decision_note"`
	DecidedAt   string `json:"decided_at"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (s *Store) InsertApproval(a Approval) error {
	_, err := s.db.Exec(`INSERT INTO approvals(id,task_id,subtask_id,incubator_id,approval_type,status,requester,approver,
		title,description,payload,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.SubtaskID, a.IncubatorID, a.ApproveType, "pending", a.Requester, "",
		a.Title, a.Description, a.Payload, a.ExpiresAt, now(), now())
	return err
}

func (s *Store) ListApprovals(status string, limit int) ([]Approval, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id,COALESCE(task_id,''),COALESCE(subtask_id,''),COALESCE(incubator_id,''),approval_type,status,requester,approver,
		title,description,payload,COALESCE(expires_at,''),COALESCE(decision,''),COALESCE(decision_note,''),COALESCE(decided_at,''),created_at,updated_at
		FROM approvals`
	var args []any
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	return s.scanApprovals(q, args...)
}

func (s *Store) GetApproval(id string) (Approval, error) {
	rows, err := s.scanApprovals(`SELECT id,COALESCE(task_id,''),COALESCE(subtask_id,''),COALESCE(incubator_id,''),approval_type,status,requester,approver,
		title,description,payload,COALESCE(expires_at,''),COALESCE(decision,''),COALESCE(decision_note,''),COALESCE(decided_at,''),created_at,updated_at
		FROM approvals WHERE id=?`, id)
	if err != nil || len(rows) == 0 {
		return Approval{}, err
	}
	return rows[0], nil
}

// DecideApproval 审批决策（pending 才可决策，幂等拒绝二次变更）
func (s *Store) DecideApproval(id, decision, approver, note string) (bool, error) {
	res, err := s.db.Exec(`UPDATE approvals SET status=?, approver=?, decision=?, decision_note=?, decided_at=?, updated_at=?
		WHERE id=? AND status='pending'`, decision, approver, decision, note, now(), now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ExpireApprovals 超时审批过期（FR-19 多场景超时）
func (s *Store) ExpireApprovals() (int64, error) {
	res, err := s.db.Exec(`UPDATE approvals SET status='expired', updated_at=? WHERE status='pending' AND expires_at <> '' AND expires_at <= ?`,
		now(), now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) scanApprovals(q string, args ...any) ([]Approval, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.TaskID, &a.SubtaskID, &a.IncubatorID, &a.ApproveType, &a.Status, &a.Requester,
			&a.Approver, &a.Title, &a.Description, &a.Payload, &a.ExpiresAt, &a.Decision, &a.DecisionNote, &a.DecidedAt,
			&a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PendingApprovalExists 幂等护栏：同实体同类型只挂一条待审
func (s *Store) PendingApprovalExists(approveType, taskID, subtaskID string) (bool, error) {
	var n int
	q := `SELECT COUNT(1) FROM approvals WHERE approval_type=? AND status='pending' AND task_id=? AND subtask_id=?`
	if err := s.db.QueryRow(q, approveType, taskID, subtaskID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ── 抽查（FR-16）──────────────────────────────

type SpotCheck struct {
	ID            string  `json:"id"`
	TaskID        string  `json:"task_id"`
	SubtaskID     string  `json:"subtask_id"`
	IncubatorID   string  `json:"incubator_id"`
	CheckScope    string  `json:"check_scope"`
	Status        string  `json:"status"`
	CheckItems    string  `json:"check_items"`
	PassRate      float64 `json:"pass_rate"`
	OverallPassed int     `json:"overall_passed"`
	AttemptCount  int     `json:"attempt_count"`
	MaxAttempts   int     `json:"max_attempts"`
	NextAttemptAt string  `json:"next_attempt_at"`
	LastAttemptAt string  `json:"last_attempt_at"`
	Note          string  `json:"note"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

const spotCols = `id,task_id,subtask_id,incubator_id,check_scope,status,check_items,COALESCE(pass_rate,0),COALESCE(overall_passed,0),
	attempt_count,max_attempts,next_attempt_at,last_attempt_at,note,created_at,updated_at`

func (s *Store) scanSpotChecks(q string, args ...any) ([]SpotCheck, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpotCheck{}
	for rows.Next() {
		var c SpotCheck
		if err := rows.Scan(&c.ID, &c.TaskID, &c.SubtaskID, &c.IncubatorID, &c.CheckScope, &c.Status, &c.CheckItems,
			&c.PassRate, &c.OverallPassed, &c.AttemptCount, &c.MaxAttempts, &c.NextAttemptAt, &c.LastAttemptAt,
			&c.Note, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) InsertSpotCheck(c SpotCheck) error {
	_, err := s.db.Exec(`INSERT INTO spot_checks(id,task_id,subtask_id,incubator_id,check_scope,status,check_items,
		attempt_count,max_attempts,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.TaskID, c.SubtaskID, c.IncubatorID, c.CheckScope, "pending", c.CheckItems,
		c.AttemptCount, c.MaxAttempts, c.NextAttemptAt, now(), now())
	return err
}

func (s *Store) ListSpotChecks(limit int) ([]SpotCheck, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.scanSpotChecks(`SELECT `+spotCols+` FROM spot_checks ORDER BY created_at DESC LIMIT ?`, limit)
}

func (s *Store) ListPendingSpotChecks() ([]SpotCheck, error) {
	return s.scanSpotChecks(`SELECT `+spotCols+` FROM spot_checks WHERE status='pending' ORDER BY created_at`)
}

func (s *Store) TaskSpotChecked(taskID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM spot_checks WHERE task_id=?`, taskID).Scan(&n)
	return n > 0, err
}

// FinishSpotCheck 抽查终态落库
func (s *Store) FinishSpotCheck(id, status, items string, passRate float64, passed bool, note string) error {
	op := 0
	if passed {
		op = 1
	}
	_, err := s.db.Exec(`UPDATE spot_checks SET status=?, check_items=?, pass_rate=?, overall_passed=?, last_attempt_at=?, note=?, updated_at=? WHERE id=?`,
		status, items, passRate, op, now(), note, now(), id)
	return err
}

// ── 追溯链（FR-17，6.4.4 指纹链；UNIQUE(subtask_id,fingerprint) 幂等）──

type OriginTrace struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	SubtaskID      string `json:"subtask_id"`
	FromKind       string `json:"from_kind"`
	FromID         string `json:"from_id"`
	ToKind         string `json:"to_kind"`
	ToID           string `json:"to_id"`
	Fingerprint    string `json:"fingerprint"`
	FingerprintAlg string `json:"fingerprint_alg"`
	ContentSummary string `json:"content_summary"`
	ArtifactType   string `json:"artifact_type"`
	SizeBytes      int64  `json:"size_bytes"`
	Depth          int    `json:"depth"`
	Metadata       string `json:"metadata"`
	CreatedAt      string `json:"created_at"`
}

func (s *Store) InsertOriginTrace(t OriginTrace) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO origin_traces(id,task_id,subtask_id,from_kind,from_id,to_kind,to_id,fingerprint,
		fingerprint_alg,content_summary,artifact_type,size_bytes,depth,metadata,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.TaskID, t.SubtaskID, t.FromKind, t.FromID, t.ToKind, t.ToID, t.Fingerprint,
		t.FingerprintAlg, t.ContentSummary, t.ArtifactType, t.SizeBytes, t.Depth, t.Metadata, now())
	return err
}

// GetTaskRootFingerprint 任务根指纹（trace.Record 落的 depth-0 节点）；链父 = 根，保证并行子任务指纹可确定性重算
func (s *Store) GetTaskRootFingerprint(taskID string) (string, error) {
	var fp string
	err := s.db.QueryRow(`SELECT fingerprint FROM origin_traces WHERE task_id=? AND subtask_id='' LIMIT 1`, taskID).Scan(&fp)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return fp, err
}

func (s *Store) ListOriginTraces(taskID string, limit int) ([]OriginTrace, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id,task_id,COALESCE(subtask_id,''),from_kind,from_id,to_kind,to_id,fingerprint,
		fingerprint_alg,content_summary,artifact_type,size_bytes,depth,metadata,created_at
		FROM origin_traces WHERE task_id=? ORDER BY depth, created_at LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OriginTrace{}
	for rows.Next() {
		var t OriginTrace
		if err := rows.Scan(&t.ID, &t.TaskID, &t.SubtaskID, &t.FromKind, &t.FromID, &t.ToKind, &t.ToID, &t.Fingerprint,
			&t.FingerprintAlg, &t.ContentSummary, &t.ArtifactType, &t.SizeBytes, &t.Depth, &t.Metadata, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ── 归档（FR-18）──────────────────────────────

type ArchiveDoc struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	SubtaskID     string `json:"subtask_id"`
	IncubatorID   string `json:"incubator_id"`
	Source        string `json:"source"`
	DocType       string `json:"doc_type"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	FormatVersion string `json:"format_version"`
	WordCount     int    `json:"word_count"`
	HasCode       bool   `json:"has_code"`
	Version       int    `json:"version"`
	IsLatest      bool   `json:"is_latest"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func (s *Store) InsertArchiveDoc(d ArchiveDoc) error {
	_, err := s.db.Exec(`INSERT INTO archive_documents(id,task_id,subtask_id,incubator_id,source,doc_type,title,content,
		format_version,word_count,has_code,version,is_latest,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.TaskID, d.SubtaskID, d.IncubatorID, d.Source, d.DocType, d.Title, d.Content,
		d.FormatVersion, d.WordCount, boolInt(d.HasCode), d.Version, 1, now(), now())
	return err
}

func (s *Store) ListArchiveDocs(taskID, docType string, limit int) ([]ArchiveDoc, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id,task_id,subtask_id,incubator_id,source,doc_type,title,content,format_version,word_count,has_code,version,is_latest,created_at,updated_at
		FROM archive_documents WHERE 1=1`
	var args []any
	if taskID != "" {
		q += ` AND task_id=?`
		args = append(args, taskID)
	}
	if docType != "" {
		q += ` AND doc_type=?`
		args = append(args, docType)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArchiveDoc{}
	for rows.Next() {
		var d ArchiveDoc
		var hasCode, isLatest int
		if err := rows.Scan(&d.ID, &d.TaskID, &d.SubtaskID, &d.IncubatorID, &d.Source, &d.DocType, &d.Title, &d.Content,
			&d.FormatVersion, &d.WordCount, &hasCode, &d.Version, &isLatest, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.HasCode, d.IsLatest = hasCode == 1, isLatest == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetArchiveDoc(id string) (ArchiveDoc, error) {
	var d ArchiveDoc
	var hasCode, isLatest int
	err := s.db.QueryRow(`SELECT id,task_id,COALESCE(subtask_id,''),COALESCE(incubator_id,''),source,doc_type,title,content,
		format_version,word_count,has_code,version,is_latest,created_at,updated_at FROM archive_documents WHERE id=?`, id).
		Scan(&d.ID, &d.TaskID, &d.SubtaskID, &d.IncubatorID, &d.Source, &d.DocType, &d.Title, &d.Content,
			&d.FormatVersion, &d.WordCount, &hasCode, &d.Version, &isLatest, &d.CreatedAt, &d.UpdatedAt)
	d.HasCode, d.IsLatest = hasCode == 1, isLatest == 1
	return d, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ── 护栏事件（6.4.10）─────────────────────────

type GuardrailEvent struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	SubtaskID  string `json:"subtask_id"`
	IncubatorID string `json:"incubator_id"`
	Type       string `json:"guardrail_type"`
	Action     string `json:"action"`
	Detail     string `json:"detail"`
	CreatedAt  string `json:"created_at"`
}

func (s *Store) InsertGuardrailEvent(g GuardrailEvent) error {
	_, err := s.db.Exec(`INSERT INTO guardrail_events(id,task_id,subtask_id,incubator_id,guardrail_type,action,detail,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, g.ID, g.TaskID, g.SubtaskID, g.IncubatorID, g.Type, g.Action, g.Detail, now())
	return err
}

func (s *Store) ListGuardrailEvents(limit int) ([]GuardrailEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,task_id,subtask_id,incubator_id,guardrail_type,action,detail,created_at
		FROM guardrail_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuardrailEvent{}
	for rows.Next() {
		var g GuardrailEvent
		if err := rows.Scan(&g.ID, &g.TaskID, &g.SubtaskID, &g.IncubatorID, &g.Type, &g.Action, &g.Detail, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ── 遥测事件（§6.4.13.4）───────────────────────

type TelemetryEvent struct {
	ID             int64  `json:"id"`
	TaskID         string `json:"task_id"`
	SubtaskID      string `json:"subtask_id"`
	WorkerID       string `json:"worker_id"`
	PlatformAdapter string `json:"platform_adapter"`
	AgentPlatform  string `json:"agent_platform"`
	EventType      string `json:"event_type"`
	Payload        string `json:"payload"`
	Timestamp      string `json:"timestamp"`
}

func (s *Store) InsertTelemetryEvent(e TelemetryEvent) error {
	_, err := s.db.Exec(`INSERT INTO telemetry_events(task_id,subtask_id,worker_id,platform_adapter,agent_platform,event_type,payload,timestamp)
		VALUES(?,?,?,?,?,?,?,?)`, e.TaskID, e.SubtaskID, e.WorkerID, e.PlatformAdapter, e.AgentPlatform, e.EventType, e.Payload, now())
	return err
}

// ListTelemetryEvents 附录 B.1：按 task_id / subtask_id / event_type 过滤，最近 N 条
func (s *Store) ListTelemetryEvents(taskID, subtaskID, eventType string, limit int) ([]TelemetryEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,COALESCE(task_id,''),COALESCE(subtask_id,''),worker_id,platform_adapter,agent_platform,event_type,payload,timestamp
		FROM telemetry_events WHERE 1=1`
	var args []any
	if taskID != "" {
		q += ` AND task_id=?`
		args = append(args, taskID)
	}
	if subtaskID != "" {
		q += ` AND subtask_id=?`
		args = append(args, subtaskID)
	}
	if eventType != "" {
		q += ` AND event_type=?`
		args = append(args, eventType)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelemetryEvent{}
	for rows.Next() {
		var e TelemetryEvent
		if err := rows.Scan(&e.ID, &e.TaskID, &e.SubtaskID, &e.WorkerID, &e.PlatformAdapter, &e.AgentPlatform, &e.EventType, &e.Payload, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

var _ = time.Now // 保留 time 引用（stale 判定在同包 store.go）
