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
执行单元可替换：内置文本分析、本机 CLI Agent（argv 直启）、HTTP Agent、OpenAI / Anthropic 协议驱动（凭据仅存孵化器本机）。
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

**Worker 执行单元** — 统一 `Job → Outcome`；内置 `text_analysis`（字符统计）、`merge`（汇总）、`review`（独立复核）；Agent 接入 `cli_agent`（argv 直启）、`http_agent`、`openai_compatible` / `anthropic` 协议驱动（凭据仅存本机）。

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
# 可选：允许本机 Agent 终端接入（Zcode / DeepSeek Harness）
# $env:MACS_ALLOW_CLI_AGENT='1'; $env:MACS_AGENT_ALLOW='zcode,dsh'
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

### 本地开发脚本（启动/停止）

冒烟与联调建议使用安全脚本，避免手工清理进程：

```bash
scripts/dev-start.sh tower incubator cmdterm   # 构建 + 启动 + 登记 PID（幂等）
scripts/dev-stop.sh                            # 定向清理（SIGTERM + 身份核验 + 审计）
```

安全规则与回归测试（37 项断言）见 [scripts/README.md](scripts/README.md)。
**禁止** `kill -9 $(lsof -ti -i :端口)` 式清理——裸 `-i` 会把全部联网进程 PID
形成并集传给 kill，曾导致无关应用被批量误杀（详见该文件的事故记录）。

一次成功的目标会经历（M3 三层质量管线）：

```
POST /api/v1/tasks  →  goal 按字符对半切为子任务 A / B（可并行）+ 汇总 C
                    →  6.4.7 评分选机 → 广播 first-ACK-wins 下发（§6.4.8）
                    →  每台回 ACK → 孵化 Worker → 真实统计 + 自检报告
                    →  独立复核 Worker 重跑比对指纹（approved / E04 打回）
                    →  execution_result 上行 → 云端抽查（E06 追溯链对账）
                    →  归档（交接文档 / 复核报告 / 终审汇总）→ completed
```

在 `http://localhost:5173` 上可以看到：KPI（在线孵化器 x/y、进行中、任务总数、调度记录）、任务卡片与进度条、子任务表（状态 / 执行节点 / worker / 耗时 / 结果）、集群状态与拓扑、调度记录。

### 接入本机 Agent（Zcode / DeepSeek Harness）

两种接入方式，Web 表单与 `POST /api/v1/tasks` 的 `plan` 字段都支持。

**A. 终端 CLI（`cli_agent`）** — 孵化器侧先开闸：

```powershell
$env:MACS_ALLOW_CLI_AGENT='1'
$env:MACS_AGENT_ALLOW='zcode,dsh'   # 可执行文件名白名单，逗号分隔
```

然后提交：

```powershell
curl -X POST http://127.0.0.1:8471/api/v1/tasks `
  -H "Content-Type: application/json; charset=utf-8" `
  -d '{"goal":"审查 src 目录并发安全","plan":[{"mode":"cli_agent","command":"zcode","args":["-p","{{text}}"],"cwd":"E:/proj","timeout_ms":300000}]}'
```

`command` 以 argv 直接启动、不经 shell，参数不会被拼接执行；`{{text}}` 替换为任务目标；退出码 0 = 完成，非 0 或超时 = 失败；stdout 8KB / stderr 32KB 截断回传。

**B. HTTP 接口（`http_agent`）**：

```powershell
curl -X POST http://127.0.0.1:8471/api/v1/tasks -H "Content-Type: application/json" `
  -d '{"goal":"总结这段变更","plan":[{"mode":"http_agent","url":"http://127.0.0.1:9000/run","method":"POST","body":"{\"prompt\":\"{{text}}\"}","timeout_ms":120000}]}'
