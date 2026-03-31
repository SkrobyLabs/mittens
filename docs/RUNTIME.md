# mittens Runtime

This document covers the single-session runtime architecture behind `mittens`.

## Container Isolation

The AI CLI runs inside a Docker container with `--cap-drop ALL` unless `--docker dind` is enabled. The workspace is mounted at `/workspace`, and the CLI config is copied from the host at startup.

The container starts as root for setup work such as firewall rules and Docker daemon startup, then drops to a non-root user via `syscall.Setuid/Setgid`. Containers are force-removed on exit.

## Host Broker

Mittens runs a host-side TCP server called `HostBroker` that bridges communication between the host and containers. It handles:

- credential sync
- OAuth login
- URL forwarding
- desktop notifications
- proactive refresh coordination

## Credential Syncing

```text
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

The broker uses a freshest-wins model based on credential expiry timestamps. When the CLI refreshes a token inside the container, the daemon pushes it to the broker, which writes through to the host credential stores. The host side also polls its own stores and pushes newer credentials back when needed.

## OAuth Login

When the AI CLI opens an OAuth URL inside the container:

1. The `xdg-open` shim forwards the URL to the broker via `POST /open`.
2. The broker starts a temporary HTTP listener on the callback port and opens the URL in the host browser.
3. After authentication, the browser redirects to `localhost:<port>`, where the broker captures the callback.
4. The container polls `GET /login-callback` and replays the callback URL to the CLI's local server.

This makes browser login work without manual copy-paste.

## Path Translation

When files are dragged into the terminal, pasted paths refer to the host filesystem. `DropProxy` intercepts bracketed paste sequences and:

- translates host paths to container mount points such as `/workspace/file.go`
- copies files outside mounted paths into `/tmp/mittens-drops`

The AI CLI sees only container-valid paths.

## Network Firewall

The firewall is enabled by default unless `--no-firewall` is used. Mittens runs a built-in Go forward proxy plus iptables rules to restrict outbound HTTP and HTTPS to an allowlist.

The default whitelist includes provider APIs, major source hosts, package registries, Docker registries, Helm, and Terraform. Extensions can add domains, and MCP server domains are resolved from config plus `mcp-domains.conf`. SSH on port 22 bypasses the proxy.

Use `--firewall-dev` for a broader developer-focused allowlist.

## Docker In Docker

`--docker dind` runs the container in privileged mode with a dedicated Docker volume. Mittens starts a separate `dockerd` inside the container so the AI can build and run containers during its work. The DinD volume is named `<container>-docker` and cleaned up on exit.

## Worktree Isolation

`--worktree` creates a detached-HEAD git worktree per invocation so the AI works on a copy rather than the primary checkout. The worktree is removed on exit if it is clean and left behind if it is dirty.

Extra directories added with `--dir` get their own worktrees when possible. Worktrees created inside the container only persist if they live under `/workspace` or another read-write mounted path. Sibling worktrees such as `../feature` are outside the bind mount and disappear with the container. Read-only mounts from `--dir-ro` cannot host worktrees.

## Model Profiles

`--profile NAME` selects a saved model and effort preset. Profiles are per-provider and per-project.

```bash
mittens --profile planner
mittens init --profile fast
mittens init --profile planner --delete
```

If profiles exist for the current provider and no profile is specified, mittens shows a picker at startup.

## Session Persistence

Conversation history is enabled by default unless `--no-history` is used. To resume a previous session, pass `--resume` or `--resume SESSION_ID` after the `--` separator (e.g. `mittens -- --resume`).

## Clipboard And Image Sync

On macOS, a host-side script polls the clipboard for images and writes PNGs to a mounted temp directory. An `xclip` shim inside the container reads those images so the AI CLI can consume them.

For `--provider codex` on macOS, mittens also starts an `Xvfb` display in the container and mirrors the synced PNG into the X11 clipboard using a real `xclip` process so Codex can read it through its Linux clipboard backend.

## Notifications

When the AI CLI triggers a hook event such as completion or a permission prompt, the container sends a notification to the broker via `POST /notify`. The host displays a desktop notification and re-focuses the terminal window that launched mittens.

## URL Forwarding

The container's `xdg-open` is replaced with a shim that forwards all URLs to the host browser through `POST /open`. This is used for both OAuth flows and ordinary browser links.
