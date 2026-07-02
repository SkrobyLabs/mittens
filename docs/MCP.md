# MCP Support

Status: implemented (v1, experimental).

MCP servers are configured per provider, but their runtime assumptions vary:
stdio servers can reference host-local scripts or executables by absolute
path, remote bridges and local cloud MCP servers can depend on host CLI auth,
keychains, MSAL caches, or PAT environment variables, and browser OAuth
callbacks that work on the host often fail when the listener moves into a
container. Mittens does not try to infer or mount arbitrary credential
directories for this; instead it chooses per server between leaving MCP config
alone, mounting MCP helper code, and proxying MCP protocol traffic through the
host broker.

## Policy Model

MCP is a first-class policy section with a per-server `mode`, defaulting to
`direct`:

```yaml
mcp:
  servers:
    - name: shortcut
      mode: proxy
      command_pin: sha256:9f2c… # pinned at approval time; refused on drift
    - name: local-filesystem
      mode: mount
```

Legacy `--mcp`/`--mcp-all` selections and the older `capabilities: [{name:
mcp}]` config form migrate to `mode: direct` entries automatically in the
policy normalize path, with a deprecation warning. `policy show` renders each
server as `name (mode)`, including refusal status. `policy set
mcp.<server>.mode <mode>` edits a single server; setting `proxy` resolves the
server against current host config, prints the full command line, and
computes its `command_pin`.

## Modes

Each configured server has an explicit mode:

| Mode | Behavior |
|---|---|
| `direct` | Provider MCP config is left unchanged. The server runs (or fails) inside the container exactly as configured. Works for URL-based remote servers (subject to firewall whitelisting) and stdio servers whose command resolves inside the container image (`npx`, `uvx`, container-installed binaries). A stdio server with a host-absolute command path fails to start under `direct` -- that's expected; use `mount` or `proxy` instead. |
| `mount` | The server runs inside the container; its helper code is mounted read-only (see Mount Mode below). |
| `proxy` | The provider MCP command is replaced with a container shim that forwards MCP protocol traffic to a host-side process managed by the broker. |

`mcp_classify.go` recommends a mode per server shape for the wizard, and never
pre-selects `proxy`:

| Server shape | Recommended | Notes |
|---|---|---|
| `url:` remote server | `direct` | firewall whitelisting applies |
| stdio, command resolvable in container (`npx`, `uvx`, …) | `direct` | may still need auth env; host-auth-dependent servers should use `proxy` |
| stdio, host-absolute helper path, no host auth | `mount` | helper code only, read-only |
| stdio, host auth dependency (keychain, CLI token, PAT) | `proxy` | credential never enters the container |
| stdio, broad local capability (filesystem, shell, docker, kubernetes) | `direct` or nothing | wizard warns; never defaults to `proxy` |

The wizard asks mode per selected server (not one mode for all).

## Config Provenance

MCP server definitions come from two trust domains: user-scope provider config
(`~/.claude.json`, `~/.codex/config.toml`, `~/.gemini/settings.json`) and the
workspace `.mcp.json`, which is repo-controlled. `proxy` mode executes the
configured command on the host with host environment, so granting it to a
repo-controlled definition would be a sandbox escape by configuration.

- **Command pinning.** At approval time (wizard or `policy set`), Mittens
  hashes the resolved `command` + `args` + sorted env var names
  (`sha256` over canonical JSON, pre-env-expansion) into `command_pin`. At
  launch, the broker recomputes the hash from current config; on mismatch it
  refuses to register the proxy endpoint, logs the old and new command lines,
  and skips the staged config rewrite for that server. The launch summary
  shows it as `proxy (refused: command changed)`. Pin verification failure is
  never fatal to the launch. Re-approval through the wizard or `policy set`
  updates the pin.
- **Workspace refusal.** `mode: proxy` is user-scope config only. Servers
  defined solely in the workspace `.mcp.json` are refused proxy mode in both
  the wizard (`proxy — unavailable: workspace-defined server`) and `policy
  set` (`proxy mode for workspace-scope servers is not supported in v1`).
  Their rewrite can't be staged without shadow-binding into the
  identity-mounted workspace.