```

2xx 视为完成，其余为失败；响应体截断 8KB 回传。

**多步 DAG**：`plan` 数组按顺序建子任务，`depends_on` 填前序步骤下标（0 起），前序完成后才下发；下游步骤可用 `{{deps}}` 引用前序结果摘要。Web 表单支持单步 CLI/HTTP，多步直接用 API。

> 安全默认值：`cli_agent` 默认关闭，必须显式 `MACS_ALLOW_CLI_AGENT=1`；命令首 token 必须命中 `MACS_AGENT_ALLOW`，未配置则一律拒绝。HTTP 目标只允许 `http/https`。

### 配置项

`macs-tower`

| 变量                       | 默认                | 说明                                                       |
| -------------------------- | ------------------- | ---------------------------------------------------------- |
| `MACS_TOWER_UDP_PORT`      | `8470`              | UDP 信封协议端口                                           |
| `MACS_TOWER_HTTP_PORT`     | `8471`              | HTTP 管理端口                                              |
| `MACS_TOWER_DB`            | `macs-tower.db`     | SQLite 文件路径                                            |
| `MACS_MIGRATIONS_DIR`      | `migrations`        | SQL 迁移目录                                               |
| `MACS_REGISTER_TOKEN`      | 空（开发模式）      | 孵化器注册令牌                                             |
| `MACS_ADMIN_TOKEN`         | 空                  | Admin Token 引导令牌（danger scope；空 = 开发模式放行）    |
| `DATABASE_URL`             | 空（用 SQLite）     | 预留 PostgreSQL（见 `docker-compose.yml`）                 |
| `MACS_FEATURE_REVIEW`      | `1`                 | 独立复核开关（10.3 feature.review.enabled）                |
| `MACS_FEATURE_SPOT_CHECK`  | `0`                 | 云端抽查开关（10.3 feature.spot_check.enabled）            |
| `MACS_SPOT_CHECK_RATIO`    | `20`                | 抽查抽样比例（Q7）                                         |
| `MACS_MAX_REVIEW_ROUNDS`   | `3`                 | 复核最大打回轮次（Q5）                                     |
| `MACS_BROADCAST_ENABLED`   | `1`                 | 广播下发 first-ACK-wins（关闭则点名单播）                  |
| `MACS_REBALANCE_ENABLED`   | `1`                 | 全局负载再平衡（每 30s）                                   |
| `MACS_SUBTASK_TTL_MIN`     | `30`                | Worker TTL（超时销毁 + E01 重试）                          |
| `MACS_MAX_RETRIES`         | `3`                 | 子任务失败重试上限（E01）                                  |

`macs-incubator`

| 变量                        | 默认                        | 说明                                     |
| --------------------------- | --------------------------- | ---------------------------------------- |
| `MACS_TOWER_HOST`           | `127.0.0.1`                 | 调度塔地址                               |
| `MACS_TOWER_PORT`           | `8470`                      | 调度塔 UDP 端口                          |
| `MACS_INCUBATOR_PORT`       | `8473`                      | 本实例监听端口                           |
| `MACS_INCUBATOR_NAME`       | `dev-incubator`             | 实例名（worker_id 前缀）                 |
| `MACS_MAX_WORKERS`          | `4`                         | 本机并发槽                               |
| `MACS_INCUBATOR_DB`         | `incubator.db`              | 离线缓冲 SQLite 文件                     |
| `MACS_INCUBATOR_TOKEN`      | 空                          | 注册令牌                                 |
| `MACS_ALLOW_CLI_AGENT`      | 空（关闭）                  | 设为 `1` 才允许 `cli_agent`              |
| `MACS_AGENT_ALLOW`          | 空（全拒）                  | CLI 可执行文件白名单，逗号分隔           |
| `MACS_OPENAI_API_KEY`       | 空                          | OpenAI 兼容协议驱动凭据（不上塔）        |
| `MACS_OPENAI_BASE_URL`      | `https://api.openai.com/v1` | 兼容网关地址（可接第三方）               |
| `MACS_OPENAI_MODEL`         | 空                          | 默认模型（任务可按步覆盖）               |
| `MACS_ANTHROPIC_API_KEY`    | 空                          | Anthropic 协议驱动凭据（不上塔）         |
| `MACS_ANTHROPIC_MODEL`      | 空                          | 默认模型                                 |

`macs-cmdterm`

| 变量                    | 默认                                                      | 说明                                                    |
| ----------------------- | --------------------------------------------------------- | ------------------------------------------------------- |
| `MACS_CMDTERM_PORT`     | `8472`                                                    | 命令通道端口                                            |
| `MACS_CMDTERM_ID`       | 随机                                                      | 终端 ID（与塔侧登记的终端对应）                         |
| `MACS_CMDTERM_SECRET`   | 空（拒绝一切指令）                                        | 塔侧登记时生成（一次性展示）的独立密钥                  |
| `MACS_CMDTERM_ALLOW`    | `systeminfo,ipconfig,dir,type,hostname,whoami`            | 命令白名单                                              |

<br/>

## HTTP API

