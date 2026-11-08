<p align="center">
  <strong>English</strong> &middot;
  <a href="README.zh-CN.md"><strong>简体中文</strong></a>
</p>

<p align="center">
  <a href="#quickstart"><strong>Quickstart</strong></a> &middot;
  <a href="design/MACS技术设计文档.md"><strong>Technical Design</strong></a> &middot;
  <a href="design/UI交互与运维监控设计.md"><strong>UI Design</strong></a> &middot;
  <a href="design/数据库存储设计.md"><strong>Storage Design</strong></a> &middot;
  <a href="https://github.com/Jarcem/MACS"><strong>GitHub</strong></a>
</p>

<br/>

# MACS — Multi-Agent Cooperative System

An open-source multi-agent execution system: one control tower, a swarm of workers that are incubated on demand and destroyed when done.

**Agents live everywhere. Scheduling lives in one place.**

MACS is built from a cloud-side control tower in Go (`macs-tower`) and incubators running on each server (`macs-incubator`). A single goal is decomposed into parallel subtasks, dispatched to workers on different machines, and folded back into a single dark-theme HUD.

It looks like a task board. Underneath: a UDP envelope protocol, DAG-aware scheduling, slot-based load balancing, offline buffering, and signed command auditing.

**Any machine joins. Every task converges.**

|        | Step                 | Example                                                                                      |
| ------ | -------------------- | -------------------------------------------------------------------------------------------- |
| **01** | Define a goal        | _"Analyze the CJK/ASCII distribution of this text and report stats for both shards."_        |
| **02** | Incubate nodes       | Run one incubator per machine; it registers, comes online, and takes work by free slot.       |
| **03** | Submit and observe   | Goal enters the tower → decompose → dispatch in parallel → results return → 100% on the board.|

<br/>

<div align="center">
<table>
  <tr>
    <td align="center"><strong>Stack</strong></td>
    <td align="center"><sub>Go 1.22</sub></td>
    <td align="center"><sub>UDP V3.5</sub></td>
    <td align="center"><sub>SQLite (pure Go)</sub></td>
    <td align="center"><sub>React + Vite</sub></td>
    <td align="center"><sub>Ink TUI</sub></td>
    <td align="center"><sub>HMAC-SHA256</sub></td>
  </tr>
</table>

<em>If it can receive a dispatch, it can take the job.</em>

</div>

<br/>

## MACS is for you if

- ✅ You run **multiple machines and multiple agent terminals**, and nobody knows what the others are doing
- ✅ You want one goal **split into parallel subtasks**, not queued into a single long line
- ✅ You need **work spread across machines**, not piled onto one box
- ✅ You want **observability**: who took it, how far it got, what came back, how long it took
- ✅ You need a **controlled command channel** without installing third-party clients on every server
- ✅ You want **results to survive a network outage** and be re-sent automatically afterwards

<br/>

## Features

<table>
<tr>
<td align="center" width="33%">
<h3>📡 UDP envelope protocol</h3>
One V3.5 envelope: <code>msg_id / type / version / in_reply_to / payload</code> — registration, dispatch, results and heartbeat share a single protocol.
</td>
<td align="center" width="33%">
<h3>🌱 Incubator model</h3>
Workers are incubated on demand and destroyed when done. Concurrency is bounded by local <code>max_workers</code> slots — no idle processes.
</td>
<td align="center" width="33%">
<h3>🧩 Decomposition / DAG</h3>
Goal → subtasks → dependency edges. Ready only when <code>depends_on</code> is satisfied: parallel where possible, serial where required.
</td>
</tr>
<tr>
<td align="center">
<h3>⚖️ Slot-based balancing</h3>
Pick the machine with the most free slots: <code>capacity - running</code>. Multiple incubators share load without manual pinning.
</td>
<td align="center">
<h3>🔁 ACK + retry</h3>
Every dispatch is ACKed: 3s timeout, exponential backoff, up to 3 attempts, all recorded in <code>dispatch_log</code>.
</td>
<td align="center">
<h3>🛡️ Idempotency</h3>
<code>msg_id</code> is deduplicated for 10 minutes; replays are never executed twice, and collisions are logged instead of silently dropped.
</td>
</tr>
<tr>
<td align="center">
<h3>💾 Offline buffer</h3>
Failed uploads land in local SQLite first; the Reporter re-sends them periodically so results survive outages.
</td>
<td align="center">
<h3>🖥️ Dark HUD</h3>
Single-page monitoring: KPIs, task composer, cluster state, topology, dispatch log; SSE events plus 2s polling.
</td>
<td align="center">
<h3>⌨️ Terminal monitor</h3>
A read-only Ink TUI mirroring the web board, ideal for transparent displays or SSH sessions.
</td>
</tr>
<tr>
<td align="center">
<h3>🔐 Controlled command terminal</h3>
<code>macs-cmdterm</code> accepts only tower-signed <code>cmd_exec</code>: HMAC-SHA256 + 60s time window + command allowlist.
</td>
<td align="center">
<h3>🧾 Full audit trail</h3>
Registration, dispatch, ACK, execution results and commands are all persisted (SQLite or JSONL) and traceable.
</td>
<td align="center">
<h3>🧠 Bring your own agent</h3>
The execution unit is replaceable: today a real text-analysis shard, tomorrow a CLI, an API, or a protocol adapter.
</td>
</tr>
</table>

