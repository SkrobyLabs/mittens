# Mittens

Run [Claude Code](https://docs.anthropic.com/en/docs/claude-code) in an isolated Docker container with credential forwarding, network firewall, and pluggable extensions.

Claude Code runs commands on your host. Mittens puts mittens on it — containerised, firewalled, with only the credentials you choose to pass in.

## Requirements

- Docker
- Go 1.23+ (to build from source)

## Install

```bash
git clone https://github.com/Skroby/mittens.git
cd mittens
make build
make install  # → /usr/local/bin/mittens (or PREFIX=~/.local)
```

## Quick Start

```bash
cd your-project
mittens              # run Claude Code in a container
mittens init         # interactive setup wizard
mittens --ssh        # forward SSH keys
mittens --aws prod   # mount specific AWS profile
mittens --dind       # enable Docker-in-Docker
mittens --yolo       # skip all permission prompts
mittens --help       # see all flags
```

Project configs are saved to `~/.mittens/projects/` — one flag per line, loaded automatically next time.

## How It Works

### Container Isolation

Claude Code runs inside a Docker container with `--cap-drop ALL` (unless `--dind` is used). The workspace is mounted at `/workspace` and Claude's config is copied from the host at startup.

The container starts as root for initial setup (firewall rules, Docker daemon), then drops to a non-root `claude` user via `gosu`. Containers are force-removed on exit — each invocation is ephemeral.

### Credential Syncing

Mittens runs a TCP credential broker on the host that keeps OAuth tokens synchronized between the host and all running containers.

```
Host                              Container
┌──────────────┐                  ┌──────────────┐
│  Keychain /  │◄── pull ────────►│  cred-sync   │
│  file store  │    (5s poll)     │  daemon      │
└──────┬───────┘                  └──────┬───────┘
       │                                 │
       ▼                                 ▼
┌──────────────┐   PUT (fresher)  ┌──────────────┐
│   Broker     │◄────────────────►│ .credentials │
│   (TCP)      │   GET (latest)   │    .json     │
└──────────────┘                  └──────────────┘
```

The broker uses a **freshest-wins** model: each credential set has an `expiresAt` timestamp, and only newer tokens replace older ones. When Claude Code refreshes a token inside the container, the daemon pushes it to the broker, which writes it through to the host's credential stores. The host side polls its stores (Keychain on macOS, file stores everywhere) and updates the broker if the host has fresher credentials.

### OAuth Login

When Claude Code opens an OAuth URL inside the container:

1. The `xdg-open` shim forwards the URL to the broker via `POST /open`
2. The broker starts a temporary HTTP listener on the OAuth callback port and opens the URL in the host browser
3. After the user authenticates, the browser redirects to `localhost:<port>` — the broker captures the callback
4. The container polls `GET /login-callback` and replays the callback URL to Claude Code's local server

Result: seamless `/login` without manual copy-paste between host and container.

### Network Firewall

Enabled by default (`--no-firewall` to disable). Uses Squid proxy + iptables to restrict outbound HTTP/HTTPS to whitelisted domains only.

Default whitelist includes: Claude API, GitHub/GitLab/Bitbucket, npm/PyPI/crates.io/Go proxy, Docker registries, Helm, and Terraform.

Extensions declare additional domains (e.g. AWS endpoints when `--aws` is enabled). MCP server domains are auto-resolved from `~/.claude.json` config and the built-in `mcp-domains.conf` mapping file. SSH traffic (port 22) bypasses the proxy entirely.

### Docker-in-Docker

`--dind` runs the container in `--privileged` mode with a dedicated Docker volume. A separate `dockerd` starts inside the container, allowing Claude to build and run containers as part of its work. The DinD volume is named `<container>-docker` and cleaned up on exit.

### Worktree Isolation

`--worktree` creates a detached-HEAD git worktree for each invocation, so Claude works on a copy instead of the primary working tree. On exit, the worktree is removed if clean (no changes, no new commits) or kept if dirty. Extra directories (`--dir`) also get their own worktrees when possible.

### Session Persistence

Enabled by default (`--no-history` to disable). Conversation history is persisted at `~/.claude/projects/<id>/` and mounted into the container. Mittens auto-resumes the last session on the next launch (`--no-resume` to start fresh).

### Clipboard & Image Sync (macOS)

On macOS, a host-side script polls the clipboard for images every second and writes PNGs to a temp directory mounted into the container. An `xclip` shim inside the container reads these images, allowing Claude Code to access clipboard images.

### URL Forwarding

The container's `xdg-open` is replaced with a shim that forwards all URLs to the host browser via the broker's `POST /open` endpoint. This works for any URL Claude Code tries to open, not just OAuth flows.

## Core Flags

| Flag | Description |
|------|-------------|
| `--verbose`, `-v` | Show the full docker command being run |
| `--dind` | Enable Docker-in-Docker (`--privileged`) |
| `--yolo` | Skip all Claude Code permission prompts |
| `--network-host` | Use host networking instead of bridge + firewall |
| `--worktree` | Git worktree isolation per invocation |
| `--shell` | Start a bash shell instead of Claude Code |
| `--dir PATH` | Mount an additional directory (repeatable) |
| `--no-config` | Skip loading project config file |
| `--no-history` | Disable session persistence (fully ephemeral) |
| `--no-resume` | Start a new session instead of continuing the last |
| `--no-build` | Skip the Docker image build step |

Unrecognised flags are forwarded to Claude Code (e.g. `--resume`, `--model`, `--print`).

## Debugging

- `mittens logs [-f]` — view broker logs (credential sync, OAuth intercept, URL forwarding). `-f` follows the log.
- `--verbose` — prints the full `docker run` command so you can see all mounts, env vars, and flags.
- `--shell` — drops into a bash shell inside the container for manual inspection.

## Extensions

Built-in: `--ssh`, `--gh`, `--aws`, `--azure`, `--gcp`, `--k8s`, `--dotnet`, `--go`, `--mcp`, `--firewall` (on by default)

External plugins: drop an executable at `~/.mittens/extensions/<name>/plugin` — no recompilation needed.

See [EXTENSIONS.md](EXTENSIONS.md) for the full extension system docs.

## License

MIT
