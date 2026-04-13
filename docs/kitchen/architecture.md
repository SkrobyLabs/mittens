# Kitchen Architecture

## Product Split

Kitchen and Mittens are separate binaries with distinct responsibilities:

**Mittens** owns container lifecycle: Docker build/run, credential
forwarding, firewall/extension policy, runtime daemon API.

**Kitchen** owns orchestration: planning, scheduling, retries, complexity
routing, git workflow, operator API.

## Subsystems

### Scheduler

The scheduler runs a continuous event loop consuming notifications from the
pool manager. On each cycle it:

1. Refreshes pending worker spawns
2. Dispatches queued tasks to idle workers
3. Spawns new workers for excess queued tasks
4. Enforces task timeouts
5. Reconciles container state with the runtime daemon

The scheduler is deterministic — no AI logic, no probabilistic decisions.

### Planner

Planners are regular workers that receive an idea prompt and produce a
structured plan artifact. The plan contains tasks with IDs, prompts,
complexity ratings, dependencies, and optional metadata (outputs, success
criteria, review complexity).

Planners are stateless between runs except for affinity metadata tracked by
Kitchen. Multiple planners can run concurrently on different lineages.

### Council Planning

Planning uses a multi-seat council model. Each plan starts with a council of
two seats (A and B) running over multiple turns (default 4). Seats take
different roles in the discussion, with each turn building on the previous.
`POST /v1/plans/{id}/extend` can grant additional turns for complex plans.
Council seat routing is configured via `councilSeats` in the Kitchen config.

### Implementation Review

When `--impl-review` is passed to `kitchen submit`, a post-implementation
adversarial review is requested. After all implementation tasks complete, a
reviewer worker critiques the result before the plan reaches the `completed`
state. The plan enters `implementation_review` state during this phase.

### Plan Store

Plans are persisted as JSON files under `~/.kitchen/projects/<repo>/plans/`:

```
plans/<planId>/
  plan.json        # plan record (tasks, anchor, lineage, ownership)
  execution.json   # execution state (active/completed/failed tasks, history)
  affinity.json    # worker affinity preferences
```

All writes are atomic (temp file + rename).

### Lineage Manager

Lineages are named branch families. Each lineage has at most one active plan.
State is tracked under `~/.kitchen/projects/<repo>/lineages/<name>/`.

### Complexity Router

Maps task complexity (trivial, low, medium, high, critical) to ordered
provider/model preferences. Filters by provider health before returning
candidates. Supports escalation to higher complexity on capability failure.

The router is role-aware: each task can carry a role (e.g. `planner`,
`implementer`, `reviewer`). Role-specific routing is resolved via
`roleRouting` (per-role, per-complexity overrides) and `roleDefaults`
(per-role fallback for all complexity tiers), layered on top of the
global `routing` table. Council seats A and B have their own routing
layer (`councilSeats`) resolved on top of the planner role routing.

Resolution order (most specific wins): `councilSeats` → `roleRouting` → `roleDefaults` → `routing`.

### Provider Health

Tracks per-provider/model health with cooldown timers and permanent auth
failure flags. Persisted at `~/.kitchen/state/provider_health.json`. The
router consults health before including a provider in the candidate list.

### Failure Classifier

Classifies task failures into 8 classes:

| Class | Signal | Default action |
|-------|--------|----------------|
| `capability` | Model output insufficient | Escalate complexity |
| `plan` | Prompt/plan stale or wrong | Re-plan |
| `environment` | Build/test/tooling issue | Retry same complexity |
| `conflict` | Git merge conflict | Retry from new lineage HEAD |
| `auth` | Provider 401/429/quota | Cooldown, try next provider |
| `timeout` | Task exceeded time budget | Re-plan as subtasks |
| `infrastructure` | Container/heartbeat/OOM failure | Respawn worker |
| `unknown` | Unclassified | Retry |

Classification priority: Kitchen-detected signals first, then worker-reported
class, then message heuristics, then fallback to `unknown`.

### Conflict Retry

When a task fails with `FailureConflict` and the failure policy is
`retry_merge`:

1. Kill the failed worker
2. Discard the stale child worktree
3. Revive the same task ID back to `queued` (preserves dependency graph)
4. Record a `conflict_retried` plan history entry
5. Spawn a fresh worker with a new child worktree from current lineage HEAD

