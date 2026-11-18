# 位置无关 Agent 协作控制塔实施计划（V1.5）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 MVP 单 Agent 闭环基础上，增加 Mac ↔ 云服务器双 Agent 联动：Mac Zcode 开发，云 Agent 部署，结果与制品通过共享存储跨机传递。WSS 出站、outbox、幂等、失败关闭全部保留。

**Architecture:** 改造 Tower 身份目录、计划校验、冻结分派、步骤依赖与制品传递、Incubator Connector。WSS 是唯一跨网络控制通道。Connector 只从本机 action 构造 Worker Job，事件先写本地 outbox 再回传；跨机制品上传共享存储并校验 digest。

**Tech Stack:** Go 1.22、`database/sql`、`modernc.org/sqlite v1.30.0`、SQL 迁移、`github.com/coder/websocket v1.8.12`；React 18、TypeScript、Vite 5、Vitest 2、Node ≥20。

**规格事实源:** `D:\GitWorks\DailyWorks\projects\macs\design\位置无关Agent协作控制塔设计.md` V1.5。

**当前状态:** V1.5 规格已补齐，未开始编码。

## Global Constraints

- 工程 Git 根目录 `E:\LLM\DSH\macs`；设计和验收记录只放 DailyWorks。
- Tower 与 Connector 仅新增直接依赖 `github.com/coder/websocket v1.8.12`。
- 固定入口 `wss://tower.example.com/agent/connect`，Connector 仅出站 TLS 443，不开放入站端口。
- WSS 是唯一跨网络 Agent 控制通道，旧 UDP 不作为降级路径。
- `connector_id + local_agent_key` 去重；心跳只更新健康/负载/在线状态，不覆盖人工标签。
- 只有 `identity_status = verified` 且 action `enabled = true` 可进入规划和调度；离线不改派冻结 `agent_id`。
- PlanDraft 禁止 `command`、`args`、`cwd`、`stdin`、`url`、`method`、`headers`、`body`、密码、token、secret、authorization、api_key；`instruction` 不作为 shell 命令执行。
- 计划批准后冻结 `plan_revision + step_id + agent_id + action_id + environment + input_digest`；修改必须新建修订并重新审批。
- `input_digest = SHA256(plan_revision | step_id | action_id | environment | normalized_instruction | sorted(input_ref_digests))`，批准时计算，运行时校验实际制品 digest 与声明一致，不一致拒绝执行。
- 跨机制品 URI 只允许 `s3://`、`oci://`、`https://`；本地路径不得进入其他 Connector 的 `input_refs`。制品上传失败时 attempt 标记 failed，不得以本地路径替代。
- `idempotency_key = SHA256(task_id|step_key|plan_revision)`；每次 attempt 新 `attempt_id`。
- `non_retryable` 状态不明转人工；迟到结果按 `plan_revision + attempt_id` 核销，不覆盖新 attempt 或取消终态。
- 执行状态与 `acceptance_status = pending/passed/failed` 分离；缺自检、验证器失败不得放行。
- 断路器：同一 Connector×适配器×模型连续 5 次 5xx/超时熔断 60s 半开；Worker 10 分钟无 token 无产物二次确认销毁；30 分钟 3 次无产物唤醒进 frozen。
- Zcode 契约测试必须用真实本机 action 配置，mock 不算通过。

## 文件与职责地图

| 范围 | 文件 | 职责 |
| --- | --- | --- |
| Tower 持久化 | `macs-tower/migrations/0009_agent_collaboration.sql`、`internal/store/agent_collaboration.go` | 身份、计划、分派、attempt 持久化 |
| 身份目录 | `internal/agentdir/` | 人工标注、verified 目录、禁用与审计 |
| WSS 协议 | `internal/agentmsg/`、`internal/agentgate/` | 消息信封、认证、连接与发送 |
| Connector | `macs-incubator/internal/connector/` | 出站客户端、本机 action、attempt 执行与 outbox |
| 计划 | `macs-tower/internal/plan/` | Draft 解码、确定性校验、LLM 规划 |
| 调度 | `internal/orchestrator/` | 冻结分派、结果核销 |
| 验收 | `internal/acceptance/` | 验证器、自检失败关闭 |
| 管理面 | `macs-tower-web/src/` | 身份标注、计划编辑、批准 UI |
| 制品 | `macs-incubator/internal/connector/artifact.go` | 跨机制品发布、获取与 digest 校验 |
| 依赖 | `macs-tower/internal/orchestrator/deps.go` | 步骤依赖解析与上游制品注入 |
| 验收 | `macs-incubator/internal/connector/contract_test.go`、`macs-tower/test/e2e/` | 真实 Zcode/云 Agent 契约和跨机 E2E |

## 跨任务接口基线

