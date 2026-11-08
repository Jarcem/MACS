<p align="center">
  <a href="README.md"><strong>English</strong></a> &middot;
  <strong>简体中文</strong>
</p>

<p align="center">
  <a href="#quickstart"><strong>Quickstart</strong></a> &middot;
  <a href="design/MACS技术设计文档.md"><strong>技术设计</strong></a> &middot;
  <a href="design/UI交互与运维监控设计.md"><strong>UI 设计</strong></a> &middot;
  <a href="design/数据库存储设计.md"><strong>数据库设计</strong></a> &middot;
  <a href="https://github.com/Jarcem/MACS"><strong>GitHub</strong></a>
</p>

<br/>

# MACS — Multi-Agent Cooperative System

开源的「多 Agent 协作执行系统」：一个调度塔，一群随时孵化、用完即毁的 Worker。

**分散的 Agent 终端。集中的调度塔。**

MACS 由 Go 编写的云端调度塔（`macs-tower`）与运行在各服务器上的孵化器（`macs-incubator`）组成，把一句目标拆成可并行的子任务，分发给不同机器上的 Worker 执行，并把过程与结果收敛到一块深色 HUD 监控屏上。

它看起来像一个任务看板。底层是：UDP 信封协议、DAG 依赖调度、槽位负载均衡、断网缓冲、命令签名审计。

**任意机器入驻。全局任务收敛。**

|        | 步骤           | 示例                                                                 |
| ------ | -------------- | -------------------------------------------------------------------- |
| **01** | 定义目标       | _"分析这段文本的中文与英文分布，给出两个片段的统计结果。"_           |
| **02** | 孵化节点       | 每台机器跑一个孵化器进程，注册即上线，按空槽自动接单。               |
| **03** | 提交并观察     | 目标进塔 → 拆解 → 并行下发 → Worker 回传 → 看板进度 100%。           |

<br/>

<div align="center">
<table>
  <tr>
    <td align="center"><strong>技术栈</strong></td>
    <td align="center"><sub>Go 1.22</sub></td>
    <td align="center"><sub>UDP V3.5</sub></td>
    <td align="center"><sub>SQLite（纯 Go）</sub></td>
    <td align="center"><sub>React + Vite</sub></td>
    <td align="center"><sub>Ink TUI</sub></td>
    <td align="center"><sub>HMAC-SHA256</sub></td>
  </tr>
</table>

<em>只要能收到 dispatch，就能接活。</em>

</div>

<br/>

## MACS 适合你，如果你

- ✅ 手上有 **多台机器、多个 Agent 终端**，但没人知道彼此在干什么
- ✅ 想把一个目标 **拆成并行的子任务**，而不是排成一条长队
- ✅ 需要 **跨机器分配负载**，而不是把活都堆在一台机器上
- ✅ 想要 **过程可观测**：谁接了、跑到哪、回了什么、耗时多少
- ✅ 需要 **受控的命令通道**，不想在每台服务器上装第三方客户端
- ✅ 想要 **断网不丢结果**，恢复后自动补传

<br/>

## Features

