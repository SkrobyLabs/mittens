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

## Extensions

Built-in: `--ssh`, `--gh`, `--aws`, `--azure`, `--gcp`, `--k8s`, `--dotnet`, `--go`, `--mcp`, `--firewall` (on by default)

External plugins: drop an executable at `~/.mittens/extensions/<name>/plugin` — no recompilation needed.

See [EXTENSIONS.md](EXTENSIONS.md) for the full extension system docs.

## License

MIT
