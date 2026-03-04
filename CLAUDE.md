# mittens

Run Claude Code in isolated Docker containers with credential forwarding, network firewall, Docker-in-Docker, and pluggable extensions.

## Project Structure

```
mittens              # compiled binary (Go)
main.go                  # CLI entry point (cobra, flag routing)
app.go                   # core orchestration (ParseFlags, Run, Cleanup)
config.go                # per-project config (~/.mittens/projects/...)
wizard.go                # TUI setup wizard (charmbracelet/huh)
docker.go                # docker build/run/cp operations
credentials.go           # credential extraction and persistence
credentials_darwin.go    # macOS Keychain credential source
credentials_other.go     # non-macOS stub
helpers.go               # logging, scriptDir, captureCommand
embed.go                 # go:embed for extension YAMLs
container/               # Docker image files (not embedded, must ship with binary)
  Dockerfile
  entrypoint.sh          # two-phase: root (firewall/DinD) then gosu to claude
  squid.conf             # proxy config for firewall mode
  firewall.conf          # default domain whitelist
  mcp-domains.conf       # MCP server name -> domain mappings
  clipboard-*.sh         # clipboard image sync (macOS)
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
examples/
  redis-extension/       # example external extension (Python subprocess protocol)
```

## Build

```
make build               # build binary
make install             # install to /usr/local/bin (or PREFIX=~/.local)
make help                # all targets
```

Requires Go 1.23+. The binary embeds extension YAMLs but needs `container/` and `extensions/*/build.sh` at runtime (resolved relative to binary location).

## Extension System

See [EXTENSIONS.md](EXTENSIONS.md) for the full extension architecture, YAML manifest schema, Go resolver API, and external subprocess protocol.

## Key Patterns

- Extensions self-register via `init()` + blank imports in main.go
- `go:embed extensions/*/extension.yaml` bakes manifests into the binary
- Flag parsing: cobra with `DisableFlagParsing: true`, manual dispatch to core flags then extensions
- Config files: one flag per line at `~/.mittens/projects/<project>/config`, loaded and split with `strings.Fields`
- Docker image tags derived from enabled extensions (e.g. `mittens:aws-dotnet9`)
- Credentials: compare Keychain + file freshness, mount read-only, extract via `docker cp` after exit
- Container runs as root initially (for iptables/DinD), drops to claude user via gosu

## Core Flags

`--verbose`, `--no-config`, `--no-history`, `--no-build`, `--dind`, `--yolo`, `--network-host`, `--worktree`, `--shell`, `--dir PATH`, `--extensions`, `--help`

Unrecognised flags are forwarded to Claude Code (e.g. `--model`, `--print`).
