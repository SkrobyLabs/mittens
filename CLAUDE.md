# mittens

Run Claude Code in isolated Docker containers with credential forwarding, network firewall, Docker-in-Docker, and pluggable extensions.

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
examples/
  redis-extension/           # example external extension (Python subprocess protocol)
```

## Build

```
make build               # build binary
make install             # install to /usr/local/bin (or PREFIX=~/.local)
make help                # all targets
```

Requires Go 1.23+. The binary embeds extension YAMLs but needs `cmd/mittens/container/` and `cmd/mittens/extensions/*/build.sh` at runtime (resolved relative to binary location).

## Extension System

See [EXTENSIONS.md](docs/EXTENSIONS.md) for the full extension architecture, YAML manifest schema, Go resolver API, and external subprocess protocol.

## Key Patterns

- Extensions self-register via `init()` + blank imports in main.go
- `go:embed extensions/*/extension.yaml` bakes manifests into the binary
- Flag parsing: cobra with `DisableFlagParsing: true`, manual dispatch to core flags then extensions
- Config files: one flag per line at `~/.mittens/projects/<project>/config`, loaded and split with `strings.Fields`
- Docker image tags derived from enabled extensions (e.g. `mittens:aws-dotnet9`)
- Credentials: compare Keychain + file freshness, mount read-only, extract via `docker cp` after exit
- `HostBroker` (broker.go): single TCP server bridging host↔container for creds, URLs, OAuth, notifications, refresh coordination
- `DropProxy` (drop.go): wraps stdin through a PTY to translate host paths in bracketed paste sequences
- Container runs as root initially (for iptables/DinD), drops to AI user via syscall.Setuid/Setgid in mittens-init
- `mittens-init` (cmd/mittens-init): container entrypoint binary; handles root setup (proxy, iptables, DinD, priv-drop), user setup (config staging, JSON settings, cred sync), and busybox-style argv[0] dispatch for xdg-open, xclip, notify.sh symlinks

## Core Flags

`--verbose`, `--no-config`, `--no-history`, `--no-build`, `--rebuild`, `--docker MODE`, `--no-yolo`, `--network-host`, `--worktree`, `--shell`, `--dir PATH`, `--extensions`, `--help`

Unrecognised flags are forwarded to Claude Code (e.g. `--model`, `--print`).
