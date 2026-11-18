// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// Package agentdir V1.5 人工身份目录（设计 §4 人工标注流程）
// Connector 上报的候选只是事实快照；display_name/平台/标签/动作只能经人工标注修改，
// 全部标注写审计。只有 identity_status=verified 且 action enabled=true 才进入规划目录。
package agentdir

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"macs.local/tower/internal/store"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// VerifyPatch 人工标注补丁（实施计划接口基线）。ExpectedRevision 乐观锁：
// 与当前 revision 不一致拒绝（防止并发标注互相覆盖）。
type VerifyPatch struct {
	DisplayName, PlatformID, AdapterType      string
	RoleTags, CapabilityTags, EnvironmentTags []string
	ExpectedRevision                          int
	Actor                                     string
}

// Service 身份目录服务
type Service struct{ st *store.Store }

func NewService(st *store.Store) *Service { return &Service{st: st} }

// Verify 人工确认身份：pending → verified。revision 乐观锁失败返回错误。
func (s *Service) Verify(agentID string, p VerifyPatch) (store.AgentInstance, error) {
	if p.Actor == "" {
		return store.AgentInstance{}, fmt.Errorf("verify: actor required")
	}
	if p.ExpectedRevision <= 0 {
		return store.AgentInstance{}, fmt.Errorf("verify: expected_revision required")
	}
	// 动作存在性前置校验（平台/适配器与已登记动作一致）
	actions, err := s.st.ListAgentActions(agentID)
	if err != nil {
		return store.AgentInstance{}, err
	}
	for _, a := range actions {
		if p.AdapterType != "" && a.AdapterType != p.AdapterType {
			return store.AgentInstance{}, fmt.Errorf("verify: action %s adapter %s != patch adapter %s", a.ActionKey, a.AdapterType, p.AdapterType)
		}
	}
	patchJSON, _ := json.Marshal(p)

	tx, err := s.st.Begin()
	if err != nil {
		return store.AgentInstance{}, err
	}
	defer tx.Rollback()
	nowTS := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`UPDATE agent_instances SET
		display_name=?, platform_id=?, adapter_type=?,
		role_tags=?, capability_tags=?, environment_tags=?,
		identity_status='verified', verified_by=?, verified_at=?, revision=?,
		online_status=CASE WHEN online_status='offline' THEN online_status ELSE online_status END, updated_at=?
		WHERE id=? AND revision=?`,
		p.DisplayName, p.PlatformID, p.AdapterType,
		marshalTags(p.RoleTags), marshalTags(p.CapabilityTags), marshalTags(p.EnvironmentTags),
		p.Actor, nowTS, p.ExpectedRevision+1, nowTS, agentID, p.ExpectedRevision)
	if err != nil {
		return store.AgentInstance{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return store.AgentInstance{}, fmt.Errorf("verify: revision conflict for %s (expected %d)", agentID, p.ExpectedRevision)
	}
	if err := insertAuditTx(tx, agentID, "", p.Actor, "verify", string(patchJSON)); err != nil {
		return store.AgentInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return store.AgentInstance{}, err
	}
	return s.st.GetAgentInstance(agentID)
}

// Disable 禁用 Agent（设计 §4.5：禁用立即阻止尚未开始的分派）
func (s *Service) Disable(agentID, actor string) error {
	if actor == "" {
		return fmt.Errorf("disable: actor required")
	}
	res, err := s.st.Exec(`UPDATE agent_instances SET identity_status='disabled', updated_at=? WHERE id=? AND identity_status <> 'disabled'`,
		time.Now().UTC().Format(time.RFC3339), agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("disable: agent %s already disabled or missing", agentID)
	}
	return s.st.InsertIdentityAudit(agentID, "", actor, "disable", `{}`)
}

