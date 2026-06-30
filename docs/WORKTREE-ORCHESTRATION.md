# Worktree orchestration for programmatic consumers

This guide explains how an orchestration tool should drive mittens' explicit
worktree support to launch many isolated agent runs with deterministic worktree
locations, branch handling, and machine-readable output.

mittens creates the worktrees, puts work on a real branch in the source repo, and
reports exactly what happened in a JSON manifest — so callers do not clone repos
manually, do not guess where work landed, and do not fetch committed branches
back.

## TL;DR

The run's `--policy` file **must enable worktree mode** (`execution.worktree:
true`); `--worktree` is policy-shaped and not a launch flag. mittens must also be
launched with its working directory set to the **primary repo** (see [Primary
workspace requirement](#primary-workspace-requirement)).

```bash
# cwd MUST be the primary repo, e.g.:
cd /Users/me/src/Quix.Portal.Frontend

# /path/to/run/policy.yaml MUST contain:
#   execution:
#     worktree: true

mittens \
  --headless \
  --report-progress \
  --policy /path/to/run/policy.yaml \
  --worktree-root /path/to/run/worktrees \
  --worktree-branch feature/123-story-title \
  --worktree-manifest /path/to/run/worktrees.json \
  --worktree-cleanup keep \
  --name sm-implement-sc-123 \
  -- <provider args>
```

After the run, read `worktrees.json` to learn which repos were available, where
the agent worked, and which branches/commits changed.

## Enabling worktree mode

`--worktree` is **policy-shaped** in mittens and is not accepted as a launch flag.
Enable worktree mode in the run's policy file instead — which orchestrators
already write per run and pass with `--policy`:

```yaml
# /path/to/run/policy.yaml
provider:
  name: claude
execution:
  worktree: true        # required for the --worktree-* flags below
  yolo: true
  headless: true
workspace:
  mounts:                                       # list of {path, access} objects
    - path: /Users/me/src/Quix.Portal.Backend   # extra repo, also gets a worktree
      access: rw
    - path: /Users/me/src/adminui               # extra repo, also gets a worktree
      access: rw