- Bulk selection (`--mcp-all`, "select all" in the wizard) never sets `proxy`
  for any server.

## Shared Parser (`internal/mcpconfig`)

A single parser replaces what used to be three duplicated readers: the
firewall resolver (`extensions/mcp/resolver.go`), the helper-mount planner
(`mcp_runtime.go`), and the container-side firewall resolution in
`cmd/mittens-init/phase1.go`. All four consumers -- those three plus broker
proxy registration -- read `internal/mcpconfig.Server` values (`Name`,
`Command`, `Args`, `URL`, `Env`, `Headers`, `Dir`, `Source`, `Scope`), parsed
from Claude's JSON (including the `projects` key), Codex TOML, Gemini JSON, and
workspace `.mcp.json`. `Scope` is `user` or `workspace`; Claude's
`projects.<path>` entries in `~/.claude.json` count as user scope since they
live in user config. Because `mittens-init` parses the *staged* config with
the same module, the staged transform preserves `url` keys for direct servers
so container-side firewall URL resolution keeps working.

## Env Expansion

`${VAR}` and `${VAR:-default}` tokens in `env`, `headers`, and `args` of
`direct`/`mount` servers are expanded host-side, at stage time, against the
Mittens process environment, into the staged provider config copy -- host
originals are never modified. An unset variable with no default is left
untouched (never substituted with empty) and produces a `logWarn` naming it.
`proxy` servers never get expansion: their `env` map is stripped from the
staged copy entirely and delivered host-side instead (see Runtime Behavior).
The launch summary lists injected variable names per server (`MCP env
injected: ...`).

## Mount Mode

Mount mode covers simple local MCP servers where the helper code is safe to
expose to the container, has no host-only auth dependency, and behaves the
same whether it runs in the container or on the host.

Path discovery is deliberately conservative -- mounting every absolute path in
`command`/`args` would expose data-path arguments (log files, filesystem-scope
directories) as well as helper code:

- Auto-mount candidates are the `command` path and args that plausibly *are*
  the entry point: the first non-flag argument, or an argument with a
  script extension (`.js`, `.mjs`, `.cjs`, `.py`, `.sh`, `.rb`).
- Candidates must stat as a regular file with an exec bit or a script
  extension, or be a directory given explicitly as the command.
- A candidate file is promoted to its git repo root so relative imports work
  (logged prominently -- this mounts the whole repo, `.env` files included).
- Mounts whose container-side path would sit at or under `/etc`, `/usr`,
  `/bin`, `/sbin`, `/lib`, `/opt`, `/var`, or `/root` are refused outright: a
  host `/etc` mounted over the container's `/etc` shadows `passwd` and breaks
  `mittens-init`'s user setup. The sensitive-directory denylist (`.ssh`,
  `.aws`, `.azure`, `.config`, `.gnupg`, `.kube`, `.docker`) is a backstop on
  top of this.
- Mounts are always read-only.

## Proxy Architecture

For stdio MCP servers in `proxy` mode:

```text
AI CLI inside container
  -> provider MCP config points to mittens-mcp-proxy
  -> mittens-mcp-proxy connects to the Mittens broker
  -> broker starts the original host MCP command
  -> broker pipes stdin/stdout between shim and host MCP process
```

`mittens-mcp-proxy` is an argv[0] persona of `mittens-init`, alongside
`xdg-open`, `xclip`, and `notify.sh`, reusing `broker_client.go` for transport
and auth. The broker is a plain HTTP/1.1 server over both a unix socket and
TCP (`broker.go`); a hijacked connection (`http.Hijacker`) on a
`/mcp/<name>` endpoint gives stdio a long-lived bidirectional byte stream
without WebSockets or a second listener. The broker never parses JSON-RPC --
it pipes bytes, so server-initiated traffic (sampling requests, notifications)
works transparently.

The host MCP process runs with host environment, host filesystem access, and
host credential state. The container only ever sees MCP protocol messages.

### Broker Enforcement

The staged config rewrite is cosmetic; the real capability boundary is which
`/mcp/<name>` endpoints the broker registers:

- `NewHostBroker` registers `mux.HandleFunc("/mcp/", ...)` as a catch-all
  route (server name parsed from the path, behind `withAuth`); only names with
  a registered spec are served, so there is no path-traversal surface.
- An endpoint is registered only for servers with `mode: proxy` in policy
  whose `command_pin` verifies against current host config, before the broker
  starts serving.
- For each registered endpoint the broker stores the resolved command, args,
  env, working directory, and source config path, all read host-side at
  launch. Nothing the container writes can change what command runs.
- A container process connecting to an unregistered `/mcp/<name>` gets a plain
  HTTP error.

### Runtime Behavior

For each proxied stdio server, the staged provider config is rewritten:

```toml
command = "mittens-mcp-proxy"
args = ["shortcut"]
```

with the `env` map stripped (delivered host-side instead). The staged
transform is applied per provider:

- **Claude**: the transformed copy is mounted at `StagingUserPrefsPath()` in
  place of the host `.claude.json`; everything outside the `mcpServers` keys
  (top-level and the current project's `projects.<path>` entry) is copied
  through byte-identical.
- **Codex**: the TOML `[mcp_servers.<name>]` sections are rewritten with a
  targeted line edit (command/args replaced, `env` table lines dropped for
  proxied servers), leaving the rest of the file untouched. If this proves too
  fragile for a given config, refusing Codex proxy mode is the sanctioned
  fallback.
- **Gemini**: a nested file-over-dir bind mounts the transformed file over its
  path inside the staged config directory.

When `mittens-mcp-proxy <name>` starts in the container, it opens a hijacked
stream to the broker's `/mcp/<name>` endpoint. The broker starts one host-side
MCP child process for that session and server name, in its own process group.
The child's environment is the broker process's `os.Environ()` with the MCP
config entry's `env` map merged on top (config wins). Upstream stderr is
captured in Mittens logs (env values redacted, names only), not forwarded as
MCP stdout. Working directory is the config entry's cwd if set, otherwise the
workspace path.

### Lifecycle

- **Half-close propagation.** Shim stdin EOF closes the write side of the
  hijacked connection, which closes the child's stdin; child stdout EOF closes
  the connection's write side back to the shim, which then exits. Both unix
  and TCP transports support `CloseWrite`.
- **Kill-tree.** Termination signals the child's whole process group
  (`SysProcAttr{Setpgid: true}`, SIGTERM then a 5s grace then SIGKILL to
  `-pgid`), not just the direct child -- MCP servers commonly spawn children
  (`npx` -> `node` -> workers).
- **Kill-and-respawn on reconnect.** A shim disconnect followed by a reconnect
  for the same server name is kill-and-respawn, not an error (agent CLIs do
  this on manual MCP reconnect). Respawn is serialized per (session, server)
  key: a per-name mutex is held only across {kill old pgid -> wait reap ->
  spawn new -> record}, never across connection or pipe I/O, so a reconnect
  can never race two children into existing for the same key.
- **No protocol-level timeouts.** Child lifetime is bound to the stream --
  killed on shim disconnect and on broker shutdown (`Close()` sweeps all live
  children). A child that fails at startup surfaces as immediate stream EOF,
  which the agent CLI reports itself.

### Path Translation

Mittens identity-mounts the workspace (host path = container path, `-v
src:src`), including in worktree mode, so paths a container-side agent passes
to a host-side MCP server are valid on both sides by construction. Container-only
locations such as `/tmp/mittens-drops` are the exception and are a documented
limitation of proxy mode rather than a translation layer.

### Session Model

Stdio MCP proxying starts a separate host MCP process per Mittens session and
server:

```text
mittens session A -> broker A -> host MCP process A
mittens session B -> broker B -> host MCP process B
```

This isolates JSON-RPC IDs, initialization state, cancellations, roots,
prompts, subscriptions, and notifications across sessions. It does not isolate
host-side server state: two concurrent sessions proxying the same server still
share that server's own caches, SQLite files, and locks on the host.

### OAuth Callback Tunneling

Browser OAuth flows for host-spawned proxy servers reuse the existing
callback tunnel rather than new infrastructure: `broker_host.go` intercepts
any opened URL with a localhost `redirect_uri` port
(`extractOAuthCallbackPort` -> `interceptOAuthCallback`) and stores it for
`GET /login-callback`; `cmd/mittens-init/handlers.go`'s `runOpenURL` polls and
replays it to the CLI's in-container listener. The single pending-callback
slot handles one auth flow at a time.

## Firewall Interaction

Proxied MCP traffic egresses from the host, so it bypasses the container
firewall entirely -- `mcp-domains.conf` whitelisting is both unnecessary and
unenforceable for `proxy` mode servers. The firewall selection passed to the
mcp extension excludes proxy-mode server names, and if the selection is the
`__all__` sentinel while any server is `proxy` mode, it's expanded host-side
into the explicit non-`proxy` name list before reaching the container (the
container-side `__all__` expansion in `phase1.go` enumerates staged config and
would otherwise re-whitelist proxied servers). The launch summary states that
a proxied server's traffic is outside the firewall.

## Launch-Time Escalation

When a `direct`-mode stdio server's command is a host-absolute path that isn't
mounted into the container, Mittens logs a warning suggesting `mittens policy
set mcp.<name>.mode proxy`. Runtime auth-failure detection is out of scope.

## Security Boundary

`proxy` mode moves a tool outside the sandbox entirely, not just gives the
container a protocol-level way to invoke it. The threat model shifts from
"what can escape the container" to "what can this MCP server's tool surface do
on the host."

That trade is right for cloud-backed MCPs whose local process is just an
authenticated API client (issue trackers, docs services): the blast radius
stays bounded by the API token's scope, and the token never enters the
container -- strictly better than mounting credentials. It is wrong for
servers with broad local capability (filesystem access, shell executors):
proxying those hands the sandboxed agent host-level power through a
legitimate channel. The wizard warns, and never defaults to `proxy`, for
servers whose command suggests broad local capability.

- **Provenance matters as much as capability.** Without command pinning and
  broker-side allowlisting, `mode: proxy` per *name* would be a grant a
  repo-controlled `.mcp.json` could later redirect. See Config Provenance and
  Broker Enforcement.
- **Broker-token reach.** The broker token reaches the container via the 0644
  session config file (a pre-existing, deliberate exposure -- the container uid
  must read it after privilege drop), so the agent process can open
  `/mcp/<name>` streams directly, not only through the shim. This is within
  the threat model: only pin-verified, user-approved commands are reachable,
  and nothing container-side can change what a given name executes.

## Launch Summary

```text
MCP servers (experimental): shortcut, local-filesystem
MCP proxy: shortcut via host broker (traffic outside firewall)
MCP env injected: local-filesystem: API_TOKEN
MCP helper mounts: /path/to/helper ro
```

## Coverage

`proxy` mode fixes the dominant failure class by construction: anything that
already works in a host terminal works proxied, because the process runs on
the host. That covers CLI token caches, keychains, MSAL caches, PAT
environment variables, `mcp-remote` OAuth caches, and browser OAuth callbacks
for host-spawned servers (the server's `localhost:PORT` listener and the host
browser are on the same machine again).

Three classes remain unsupported:

1. **Remote HTTP/SSE servers where the agent CLI performs OAuth itself.** The
   CLI runs in the container; the broker opens the auth URL in the host
   browser; but the `redirect_uri` points at `localhost:PORT`, which resolves
   to the host's localhost while the listener is inside the container. Stdio
   proxying doesn't touch this -- it would need HTTP proxying or callback
   port-mapping.
2. **Servers whose semantics change when moved to the host.** A filesystem or
   shell MCP proxied to the host "works" -- it just sees host state, not the
   container or worktree. Deliberately discouraged, not fixed (see Security
   Boundary).
3. **Concurrent-session contention on host-side server state.** Each session
   spawns its own host process (correct at the protocol layer), but concurrent
   sessions still share the server's own caches, SQLite files, and locks on
   the host.

The realistic claim: every MCP that works in the user's host terminal, minus
deliberately excluded local-capability servers and CLI-managed remote OAuth.
