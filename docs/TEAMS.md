# mittens Teams

Multi-agent orchestration where a leader AI session coordinates worker agents in separate containers to parallelize complex tasks.

Use this document for the full Teams feature reference. `CLAUDE.md` keeps only the condensed summary.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Host (mittens binary)                                  │
│  ┌─────────────┐                                        │
│  │ HostBroker   │ ← /pool/spawn, /pool/kill,           │
│  │ (TCP server) │   /pool/containers                    │
│  └──────┬──────┘                                        │
│         │ docker run / docker stop                      │
├─────────┼───────────────────────────────────────────────┤
│  Leader Container                                       │
│  ┌──────────────┐   ┌────────────────────────────────┐  │
│  │ AI CLI         │──│ team-mcp (MCP sidecar)         │  │
│  │ (interactive)  │   │  ├─ PoolManager (state machine)│  │
│  └──────────────┘   │  ├─ WorkerBroker (:8080)       │  │
│                      │  ├─ PipelineExecutor           │  │
│                      │  ├─ Heartbeat Reaper           │  │
│                      │  └─ WAL (events.jsonl)         │  │
│                      └────────────────────────────────┘  │
├──────────────────────────────────────────────────────────┤
│  Worker Containers (1..N)                                │
│  ┌─────────────────┐  ┌─────────────────┐               │
│  │ Claude --print   │  │ Claude --print   │  ...         │
│  │ role: planner    │  │ role: implementer│               │
│  │ polls /task      │  │ polls /task      │               │
│  │ POSTs /complete  │  │ POSTs /complete  │               │
│  └─────────────────┘  └─────────────────┘               │
└──────────────────────────────────────────────────────────┘
```

## CLI Subcommands

```bash
mittens team --name strike-team-a   # Launch an existing named team
mittens team init                   # Create a new named team, then launch it
mittens team init --name review-lab # Create or edit a named team, then launch it
mittens team status                 # Show sessions for the current project
mittens team status --all-projects  # Show sessions across all projects
mittens team status --limit 5       # Cap stopped sessions shown (default 10)
mittens team status --json          # Machine-readable JSON output
mittens team resume <session-id>    # Reconnect to a crashed session
mittens team clean                  # Prune sessions older than 7 days
mittens team clean --all            # Remove all non-running sessions
mittens team clean --dry-run        # Preview what would be removed
mittens team help                   # Show help
```

## Team Model

Team configs are now global and named. A team is a reusable worker-routing preset, while a session is one concrete launch of that team in a project.

- `mittens team --name strike-team-a` launches an already-configured team
- `mittens team init --name strike-team-a` creates or edits that team config, then launches it
- unnamed launches are not supported; new teams must be explicitly configured first

Each launch gets a generated session ID derived from the team name, so you can run and resume multiple sessions of the same team without config-name collisions.

## Leader Provider

The team leader uses the normal mittens provider selection flow, not the named team config.

- `mittens team --name strike-team-a --provider codex` launches a Codex-led team session
- `mittens team --name strike-team-a --provider claude` launches a Claude-led team session
- project defaults from `mittens init` also apply to the leader

The named team config still controls worker routing only.

## Team Status

`mittens team status` lists team sessions discovered from disk (the `pools/` directory), not just running Docker containers. This means stopped sessions are visible too.

**Default behavior**: shows sessions for the current project — all running sessions plus the most recent 10 stopped sessions, sorted with running first.

**Flags**:

| Flag | Description |
|---|---|
| `--all-projects` | Show sessions from all projects, not just the current workspace |
| `--limit N` | Cap the number of stopped sessions shown (default 10); running sessions are always shown |
| `--json` | Output as a JSON array of session objects for scripting |

**Table output**:

```
PROJECT              TEAM               SESSION              STATUS     STARTED            LAST ACTIVITY      WORKERS
my-project           strike-team-a      strike-team-a-kx1    running    2026-03-31 14:00   2026-03-31 14:12   3
my-project           review-lab         review-lab-kx2       stopped    2026-03-30 09:00   2026-03-30 10:30   -
```

**Session metadata**: each session persists a `session.json` file in its state directory (`~/.mittens/projects/<projDir>/pools/<session-id>/session.json`) containing the workspace path, team name, session ID, and start time. This enables `team status` to display accurate metadata for stopped sessions without requiring Docker.

## Configuration

Named team configs live at `~/.mittens/teams/<team>/team.yaml`.

`mittens team init` now edits a named team in a role-first loop:

- set `max_workers`
- set the worker-session process (`fresh each task`, `balanced reuse`, `aggressive reuse`, or custom)
- edit `default`, `planner`, `implementer`, or `reviewer`
- for each role, choose provider from a preset list
- then choose model as provider default, recommended preset, or custom

`adapter` is no longer part of the normal team-init flow.

```yaml
max_workers: 4
models:
  default:
    provider: "codex"
    model: "gpt-5.4"
  planner:
    provider: "codex"
    model: ""
  implementer:
    provider: "claude"
    model: "claude-sonnet-4-6"
  reviewer:
    provider: "claude"
    model: "claude-sonnet-4-6"