```

Each mount is an object with `path` and `access` (`rw` or `ro`) — those are the
only valid `access` values; the policy is rejected otherwise. The primary
workspace is the repo mittens is launched from (its git root); each mount that is
a git repo also gets its own worktree.

`access` matters for write-back: an `ro` mount is bind-mounted **read-only inside
the container**, so even though mittens still creates the worktree on the host,
the agent cannot edit files or commit in it. Keep every repo the agent must change
— and the run output directory it writes `result.json` to — at `access: rw`. Use
`ro` only for reference repos the agent should read but not modify.

The four `--worktree-*` flags are **per-run orchestration knobs** (transient
operational state, like `--name` and `--policy`). They require worktree mode and
error out with a clear message if it is not enabled.

## Primary workspace requirement

The **primary workspace is the git root of mittens' current working directory** —
not a flag. This has a concrete implication for orchestrators: you cannot keep
launching mittens from a neutral run directory like `runs/implement-sc-123/` and
expect a primary worktree. You must:

1. **Launch mittens with `cwd` set to the selected primary repo** (e.g.
   `/Users/me/src/Quix.Portal.Frontend`). That repo becomes the primary workspace
   and gets the primary worktree (`primary: true` in the manifest).
2. **Mount the run directory separately** for I/O the agent needs — task spec,
   `result.json`, scratch — via a `workspace.mounts` entry in the policy, since
   the run dir is no longer the cwd:

   ```yaml
   execution:
     worktree: true
   workspace:
     mounts:
       - path: /runs/sc-123                       # run I/O: task.md, result.json
         access: rw                               # must be rw so the agent can write
       - path: /Users/me/src/Quix.Portal.Backend  # extra repo (gets its own worktree)
         access: rw
   ```

3. **Tell the agent where to write outputs** using the run-dir mount path, e.g.
   "write your result JSON to `/runs/sc-123/result.json`", and where the task
   spec lives ("read `/runs/sc-123/task.md`").

Note that the run dir mount is *not* a git repo, so it gets no worktree — it is a
plain read-write mount, exactly what you want for output files. Only git repos
among the mounts get worktrees.

## Which flags to pass

| Flag | Purpose | Default without it |
| --- | --- | --- |
| `--worktree-root PATH` | Place all worktrees under `PATH` | Sibling `<repo>.wt-<pid>` dirs |
| `--worktree-branch NAME` | Create/checkout `NAME` in each worktree | Detached HEAD |
| `--worktree-manifest PATH` | Write a JSON manifest | No manifest |
| `--worktree-cleanup MODE` | `keep` \| `keep-dirty` | `keep-dirty` |

## How worktree paths are chosen

With `--worktree-root /runs/sc-123/worktrees`, each worktree is created under that
root using the source repo's base name:

```text
/Users/me/src/Quix.Portal.Frontend  ->  /runs/sc-123/worktrees/Quix.Portal.Frontend
/Users/me/src/Quix.Portal.Backend   ->  /runs/sc-123/worktrees/Quix.Portal.Backend
/Users/me/src/adminui               ->  /runs/sc-123/worktrees/adminui
```

If two source repos share a base name, the second one gets a short hash derived
from its original path appended, so paths stay unique and stable across runs:

```text
/runs/sc-123/worktrees/Quix.Portal.Frontend
/runs/sc-123/worktrees/Quix.Portal.Frontend-4f3a91
```

The root directory is created if it does not exist. Without `--worktree-root`,
mittens keeps the legacy sibling placement (`<repo>.wt-<pid>`).

The worktree is mounted into the container at the **same absolute path** it has
on the host (identity mount), so `mountPath` in the manifest equals the host
worktree path. Tell the agent to work in those mounted paths and not to clone
anything itself.

## How branch creation works

With `--worktree-branch feature/123-story-title`:

- If the branch **does not exist** in the source repo, mittens creates it from the
  source repo's current `HEAD` (`git worktree add -b NAME <path> HEAD`).
- If the branch **exists**, mittens checks it out into the worktree
  (`git worktree add <path> NAME`).
- If the branch is **already checked out** in another worktree, git fails with a
  clear error and mittens surfaces it.
- The **same branch name** is applied to the primary workspace and every extra
  repo worktree.

Because the branch lives in the source repo's git metadata (worktrees share the
repo's `.git`), the agent's commits are visible in the original local repo
immediately after the run — **no fetch-back step is required**. Mittens never
pushes to a remote and never opens pull requests; that remains the orchestrator's
responsibility.

Without `--worktree-branch`, worktrees use a detached HEAD (the existing
behavior); the manifest reports an empty `branch` for those.

### Retry / requeue strategy

The "already checked out in another worktree" failure becomes **common in
retry/requeue flows** when you combine `--worktree-cleanup keep` with reusing the
same branch name: the previous attempt's worktree still holds the branch, so the
next attempt's `git worktree add <branch>` fails.

mittens does not auto-reuse or auto-evict an existing worktree — that policy
belongs to the orchestrator. Pick one of:

- **Tear down the previous run first (recommended).** Before relaunching, remove
  the prior worktrees so the branch is free. Per repo:

  ```bash
  # for each entry in the previous run's manifest:
  git -C "$repo" worktree remove --force "$prev_worktree"
  ```

  `git worktree remove` detaches the worktree and frees the branch; the branch and
  its commits remain in the repo. Then relaunch with the same branch name and
  mittens will check it out fresh.

- **Use a per-attempt branch name** (e.g. `feature/123-story-title.attempt-2`).
  Avoids the conflict entirely but spreads work across branches you must
  reconcile afterward.

- **Use a per-attempt run dir + `--worktree-root`** and let the prior worktree be
  garbage-collected with its run dir, but still call `git worktree remove` (or
  `git worktree prune`) so the repo's worktree registry doesn't keep stale entries
  that hold the branch.

Whichever you choose, the manifest from the prior run is the source of truth for
which worktrees/branches exist to clean up.

## How to read the manifest

`--worktree-manifest PATH` writes JSON like:

```json
{
  "worktrees": [
    {
      "repo": "/Users/me/src/Quix.Portal.Frontend",
      "worktree": "/runs/sc-123/worktrees/Quix.Portal.Frontend",
      "mountPath": "/runs/sc-123/worktrees/Quix.Portal.Frontend",
      "branch": "feature/123-story-title",
      "startHead": "abc123",
      "endHead": "def456",
      "dirty": false,
      "kept": true,
      "primary": true
    },
    {
      "repo": "/Users/me/src/Quix.Portal.Backend",
      "worktree": "/runs/sc-123/worktrees/Quix.Portal.Backend",
      "mountPath": "/runs/sc-123/worktrees/Quix.Portal.Backend",
      "branch": "feature/123-story-title",
      "startHead": "123abc",
      "endHead": "123abc",
      "dirty": false,
      "kept": false,
      "primary": false
    }
  ]
}
```

Field reference:

| Field | Meaning |
| --- | --- |
| `repo` | Original source repo root |
| `worktree` | Worktree directory on the host |
| `mountPath` | Path the worktree is mounted at in the container (identity mount) |
| `branch` | Branch name; empty string means detached HEAD |
| `startHead` | Commit SHA at worktree creation |
| `endHead` | Commit SHA at cleanup time (empty if it could not be read) |
| `dirty` | Whether the working tree had uncommitted changes at cleanup |
| `kept` | `true` if mittens left the worktree on disk, `false` if it removed it |
| `primary` | `true` for the primary workspace, `false` for extra-directory repos |

To detect work: a repo changed if `endHead != startHead` (new commits) or
`dirty == true`. To find committed work to merge or PR, use `branch` + `endHead`
in the corresponding `repo`.

**The manifest is essential for multi-repo runs.** `--worktree-branch` applies the
same branch name to *every* repo worktree, but a given story rarely touches all of
them. The branch will exist in every mounted repo (created at its `startHead`),
yet only the repos where `endHead != startHead` actually changed. **Do not assume
every repo with the branch has work** — filter on `endHead != startHead` (or
`dirty`) from the manifest before opening PRs or merging, or you will create empty
branches/PRs in untouched repos.

**Timing guarantees.** The manifest is written during cleanup, which runs even
when the agent exits non-zero — so it is produced on successful runs, failed
agent runs, and any path where mittens reached worktree setup. It is **not**
written if no worktree was created (worktree setup never ran) or if
`--worktree-manifest` was omitted.

## How cleanup behavior affects callers

`--worktree-cleanup` controls whether mittens removes worktrees on exit:

| Mode | Behavior |
| --- | --- |
| `keep-dirty` (default) | Remove a worktree only if it is clean and still at `startHead`; keep anything dirty or with new commits. |
| `keep` | Keep all worktrees regardless of state. |

For orchestrators, **`keep` is usually the right choice**: the caller owns the run
directory (`/runs/sc-123/`) and will garbage-collect it as a whole, so it wants
predictable, always-present artifacts to inspect. Removing a worktree never
deletes its branch — the branch stays in the source repo's git metadata — so even
under `keep-dirty` removal, committed work is preserved.

Remember the interaction with retries: `keep` leaves the worktree (and its hold on
the branch) in place, so a requeue that reuses the branch name must explicitly
`git worktree remove` the prior worktree first. See [Retry / requeue
strategy](#retry--requeue-strategy).

The `kept` field in the manifest tells you, per worktree, whether the directory is
still on disk after the run.

## Example: multi-repo agent run

```bash
RUN=/runs/sc-123
mkdir -p "$RUN"

