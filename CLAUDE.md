# mittens

Run AI coding agents in isolated Docker containers with credential forwarding,
network firewalling, Docker-in-Docker, host integrations, and policy-driven
capabilities.

## Project Structure

```
cmd/
  mittens/                   # main binary source
    main.go                  # CLI entry point, manual command/flag routing
    app.go                   # core orchestration, policy application, Docker run assembly
    policy.go                # structured project policy model + legacy config conversion
    policy_cmd.go            # mittens policy show/set
    summary.go               # launch boundary summary rendering
    runtime_assets.go        # materializes embedded runtime files for installed binaries
    runtime_plan.go          # provider/capability runtime planning seams
    broker.go                # HostBroker lifecycle and shared state
    broker_host.go           # URL opening, OAuth callbacks, notifications, clipboard endpoints
    broker_credentials.go    # credential sync and freshest-wins storage
    broker_refresh.go        # token refresh coordination
    broker_http.go           # broker HTTP helper functions
    config.go                # user defaults and legacy per-project config helpers
    wizard.go                # TUI setup wizard (mittens init / --session)
    docker.go                # docker build/run/cp operations
    drop.go                  # DropProxy for drag-and-drop path translation
    credentials.go           # credential extraction and persistence
    credentials_darwin.go    # macOS Keychain credential source
    credentials_other.go     # non-macOS credential source
    terminal_focus.go        # detect terminal and re-focus on notification
    helpers.go               # logging and shared helpers
    embed.go                 # go:embed runtime assets and extension manifests
    container/               # runtime files embedded into the mittens binary
      Dockerfile
      mittens-init           # container entrypoint binary (built from cmd/mittens-init)
      firewall.conf          # default domain whitelist
      firewall-dev.conf      # developer-friendly whitelist superset
      mcp-domains.conf       # MCP server name -> domain mappings
      clipboard-sync.sh      # macOS host-side clipboard polling
    extensions/              # built-in capability system
      registry/              # shared types + registration
      <name>/                # one directory per built-in capability
        extension.yaml       # manifest embedded at compile time
        resolver.go          # Go resolver with init() registration (optional)
        build.sh             # Docker build-time install script (optional)
  mittens-init/              # container-side entrypoint binary
    main.go                  # busybox-style argv[0] dispatch + entrypoint
    config.go                # MITTENS_* env var loading
    phase1.go                # root: DinD, Docker socket, proxy, iptables, priv-drop
    phase2.go                # user: config staging, settings, trust dirs, hooks, cred-sync
    proxy.go                 # FQDN-filtering HTTP/HTTPS forward proxy
    whitelist.go             # domain whitelist with .domain and *.domain wildcard matching
    credsync.go              # credential sync goroutine
    broker_client.go         # shared HTTP client for host broker
    handlers.go              # xdg-open, notify, xclip, x11-clipboard handlers
  shim/                      # Windows WSL shim
internal/
  fileutil/                  # CopyFile, CopyDir helpers
```

## Build

```
make build               # build binary
make install             # install to /usr/local/bin (or PREFIX=~/.local)
make help                # all targets
```

Requires Go 1.23+.

The installed binary embeds built-in runtime files (`container/*` and
`extensions/*`) and materializes them under
`~/.mittens/runtime/<version>-<commit>/` when no adjacent source runtime exists.
Set `MITTENS_RUNTIME_ROOT=/path/to/cmd/mittens` to force a checkout during
runtime development.

## Policy Model

Mittens v2 uses structured project policy:

- Project policies live at `~/.mittens/projects/<project>/policy.yaml`.
- `mittens init` is the interactive policy editor.
- `mittens policy show [--json]` prints the effective policy and launch boundary.
- `mittens policy set <field> <value>` edits narrow scalar fields for automation.
- Older one-flag-per-line project configs are still readable and are converted to `policy.yaml` automatically on launch or init.
- Policy-shaped launch flags are intentionally rejected; use `mittens init` or `mittens policy set`.