<br/>

## Problems MACS solves

| Without MACS                                                                                                      | With MACS                                                                                                   |
| ----------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| ❌ A dozen agent terminals across servers; who is doing what lives only in your head.                              | ✅ A goal enters the tower, gets decomposed, and subtasks / nodes / workers / results are all persisted.     |
| ❌ Work is strictly serial: one machine saturated while the others idle.                                            | ✅ Slot-based placement lets subtasks of the same goal run in parallel on different incubators.              |
| ❌ Under UDP loss or missing ACKs, a task hangs at "dispatched" with no explanation.                                | ✅ ACKs have timeouts and retries; failures are persisted as failed; duplicate results are never re-run.      |
| ❌ One network blip and the worker's result is gone.                                                                | ✅ Failed uploads are buffered locally and re-sent by the Reporter once connectivity returns.                 |
| ❌ Remote execution requires SSH clients or third-party agents with unbounded privileges.                            | ✅ The command terminal only honors signed instructions, with an allowlist and an audit trail.                 |
| ❌ Building multi-machine orchestration from scratch means weeks of registration, heartbeat, retry and dedup work.  | ✅ It's already in the tower; onboarding a machine is running one incubator process.                          |

<br/>

## Why MACS is different

MACS gets the parts of distributed orchestration that are easiest to get wrong, right.

|                                     |                                                                                                              |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| **Register before send.**            | An ACK can arrive before the pending entry exists (localhost / low latency). Reversing the order means guaranteed false timeouts → retries → duplicate execution. |
| **Unique message IDs.**              | `msg_id` uses 16 random bytes, not a timestamp. Timestamps collide when messages are emitted in the same tick and get silently swallowed by the dedup window. |
| **Serialized single writer.**        | SQLite uses `_pragma=busy_timeout` plus `SetMaxOpenConns(1)`, so concurrent writes no longer hit `SQLITE_BUSY`. |
| **One-pass scheduling loop.**        | A `kick` channel debounces work; writes wake the loop, which picks ready tasks by satisfied dependencies and free slots; claims are atomic (pending only). |
| **Derived progress.**                | Task progress is aggregated from subtask states instead of trusting worker-reported percentages; failures propagate immediately to the parent. |
| **Fail-open uploads.**               | A failed upload never blocks execution: buffer first, re-send later, keep it locally if that fails too. |
| **Zero CGO.**                        | `modernc.org/sqlite` is a pure-Go implementation; cross-compilation and deployment need no C toolchain. |

<br/>

## Architecture

```
                    ┌──────────────────────────────────────────────┐
                    │              macs-tower  control tower       │
                    │                                              │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
                    │  │ UDP      │  │ Broadcast│  │ Cluster  │    │
                    │  │ Gateway  │  │ Dispatcher│ │ Manager  │    │
                    │  └──────────┘  └──────────┘  └──────────┘    │
                    │  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
                    │  │ Task     │  │ Task/    │  │ Event    │    │
                    │  │ Orchestr.│  │ Subtask  │  │ Bus      │    │
                    │  │          │  │ Store    │  │          │    │
                    │  └──────────┘  └──────────┘  └──────────┘    │
                    └───────▲──────────────▲──────────────┬───────┘
             register / │              │ dispatch      │ REST / SSE
             report /   │              │ (UDP unicast) │ :8471
          execution_result            │               ▼
                    ┌───────┴──────┐   │      ┌────────────────────┐
                    │macs-incubator│◄─┘      │   macs-tower-web   │
                    │  (incubator) │          │  dark HUD, 1 page  │
                    │              │          └────────────────────┘
                    │ ┌──────────┐ │          ┌────────────────────┐
                    │ │ spawner  │ │          │ macs-dsh-monitor   │
                    │ │ worker   │ │          │ Ink TUI (read-only)│
                    │ └──────────┘ │          └────────────────────┘
                    │ ┌──────────┐ │          ┌────────────────────┐
                    │ │ SQLite   │ │          │   macs-cmdterm     │
                    │ │ offline  │ │          │ signed cmds :8472  │
                    │ │ buffer   │ │          └────────────────────┘
                    │ └──────────┘ │
                    └──────────────┘
```

