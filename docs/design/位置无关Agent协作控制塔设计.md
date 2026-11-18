# MACS 位置无关 Agent 协作控制塔设计（V1.5）

| 项目 | 内容 |
| --- | --- |
| 文档版本 | V1.5 |
| 编写日期 | 2026-11-18 |
| 状态 | V1.5 规格已补齐，待开发 Agent 实施 |
| 设计事实源 | DailyWorks，不进入 GitHub 工程仓库 |
| 实施计划 | [位置无关 Agent 协作控制塔实施计划（V1.5）](./位置无关Agent协作控制塔实施计划.md) |
| 目标 | 在 MVP 单 Agent 闭环基础上，增加 Mac ↔ 云服务器双 Agent 联动：Mac Zcode 开发，云 Agent 部署，结果与制品跨机传递 |

## 1. 目标与非目标

### 1.1 V1.5 目标

在 MVP 单 Agent 闭环基础上，增加双 Agent 协作：

1. Mac Connector 管理 Zcode（开发、测试），云服务器 Connector 管理部署 Agent（如 OpenClaw）。
2. 一个 PlanRevision 可包含多个步骤，跨 Agent 有依赖关系。
3. 上游步骤的 commit SHA、镜像 digest、测试结果等通过制品引用传递给下游。
4. 跨机制品通过共享存储交换，带 digest 校验和访问控制。
5. 跨机 E2E 验证：Zcode 开发 → 输出制品 → 云 Agent 部署 → 验收。

### 1.2 V1.5 仍不做（V2 再做）

- merge、独立 review、云端抽查、打回轮次。
- 高风险独立执行审批（仍统一走整计划人工批准）。
- advisory 验收模式（仍统一 strict）。
- 控制塔位置迁移、第三方云 E2E。
- 成本预算、通知、模板、定时任务。

## 2. 架构

```text
用户 → PlanDraft(LLM/人工) → 人工批准 → 冻结 assignment
                                       ↓
                              WSS dispatch → Connector(Mac) → Zcode
                                       ↓
                              result + artifact_refs(共享存储)
                                       ↓
                              WSS dispatch → Connector(云) → 部署 Agent
                                       ↓
                              result + self_check → 验收 → 归档
```

- WSS 是唯一跨网络 Agent 控制通道，旧 UDP 不作为降级路径。
- 每个 Connector 只发起出站 TLS 443，不开放入站端口。
- 实际 CLI 参数、URL、认证头和凭据只存在于对应 Connector 本机配置。
- 跨机制品必须上传到共享存储（S3/OCI/HTTPS），本地路径不得进入其他 Connector 的 `input_refs`。
- 所有执行事件先写 Connector 本地 `sync_outbox`，塔侧确认后才标记同步。

## 3. 身份数据模型

### 3.1 Connector

| 字段 | 说明 |
| --- | --- |
| `connector_id` | 稳定 UUID |
| `name` | 例如 `mac-dev`、`cloud-deploy` |
| `credential_id` / `credential_hash` | 凭据标识与不可逆校验值 |
| `protocol_version` | 接入协议版本 |
| `online_status` / `health_status` / `load_json` / `last_seen` | 连接健康状态 |

### 3.2 AgentInstance

| 字段 | 说明 |
| --- | --- |
| `agent_id` | 稳定 UUID |
| `connector_id` | 所属 Connector |
| `local_agent_key` | Connector 内稳定本地键；与 `connector_id` 联合去重 |
| `display_name` | 人工名称 |
| `platform_id` | 人工确认的平台类型，如 `zcode`、`openclaw` |
| `adapter_type` | CLI / HTTP / OpenAI 兼容 / 原生 |
| `role_tags` / `capability_tags` / `environment_tags` | 受控标签 |
| `identity_status` | `pending` / `verified` / `disabled` |
| `verified_by` / `verified_at` / `revision` | 人工标注记录 |
| `online_status` / `health_status` / `load_json` | 实时状态，与身份分离 |

### 3.3 AgentAction

| 字段 | 说明 |
| --- | --- |
| `action_id` | 稳定动作 ID |
| `agent_id` | 所属 Agent |
| `action_key` | 如 `zcode.develop`、`openclaw.deploy` |
| `action_kind` | `execute`（V1.5 只支持 execute） |
| `adapter_type` | 本地适配器 |
| `risk_level` | V1.5 不区分，统一走整计划批准 |
| `retry_policy` | `retryable` / `non_retryable` |
| `max_attempts` | 最大尝试次数 |
| `validator_keys` | 验收验证器白名单 |
| `config_ref` / `secret_refs` | 本机配置和秘密引用，不存值 |
| `enabled` | 是否允许规划与执行 |

### 3.4 PlanRevision 与 TaskAssignment

每次批准生成不可变 `plan_revision`。冻结分派保存：

- `task_id`、`step_id`、`plan_revision`。
- `agent_id`、`action_id`、`environment`、`input_digest`。
- `approved_by`、`approved_at`。