<table>
<tr>
<td align="center" width="33%">
<h3>📡 UDP 信封协议</h3>
统一的 V3.5 信封：<code>msg_id / type / version / in_reply_to / payload</code>，注册、下发、回传、心跳一套协议走通。
</td>
<td align="center" width="33%">
<h3>🌱 孵化器模型</h3>
Worker 按需孵化、用完即毁。并发受本机 <code>max_workers</code> 槽位限制，不常驻、不空转。
</td>
<td align="center" width="33%">
<h3>🧩 目标拆解 / DAG</h3>
目标 → 子任务 → 依赖边。<code>depends_on</code> 满足才就绪，可并行的并行，需串行的等待。
</td>
</tr>
<tr>
<td align="center">
<h3>⚖️ 槽位负载均衡</h3>
按「空槽最多」选机：<code>capacity - running</code>，多台孵化器自然分活，不靠人工点名。
</td>
<td align="center">
<h3>🔁 ACK + 重传</h3>
下发必有 ACK，3s 超时、指数退避重传 ≤3 次，下发全过程落 <code>dispatch_log</code>。
</td>
<td align="center">
<h3>🛡️ 幂等去重</h3>
<code>msg_id</code> 10 分钟窗口去重，重放不重复执行；撞号直接告警丢弃，不再静默。
</td>
</tr>
<tr>
<td align="center">
<h3>💾 断网缓冲</h3>
回传失败先落本地 SQLite，Reporter 周期补传，网络恢复后结果不丢。
</td>
<td align="center">
<h3>🖥️ 深色 HUD</h3>
单页监控：KPI、任务编排、集群状态、拓扑、调度记录；SSE 事件驱动刷新（含历史回放）+ 轮询兜底。
</td>
<td align="center">
<h3>⌨️ 终端监控</h3>
Ink TUI 只读终端，内容对齐 Web 看板，适合放在透明显示设备或 SSH 会话里。
</td>
</tr>
<tr>
<td align="center">
<h3>🔐 受控命令终端</h3>
<code>macs-cmdterm</code> 仅接受塔签发的 <code>cmd_exec</code>：HMAC-SHA256 + 60s 时间窗 + 命令白名单。
</td>
<td align="center">
<h3>🧾 全量审计</h3>
注册、下发、ACK、执行结果、命令执行逐条落库/落 JSONL，可回溯每一次决策。
</td>
<td align="center">
<h3>🧠 自带 Agent</h3>
Worker 执行单元可替换：当前是真实文本分析切片，后续接 CLI / API / 协议适配器。
</td>
</tr>
</table>

<br/>

## MACS 解决的问题

| 没有 MACS                                                                                                       | 有 MACS                                                                                                     |
| --------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| ❌ 几台机器上开着十几个 Agent 终端，谁在做什么全靠记忆。                                                          | ✅ 目标进塔即拆解，子任务、执行节点、Worker、结果全部落库可见。                                              |
| ❌ 任务只能串行，一台机器忙死、其他机器闲着。                                                                     | ✅ 按空槽选机，同一目标的子任务可并行落到不同孵化器。                                                          |
| ❌ UDP 丢包、ACK 丢失时，任务永远卡在「已下发」，没人知道发生了什么。                                              | ✅ ACK 有超时与重传，失败落库并标失败；重复结果不会被执行两遍。                                                |
| ❌ 网络一断，Worker 的结果就没了。                                                                                | ✅ 回传失败进本地缓冲，恢复后由 Reporter 自动补传。                                                            |
| ❌ 想远程执行一条命令，只能装 SSH 客户端或第三方 Agent，权限收不住。                                               | ✅ 命令终端只认塔签发的签名指令，白名单 + 审计，不开放任意入口。                                                |
| ❌ 想做多机协作，先花两周自己写注册、心跳、调度、重试、去重。                                                       | ✅ 这些都在塔里了，接入只需在机器上跑一个孵化器进程。                                                          |

<br/>

## 为什么 MACS 特别

MACS 把分布式编排里最容易写错的地方做对了。

|                                   |                                                                                                        |
| --------------------------------- | ------------------------------------------------------------------------------------------------------ |
| **先登记再发包。**                | ACK 可能比登记更早到达（本机/低延迟），顺序反了就是必然假超时 → 重试 → 重复执行。                      |
| **唯一消息 ID。**                 | `msg_id` 用随机 16 字节，而不是时间戳；时间戳在同时刻连发时会撞号，被去重窗口静默吞掉。                  |
| **单写连接串行化。**              | SQLite 用 `_pragma=busy_timeout` + `SetMaxOpenConns(1)`，并发写不再 `SQLITE_BUSY`。                      |
| **一遍扫描的调度循环。**          | `kick` 通道去抖，落库即唤醒，按依赖满足 + 空槽挑机，claim 有原子性（只抢 pending）。                      |
| **进度可推导。**                  | 任务进度由子任务状态聚合计算，不靠 Worker 自己报百分比；失败即刻反映到父任务。                          |
| **断网 fail-open。**              | 回传失败不阻塞执行，先缓冲再补传；补传失败继续留在本地，不丢数据。                                      |
| **零 CGO 依赖。**                 | `modernc.org/sqlite` 纯 Go 实现，交叉编译与部署不需要 C 工具链。                                        |