```go
// Task 1，package store。
type Connector struct {
	ID, Name, CredentialID, CredentialHash, ProtocolVersion string
	OnlineStatus, HealthStatus, LoadJSON, LastSeen, RevokedAt string
}
type AgentInstance struct {
	ID, ConnectorID, LocalAgentKey, DisplayName string
	PlatformID, AdapterType string
	RoleTags, CapabilityTags, EnvironmentTags []string
	IdentityStatus, VerifiedBy, VerifiedAt, OnlineStatus string
	HealthStatus, LoadJSON string
	Revision int
	ReportedJSON string
}
type Heartbeat struct {
	ConnectorID, LocalAgentKey, HealthStatus, LoadJSON, OnlineStatus string
}
type AgentAction struct {
	ID, AgentID, ActionKey, ActionKind, AdapterType string
	RiskLevel, RetryPolicy string
	MaxAttempts int
	ValidatorKeys []string
	ConfigRef string
	SecretRefs []string
	Enabled bool
	Revision int
}
type PlanRevision struct {
	TaskID string
	Revision int
	Source, Status, DraftJSON, CatalogDigest string
	ApprovedBy, ApprovedAt string
}
type TaskAssignment struct {
	TaskID, StepID string
	PlanRevision int
	AgentID, ActionID string
	Environment, InputDigest, ApprovedBy, ApprovedAt string
}
type ExecutionAttempt struct {
	AttemptID, TaskID, StepID string
	PlanRevision, AttemptNo int
	IdempotencyKey, ExecutionContextID string
	AgentID, ActionID, Environment, InputDigest string
	Status, AcceptanceStatus string
	ResultJSON string
	LeaseExpiresAt, CreatedAt, FinishedAt string
}
type StoredArtifact struct {
	ID, AttemptID, URI, Digest, Kind string
	SizeBytes int64
	CreatedAt string
}

// Task 2，package agentdir。
type VerifyPatch struct {
	DisplayName, PlatformID, AdapterType string
	RoleTags, CapabilityTags, EnvironmentTags []string
	ExpectedRevision int
	Actor string
}
type Service struct{ st *store.Store }

// Task 3，package agentmsg。
type ArtifactRef struct {
	URI string `json:"uri"`
	Digest string `json:"digest"`
	Kind string `json:"kind"`
	SizeBytes int64 `json:"size_bytes"`
}
type Envelope struct {
	MessageID, MessageType, ProtocolVersion string
	ConnectorID, AgentID string
	TaskID, StepID, PlanRevision string
	ActionID, ActionKey, AttemptID, IdempotencyKey string
	Environment, InputDigest string
	LeaseExpiresAt, Timeout string
	InputRefs []ArtifactRef `json:"input_refs,omitempty"`
	Payload json.RawMessage
}
type ResultPayload struct {
	Status, Summary, ResultRef string
	ArtifactRefs []ArtifactRef `json:"artifact_refs"`
	SelfCheck json.RawMessage
	AcceptanceEvidence json.RawMessage
	TokenUsage struct{ Input, Output int64 }
	Error string
}

// Task 4，package connector。
type Registry struct{ byBinding map[string]ActionConfig }
type Runner interface{ Run(ctx context.Context, job worker.Job) worker.Outcome }

// Task 5，package plan。
type Draft struct {
	Goal, TargetEnvironment string
	Steps []Step
	TimeoutMS int64
}
type Step struct {
	StepKey, Title, Instruction string
	AgentID, ActionKey string
	StepKind string
	DependsOn []string
	ExpectedOutputs []string
	InputRefs []string `json:"input_refs"`
	AcceptanceCriteria []Criterion
	TimeoutMS int64
}
type Criterion struct {
	ValidatorKey string
	Params json.RawMessage
}

// Task 6，package acceptance。
type ValidationResult struct{ Status string; Reason string }
```

---

## Task 1: Tower 协作 Schema 与身份去重持久化

**Files:**
- Create: `macs-tower/migrations/0009_agent_collaboration.sql`
- Create: `macs-tower/internal/store/agent_collaboration.go`
- Test: `macs-tower/internal/store/agent_collaboration_test.go`

- [ ] **Step 1: 写候选重连不覆盖人工身份和心跳隔离测试**

```go
func TestAgentCandidateReconnectPreservesVerifiedIdentity(t *testing.T) {
	st := openCollaborationStore(t)
	st.UpsertConnector(Connector{ID: "c1", Name: "mac-dev", CredentialID: "cred-1", CredentialHash: "hash", ProtocolVersion: "1.0"})
	first, _ := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Zcode", ReportedJSON: `{"adapter":"cli_agent"}`})
	st.Exec(`UPDATE agent_instances SET identity_status='verified', platform_id='zcode', role_tags='["developer"]', revision=3, verified_by='admin-1', display_name='Mac Zcode' WHERE id=?`, first.ID)
	second, _ := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "renamed-by-report", ReportedJSON: `{"adapter":"http_agent"}`})
	if second.DisplayName != "Mac Zcode" { t.Fatalf("display_name overwritten: %s", second.DisplayName) }
	if second.IdentityStatus != "verified" || second.PlatformID != "zcode" || second.Revision != 3 { t.Fatalf("identity overwritten: %+v", second) }
	st.UpdateHeartbeat(Heartbeat{ConnectorID: "c1", LocalAgentKey: "zcode", HealthStatus: "degraded", LoadJSON: `{"active":2}`, OnlineStatus: "online"})
	after, _ := st.GetAgentInstance(second.ID)
	if after.HealthStatus != "degraded" || after.PlatformID != "zcode" || after.Revision != 3 { t.Fatalf("heartbeat changed identity") }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/store -run TestAgentCandidateReconnectPreservesVerifiedIdentity -count=1`

