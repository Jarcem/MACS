# 统一 Agent 平台协议 · V3.0 设计变更总结

## 📋 变更概览

本次更新为 MACS 系统新增**全链路统一通信协议**（§6.4.13），使 MACS 能够与所有 OpenClaw 同类 Agent 平台实现跨平台互操作。核心目标是支持 AutoGen、LangChain Agents、CrewAI、LlamaIndex 等主流平台的无缝接入，通过标准化协议层屏蔽各框架异构实现差异。

---

## 🎯 设计目标

1. **全链路覆盖**：从任务下发、执行、上报到质量保障的完整生命周期统一协议格式
2. **平台无关性**：不依赖特定 Agent 框架，通过适配器层实现统一接入
3. **能力抽象**：将异构能力映射为标准化的任务执行、状态上报、质量反馈等原子操作
4. **双向兼容**：既有平台可作为 Adapter 接入 MACS，MACS 也可调度这些平台上的 Agent

---

## 📦 核心变更清单

### 一、数据库 Schema 变更（§2.10a）

新增 4 张统一协议相关表：

#### 1. `unified_agent_platforms` - 统一 Agent 平台注册表
```sql
CREATE TABLE unified_agent_platforms (
    id              UUID PRIMARY KEY,
    platform_name   VARCHAR(128) NOT NULL,      -- auto-gen / langchain-agents / crew-ai
    vendor          VARCHAR(64),                -- microsoft / huggingface / crewai
    version         VARCHAR(32) NOT NULL DEFAULT '1.0',
    protocol_spec   VARCHAR(64) NOT NULL,       -- 协议规范文档链接
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    is_system       BOOLEAN NOT NULL DEFAULT FALSE,
    base_url        VARCHAR(512),               -- API 网关地址（可选）
    default_driver  VARCHAR(64) NOT NULL,       -- 默认协议驱动
    capabilities    JSONB NOT NULL DEFAULT '[]',
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**用途**：记录所有兼容平台的元数据，支持平台版本管理和能力查询。

#### 2. `protocol_drivers` - 协议驱动注册表
```sql
CREATE TABLE protocol_drivers (
    id              UUID PRIMARY KEY,
    driver_name     VARCHAR(64) NOT NULL,       -- openai-compat-protocol / anthropic-msg-protocol
    version         VARCHAR(32) NOT NULL DEFAULT '1.0',
    description     TEXT,                       -- 协议说明文档链接
    protocol_family VARCHAR(32),                -- openai-compatible / anthropic / cli-native
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    spec_url        VARCHAR(512) NOT NULL,      -- 规范文档 URL
    message_schema  JSONB,                      -- 消息信封 Schema（JSON Schema）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 唯一约束：同一驱动名称+版本只能有一个活跃实例
CREATE UNIQUE INDEX idx_protocol_drivers_name_version 
ON protocol_drivers(driver_name, version) WHERE is_active = TRUE;
```

**用途**：定义标准化的协议实现，如 `openai-compat-protocol@1.0`、`anthropic-msg-protocol@2023-06-01`。

#### 3. `platform_adapter_mappings` - 平台 - 协议适配器映射表
```sql
CREATE TABLE platform_adapter_mappings (
    id              BIGSERIAL PRIMARY KEY,
    platform_id     UUID NOT NULL REFERENCES unified_agent_platforms(id) ON DELETE CASCADE,
    driver_id       UUID NOT NULL REFERENCES protocol_drivers(id) ON DELETE RESTRICT,
    adapter_name    VARCHAR(128) NOT NULL,      -- auto-gen-to-openai-compat
    status          VARCHAR(32) NOT NULL DEFAULT 'developing',  -- developing / ready / deprecated
    compatibility_mode VARCHAR(32) DEFAULT 'strict',   -- strict / lenient / compatible
    supported_features JSONB DEFAULT '{}',            -- 支持的功能列表
    config_schema   JSONB,                           -- 适配器配置 Schema
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(platform_id, driver_id)
);

CREATE INDEX idx_platform_adapter_mappings_platform ON platform_adapter_mappings(platform_id);
CREATE INDEX idx_platform_adapter_mappings_driver ON platform_adapter_mappings(driver_id);
```

**用途**：定义各平台与标准化协议的适配关系，如 `auto-gen-to-openai-compat`（status: ready）。

#### 4. `telemetry_events` - 细粒度遥测事件表（云端）
```sql
CREATE TABLE telemetry_events (
    id              BIGSERIAL PRIMARY KEY,
    task_id         UUID REFERENCES tasks(id),
    subtask_id      UUID REFERENCES subtasks(id),
    worker_id       VARCHAR(255),
    platform_adapter VARCHAR(128),              -- langchain-to-openai-compat
    agent_platform  VARCHAR(64),                -- Agent 平台名称
    event_type      VARCHAR(32) NOT NULL,       -- task_start / step_progress / token_usage
    payload         JSONB NOT NULL DEFAULT '{}',
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_telemetry_events_task ON telemetry_events(task_id, timestamp DESC);
```

**用途**：存储细粒度遥测数据（token 使用、执行耗时、进度等），支持成本核算与性能分析。

---

### 二、详细设计文档变更（§6.4.13）

在 `MACS 技术设计文档.md` 中新增 **§6.4.13 统一 Agent 平台协议**，包含以下子章节：

#### 6.4.13.1 协议驱动注册与能力映射
- 数据库设计说明（对应上述 4 张表）
- 平台元数据管理流程
- 适配器状态流转机制

#### 6.4.13.2 UDP 消息信封扩展（全链路统一格式）
```json
{
  "msg_id": "uuid-v4",
  "type": "dispatch | report | telemetry | event | ack",
  "version": "3.0",                              // V3.0+ 支持统一协议
  "protocol_driver": "openai-compat-protocol@1.0", 
  "platform_adapter": "auto-gen-to-openai-compat", 
  "timestamp": "2026-11-08T07:00:00.050Z",
  "seq": 1025,
  "payload": {
    "task_execution_id": "uuid-v4",              // 跨平台唯一任务执行 ID
    "agent_platform": "langchain-agents",        // 使用的 Agent 平台
    "adapter_config": {},                        // 适配器配置
    "telemetry_metrics": {                       // §6.4.13.4
      "start_time_ms": 100,
      "end_time_ms": 5000,
      "token_usage": {"prompt": 1000, "completion": 500},
      "cost_cents": 50
    }
  }
}
```

**新增消息类型**：
- `execution_result` - Worker 执行结果上报（上行）
- `telemetry_stream` - 实时遥测指标推送（UDP + SSE）
- `platform_event` - 平台原生事件上报（如 AutoGen 的 message_published）
- `capability_query` - 查询孵化器支持的 Agent 平台能力
- `adapter_config_update` - 适配器配置热更新

#### 6.4.13.3 全链路消息流转示例
提供了从云端创建任务到最终结果上报的完整协议交互流程，包括：
1️⃣ 云端 REST API 创建任务（指定 agent_platform）  
2️⃣ UDP dispatch 下发调度  
3️⃣ 孵化器转发至 LangChain Agent（内部 gRPC）  
4️⃣ Agent 执行中实时遥测推送（UDP + SSE）  
5️⃣ Agent 执行完成，结果上报  
6️⃣ 云端接收并更新状态

#### 6.4.13.4 统一遥测监控指标体系
| 指标 | 维度 | 说明 | 用途 |
|------|------|------|------|
| **token_usage** | task/subtask/worker | prompt / completion tokens（含缓存 token） | 成本核算、预算控制 |
| **execution_duration_ms** | task/subtask/agent_call | 从 dispatch 到 result 的总耗时 | 性能分析、SLA 监控 |
| **agent_steps_count** | subtask | Agent 执行过程中的步骤数（如 LangChain chain 节点） | 质量评估 |
| **adapter_latency_ms** | dispatch→execution / execution→report | 适配器转发延迟 | 瓶颈识别 |
| **cost_cents** | task/subtask/worker | 实际消耗的货币成本 | 预算控制、对账 |
| **platform_version** | task | 使用的 Agent 平台版本（auto-gen@0.2.1） | 兼容性管理 |

#### 6.4.13.5 平台适配器开发规范（契约）
定义了所有接入 MACS 的 Agent 平台需实现的标准接口：

| 方法 | 签名 | 说明 |
|------|------|------|
| `init(config)` | `(adapter_config: JSON) → void` | 初始化适配器，加载配置 |
| `dispatch_task(task_payload)` | `({ subtask_id, task_def }) → TaskExecutionId` | 接收调度并启动任务执行 |
| `report_execution_result(execution_id, result)` | `(uuid, { output, metadata }) → void` | 上报执行结果（含遥测） |
| `stream_telemetry(metrics_stream)` | `(Stream<TelemetryEvent>) → void` | 推送实时遥测指标 |
| `get_capabilities()` | `() → PlatformCapabilities` | 返回平台能力清单 |

**PlatformCapabilities 接口定义**：
```json
{
  "platform": "langchain-agents",
  "version": "0.1.0",
  "supported_features": ["executor", "reviewer", "planner"],
  "protocol_drivers": ["openai-compat-protocol@1.0"],
  "max_concurrent_tasks": 8,
  "default_timeout_sec": 3600,
  "quality_capabilities": {
    "self_check_supported": true,
    "review_supported": true,
    "spot_check_supported": true
  }
}
```

#### 6.4.13.6 现有协议驱动扩展（V2.0 → V3.0）
- **OpenAI Compatible**：支持所有基于 OpenAI Chat Completions API 的平台（AutoGen、LangChain、CrewAI 等），通过 `openai-compat-protocol` 驱动统一接入
- **Anthropic**：支持基于 Claude API 的平台，通过 `anthropic-msg-protocol` 驱动
- **CLI Native**：支持 CLI 型 Agent 适配器（如 claude-cli、llama-cli）

能力协商规则扩展：
- 孵化器注册时携带支持的协议驱动列表
- 任务调度时指定 `required_protocol_driver`，孵化器自动匹配适配该驱动的 Agent 平台
- 若本机无满足协议要求的 Agent 平台，孵化器回执 `driver_mismatch`（不记 ACK 失败）

---

### 三、模块交付清单变更（§七）

新增 3 个 V3.0 核心模块：

| # | 模块 | 仓库 / 路径 | 改动类型 | 说明 |
|---|------|-----------|---------|------|
| **28** | **统一 Agent 平台协议层** | `macs-tower/src/modules/unified-protocol` | **新增 V3.0** | 协议驱动注册表 + 能力映射 + 消息编解码扩展 |
| **29** | **跨平台适配器加载器** | `macs-incubator/src/adapters/loader` | **新增 V3.0** | 插件化适配器加载 + AutoGen/LangChain/CrewAI 等原生协议转发 |
| **30** | **遥测监控服务** | `macs-tower/src/modules/telemetry` | **新增 V3.0** | 细粒度指标采集 + SSE 流推送 + 成本核算增强 |

---

### 四、数据库表清单变更（§八）

**云端核心表扩展**：从 13 张 → **16 张**
- 新增：`unified_agent_platforms` / `protocol_drivers` / `platform_adapter_mappings` / `telemetry_events`

**本地核心表扩展**：从 14 张 → **17 张**  
- 新增：`telemetry_events_local`（本地遥测事件，高写入日志）
- 已有：`guardrail_events`（护栏事件，已存在）

---

### 五、架构演进图

```
┌─────────────────────────────────────────────────────┐
│                   MACS 统一协议层                    │
│  - UDP 消息信封 v3.0                                 │
│  - REST API Schema                                  │
│  - SSE Stream Protocol                              │
└─────────────────────┬───────────────────────────────┘
                      │
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
┌──────────────┐ ┌──────────────┐ ┌──────────────┐
│  AutoGen    │ │ LangChain   │ │ CrewAI      │
│ Adapter     │ │ Adapter     │ │ Adapter     │
│ (JSON-RPC)  │ │ (gRPC)      │ │ (REST)      │
└──────────────┘ └──────────────┘ └──────────────┘
        ▲             ▲             ▲
        │             │             │
┌─────────────────────────────────────────────────────┐
│                    Agent 平台层                      │
│  - AutoGen / LangChain Agents / CrewAI / LlamaIndex等 │
└─────────────────────────────────────────────────────┘
```

---

## 📊 影响范围评估

### 后端服务（macs-tower）
- ✅ 新增 `unified-protocol` 模块：协议驱动注册、能力映射查询、消息编解码
- ✅ 新增 `telemetry` 模块：遥测事件采集与 SSE 流推送
- ⚠️ 需扩展 REST API：任务创建支持 `agent_platform` / `adapter_config` 字段

### 孵化器服务（macs-incubator）
- ✅ 新增适配器加载器：动态发现并加载各平台插件
- ✅ UDP 消息处理逻辑扩展：解析 v3.0 协议字段，转发至对应 Adapter
- ⚠️ 需实现 SSE Server：支持实时遥测指标推送

### 管理后台（macs-tower-web）
- 📋 待新增 UI 组件：统一平台列表展示、适配器状态监控、遥测数据可视化

---

## 🔧 兼容性说明

1. **向后兼容**：UDP 消息中未识别的 `version` 字段直接忽略，新旧版本可混部
2. **数据库兼容**：新表使用 JSONB 存储扩展字段，不影响现有业务逻辑
3. **API 兼容**：管理后台 REST API 保留版本号（/api/v1/），UDP 消息含 version 字段

---

## 📅 时间线规划

| 阶段 | 任务 | 负责人 | 预计完成 |
|------|------|--------|----------|
| Phase 1 | 实现协议驱动注册表与能力映射 API | Agent-A | Week 1 |
| Phase 2 | 开发 OpenAI Compatible / Anthropic 两个核心协议驱动 | Agent-B | Week 1-2 |
| Phase 3 | 实现 AutoGen/LangChain/CrewAI三个原生适配器插件 | Agent-C | Week 2-4 |
| Phase 4 | 开发遥测监控服务 + SSE 流推送 | Agent-D | Week 3-4 |
| Phase 5 | 管理后台 UI：平台列表/状态监控/遥测数据可视化 | Frontend-A | Week 4-6 |

---

## 📝 后续迭代规划

**V3.1 待办事项**（建议）：
- [ ] 增加更多协议驱动：LlamaIndex Protocol、CrewAI Native Protocol
- [ ] 适配器热更新机制：支持运行时动态下发新 Adapter 配置
- [ ] 跨平台任务迁移：同一任务在不同 Agent 平台间无缝切换执行

---

## 📚 相关文档

- [MACS 技术设计文档 V3.0](./MACS 技术设计文档.md) — 完整技术设计方案
- [数据库存储设计 V2.0](./数据库存储设计.md) — 数据库 Schema 详情

---

**文档版本**：V3.0  
**更新日期**：2026-11-08  
**维护人**：Team Lead  