<br/>

## 架构

```
                    ┌──────────────────────────────────────────────┐
                    │              macs-tower  调度塔              │
                    │                                              │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
                    │  │ UDP      │  │ 广播调度  │  │ 集群管理  │    │
                    │  │ Gateway  │  │ Dispatcher│ │ Cluster  │    │
                    │  └──────────┘  └──────────┘  └──────────┘    │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
                    │  │ 任务编排  │  │ 任务/子任务│ │ 事件总线  │    │
                    │  │Orchestr. │  │ Store    │  │ Events   │    │
                    │  └──────────┘  └──────────┘  └──────────┘    │
                    └───────▲──────────────▲──────────────┬───────┘
             register / │              │ dispatch      │ REST / SSE
             report /   │              │ (UDP 单播)    │ :8471
          execution_result            │               ▼
                    ┌───────┴──────┐   │      ┌────────────────────┐
                    │macs-incubator│◄─┘      │   macs-tower-web   │
                    │   孵化器      │          │  深色 HUD 单页      │
                    │              │          └────────────────────┘
                    │ ┌──────────┐ │          ┌────────────────────┐
                    │ │ spawner  │ │          │ macs-dsh-monitor   │
                    │ │ worker   │ │          │ Ink TUI（只读）     │
                    │ └──────────┘ │          └────────────────────┘
                    │ ┌──────────┐ │          ┌────────────────────┐
                    │ │ SQLite   │ │          │   macs-cmdterm     │
                    │ │ 离线缓冲  │ │          │ 受控命令终端 :8472  │
                    │ └──────────┘ │          └────────────────────┘
                    └──────────────┘
```

端口口径（设计文档附录 A · Q9）：

| 组件                | 端口        | 协议             |
| ------------------- | ----------- | ---------------- |
| `macs-tower`        | 8470        | UDP 信封协议     |
| `macs-tower`        | 8471        | HTTP REST / SSE  |
| `macs-cmdterm`      | 8472        | UDP 命令通道     |
| `macs-incubator`    | 8473 / 8474 | UDP（每实例独立）|
| `macs-tower-web`    | 5173        | Vite 开发服务器  |

### 子系统

<table>
<tr>
<td width="50%">

**UDP 接入网关** — 256KB 上限、按 `type` 路由、`msg_id` 10 分钟幂等去重；ACK 不再回 ACK，避免乒乓风暴。

</td>
<td width="50%">

**广播调度器** — 指定目标单播下发、等待 ACK（3s）、指数退避重试、终态写 `dispatch_log`；先登记 pending 再发包。

</td>
</tr>
<tr>
<td>

**任务编排器** — 目标落库 → 拆解子任务 → 扫描依赖已满足的 pending → 按空槽选机 → claim → 下发；收回传后聚合进度。

</td>
<td>

**集群管理** — 孵化器注册表、心跳与 stale 巡检、资源画像、在线状态与容量（`max_workers`）。

</td>
</tr>
<tr>
<td>

**孵化器** — 注册取 `incubator_id`、监听 dispatch、回 ACK、按并发槽执行 Worker、上行 `execution_result`、周期 `report`。

</td>
<td>

**Worker 执行单元** — 统一 `Job{Mode, Shard, Text} → Outcome`；当前实现 `text_analysis`（字符/有效字符/去重/高频字符统计），后续替换为真实 Agent。

</td>
</tr>
<tr>
<td>

**本地存储** — SQLite（WAL + busy_timeout），任务、子任务、调度日志、孵化器、离线缓冲；云端预留 PostgreSQL。

</td>
<td>

**事件总线** — 进程内广播 `task_created / subtask_dispatched / subtask_finished / task_finished`，非阻塞丢帧，SSE 30s 心跳。

</td>
</tr>
<tr>
<td>

