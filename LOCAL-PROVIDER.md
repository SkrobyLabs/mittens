# Using Mittens with a Local LLM

Run Codex inside mittens against a local model (Ollama, LM Studio, etc.) on your LAN instead of OpenAI's cloud.

## Prerequisites

- A machine on your LAN running an OpenAI-compatible API (e.g. Ollama)
- Mittens built with `--provider codex` support

## Server Setup (Mac with Apple Silicon)

### 1. Install Ollama

```bash
brew install ollama
```

### 2. Pull a coding model

```bash
ollama pull qwen3-coder
```

`qwen3-coder` is a 30B MoE model with only 3B active parameters -- fast on Apple Silicon with good coding performance. Requires ~18GB disk and runs entirely on GPU with 32GB+ unified memory.

### 3. Start as a LAN-accessible service

Ollama defaults to `127.0.0.1` (localhost only). To serve on the LAN, create a launchd service:

```bash
cat > ~/Library/LaunchAgents/com.ollama.serve.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ollama.serve</string>
    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/bin/ollama</string>
        <string>serve</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>OLLAMA_HOST</key>
        <string>0.0.0.0</string>
        <key>HOME</key>
        <string>$HOME</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/ollama-serve.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/ollama-serve.log</string>
</dict>
</plist>
EOF

# Fix the HOME path (launchd doesn't expand $HOME in plists)
sed -i '' "s|\$HOME|$HOME|" ~/Library/LaunchAgents/com.ollama.serve.plist
```

Load the service:

```bash
launchctl load ~/Library/LaunchAgents/com.ollama.serve.plist
```

Verify it's running:

```bash
curl -s http://$(hostname -I | awk '{print $1}'):11434/api/tags | python3 -m json.tool
```

### Service management

```bash
# Stop
launchctl unload ~/Library/LaunchAgents/com.ollama.serve.plist

# Start
launchctl load ~/Library/LaunchAgents/com.ollama.serve.plist

# Restart
launchctl unload ~/Library/LaunchAgents/com.ollama.serve.plist && launchctl load ~/Library/LaunchAgents/com.ollama.serve.plist

# Logs
tail -f /tmp/ollama-serve.log
```

The service starts automatically on login (`RunAtLoad`) and restarts if it crashes (`KeepAlive`).

## Client Setup (Mittens)

### 1. Configure Codex

Create or update `~/.codex/config.toml`:

```toml
model = "qwen3-coder"
```

No `model_providers` section is needed -- the base URL is passed via environment variable.

### 2. Run Mittens

```bash
OPENAI_API_KEY=sk-local-0 \
OPENAI_BASE_URL=http://<server-ip>:11434/v1 \
mittens --provider codex --network-host --no-firewall
```

Replace `<server-ip>` with your Ollama server's LAN IP (e.g. `10.0.26.137`).

Flags explained:
- `--network-host` -- gives the container direct LAN access (required to reach the local server)
- `--no-firewall` -- disables the HTTP proxy (which only allows ports 80/443 and would block port 11434)
- `OPENAI_API_KEY` -- Ollama doesn't require a real key, but Codex expects one to be set. Any non-empty value works.
- `OPENAI_BASE_URL` -- points Codex at your Ollama server's OpenAI-compatible endpoint

When `OPENAI_BASE_URL` is set, mittens automatically skips OAuth credential staging to avoid stale token refresh failures.

### 3. Save as project config (optional)

To avoid typing the flags every time:

```bash
mittens init
# Or manually create the config:
mkdir -p ~/.mittens/projects/<project-name>
cat > ~/.mittens/projects/<project-name>/config << 'EOF'
--provider codex
--network-host
--no-firewall
EOF
```

Then run with just the env vars:

```bash
OPENAI_API_KEY=sk-local-0 OPENAI_BASE_URL=http://<server-ip>:11434/v1 mittens
```

## Troubleshooting

**"timed out waiting for cloud requirements"** -- Codex is trying to reach OpenAI's servers. Make sure `OPENAI_BASE_URL` is set and the Ollama server is reachable.

**"access token could not be refreshed"** -- Stale OpenAI OAuth tokens in `~/.codex/auth.json`. Delete the file: `rm ~/.codex/auth.json`

**No response to prompts** -- Check that Ollama is running on the server (`curl http://<server-ip>:11434/api/tags`). First request after a cold start takes ~30s while the model loads into GPU memory.

**"Model metadata not found"** -- Expected warning for non-OpenAI models. Safe to ignore.

## Performance

On an M2 Max (32GB), `qwen3-coder` achieves approximately:
- Prompt processing: ~170 tok/s
- Generation: ~68 tok/s

First request has a ~30s cold start while the model loads into GPU memory. Subsequent requests are fast.
