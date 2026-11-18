<p align="center">
  <strong>English</strong> &middot;
  <a href="README.zh-CN.md"><strong>简体中文</strong></a>
</p>

<p align="center">
  <a href="#quickstart"><strong>Quickstart</strong></a> &middot;
  <a href="#roadmap"><strong>Development Progress</strong></a> &middot;
  <a href="#contributing"><strong>Contributing Rules</strong></a> &middot;
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
Single-page monitoring: KPIs, task composer, cluster state, topology, dispatch log; SSE-driven refresh with history replay, plus polling fallback.
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
Replaceable execution units: built-in text analysis, local CLI agents (argv launch), HTTP agents, and OpenAI / Anthropic protocol drivers (credentials stay on the incubator).
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

**Worker unit** — a uniform `Job → Outcome` contract; built-in `text_analysis` (character stats), `merge`, `review` (independent re-verification); agent adapters `cli_agent` (argv launch), `http_agent`, and `openai_compatible` / `anthropic` protocol drivers (credentials stay on the incubator).

</td>
</tr>
<tr>
<td>

**Local storage** — SQLite (WAL + busy_timeout) for tasks, subtasks, dispatch log, incubators and offline buffer; PostgreSQL reserved for the cloud side.

</td>
<td>

**Event bus** — in-process broadcast of `task_created / subtask_dispatched / subtask_finished / task_finished`, non-blocking drops, SSE with 15s heartbeats.

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
# Optional: allow local agent terminals (Zcode / DeepSeek Harness)
# $env:MACS_ALLOW_CLI_AGENT='1'; $env:MACS_AGENT_ALLOW='zcode,dsh'
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

### Local dev scripts (start/stop)

Use the safe scripts for smoke testing — never hand-roll process cleanup:

```bash
scripts/dev-start.sh tower incubator cmdterm   # build + run + register PIDs (idempotent)
scripts/dev-stop.sh                            # targeted stop (SIGTERM + identity check + audit)
```

Safety rules and the 37-assertion regression suite: [scripts/README.md](scripts/README.md).
**Never** use `kill -9 $(lsof -ti -i :port)` style cleanup — a bare `-i` unions every
networked process PID into the kill list (incident record in that file).

A successful goal goes through (M3 three-layer quality pipeline):

```
POST /api/v1/tasks  →  goal is split by character into subtasks A / B (parallelizable) + merge C
                    →  6.4.7 scored selection → broadcast first-ACK-wins dispatch (§6.4.8)
                    →  each replies ACK → worker incubated → real analysis + self-check report
                    →  independent review worker re-runs and compares fingerprints (approved / E04 rework)
                    →  execution_result upstream → cloud spot check (E06 trace-chain reconciliation)
                    →  archived (handoff / review / final summary) → completed
```

At `http://localhost:5173` you'll see: KPIs (online incubators x/y, running, total tasks, dispatch entries), task cards with progress bars, a subtask table (status / node / worker / duration / result), cluster status and topology, and the dispatch log.

### Attach local agents (Zcode / DeepSeek Harness)

Two adapters, both available from the web composer and the `plan` field of `POST /api/v1/tasks`.

**A. Terminal CLI (`cli_agent`)** — open the gate on the incubator first:

```powershell
$env:MACS_ALLOW_CLI_AGENT='1'
$env:MACS_AGENT_ALLOW='zcode,dsh'   # executable allowlist, comma-separated
```

Then submit:

```powershell
curl -X POST http://127.0.0.1:8471/api/v1/tasks `
  -H "Content-Type: application/json; charset=utf-8" `
  -d '{"goal":"Review concurrency safety in src","plan":[{"mode":"cli_agent","command":"zcode","args":["-p","{{text}}"],"cwd":"E:/proj","timeout_ms":300000}]}'
```

`command` is launched as argv with no shell, so arguments are never re-interpreted; `{{text}}` expands to the goal; exit code 0 = completed, non-zero or timeout = failed; stdout is capped at 8KB and stderr at 32KB.

**B. HTTP endpoint (`http_agent`)**:

```powershell
curl -X POST http://127.0.0.1:8471/api/v1/tasks -H "Content-Type: application/json" `
  -d '{"goal":"Summarize this change","plan":[{"mode":"http_agent","url":"http://127.0.0.1:9000/run","method":"POST","body":"{\"prompt\":\"{{text}}\"}","timeout_ms":120000}]}'
