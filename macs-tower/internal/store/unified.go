// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Jarcem

// 统一 Agent 平台协议（模块 #28 §6.4.13.1）+ 模板（FR-22）+ 命令终端（模块 #32 §6.4.14）+ Admin Token（§6.4.16）
package store

import (
	"database/sql"
	"time"
)

// ── 统一协议三表（§2.10a）──────────────────────

type AgentPlatform struct {
	ID            string `json:"id"`
	PlatformName  string `json:"platform_name"`
	Vendor        string `json:"vendor"`
	Version       string `json:"version"`
	ProtocolSpec  string `json:"protocol_spec"`
	IsActive      bool   `json:"is_active"`
	IsSystem      bool   `json:"is_system"`
	BaseURL       string `json:"base_url"`
	DefaultDriver string `json:"default_driver"`
	Capabilities  string `json:"capabilities"`
	Metadata      string `json:"metadata"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type ProtocolDriver struct {
	ID             string `json:"id"`
	DriverName     string `json:"driver_name"`
	Version        string `json:"version"`
	Description    string `json:"description"`
	ProtocolFamily string `json:"protocol_family"`
	IsActive       bool   `json:"is_active"`
	SpecURL        string `json:"spec_url"`
	MessageSchema  string `json:"message_schema"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type PlatformAdapter struct {
	ID                int64  `json:"id"`
	PlatformID        string `json:"platform_id"`
	DriverID          string `json:"driver_id"`
	AdapterName       string `json:"adapter_name"`
	Status            string `json:"status"`
	CompatibilityMode string `json:"compatibility_mode"`
	SupportedFeatures string `json:"supported_features"`
	ConfigSchema      string `json:"config_schema"`
	CreatedAt         string `json:"created_at"`
}

const platformCols = `id,platform_name,COALESCE(vendor,''),version,protocol_spec,is_active,is_system,COALESCE(base_url,''),default_driver,capabilities,metadata,created_at,updated_at`

func scanPlatforms(q string, args ...any) func(*Store) ([]AgentPlatform, error) {
	return func(s *Store) ([]AgentPlatform, error) {
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []AgentPlatform{}
		for rows.Next() {
			var p AgentPlatform
			var act, sys int
			if err := rows.Scan(&p.ID, &p.PlatformName, &p.Vendor, &p.Version, &p.ProtocolSpec, &act, &sys, &p.BaseURL,
				&p.DefaultDriver, &p.Capabilities, &p.Metadata, &p.CreatedAt, &p.UpdatedAt); err != nil {
				return nil, err
			}
			p.IsActive, p.IsSystem = act == 1, sys == 1
			out = append(out, p)
		}
		return out, rows.Err()
	}
}

func (s *Store) ListPlatforms(activeOnly bool) ([]AgentPlatform, error) {
	q := `SELECT ` + platformCols + ` FROM unified_agent_platforms`
	if activeOnly {
		q += ` WHERE is_active=1`
	}
	q += ` ORDER BY created_at`
	return scanPlatforms(q)(s)
}

func (s *Store) GetPlatform(id string) (AgentPlatform, error) {
	ps, err := scanPlatforms(`SELECT `+platformCols+` FROM unified_agent_platforms WHERE id=?`, id)(s)
	if err != nil || len(ps) == 0 {
		return AgentPlatform{}, err
	}
	return ps[0], nil
}

// UpsertPlatform 平台注册（POST /api/v1/agent-platforms）；platform_name 幂等更新
func (s *Store) UpsertPlatform(p AgentPlatform) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM unified_agent_platforms WHERE platform_name=?`, p.PlatformName).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE unified_agent_platforms SET vendor=?, version=?, protocol_spec=?, base_url=?, default_driver=?, capabilities=?, is_active=1, updated_at=? WHERE id=?`,
			p.Vendor, p.Version, p.ProtocolSpec, p.BaseURL, p.DefaultDriver, p.Capabilities, now(), id)
		return id, err
	}
	if err != sqlNoRows() {
		return "", err
	}
	id = NewID()
	_, err = s.db.Exec(`INSERT INTO unified_agent_platforms(id,platform_name,vendor,version,protocol_spec,is_active,is_system,base_url,default_driver,capabilities,metadata,created_at,updated_at)
		VALUES(?,?,?,?,?,1,0,?,?,?,?,?,?)`,
		id, p.PlatformName, p.Vendor, p.Version, p.ProtocolSpec, p.BaseURL, p.DefaultDriver, p.Capabilities, p.Metadata, now(), now())
	return id, err
}