Expected: FAIL，类型和迁移缺失。

- [ ] **Step 3: 实现迁移和 Store**

```sql
CREATE TABLE connectors (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, credential_id TEXT NOT NULL,
  credential_hash TEXT NOT NULL, protocol_version TEXT NOT NULL,
  online_status TEXT NOT NULL DEFAULT 'offline', health_status TEXT NOT NULL DEFAULT 'unknown',
  load_json TEXT NOT NULL DEFAULT '{}', last_seen TEXT NOT NULL DEFAULT '', revoked_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE agent_instances (
  id TEXT PRIMARY KEY, connector_id TEXT NOT NULL, local_agent_key TEXT NOT NULL,
  display_name TEXT NOT NULL, platform_id TEXT NOT NULL DEFAULT '', adapter_type TEXT NOT NULL DEFAULT '',
  role_tags TEXT NOT NULL DEFAULT '[]', capability_tags TEXT NOT NULL DEFAULT '[]', environment_tags TEXT NOT NULL DEFAULT '[]',
  identity_status TEXT NOT NULL DEFAULT 'pending', verified_by TEXT NOT NULL DEFAULT '', verified_at TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1, online_status TEXT NOT NULL DEFAULT 'offline',
  health_status TEXT NOT NULL DEFAULT 'unknown', load_json TEXT NOT NULL DEFAULT '{}',
  reported_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(connector_id, local_agent_key)
);
CREATE TABLE agent_actions (
  id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, action_key TEXT NOT NULL,
  action_kind TEXT NOT NULL DEFAULT 'execute', adapter_type TEXT NOT NULL,
  risk_level TEXT NOT NULL DEFAULT 'normal', retry_policy TEXT NOT NULL DEFAULT 'retryable',
  max_attempts INTEGER NOT NULL DEFAULT 3, validator_keys TEXT NOT NULL DEFAULT '[]',
  config_ref TEXT NOT NULL, secret_refs TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE(agent_id, action_key)
);
CREATE TABLE plan_revisions (
  task_id TEXT NOT NULL, revision INTEGER NOT NULL, source TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft', draft_json TEXT NOT NULL,
  catalog_digest TEXT NOT NULL, approved_by TEXT NOT NULL DEFAULT '', approved_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(task_id, revision)
);
CREATE TABLE task_assignments (
  task_id TEXT NOT NULL, step_id TEXT NOT NULL, plan_revision INTEGER NOT NULL,
  agent_id TEXT NOT NULL, action_id TEXT NOT NULL,
  environment TEXT NOT NULL, input_digest TEXT NOT NULL,
  approved_by TEXT NOT NULL, approved_at TEXT NOT NULL,
  PRIMARY KEY(task_id, step_id, plan_revision)
);
CREATE TABLE execution_attempts (
  attempt_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, step_id TEXT NOT NULL,
  plan_revision INTEGER NOT NULL, attempt_no INTEGER NOT NULL,
  idempotency_key TEXT NOT NULL, execution_context_id TEXT NOT NULL,
  agent_id TEXT NOT NULL, action_id TEXT NOT NULL, environment TEXT NOT NULL, input_digest TEXT NOT NULL,
  status TEXT NOT NULL, acceptance_status TEXT NOT NULL DEFAULT 'pending',
  result_json TEXT NOT NULL DEFAULT '', lease_expires_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '',
  UNIQUE(step_id, attempt_no)
);
```

```go
func (s *Store) UpsertAgentCandidate(a AgentInstance) (AgentInstance, error) {
	ts := now()
	if a.ID == "" { a.ID = NewID() }
	_, err := s.db.Exec(`INSERT INTO agent_instances(
		id,connector_id,local_agent_key,display_name,reported_json,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?) ON CONFLICT(connector_id,local_agent_key) DO UPDATE SET
		reported_json=excluded.reported_json, online_status='online', updated_at=excluded.updated_at`,
		a.ID, a.ConnectorID, a.LocalAgentKey, a.DisplayName, a.ReportedJSON, ts, ts)
	if err != nil { return AgentInstance{}, err }
	return s.GetAgentInstanceByKey(a.ConnectorID, a.LocalAgentKey)
}

func (s *Store) UpdateHeartbeat(h Heartbeat) error {
	if h.OnlineStatus == "" { h.OnlineStatus = "online" }
	result, err := s.db.Exec(
		`UPDATE agent_instances SET health_status=?, load_json=?, online_status=?, updated_at=? WHERE connector_id=? AND local_agent_key=?`,
		h.HealthStatus, h.LoadJSON, h.OnlineStatus, now(), h.ConnectorID, h.LocalAgentKey)
	if err != nil { return err }
	rows, _ := result.RowsAffected()
	if rows != 1 { return fmt.Errorf("agent heartbeat target not found: %s/%s", h.ConnectorID, h.LocalAgentKey) }
	return nil
}
```