```

2xx means completed, anything else failed; the response body is capped at 8KB.

**Multi-step DAG**: the `plan` array becomes subtasks in order; `depends_on` holds indices of earlier steps (0-based) and gates dispatch until they finish. Downstream steps can reference prior output with `{{deps}}`. The web composer covers single-step CLI/HTTP; use the API for multi-step plans.

> Safe defaults: `cli_agent` is off unless `MACS_ALLOW_CLI_AGENT=1`; the first token of the command must be in `MACS_AGENT_ALLOW`, otherwise everything is rejected. `http_agent` only accepts `http/https` targets.

### Configuration

`macs-tower`

| Variable                   | Default             | Description                                                      |
| -------------------------- | ------------------- | ---------------------------------------------------------------- |
| `MACS_TOWER_UDP_PORT`      | `8470`              | UDP envelope protocol port                                       |
| `MACS_TOWER_HTTP_PORT`     | `8471`              | HTTP admin port                                                  |
| `MACS_TOWER_DB`            | `macs-tower.db`     | SQLite file path                                                 |
| `MACS_MIGRATIONS_DIR`      | `migrations`        | SQL migration directory                                          |
| `MACS_REGISTER_TOKEN`      | empty (dev mode)    | Incubator registration token                                     |
| `MACS_ADMIN_TOKEN`         | empty               | Bootstrap admin token (danger scope; empty = dev mode, open)     |
| `DATABASE_URL`             | empty (uses SQLite) | PostgreSQL reserved (see `docker-compose.yml`)                   |
| `MACS_FEATURE_REVIEW`      | `1`                 | Independent review toggle (10.3 feature.review.enabled)          |
| `MACS_FEATURE_SPOT_CHECK`  | `0`                 | Cloud spot-check toggle (10.3 feature.spot_check.enabled)        |
| `MACS_SPOT_CHECK_RATIO`    | `20`                | Spot-check sampling ratio (Q7)                                   |
| `MACS_MAX_REVIEW_ROUNDS`   | `3`                 | Max review rework rounds (Q5)                                    |
| `MACS_BROADCAST_ENABLED`   | `1`                 | Broadcast dispatch first-ACK-wins (off = targeted unicast)       |
| `MACS_REBALANCE_ENABLED`   | `1`                 | Global load rebalancing (every 30s)                              |
| `MACS_SUBTASK_TTL_MIN`     | `30`                | Worker TTL (destroy on timeout + E01 retry)                      |
| `MACS_MAX_RETRIES`         | `3`                 | Max subtask failure retries (E01)                                |

`macs-incubator`

| Variable                   | Default                     | Description                                        |
| -------------------------- | --------------------------- | -------------------------------------------------- |
| `MACS_TOWER_HOST`          | `127.0.0.1`                 | Tower address                                      |
| `MACS_TOWER_PORT`          | `8470`                      | Tower UDP port                                     |
| `MACS_INCUBATOR_PORT`      | `8473`                      | Local listen port                                  |
| `MACS_INCUBATOR_NAME`      | `dev-incubator`             | Instance name (prefix for `worker_id`)             |
| `MACS_MAX_WORKERS`         | `4`                         | Local concurrency slots                            |
| `MACS_INCUBATOR_DB`        | `incubator.db`              | Offline-buffer SQLite file                         |
| `MACS_INCUBATOR_TOKEN`     | empty                       | Registration token                                 |
| `MACS_ALLOW_CLI_AGENT`     | empty (off)                 | Set to `1` to enable `cli_agent`                   |
| `MACS_AGENT_ALLOW`         | empty (deny all)            | CLI executable allowlist, comma-separated          |
| `MACS_ALLOW_SESSION_AGENT` | empty (off)                 | Set to `1` to enable session-carrier mode (`session_agent`) |
| `MACS_MAX_TOKENS_CAP`      | `8192`                      | Hard cap on LLM tokens per request (bounded-context guard) |
| `MACS_PLUGINS_ENABLED`      | empty (off)                 | Set to `1` to enable plugin adapter loading (module #29) |
| `MACS_OPENAI_API_KEY`      | empty                       | OpenAI-compatible protocol driver credential (never sent to tower) |
| `MACS_OPENAI_BASE_URL`     | `https://api.openai.com/v1` | Compatible gateway URL (third-party OK)            |
| `MACS_OPENAI_MODEL`        | empty                       | Default model (per-step override supported)        |
| `MACS_ANTHROPIC_API_KEY`   | empty                       | Anthropic protocol driver credential (never sent to tower) |
| `MACS_ANTHROPIC_MODEL`     | empty                       | Default model                                      |

