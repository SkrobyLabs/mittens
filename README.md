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

## Supported Platforms

| Platform | How it works |
|----------|-------------|
| **macOS** | Native binary, Keychain credential sync, clipboard image sync |
| **Linux** | Native binary, file-based credential store |
| **Windows** | `mittens.exe` shim auto-delegates to the real binary via WSL |

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

## Quick Start

```bash
cd your-project
mittens                  # run in a container — that's it
mittens init             # interactive project setup (extensions, dirs, firewall)
mittens init --defaults  # set user-wide defaults (provider, firewall, paste key)
mittens init --help      # see all init subcommands
mittens help             # see all flags and commands
```

Project configs are saved to `~/.mittens/projects/` — one flag per line, loaded automatically next time.

## Providers

Mittens supports multiple AI coding agents via `--provider`:

| Provider | Flag |
|----------|------|
| **Claude** (default) | `--provider claude` |
| **Codex** | `--provider codex` |
| **Gemini** | `--provider gemini` |

Each provider brings its own credential layout, firewall domains, CLI flags, and config format. Mittens handles all the differences — same workflow regardless of provider.

That provider selection also applies to `mittens team`: for example, `mittens team --provider codex` launches a Codex-led team session, while `team.yaml` continues to control worker routing.

## How It Works

Mittens is built around a few core pieces:

- The AI CLI runs inside a Docker container with a mounted workspace and copied config
- A host-side `HostBroker` bridges credentials, browser login, URL opening, notifications, and refresh coordination
- Network access is firewalled by default to a curated allowlist
- Optional DinD support lets the agent build and run containers in-container
- Optional worktree isolation keeps agent changes off the primary checkout
- Conversation history, model profiles, and project config persist under `~/.mittens/projects/`

The detailed runtime architecture, credential syncing, OAuth flow, clipboard sync, path translation, and related internals are documented in [docs/RUNTIME.md](docs/RUNTIME.md).

Run `mittens help` for all flags and commands, or `mittens init --help` for setup subcommands.

## Debugging

- `mittens logs [-f]` — view broker logs (credential sync, OAuth intercept, URL forwarding). `-f` follows the log.
- `--verbose` — prints the full `docker run` command so you can see all mounts, env vars, and flags.
- `--shell` — drops into a bash shell inside the container for manual inspection.

## Documentation

- [docs/RUNTIME.md](docs/RUNTIME.md) — runtime architecture, broker flows, firewall, worktrees, profiles, and session persistence
- [docs/TEAMS.md](docs/TEAMS.md) — multi-agent team sessions, worker orchestration, pipelines, and recovery
- [docs/EXTENSIONS.md](docs/EXTENSIONS.md) — built-in and external extension architecture
- [docs/LOCAL-PROVIDER.md](docs/LOCAL-PROVIDER.md) — running against a local model provider

## Extensions

Built-in: `--ssh`, `--gh`, `--aws`, `--azure`, `--gcp`, `--k8s`, `--helm`, `--docker`, `--dotnet`, `--go`, `--python`, `--rust`, `--mcp`, `--firewall` (on by default)

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