Important policy areas:

- `provider.name`: `claude`, `codex`, or `gemini`
- `workspace.mounts`: extra read/write or read-only mounts
- `network.mode`: `bridge` or `host`
- `network.firewall`: `strict`, `dev`, `custom`, or `disabled`
- `network.extra_domains`: project-specific firewall allowlist domains, including `*.domain` wildcards
- `network.ssh_egress`: allow outbound SSH (port 22); unset/true permits git-over-SSH, false closes the channel
- `host`: URL opening, notifications, clipboard images, and path translation
- `capabilities`: built-in or external capability selections
- `execution`: yolo, history, worktree, shell, and Docker access

## Extension And Capability System

See [docs/EXTENSIONS.md](docs/EXTENSIONS.md) for the full architecture, YAML
manifest schema, Go resolver API, and external subprocess protocol.

Built-in capabilities are loaded from embedded manifests with disk-first
override during source/runtime development. User extensions are discovered from
`~/.mittens/extensions/<name>/` and may provide YAML, a `plugin`, and optional
`build.sh`.

## Key Patterns

- Providers encapsulate CLI identity, config paths, credential files, firewall domains, history handling, and resume/continue flags.
- Runtime planning happens before launch: provider plans and capability plans contribute image tag parts, build args, mounts, env vars, Linux capabilities, firewall domains, and setup resolver output.
- `App.applyProjectPolicy` applies structured policy directly; runtime behavior should not depend on round-tripping through synthetic launch flags.
- Extension legacy flags remain in manifests for migration and wizard plumbing, but are not supported as launch flags.
- `go:embed` covers runtime assets and extension manifests; installed binaries materialize runtime files into the Mittens runtime cache.
- Credentials use a freshest-wins model across host and container stores. Only the selected provider's stores are read/synced; the full token (incl. refresh token) necessarily enters the container so the agent CLI can refresh it, and write-through to the host is deliberate — refresh tokens rotate, so a read-only mode would invalidate the host copy after the first refresh. Host-side refresh is intentionally not attempted (it would require hooking each harness's refresh internals).
- `DropProxy` wraps stdin through a PTY to translate host paths in bracketed paste sequences.
- The container starts as root for iptables/DinD setup, then drops to the provider user via `syscall.Setuid/Setgid` in `mittens-init`.
- `mittens-init` handles root setup, user setup, credential sync, and busybox-style dispatch for `xdg-open`, `xclip`, and `notify.sh` symlinks.

## Core CLI Surface

Commands:

- `mittens init` edits project policy.
- `mittens init --defaults` edits user defaults.
- `mittens init --profile NAME` edits provider model profiles.
- `mittens policy show [--json]` inspects effective policy and boundary.
- `mittens policy set <field> <value>` updates narrow scalar policy fields.
- `mittens policy allow <domain...>` appends and de-duplicates firewall allowlist domains.
- `mittens extension list|install|remove` manages external extensions.
- `mittens doctor [--migrate-all]` checks environment health (Docker, runtime assets, broker transport) and migrates legacy per-project config to `policy.yaml`.
- `mittens logs [-f]`, `mittens clean`, and `mittens version`.

Launch/runtime flags are intentionally small:

`--verbose`, `--session`, `--no-config`, `--no-history`,
`--no-build`, `--rebuild`, `--name NAME`, `--firewall-learn`, `--extensions`,
`--json-caps`, `--version`, `--help`

`--firewall-learn` runs one permissive-but-logging pass that records out-of-allowlist domains and offers to add them to `network.extra_domains`; `mittens init` can arm a one-time pass via a `.learn-once` sentinel under the project dir (transient operational state, deliberately not a policy field).

Unrecognised arguments after Mittens parsing are forwarded to the selected
provider CLI. Use `--` when passing provider args explicitly, for example:

```
mittens -- --model opus
```