`input_digest = SHA256(plan_revision | step_id | action_id | environment | normalized_instruction | sorted(input_ref_digests))`，计划批准时计算并冻结。`input_refs` 是上游步骤的 `result_ref` 或 artifact digest，批准时来自草案声明，执行时校验实际制品 digest 与声明一致。

### 3.5 ExecutionAttempt

每次执行保存：

- `attempt_id`、`task_id`、`step_id`、`plan_revision`、`attempt_no`。
- `idempotency_key = SHA256(task_id|step_key|plan_revision)`。
- `agent_id`、`action_id`、`environment`、`input_digest`。
- `status`（执行状态）、`acceptance_status`（`pending`/`passed`/`failed`）。
- `result_json`、`lease_expires_at`、`created_at`、`finished_at`。

### 3.6 Artifact（跨机制品）

| 字段 | 说明 |
| --- | --- |
| `id` | 稳定 ID |
| `attempt_id` | 产生该制品的 attempt |
| `uri` | 共享存储 URI（`s3://`、`oci://`、`https://`） |
| `digest` | SHA-256 内容摘要 |
| `kind` | `commit`、`image`、`test_report`、`log`、`screenshot` 等 |
| `size_bytes` | 大小 |
| `created_at` | 创建时间 |

## 4. 人工标注流程

```text
Connector 建立可信连接
  ↓
上报本地 Agent 候选和适配器事实
  ↓
控制塔创建或匹配 pending AgentInstance
  ↓
人工确认名称、平台、角色、能力、环境和动作
  ↓
状态变为 verified
  ↓
进入规划目录
```

规则：

1. `connector_id + local_agent_key` 用于重连去重。
2. 候选上报的冲突更新只允许刷新 `reported_json`、在线状态和时间；`display_name` 只能由人工接口修改。
3. 心跳只能更新健康、负载和在线状态，不能覆盖人工标注。
4. 只有 `identity_status = verified` 且 action `enabled = true` 才能进入规划和调度。
5. 禁用 Agent/action 或吊销 Connector 凭据立即阻止尚未开始的分派；执行中任务按取消处理。

## 5. 大模型规划与审批

### 5.1 PlanDraft

每个步骤包含：

```text
step_key
title
instruction
agent_id
action_key
step_kind          # V1.5 只支持 execute
depends_on         # 可跨 Agent 引用上游 step_key
expected_outputs
acceptance_criteria
input_refs         # 声明需要的上游制品类型，如 ["commit", "image"]
timeout
```

PlanDraft 不得包含 `command`、`args`、`url`、`headers`、密码、token、secret、authorization、api_key 等可执行配置字段。`instruction` 不会被当作 shell 命令执行。

`acceptance_criteria` 是结构化规则列表，每项引用该 action 已登记的 `validator_key` 和预期参数。

### 5.2 确定性校验

- JSON 结构及字段完整。
- DAG 无循环，`depends_on` 引用的 step_key 存在。
- 所有 `agent_id` 的 `identity_status = verified`。
- action 已登记且 `enabled = true`，验证器在白名单内。
- 角色、能力、环境与步骤匹配。
- `input_refs` 声明的制品类型必须能从上游步骤的 `expected_outputs` 中获得。
- 不含禁用字段。
- 步骤数、超时符合限制。

### 5.3 人工批准

UI 展示步骤依赖、Agent、action、指令、输入制品声明、验收标准、环境。批准时冻结 `plan_revision`、目录快照和 `input_digest`。

批准后任何影响执行内容的修改（Agent、action、环境、验收标准、输入）都生成新 `plan_revision` 并重新审批。

## 6. 执行与结果

### 6.1 执行消息

```text
message_id / message_type / protocol_version
connector_id / agent_id
task_id / step_id / plan_revision
action_id / action_key / attempt_id / idempotency_key
environment / input_digest
lease_expires_at / timeout
input_refs         # 上游 result_ref 与 artifact URI+digest
```

Connector 校验 Connector、Agent、action、计划修订、租约和 `input_digest`，不匹配则拒绝。

### 6.2 结构化结果

```text
status
summary
result_ref
artifact_refs      # URI + digest + kind + size
self_check
acceptance_evidence
token_usage
error
```

`status` 是执行适配器状态，不等于验收通过。控制塔另行保存 `acceptance_status`。

- 缺 `self_check` → `acceptance_status = failed`。
- 验收验证器失败 → `failed`。
- Agent 自报 `completed`、大模型摘要不能直接改写验收状态。

### 6.3 步骤依赖与制品传递

1. 上游步骤完成且验收通过后，其 `result_ref` 和 `artifact_refs` 写入 `execution_artifacts`。
2. 下游步骤调度前，调度器根据 `depends_on` 收集上游已完成的制品，按 `input_refs` 声明的类型过滤，生成 `input_refs` 注入执行消息。
3. 下游 Connector 根据 `input_refs` 中的 URI 下载制品，并校验 digest；不匹配则拒绝执行。
4. 上游失败则下游保持 pending，不调度。

## 7. 跨机制品交换

### 7.1 制品发布