- [ ] **Step 4: 运行 store 回归**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/store -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/migrations/0009_agent_collaboration.sql macs-tower/internal/store/agent_collaboration.go macs-tower/internal/store/agent_collaboration_test.go
git -C E:\LLM\DSH\macs commit -m "feat: add agent collaboration schema and identity store"
```

---

## Task 2: 人工身份目录与受控标注

**Files:**
- Create: `macs-tower/internal/agentdir/directory.go`
- Test: `macs-tower/internal/agentdir/directory_test.go`

- [ ] **Step 1: 写 verified 目录只用 verified enabled action 测试**

```go
func TestVerifiedCatalogUsesOnlyVerifiedEnabledActions(t *testing.T) {
	st := openStore(t)
	seedConnector(t, st, "c1")
	pending, _ := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "pending", DisplayName: "pending"})
	verified, _ := st.UpsertAgentCandidate(AgentInstance{ConnectorID: "c1", LocalAgentKey: "zcode", DisplayName: "Mac Zcode"})
	svc := agentdir.NewService(st)
	svc.Verify(verified.ID, VerifyPatch{DisplayName: "Mac Zcode", PlatformID: "zcode", AdapterType: "cli_agent", RoleTags: []string{"developer"}, CapabilityTags: []string{"repo.write", "test.run"}, EnvironmentTags: []string{"dev"}, ExpectedRevision: 1, Actor: "admin-1"})
	st.UpsertAgentAction(AgentAction{ID: "act-disabled", AgentID: verified.ID, ActionKey: "zcode.develop", AdapterType: "cli_agent", ValidatorKeys: []string{"test_report"}, ConfigRef: "zcode", Enabled: false})
	st.UpsertAgentAction(AgentAction{ID: "act-enabled", AgentID: verified.ID, ActionKey: "zcode.test", AdapterType: "cli_agent", ValidatorKeys: []string{"test_report"}, ConfigRef: "zcode", Enabled: true})
	cat := svc.Catalog()
	if len(cat.Agents) != 1 || cat.Agents[0].AgentID != verified.ID { t.Fatal("pending agent leaked into catalog") }
	keys := []string{}
	for _, a := range cat.Agents[0].Actions { keys = append(keys, a.ActionKey) }
	if !contains(keys, "zcode.test") || contains(keys, "zcode.develop") { t.Fatalf("wrong actions: %v", keys) }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentdir -run TestVerifiedCatalogUsesOnlyVerifiedEnabledActions -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现 Service.Verify 和 Catalog**

`Verify` 用事务更新 `display_name`、`platform_id`、`adapter_type`、标签、`identity_status='verified'`、`revision=ExpectedRevision+1`、`verified_by/at`。`Catalog` 只返回 `identity_status='verified'` 且 `enabled=1` 的 action。标签修改写审计表。