```

Canonical provider names are `claude`, `codex`, and `gemini`. The aliases `anthropic`, `openai`, and `google` are also accepted in `team.yaml`.

## Worker Roles

- **Planner**: researches the codebase and creates structured execution plans with phases and dependencies
- **Implementer**: executes implementation tasks, produces code changes, and can ask blocking questions
- **Reviewer**: reviews completed tasks, reports pass/fail with feedback, and cannot self-review

`ModelRouter` maps role to `ModelConfig` from `team.yaml`. Fallback key: `default`.

## Worker Lifecycle

1. **Spawn**: `PoolManager` creates a Docker container with `cap-drop ALL`, `--print` mode, and tracking labels.
2. **Register**: Worker POSTs `/register` to `WorkerBroker`, transitioning from `spawning` to `idle`.
3. **Dispatch**: Leader calls `dispatch_task`; worker polls `GET /task/{workerId}`.
4. **Execute**: Worker runs the AI CLI in print mode on the task prompt.
5. **Complete**: Worker POSTs `/complete` with the result and optional `TaskHandover` context.
6. **Heartbeat**: Workers POST `/heartbeat`; the reaper checks every 30s with a 90s timeout, marks stale workers dead, and requeues their tasks.

## Communication

`WorkerBroker` runs as HTTP on `:8080` inside the leader container.

| Endpoint | Method | Purpose |
|---|---|---|
| `/register` | POST | Worker startup registration |
| `/heartbeat` | POST | Liveness check |
| `/task/{workerId}` | GET | Worker polls for next task |
| `/complete` | POST | Signal task completion (data on filesystem) |
| `/fail` | POST | Report task failure |
| `/question` | POST | Ask a blocking question |
| `/answer/{qid}` | GET | Poll for an answer |

`HostBroker` runs on the host and exposes `/pool/spawn`, `/pool/kill`, `/pool/containers`, and `/pool/session-alive` for Docker operations.

## MCP Tools

The leader-facing `team-mcp` server exposes:

`spawn_worker`, `kill_worker`, `enqueue_task`, `dispatch_task`, `wait_for_task`, `get_pool_state`, `get_task_state`, `get_status`, `get_worker_activity`, `get_task_result`, `get_task_output`, `submit_pipeline`, `cancel_pipeline`, `dispatch_review`, `report_review`, `answer_question`, `resolve_escalation`, `pending_questions`, `create_plan`, `list_plans`, `read_plan`, `claim_plan`, `update_plan_progress`, `complete_plan`, `check_session`

`get_pool_state` stays the cheap polling surface for scheduling and capacity checks.
Use `get_status` for explicit full status reports. Its worker rows now surface the
latest live activity summary for Claude and Codex workers, pending question
metadata, and an `inspectionTool` hint when deeper inspection is available.
Use `get_worker_activity` selectively after `get_status` points you at a worker
that needs deeper inspection of its current task, pending blocker, or worker-side
artifacts such as `task.md`, `result.txt`, `handover.json`, or `error.txt`.

## Leader Commands

Claude leaders register slash commands:

- **`/mt:status`**: calls `get_status` and shows workers with live activity/blocker summaries plus `get_worker_activity` inspection hints, alongside tasks, pipelines, and pending questions
- **`/mt:plan <description>`**: spawns a planner worker, presents a structured plan, and waits for approval
- **`/mt:execute <plan-id>`**: executes a pending plan from the plans directory
- **`/mt:plans`**: lists all plans with status and progress

Codex leaders install user skills:

- **`$mt-status`**: calls `get_status` and shows workers with live activity/blocker summaries plus `get_worker_activity` inspection hints, alongside tasks, pipelines, and pending questions
- **`$mt-plan <description>`**: spawns a planner worker, presents a structured plan, and waits for approval
- **`$mt-execute <plan-id>`**: executes a pending plan from the plans directory
- **`$mt-plans`**: lists all plans with status and progress

Codex built-ins such as `/mcp`, `/agent`, and `/plan` remain available alongside the team skills.

Codex leaders should follow up on every dispatched task with `get_task_state`
from the main leader flow.
Long-running active tasks should be re-checked proactively at a coarse cadence,
even if the user has not asked for a status update yet.

For leaders and operators, the intended inspection flow is:

1. Use `get_pool_state` for routine scheduling and capacity polling.
2. Use `get_status` when you need a user-facing or operator-facing full report.
3. If a worker row shows a blocker, suspicious activity, or an `inspectionTool`
   hint, call `get_worker_activity` for that worker only.

## Pipelines And Reviews

A pipeline is a multi-stage task graph submitted with `submit_pipeline`. `PipelineExecutor` watches task completions and automatically advances stages. `TaskHandover` context flows between stages.

Review flow:

1. Task completes.
2. `dispatch_review` sends it to a reviewer.
3. Reviewer reports a verdict.
4. On pass, the task is accepted and the pipeline advances.
5. On fail below the retry limit, the task is rejected and re-queued.
6. On fail at the retry limit, the task is escalated and the leader must `resolve_escalation` with `accept`, `retry`, or `abort`.

## WAL And Crash Recovery

All `PoolManager` state mutations follow `lock -> WAL append -> in-memory apply -> unlock`.

WAL file:

`~/.mittens/projects/<projDir>/pools/<session-id>/events.jsonl`

Event types include:

- `PoolCreated`
- `Worker{Spawned,Ready,Busy,Idle,Blocked,Dead}`
- `Task{Created,Dispatched,Completed,Failed,Canceled,Requeued,Accepted,Rejected,Escalated}`
- `Review{Dispatched,Completed}`
- `Pipeline{Created,Completed,Failed,Blocked,Unblocked,StageAdvanced}`
- `Worker{Question,QuestionAnswered}`

Recovery for `mittens team resume`:

1. `WAL.Replay()`
2. Rebuild in-memory state
3. `Reconcile()` against running containers
4. Mark missing workers dead
5. `RequeueOrphanedTasks()`
6. `PipelineExecutor.ScanStuckPipelines()`

## Worker Activity Files

High-churn worker activity does not go into the WAL.

- Live worker status still comes from heartbeat-backed `CurrentActivity` / `CurrentTool`.
- Recent worker activity history is persisted under the existing worker state directory:
  `~/.mittens/projects/<projDir>/pools/<session-id>/workers/<worker-id>/`
- The worker writes JSONL snapshots to `activity.jsonl`, one normalized `WorkerActivityRecord` per line:
  `{"recordedAt":"...","taskId":"t-1","activity":{"kind":"tool","phase":"started","name":"Read","summary":"README.md"}}`
- Retention is bounded with simple rotation: each `activity.jsonl` generation keeps up to 128 records, then the file is rotated to `activity.prev.jsonl` and a fresh `activity.jsonl` starts. This keeps recent debugging history on disk without unbounded growth or WAL churn.
- `get_worker_activity` exposes a compact recent tail from `activity.prev.jsonl` + `activity.jsonl` so operators can inspect what a worker was doing even after the live heartbeat state has cleared.

## Session Reuse

Workers can optionally keep their AI session alive between tasks instead of clearing it each time. This lets the underlying model benefit from prompt caching and accumulated context, reducing latency and token cost for sequential tasks on the same worker.

Session reuse is **opt-in** (disabled by default). Enable it in `team.yaml`:

```yaml
session_reuse:
  enabled: true
  ttl_seconds: 300      # max idle time before a session is cleared (default 300)
  max_tasks: 3           # max tasks per session before forced clear (default 3)
  max_tokens: 100000     # cumulative input+output tokens before forced clear (default 100000)
  same_role_only: true   # only reuse when consecutive tasks share the same role (default true)