- Connector 执行完成后，将大型制品（构建产物、镜像、日志、截图）上传到预先配置的共享存储（S3/OCI/HTTPS）。
- 上传完成后计算 SHA-256 digest，生成 `artifact_refs` 写入结果。
- 小型文本结果（commit SHA、测试摘要）可直接放在 `result_ref` 或 `summary`，无需上传。
- 上传失败时，attempt 标记为 `failed`，不得用本地路径替代共享 URI。

### 7.2 制品获取

- 下游 Connector 根据 `input_refs` 中的 URI 从共享存储下载。
- 下载后校验 SHA-256 digest，与声明不一致则拒绝执行并返回错误。
- 共享存储访问凭据只存在于对应 Connector 本机配置，不进入 Tower。

### 7.3 访问控制

- 共享存储使用短期凭证或预签名 URL，凭证由 Connector 本机配置生成。
- Tower 不保存共享存储密钥，只保存 URI 和 digest。
- 制品 URI 不得是本地文件路径（`/Users/...`、`C:\...`）。

## 8. 断线、重连与重试

- Connector 用带抖动的指数退避重连，沿用原 `connector_id`。
- 未开始步骤在 Agent 离线时等待，不改派。
- 执行中断线进入 `unknown`，不立即重试。
- 所有事件、结果、证据先写 `sync_outbox`，塔侧确认后才标记同步；重启后恢复未确认记录。
- 重连后先对账执行中 `attempt_id` 的真实状态。
- 每个步骤有稳定 `idempotency_key`，每次 attempt 有独立 `attempt_id`。
- `non_retryable` 状态不明转人工。
- 迟到结果按 `plan_revision + attempt_id` 核销，不能覆盖新 attempt 或已取消终态。

## 9. 运行时护栏（保留原阈值）

| 护栏 | 语义 |
| --- | --- |
| 上游断路器 | 同一 Connector×适配器×模型连续 5 次 5xx/超时熔断 60s，半开探测；不改派 |
| 空闲下岗 | Worker 连续 10 分钟无 token 且无产物更新，二次确认后销毁并按原分派重试 |
| 唤醒环检测 | 同一步骤 30 分钟内至少 3 次无新产物唤醒，进入 `frozen` 并创建审批 |
| 有界上下文 | Worker 启动前预检 token 预算，超限收尾 |

## 10. 安全边界

- 每个 Connector 使用独立凭据，支持轮换和吊销。
- 全链路 TLS，Connector 校验服务端证书；不开放 Agent 入站端口。
- 控制塔不保存 Connector 明文秘密，日志不记录认证头。
- 平台标注、计划批准写审计。
- 大模型输出和 Agent 返回文本视为不可信数据。
- 规划器只能通过 action 白名单表达执行意图。
- Connector 只执行签发给本机、原 Agent、原 action 的有效任务。
- 跨机制品必须通过共享存储交换，digest 校验失败不得执行。

## 11. 测试与验收

### 11.1 单元与集成测试

| 场景 | 必须结果 |
| --- | --- |
| 候选重连 | 不覆盖人工 `display_name`、平台、标签、revision |
| 心跳 | 只更新健康/负载/在线状态 |
| 未 verified Agent | 不进入规划和调度 |
| 禁用 Agent/action 或吊销 Connector | 未开始分派立即停止 |
| PlanDraft 含 `command`/`args`/`headers`/凭据 | 校验拒绝 |
| 计划批准后修改 Agent 或环境 | 必须新建修订并重新审批 |
| Agent 离线 | 任务保持 pending，不改派 |
| 缺 `self_check` | `acceptance_status = failed`，不归档 |
| 验收验证器失败 | 不归档 |
| 重连后执行中 attempt | 先对账，不重复执行 |
| 已取消 attempt 收到迟到结果 | 状态仍为 cancelled |
| 本地路径作为 artifact URI | 校验拒绝 |
| artifact digest 不匹配 | 下游拒绝执行 |
| 上游步骤失败 | 下游不调度 |

### 11.2 真实契约 E2E（Mac + 云服务器）

```text
人工标注 Mac Zcode 和云部署 Agent
  ↓
用户提交：开发并部署到 staging
  ↓
LLM 生成双步骤 PlanDraft（develop → deploy）
  ↓
人工批准
  ↓
Zcode 执行，输出 commit SHA 和构建制品到共享存储
  ↓
云 Agent 下载制品，部署到 staging
  ↓
控制塔验收通过 → 归档为 done
```

必须满足：

- 两个 Agent 的 `agent_id` 不变。
- Agent 不修改本机接入配置。
- 跨机制品通过共享存储交换，digest 校验通过。
- 上游失败时下游不执行。
- 验收失败不能归档成功。
- 全链路分派、执行、重试、审批和结果可审计。
- 必须使用管理员提供的真实本机 action 配置，mock 通过不算通过。

## 12. V2 清单（明确非 V1.5）

- merge、独立 review、云端抽查、打回轮次。
- 高风险独立执行审批。
- advisory 验收模式。
- 控制塔位置迁移与第三方云 E2E。
- 成本预算、通知、模板、定时任务。