- [ ] **Step 4: 运行目录回归**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentdir -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/internal/agentdir
git -C E:\LLM\DSH\macs commit -m "feat: add verified agent identity catalog"
```

---

## Task 3: WSS 网关与 Connector 出站客户端

**Files:**
- Create: `macs-tower/internal/agentmsg/envelope.go`
- Create: `macs-tower/internal/agentgate/gateway.go`
- Create: `macs-incubator/internal/connector/client.go`
- Test: `macs-tower/internal/agentgate/gateway_test.go`
- Test: `macs-incubator/internal/connector/client_test.go`

- [ ] **Step 1: 写凭据和协议版本校验测试**

```go
func TestGatewayRejectsInvalidCredentialAndProtocol(t *testing.T) {
	gw := newTestGateway(t, validCredential("c1", "hash-1"))
	conn := dial(t, gw.URL())
	send(t, conn, agentmsg.Envelope{MessageType: "hello", ProtocolVersion: "2.0", ConnectorID: "c1"})
	msg := read(t, conn)
	if msg.MessageType != "error" || !strings.Contains(msg.Payload, "protocol") { t.Fatalf("expected protocol error: %+v", msg) }
	conn2 := dial(t, gw.URL())
	send(t, conn2, agentmsg.Envelope{MessageType: "hello", ProtocolVersion: "1.0", ConnectorID: "c1", CredentialHash: "wrong"})
	msg2 := read(t, conn2)
	if msg2.MessageType != "error" { t.Fatal("invalid credential accepted") }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentgate -run TestGatewayRejectsInvalidCredentialAndProtocol -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现网关和客户端**

网关用 `github.com/coder/websocket`，握手校验 `connector_id + credential_hash` 和 `protocol_version`。消息用 `agentmsg.Envelope` JSON。Connector 客户端带退避重连，发送候选上报和心跳，接收 dispatch。

- [ ] **Step 4: 运行网关和客户端测试**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentgate -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/internal/agentmsg macs-tower/internal/agentgate macs-incubator/internal/connector
git -C E:\LLM\DSH\macs commit -m "feat: add WSS gateway and connector outbound client"
```

---

## Task 4: PlanDraft 校验、人工批准与冻结分派

**Files:**
- Create: `macs-tower/internal/plan/draft.go`
- Create: `macs-tower/internal/plan/validate.go`
- Modify: `macs-tower/internal/orchestrator/orchestrator.go`
- Modify: `macs-tower/internal/store/tasks.go`
- Test: `macs-tower/internal/plan/validate_test.go`
- Test: `macs-tower/internal/orchestrator/strict_dispatch_test.go`

- [ ] **Step 1: 写禁用字段拒绝和冻结分派测试**

```go
func TestDraftRejectsExecutableFieldsAndInvalidAssignments(t *testing.T) {
	bad := []byte(`{"goal":"x","steps":[{"step_key":"s1","instruction":"do","agent_id":"a1","action_key":"act","command":"rm -rf /"}]}`)
	_, err := plan.DecodeDraft(bad)
	if err == nil || !strings.Contains(err.Error(), "command") { t.Fatalf("command field accepted: %v", err) }
}

func TestStrictDispatchWaitsForFrozenAgentAndDoesNotReassign(t *testing.T) {
	o, st, transport := newStrictDispatchFixture(t, "offline")
	step := seedApprovedStep(t, st, "agent-zcode", "action-develop")
	o.scheduleOnce(context.Background())
	if len(transport.messages) != 0 { t.Fatal("offline agent received dispatch") }
	got, _ := st.GetSubtask(step)
	if got.AgentID != "agent-zcode" || got.Status != "pending" { t.Fatalf("assignment changed: %+v", got) }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/plan -run TestDraftRejectsExecutableFieldsAndInvalidAssignments -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/orchestrator -run TestStrictDispatchWaitsForFrozenAgentAndDoesNotReassign -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现校验、批准和冻结分派**

`DecodeDraft` 拒绝任何禁用字段。`Validate` 检查 Agent verified、action enabled、验证器白名单。`Approve` 计算 `input_digest`，保存 `plan_revisions`、`task_assignments` 和 subtask，状态为 pending。`scheduleOnce` 只 claim 冻结的 subtask，Agent 离线返回 pending 不改派。删除 `selectIncubators`、top-K、first-ACK-wins 改派路径。

- [ ] **Step 4: 运行计划和调度回归**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/plan ./internal/orchestrator ./internal/store -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/internal/plan macs-tower/internal/orchestrator macs-tower/internal/store
git -C E:\LLM\DSH\macs commit -m "feat: validate drafts, approve frozen assignments"
```

---

## Task 5: WSS 执行、outbox、幂等与自检失败关闭

**Files:**
- Modify: `macs-incubator/internal/connector/client.go`
- Create: `macs-incubator/internal/connector/outbox.go`
- Create: `macs-tower/internal/acceptance/validators.go`
- Modify: `macs-tower/internal/quality/selfcheck.go`
- Test: `macs-incubator/internal/connector/outbox_test.go`
- Test: `macs-tower/internal/acceptance/validators_test.go`

- [ ] **Step 1: 写 outbox 持久化和缺自检失败测试**

```go
func TestOutboxPersistsBeforeSendAndSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "outbox.db")
	out := openOutbox(t, dbPath)
	ev := OutboxEvent{EventID: "e1", ConnectorID: "c1", EventType: "result", Payload: json.RawMessage(`{"status":"completed"}`)}
	out.Append(ev)
	out2 := openOutbox(t, dbPath)
	pending := out2.Pending()
	if len(pending) != 1 || pending[0].EventID != "e1" { t.Fatalf("outbox not persisted: %+v", pending) }
}