// SetActionEnabled 启用/禁用动作（人工接口，审计留痕）
func (s *Service) SetActionEnabled(actionID string, enabled bool, actor string) error {
	if actor == "" {
		return fmt.Errorf("action: actor required")
	}
	if err := s.st.SetAgentActionEnabled(actionID, enabled); err != nil {
		return err
	}
	act, err := s.st.GetAgentAction(actionID)
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]any{"action_key": act.ActionKey, "enabled": enabled})
	return s.st.InsertIdentityAudit(act.AgentID, "", actor, "action_enabled", string(patch))
}

// CatalogAction 规划目录中的动作视图
type CatalogAction struct {
	ActionID     string   `json:"action_id"`
	ActionKey    string   `json:"action_key"`
	ActionKind   string   `json:"action_kind"`
	AdapterType  string   `json:"adapter_type"`
	RetryPolicy  string   `json:"retry_policy"`
	MaxAttempts  int      `json:"max_attempts"`
	ValidatorKeys []string `json:"validator_keys"`
}

// CatalogAgent 规划目录中的 Agent 视图
type CatalogAgent struct {
	AgentID         string          `json:"agent_id"`
	ConnectorID     string          `json:"connector_id"`
	LocalAgentKey   string          `json:"local_agent_key"`
	DisplayName     string          `json:"display_name"`
	PlatformID      string          `json:"platform_id"`
	AdapterType     string          `json:"adapter_type"`
	RoleTags        []string        `json:"role_tags"`
	CapabilityTags  []string        `json:"capability_tags"`
	EnvironmentTags []string        `json:"environment_tags"`
	OnlineStatus    string          `json:"online_status"`
	Actions         []CatalogAction `json:"actions"`
}

// Catalog verified 目录（设计 §4.4）：identity_status=verified 且 enabled=1 的动作
func (s *Service) Catalog() (Catalog, error) {
	agents, err := s.st.ListAgentInstances("verified")
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Agents: []CatalogAgent{}}
	for _, a := range agents {
		view := CatalogAgent{
			AgentID: a.ID, ConnectorID: a.ConnectorID, LocalAgentKey: a.LocalAgentKey,
			DisplayName: a.DisplayName, PlatformID: a.PlatformID, AdapterType: a.AdapterType,
			RoleTags: a.RoleTags, CapabilityTags: a.CapabilityTags, EnvironmentTags: a.EnvironmentTags,
			OnlineStatus: a.OnlineStatus, Actions: []CatalogAction{},
		}
		actions, err := s.st.ListAgentActions(a.ID)
		if err != nil {
			return Catalog{}, err
		}
		for _, act := range actions {
			if !act.Enabled {
				continue
			}
			view.Actions = append(view.Actions, CatalogAction{
				ActionID: act.ID, ActionKey: act.ActionKey, ActionKind: act.ActionKind,
				AdapterType: act.AdapterType, RetryPolicy: act.RetryPolicy,
				MaxAttempts: act.MaxAttempts, ValidatorKeys: act.ValidatorKeys,
			})
		}
		out.Agents = append(out.Agents, view)
	}
	return out, nil
}

// Catalog 规划目录快照（批准时冻结 digest）
type Catalog struct {
	Agents []CatalogAgent `json:"agents"`
}

// Digest 确定性目录快照摘要（批准时冻结进 plan_revisions.catalog_digest）
func (c Catalog) Digest() string {
	b, _ := json.Marshal(c)
	return sha256Hex(b)
}

// ---------- 内部 ----------

func marshalTags(t []string) string {
	if t == nil {
		t = []string{}
	}
	b, _ := json.Marshal(t)
	return string(b)
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertAuditTx(tx execer, agentID, taskID, actor, action, patchJSON string) error {
	_, err := tx.Exec(`INSERT INTO agent_identity_audit(id,agent_id,task_id,actor,action,patch_json,created_at)
		VALUES(?,?,?,?,?,?,?)`,
		store.NewID(), agentID, taskID, actor, action, patchJSON,
		time.Now().UTC().Format(time.RFC3339))
	return err
}