**管理后台** — React 18 + Vite 单页 HUD：KPI、任务编排表单、任务卡片与子任务表、集群状态、拓扑、调度记录。

</td>
<td>

**受控命令终端** — 注册 + 心跳；仅执行签名 `cmd_exec`：ConstantTimeCompare、nonce/时间窗防重放、命令白名单、shell 元字符拒绝。

</td>
</tr>
</table>

<br/>

## MACS 不是什么

|                              |                                                                                          |
| ---------------------------- | ---------------------------------------------------------------------------------------- |
| **不是 Agent 框架。**        | 不规定你怎么写 Agent，只规定它怎么被调度、被观测、被审计。                                 |
| **不是工作流编辑器。**        | 没有拖拽画布。任务 = 目标 + 子任务 DAG，用数据表达，不靠画。                                |
| **不是消息队列。**            | UDP + ACK + 去重是为了低延迟内网调度，不是做通用 MQ。                                       |
| **不是单机脚本。**            | 单机可以跑，但它的价值在于多台机器一起接活。                                                |
| **不是 SSH 替代品的全部。**   | `macs-cmdterm` 只做受控命令通道：签名、白名单、审计，不提供交互式 shell。                   |

<br/>

## Quickstart

开源、自托管、无需账号。

```powershell
# 1) 构建两个 Go 二进制（Go 1.22+）
cd macs-tower
go build -o ..\bin\tower.exe ./cmd/tower
cd ..\macs-incubator
go build -o ..\bin\incubator.exe ./cmd/incubator

# 2) 启动调度塔（UDP :8470 / HTTP :8471）
cd ..\macs-tower
$env:MACS_MIGRATIONS_DIR = 'migrations'
..\bin\tower.exe

# 3) 启动两台孵化器（单机双 Worker 垂直切片）
$env:MACS_INCUBATOR_PORT='8473'; $env:MACS_INCUBATOR_NAME='worker-a'; ..\bin\incubator.exe
$env:MACS_INCUBATOR_PORT='8474'; $env:MACS_INCUBATOR_NAME='worker-b'; ..\bin\incubator.exe

# 4) 启动监控看板
cd ..\macs-tower-web
npm install
npm run dev      # http://localhost:5173

# 5) 提交第一个目标
curl -X POST http://127.0.0.1:8471/api/v1/tasks `
  -H "Content-Type: application/json; charset=utf-8" `
  -d '{"goal":"分析这段文本的中文字符与英文单词分布，给出两个片段的统计结果"}'
```

> Bash 等价写法：把 `$env:VAR='v'` 换成 `VAR=v ./bin/xxx` 即可。

一次成功的目标会经历：

```
POST /api/v1/tasks  →  goal 按字符对半切为子任务 A / B（可并行）
                    →  按空槽选机：A → worker-a:8473，B → worker-b:8474
                    →  每台回 ACK → 孵化 Worker → 真实统计
                    →  execution_result 上行 → 进度 50% → 100% → completed
```

在 `http://localhost:5173` 上可以看到：KPI（在线孵化器 x/y、进行中、任务总数、调度记录）、任务卡片与进度条、子任务表（状态 / 执行节点 / worker / 耗时 / 结果）、集群状态与拓扑、调度记录。

### 配置项

`macs-tower`

| 变量                    | 默认                | 说明                                        |
| ----------------------- | ------------------- | ------------------------------------------- |
| `MACS_TOWER_UDP_PORT`   | `8470`              | UDP 信封协议端口                            |
| `MACS_TOWER_HTTP_PORT`  | `8471`              | HTTP 管理端口                               |
| `MACS_TOWER_DB`         | `macs-tower.db`     | SQLite 文件路径                             |
| `MACS_MIGRATIONS_DIR`   | `migrations`        | SQL 迁移目录                                |
| `MACS_REGISTER_TOKEN`   | 空（开发模式）      | 孵化器注册令牌                              |
| `MACS_ADMIN_TOKEN`      | 空                  | `/api/v1` 管理令牌（预留）                  |
| `DATABASE_URL`          | 空（用 SQLite）     | 预留 PostgreSQL（见 `docker-compose.yml`）  |

