# Kitchen Operations

Operator-facing notes for the current Kitchen control plane.

## Runtime Daemon

Recommended operator flow:

```bash
# terminal 1
kitchen serve

# terminal 2
kitchen submit "Add typed parser errors"
```

In this supervised mode, `kitchen serve` owns:

- the Kitchen API
- the scheduler and broker
- one child `mittens daemon` per configured provider

Kitchen stores supervised runtime state under `~/.kitchen/runtime/`,
including provider-scoped PID and socket files. On startup, supervised serve
uses kill-and-replace semantics for stale Kitchen-owned runtime state.

Use `kitchen serve --provider <name>` when you intentionally want a
single-provider supervised session for debugging or restricted testing.

Advanced/debug path:

```bash
MITTENS_RUNTIME_SOCKET=/var/run/m.sock \
MITTENS_POOL_TOKEN=... \
kitchen serve
```

The external-runtime override is explicit: `MITTENS_RUNTIME_SOCKET` plus
`MITTENS_POOL_TOKEN`. This avoids stale ambient runtime metadata silently
replacing the new supervised default.

If no runtime daemon is available, Kitchen runs in offline CLI-only mode:
plan inspection and config commands work, but no workers can be spawned.

## Worker Recycle

Reset a worker's AI session without killing the container:

```bash
# Via Mittens runtime daemon
curl --unix-socket ~/.mittens/runtime.sock \
  -X POST http://runtime/v1/workers/w-1/recycle \
  -H "X-Mittens-Token: $MITTENS_RUNTIME_TOKEN" \
  -H "X-Mittens-Pool-Token: $MITTENS_POOL_TOKEN"
```

Behavior:

- Clears worker metadata and artifacts on the daemon side
- Signals the worker via the broker poll channel
- Worker calls `ForceClean()` on next poll — kills adapter process
- Worker resumes idle polling with a clean session
- Non-interrupting: active tasks are not preempted

## Conflict Retry

When a task's merge-back fails due to git conflicts:

1. Kitchen classifies the failure as `conflict`
2. If the failure policy for `conflict` is `retry_merge`:
   - The failed worker is killed
   - The stale worktree is discarded
   - The same task is re-queued with `RequireFreshWorker`
   - A new worker gets a fresh worktree from the current lineage HEAD
3. If retries are exhausted, the task stays failed for operator intervention

Configure in `config.yaml`:

```yaml
failure_policy:
  conflict:
    action: retry_merge
    max: 3
```

## Provider Health

Track and manage provider availability:

```bash
kitchen provider reset anthropic/opus   # clear health state for a provider/model
```

Via API:

- `GET /v1/providers/health` — health map for all providers
- `POST /v1/providers/{provider}/models/{model}/reset` — clear cooldown/auth state

Health state includes:

- **Cooldown**: temporary unavailability with expiry (e.g., rate limit)
- **Auth failure**: permanent until manually reset (e.g., revoked API key)

The complexity router skips unavailable providers when selecting candidates.

## Timeout Enforcement

Tasks with `timeoutMinutes` configured in the plan are failed by the
scheduler if they remain dispatched past their budget:

- The scheduler checks dispatch times on each reconciliation cycle
- Overdue tasks are classified as `timeout`
- The failure policy for `timeout` determines the action (default: re-plan)

## Snapshot History Windows

Kitchen caps the plan-history timeline embedded in snapshot-style surfaces:

- `kitchen status`
- `GET /v1/events` initial `snapshot` event

This does not affect full reads:

- `kitchen plan PLAN_ID`
- `kitchen evidence PLAN_ID`
- `kitchen history PLAN_ID`
- `GET /v1/plans/{id}`
- `GET /v1/plans/{id}/evidence`
- `GET /v1/plans/{id}/history`

Those continue to return the full persisted plan history.

### Config

```yaml
snapshots:
  planHistoryLimit: 8
```

Behavior:

- Positive value: include up to that many most-recent history entries
- `0`: omit embedded history from snapshots entirely
- Omitted: use Kitchen's default

### One-Off Overrides

CLI:

```bash
kitchen status --history-limit 2
```

API:

```http
GET /v1/events?historyLimit=2
```

Override values:

- Positive value: use that snapshot history window
- `0`: suppress embedded history
- `-1` or omitted: use the configured Kitchen default

## Worktree Cleanup

Remove orphaned worktrees from completed or failed lineages:

```bash
kitchen clean
```

This scans `~/.kitchen/worktrees/`, removes directories not corresponding to
active tasks, and runs `git worktree prune`.