func TestMissingSelfCheckFailsClosed(t *testing.T) {
	report := quality.SelfCheckFromResult(`{"status":"completed","summary":"done"}`)
	if report.Status != "failed" { t.Fatalf("missing self_check passed: %+v", report) }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -run TestOutboxPersistsBeforeSendAndSurvivesRestart -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/acceptance ./internal/quality -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现 outbox、幂等和自检失败关闭**

outbox 用 SQLite，`Append` 在发送前持久化，`MarkSynced` 只在塔侧 ACK 后调用。Connector 用 `idempotency_key` 去重重复 dispatch。迟到结果按 `plan_revision + attempt_id` 核销，不覆盖已取消/失败。

```go
func SelfCheckFromResult(result string) SelfCheckReport {
	var wrapper struct{ SelfCheck *SelfCheckReport `json:"self_check"` }
	if json.Unmarshal([]byte(result), &wrapper) != nil || wrapper.SelfCheck == nil {
		return SelfCheckReport{Status: "failed", Findings: []Finding{{Severity: "blocking", Message: "missing structured self_check"}}}
	}
	report := *wrapper.SelfCheck
	if report.Status == "" { report.Status = "failed" }
	return report
}
```

验收内置 `json_field_equals`、`test_report`、`manual_review`；机器规则失败得 failed，只剩 manual_review 得 pending，全部通过得 passed。

- [ ] **Step 4: 运行 outbox、验收和质量测试**

Run: `go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/acceptance ./internal/quality ./internal/orchestrator -count=1`

Expected: PASS；缺自检、验证器失败不归档。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-incubator/internal/connector macs-tower/internal/acceptance macs-tower/internal/quality
git -C E:\LLM\DSH\macs commit -m "feat: persist outbox, enforce idempotency and self-check fail-closed"
```

---

## Task 6: Mac Zcode 真实契约 E2E

**Files:**
- Create: `macs-incubator/internal/connector/contract_test.go`
- Create: `macs-tower/test/e2e/mvp_zcode_test.go`
- Evidence: `D:\GitWorks\DailyWorks\projects\macs\verification\agent-collaboration\mvp-zcode-e2e.json`

**Interfaces:**
- Consumes: 真实 `MACS_E2E_API`、`MACS_E2E_WSS_URL`、`MACS_E2E_ADMIN_TOKEN`、`MACS_E2E_ZCODE_AGENT_ID`、管理员提供的真实本机 action 配置。
- Produces: go test JSON 证据；不提交 token、配置或虚构结果。

- [ ] **Step 1: 写真实 Zcode 闭环测试**

```go
func TestRealZcodeDevelopAndArchive(t *testing.T) {
	requireE2EEnvironment(t)
	before := getAgentSnapshot(t, os.Getenv("MACS_E2E_ZCODE_AGENT_ID"))
	draft := createDraft(t, "implement a small fixture change and run tests")
	compiled := approvePlan(t, draft)
	waitTaskTerminal(t, compiled.TaskID, 30*time.Minute)
	after := getAgentSnapshot(t, os.Getenv("MACS_E2E_ZCODE_AGENT_ID"))
	if before.AgentID != after.AgentID { t.Fatal("agent identity changed") }
	result := getTaskResult(t, compiled.TaskID)
	if result.Status != "completed" || result.AcceptanceStatus != "passed" { t.Fatalf("real acceptance failed: %+v", result) }
	assertAuditChain(t, compiled.TaskID)
}
```

- [ ] **Step 2: 运行真实契约测试**

Run:

```powershell
$env:MACS_RUN_REAL_AGENT_CONTRACT='1'
go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -run TestRealRegisteredAgentActions -v -count=1
```

Expected: 使用真实 Zcode action 配置通过；缺少真实配置记 BLOCKED，不用 mock 替代。

- [ ] **Step 3: 运行 Mac E2E 并保存证据**

```powershell
$evidence = 'D:\GitWorks\DailyWorks\projects\macs\verification\agent-collaboration'
New-Item -ItemType Directory -Force $evidence | Out-Null
go -C E:\LLM\DSH\macs\macs-tower test ./test/e2e -run TestRealZcodeDevelopAndArchive -v -json -count=1 | Tee-Object -FilePath "$evidence\mvp-zcode-e2e.json"
if ($LASTEXITCODE -ne 0) { throw "MVP E2E failed: $LASTEXITCODE" }
```

Expected: 退出码 0，JSON 包含 Zcode 执行→自检→验收→归档全链路，`agent_id` 不变，验收失败不归档。

- [ ] **Step 4: 提交测试代码**

```powershell
git -C E:\LLM\DSH\macs add macs-incubator/internal/connector/contract_test.go macs-tower/test/e2e
git -C E:\LLM\DSH\macs commit -m "test: verify real Zcode MVP end-to-end"
```

DailyWorks 证据文件不加入工程 Git。

---

## Task 7: 跨机制品发布、获取与 digest 校验

**Files:**
- Create: `macs-incubator/internal/connector/artifact.go`
- Create: `macs-tower/internal/store/artifact.go`（或并入 `agent_collaboration.go`）
- Modify: `macs-tower/migrations/0009_agent_collaboration.sql`
- Test: `macs-incubator/internal/connector/artifact_test.go`

- [ ] **Step 1: 写制品 URI 校验和 digest 校验测试**

```go
func TestValidateArtifactRefRejectsLocalPathAndBadDigest(t *testing.T) {
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "/Users/mac/shot.png", Digest: "sha256:abc", Kind: "screenshot", SizeBytes: 10}); err == nil {
		t.Fatal("local path accepted as shared artifact")
	}
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "s3://bucket/obj", Digest: "not-a-sha256", Kind: "log", SizeBytes: 10}); err == nil {
		t.Fatal("invalid digest accepted")
	}
	if err := ValidateArtifactRef(agentmsg.ArtifactRef{URI: "https://store/obj", Digest: "sha256:" + strings.Repeat("a", 64), Kind: "image", SizeBytes: 1024}); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
}

func TestDownloadVerifiesDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer server.Close()
	// digest of "hello" is 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	good := agentmsg.ArtifactRef{URI: server.URL, Digest: "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", Kind: "log"}
	if _, err := DownloadAndVerify(context.Background(), good); err != nil { t.Fatalf("matching digest rejected: %v", err) }
	bad := agentmsg.ArtifactRef{URI: server.URL, Digest: "sha256:" + strings.Repeat("0", 64), Kind: "log"}
	if _, err := DownloadAndVerify(context.Background(), bad); err == nil { t.Fatal("digest mismatch accepted") }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -run 'TestValidateArtifactRefRejectsLocalPathAndBadDigest|TestDownloadVerifiesDigest' -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现制品发布与获取**

迁移增加 `execution_artifacts` 表（id, attempt_id, uri, digest, kind, size_bytes, created_at）。`ValidateArtifactRef` 只接受 `s3://`、`oci://`、`https://` URI 和合法 SHA-256 digest。`DownloadAndVerify` 下载后校验 digest，不一致返回错误。Connector 执行完成后将大型制品上传共享存储，生成 `ArtifactRef`；上传失败则 attempt 标记 failed。