| 方法    | 路径                                          | 说明                                                          |
| ------- | --------------------------------------------- | ------------------------------------------------------------- |
| GET     | `/healthz`                                    | 健康检查                                                      |
| POST   | `/api/v1/tasks`                 | 提交目标 `{"goal":"..."}`，可选 `plan` 接本机 Agent / `budget_cents` 任务预算 |
| GET     | `/api/v1/tasks`                               | 任务列表（V3.3：`status`/`from`/`to`/`q`/`page`/`pageSize`/`sort`） |
| GET     | `/api/v1/tasks/{id}`                          | 任务详情（预算消耗 + 追溯链，V3.2）                           |
| GET     | `/api/v1/tasks/{id}/subtasks`                 | 指定任务的子任务                                              |
| GET     | `/api/v1/tasks/{id}/dag`                      | 子任务 DAG（V3.2）                                            |
| GET     | `/api/v1/tasks/{id}/workers`                  | 子 Worker 状态投影（V3.2）                                    |
| GET     | `/api/v1/tasks/{id}/events`                   | 任务执行事件 feed（V3.2）                                     |
| POST    | `/api/v1/tasks/{id}/cancel`                   | 取消任务（ops scope）                                         |
| GET     | `/api/v1/incubators`                          | 孵化器在线状态与负载画像                                      |
| GET     | `/api/v1/dispatch-log`                        | 调度记录（ACK 状态、应答时间）                                |
| GET/POST| `/api/v1/approvals` · `/{id}/decision`        | 审批中心（E04/E05/E09/guardrail 场景，决策 ops scope）        |
| GET     | `/api/v1/spot-checks`                         | 抽查记录（E06 追溯链对账结论）                                |
| GET     | `/api/v1/trace?task_id=`                      | 追溯链查询（含 broken 检出）                                  |
| GET     | `/api/v1/documents` · `/{id}`                 | 归档文档库（handoff/review/final_summary）                    |
| GET     | `/api/v1/guardrails`                          | 护栏事件（6.4.10）                                            |
| GET     | `/api/v1/costs/summary` · `/costs/records`    | 成本看板（FR-21）                                             |
| GET/POST| `/api/v1/budgets`                             | 多级预算上界（塔侧仅收紧，6.4.11；POST danger scope）         |
| GET/POST| `/api/v1/templates` · `/{id}/deactivate`      | Agent 模板管理（FR-22）                                       |
| GET/POST/PATCH | `/api/v1/agent-platforms`              | 统一 Agent 平台注册表（V3.0 §6.4.13.1）                       |
| GET/POST| `/api/v1/protocol-drivers`                    | 协议驱动注册表（openai-compat / anthropic-msg / cli-native）  |
| GET/POST| `/api/v1/platform-adapters`                   | 平台-驱动适配映射                                             |
| GET     | `/api/v1/telemetry/events`                    | 遥测事件（按 task/subtask/type 过滤）                         |
| GET     | `/api/v1/telemetry/stream`                    | SSE 实时事件流                                                |
| GET/POST| `/api/v1/command-terminals`                   | 命令终端列表 / 登记（登记 danger scope，密钥一次性展示）      |
| POST    | `/api/v1/command-terminals/{id}/exec`         | 签发服务器指令（danger scope，HMAC + nonce + 60s 时间窗）     |
| POST    | `/api/v1/command-terminals/{id}/rotate-secret`| 终端密钥轮换（danger scope，10 分钟双验窗口）                 |
| GET     | `/api/v1/command-terminals/{id}/audit`        | 终端指令审计（A3 对账）                                       |
| POST    | `/api/v1/admin-tokens`                        | 签发 Admin Token（danger scope，read/ops/danger）             |
| POST    | `/api/v1/incubators/{id}/rotate-token`        | 孵化器注册令牌轮换（danger scope，独立令牌一次性展示）        |
| GET/POST| `/api/v1/notify-channels`                     | 告警通知渠道（webhook 单通道；POST danger scope）             |
| POST    | `/api/v1/dispatch`                            | 手工下发（调试用，ops scope）                                 |

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
│  ├─ internal/         gateway dispatcher orchestrator cluster registry store events udp
│  │                    quality（复核+抽查） approvals（审批） trace（追溯链） archive（归档）
│  │                    billing（成本预算） templates（模板） unified（M6 统一协议）
│  │                    cmdterm（M8 命令终端塔侧） authz（三级 scope）
│  └─ migrations/       0001_init / 0002_tasks / 0003_quality（M3~M8 全量表）
├─ macs-incubator/      孵化器（Go）：注册 / 监听 / 并发槽 Worker / 回传 / 离线缓冲
│  ├─ cmd/incubator/    入口（UDP :8473；dispatch_inbox 幂等 + rollback）
│  ├─ internal/         worker（含自检/复核/OpenAI/Anthropic 驱动） probe store
│  └─ src/db/migrations/ 本地 SQLite schema
├─ macs-cmdterm/        受控命令终端（Go，:8472）：HMAC 签名 / 白名单 / 审计 / 注册重试
├─ macs-tower-web/      管理后台（React + Vite）：深色 HUD 单页（审批/质量/成本/归档/终端/平台面板）
├─ macs-dsh-monitor/    DSH 监控终端（Ink TUI，只读）
├─ scripts/             开发环境启停脚本 + 进程安全清理库（含 37 项回归测试）
├─ design/              技术设计 / UI 设计 / 数据库设计 / 协议变更说明 / 设计实现对照表
├─ architecture/        云 Agent 生态架构图
├─ bin/                 构建产物（不入库）
└─ docker-compose.yml   本地 PostgreSQL（可选）
```

<br/>

## Roadmap

- ✅ **M1 核心通信链路**：孵化器注册 + UDP 收发 + 单播调度 + ACK + 集群管理 + 心跳
- ✅ **M2 单 Worker 执行（垂直切片）**：任务表 + 子任务 DAG + 目标拆解 + 真实 Worker + 结果回传 + 进度聚合 + 依赖汇总子任务 + Web 任务看板
- ✅ **M2.5 本机 Agent 接入**：`cli_agent`（argv 直启 + 白名单 + 超时 + 输出截断）+ `http_agent`（2xx 判定 + 响应截断）+ plan DAG + Web 执行方式选择
- ✅ **M3 质量与归档**：自检报告（done 验收门）+ 独立复核 Worker（E04 打回 ≤3 轮）+ 云端抽查引擎（Q7 抽样 + E06 追溯链对账）+ 审批中心（多场景 + 超时）+ 归档文档（交接/复核/终审汇总）
- ✅ **M4 并行执行完善**：6.4.7 评分选机（空槽 + 负载 + 分散惩罚，top-K 候选）+ 6.4.8 广播 first-ACK-wins（dispatch_commit + worker_rollback 幂等回滚）+ E01 失败重试（指数退避）+ Worker TTL 看门狗 + 再平衡（overloaded 巡检）
- ✅ **M5 完善优化（首期）**：成本中心（cost_records + 遥测回填）+ 多级预算（任务级/全局上界 + 80% 告警 + 100% 硬停 E09 审批恢复）+ Agent 模板管理（系统模板播种 + CRUD）+ webhook 告警通知
- ✅ **M6 统一 Agent 平台协议**：协议驱动注册与能力映射三表 + OpenAI/Anthropic 协议驱动（孵化器侧凭据注入 + usage 回填成本）+ 遥测事件（telemetry_events + SSE）
- ✅ **M7 DSH 监控终端**：Ink TUI 只读监控（REST/SSE 数据源，对齐 Web 看板）
- ✅ **M8 命令终端**：每终端独立密钥 + HMAC 签发（60s 时间窗防重放）+ 白名单执行 + 注册 ACK 重试 + 审计双写对账 + 密钥轮换（10 分钟双验）
- ⚙️ **后续迭代**：AI 语义拆解、TCP 兜底通道、模板市场、多租户（见主设计 §3.3 范围三态）

**已知边界**（协议/数据面已就绪、特性未接线的部分）：

- `fetch_request` 大内容分片传输：UDP 信封常量已定义，当前产物均 <256KB 未启用
- 6.4.10 护栏已实现唤醒环冻结（frozen + guardrail 审批）与 done 验收门 + Worker TTL 看门狗；断路器 / 空闲下岗 / 有界上下文待接入
- 下行指令 `task_updated` / `approval_result` / `template_updated` / `config_updated` / `spot_check_request` / `merge_instruction`：协议常量就绪，对应特性按需接线（任务取消已在塔侧停派生效）
- 孵化器侧 `budget_changed` 上界应用待接线（塔侧预算门已在派发边界生效）
- 6.5 预算月度重置、归档清理定时任务；cmdterm 审计 90 天轮转
- 孵化器本地 §3.7 自检/复核报告表（当前报告嵌入 `execution_result` 载荷 + 塔侧归档，不落本地表）
- PostgreSQL 正式落地（当前 SQLite 单库，`DATABASE_URL` 预留）、多塔联邦
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