cat > "$RUN/task.md" <<'MD'
Add environment guards to the portal config loader. ...
MD

# The run dir is a plain RW mount (not a git repo -> no worktree); the extra
# repos are git repos and each gets its own worktree on the branch.
cat > "$RUN/policy.yaml" <<YAML
provider:
  name: claude
execution:
  worktree: true
  yolo: true
  headless: true
workspace:
  mounts:
    - path: $RUN                              # run I/O: task.md, result.json
      access: rw
    - path: /Users/me/src/Quix.Portal.Backend # extra repo (gets a worktree)
      access: rw
    - path: /Users/me/src/adminui             # extra repo (gets a worktree)
      access: rw
YAML

# Launch FROM the primary repo so its git root is the primary workspace.
# (Do not launch from $RUN — it is not a repo and would have no primary worktree.)
cd /Users/me/src/Quix.Portal.Frontend

mittens \
  --headless \
  --report-progress \
  --policy "$RUN/policy.yaml" \
  --worktree-root "$RUN/worktrees" \
  --worktree-branch feature/123-add-env-guards \
  --worktree-manifest "$RUN/worktrees.json" \
  --worktree-cleanup keep \
  --name sm-implement-sc-123 \
  -- -p "Read the task from $RUN/task.md. Work only in the mounted repo worktree
        paths mittens created under $RUN/worktrees. Do not clone repositories.
        Commit your changes on the already-checked-out branch in each repo.
        Write a summary to $RUN/result.json."

# After the run — list only repos that actually changed (NOT every repo with the
# branch; the same branch was created in all of them):
jq -r '.worktrees[] | select(.endHead != .startHead or .dirty)
       | "\(.repo)\t\(.branch)\t\(.endHead)"' "$RUN/worktrees.json"
```

This prints one line per repo that actually changed, with the branch and final
commit — everything the orchestrator needs to open PRs or merge, without guessing
worktree paths from logs.