`macs-incubator`

| 变量                        | 默认                | 说明                          |
| --------------------------- | ------------------- | ----------------------------- |
| `MACS_TOWER_HOST`           | `127.0.0.1`         | 调度塔地址                    |
| `MACS_TOWER_PORT`           | `8470`              | 调度塔 UDP 端口               |
| `MACS_INCUBATOR_PORT`       | `8473`              | 本实例监听端口                |
| `MACS_INCUBATOR_NAME`       | `dev-incubator`     | 实例名（worker_id 前缀）      |
| `MACS_MAX_WORKERS`          | `4`                 | 本机并发槽                    |
| `MACS_INCUBATOR_DB`         | `incubator.db`      | 离线缓冲 SQLite 文件          |
| `MACS_INCUBATOR_TOKEN`      | 空                  | 注册令牌                      |

`macs-cmdterm`

| 变量                    | 默认                                                      | 说明                                  |
| ----------------------- | --------------------------------------------------------- | ------------------------------------- |
| `MACS_CMDTERM_PORT`     | `8472`                                                    | 命令通道端口                          |
| `MACS_CMDTERM_SECRET`   | 空（拒绝一切指令）                                        | 与塔共享的 HMAC-SHA256 密钥           |
| `MACS_CMDTERM_ALLOW`    | `systeminfo,ipconfig,dir,type,hostname,whoami`            | 命令白名单                            |

<br/>

## HTTP API

| 方法   | 路径                            | 说明                                  |
| ------ | ------------------------------- | ------------------------------------- |
| GET    | `/healthz`                      | 健康检查                              |
| POST   | `/api/v1/tasks`                 | 提交目标 `{"goal": "..."}`            |
| GET    | `/api/v1/tasks`                 | 任务列表（内嵌 subtasks）             |
| GET    | `/api/v1/tasks/{id}/subtasks`   | 指定任务的子任务                      |
| GET    | `/api/v1/incubators`            | 孵化器在线状态                        |
| GET    | `/api/v1/dispatch-log`          | 调度记录（ACK 状态、应答时间）        |
| GET    | `/api/v1/agent-platforms`       | Agent 平台清单                        |
| GET    | `/api/v1/platform-adapters`     | 平台适配器清单                        |
| GET    | `/api/v1/telemetry/events`      | 遥测事件（最近 N 条）                 |
| GET    | `/api/v1/telemetry/stream`      | SSE 实时事件流                        |
| POST   | `/api/v1/dispatch`              | 手工下发（调试用）                    |

<br/>

## FAQ

**一个孵化器能跑几个 Worker？**
受 `MACS_MAX_WORKERS` 限制（默认 4）。塔按 `capacity - running` 计算空槽，槽位满就不再往这台派活。

**为什么用 UDP 而不是 TCP/HTTP？**
内网调度讲究低延迟、无连接、天然适合广播与多机入驻。可靠性由 ACK + 重传 + 幂等去重补上，而不是靠传输层。

**子任务怎么拆？**
当前垂直切片是「目标按字符对半切」的真实并行演示；`decompose()` 是唯一改动点，接入 LLM 拆解后即可替换为语义级 DAG。

**结果会不会重复执行？**
不会。`msg_id` 10 分钟窗口去重，重复回传直接丢弃并告警；下发侧先登记 pending 再发包，避免 ACK 先到导致的假超时重试。

**断网了会怎样？**
执行照常进行，`execution_result` 落本地 SQLite 缓冲；Reporter 每 60s 尝试补传，成功即标记 sync。

**能不能只用 Web 看板，不装其他东西？**
可以。看板只依赖塔的 `/api/v1`；需要终端形态时再用 `macs-dsh-monitor`（Ink TUI，只读）。

<br/>

## 目录结构