- [ ] **Step 4: 运行制品测试**

Run: `go -C E:\LLM\DSH\macs\macs-incubator test ./internal/connector -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/store -count=1`

Expected: PASS。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/migrations/0009_agent_collaboration.sql macs-tower/internal/store macs-incubator/internal/connector
git -C E:\LLM\DSH\macs commit -m "feat: publish and verify cross-machine artifacts"
```

---

## Task 8: 多 Agent 步骤依赖与上游制品注入

**Files:**
- Modify: `macs-tower/internal/orchestrator/orchestrator.go`
- Create: `macs-tower/internal/orchestrator/deps.go`
- Modify: `macs-tower/internal/plan/validate.go`
- Test: `macs-tower/internal/orchestrator/deps_test.go`
- Test: `macs-tower/internal/plan/validate_test.go`

- [ ] **Step 1: 写依赖校验和上游失败阻断测试**

```go
func TestValidateRejectsCyclicAndMissingDependencies(t *testing.T) {
	cyclic := Draft{Steps: []Step{
		{StepKey: "a", DependsOn: []string{"b"}},
		{StepKey: "b", DependsOn: []string{"a"}},
	}}
	if _, err := Validate(cyclic, emptyCatalog()); err == nil { t.Fatal("cycle accepted") }
	missing := Draft{Steps: []Step{{StepKey: "a", DependsOn: []string{"none"}}}}
	if _, err := Validate(missing, emptyCatalog()); err == nil { t.Fatal("missing dependency accepted") }
}

func TestDownstreamNotDispatchedWhenUpstreamFailed(t *testing.T) {
	o, st, transport := newTwoAgentFixture(t)
	develop, deploy := seedTwoStepPlan(t, st)
	markAttemptFailed(t, st, develop)
	o.scheduleOnce(context.Background())
	if len(transport.messages) != 0 { t.Fatal("downstream dispatched after upstream failure") }
}
```

- [ ] **Step 2: 运行并确认失败**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/plan -run TestValidateRejectsCyclicAndMissingDependencies -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/orchestrator -run TestDownstreamNotDispatchedWhenUpstreamFailed -count=1`

Expected: FAIL。

- [ ] **Step 3: 实现依赖解析与制品注入**

`Validate` 检查 DAG 无环、`depends_on` 引用存在、`input_refs` 声明的制品类型可从上游 `expected_outputs` 获得。`scheduleOnce` 只调度所有上游 `acceptance_status = passed` 的步骤；调度前收集上游 `execution_artifacts`，按 `input_refs` 类型过滤，注入 `Envelope.InputRefs`。`Approve` 计算 `input_digest` 时包含排序后的声明输入制品 digest。

- [ ] **Step 4: 运行依赖和调度回归**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/plan ./internal/orchestrator ./internal/store -count=1`

Expected: PASS；上游失败下游不调度，上游制品注入下游。

- [ ] **Step 5: 提交**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/internal/orchestrator macs-tower/internal/plan
git -C E:\LLM\DSH\macs commit -m "feat: resolve step dependencies and inject upstream artifacts"
```

---

## Task 9: 云服务器 Connector 与第二个 Agent

**Files:**
- Reuse: `macs-tower/internal/agentgate/gateway.go`、`macs-incubator/internal/connector/client.go`
- Test: `macs-tower/internal/agentgate/gateway_test.go`（多连接）
- Test: `macs-tower/internal/agentdir/directory_test.go`（双 Agent 目录）

- [ ] **Step 1: 写双 Connector 并发连接和目录测试**

```go
func TestGatewaySupportsMultipleConnectors(t *testing.T) {
	gw := newTestGateway(t, validCredential("mac", "mac-hash"), validCredential("cloud", "cloud-hash"))
	mac := dialAndHello(t, gw.URL(), "mac", "mac-hash")
	cloud := dialAndHello(t, gw.URL(), "cloud", "cloud-hash")
	if mac == nil || cloud == nil { t.Fatal("both connectors must connect") }
}

func TestCatalogContainsBothVerifiedAgents(t *testing.T) {
	st := openStore(t)
	seedConnector(t, st, "mac")
	seedConnector(t, st, "cloud")
	macID := seedVerifiedAgent(t, st, "mac", "zcode")
	cloudID := seedVerifiedAgent(t, st, "cloud", "openclaw")
	cat := agentdir.NewService(st).Catalog()
	ids := map[string]bool{}
	for _, a := range cat.Agents { ids[a.AgentID] = true }
	if !ids[macID] || !ids[cloudID] { t.Fatalf("both agents must be in catalog: %v", ids) }
}
```

- [ ] **Step 2: 运行并确认失败（如未实现多连接）**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentgate -run TestGatewaySupportsMultipleConnectors -count=1`

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentdir -run TestCatalogContainsBothVerifiedAgents -count=1`

Expected: 若 Task 3 网关已支持多连接则 PASS；否则 FAIL。

- [ ] **Step 3: 确认云 Connector 复用现有出站客户端**

云服务器 Connector 与 Mac Connector 使用相同 `client.go` 和 `outbox.go`，仅本机 action 配置不同。网关已支持多 Connector 并发连接，无需额外代码。人工在 Web UI 标注云 Agent 为 verified 并登记 action。