Port assignments (per Appendix A · Q9 of the design docs):

| Component        | Port        | Protocol                |
| ---------------- | ----------- | ----------------------- |
| `macs-tower`     | 8470        | UDP envelope protocol   |
| `macs-tower`     | 8471        | HTTP REST / SSE         |
| `macs-cmdterm`   | 8472        | UDP command channel     |
| `macs-incubator` | 8473 / 8474 | UDP (one per instance)  |
| `macs-tower-web` | 5173        | Vite dev server         |

### Subsystems

<table>
<tr>
<td width="50%">

**UDP gateway** — 256KB limit, routing by `type`, 10-minute `msg_id` dedup; ACKs are never ACKed, preventing ping-pong storms.

</td>
<td width="50%">

**Dispatcher** — unicast to a target, wait for ACK (3s), exponential backoff, terminal state written to `dispatch_log`; pending is registered before the packet is sent.

</td>
</tr>
<tr>
<td>

**Orchestrator** — persist the goal → decompose → scan ready pending subtasks → pick by free slots → claim → dispatch; aggregate progress as results return.

</td>
<td>

**Cluster manager** — incubator registry, heartbeat and stale sweeps, resource profile, online state and capacity (`max_workers`).

</td>
</tr>
<tr>
<td>

**Incubator** — registers for an `incubator_id`, listens for dispatches, ACKs them, runs workers within its slot budget, uploads `execution_result`, and reports periodically.

</td>
<td>

**Worker unit** — a uniform `Job{Mode, Shard, Text} → Outcome` contract; currently `text_analysis` (characters, non-space, unique, top characters), replaceable with a real agent.

</td>
</tr>
<tr>
<td>

**Local storage** — SQLite (WAL + busy_timeout) for tasks, subtasks, dispatch log, incubators and offline buffer; PostgreSQL reserved for the cloud side.

</td>
<td>

**Event bus** — in-process broadcast of `task_created / subtask_dispatched / subtask_finished / task_finished`, non-blocking drops, SSE with 30s heartbeats.

</td>
</tr>
<tr>
<td>

**Admin console** — React 18 + Vite single-page HUD: KPIs, task composer, task cards and subtask table, cluster status, topology, dispatch log.

</td>
<td>

**Controlled terminal** — registration and heartbeat; only signed `cmd_exec` is executed: ConstantTimeCompare, nonce/time-window replay protection, allowlist, shell metacharacter rejection.

</td>
</tr>
</table>

<br/>

## What MACS is not

|                                |                                                                                             |
| ------------------------------ | ------------------------------------------------------------------------------------------- |
| **Not an agent framework.**     | It doesn't dictate how you build agents — only how they're scheduled, observed, and audited. |
| **Not a workflow editor.**      | No drag-and-drop canvas. A task is a goal plus a subtask DAG, expressed as data.              |
| **Not a message queue.**        | UDP + ACK + dedup exists for low-latency intranet scheduling, not general-purpose MQ.         |
| **Not a single-machine script.**| It runs on one box, but its value comes from many machines taking work together.              |
| **Not a full SSH replacement.** | `macs-cmdterm` is a controlled command channel — signed, allowlisted, audited; not an interactive shell. |

<br/>

## Quickstart

Open source, self-hosted, no account required.

```powershell
# 1) Build the two Go binaries (Go 1.22+)
cd macs-tower
go build -o ..\bin\tower.exe ./cmd/tower
cd ..\macs-incubator
go build -o ..\bin\incubator.exe ./cmd/incubator

# 2) Start the control tower (UDP :8470 / HTTP :8471)
cd ..\macs-tower
$env:MACS_MIGRATIONS_DIR = 'migrations'
..\bin\tower.exe

# 3) Start two incubators (single-machine dual-worker vertical slice)
$env:MACS_INCUBATOR_PORT='8473'; $env:MACS_INCUBATOR_NAME='worker-a'; ..\bin\incubator.exe
$env:MACS_INCUBATOR_PORT='8474'; $env:MACS_INCUBATOR_NAME='worker-b'; ..\bin\incubator.exe

# 4) Start the monitoring board
cd ..\macs-tower-web
npm install
npm run dev      # http://localhost:5173

# 5) Submit your first goal
curl -X POST http://127.0.0.1:8471/api/v1/tasks `
  -H "Content-Type: application/json; charset=utf-8" `
  -d '{"goal":"Analyze the CJK and ASCII distribution of this text and report stats for both shards."}'
```

