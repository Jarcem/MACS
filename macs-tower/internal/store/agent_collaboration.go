// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// agent_collaboration.go V1.5 位置无关 Agent 协作持久化（设计 §3 身份数据模型）
// Connector / AgentInstance / AgentAction 身份目录、PlanRevision 冻结分派、
// ExecutionAttempt、跨机制品。人工标注与心跳写入严格分离：
//   - 候选上报只刷新 reported_json + 在线状态（重连不覆盖人工身份）
//   - 心跳只刷新健康/负载/在线状态
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ---------- 类型（实施计划「跨任务接口基线」） ----------

type Connector struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CredentialID    string `json:"credential_id"`
	CredentialHash  string `json:"credential_hash"`
	ProtocolVersion string `json:"protocol_version"`
	OnlineStatus    string `json:"online_status"`
	HealthStatus    string `json:"health_status"`
	LoadJSON        string `json:"load_json"`
	LastSeen        string `json:"last_seen"`
	RevokedAt       string `json:"revoked_at"`
}

type AgentInstance struct {
	ID              string   `json:"id"`
	ConnectorID     string   `json:"connector_id"`
	LocalAgentKey   string   `json:"local_agent_key"`
	DisplayName     string   `json:"display_name"`
	PlatformID      string   `json:"platform_id"`
	AdapterType     string   `json:"adapter_type"`
	RoleTags        []string `json:"role_tags"`
	CapabilityTags  []string `json:"capability_tags"`
	EnvironmentTags []string `json:"environment_tags"`
	IdentityStatus  string   `json:"identity_status"`
	VerifiedBy      string   `json:"verified_by"`
	VerifiedAt      string   `json:"verified_at"`
	OnlineStatus    string   `json:"online_status"`
	HealthStatus    string   `json:"health_status"`
	LoadJSON        string   `json:"load_json"`
	Revision        int      `json:"revision"`
	ReportedJSON    string   `json:"reported_json"`
}

type Heartbeat struct {
	ConnectorID, LocalAgentKey, HealthStatus, LoadJSON, OnlineStatus string
}

type AgentAction struct {
	ID            string   `json:"id"`
	AgentID       string   `json:"agent_id"`
	ActionKey     string   `json:"action_key"`
	ActionKind    string   `json:"action_kind"`
	AdapterType   string   `json:"adapter_type"`
	RiskLevel     string   `json:"risk_level"`
	RetryPolicy   string   `json:"retry_policy"`
	MaxAttempts   int      `json:"max_attempts"`
	ValidatorKeys []string `json:"validator_keys"`
	ConfigRef     string   `json:"config_ref"`
	SecretRefs    []string `json:"secret_refs"`
	Enabled       bool     `json:"enabled"`
	Revision      int      `json:"revision"`
}