- [ ] **Step 4: 运行网关和目录回归**

Run: `go -C E:\LLM\DSH\macs\macs-tower test ./internal/agentgate ./internal/agentdir -count=1`

Expected: PASS。

- [ ] **Step 5: 提交（如有代码变更）**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/internal/agentgate macs-tower/internal/agentdir
git -C E:\LLM\DSH\macs commit -m "test: verify multiple connectors and dual agent catalog"
```

---

## Task 10: 跨机 E2E（Mac 开发 → 云部署）

**Files:**
- Create: `macs-tower/test/e2e/cross_machine_test.go`
- Evidence: `D:\GitWorks\DailyWorks\projects\macs\verification\agent-collaboration\cross-machine-e2e.json`

**Interfaces:**
- Consumes: 真实 `MACS_E2E_API`、`MACS_E2E_WSS_URL`、`MACS_E2E_ADMIN_TOKEN`、`MACS_E2E_ZCODE_AGENT_ID`、`MACS_E2E_CLOUD_AGENT_ID`、共享存储配置、两个 Agent 的真实本机 action 配置。
- Produces: go test JSON 证据；不提交 token、配置或虚构结果。

- [ ] **Step 1: 写跨机双 Agent 闭环测试**

```go
func TestRealCrossMachineDevelopAndDeploy(t *testing.T) {
	requireE2EEnvironment(t)
	zcodeID := os.Getenv("MACS_E2E_ZCODE_AGENT_ID")
	cloudID := os.Getenv("MACS_E2E_CLOUD_AGENT_ID")
	before := getAgentSnapshot(t, zcodeID, cloudID)
	draft := createTwoStepDraft(t, zcodeID, cloudID, "implement fixture change, deploy to staging, verify health")
	compiled := approvePlan(t, draft)
	waitTaskTerminal(t, compiled.TaskID, 45*time.Minute)
	after := getAgentSnapshot(t, zcodeID, cloudID)
	if before.ZcodeID != after.ZcodeID || before.CloudID != after.CloudID { t.Fatal("agent identity changed") }
	result := getTaskResult(t, compiled.TaskID)
	if result.Status != "completed" || result.AcceptanceStatus != "passed" { t.Fatalf("cross-machine acceptance failed: %+v", result) }
	assertArtifactExchangedViaSharedStorage(t, compiled.TaskID)
	assertAuditChain(t, compiled.TaskID)
}

func TestUpstreamFailureBlocksDownstreamInE2E(t *testing.T) {
	requireE2EEnvironment(t)
	taskID := runFailingDevelopStep(t)
	waitTaskTerminal(t, taskID, 30*time.Minute)
	result := getTaskResult(t, taskID)
	if result.AcceptanceStatus != "failed" { t.Fatal("upstream failure not propagated") }
	if downstreamAttemptCreated(t, taskID) { t.Fatal("downstream ran after upstream failure") }
}
```

- [ ] **Step 2: 运行跨机 E2E 并保存证据**

```powershell
$evidence = 'D:\GitWorks\DailyWorks\projects\macs\verification\agent-collaboration'
New-Item -ItemType Directory -Force $evidence | Out-Null
go -C E:\LLM\DSH\macs\macs-tower test ./test/e2e -run 'TestRealCrossMachineDevelopAndDeploy|TestUpstreamFailureBlocksDownstreamInE2E' -v -json -count=1 | Tee-Object -FilePath "$evidence\cross-machine-e2e.json"
if ($LASTEXITCODE -ne 0) { throw "cross-machine E2E failed: $LASTEXITCODE" }
```

Expected: 退出码 0，JSON 包含 Zcode 开发→共享存储制品→云 Agent 部署→验收→归档全链路；上游失败时下游不执行；两个 `agent_id` 不变。

- [ ] **Step 3: 提交测试代码**

```powershell
git -C E:\LLM\DSH\macs add macs-tower/test/e2e
git -C E:\LLM\DSH\macs commit -m "test: verify cross-machine develop and deploy"
```

DailyWorks 证据文件不加入工程 Git。

---

## 完成定义

V1.5 完成当且仅当：

1. Task 1–9 全部单元/集成测试通过。
2. Task 6（Mac Zcode）和 Task 10（跨机）真实 E2E 证据文件存在且退出码 0。
3. 以下不变量全部成立：
   - 候选重连不覆盖人工身份。
   - 未 verified Agent 不进入调度。
   - PlanDraft 含可执行字段被拒绝。
   - Agent 离线不改派。
   - 缺自检或验收失败不归档。
   - 重连后执行中 attempt 先对账不重复。
   - 已取消 attempt 迟到结果不覆盖。
   - 本地路径不得作为跨机制品 URI。
   - 制品 digest 不匹配下游拒绝执行。
   - 上游步骤失败下游不调度。
   - 跨机制品通过共享存储交换，两个 Agent `agent_id` 不变。
4. 设计状态可写为“V1.5 已实现并验收”。

V2 能力（merge/review、抽查、高风险独立审批、advisory、控制塔迁移、预算、通知、模板、定时）不在本完成定义内。
