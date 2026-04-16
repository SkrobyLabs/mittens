# Kitchen Reference

Current reference for the implemented Kitchen CLI and HTTP API.

## Concepts

- `idea`: operator-submitted planning request
- `plan`: persisted planning/execution record with tasks, dependencies, and complexity ratings
- `lineage`: long-lived branch family for one approved plan (`kitchen/<name>/lineage`)
- `history`: persisted planning/review timeline for a plan
- `snapshot`: lightweight queue/worker/open-plan view used by `status` and SSE
- `evidence`: plan context bundle available in compact or rich tiers
- `recycle`: reset a worker's AI adapter session without killing the container

## Config

Kitchen reads config from `$KITCHEN_HOME/config.yaml`.

Current top-level sections:

```yaml
routing:
  trivial:
    prefer:
      - provider: anthropic
        model: haiku
roleDefaults:
  reviewer:
    prefer:
      - provider: openai
        model: gpt-5.4
roleRouting:
  reviewer:
    high:
      prefer:
        - provider: anthropic
          model: opus
councilSeats:
  A:
    default:
      prefer:
        - provider: anthropic
          model: sonnet
concurrency:
  maxActiveLineages: 5
  maxPlanningWorkers: 2
  maxWorkersTotal: 12
  maxWorkersPerPool: 6
  maxWorkersPerLineage: 4
  maxIdlePerPool: 2
  councilSeatIdleTTLSeconds: 270
failure_policy:
  auth:
    action: try_next_provider
    cooldown: 60s
snapshots:
  planHistoryLimit: 8
```

### `roleDefaults`

Optional `map[string]RoutingRule`. Per-role default routing fallback. When a task has a role (e.g. `reviewer`, `implementer`) and no complexity-specific override exists in `roleRouting`, this provides the fallback routing rule for all complexity tiers. The role name `"default"` is reserved; use `routing` for the global default.

```yaml
roleDefaults:
  implementer:
    prefer:
      - provider: anthropic
        model: opus
  reviewer:
    prefer:
      - provider: openai
        model: gpt-5.4
```

### `roleRouting`

Optional `map[string]map[Complexity]RoutingRule`. Per-role, per-complexity routing overrides. Allows different models for different roles at specific complexity tiers. Overrides `roleDefaults` for the specified role+complexity pairs. The role name `"default"` is reserved. Every referenced `prefer` list must be non-empty.

```yaml
roleRouting:
  reviewer:
    high:
      prefer:
        - provider: anthropic
          model: opus
    critical:
      prefer:
        - provider: anthropic
          model: opus
```

### `councilSeats`

Optional `map[string]CouncilSeatRoutingConfig`. Routing config for planner council seats. Only seat names `"A"` and `"B"` are valid. Each seat config has an optional `default` (RoutingRule applied to all complexity tiers for that seat) and optional `routing` (per-complexity overrides for that seat). Every referenced `prefer` list must be non-empty.

```yaml
councilSeats:
  A:
    default:
      prefer:
        - provider: anthropic
          model: sonnet
  B:
    default:
      prefer:
        - provider: openai
          model: gpt-5.4
    routing:
      high:
        prefer:
          - provider: anthropic
            model: opus
```

**Routing resolution order** (most specific wins): `councilSeats` → `roleRouting` → `roleDefaults` → `routing`.

### `concurrency`

`councilSeatIdleTTLSeconds` (default `270`) controls how long an idle council-seat worker is kept alive before being reaped. All other concurrency fields must be positive; `maxIdlePerPool` may be zero.

For snapshot-history behavior and overrides, see [Operations](./operations.md).

## CLI

### Planning

```bash
kitchen submit [--lineage LINEAGE] [--auto] [--impl-review] [--file PATH|-] [IDEA]
kitchen research [--file PATH|-] [TOPIC]
kitchen promote PLAN_ID [--lineage LINEAGE] [--auto] [--impl-review|--no-impl-review]
```

`submit` starts the full council planning flow.

`research` spawns a read-only researcher worker to investigate a topic and produce a structured report. The resulting plan enters `research_complete` state. Use `promote` to convert it into an implementation plan.

`promote` creates a new council-planned implementation from a completed research plan. The `--impl-review` flag defaults to true; pass `--no-impl-review` to skip post-implementation review.

Input modes (shared by `submit` and `research`):

- inline argument
- `--file PATH`
- `--file -` or piped stdin
- `$VISUAL` / `$EDITOR` fallback when no argument is provided

Examples:

```bash
kitchen submit "Add typed parser errors"
kitchen submit --lineage parser-errors --impl-review "Add typed parser errors"
kitchen submit --file docs/kitchen/design.md
cat notes.md | kitchen submit --lineage tui
kitchen research "How does the router select a provider?"
kitchen promote PLAN_ID --lineage parser-errors
```

### Plan inspection

```bash
kitchen plans [--completed]
kitchen plan PLAN_ID
kitchen evidence [--compact] PLAN_ID
kitchen history PLAN_ID [--cycle N] [--json]
```

Use:

- `plans` for a compact open-plan list
- `plan` for one full persisted plan detail
- `evidence` for plan plus queue/workers/questions/lineages context
- `evidence --compact` for a lightweight summary (planId, state, phase, task counts); default without flag is rich
- `history` for the persisted planning/review timeline

### Plan control

```bash
kitchen approve PLAN_ID
kitchen reject PLAN_ID
kitchen replan PLAN_ID [--reason TEXT]
kitchen cancel PLAN_ID
kitchen delete PLAN_ID
kitchen review PLAN_ID
kitchen remediate-review PLAN_ID [--include-nits]
kitchen steer PLAN_ID [--file PATH|-] [NOTE]
kitchen steer-implementation PLAN_ID [--file PATH|-] [NOTE]
```

- `review` — trigger a manual implementation review on a completed plan
- `remediate-review` — queue a follow-up remediation task from a passed review; `--include-nits` includes minor findings
- `steer` — append directional guidance to an in-progress planning council
- `steer-implementation` — queue lightweight implementation guidance on the existing lineage without a full replan

### Questions

```bash
kitchen questions
kitchen answer QUESTION_ID ANSWER
kitchen unhelpful QUESTION_ID
```

### Status and operations

```bash
kitchen status [--history-limit N]
kitchen config [--paths]
kitchen capabilities [--cli]
kitchen lineages
kitchen merge-check LINEAGE
kitchen merge [--no-commit] LINEAGE
kitchen reapply LINEAGE
kitchen fix-merge LINEAGE
kitchen retry TASK_ID [--same-worker]
kitchen clean
kitchen provider reset PROVIDER/MODEL
```

Notes:

- when a matching `kitchen serve` is running for the current repo, live control-plane commands route through its HTTP API automatically
- when no running server is detected, those same commands fall back to direct local state mutation
- `status` returns JSON and embeds open-plan snapshot history
- `config` returns the effective Kitchen config plus resolved paths
- `capabilities` returns machine-readable feature metadata for automation
- `merge [--no-commit] LINEAGE` squash-merges the lineage into its base; `--no-commit` previews without moving the base branch
- `reapply LINEAGE` merges the base branch into the lineage to absorb upstream changes
- `fix-merge` queues a worker to resolve lineage→base merge conflicts
- `retry` re-queues a failed task; `--same-worker` allows reuse of an existing idle worker
- `clean` removes orphaned completed worktrees

### Interactive and tooling

```bash
kitchen tui
kitchen configure
kitchen mittens [mittens args...]
```

Notes:

- `tui` launches the interactive terminal UI (also the default when running `kitchen` with no subcommand)
- `configure` starts an interactive setup wizard for provider and model routing
- `mittens` runs the mittens binary with `--dir $KITCHEN_HOME` injected; all additional flags are forwarded

### Runtime

```bash
kitchen serve [--provider PROVIDER] [--addr HOST:PORT] [--token TOKEN] [--broker-addr HOST:PORT] [--broker-token TOKEN] [--advertise-addr HOST:PORT]
```

Defaults:

- API addr: `127.0.0.1:7681`
- broker addr: `127.0.0.1:7682`
- API token from `KITCHEN_API_TOKEN`
- broker token from `KITCHEN_BROKER_TOKEN`

`serve` starts:

- the Kitchen HTTP API
- the worker broker
- the scheduler loop

Recommended operator flow:

- `kitchen serve`
- then submit and operate from another terminal with `kitchen submit`, `kitchen status`, `kitchen approve`, and `kitchen merge`

By default, `serve` starts one supervised Mittens runtime daemon per unique
provider referenced by the effective Kitchen routing config and injects a
runtime multiplexer into Kitchen. This is the recommended simple path.

When `--provider` is set, `serve` restricts supervised startup to that one
provider.

When `MITTENS_RUNTIME_SOCKET` and `MITTENS_POOL_TOKEN` are explicitly set,
`serve` uses that external runtime instead of supervising child daemons.
That manual-daemon mode remains useful for debugging and lower-level runtime
work.

`serve` also writes project-scoped server metadata to the current Kitchen project state so later CLI invocations for the same repo can discover and use the running control plane. The metadata includes:

- API URL
- optional API token
- PID
- repo path
- timestamp

### Misc

```bash
kitchen version
```

## HTTP API

If `kitchen serve` is started with a token, clients can authenticate with:

- `X-Kitchen-Token: <token>`
- `Authorization: Bearer <token>`

### Ideas, quick plans, and research

`POST /v1/ideas` — start a full council planning flow

Request:

```json
{
  "idea": "Add typed parser errors",
  "lineage": "parser-errors",
  "auto": false,
  "implReview": false
}
```

`POST /v1/quick` — create a single-task plan that activates immediately, bypassing the council. Auto-retries on failure.

Request:

```json
{
  "prompt": "Fix the failing lint check",
  "title": "Fix lint",
  "lineage": "fix-lint",
  "complexity": "low",
  "maxRetries": 3,
  "dependsOn": ["plan_abc123"]
}
```

`POST /v1/research` — spawn a read-only researcher worker to investigate a topic. Plan enters `research_complete` when done.

Request:

```json
{ "topic": "How does the complexity router select a provider?" }
```

`POST /v1/plans/{id}/promote` — promote a `research_complete` plan into a full council-planned implementation.

Request:

```json
{ "lineage": "my-feature", "auto": false, "implReview": true }
```

`POST /v1/plans/{id}/refine-research` — queue a follow-up research turn with a clarification note.

Request:

```json
{ "clarification": "Focus specifically on the fallback path" }
```

### Plans

- `GET /v1/plans`
- `GET /v1/plans/{id}`
- `GET /v1/plans/{id}/history`
- `GET /v1/plans/{id}/history?cycle=2`
- `GET /v1/plans/{id}/evidence` or `GET /v1/plans/{id}/evidence?tier=compact|rich`
- `POST /v1/plans/{id}/approve`
- `POST /v1/plans/{id}/reject`
- `POST /v1/plans/{id}/replan`
- `POST /v1/plans/{id}/review` — trigger a manual implementation review
- `POST /v1/plans/{id}/remediate-review` — queue a remediation task from a passed review; optional body `{"includeNits": true}`
- `POST /v1/plans/{id}/steer` — append directional guidance to an in-progress planning council; body `{"note": "..."}`
- `POST /v1/plans/{id}/steer-implementation` — queue lightweight implementation guidance without a full replan; body `{"note": "..."}`
- `POST /v1/plans/{id}/affinity/invalidate`
- `POST /v1/plans/{id}/extend`
- `DELETE /v1/plans/{id}` — cancel plan
- `DELETE /v1/plans/{id}/purge` — permanently delete plan and tasks
- `DELETE /v1/plans/{id}/purge-with-lineage` — permanently delete plan, tasks, and lineage branch

### Tasks

- `GET /v1/tasks/{id}/activity` — task activity transcript
- `GET /v1/tasks/{id}/output` — task output
- `POST /v1/tasks/{id}/retry` — re-queue a failed task; optional body `{"requireFreshWorker": true}` (default true)
- `POST /v1/tasks/{id}/fix-conflicts` — queue a conflict-resolution worker for a failed task
- `DELETE /v1/tasks/{id}` — cancel a task

### Questions

- `GET /v1/questions`
- `POST /v1/questions/{id}/answer`
- `POST /v1/questions/{id}/unhelpful`

### Queue and workers

- `GET /v1/status`
- `GET /v1/queue`
- `GET /v1/workers`
- `GET /v1/meta`

`GET /v1/status` returns the same authoritative snapshot shape used by `kitchen status`, including queue, workers, runtime activity, plans, snapshot policy, lineages, providers, and generation timestamp.

`GET /v1/meta` returns:

- build metadata (`version`, `commit`, `date`)
- effective Kitchen config
- resolved Kitchen and repo paths
- machine-readable capability metadata

Capability metadata includes:

- schema-level compatibility metadata
- section versions and stability levels
- supported CLI and API surfaces
- option/query support
- selected default values
- enum/value sets for merge and snapshot controls

Compatibility rules:

- additive fields may appear within a schema version
- clients should ignore unknown fields
- breaking machine-readable changes require a `schemaVersion` increment
- section `version` increments indicate added or changed semantics for that section

### Lineages and merges

- `GET /v1/lineages`
- `GET /v1/lineages/{name}/merge-check`
- `POST /v1/lineages/{name}/merge`
- `POST /v1/lineages/{name}/reapply` — merge base branch into lineage to absorb upstream changes
- `POST /v1/lineages/{name}/fix-merge` — queue a worker to resolve lineage→base merge conflicts

Merge request body:

```json
{
  "mode": "direct",
  "noCommit": false
}
```