type PlanRevision struct {
	TaskID        string `json:"task_id"`
	Revision      int    `json:"revision"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	DraftJSON     string `json:"draft_json"`
	CatalogDigest string `json:"catalog_digest"`
	ApprovedBy    string `json:"approved_by"`
	ApprovedAt    string `json:"approved_at"`
}

type TaskAssignment struct {
	TaskID       string `json:"task_id"`
	StepID       string `json:"step_id"`
	PlanRevision int    `json:"plan_revision"`
	AgentID      string `json:"agent_id"`
	ActionID     string `json:"action_id"`
	Environment  string `json:"environment"`
	InputDigest  string `json:"input_digest"`
	ApprovedBy   string `json:"approved_by"`
	ApprovedAt   string `json:"approved_at"`
}

type ExecutionAttempt struct {
	AttemptID          string `json:"attempt_id"`
	TaskID             string `json:"task_id"`
	StepID             string `json:"step_id"`
	PlanRevision       int    `json:"plan_revision"`
	AttemptNo          int    `json:"attempt_no"`
	IdempotencyKey     string `json:"idempotency_key"`
	ExecutionContextID string `json:"execution_context_id"`
	AgentID            string `json:"agent_id"`
	ActionID           string `json:"action_id"`
	Environment        string `json:"environment"`
	InputDigest        string `json:"input_digest"`
	Status             string `json:"status"`
	AcceptanceStatus   string `json:"acceptance_status"`
	ResultJSON         string `json:"result_json"`
	LeaseExpiresAt     string `json:"lease_expires_at"`
	CreatedAt          string `json:"created_at"`
	FinishedAt         string `json:"finished_at"`
}

type StoredArtifact struct {
	ID        string `json:"id"`
	AttemptID string `json:"attempt_id"`
	URI       string `json:"uri"`
	Digest    string `json:"digest"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

// AgentTask V1.5 任务级状态（plan_revisions 只承载不可变修订）
type AgentTask struct {
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	Goal      string `json:"goal"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// IdentityAuditEntry 人工标注/计划批准审计行（设计 §10）
type IdentityAuditEntry struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	PatchJSON string `json:"patch_json"`
	CreatedAt string `json:"created_at"`
}

// ---------- JSON 标签序列化助手 ----------

func marshalTags(t []string) string {
	if t == nil {
		t = []string{}
	}
	b, _ := json.Marshal(t)
	return string(b)
}

func unmarshalTags(s string) []string {
	var t []string
	_ = json.Unmarshal([]byte(s), &t)
	if t == nil {
		t = []string{}
	}
	return t
}

// ---------- Connector ----------

// UpsertConnector 注册 Connector（凭据哈希由调用方算好，明文秘密不落库，设计 §10）
func (s *Store) UpsertConnector(c Connector) error {
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.ProtocolVersion == "" {
		c.ProtocolVersion = "1.0"
	}
	ts := now()
	_, err := s.db.Exec(`INSERT INTO connectors(id,name,credential_id,credential_hash,protocol_version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, credential_id=excluded.credential_id,
		credential_hash=excluded.credential_hash, protocol_version=excluded.protocol_version, updated_at=excluded.updated_at`,
		c.ID, c.Name, c.CredentialID, c.CredentialHash, c.ProtocolVersion, ts, ts)
	return err
}

const connectorCols = `id,name,credential_id,credential_hash,protocol_version,online_status,health_status,load_json,last_seen,revoked_at`

func scanConnector(sc interface{ Scan(...any) error }) (Connector, error) {
	var c Connector
	err := sc.Scan(&c.ID, &c.Name, &c.CredentialID, &c.CredentialHash, &c.ProtocolVersion,
		&c.OnlineStatus, &c.HealthStatus, &c.LoadJSON, &c.LastSeen, &c.RevokedAt)
	return c, err
}

func (s *Store) GetConnector(id string) (Connector, error) {
	return scanConnector(s.db.QueryRow(`SELECT `+connectorCols+` FROM connectors WHERE id=?`, id))
}

func (s *Store) ListConnectors() ([]Connector, error) {
	rows, err := s.db.Query(`SELECT ` + connectorCols + ` FROM connectors ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connector
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TouchConnector 连接建立/消息到达时刷新 last_seen + 在线状态
func (s *Store) TouchConnector(id string) {
	ts := now()
	_, _ = s.db.Exec(`UPDATE connectors SET online_status='online', last_seen=?, updated_at=? WHERE id=?`, ts, ts, id)
}

// MarkConnectorOffline 连接断开时落离线（不改身份）
func (s *Store) MarkConnectorOffline(id string) {
	_, _ = s.db.Exec(`UPDATE connectors SET online_status='offline', updated_at=? WHERE id=?`, now(), id)
}

// RevokeConnector 吊销凭据（设计 §4.5：立即阻止尚未开始的分派）
func (s *Store) RevokeConnector(id string) error {
	_, err := s.db.Exec(`UPDATE connectors SET revoked_at=?, online_status='offline', updated_at=? WHERE id=? AND revoked_at=''`,
		now(), now(), id)
	return err
}

// ---------- AgentInstance ----------

const agentInstanceCols = `id,connector_id,local_agent_key,display_name,platform_id,adapter_type,
	role_tags,capability_tags,environment_tags,identity_status,verified_by,verified_at,revision,
	online_status,health_status,load_json,reported_json`

func scanAgentInstance(sc interface{ Scan(...any) error }) (AgentInstance, error) {
	var a AgentInstance
	var roleTags, capTags, envTags string
	err := sc.Scan(&a.ID, &a.ConnectorID, &a.LocalAgentKey, &a.DisplayName, &a.PlatformID, &a.AdapterType,
		&roleTags, &capTags, &envTags, &a.IdentityStatus, &a.VerifiedBy, &a.VerifiedAt, &a.Revision,
		&a.OnlineStatus, &a.HealthStatus, &a.LoadJSON, &a.ReportedJSON)
	if err != nil {
		return a, err
	}
	a.RoleTags, a.CapabilityTags, a.EnvironmentTags = unmarshalTags(roleTags), unmarshalTags(capTags), unmarshalTags(envTags)
	return a, nil
}

const agentInstanceSel = `SELECT ` + agentInstanceCols + ` FROM agent_instances `

// UpsertAgentCandidate 候选上报（设计 §4.2）：connector_id + local_agent_key 联合去重；
// 冲突路径只刷新 reported_json 与在线状态，人工标注字段（display_name/platform/标签/revision）绝不覆盖
func (s *Store) UpsertAgentCandidate(a AgentInstance) (AgentInstance, error) {
	ts := now()
	if a.ReportedJSON == "" {
		a.ReportedJSON = "{}"
	}
	_, err := s.db.Exec(`INSERT INTO agent_instances(
		id,connector_id,local_agent_key,display_name,reported_json,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?)
	ON CONFLICT(connector_id,local_agent_key) DO UPDATE SET
		reported_json=excluded.reported_json, online_status='online', updated_at=excluded.updated_at`,
		NewID(), a.ConnectorID, a.LocalAgentKey, a.DisplayName, a.ReportedJSON, ts, ts)
	if err != nil {
		return AgentInstance{}, err
	}
	return s.GetAgentInstanceByKey(a.ConnectorID, a.LocalAgentKey)
}

func (s *Store) GetAgentInstanceByKey(connectorID, localKey string) (AgentInstance, error) {
	return scanAgentInstance(s.db.QueryRow(agentInstanceSel+`WHERE connector_id=? AND local_agent_key=?`, connectorID, localKey))
}

func (s *Store) GetAgentInstance(id string) (AgentInstance, error) {
	return scanAgentInstance(s.db.QueryRow(agentInstanceSel+`WHERE id=?`, id))
}

// ListAgentInstances 身份目录全量（status 过滤可选：pending/verified/disabled）
func (s *Store) ListAgentInstances(status string) ([]AgentInstance, error) {
	q := agentInstanceSel
	if status != "" {
		q += `WHERE identity_status=? `
	}
	q += `ORDER BY created_at`
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.db.Query(q, status)
	} else {
		rows, err = s.db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentInstance
	for rows.Next() {
		a, err := scanAgentInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateHeartbeat 心跳（设计 §4.3）：只更新健康/负载/在线状态，不触碰人工标注。
// 目标不存在时报错（连接里的 agent 必须先候选上报过）。
func (s *Store) UpdateHeartbeat(h Heartbeat) error {
	if h.OnlineStatus == "" {
		h.OnlineStatus = "online"
	}
	result, err := s.db.Exec(
		`UPDATE agent_instances SET health_status=?, load_json=?, online_status=?, updated_at=? WHERE connector_id=? AND local_agent_key=?`,
		h.HealthStatus, h.LoadJSON, h.OnlineStatus, now(), h.ConnectorID, h.LocalAgentKey)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return fmt.Errorf("agent heartbeat target not found: %s/%s", h.ConnectorID, h.LocalAgentKey)
	}
	return nil
}

// MarkAgentsOfflineConnector Connector 断开后其所有 Agent 落离线（不改身份）
func (s *Store) MarkAgentsOfflineConnector(connectorID string) {
	_, _ = s.db.Exec(`UPDATE agent_instances SET online_status='offline', updated_at=? WHERE connector_id=?`, now(), connectorID)
}

// ---------- AgentAction ----------

const agentActionCols = `id,agent_id,action_key,action_kind,adapter_type,risk_level,retry_policy,max_attempts,
	validator_keys,config_ref,secret_refs,enabled,revision`

func scanAgentAction(sc interface{ Scan(...any) error }) (AgentAction, error) {
	var a AgentAction
	var validators, secrets string
	var enabled int
	err := sc.Scan(&a.ID, &a.AgentID, &a.ActionKey, &a.ActionKind, &a.AdapterType, &a.RiskLevel, &a.RetryPolicy,
		&a.MaxAttempts, &validators, &a.ConfigRef, &secrets, &enabled, &a.Revision)
	if err != nil {
		return a, err
	}
	a.ValidatorKeys, a.SecretRefs = unmarshalTags(validators), unmarshalTags(secrets)
	a.Enabled = enabled == 1
	return a, nil
}

const agentActionSel = `SELECT ` + agentActionCols + ` FROM agent_actions `

// UpsertAgentAction 人工登记动作（凭据只存引用，不存值，设计 §3.3）
func (s *Store) UpsertAgentAction(a AgentAction) (AgentAction, error) {
	ts := now()
	if a.ActionKind == "" {
		a.ActionKind = "execute"
	}
	if a.RiskLevel == "" {
		a.RiskLevel = "normal"
	}
	if a.RetryPolicy == "" {
		a.RetryPolicy = "retryable"
	}
	if a.MaxAttempts <= 0 {
		a.MaxAttempts = 3
	}
	_, err := s.db.Exec(`INSERT INTO agent_actions(
		id,agent_id,action_key,action_kind,adapter_type,risk_level,retry_policy,max_attempts,
		validator_keys,config_ref,secret_refs,enabled,revision,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)
	ON CONFLICT(agent_id,action_key) DO UPDATE SET
		action_kind=excluded.action_kind, adapter_type=excluded.adapter_type, risk_level=excluded.risk_level,
		retry_policy=excluded.retry_policy, max_attempts=excluded.max_attempts, validator_keys=excluded.validator_keys,
		config_ref=excluded.config_ref, secret_refs=excluded.secret_refs, enabled=excluded.enabled,
		revision=agent_actions.revision+1, updated_at=excluded.updated_at`,
		NewID(), a.AgentID, a.ActionKey, a.ActionKind, a.AdapterType, a.RiskLevel, a.RetryPolicy, a.MaxAttempts,
		marshalTags(a.ValidatorKeys), a.ConfigRef, marshalTags(a.SecretRefs), boolInt(a.Enabled), ts, ts)
	if err != nil {
		return AgentAction{}, err
	}
	return s.GetAgentActionByAgent(a.AgentID, a.ActionKey)
}

func (s *Store) GetAgentActionByAgent(agentID, actionKey string) (AgentAction, error) {
	return scanAgentAction(s.db.QueryRow(agentActionSel+`WHERE agent_id=? AND action_key=?`, agentID, actionKey))
}

func (s *Store) GetAgentAction(id string) (AgentAction, error) {
	return scanAgentAction(s.db.QueryRow(agentActionSel+`WHERE id=?`, id))
}

func (s *Store) ListAgentActions(agentID string) ([]AgentAction, error) {
	q := agentActionSel
	if agentID != "" {
		q += `WHERE agent_id=? `
	}
	q += `ORDER BY action_key`
	var rows *sql.Rows
	var err error
	if agentID != "" {
		rows, err = s.db.Query(q, agentID)
	} else {
		rows, err = s.db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentAction
	for rows.Next() {
		a, err := scanAgentAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAgentActionEnabled 启用/禁用（设计 §4.5：禁用立即阻止尚未开始的分派）
func (s *Store) SetAgentActionEnabled(id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE agent_actions SET enabled=?, revision=revision+1, updated_at=? WHERE id=?`,
		boolInt(enabled), now(), id)
	return err
}

// ---------- AgentTask / PlanRevision / TaskAssignment ----------

// InsertAgentTask 创建 V1.5 任务（首个修订提交时）
func (s *Store) InsertAgentTask(t AgentTask) error {
	if t.Status == "" {
		t.Status = "running"
	}
	ts := now()
	_, err := s.db.Exec(`INSERT INTO agent_tasks(task_id,title,goal,status,created_at,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(task_id) DO NOTHING`, t.TaskID, t.Title, t.Goal, t.Status, ts, ts)
	return err
}

func (s *Store) GetAgentTask(taskID string) (AgentTask, error) {
	var t AgentTask
	err := s.db.QueryRow(`SELECT task_id,title,goal,status,created_at,updated_at FROM agent_tasks WHERE task_id=?`, taskID).
		Scan(&t.TaskID, &t.Title, &t.Goal, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (s *Store) ListAgentTasks(status string) ([]AgentTask, error) {
	q := `SELECT task_id,title,goal,status,created_at,updated_at FROM agent_tasks`
	if status != "" {
		q += ` WHERE status=?`
	}
	q += ` ORDER BY created_at DESC`
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.db.Query(q, status)
	} else {
		rows, err = s.db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentTask
	for rows.Next() {
		var t AgentTask
		if err := rows.Scan(&t.TaskID, &t.Title, &t.Goal, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SetAgentTaskStatus(taskID, status string) error {
	_, err := s.db.Exec(`UPDATE agent_tasks SET status=?, updated_at=? WHERE task_id=?`, status, now(), taskID)
	return err
}

// InsertPlanRevision 提交草案（revision 递增；status=draft 待人工批准）
func (s *Store) InsertPlanRevision(p PlanRevision) (PlanRevision, error) {
	ts := now()
	if p.Status == "" {
		p.Status = "draft"
	}
	// 现有最大修订号 + 1（p.Revision==0 时自动取下一号）
	if p.Revision <= 0 {
		var maxRev int
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(revision),0) FROM plan_revisions WHERE task_id=?`, p.TaskID).Scan(&maxRev)
		p.Revision = maxRev + 1
	}
	_, err := s.db.Exec(`INSERT INTO plan_revisions(task_id,revision,source,status,draft_json,catalog_digest,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(task_id,revision) DO UPDATE SET draft_json=excluded.draft_json, catalog_digest=excluded.catalog_digest,
		status=excluded.status, updated_at=excluded.updated_at`,
		p.TaskID, p.Revision, p.Source, p.Status, p.DraftJSON, p.CatalogDigest, ts, ts)
	if err != nil {
		return p, err
	}
	return s.GetPlanRevision(p.TaskID, p.Revision)
}

func (s *Store) GetPlanRevision(taskID string, revision int) (PlanRevision, error) {
	var p PlanRevision
	err := s.db.QueryRow(`SELECT task_id,revision,source,status,draft_json,catalog_digest,approved_by,approved_at
		FROM plan_revisions WHERE task_id=? AND revision=?`, taskID, revision).
		Scan(&p.TaskID, &p.Revision, &p.Source, &p.Status, &p.DraftJSON, &p.CatalogDigest, &p.ApprovedBy, &p.ApprovedAt)
	return p, err
}

func (s *Store) ListPlanRevisions(taskID string) ([]PlanRevision, error) {
	rows, err := s.db.Query(`SELECT task_id,revision,source,status,draft_json,catalog_digest,approved_by,approved_at
		FROM plan_revisions WHERE task_id=? ORDER BY revision`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanRevision
	for rows.Next() {
		var p PlanRevision
		if err := rows.Scan(&p.TaskID, &p.Revision, &p.Source, &p.Status, &p.DraftJSON, &p.CatalogDigest, &p.ApprovedBy, &p.ApprovedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApprovePlanRevision 人工批准：修订置 approved + 写入冻结分派（单事务，设计 §5.3）
func (s *Store) ApprovePlanRevision(p PlanRevision, approvedBy string, assignments []TaskAssignment) error {
	ts := now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE plan_revisions SET status='approved', approved_by=?, approved_at=?, updated_at=?
		WHERE task_id=? AND revision=? AND status='draft'`, approvedBy, ts, ts, p.TaskID, p.Revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("plan revision %s/%d not in draft state", p.TaskID, p.Revision)
	}
	for _, a := range assignments {
		if _, err := tx.Exec(`INSERT INTO task_assignments(
			task_id,step_id,plan_revision,agent_id,action_id,environment,input_digest,approved_by,approved_at)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(task_id,step_id,plan_revision) DO UPDATE SET agent_id=excluded.agent_id, action_id=excluded.action_id,
			environment=excluded.environment, input_digest=excluded.input_digest`,
			a.TaskID, a.StepID, a.PlanRevision, a.AgentID, a.ActionID, a.Environment, a.InputDigest, approvedBy, ts); err != nil {
			return err
		}
	}
	// 旧修订保持不可变历史；agent_tasks 置 running
	if _, err := tx.Exec(`UPDATE agent_tasks SET status='running', updated_at=? WHERE task_id=?`, ts, p.TaskID); err != nil {
		return err
	}
	return tx.Commit()
}

const taskAssignmentCols = `task_id,step_id,plan_revision,agent_id,action_id,environment,input_digest,approved_by,approved_at`

func scanAssignment(sc interface{ Scan(...any) error }) (TaskAssignment, error) {
	var a TaskAssignment
	err := sc.Scan(&a.TaskID, &a.StepID, &a.PlanRevision, &a.AgentID, &a.ActionID, &a.Environment, &a.InputDigest, &a.ApprovedBy, &a.ApprovedAt)
	return a, err
}

func (s *Store) ListTaskAssignments(taskID string, revision int) ([]TaskAssignment, error) {
	rows, err := s.db.Query(`SELECT `+taskAssignmentCols+` FROM task_assignments WHERE task_id=? AND plan_revision=? ORDER BY step_id`,
		taskID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskAssignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetTaskAssignment(taskID, stepID string, revision int) (TaskAssignment, error) {
	return scanAssignment(s.db.QueryRow(`SELECT `+taskAssignmentCols+` FROM task_assignments
		WHERE task_id=? AND step_id=? AND plan_revision=?`, taskID, stepID, revision))
}

// ---------- ExecutionAttempt ----------

const attemptCols = `attempt_id,task_id,step_id,plan_revision,attempt_no,idempotency_key,execution_context_id,
	agent_id,action_id,environment,input_digest,status,acceptance_status,result_json,lease_expires_at,created_at,finished_at`

func scanAttempt(sc interface{ Scan(...any) error }) (ExecutionAttempt, error) {
	var a ExecutionAttempt
	err := sc.Scan(&a.AttemptID, &a.TaskID, &a.StepID, &a.PlanRevision, &a.AttemptNo, &a.IdempotencyKey, &a.ExecutionContextID,
		&a.AgentID, &a.ActionID, &a.Environment, &a.InputDigest, &a.Status, &a.AcceptanceStatus, &a.ResultJSON,
		&a.LeaseExpiresAt, &a.CreatedAt, &a.FinishedAt)
	return a, err
}

const attemptSel = `SELECT ` + attemptCols + ` FROM execution_attempts `

// InsertExecutionAttempt 每次尝试新 attempt_id；UNIQUE(step_id, attempt_no) 保证重试序号单调
func (s *Store) InsertExecutionAttempt(a ExecutionAttempt) (ExecutionAttempt, error) {
	ts := now()
	if a.AttemptID == "" {
		a.AttemptID = NewID()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if a.AcceptanceStatus == "" {
		a.AcceptanceStatus = "pending"
	}
	if a.ExecutionContextID == "" {
		a.ExecutionContextID = NewID()
	}
	_, err := s.db.Exec(`INSERT INTO execution_attempts(
		attempt_id,task_id,step_id,plan_revision,attempt_no,idempotency_key,execution_context_id,
		agent_id,action_id,environment,input_digest,status,acceptance_status,lease_expires_at,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.AttemptID, a.TaskID, a.StepID, a.PlanRevision, a.AttemptNo, a.IdempotencyKey, a.ExecutionContextID,
		a.AgentID, a.ActionID, a.Environment, a.InputDigest, a.Status, a.AcceptanceStatus, a.LeaseExpiresAt, ts)
	if err != nil {
		return a, err
	}
	return s.GetExecutionAttempt(a.AttemptID)
}

func (s *Store) GetExecutionAttempt(attemptID string) (ExecutionAttempt, error) {
	return scanAttempt(s.db.QueryRow(attemptSel+`WHERE attempt_id=?`, attemptID))
}

// ListAttemptsByTask 任务全部尝试（步骤内按 attempt_no 升序）
func (s *Store) ListAttemptsByTask(taskID string) ([]ExecutionAttempt, error) {
	rows, err := s.db.Query(attemptSel+`WHERE task_id=? ORDER BY step_id, attempt_no`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionAttempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// NextAttemptNo 步骤已有最大尝试号 + 1（1 起）
func (s *Store) NextAttemptNo(taskID, stepID string, revision int) (int, error) {
	var maxNo int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(attempt_no),0) FROM execution_attempts
		WHERE task_id=? AND step_id=? AND plan_revision=?`, taskID, stepID, revision).Scan(&maxNo)
	return maxNo + 1, err
}

// SetExecutionAttemptStatus 状态推进。守卫：终态（completed/failed/cancelled）不可被覆盖
// ——迟到结果按 plan_revision + attempt_id 核销，不覆盖新 attempt 或已取消终态（设计 §8）。
func (s *Store) SetExecutionAttemptStatus(attemptID, status string) (bool, error) {
	res, err := s.db.Exec(`UPDATE execution_attempts SET status=? WHERE attempt_id=? AND status NOT IN ('completed','failed','cancelled')`,
		status, attemptID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FormatLease 租约时间戳固定宽度格式（毫秒精度；保证字符串比较 == 时间比较）
func FormatLease(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// SetExecutionAttemptLease 分派后设置租约（到期由 AgentScheduler 看门狗回收）
func (s *Store) SetExecutionAttemptLease(attemptID string, leaseExpiresAt time.Time) error {
	_, err := s.db.Exec(`UPDATE execution_attempts SET lease_expires_at=? WHERE attempt_id=?`,
		FormatLease(leaseExpiresAt), attemptID)
	return err
}

// FinishExecutionAttempt 终态核销：status + acceptance_status + result_json（finished_at 落库）。
// 条件更新保证已终态（如 cancelled）不被迟到结果覆盖。
func (s *Store) FinishExecutionAttempt(attemptID, status, acceptanceStatus, resultJSON string) (bool, error) {
	res, err := s.db.Exec(`UPDATE execution_attempts SET status=?, acceptance_status=?, result_json=?, finished_at=?
		WHERE attempt_id=? AND status NOT IN ('completed','failed','cancelled')`,
		status, acceptanceStatus, resultJSON, now(), attemptID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelAttemptsForTask 强制取消任务全部未终态尝试（返回取消数量）
func (s *Store) CancelAttemptsForTask(taskID string) (int, error) {
	res, err := s.db.Exec(`UPDATE execution_attempts SET status='cancelled', finished_at=?
		WHERE task_id=? AND status NOT IN ('completed','failed','cancelled')`, now(), taskID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListInflightAttemptsByConnector 重连对账（设计 §8）：Connector 执行中的尝试
func (s *Store) ListInflightAttemptsByConnector(connectorID string) ([]ExecutionAttempt, error) {
	rows, err := s.db.Query(attemptSel+`WHERE status IN ('dispatched','running','unknown')
		AND agent_id IN (SELECT id FROM agent_instances WHERE connector_id=?) ORDER BY created_at`, connectorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionAttempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListExpiredAttempts 租约到期且未终态的尝试（AgentScheduler 看门狗回收）
func (s *Store) ListExpiredAttempts(nowTS string) ([]ExecutionAttempt, error) {
	rows, err := s.db.Query(attemptSel+`WHERE status IN ('dispatched','running','unknown') AND lease_expires_at <> '' AND lease_expires_at < ?`, nowTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionAttempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- ExecutionArtifact ----------

// InsertArtifact 落库跨机制品（URI/digest 已由上游校验）
func (s *Store) InsertArtifact(taskID, stepID string, revision int, art StoredArtifact) error {
	_, err := s.db.Exec(`INSERT INTO execution_artifacts(id,attempt_id,task_id,step_id,plan_revision,uri,digest,kind,size_bytes,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		art.ID, art.AttemptID, taskID, stepID, revision, art.URI, art.Digest, art.Kind, art.SizeBytes, now())
	return err
}

const artifactCols = `id,attempt_id,uri,digest,kind,size_bytes,created_at`

func scanArtifact(sc interface{ Scan(...any) error }) (StoredArtifact, error) {
	var a StoredArtifact
	err := sc.Scan(&a.ID, &a.AttemptID, &a.URI, &a.Digest, &a.Kind, &a.SizeBytes, &a.CreatedAt)
	return a, err
}

// ListArtifactsForStep 某步骤产出的制品（下游依赖收集，设计 §6.3）
func (s *Store) ListArtifactsForStep(taskID, stepID string, revision int) ([]StoredArtifact, error) {
	rows, err := s.db.Query(`SELECT `+artifactCols+` FROM execution_artifacts
		WHERE task_id=? AND step_id=? AND plan_revision=? ORDER BY created_at`, taskID, stepID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredArtifact
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- 身份审计 ----------

// InsertIdentityAudit 人工标注/计划批准审计（设计 §10）
func (s *Store) InsertIdentityAudit(agentID, taskID, actor, action, patchJSON string) error {
	_, err := s.db.Exec(`INSERT INTO agent_identity_audit(id,agent_id,task_id,actor,action,patch_json,created_at)
		VALUES(?,?,?,?,?,?,?)`, NewID(), agentID, taskID, actor, action, patchJSON, now())
	return err
}

func (s *Store) ListIdentityAudit(agentID string, limit int) ([]IdentityAuditEntry, error) {
	q := `SELECT id,agent_id,task_id,actor,action,patch_json,created_at FROM agent_identity_audit`
	if agentID != "" {
		q += ` WHERE agent_id=?`
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	var rows *sql.Rows
	var err error
	if agentID != "" {
		rows, err = s.db.Query(q, agentID, limit)
	} else {
		rows, err = s.db.Query(q, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IdentityAuditEntry
	for rows.Next() {
		var e IdentityAuditEntry
		if err := rows.Scan(&e.ID, &e.AgentID, &e.TaskID, &e.Actor, &e.Action, &e.PatchJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