`macs-cmdterm`

| Variable                | Default                                         | Description                                              |
| ----------------------- | ----------------------------------------------- | -------------------------------------------------------- |
| `MACS_CMDTERM_PORT`     | `8472`                                          | Command channel port                                     |
| `MACS_CMDTERM_ID`       | random                                          | Terminal ID (must match a tower-side registered terminal) |
| `MACS_CMDTERM_SECRET`   | empty (rejects all commands)                    | Per-terminal secret generated at registration (shown once) |
| `MACS_CMDTERM_ALLOW`    | `systeminfo,ipconfig,dir,type,hostname,whoami`  | Command allowlist                                        |

<br/>

## HTTP API

| Method         | Path                                            | Description                                                            |
| -------------- | ----------------------------------------------- | ---------------------------------------------------------------------- |
| GET            | `/healthz`                                      | Health check                                                           |
| POST           | `/api/v1/tasks`                                 | Submit a goal `{"goal":"..."}`; optional `plan` for agents, `budget_cents` budget |
| GET            | `/api/v1/tasks`                                 | Task list (V3.3: `status`/`from`/`to`/`q`/`page`/`pageSize`/`sort`)    |
| GET            | `/api/v1/tasks/{id}`                            | Task detail (budget spend + trace chain, V3.2)                         |
| GET            | `/api/v1/tasks/{id}/subtasks`                   | Subtasks of a specific task                                            |
| GET            | `/api/v1/tasks/{id}/dag`                        | Subtask DAG (V3.2)                                                     |
| GET            | `/api/v1/tasks/{id}/workers`                    | Worker state projection (V3.2)                                         |
| GET            | `/api/v1/tasks/{id}/events`                     | Task execution event feed (V3.2)                                       |
| POST           | `/api/v1/tasks/{id}/cancel`                     | Cancel a task (ops scope)                                              |
| GET            | `/api/v1/incubators`                            | Incubator online state and load profile                                |
| GET            | `/api/v1/dispatch-log`                          | Dispatch log (ACK status, response time)                               |
| GET/POST       | `/api/v1/approvals` · `/{id}/decision`          | Approval center (E04/E05/E09/guardrail scenes; decision = ops scope)   |
| GET            | `/api/v1/spot-checks`                           | Spot-check records (E06 trace-chain reconciliation verdicts)           |
| GET            | `/api/v1/trace?task_id=`                        | Trace chain query (with broken-fingerprint detection)                  |
| GET            | `/api/v1/documents` · `/{id}`                   | Archive document library (handoff/review/final_summary)                |
| GET            | `/api/v1/guardrails`                            | Guardrail events (6.4.10)                                              |
| GET            | `/api/v1/costs/summary` · `/costs/records`      | Cost board (FR-21)                                                     |
| GET/POST       | `/api/v1/budgets`                               | Multi-level budget caps (tower-side shrink-only, 6.4.11; POST = danger) |
| GET/POST       | `/api/v1/templates` · `/{id}/deactivate`        | Agent template management (FR-22)                                      |
| GET/POST/PATCH | `/api/v1/agent-platforms`                       | Unified agent platform registry (V3.0 §6.4.13.1)                       |
| GET/POST       | `/api/v1/protocol-drivers`                      | Protocol driver registry (openai-compat / anthropic-msg / cli-native)  |
| GET/POST       | `/api/v1/platform-adapters`                     | Platform-driver adapter mappings                                       |
| GET            | `/api/v1/telemetry/events`                      | Telemetry events (filter by task/subtask/type)                         |
| GET            | `/api/v1/telemetry/stream`                      | SSE real-time event stream                                             |
| GET/POST       | `/api/v1/command-terminals`                     | Command terminal list / registration (danger scope; secret shown once) |
| POST           | `/api/v1/command-terminals/{id}/exec`           | Issue a server command (danger scope; HMAC + nonce + 60s window)       |
| POST           | `/api/v1/command-terminals/{id}/rotate-secret`  | Rotate terminal secret (danger scope; 10-minute dual-validity window)  |
| GET            | `/api/v1/command-terminals/{id}/audit`          | Terminal command audit (A3 reconciliation)                             |
| POST           | `/api/v1/admin-tokens`                          | Issue admin tokens (danger scope; read/ops/danger)                     |
| POST           | `/api/v1/incubators/{id}/rotate-token`          | Rotate incubator registration token (danger scope; shown once)         |
| GET/POST       | `/api/v1/notify-channels`                       | Alert notification channels (webhook first; POST = danger scope)       |
| POST           | `/api/v1/dispatch`                              | Manual dispatch (debugging, ops scope)                                 |
| GET            | `/agent/connect`                                | **V1.5** WSS agent channel (auth via hello: credential hash + protocol; Connector outbound-only) |
| GET/POST       | `/api/v1/agent-connectors` · `/{id}/revoke`     | **V1.5** Connector registry (SHA-256 credential, plaintext shown once; POST/revoke = danger) |
| GET            | `/api/v1/agent-instances`                       | **V1.5** Agent candidate/identity directory (`?status=pending\|verified\|disabled`) |
| POST           | `/api/v1/agent-instances/{id}/verify` · `/{id}/disable` | **V1.5** Human annotation (optimistic-lock revision; ops scope) |
| GET/PUT        | `/api/v1/agent-actions`                         | **V1.5** Action registry (validator whitelist, retry policy; PUT = ops) |
| GET            | `/api/v1/agent-catalog`                         | **V1.5** Verified catalog snapshot (planning input)                    |
| POST           | `/api/v1/agent-plans`                           | **V1.5** Submit PlanDraft (forbidden fields rejected; ops scope)       |
| POST           | `/api/v1/agent-plans/{task}/revisions/{rev}/approve` | **V1.5** Approve plan → frozen assignment + input_digest (danger scope) |
| GET            | `/api/v1/agent-tasks` · `/{id}/attempts` · `/{id}/artifacts` | **V1.5** Collaboration tasks / attempts / shared-storage artifacts |
| POST           | `/api/v1/agent-tasks/{id}/cancel`               | **V1.5** Cancel collaboration task (ops scope)                         |
| GET            | `/api/v1/agent-identity-audit`                  | **V1.5** Annotation/approval audit trail                               |