### Provider health

- `GET /v1/providers/health`
- `POST /v1/providers/{provider}/models/{model}/reset`

### Event stream

`GET /v1/events`

Behavior:

- first event is always `snapshot`
- subsequent events stream live pool and Kitchen lifecycle notifications
- plan lifecycle events include `progress`
- plan lifecycle events also include `historyEntry` when the notification maps
  to a persisted timeline item

Snapshot overrides:

- `GET /v1/events?historyLimit=2`
- `GET /v1/events?historyLimit=0`
- omit or use `-1` to fall back to configured default

Snapshot payload includes:

- `queue`
- `workers`
- `plans`
- `snapshot`

The top-level `snapshot` object reports the applied history policy:

```json
{
  "snapshot": {
    "planHistoryLimit": 2,
    "configuredPlanHistoryLimit": 8,
    "historyLimitOverridden": true
  }
}
```

## Data shape notes

`PlanProgress` in snapshots includes:

- current execution phase and review counters
- active/completed/failed task IDs
- cycle summaries
- bounded embedded history
- history window metadata:
  - `historyTotal`
  - `historyIncluded`
  - `historyTruncated`

Full plan detail and evidence remain the authoritative uncapped reads.

## Mittens RuntimeAPI

Kitchen communicates with the Mittens daemon over a Unix socket. The daemon
exposes these endpoints:

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/v1/workers` | Spawn worker container |
| DELETE | `/v1/workers/{id}` | Kill worker |
| GET | `/v1/workers` | List workers |
| GET | `/v1/workers/{id}` | Worker status |
| POST | `/v1/workers/{id}/recycle` | Reset worker session |
| GET | `/v1/workers/{id}/activity` | Worker activity |
| POST | `/v1/workers/{id}/assignments` | Submit work (persist-only stub) |
| GET | `/v1/events` | SSE runtime events |

For manually started external daemons, Kitchen discovers the runtime via:

1. `MITTENS_RUNTIME_SOCKET` env var (highest priority)
2. `~/.mittens/runtime.json` metadata file (autodiscovery)
3. If neither: Kitchen runs in offline CLI-only mode (`hostAPI = nil`)

When plain `kitchen serve` is used, Kitchen supervises one child daemon per
configured provider, captures their runtime tokens, and injects a runtime
multiplexer directly into the active Kitchen process.

When `kitchen serve --provider <name>` is used, the same supervised path is
used but restricted to one provider.

Runtime events from the daemon are forwarded into Kitchen's own notification
system and SSE stream.

## Evidence Tiering

`GET /v1/plans/{id}/evidence` supports a `tier` query parameter:

**Compact** (`?tier=compact`):

```json
{
  "planId": "plan_abc",
  "lineage": "parser-errors",
  "state": "active",
  "phase": "executing",
  "anchorCommit": "abc1234",
  "baseBranch": "main",
  "currentHead": "def5678",
  "commitsSinceAnchor": 3,
  "taskCounts": { "active": 2, "completed": 1, "failed": 0, "pending": 1 }
}
```

**Rich** (`?tier=rich`, default): full bundle with plan, execution, affinity,
progress, history, tasks, questions, queue, workers, lineages, and runtime
activity.

## Failure Classification

Kitchen classifies task failures into 8 classes. Each class maps to a
configurable policy action:

| Class | Default Action |
|-------|---------------|
| `capability` | Escalate complexity |
| `plan` | Re-plan |
| `environment` | Retry same complexity |
| `conflict` | Retry from new lineage HEAD (`retry_merge`) |
| `auth` | Cooldown, try next provider |
| `timeout` | Re-plan as subtasks |
| `infrastructure` | Respawn worker |
| `unknown` | Retry |

Configure in `config.yaml`:

```yaml
failure_policy:
  conflict:
    action: retry_merge
    max: 3
  auth:
    action: try_next_provider
    cooldown: 60s
```

## Git Branch Layout

```
kitchen/<lineage>/lineage           # lineage branch
kitchen/<lineage>/tasks/<taskId>    # child task branch (temporary)
```

Worktrees created at `~/.kitchen/worktrees/<lineage>/<taskId>/`.

## Current boundaries

- Planning and plan review run through real worker tasks
- Planner/reviewer refinement is bounded, not open-ended
- Snapshot-embedded history is intentionally capped
- Merge conflicts are detected, classified, and retried automatically
- Timeout enforcement reads budgets from pool tasks directly
- Worker recycle operates via broker poll (non-interrupting)
- Assignments endpoint persists data but workers do not consume it
- Primary interface is the headless CLI/API; an interactive TUI is available via `kitchen tui`