The task's `RetryCount` is incremented. Retry is bounded by the policy's
`max` field. If exhausted, the task stays failed for operator intervention.

## Runtime Split

**Runtime** — long-lived host-side process (Mittens daemon). Owns container
lifecycle, credential state, image cache.

**Session** — per-container worker session. Ephemeral.

### Mittens Daemon

The daemon listens on a Unix socket and exposes 8 RuntimeAPI endpoints:

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

Recommended operator mode is supervised serve:

- plain `kitchen serve` starts one child Mittens daemon per configured provider
- Kitchen injects a `runtimeMux` into its `PoolManager`
- `runtimeMux` routes spawn/kill/recycle/activity/assignment calls by provider
- `runtimeMux` fans in container reconciliation and runtime events from all supervised daemons
- Kitchen also supervises provider-scoped PID/socket files under
  `~/.kitchen/runtime/`

`kitchen serve --provider <name>` remains the single-provider override.

Manual `mittens daemon` startup remains the advanced/debug path. In that
mode, Kitchen uses the explicit env override path via
`MITTENS_RUNTIME_SOCKET` and `MITTENS_POOL_TOKEN`.

Worker provider is also persisted in the pool WAL so idle-worker reuse stays
provider-correct after restart and during multi-runtime scheduling.

### Worker Recycle

Recycle resets a worker's AI adapter session without killing the container:

1. Daemon receives `POST /v1/workers/{id}/recycle`
2. Daemon clears worker metadata, emits `worker_recycled` runtime event
3. Kitchen receives event, sets recycle flag on the worker
4. Worker's next broker poll returns `{"recycle": true}`
5. Worker calls `adapter.ForceClean()`, resets role state, resumes polling

Recycle is asynchronous and non-interrupting — active tasks are never
preempted.

## Git Workflow

### Branch Model

```
main
 └── kitchen/<lineage>/lineage          # lineage branch
      ├── kitchen/<lineage>/tasks/t-1   # child task branch
      ├── kitchen/<lineage>/tasks/t-2
      └── kitchen/<lineage>/tasks/t-3
```

### Worktree Lifecycle

1. Kitchen acquires per-repo mutex
2. Creates child branch from lineage HEAD
3. Creates host worktree at `~/.kitchen/worktrees/<lineage>/<taskId>/`
4. Releases mutex
5. Spawns Mittens worker with the worktree as workspace
6. Worker operates directly in the mounted worktree

### Merge-Back

On task completion:

1. Acquire per-lineage merge lock
2. Try fast-forward merge of child into lineage branch
3. If conflicts: classify as `FailureConflict`, discard child, trigger retry
4. If clean: update lineage branch, discard child worktree

### Orphan Cleanup

On Kitchen restart or `kitchen clean`:

1. Scan `~/.kitchen/worktrees/`
2. Remove worktrees not corresponding to active tasks
3. Run `git worktree prune` under repo mutex

## Evidence Tiering

The evidence endpoint supports two tiers:

- **Compact** (`?tier=compact`): planId, lineage, state, phase, anchor,
  task counts — for status polling
- **Rich** (`?tier=rich`, default): full bundle with plan, execution,
  affinity, progress, history, tasks, questions, queue, workers, lineages,
  runtime activity

CLI: `kitchen evidence PLAN_ID` (rich) or `kitchen evidence --compact PLAN_ID`.

## Plan Approval Gate

Plans are held for operator approval by default:

```
planning → pending_approval → active → [implementation_review →] completed → merged
```

The `--auto` flag on `kitchen submit` skips `pending_approval` for
trusted/low-risk work.

Additional states: `planning_failed`, `rejected`, `reviewing`, `implementation_review_failed`, `closed`.

## Concurrency Limits

```yaml
concurrency:
  maxActiveLineages: 5
  maxPlanningWorkers: 2
  maxWorkersTotal: 12
  maxWorkersPerPool: 6
  maxWorkersPerLineage: 4
  maxIdlePerPool: 2
  councilSeatIdleTTLSeconds: 270
```

One active plan per lineage at a time is enforced. `maxWorkersPerLineage`
is currently configuration only; the scheduler does not yet enforce a
per-lineage worker cap.
