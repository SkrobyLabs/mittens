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
ollama pull gemma4:26b
```

`gemma4:26b` is a 26B MoE model with 3.8B active parameters -- fast on Apple Silicon with good coding performance. Requires ~18GB disk and runs entirely on GPU with 32GB unified memory.

Other options:
- `gemma4:e4b` (9.6GB, 4.5B params) -- smaller, faster, less capable
- `gemma4:31b` (20GB, 31B dense) -- highest quality but requires 64GB+ for comfortable use

### 3. Create a 64K context variant

Codex requires a large context window. Ollama defaults to 4K tokens which is too small. Create a custom variant with 64K context:

```bash
printf 'FROM gemma4:26b\nPARAMETER num_ctx 65536\n' > /tmp/Modelfile-gemma4
ollama create gemma4-26b-64k -f /tmp/Modelfile-gemma4
```

### 4. Start as a LAN-accessible service

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
        <key>OLLAMA_FLASH_ATTENTION</key>
        <string>1</string>
        <key>OLLAMA_KV_CACHE_TYPE</key>
        <string>q8_0</string>
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
personality = "pragmatic"

[model_providers.ollama-remote]
name = "Ollama LAN"
base_url = "http://<server-ip>:11434/v1"
env_key = "OPENAI_API_KEY"
supports_websockets = false
stream_idle_timeout_ms = 600000

[profiles.gemma]
model = "gemma4-26b-64k"
model_provider = "ollama-remote"
```

Replace `<server-ip>` with the Ollama server's LAN IP (e.g. `10.0.26.137`).

Key settings:
- `supports_websockets = false` -- Ollama does not support WebSocket transport; without this, Codex tries WebSocket first and times out
- `stream_idle_timeout_ms = 600000` -- 10 minute timeout; local models with reasoning can be slow on first response
- `env_key = "OPENAI_API_KEY"` -- Ollama doesn't validate keys, but Codex requires one

For local use (Ollama on the same machine), use `http://localhost:11434/v1` as the `base_url`.

### 2. Run Mittens

```bash
OPENAI_API_KEY=sk-local-0 \
mittens --provider codex --network-host --no-firewall -- --profile gemma
```

Flags explained:
- `--network-host` -- gives the container direct LAN access (required to reach the local server)
- `--no-firewall` -- disables the HTTP proxy (which only allows ports 80/443 and would block port 11434)
- `--profile gemma` -- selects the Ollama provider and model (passed through to Codex via `--`)
- `OPENAI_API_KEY` -- Ollama doesn't require a real key, but Codex expects one to be set. Any non-empty value works.

Mittens automatically skips OAuth credential staging when a custom model provider base URL is configured.

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

Then run with just the env var and profile:

```bash
OPENAI_API_KEY=sk-local-0 mittens -- --profile gemma
```

### Alternative: OLLAMA_HOST forwarding

If you prefer Codex's built-in `--oss` mode over a custom provider profile, mittens forwards the `OLLAMA_HOST` environment variable into the container:

```bash
OLLAMA_HOST=http://<server-ip>:11434 \
mittens --provider codex --network-host --no-firewall -- --oss
```

Note: `--oss` mode uses Codex's built-in Ollama integration which auto-pulls models. The profile-based approach above gives you more control over model selection and avoids redundant downloads.

## Troubleshooting

**"timed out waiting for cloud requirements"** -- Codex is trying to reach OpenAI's servers. Make sure a `model_provider` profile is selected or `OPENAI_BASE_URL` is set.

**"access token could not be refreshed"** -- Stale OpenAI OAuth tokens in `~/.codex/auth.json`. Delete the file: `rm ~/.codex/auth.json`

**"405 Method Not Allowed" on /v1/responses** -- Codex is trying WebSocket transport. Set `supports_websockets = false` in your model provider config.

**Very slow responses (minutes)** -- Check memory pressure on the server (`vm_stat`, `sysctl vm.swapusage`). If swapping, the model + KV cache is too large. Use a smaller model or reduce `num_ctx`. The 31B dense model with 64K context requires ~26GB, leaving little room on 32GB machines.

**No response to prompts** -- Check that Ollama is running on the server (`curl http://<server-ip>:11434/api/tags`). First request after a cold start takes ~10s while the model loads into GPU memory.

**"Model metadata not found"** -- Expected warning for non-OpenAI models. Safe to ignore.

## Performance

On an M2 Max (32GB), `gemma4-26b-64k` achieves approximately:
- 200 tokens in ~12s (~17 tok/s generation)
- Reasoning overhead: Gemma 4 generates internal reasoning tokens before the visible response, adding latency

First request has a ~10s cold start while the model loads into GPU memory. Subsequent requests are fast.

### Memory requirements

| Model | Weights | 64K KV cache | Total | Recommended RAM |
|-------|---------|-------------|-------|-----------------|
| gemma4:e4b | ~10GB | ~1GB | ~11GB | 16GB |
| gemma4:26b (MoE) | ~18GB | ~2GB | ~20GB | 32GB |
| gemma4:31b (dense) | ~20GB | ~5GB | ~25GB | 64GB |