> Bash equivalent: replace `$env:VAR='v'` with `VAR=v ./bin/xxx`.

A successful goal goes through:

```
POST /api/v1/tasks  →  goal is split by character into subtasks A / B (parallelizable)
                    →  slot-based placement: A → worker-a:8473, B → worker-b:8474
                    →  each replies ACK → worker incubated → real analysis
                    →  execution_result upstream → 50% → 100% → completed
```

At `http://localhost:5173` you'll see: KPIs (online incubators x/y, running, total tasks, dispatch entries), task cards with progress bars, a subtask table (status / node / worker / duration / result), cluster status and topology, and the dispatch log.

### Configuration

`macs-tower`

| Variable                | Default             | Description                                       |
| ----------------------- | ------------------- | ------------------------------------------------- |
| `MACS_TOWER_UDP_PORT`   | `8470`              | UDP envelope protocol port                        |
| `MACS_TOWER_HTTP_PORT`  | `8471`              | HTTP admin port                                   |
| `MACS_TOWER_DB`         | `macs-tower.db`     | SQLite file path                                  |
| `MACS_MIGRATIONS_DIR`   | `migrations`        | SQL migration directory                           |
| `MACS_REGISTER_TOKEN`   | empty (dev mode)    | Incubator registration token                      |
| `MACS_ADMIN_TOKEN`      | empty               | `/api/v1` admin token (reserved)                  |
| `DATABASE_URL`          | empty (uses SQLite) | PostgreSQL reserved (see `docker-compose.yml`)    |

`macs-incubator`

| Variable                | Default             | Description                            |
| ----------------------- | ------------------- | -------------------------------------- |
| `MACS_TOWER_HOST`       | `127.0.0.1`         | Tower address                          |
| `MACS_TOWER_PORT`       | `8470`              | Tower UDP port                         |
| `MACS_INCUBATOR_PORT`   | `8473`              | Local listen port                      |
| `MACS_INCUBATOR_NAME`   | `dev-incubator`     | Instance name (prefix for `worker_id`) |
| `MACS_MAX_WORKERS`      | `4`                 | Local concurrency slots                |
| `MACS_INCUBATOR_DB`     | `incubator.db`      | Offline-buffer SQLite file             |
| `MACS_INCUBATOR_TOKEN`  | empty               | Registration token                     |

`macs-cmdterm`

| Variable                | Default                                         | Description                           |
| ----------------------- | ----------------------------------------------- | ------------------------------------- |
| `MACS_CMDTERM_PORT`     | `8472`                                          | Command channel port                  |
| `MACS_CMDTERM_SECRET`   | empty (rejects all commands)                    | HMAC-SHA256 secret shared with tower  |
| `MACS_CMDTERM_ALLOW`    | `systeminfo,ipconfig,dir,type,hostname,whoami`  | Command allowlist                     |

<br/>

## HTTP API

| Method | Path                            | Description                                |
| ------ | ------------------------------- | ------------------------------------------ |
| GET    | `/healthz`                      | Health check                               |
| POST   | `/api/v1/tasks`                 | Submit a goal `{"goal": "..."}`            |
| GET    | `/api/v1/tasks`                 | Task list (subtasks embedded)              |
| GET    | `/api/v1/tasks/{id}/subtasks`   | Subtasks of a specific task                |
| GET    | `/api/v1/incubators`            | Incubator online state                     |
| GET    | `/api/v1/dispatch-log`          | Dispatch log (ACK status, response time)   |
| GET    | `/api/v1/agent-platforms`       | Agent platform catalog                     |
| GET    | `/api/v1/platform-adapters`     | Platform adapter catalog                   |
| GET    | `/api/v1/telemetry/events`      | Recent telemetry events                    |
| GET    | `/api/v1/telemetry/stream`      | SSE real-time event stream                 |
| POST   | `/api/v1/dispatch`              | Manual dispatch (debugging)                |

<br/>

## FAQ

**How many workers can one incubator run?**
Bounded by `MACS_MAX_WORKERS` (default 4). The tower computes `capacity - running`; once full, it stops placing work there.

**Why UDP instead of TCP/HTTP?**
Intranet scheduling favors low latency, connectionless delivery and easy multi-machine onboarding. Reliability is added via ACK, retry and idempotent dedup rather than at the transport layer.