```

A session is reused when **all** of the following hold:

1. `enabled` is `true`
2. The worker has an existing session ID
3. Time since the last task completed is within `ttl_seconds`
4. The number of tasks run in this session is below `max_tasks`
5. The cumulative token count is below `max_tokens`

When any condition fails, the session is cleared and a fresh one is started. On reuse, the task prompt is prefixed with a session-break instruction telling the model to ignore prior task context.

The configuration is propagated from `team.yaml` through environment variables to worker containers (see the table below).

## Environment Variables

| Variable | Purpose |
|---|---|
| `MITTENS_STATE_DIR` | WAL and team metadata directory |
| `MITTENS_SESSION_ID` | Unique session ID (`team-<pid>`) |
| `MITTENS_MAX_WORKERS` | Max concurrent workers (default 4) |
| `MITTENS_TEAM_CONFIG` | Path to `team.yaml` inside the container |
| `MITTENS_BROKER_PORT` | Host broker port |
| `MITTENS_BROKER_TOKEN` | Auth token for the broker |
| `MITTENS_LEADER_ADDR` | Worker-accessible leader address |
| `MITTENS_TEAM_DIR` | Worker-side team directory mount path, for example `/team` |
| `MITTENS_PLANS_DIR` | Plans directory path for cross-session plan persistence |
| `MITTENS_WORKER_ID` | Worker identifier, set on workers |
| `MITTENS_SESSION_REUSE` | Set to `1` to enable session reuse on workers |
| `MITTENS_SESSION_REUSE_TTL` | Session idle TTL in seconds (default 300) |
| `MITTENS_SESSION_REUSE_MAX_TASKS` | Max tasks per session before forced clear (default 3) |
| `MITTENS_SESSION_REUSE_MAX_TOKENS` | Max cumulative tokens before forced clear (default 100000) |
| `MITTENS_SESSION_REUSE_SAME_ROLE` | Set to `0` to allow cross-role reuse (default enabled) |

## Docker Labels

```text
mittens.pool=<session-id>        # All containers for this session
mittens.role=leader|worker       # Role type
mittens.worker_id=<id>           # Worker identifier
mittens.workspace=<path>         # Host workspace
```

## Key Source Files

| Area | Files |
|---|---|
| CLI & session | `cmd/mittens/team.go`, `cmd/mittens/pool_handlers.go` |
| Host broker | `cmd/mittens/broker.go` for `HostBroker` `/pool/*` endpoints |
| MCP sidecar | `cmd/team-mcp/main.go`, `cmd/team-mcp/tools.go`, `cmd/team-mcp/broker.go`, `cmd/team-mcp/notify.go` |
| Worker adapters | `internal/adapter/adapter.go`, `internal/adapter/claude.go`, `internal/adapter/codex.go`, `internal/adapter/handover.go` |
| Pool state machine | `internal/pool/manager.go`, `internal/pool/types.go`, `internal/pool/wal.go`, `internal/pool/event.go` |
| Pipeline execution | `internal/pool/pipeline.go` |
| Review system | `internal/pool/review.go` |
| Crash recovery | `internal/pool/recovery.go`, `internal/pool/queue.go`, `internal/pool/reaper.go` |
| Model routing | `internal/pool/router.go` |
| Plan persistence | `internal/pool/plan.go` |
| Container init | `cmd/mittens-init/phase2.go` via `setupTeamMCP` |
| Prompts and skills | `cmd/mittens/team/prompt.go` |

## Design Notes

- Workers run in `--print` mode, so no DinD is needed.
- Workers do not share a filesystem; coordination flows through `PoolManager` and the WAL.
- Workers are stateless; durable state stays in `PoolManager`.
- Workers run with `cap-drop ALL`, minimal capabilities, and `no-new-privileges`.
- `TaskHandover` carries summary, key decisions, files changed, and open questions between stages.
- Full worker output is stored as side files under `<stateDir>/outputs/` to avoid WAL bloat and is retrievable through `get_task_output` with a 1 MB truncation limit.
- `/complete` and `/fail` are signal endpoints only; rich results and handover data flow through per-worker directories under `<stateDir>/workers/<wid>/`.
- The plans directory at `~/.mittens/projects/<projDir>/plans/` persists across sessions and uses atomic writes with file-based locking.
- Session IDs use `team-<pid>-<base36_epoch>` to avoid PID reuse collisions; `--name` enables human-readable session names.
- Pool state directories are project-scoped under `~/.mittens/projects/<projDir>/pools/` and are auto-pruned on startup by keeping the 20 newest and pruning entries older than 7 days.
