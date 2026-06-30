<p align="center">
  <img src="./assets/mittens.png" alt="Mittens" width="200">
</p>

<h3 align="center">Mittens on. Don't get burned.</h3>

<p align="center">
  Run AI coding agents in isolated Docker containers with credential forwarding, network firewall, Docker-in-Docker, and pluggable extensions.
</p>

---

> **Early development** — Mittens already works quite well, especially on macOS and Linux, but issues are to be expected. If you run into something, [raising an issue](https://github.com/SkrobyLabs/mittens/issues) is appreciated.

Mittens wraps [Claude Code](https://docs.anthropic.com/en/docs/claude-code), [Codex](https://github.com/openai/codex), and [Gemini CLI](https://github.com/google-gemini/gemini-cli) so they run containerised, firewalled, with only the credentials you choose to pass in.

## Why

AI coding agents work best when you stop babysitting them and let them run autonomously — but giving an agent full permission on your host machine is a terrible idea. Mittens creates a sandboxed environment where agents can do anything they need to, while the blast radius is limited to only what you explicitly give them access to.

You could spin up a VM or a remote box, but then you lose everything that makes local development comfortable: clipboard access, drag-and-drop, desktop notifications, browser-based OAuth login, and instant access to your credentials. You'd also be paying for cloud compute and waiting for VMs to boot. Mittens containers start in seconds, run on your own machine, and handle all the host integration transparently — credential syncing, URL forwarding, path translation — so it feels like working locally, just sandboxed.

## What Mittens Is (And Isn't)

Mittens is built to **contain mistakes and limit blast radius**. An agent that goes off-task, runs a destructive command, or makes a bad call can only touch what you explicitly mounted and the domains you allowed — not your whole home directory, not arbitrary hosts, not your other projects.

It is **not** a hardened jail against an agent (or a compromised dependency) *actively trying to escape*. A few egress channels stay open by design, and the host integrations that make Mittens pleasant to use are, by definition, channels back to your host. See [Security Model](#security-model) for exactly what is and isn't enforced.

In short: Mittens protects you from an agent's **errors** far better than a bare `--dangerously-skip-permissions` session on your host, while keeping local development ergonomics. Treat the credentials and directories you pass in as "things the agent can use," not "things the agent can never leak."

## Supported Platforms

| Platform | How it works |
|----------|-------------|
| **macOS** | Native binary, Keychain credential sync, clipboard image sync |
| **Linux** | Native binary, file-based credential store |
| **Windows** | `mittens.exe` shim auto-delegates to the real binary via WSL |

Maturity varies by platform: **macOS and Linux are the best-tested paths**; **Windows/WSL works but is newer** and more likely to surprise you. Ollama as a provider is supported on all platforms (see [docs/LOCAL-PROVIDER.md](docs/LOCAL-PROVIDER.md)).

## Requirements

- Docker (or Docker Desktop on Windows/macOS)
- Go 1.23+ (to build from source)
- WSL (Windows only)

## Install

```bash
git clone https://github.com/SkrobyLabs/mittens.git
cd mittens
make build
make install  # → /usr/local/bin/mittens (or PREFIX=~/.local)
```

On Windows, `make build` produces both `mittens.exe` (WSL shim) and `mittens-linux` (real binary). Run `mittens.exe` from PowerShell or cmd — it transparently uses WSL under the hood.

The installed binary carries the built-in Docker runtime files with it. If no adjacent `container/` directory is present, Mittens materializes those assets under `~/.mittens/runtime/<version>-<commit>/`. For local runtime development, set `MITTENS_RUNTIME_ROOT=/path/to/cmd/mittens` to force a source checkout.

## Quick Start

```bash
cd your-project
mittens                  # run in a container — that's it
mittens init             # interactive project setup (extensions, dirs, firewall)
mittens init --defaults  # set user-wide defaults (provider, firewall, paste key)
mittens init --help      # see all init subcommands
mittens policy show      # inspect the effective project policy and boundary
mittens policy set host.open_urls deny
mittens help             # see all flags and commands
```

Project policies are saved to `~/.mittens/projects/` as `policy.yaml` and loaded automatically next time. Older one-flag-per-line project configs remain readable. When an older project config is found at launch, Mittens converts it to `policy.yaml` automatically. Policy-shaped launch flags are no longer accepted; use `mittens init` or `mittens policy set` instead.

Policy can also disable host integrations directly. For example, `host.open_urls: deny`, `host.clipboard_images: false`, `host.notifications: false`, and `host.path_translation: false` are enforced at launch time.

## Providers

Mittens supports multiple AI coding agents configured in project policy:

| Provider | Policy value |
|----------|--------------|
| **Claude** (default) | `provider.name: claude` |
| **Codex** | `provider.name: codex` |
| **Gemini** | `provider.name: gemini` |
| **Ollama** | `provider.name: ollama` |

Each provider brings its own credential layout, firewall domains, agent CLI args, and config format. Mittens handles all the differences — same workflow regardless of provider.

Claude can also run against an Anthropic-compatible proxy that routes to OpenAI. In `mittens init`, choose Claude, then choose the OpenAI proxy backend. By default Mittens manages the proxy inside the container:

```yaml
provider:
  name: claude
  backend: openai
  model: opus # optional proxy alias
```

Managed mode starts UniClaudeProxy on `127.0.0.1:9223` in the container. It uses `OPENAI_API_KEY` when set, otherwise it can read `OPENAI_API_KEY` or a ChatGPT `tokens.access_token` from `~/.codex/auth.json`. Missing credentials produce a `codex login` prompt; invalid or expired credentials are left for the proxy's upstream request to reject. By default, Mittens maps Claude-side aliases by tier: `fable` to `gpt-5.5` with `xhigh` reasoning, `opus` to `gpt-5.5` with `medium`, `sonnet` to `gpt-5.5` with `low`, and `haiku` to `gpt-5.4-mini` with `low`. The `fable`, `opus`, and `sonnet` defaults are advertised to Claude Code with its `[1m]` suffix because those routes use 1M-context OpenAI models; `haiku` stays unsuffixed because its mini route is 400K context. Set `MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL` or `MITTENS_CLAUDE_OPENAI_REASONING_EFFORT` to override all managed alias routes.

To use your own proxy instead, set `provider.endpoint`:

```yaml
provider:
  name: claude
  backend: openai
  endpoint: http://host.docker.internal:9223
```

This still runs Claude Code inside the container; external proxies must implement Anthropic Messages API translation and handle OpenAI upstream credentials.

## How It Works

### Container Isolation

The AI CLI runs inside a Docker container with `--cap-drop ALL` unless Docker-in-Docker is enabled in policy. The workspace is mounted at `/workspace` and the CLI's config is copied from the host at startup.

The container starts as root for initial setup (firewall rules, Docker daemon), then drops to a non-root user via `syscall.Setuid/Setgid`. Containers are force-removed on exit — each invocation is ephemeral.

### Host Broker

Mittens runs a host-side TCP server (`HostBroker`) that bridges all communication between the host and containers. It handles:

- **Credential sync** — bidirectional OAuth token synchronization
- **OAuth login** — intercepts browser callbacks and replays them into the container
- **URL forwarding** — opens URLs from the container in the host browser
- **Notifications** — relays container events to host desktop notifications
- **Refresh coordination** — ensures only one container triggers a proactive token refresh

#### Credential Syncing

```
Host                              Container
┌──────────────┐                  ┌──────────────┐
│  Keychain /  │◄── pull ────────►│  cred-sync   │
│  file store  │    (5s poll)     │  goroutine   │
└──────┬───────┘                  └──────┬───────┘
       │                                 │
       ▼                                 ▼
┌──────────────┐   PUT (fresher)  ┌──────────────┐
│ HostBroker   │◄────────────────►│ .credentials │
│   (TCP)      │   GET (latest)   │    .json     │
└──────────────┘                  └──────────────┘
```

The broker uses a **freshest-wins** model: each credential set has an `expiresAt` timestamp, and only newer tokens replace older ones. When the CLI refreshes a token inside the container, the daemon pushes it to the broker, which writes it through to the host's credential stores. The host side polls its stores (Keychain on macOS, file stores everywhere) and updates the broker if the host has fresher credentials.

#### OAuth Login

When the AI CLI opens an OAuth URL inside the container:

1. The `xdg-open` shim forwards the URL to the broker via `POST /open`
2. The broker starts a temporary HTTP listener on the OAuth callback port and opens the URL in the host browser
3. After the user authenticates, the browser redirects to `localhost:<port>` — the broker captures the callback
4. The container polls `GET /login-callback` and replays the callback URL to the CLI's local server

Result: seamless login without manual copy-paste between host and container.

### Stdin Path Translation

When you drag-and-drop files from Finder (macOS) or a file manager into the terminal, the pasted paths refer to the host filesystem. Mittens wraps stdin through a PTY proxy (`DropProxy`) that intercepts bracketed paste sequences and:

- Translates host paths to their container mount points (e.g. `/Users/you/project/file.go` → `/workspace/file.go`)
- Copies files outside any mount into a drop zone (`/tmp/mittens-drops`) so the container can access them

This works transparently — the AI CLI sees container-valid paths.

### Network Firewall

Enabled by default. Use `mittens policy set network.firewall disabled` to disable it. Uses a built-in Go forward proxy + iptables to restrict outbound HTTP/HTTPS to whitelisted domains only.

Default whitelist includes: provider API endpoints, GitHub/GitLab/Bitbucket, npm/PyPI/crates.io/Go proxy, Docker registries, Helm, and Terraform.

Extensions declare additional domains, such as AWS endpoints when the AWS capability is enabled in policy. MCP server domains are auto-resolved from config and the built-in `mcp-domains.conf` mapping file. SSH traffic (port 22) bypasses the proxy entirely so git-over-SSH works; because that is an unrestricted outbound channel, you can close it with `mittens policy set network.ssh_egress false` (see [Security Model](#security-model)).

Project-specific domains can be added with `mittens policy set network.extra_domains` (replaces the list) or `mittens policy allow <domain...>` (appends and de-duplicates). Use `*.` or a leading dot to allow a domain and all nested subdomains, for example:

```bash
mittens policy allow api.example.com '*.apps.example.test'
```

When the firewall blocks a request, the agent receives the exact `mittens policy allow <domain>` command to relay, and the host terminal prints the same hint (deduplicated per host; also in `mittens logs`).

Use `mittens policy set network.firewall dev` for a developer-friendly whitelist that adds cloud APIs and apt repos.

#### Discovering Required Domains (learn mode)

Predicting a project's allowlist up front is the usual friction. Run `mittens --firewall-learn` once to start permissive-but-logging: every domain used outside the allowlist is forwarded *and* recorded instead of blocked, and when the container exits Mittens lists what it saw and offers to add it to `network.extra_domains` (on a non-interactive run it writes `firewall-learn.json` under the project directory and prints the `mittens policy allow` command to apply later). The next run enforces normally. Because the allowlist is not enforced during a learn pass, the launch boundary summary says so explicitly. `mittens init` can arm a one-time learn pass for the next run when you choose an enforcing firewall mode.

### Launch Boundary Summary

Before starting the container, Mittens prints a compact summary of the boundary the agent will run inside: provider, workspace mount, extra directories, credential sources, network mode, enabled extensions, host integrations, execution mode, and history mode. Sensitive values are not printed.

Use `--verbose` to also print the sanitized `docker run` command.

Use `mittens policy show` to inspect the same boundary without launching a container. Add `--json` for machine-readable policy output.

### Docker-in-Docker

`mittens policy set execution.docker dind` runs the container in `--privileged` mode with a dedicated Docker volume. A separate `dockerd` starts inside the container, allowing the AI to build and run containers as part of its work. The DinD volume is named `<container>-docker` and cleaned up on exit.

### Worktree Isolation

`mittens policy set execution.worktree true` creates a detached-HEAD git worktree for each invocation, so the AI works on a copy instead of the primary working tree. On exit, the worktree is removed if clean (no changes, no new commits) or kept if dirty. Extra directories configured as policy mounts also get their own worktrees when possible.

Git worktrees that the AI agent creates *inside* the container during a session also work. However, `git worktree add` defaults to sibling directories (e.g. `../feature`), which land outside the bind-mounted workspace and are **lost when the container exits**. Worktrees created *under* `/workspace` (or another RW-mounted path) do persist. Directories mounted read-only by policy will fail worktree creation entirely.

#### Orchestration: explicit worktree root, branch, and manifest

For tools that launch many isolated agent runs, four per-run launch flags make
worktree placement, branching, and accounting deterministic (worktree mode must
be enabled via `execution.worktree: true`, typically in a per-run `--policy`
file):

```bash
mittens \
  --headless --report-progress \
  --policy /path/to/run/policy.yaml \
  --worktree-root /path/to/run/worktrees \
  --worktree-branch feature/123-story-title \
  --worktree-manifest /path/to/run/worktrees.json \
  --worktree-cleanup keep \
  --name sm-implement-sc-123 \
  -- <provider args>
```

- `--worktree-root PATH` nests worktrees under `PATH` (named by repo base name,
  with a short hash on collision) instead of sibling `<repo>.wt-<pid>` dirs.
- `--worktree-branch NAME` creates-or-checks-out `NAME` in every repo worktree
  instead of a detached HEAD; the branch is committed into the source repo's git
  metadata, so no fetch-back is needed.
- `--worktree-manifest PATH` writes a JSON manifest of every worktree created
  (paths, branch, start/end commits, dirty, kept, primary).
- `--worktree-cleanup keep|keep-dirty` controls removal; `keep` is best for
  orchestrators that own run-directory cleanup.

Sibling placement and detached-HEAD behavior are unchanged when these flags are
omitted. See [docs/WORKTREE-ORCHESTRATION.md](docs/WORKTREE-ORCHESTRATION.md) for
the full integration guide.

### Model Profiles

`mittens policy set provider.profile NAME` selects a saved model + effort preset. Profiles are per-provider and per-project.

```bash
mittens policy set provider.profile planner
mittens init --profile fast     # configure the "fast" profile
mittens init --profile planner --delete  # remove a profile
```

If profiles exist for the current provider and no policy profile is set, mittens shows a picker at startup.

### Session Persistence

Enabled by default (`--no-history` to disable). Conversation history is persisted and mounted into the container. Mittens does not expose a generic resume flag; pass the selected provider's resume/continue arguments after `--`, for example `mittens -- --resume latest` for providers that support that syntax.

### Clipboard & Image Sync (macOS)

On macOS, a host-side script polls the clipboard for images every second and writes PNGs to a temp directory mounted into the container. An `xclip` shim inside the container reads these images, allowing the AI CLI to access clipboard images.

For the Codex provider on macOS, mittens also starts a local `Xvfb` display inside the container and mirrors the synced PNG into the X11 clipboard using a real `xclip` process. This gives Codex's Linux clipboard backend an in-container display/clipboard to read from.

### Notifications

When the AI CLI triggers a hook event (e.g. task completion, permission prompt), the container sends a notification to the broker via `POST /notify`. The host displays a desktop notification and re-focuses the terminal window that launched mittens.

### URL Forwarding

The container's `xdg-open` is replaced with a shim that forwards all URLs to the host browser via the broker's `POST /open` endpoint. This works for any URL the AI CLI tries to open, not just OAuth flows.

Run `mittens help` for commands, or `mittens init --help` for setup subcommands.

## Security Model

Before relying on Mittens for anything sensitive, it's worth knowing exactly what the boundary enforces — and what it deliberately does not.

### Enforced

- **Reduced capabilities** — the container runs with `--cap-drop ALL` unless Docker-in-Docker is explicitly enabled in policy.
- **Non-root agent** — the container performs root-only setup (iptables, DinD), then drops to an unprivileged user via `setgroups`/`setgid`/`setuid` before the agent CLI ever runs.
- **Default-deny egress** — the firewall sets the `OUTPUT` policy to `DROP` and forces all outbound HTTP/HTTPS through an in-container proxy that only permits whitelisted domains on ports 80/443.
- **Scoped mounts** — only the workspace and the directories you configure in policy are visible; the rest of your host filesystem is not mounted.
- **Ephemeral containers** — each run is force-removed on exit.
- **Authenticated host bridge** — the broker requires a per-run random token and binds to a Unix socket (Linux) or localhost (macOS/WSL), not a public port.

### Not a hard boundary

- **SSH (port 22) egress is open to any host by default.** It exists so git-over-SSH works, but it is an unrestricted outbound TCP channel — a determined agent could use it to reach or tunnel to arbitrary hosts. Close it with `mittens policy set network.ssh_egress false`; the launch boundary summary shows whether it is allowed or blocked for each run.
- **DNS (port 53) is open to any resolver,** which makes DNS-tunnel exfiltration theoretically possible.
- **The firewall filters by hostname, not destination identity.** Like any FQDN-filtering proxy, it trusts the requested hostname and cannot prevent domain-fronting through a shared CDN IP.
- **Host integrations are intentional holes.** URL opening, notifications, clipboard sync, and credential write-through each bridge the container back to your host. They are gated by policy and the broker token, but they exist.
- **Credentials are scoped to the selected provider, but the full token enters the container.** Only the active provider's credential stores are read, staged, and synced — other providers' tokens never enter the container. The complete token (including the long-lived refresh token) must live in-container, because the agent CLI refreshes it there; the broker then writes the rotated token back to your host store. Write-through is deliberate and load-bearing: refresh tokens rotate, so suppressing it would invalidate the host copy after the first refresh and break the next run. Keeping the refresh token host-side would require hooking each harness's internal refresh path, which Mittens does not attempt. Treat a forwarded credential as reachable by the agent for the duration of the run.

If your threat model includes an agent or dependency *actively trying* to exfiltrate data, tighten the boundary: use `network.firewall strict`, keep `network.extra_domains` minimal, close SSH egress with `mittens policy set network.ssh_egress false`, disable host integrations you don't need (`mittens policy set host.open_urls deny`, `host.clipboard_images false`, `host.notifications false`), and treat any credentials you forward as potentially reachable by the agent.

## Debugging

- `mittens logs [-f]` — view broker logs (credential sync, OAuth intercept, URL forwarding, blocked egress). `-f` follows the log. When the firewall blocks an outbound domain, the attempt is logged here and a one-line warning is printed to the terminal that launched mittens (deduplicated per host).
- `--verbose` — prints the full `docker run` command so you can see all mounts, env vars, and flags.
- `mittens policy set execution.shell true` — drops into a bash shell inside the container for manual inspection.
- `mittens doctor` — checks Docker, runtime assets, and broker prerequisites, and reports any problems. Run `mittens doctor --migrate-all` to convert every legacy per-project config under `~/.mittens/projects` to `policy.yaml` in one pass.

## Troubleshooting

| Symptom | Likely cause & fix |
|---------|--------------------|
| A build or tool can't reach a domain (`403 not in whitelist`) | The firewall blocked it. Add it with `mittens policy set network.extra_domains 'example.com'` (use `*.example.com` for subdomains), or check `mittens logs` for the denied host. |
| OAuth login never completes | The broker couldn't intercept the callback. Check `mittens logs` for `OAuth intercept` lines, and ensure the callback port isn't already in use on the host. |
| `host Docker socket not accessible` | The container couldn't access the host Docker socket. Ensure Docker is running and your user can reach `/var/run/docker.sock`. |
| Credentials don't sync / agent asks to log in again | Check `mittens logs` for credential-sync activity. Stale provider tokens can require a fresh login — for Codex, see the notes in [docs/LOCAL-PROVIDER.md](docs/LOCAL-PROVIDER.md). |
| Clipboard images or notifications don't work | These are macOS/WSL features and may be disabled by policy. Verify `host.clipboard_images` / `host.notifications` and see `mittens policy show`. |

For anything else, `mittens logs -f` follows the broker log live, and `--verbose` prints the full sanitized `docker run` command.

## Files & State

Mittens keeps all of its state under `~/.mittens/`:

```
~/.mittens/
  projects/<project>/policy.yaml   # per-project policy
  runtime/<version>-<commit>/      # materialized runtime assets (installed binaries)
  extensions/<name>/               # user-installed external extensions
  logs/broker.log                  # broker debug log (mittens logs)
```

Credentials are synced through the host's native stores (Keychain on macOS, file-based stores elsewhere) and a per-run broker; they are not stored in plain text under `~/.mittens/`.

To remove Mittens entirely: uninstall the binary (delete `/usr/local/bin/mittens` or your `PREFIX` path), then `rm -rf ~/.mittens`. On macOS, any Mittens-related Keychain items can be removed via Keychain Access.

## Extensions

Built-in capabilities include SSH, GitHub, AWS, Azure, GCP, Kubernetes, Helm, Docker, .NET, Go, Python, Rust, MCP, and the default firewall. Configure them with `mittens init`; legacy extension flags in old project config are converted to structured policy automatically.

External plugins: drop an executable at `~/.mittens/extensions/<name>/plugin` — no recompilation needed.

See [EXTENSIONS.md](docs/EXTENSIONS.md) for the full extension system docs.

## Support the Project

If this project helps your work, consider supporting its continued development.

Individual supporters can join here:
[Buy Me a Coffee](https://buymeacoffee.com/skrobylabs)

Companies using this project can sponsor development and have their logo displayed in this repository.

Sponsorship inquiries: ![sponsor email](./assets/sponsor-email.svg)

## License

MIT — see [TRADEMARK.md](TRADEMARK.md) for name/logo usage.

## Sponsors

Companies supporting this project:

<a href="https://quix.io/"><img src="https://avatars.githubusercontent.com/u/79305112?s=200&v=4" alt="Quix" width="80"></a>