```
macs/
├─ macs-tower/          调度塔（Go）：UDP 网关 / 调度 / 编排 / 集群 / 存储 / 事件总线
│  ├─ cmd/tower/        入口（UDP :8470 + HTTP :8471）
│  ├─ internal/         gateway dispatcher orchestrator cluster store events udp
│  └─ migrations/       0001_init.sql / 0002_tasks.sql
├─ macs-incubator/      孵化器（Go）：注册 / 监听 / 并发槽 Worker / 回传 / 离线缓冲
│  ├─ cmd/incubator/    入口（UDP :8473）
│  ├─ internal/         worker probe store
│  └─ src/db/migrations/ 本地 SQLite schema
├─ macs-cmdterm/        受控命令终端（Go，:8472）：HMAC 签名 / 白名单 / 审计
├─ macs-tower-web/      管理后台（React + Vite）：深色 HUD 单页
├─ macs-dsh-monitor/    DSH 监控终端（Ink TUI，只读）
├─ design/              技术设计 / UI 设计 / 数据库设计 / 协议变更说明
├─ architecture/        云 Agent 生态架构图
├─ bin/                 构建产物（不入库）
└─ docker-compose.yml   本地 PostgreSQL（可选）
```

<br/>

## Roadmap

- ✅ **M1 核心通信链路**：孵化器注册 + UDP 收发 + 单播调度 + ACK + 集群管理 + 心跳
- ✅ **M2 单 Worker 执行（垂直切片）**：任务表 + 子任务 DAG + 目标拆解 + 真实 Worker + 结果回传 + 进度聚合 + 依赖汇总子任务 + Web 任务看板
- ⚪ **M3 质量与归档**：复核 + 抽查 + 追溯链 + 审批 + 归档
- ⚪ **M4 并行执行完善**：跨孵化器并行调度 + 负载均衡再平衡 + 合并 + 失败重试
- ⚪ **M5 完善优化**：成本中心 + AI 语义拆解 + TCP 兜底通道 + 模板市场
- ⚪ **M6 统一 Agent 平台协议**：协议驱动注册与能力映射 + OpenAI/Anthropic 驱动 + AutoGen/LangChain/CrewAI 适配器
- ⚪ **M7 DSH 监控终端**：Ink TUI（骨架已就绪，待接三平面完整视图）
- ⚪ **M8 命令终端**：签名校验 + 白名单执行 + 审计回执（骨架已就绪）
- ⚪ PostgreSQL 正式落地 + 多塔联邦

<br/>

## Contributing

欢迎提交 Issue 与 PR。改动协议字段、数据库 Schema 前请先同步 `design/` 下的设计文档；新增模块请对齐《技术设计文档》第七章模块交付清单。

<br/>

## License

MACS 以 **GNU Affero General Public License v3.0（AGPL-3.0）** 发布，并提供商业双授权。

```
SPDX-License-Identifier: AGPL-3.0-or-later
```

| 使用方式                                            | 授权                                              |
| --------------------------------------------------- | ------------------------------------------------- |
| 个人学习、研究、内部评估、与开源项目集成            | ✅ AGPL-3.0 免费授权                              |
| 商用，且愿意按 AGPL 开源你的修改（含网络服务场景）  | ✅ AGPL-3.0 免费授权                              |
| 闭源集成、专有分发、作为托管服务提供而不开源        | 💼 需单独商业授权                                 |

**AGPL 的关键义务**：修改后若通过网络向用户提供服务，必须以 AGPL 提供对应源代码。纯内部自用、不对外提供网络服务的场景，按 GPL 系列的常规边界执行即可。

**商业授权**：`leo@leosindustry.com` · AGPL 全文见 [LICENSE](LICENSE) · 商业授权范围与申请流程见 [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md)。

**第三方依赖**：`modernc.org/sqlite`（BSD-3-Clause）、React / Vite / Ink（MIT）等组件仍适用各自许可证，不受本项目的 AGPL 覆盖，详见 [NOTICE](NOTICE)；分发二进制时请保留其版权与许可声明。

Copyright &copy; 2026 Jarcem

<br/>

---

<p align="center">
  <sub>让目标去找机器，而不是让人去找终端。</sub>
</p>
