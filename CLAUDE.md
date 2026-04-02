# mittens

Run Claude Code in isolated Docker containers with credential forwarding, network firewall, Docker-in-Docker, and pluggable extensions.

> **Directive**: When you add, rename, move, or delete source files, flags, CLI subcommands, MCP tools, environment variables, Docker labels, or extension manifests, update the relevant documentation in the same change. This includes the Project Structure tree and Core Flags list here, the Teams summary here plus `docs/TEAMS.md`, and any related runtime or extension docs. Do not leave documentation drift for a follow-up.

## Project Structure

```
cmd/
  mittens/                   # main binary source
    main.go                  # CLI entry point (cobra, flag routing)
    app.go                   # core orchestration (ParseFlags, Run, Cleanup)
    broker.go                # HostBroker — host↔container communication (creds, URLs, notifications, OAuth)
    config.go                # per-project config (~/.mittens/projects/...)
    wizard.go                # TUI setup wizard (charmbracelet/huh)
    docker.go                # docker build/run/cp operations
    drop.go                  # DropProxy — stdin PTY proxy for drag-and-drop path translation
    credentials.go           # credential extraction and persistence
    credentials_darwin.go    # macOS Keychain credential source
    credentials_other.go     # non-macOS stub
    terminal_focus.go        # detect terminal and re-focus on notification
    helpers.go               # logging, scriptDir, captureCommand
    embed.go                 # go:embed for extension YAMLs
    team.go                  # team session launch, init wizard, status, resume
    pool_handlers.go         # HostBroker HTTP handler callbacks for pool ops
    team/
      prompt.go              # provider-aware leader prompts and team helper skills
    container/               # Docker image files (not embedded, must ship with binary)
      Dockerfile
      mittens-init           # container entrypoint binary (built from cmd/mittens-init)
      firewall.conf          # default domain whitelist
      firewall-dev.conf      # developer-friendly whitelist superset
      mcp-domains.conf       # MCP server name -> domain mappings
      clipboard-sync.sh      # macOS host-side clipboard polling
    extensions/              # pluggable extension system
      registry/              # shared types + registration (registry.Register, LoadExtensions)
        types.go             # Extension, SetupContext, SetupResult, ParseFlag
        registry.go          # Register, GetListResolver, GetSetupResolver, LoadExternalExtensions
      <name>/                # one directory per extension
        extension.yaml       # manifest (embedded at compile time)
        resolver.go          # Go resolver with init() registration (optional)
        build.sh             # Docker build-time install script (optional)
    internal/
      fileutil/              # CopyFile, CopyDir helpers
  mittens-init/              # container-side entrypoint binary
    main.go                  # busybox-style argv[0] dispatch + entrypoint
    config.go                # MITTENS_* env var loading
    phase1.go                # root: DinD, Docker socket, Go proxy, iptables, priv-drop
    phase2.go                # user: config staging, JSON settings, trust dirs, hooks, cred-sync
    proxy.go                 # FQDN-filtering HTTP/HTTPS forward proxy
    whitelist.go             # domain whitelist with .domain wildcard matching
    credsync.go              # credential sync goroutine
    broker_client.go         # shared HTTP client for host broker
    handlers.go              # xdg-open, notify, xclip, x11-clipboard handlers
  shim/                      # Windows WSL shim
  team-mcp/                  # MCP sidecar for team leader container
    main.go                  # MCP stdio server, WAL recovery, HostAPI setup
    tools.go                 # MCP tool definitions and handlers
    broker.go                # WorkerBroker HTTP server (:8080)
    notify.go                # notification formatting and MCP push
internal/
  adapter/                   # worker execution adapters and handover parsing
    adapter.go               # adapter factory + provider->adapter defaults
    claude.go                # Claude Code worker adapter
    codex.go                 # Codex exec-mode worker adapter
    handover.go              # shared handover prompt suffix + parser
  pool/                      # team pool state machine and coordination
    types.go                 # Worker, Task, Pipeline, ModelConfig, ReviewRecord, Question
    manager.go               # PoolManager — spawn/dispatch/kill, task queue
    wal.go                   # write-ahead log (JSONL), replay, recovery
    event.go                 # event types, marshaling
    router.go                # ModelRouter — per-role model routing
    pipeline.go              # PipelineExecutor — auto-advance stages
    plan.go                  # PlanStore — persistent cross-session plan management
    review.go                # DispatchReview, ReportReview, verdict handling
    reaper.go                # heartbeat detection, mark stale workers dead
    recovery.go              # WAL recovery, orphan requeue, container reconciliation
    queue.go                 # priority queue, dependency resolution
examples/
  redis-extension/           # example external extension (Python subprocess protocol)
```

## Developer Notes

- Build: `make build`, `make install`, `make help`
- Requires Go 1.23+ and runtime access to `cmd/mittens/container/` plus `cmd/mittens/extensions/*/build.sh`
- Extension reference: [docs/EXTENSIONS.md](docs/EXTENSIONS.md)
- Runtime reference: [docs/RUNTIME.md](docs/RUNTIME.md)
- Key patterns: embedded extension manifests, manual flag dispatch, project-scoped config, host↔container broker flow, root setup followed by priv-drop in `mittens-init`

## Core Flags

`--verbose`/`-v`, `--session`, `--no-config`, `--no-history`, `--no-build`, `--rebuild`, `--no-yolo`, `--no-notify`, `--network-host`, `--firewall-dev`, `--worktree`, `--shell`, `--dir PATH`, `--dir-ro PATH`, `--name NAME`, `--provider NAME`, `--image-paste-key KEY`, `--profile NAME`, `--extensions`, `--json-caps`, `--help`/`-h`, `--version`/`-V`

Use `--` to pass remaining arguments directly to the AI provider.

Top-level command: `mittens version [--json]`
- `mittens version --json` prints machine-readable JSON
- `mittens --version` and `mittens -V` remain the same version aliases

## Teams Feature

Multi-agent orchestration where a leader session coordinates planner, implementer, and reviewer workers in separate containers to parallelize complex tasks.

The full architecture and operational reference now lives in [docs/TEAMS.md](docs/TEAMS.md).

### Summary

- Entry points: `mittens team`, `team init`, `team status`, `team resume`, `team clean`
- Config: `~/.mittens/projects/<workspace>/team.yaml`
- Leader provider: selected via normal `--provider` / project defaults
- Runtime shape: host broker + leader container + `team-mcp` sidecar + worker containers
- Worker roles: planner, implementer, reviewer
- Worker execution: provider-routed adapters under `internal/adapter/`
- Persistence: WAL-backed pool state under `~/.mittens/projects/<projDir>/pools/<session-id>/`
- Cross-session artifacts: plans stored under `~/.mittens/projects/<projDir>/plans/`

Implementation lives primarily in `cmd/mittens/team.go`, `cmd/team-mcp/`, `internal/pool/`, and `internal/adapter/`.