<br/>

### V1.5 Agent collaboration quickstart

```bash
# 1) Register a Connector (credential is returned once; store it in the Connector's local config only)
curl -X POST http://127.0.0.1:8471/api/v1/agent-connectors -d '{"name":"mac-dev"}'

# 2) Start the incubator in Connector mode (outbound WSS only, no inbound ports)
#    connector.json: tower_url=wss://…/agent/connect, connector_id, credential, agents[].actions[]
MACS_CONNECTOR_CONFIG=connector.json ./macs-incubator

# 3) Annotate the reported candidate as verified (web board → "Agent 协作 V1.5" tab, or API)
# 4) Submit a PlanDraft → approve → tower dispatches via WSS to the frozen agent
# 5) Results must carry a structured self_check; missing/failed self-check never passes acceptance
```

Cross-machine artifacts travel via shared storage (`s3://` / `oci://` / `https://` URIs only; local paths are rejected) and are verified against their SHA-256 digest before execution. Real-environment contract E2E (`macs-tower/test/e2e`, `MACS_RUN_REAL_AGENT_CONTRACT=1`) is BLOCKED-skip until admin-provided action configs exist — mocks do not count as passing.

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
│  ├─ agentmsg/         V1.5 WSS envelope protocol (shared with connector via replace)
│  ├─ internal/         gateway dispatcher orchestrator cluster registry store events udp
│  │                    quality (review + spot check) approvals trace archive
│  │                    billing templates unified (M6 protocol) cmdterm (M8) authz
│  │                    agentdir (V1.5 identity) agentgate (V1.5 WSS) plan acceptance
│  ├─ test/e2e/         V1.5 real-environment contract E2E (BLOCKED-skip without env)
│  └─ migrations/       0001_init / 0002_tasks / 0003_quality (all M3~M8 tables)
├─ macs-incubator/      Incubator (Go): register / listen / slotted workers / upload / offline buffer
│  ├─ cmd/incubator/    Entrypoint (UDP :8473; dispatch_inbox dedup + rollback)
│  ├─ internal/         worker (self-check / review / OpenAI / Anthropic drivers) probe store
│  │                    connector (V1.5: outbound WSS client / outbox / registry / artifacts)
│  └─ src/db/migrations/ Local SQLite schema
├─ macs-cmdterm/        Controlled command terminal (Go, :8472): HMAC signing / allowlist / audit / register retry
├─ macs-tower-web/      Admin console (React + Vite): dark single-page HUD (approvals / quality / cost / archive / terminals / platforms)
├─ macs-dsh-monitor/    DSH monitoring terminal (Ink TUI, read-only)
├─ scripts/             Dev start/stop scripts + safe process-cleanup library (37 regression assertions)
├─ bin/                 Build output (not committed)
└─ docker-compose.yml   Local PostgreSQL (optional)
```

<br/>

## Roadmap

- ✅ **M1 Core communication link**: incubator registration + UDP send/receive + unicast dispatch + ACK + cluster manager + heartbeat
- ✅ **M2 Single-worker execution (vertical slice)**: task table + subtask DAG + goal decomposition + real worker + result upload + progress aggregation + dependency-gated merge subtask + web task board
- ✅ **M2.5 Local agent adapters**: `cli_agent` (argv launch + allowlist + timeout + output truncation) + `http_agent` (2xx verdict + response truncation) + plan DAG + web executor picker
- ✅ **M3 Quality and archiving**: self-check gate + independent review worker (E04 rework ≤3 rounds) + cloud spot checks (Q7 sampling + E06 trace-chain reconciliation) + approval center + archive documents
- ✅ **M4 Parallel execution hardening**: 6.4.7 scored incubator selection (top-K) + broadcast first-ACK-wins (dispatch_commit + worker_rollback) + E01 exponential-backoff retries + worker TTL watchdog + rebalancing
- ✅ **M5 Polish (phase 1)**: cost center + multi-level budgets (80% warn / 100% hard stop via E09 approval) + agent template management + webhook alerting
- ✅ **M6 Unified agent platform protocol**: platform/driver/adapter registry + OpenAI/Anthropic protocol drivers (credential injection on incubator side + usage backfill) + telemetry events (SSE)
- ✅ **M7 DSH monitoring terminal**: Ink TUI read-only monitor (REST/SSE data source aligned with the web HUD)
- ✅ **M8 Command terminal**: per-terminal keys + HMAC-signed issuance (60s replay window) + allowlisted execution + register-ACK retry + audit reconciliation + secret rotation
- ✅ **M2.6 Task-lifecycle stopgap**: mode-aware leases + 5s leaseWatchdog recovery (wired into Start), per-kind conditional finalization for both normal (FinishSubtaskAttempt wired, side effects strictly post-finalization) and review paths, forced cancellation (single transaction + durable cancel tombstones + unicast + full closing), SSE frame spec (id line / Last-Event-ID replay / 15s heartbeat / slow-client disconnect), task-level deterministic watchdog (stalled / repeated-error alerts)
- ✅ **V1.5 Location-independent Agent collaboration (implemented, real-environment E2E pending acceptance)**: WSS-only control channel + Connector/AgentInstance/AgentAction identity directory + human annotation (pending→verified) + PlanDraft validation (forbidden-field rejection) + human approval (frozen assignment + input_digest) + step dependencies with cross-machine artifact exchange (SHA-256 verified) + structured results with fail-closed acceptance + outbox / idempotency / reconnect reconciliation
- ⚪ PostgreSQL production rollout + multi-tower federation

**Known boundaries** (current development progress and verified status):

- 6.4.10 guardrails live: wake-loop freeze, done acceptance gate, circuit breaker, bounded context (LLM max-token cap), worker TTL watchdog
- **Session-carrier workers implemented**: zcode / trae / DeepSeek Harness / claude code / codex as persistent carriers — sub-workers incubate inside the same session (task-level carrier affinity); process-tree termination landed (Unix process group / Windows taskkill /T /F)
- **Plugin adapter loading implemented (module #29)**: Manifest validation + JSON-RPC subprocess isolation + crash monitoring + capability gating + hot reload (plugins.enabled default off)
- **Packet loss simulation verified**: E16 broadcast arbitration converges at 5%/20%/50% loss rates (single winner invariant)
- **fetch_request chunked transfer ready**: FragmentBuffer reassembler + MsgFetchResponse constant (current artifacts <256KB, not yet wired)
- **M2.6 task lifecycle is partially implemented** (audited 2026-11-16 at `48a6662`):
  - Leases: `ClaimSubtaskLease`, mode-aware `LeaseTimeoutFor`, `RecoverExpiredLeases` and `lease_test.go` exist; **`watchdog()` never calls the recovery function**, so expired leases are not reclaimed in production
  - Result idempotency: `FinishSubtaskAttempt` exists with unit tests, but **the normal `HandleResult` branch neither calls it nor writes the `completed` terminal state** (`FinishSubtask` is only used on the failure path), so successful non-review subtasks stay in `dispatched`; only the review branch calls `FinishReviewAttempt`. Cost/telemetry side effects also run before conditional finalization
  - Cancellation: `POST /api/v1/tasks/{id}/cancel`, `ForceCancelSubtasks`, UDP unicast, incubator `CancelRegistry` and `cancel_tombstones` persistence all landed; however the cancel query/update pair now runs in a single transaction; `CancelTask` returns `CancelResult` directly (eliminates cancelState cross-talk)
  - SSE: the tower emits `event: telemetry.update` and Web/DSH both use `addEventListener`; `id:` line + Last-Event-ID replay ≤200 + 15s heartbeat + slow-client 256-frame disconnect
  - Task-level deterministic watchdog (`watchdog_event` / `needs_human` / no-event-for-too-long / repeated identical error) **is not implemented**; only the legacy worker TTL sweep exists
- Downlink wired: `task_cancelled` (rollback running workers) / `approval_result` / `template_updated` / `budget_changed`; `task_updated` / `config_updated` / `spot_check_request` / `merge_instruction` constants ready, wire on demand
- Known test defects (reproduced 2026-11-16 with `go test ./...`):
  - `macs-incubator` `TestSessionDriverLifecycle`: the bash fake agent has no file extension, so Windows `exec` cannot find an executable
  - `macs-incubator` `TestEnabledGateStartsPlugin`: copies `/bin/sleep` as the plugin executable, a path that does not exist on Windows
  - Both are test-portability issues; all `macs-tower` tests pass and `macs-incubator` builds and vets cleanly
- Frontend test gap: `macs-tower-web` and `macs-dsh-monitor` build successfully, but `npm test` exits 1 because no test files exist
- 6.4.11 budgets enforced: task / global / incubator / **template** level (monthly window + budget_changed downlink + dispatch filter)
- 6.5 timers live: stale detection, ACK janitor, rebalance, worker TTL, spot-check retry, monthly budget reset, resource-snapshot cleanup
- `fetch_request` large-content chunked transfer: protocol ready, unused while artifacts stay <256KB
- Local §3.7 self-check/review report tables (reports ride inside `execution_result` payload + tower-side archive)

<br/>

## Contributing

Issues and PRs are welcome. This repository contains only engineering code, tests, runtime configuration, and development/update progress. Technical designs, architecture diagrams, requirement briefs, and design-to-implementation mappings are maintained in DailyWorks and must not be committed or pushed. Agents without DailyWorks access must work from a standalone requirement brief and remove any requirement or design copies before committing and pushing. See [AGENTS.md](AGENTS.md) for the repository rules.

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

**Commercial licensing**: `leo@leosindustry.com` · full AGPL text in [LICENSE](LICENSE) · commercial terms and application steps in [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

**Third-party components**: `modernc.org/sqlite` (BSD-3-Clause), React / Vite / Ink (MIT) and others remain under their own licenses and are not covered by this project's AGPL — see [NOTICE](NOTICE). Keep their copyright and license notices when distributing binaries.

Copyright &copy; 2026 Jarcem

<br/>

---

<p align="center">
  <sub>Let goals find machines, not people find terminals.</sub>
</p>