**How are subtasks decomposed?**
The current vertical slice splits the goal by character into two halves for real parallelism. `decompose()` is the single swap point; plug in an LLM later for semantic DAGs.

**Can a result be executed twice?**
No. `msg_id` is deduplicated within a 10-minute window; duplicate uploads are dropped and logged. On the dispatch side, pending is registered before sending, avoiding false timeouts caused by early ACKs.

**What happens during a network outage?**
Execution continues; `execution_result` is buffered in local SQLite and the Reporter retries every 60s, marking the entry synced on success.

**Can I use only the web board?**
Yes. The board depends only on the tower's `/api/v1`. For a terminal form factor, use `macs-dsh-monitor` (read-only Ink TUI).

<br/>

## Repository layout

```
macs/
├─ macs-tower/          Control tower (Go): UDP gateway / dispatcher / orchestrator / cluster / store / events
│  ├─ cmd/tower/        Entrypoint (UDP :8470 + HTTP :8471)
│  ├─ internal/         gateway dispatcher orchestrator cluster store events udp
│  └─ migrations/       0001_init.sql / 0002_tasks.sql
├─ macs-incubator/      Incubator (Go): register / listen / slotted workers / upload / offline buffer
│  ├─ cmd/incubator/    Entrypoint (UDP :8473)
│  ├─ internal/         worker probe store
│  └─ src/db/migrations/ Local SQLite schema
├─ macs-cmdterm/        Controlled command terminal (Go, :8472): HMAC signing / allowlist / audit
├─ macs-tower-web/      Admin console (React + Vite): dark single-page HUD
├─ macs-dsh-monitor/    DSH monitoring terminal (Ink TUI, read-only)
├─ design/              Technical / UI / storage design documents
├─ architecture/        Cloud agent ecosystem architecture diagram
├─ bin/                 Build output (not committed)
└─ docker-compose.yml   Local PostgreSQL (optional)
```

<br/>

## Roadmap

- ✅ **M1 Core communication link**: incubator registration + UDP send/receive + unicast dispatch + ACK + cluster manager + heartbeat
- ✅ **M2 Single-worker execution (vertical slice)**: task table + subtask DAG + goal decomposition + real worker + result upload + progress aggregation + web task board
- ⚪ **M3 Quality and archiving**: review + spot checks + traceability chain + approvals + archiving
- ⚪ **M4 Parallel execution hardening**: cross-incubator parallel scheduling + load rebalancing + merging + failure retries
- ⚪ **M5 Polish**: cost center + AI semantic decomposition + TCP fallback channel + template marketplace
- ⚪ **M6 Unified agent platform protocol**: protocol-driven registration and capability mapping + OpenAI/Anthropic drivers + AutoGen/LangChain/CrewAI adapters
- ⚪ **M7 DSH monitoring terminal**: Ink TUI (skeleton ready, waiting for the full three-plane view)
- ⚪ **M8 Command terminal**: signature verification + allowlisted execution + audit receipts (skeleton ready)
- ⚪ PostgreSQL production rollout + multi-tower federation

<br/>

## Contributing

Issues and PRs are welcome. Before changing protocol fields or the database schema, update the documents under `design/` first. New modules should follow the delivery list in Chapter 7 of the technical design.

<br/>

## License

MACS is released under the **GNU Affero General Public License v3.0 (AGPL-3.0)**, with a commercial dual-licensing option.

```
SPDX-License-Identifier: AGPL-3.0-or-later
```

| Usage                                                        | License                                        |
| ------------------------------------------------------------ | ---------------------------------------------- |
| Personal study, research, internal evaluation, open-source integration | ✅ Free under AGPL-3.0                |
| Commercial use while open-sourcing your modifications (including network services) | ✅ Free under AGPL-3.0 |
| Closed-source integration, proprietary distribution, or hosting as a service without open-sourcing | 💼 Requires a separate commercial license |

**Key AGPL obligation**: if you modify the software and make it available to users over a network, you must offer the corresponding source under AGPL. Purely internal use without exposing a network service follows the usual GPL-family boundary.

**Commercial licensing**: `leo@leosindustry.com` · see [LICENSE](LICENSE) for the full terms.

**Third-party components**: `modernc.org/sqlite` (BSD-3-Clause), React / Vite / Ink (MIT) and others remain under their own licenses and are not covered by this project's AGPL. Keep their copyright and license notices when distributing binaries.

Copyright &copy; 2026 Jarcem

<br/>

---

<p align="center">
  <sub>Let goals find machines, not people find terminals.</sub>
</p>