// SetPlatformActive PATCH /api/v1/agent-platforms/:id（启停与默认驱动更新）
func (s *Store) SetPlatformActive(id string, active bool, defaultDriver string) error {
	_, err := s.db.Exec(`UPDATE unified_agent_platforms SET is_active=?, default_driver=CASE WHEN ?<>'' THEN ? ELSE default_driver END, updated_at=? WHERE id=?`,
		boolInt(active), defaultDriver, defaultDriver, now(), id)
	return err
}

const driverCols = `id,driver_name,version,COALESCE(description,''),protocol_family,is_active,spec_url,COALESCE(message_schema,''),created_at,updated_at`

func (s *Store) ListDrivers() ([]ProtocolDriver, error) {
	rows, err := s.db.Query(`SELECT ` + driverCols + ` FROM protocol_drivers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProtocolDriver{}
	for rows.Next() {
		var d ProtocolDriver
		var act int
		if err := rows.Scan(&d.ID, &d.DriverName, &d.Version, &d.Description, &d.ProtocolFamily, &act, &d.SpecURL, &d.MessageSchema, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.IsActive = act == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertDriver 协议驱动注册（POST /api/v1/protocol-drivers）；(driver_name,version) 幂等
func (s *Store) UpsertDriver(d ProtocolDriver) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM protocol_drivers WHERE driver_name=? AND version=?`, d.DriverName, d.Version).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE protocol_drivers SET description=?, protocol_family=?, spec_url=?, message_schema=?, updated_at=? WHERE id=?`,
			d.Description, d.ProtocolFamily, d.SpecURL, d.MessageSchema, now(), id)
		return id, err
	}
	if err != sqlNoRows() {
		return "", err
	}
	id = NewID()
	_, err = s.db.Exec(`INSERT INTO protocol_drivers(id,driver_name,version,description,protocol_family,is_active,spec_url,message_schema,created_at,updated_at)
		VALUES(?,?,?,?,?,1,?,?,?,?)`,
		id, d.DriverName, d.Version, d.Description, d.ProtocolFamily, d.SpecURL, d.MessageSchema, now(), now())
	return id, err
}

func (s *Store) ListPlatformAdapters(platformID string) ([]PlatformAdapter, error) {
	q := `SELECT id,platform_id,driver_id,adapter_name,status,COALESCE(compatibility_mode,'strict'),supported_features,COALESCE(config_schema,''),created_at
		FROM platform_adapter_mappings WHERE 1=1`
	var args []any
	if platformID != "" {
		q += ` AND platform_id=?`
		args = append(args, platformID)
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformAdapter{}
	for rows.Next() {
		var a PlatformAdapter
		if err := rows.Scan(&a.ID, &a.PlatformID, &a.DriverID, &a.AdapterName, &a.Status, &a.CompatibilityMode, &a.SupportedFeatures, &a.ConfigSchema, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertPlatformAdapter 平台-适配器映射（UNIQUE(platform_id,driver_id)）
func (s *Store) UpsertPlatformAdapter(a PlatformAdapter) error {
	_, err := s.db.Exec(`INSERT INTO platform_adapter_mappings(platform_id,driver_id,adapter_name,status,compatibility_mode,supported_features,config_schema,created_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(platform_id,driver_id) DO UPDATE SET adapter_name=excluded.adapter_name, status=excluded.status,
		compatibility_mode=excluded.compatibility_mode, supported_features=excluded.supported_features`,
		a.PlatformID, a.DriverID, a.AdapterName, a.Status, a.CompatibilityMode, a.SupportedFeatures, a.ConfigSchema, now())
	return err
}

// ── Agent 模板（FR-22 §2.9）────────────────────

type AgentTemplate struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	TemplateType    string `json:"template_type"`
	AdapterType     string `json:"adapter_type"`
	AdapterConfig   string `json:"adapter_config"`
	ProtocolDriver  string `json:"protocol_driver"`
	DriverVersionPin string `json:"driver_version_pin"`
	MonthlyBudget   float64 `json:"monthly_budget_cents"`
	SpentMonthly    float64 `json:"spent_monthly_cents"`
	InstructionsMD  string `json:"instructions_md"`
	Capabilities    string `json:"capabilities"`
	TimeoutSec      int    `json:"default_timeout_sec"`
	MaxRetries      int    `json:"default_max_retries"`
	IsSystem        bool   `json:"is_system"`
	IsActive        bool   `json:"is_active"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

const templateCols = `id,name,version,COALESCE(description,''),template_type,adapter_type,adapter_config,protocol_driver,
	COALESCE(driver_version_pin,''),monthly_budget_cents,spent_monthly_cents,instructions_md,capabilities,
	COALESCE(default_timeout_sec,0),COALESCE(default_max_retries,0),is_system,is_active,created_at,updated_at`

func (s *Store) scanTemplates(q string, args ...any) ([]AgentTemplate, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentTemplate{}
	for rows.Next() {
		var t AgentTemplate
		var sys, act int
		if err := rows.Scan(&t.ID, &t.Name, &t.Version, &t.Description, &t.TemplateType, &t.AdapterType, &t.AdapterConfig,
			&t.ProtocolDriver, &t.DriverVersionPin, &t.MonthlyBudget, &t.SpentMonthly, &t.InstructionsMD, &t.Capabilities,
			&t.TimeoutSec, &t.MaxRetries, &sys, &act, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.IsSystem, t.IsActive = sys == 1, act == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ListTemplates(activeOnly bool) ([]AgentTemplate, error) {
	q := `SELECT ` + templateCols + ` FROM agent_templates`
	if activeOnly {
		q += ` WHERE is_active=1`
	}
	q += ` ORDER BY name, version`
	return s.scanTemplates(q)
}

func (s *Store) GetTemplate(id string) (AgentTemplate, error) {
	ts, err := s.scanTemplates(`SELECT `+templateCols+` FROM agent_templates WHERE id=?`, id)
	if err != nil || len(ts) == 0 {
		return AgentTemplate{}, err
	}
	return ts[0], nil
}

func (s *Store) UpsertTemplate(t AgentTemplate) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM agent_templates WHERE name=? AND version=?`, t.Name, t.Version).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE agent_templates SET description=?, template_type=?, adapter_type=?, adapter_config=?,
			protocol_driver=?, driver_version_pin=?, monthly_budget_cents=?, instructions_md=?, capabilities=?,
			default_timeout_sec=?, default_max_retries=?, is_active=1, updated_at=? WHERE id=?`,
			t.Description, t.TemplateType, t.AdapterType, t.AdapterConfig, t.ProtocolDriver, t.DriverVersionPin,
			t.MonthlyBudget, t.InstructionsMD, t.Capabilities, t.TimeoutSec, t.MaxRetries, now(), id)
		return id, err
	}
	if err != sqlNoRows() {
		return "", err
	}
	id = NewID()
	_, err = s.db.Exec(`INSERT INTO agent_templates(id,name,version,description,template_type,adapter_type,adapter_config,
		protocol_driver,driver_version_pin,monthly_budget_cents,instructions_md,capabilities,default_timeout_sec,default_max_retries,
		is_system,is_active,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, t.Name, t.Version, t.Description, t.TemplateType, t.AdapterType, t.AdapterConfig,
		t.ProtocolDriver, t.DriverVersionPin, t.MonthlyBudget, t.InstructionsMD, t.Capabilities, t.TimeoutSec, t.MaxRetries,
		boolInt(t.IsSystem), now(), now())
	return id, err
}

// DeactivateTemplate 模板下线（danger 操作；系统模板禁删只停用）
func (s *Store) DeactivateTemplate(id string) error {
	_, err := s.db.Exec(`UPDATE agent_templates SET is_active=0, updated_at=? WHERE id=?`, now(), id)
	return err
}

// ── 命令终端（模块 #32，§6.4.14 A1/A2/A3）──────

type CommandTerminal struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Hostname        string `json:"hostname"`
	Secret          string `json:"-"`
	SecretPrev      string `json:"-"`
	SecretPrevUntil string `json:"-"`
	Allow           string `json:"allow"`
	Status          string `json:"status"`
	LastIP          string `json:"last_ip"`
	ListenPort      int    `json:"listen_port"`
	LastHeartbeat   string `json:"last_heartbeat"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

const terminalCols = `id,name,COALESCE(hostname,''),secret,secret_prev,secret_prev_until,COALESCE(allow,''),status,
	COALESCE(last_ip,''),listen_port,COALESCE(last_heartbeat,''),created_at,updated_at`

func (s *Store) scanTerminals(q string, args ...any) ([]CommandTerminal, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandTerminal{}
	for rows.Next() {
		var t CommandTerminal
		if err := rows.Scan(&t.ID, &t.Name, &t.Hostname, &t.Secret, &t.SecretPrev, &t.SecretPrevUntil, &t.Allow,
			&t.Status, &t.LastIP, &t.ListenPort, &t.LastHeartbeat, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) InsertCommandTerminal(t CommandTerminal) error {
	_, err := s.db.Exec(`INSERT INTO command_terminals(id,name,hostname,secret,allow,status,created_at,updated_at)
		VALUES(?,?,?,?,?,'registered',?,?)`, t.ID, t.Name, t.Hostname, t.Secret, t.Allow, now(), now())
	return err
}

func (s *Store) ListCommandTerminals() ([]CommandTerminal, error) {
	return s.scanTerminals(`SELECT ` + terminalCols + ` FROM command_terminals WHERE status<>'revoked' ORDER BY created_at`)
}

func (s *Store) GetCommandTerminal(id string) (CommandTerminal, error) {
	ts, err := s.scanTerminals(`SELECT `+terminalCols+` FROM command_terminals WHERE id=?`, id)
	if err != nil {
		return CommandTerminal{}, err
	}
	if len(ts) == 0 {
		return CommandTerminal{}, sql.ErrNoRows // 未命中（调用方以 err 判定存在性）
	}
	return ts[0], nil
}

func (s *Store) GetCommandTerminalByName(name string) (CommandTerminal, error) {
	ts, err := s.scanTerminals(`SELECT `+terminalCols+` FROM command_terminals WHERE name=?`, name)
	if err != nil {
		return CommandTerminal{}, err
	}
	if len(ts) == 0 {
		return CommandTerminal{}, sql.ErrNoRows
	}
	return ts[0], nil
}

// TouchCommandTerminal cmd_register 上行刷新（含真实监听端口；§6.4.14 60s 周期，180s 判离线）
func (s *Store) TouchCommandTerminal(id, ip string, listenPort int, hostname string) {
	_, _ = s.db.Exec(`UPDATE command_terminals SET status='online', last_heartbeat=?, last_ip=?, listen_port=?,
		hostname=CASE WHEN ?<>'' THEN ? ELSE hostname END, updated_at=? WHERE id=?`,
		now(), ip, listenPort, hostname, hostname, now(), id)
}

// TouchCommandHeartbeat cmd_heartbeat / cmd_result 上行刷新：只更新心跳与地址，不覆盖注册时的监听端口
func (s *Store) TouchCommandHeartbeat(id, ip string) {
	_, _ = s.db.Exec(`UPDATE command_terminals SET status='online', last_heartbeat=?, last_ip=?, updated_at=? WHERE id=?`,
		now(), ip, now(), id)
}

// TouchCommandTerminalOffline 巡检判离线（连续 3 个心跳周期未收）
func (s *Store) TouchCommandTerminalOffline(id string) error {
	_, err := s.db.Exec(`UPDATE command_terminals SET status='offline', updated_at=? WHERE id=? AND status='online'`, now(), id)
	return err
}

// RotateCommandSecret A2：轮换密钥（新钥生效，旧钥保留 10 分钟双验窗口）
func (s *Store) RotateCommandSecret(id, newSecret string) error {
	_, err := s.db.Exec(`UPDATE command_terminals SET secret_prev=secret, secret_prev_until=?, secret=?, updated_at=? WHERE id=?`,
		time.Now().UTC().Add(10*time.Minute).Format(time.RFC3339), newSecret, now(), id)
	return err
}

func (s *Store) RevokeCommandTerminal(id string) error {
	_, err := s.db.Exec(`UPDATE command_terminals SET status='revoked', updated_at=? WHERE id=?`, now(), id)
	return err
}

type CommandAuditRow struct {
	ID         string `json:"id"`
	TerminalID string `json:"terminal_id"`
	Command    string `json:"command"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	MsgID      string `json:"msg_id"`
	DurationMs int64  `json:"duration_ms"`
	Reject     string `json:"reject"`
	CreatedAt  string `json:"created_at"`
}

// InsertCommandAudit 塔侧审计（与 cmdterm 本地 JSONL 双写，按 msg_id 对账 A3）
func (s *Store) InsertCommandAudit(a CommandAuditRow) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO command_audit(id,terminal_id,command,ok,exit_code,msg_id,duration_ms,reject,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, a.ID, a.TerminalID, a.Command, boolInt(a.OK), a.ExitCode, a.MsgID, a.DurationMs, a.Reject, now())
	return err
}

// InsertCommandIssue 审批式记录塔侧签发指令（cmd_exec 落库，回执前 status 由 msg_id 关联）
func (s *Store) ListCommandAudit(terminalID string, dateFrom, dateTo string, limit int) ([]CommandAuditRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id,terminal_id,command,ok,COALESCE(exit_code,0),COALESCE(msg_id,''),COALESCE(duration_ms,0),reject,created_at
		FROM command_audit WHERE terminal_id=?`
	args := []any{terminalID}
	if dateFrom != "" {
		q += ` AND created_at >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		q += ` AND created_at <= ?`
		args = append(args, dateTo)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandAuditRow{}
	for rows.Next() {
		var a CommandAuditRow
		var ok int
		if err := rows.Scan(&a.ID, &a.TerminalID, &a.Command, &ok, &a.ExitCode, &a.MsgID, &a.DurationMs, &a.Reject, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.OK = ok == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── Admin Token（§6.4.16 read/ops/danger）──────

type AdminToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"-"`
	Scope     string `json:"scope"`
	Revoked   bool   `json:"revoked"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) InsertAdminToken(t AdminToken) error {
	_, err := s.db.Exec(`INSERT INTO admin_tokens(id,name,token_hash,scope,revoked,created_at) VALUES(?,?,?,?,0,?)`,
		t.ID, t.Name, t.TokenHash, t.Scope, now())
	return err
}

func (s *Store) FindAdminToken(hash string) (AdminToken, bool, error) {
	var t AdminToken
	var revoked int
	err := s.db.QueryRow(`SELECT id,name,token_hash,scope,revoked,created_at FROM admin_tokens WHERE token_hash=?`, hash).
		Scan(&t.ID, &t.Name, &t.TokenHash, &t.Scope, &revoked, &t.CreatedAt)
	if err != nil {
		return AdminToken{}, false, nil // 无匹配
	}
	t.Revoked = revoked == 1
	return t, !t.Revoked, nil
}

func (s *Store) ListAdminTokens() ([]AdminToken, error) {
	rows, err := s.db.Query(`SELECT id,name,scope,revoked,created_at FROM admin_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminToken{}
	for rows.Next() {
		var t AdminToken
		var revoked int
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &revoked, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Revoked = revoked == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeAdminToken(id string) error {
	_, err := s.db.Exec(`UPDATE admin_tokens SET revoked=1 WHERE id=?`, id)
	return err
}

func (s *Store) HasAnyAdminToken() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM admin_tokens WHERE revoked=0`).Scan(&n)
	return n > 0, err
}

// ── 孵化器注册令牌（§6.4.16 rotate-token 吊销/轮换）──

// IncubatorTokenHashByName 按孵化器名查独立注册令牌哈希（空 = 未设置）
func (s *Store) IncubatorTokenHashByName(name string) (string, error) {
	var h string
	err := s.db.QueryRow(`SELECT COALESCE(registration_token_hash,'') FROM incubators WHERE name=?`, name).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}

// SetIncubatorTokenHash 轮换/吊销（空串 = 清除独立令牌）
func (s *Store) SetIncubatorTokenHash(id, hash string) error {
	_, err := s.db.Exec(`UPDATE incubators SET registration_token_hash=? WHERE id=?`, hash, id)
	return err
}

// ── 通知渠道（§6.4.17 C9）──────────────────────

type NotifyChannel struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // webhook/wecom/smtp
	Config   string `json:"config"`
	MinLevel string `json:"min_level"`
	Enabled  bool   `json:"enabled"`
}

func (s *Store) ListNotifyChannels() ([]NotifyChannel, error) {
	rows, err := s.db.Query(`SELECT id,kind,config,min_level,enabled FROM notify_channels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyChannel{}
	for rows.Next() {
		var c NotifyChannel
		var en int
		if err := rows.Scan(&c.ID, &c.Kind, &c.Config, &c.MinLevel, &en); err != nil {
			return nil, err
		}
		c.Enabled = en == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListWebhookChannels 告警出站（enabled 且 kind=webhook）
func (s *Store) ListWebhookChannels() ([]NotifyChannel, error) {
	all, err := s.ListNotifyChannels()
	if err != nil {
		return nil, err
	}
	out := []NotifyChannel{}
	for _, c := range all {
		if c.Enabled && c.Kind == "webhook" {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) UpsertNotifyChannel(c NotifyChannel) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM notify_channels WHERE kind=? AND config=?`, c.Kind, c.Config).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE notify_channels SET min_level=?, enabled=? WHERE id=?`, c.MinLevel, boolInt(c.Enabled), id)
		return id, err
	}
	if err != sqlNoRows() {
		return "", err
	}
	id = NewID()
	_, err = s.db.Exec(`INSERT INTO notify_channels(id,kind,config,min_level,enabled) VALUES(?,?,?,?,?)`,
		id, c.Kind, c.Config, c.MinLevel, boolInt(c.Enabled))
	return id, err
}
