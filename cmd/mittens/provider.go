package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ProfilePreset defines a model + effort combination for a named profile.
type ProfilePreset struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Provider holds all values that identify an AI assistant binary, its config
// layout, settings keys, and install command. Swapping the Provider lets
// mittens drive a different AI CLI without touching orchestration code.
type Provider struct {
	Name           string // short machine name, e.g. "claude"
	DisplayName    string // human-facing name, e.g. "Claude"
	Binary         string // CLI binary name, e.g. "claude"
	Username       string // container username, e.g. "claude"
	InstallCmd     string // shell command to install the CLI in the image
	APIKeyEnv      string // env var name for the API key, e.g. "ANTHROPIC_API_KEY"
	BaseURLEnv     string // env var for custom base URL, e.g. "OPENAI_BASE_URL"; when set, skip OAuth credential staging
	SettingsFormat string // config file format: "json" or "toml"

	// Config layout
	ConfigDir      string // directory name under $HOME, e.g. ".claude"
	CredentialFile string // credential filename inside ConfigDir, e.g. ".credentials.json"
	UserPrefsFile  string // user prefs filename in $HOME, e.g. ".claude.json"
	SettingsFile   string // settings filename inside ConfigDir, e.g. "settings.json"
	ProjectFile    string // per-project instruction file, e.g. "CLAUDE.md"

	// Keychain
	KeychainService string // macOS Keychain service name

	// Firewall
	FirewallDomains   []string // domains the AI CLI needs to reach
	FirewallHostPorts []string // direct host:port endpoints permitted through the firewall
	ImageTagParts     []string // provider policy/runtime variants that affect the image

	// Config subdirs and files to copy into the container
	ConfigSubdirs []string // e.g. ["skills", "hooks", "agents", "output-styles"]
	ConfigFiles   []string // e.g. ["settings.json", "settings.local.json", "CLAUDE.md", "statusline.sh"]

	// Plugin layout
	PluginDir   string   // plugin directory name inside ConfigDir, e.g. "plugins"
	PluginFiles []string // plugin config files to copy, e.g. ["installed_plugins.json", ...]

	// Settings keys (used in jq operations inside the container)
	TrustedDirsKey  string // e.g. "trustedDirectories"
	YoloSettingsJQ  string // jq assignment applied to settings.json when yolo is enabled, e.g. `.permissions.defaultMode = "bypassPermissions"`
	MCPServersKey   string // e.g. "mcpServers"
	MCPConfigFile   string // MCP config file relative to $HOME, e.g. ".claude.json" or ".codex/config.toml"
	MCPConfigFormat string // "json" or "toml"

	// Files (relative to ConfigDir) to persist: copied in on start, copied back on exit.
	// Used for provider state files that must survive between runs (e.g. Gemini auth state).
	PersistFiles   []string
	PersistDirs    []string
	PersistGlobs   []string
	LiveMountFiles []string
	LiveMountDirs  []string

	// CLI flags
	ResumeFlags              []string // flags that mean "resume session", e.g. ["--continue", "-c", "--resume", "-r"]
	SkipPermsFlag            string   // flag to skip permission prompts, e.g. "--dangerously-skip-permissions"
	ContinueArgs             []string // args to prepend when resuming latest session, e.g. ["--continue"] or ["--resume", "latest"]
	TrustedDirsFile          string   // separate JSON array file for trusted dirs (Gemini); empty = unused
	HistoryMountsWholeConfig bool     // mount the provider config dir directly when history is enabled
	HistoryMountsProjectDirs bool
	ModelFlag                string
	EffortFlag               string
	EffortTemplate           string
	ProgressArgs             []string // CLI args appended for --report-progress to stream live events (tool calls, messages); empty = unsupported
	ProgressConflictFlag     string   // if this flag is already present in the agent args, --report-progress leaves output formatting untouched

	// Container settings
	ContainerHostname string            // fixed Docker hostname; empty = Docker default. Required when credential file encryption is hostname-dependent (e.g. Gemini).
	ContainerEnv      map[string]string // extra env vars injected at docker run time; empty value = unset the var.
	DockerArgs        []string          // extra docker run args needed by this provider.
	DefaultArgs       []string          // provider CLI args applied when the user has not supplied equivalent args.
	ManagedProxyCmd   string            // optional command started by mittens-init before the AI CLI.
	ManagedProxyPort  int               // localhost port to wait for when ManagedProxyCmd is set.
	InitSettingsJQ    string            // jq expression applied to settings.json once after all other setup; empty = unused.
	StopHookEvent     string            // hook event name for session end, e.g. "Stop" (Claude) or "SessionEnd" (Gemini); empty = skip stop hook.
	SkipCredentials   bool              // skip provider OAuth/API credential staging.
	LocalModelSource  string            // optional local model detector name, e.g. "ollama".
	DefaultModel      string            // provider default model applied when no --model arg is supplied.
	EnableX11         bool              // install the X11 stack (xvfb/xclip) at build time for clipboard image paste.
}

// HomePath returns the home directory for this provider's user inside the container.
func (p *Provider) HomePath() string {
	return "/home/" + p.Username
}

// ContainerConfigDir returns the full config directory path inside the container.
func (p *Provider) ContainerConfigDir() string {
	return filepath.Join(p.HomePath(), p.ConfigDir)
}

// ContainerCredentialPath returns the full credential file path inside the container.
func (p *Provider) ContainerCredentialPath() string {
	return filepath.Join(p.ContainerConfigDir(), p.CredentialFile)
}

// HostConfigDir returns the config directory path on the host for the given home.
func (p *Provider) HostConfigDir(home string) string {
	return filepath.Join(home, p.ConfigDir)
}

// HostCredentialPath returns the credential file path on the host for the given home.
func (p *Provider) HostCredentialPath(home string) string {
	return filepath.Join(home, p.ConfigDir, p.CredentialFile)
}

// HostUserPrefsPath returns the user prefs file path on the host for the given home.
func (p *Provider) HostUserPrefsPath(home string) string {
	return filepath.Join(home, p.UserPrefsFile)
}

const stagingMountBase = "/mnt/mittens-staging"

// StagingConfigDir returns the staging mount path for the config directory.
func (p *Provider) StagingConfigDir() string {
	return filepath.Join(stagingMountBase, p.ConfigDir)
}

// StagingCredentialPath returns the staging mount path for the credential file.
func (p *Provider) StagingCredentialPath() string {
	return filepath.Join(stagingMountBase, p.CredentialFile)
}

// StagingUserPrefsPath returns the staging mount path for the user prefs file.
func (p *Provider) StagingUserPrefsPath() string {
	return filepath.Join(stagingMountBase, p.UserPrefsFile)
}

// StagingGitconfigPath returns the staging mount path for the .gitconfig file.
func (p *Provider) StagingGitconfigPath() string {
	return filepath.Join(stagingMountBase, ".gitconfig")
}

// StagingFirewallConfPath returns the staging mount path for the firewall config.
func (p *Provider) StagingFirewallConfPath() string {
	return filepath.Join(stagingMountBase, "firewall.conf")
}

// UsingCustomBaseURL reports whether the user has set a custom base URL via
// the provider's BaseURLEnv (e.g. OPENAI_BASE_URL), indicating a local or
// third-party endpoint. When true, OAuth credential staging should be skipped
// since the tokens are for the original provider and will cause refresh failures.
func (p *Provider) UsingCustomBaseURL() bool {
	return p.BaseURLEnv != "" && os.Getenv(p.BaseURLEnv) != ""
}

// IsResumeFlag reports whether the given CLI argument is a resume/continue flag.
func (p *Provider) IsResumeFlag(arg string) bool {
	for _, f := range p.ResumeFlags {
		if arg == f {
			return true
		}
	}
	return false
}

// ClaudeProvider returns a Provider configured for Claude Code.
func ClaudeProvider() *Provider {
	return &Provider{
		Name:           "claude",
		DisplayName:    "Claude",
		Binary:         "claude",
		Username:       "claude",
		InstallCmd:     `curl -fsSL https://claude.ai/install.sh | bash && cp -L /root/.local/bin/claude /usr/local/bin/claude && chmod +x /usr/local/bin/claude && /usr/local/bin/claude --version`,
		APIKeyEnv:      "ANTHROPIC_API_KEY",
		BaseURLEnv:     "ANTHROPIC_BASE_URL",
		SettingsFormat: "json",

		ConfigDir:      ".claude",
		CredentialFile: ".credentials.json",
		UserPrefsFile:  ".claude.json",
		SettingsFile:   "settings.json",
		ProjectFile:    "CLAUDE.md",

		KeychainService: "Claude Code-credentials",

		FirewallDomains: []string{
			"api.anthropic.com",
			"claude.ai",
			"console.anthropic.com",
			"statsig.anthropic.com",
			"sentry.io",
		},

		ConfigSubdirs: []string{"skills", "hooks", "agents", "output-styles"},
		ConfigFiles:   []string{"settings.json", "settings.local.json", "CLAUDE.md", "statusline.sh"},

		PluginDir:   "plugins",
		PluginFiles: []string{"installed_plugins.json", "known_marketplaces.json", "config.json"},

		TrustedDirsKey:  "trustedDirectories",
		YoloSettingsJQ:  `.permissions.defaultMode = "bypassPermissions"`,
		MCPServersKey:   "mcpServers",
		MCPConfigFile:   ".claude.json",
		MCPConfigFormat: "json",

		ResumeFlags:              []string{"--continue", "-c", "--resume", "-r"},
		SkipPermsFlag:            "--dangerously-skip-permissions",
		ContinueArgs:             []string{"--continue"},
		TrustedDirsFile:          "",
		HistoryMountsProjectDirs: true,
		StopHookEvent:            "Stop",
		ModelFlag:                "--model",
		EffortFlag:               "--effort",
		ProgressArgs:             []string{"--output-format", "stream-json", "--verbose"},
		ProgressConflictFlag:     "--output-format",
	}
}

// CodexProvider returns a Provider configured for OpenAI Codex CLI.
func CodexProvider() *Provider {
	return &Provider{
		Name:           "codex",
		DisplayName:    "Codex",
		Binary:         "codex",
		Username:       "codex",
		InstallCmd:     `npm install -g @openai/codex && codex --version`,
		APIKeyEnv:      "OPENAI_API_KEY",
		BaseURLEnv:     "OPENAI_BASE_URL",
		SettingsFormat: "toml",
		EnableX11:      true, // Codex pastes clipboard images via the Xvfb/xclip bridge.

		ConfigDir:      ".codex",
		CredentialFile: "auth.json",
		UserPrefsFile:  "",
		SettingsFile:   "config.toml",
		ProjectFile:    "AGENTS.md",

		KeychainService: "",

		FirewallDomains: []string{
			"api.openai.com",
			"auth.openai.com",
			"chatgpt.com",
			"ab.chatgpt.com",
		},

		ConfigSubdirs: []string{"skills", "hooks", "agents", "output-styles"},
		ConfigFiles:   []string{"config.toml", "AGENTS.md"},

		PluginDir:   "",
		PluginFiles: []string{},

		TrustedDirsKey:  "",
		YoloSettingsJQ:  "",
		MCPServersKey:   "mcp_servers",
		MCPConfigFile:   ".codex/config.toml",
		MCPConfigFormat: "toml",

		ResumeFlags:              []string{"--resume", "-r", "--continue", "-l"},
		SkipPermsFlag:            "--dangerously-bypass-approvals-and-sandbox",
		ContinueArgs:             []string{"--resume", "latest"},
		TrustedDirsFile:          "",
		HistoryMountsProjectDirs: false,
		LiveMountFiles: []string{
			"history.jsonl",
		},
		LiveMountDirs: []string{
			"memories",
			"plans",
			"projects",
			"sessions",
			"tasks",
		},
		ModelFlag:  "--model",
		EffortFlag: "",
		// Codex expects reasoning effort via -c key-value configuration.
		EffortTemplate: "-c model_reasoning_effort=%s",
		// Codex streams JSON events under `codex exec --json`.
		ProgressArgs:         []string{"--json"},
		ProgressConflictFlag: "--json",

		ContainerEnv: map[string]string{
			// codex login opens the auth URL via the Rust webbrowser crate, which
			// tries $BROWSER first and otherwise needs xdg-settings/a desktop
			// environment/x-www-browser — none of which exist in the container.
			// Point it at the broker shim so the URL is forwarded to the host and
			// the OAuth callback intercept gets armed.
			"BROWSER": "/usr/local/bin/xdg-open",
		},
	}
}

// GeminiProvider returns a Provider configured for Google Gemini CLI.
func GeminiProvider() *Provider {
	return &Provider{
		Name:        "gemini",
		DisplayName: "Gemini",
		Binary:      "gemini",
		Username:    "gemini",
		// After installing, strip the execute bit from open's bundled xdg-open so
		// it falls back to the system xdg-open (our broker shim at /usr/local/bin/xdg-open).
		// Without this, the 'open' npm package bypasses our shim and tries to use
		// a real desktop browser (fails in container).
		// Post-install: prune non-linux prebuilds (~65MB) and TS artifacts (~20MB),
		// then pre-warm the V8 compile cache for ~3-4x faster cold start.
		InstallCmd: `npm install -g @google/gemini-cli` +
			` && find /usr/local/lib/node_modules -path "*/open/xdg-open" -exec chmod -x {} \;` +
			` && find /usr/local/lib/node_modules/@google/gemini-cli/node_modules -path "*/prebuilds/darwin*" -exec rm -rf {} + 2>/dev/null;` +
			` find /usr/local/lib/node_modules/@google/gemini-cli/node_modules -path "*/prebuilds/win32*" -exec rm -rf {} + 2>/dev/null;` +
			` find /usr/local/lib/node_modules/@google/gemini-cli/node_modules \( -name "*.d.ts" -o -name "*.d.mts" -o -name "*.map" \) -delete 2>/dev/null;` +
			` mkdir -p /usr/local/lib/node_modules/@google/gemini-cli/.v8-compile-cache` +
			` && NODE_COMPILE_CACHE=/usr/local/lib/node_modules/@google/gemini-cli/.v8-compile-cache gemini --version`,
		APIKeyEnv:      "GEMINI_API_KEY",
		BaseURLEnv:     "",
		SettingsFormat: "json",

		ConfigDir:      ".gemini",
		CredentialFile: "oauth_creds.json",
		UserPrefsFile:  "",
		SettingsFile:   "settings.json",
		ProjectFile:    "GEMINI.md",

		KeychainService: "",

		FirewallDomains: []string{
			"generativelanguage.googleapis.com",
			"accounts.google.com",
			"oauth2.googleapis.com",
			"www.googleapis.com",
			"cloudcode-pa.googleapis.com",
			"codeassist.google.com",
			"play.googleapis.com",
		},

		ConfigSubdirs: []string{"skills", "hooks", "agents", "output-styles"},
		ConfigFiles:   []string{"settings.json", "GEMINI.md"},

		PluginDir:   "",
		PluginFiles: []string{},

		TrustedDirsKey:  "",
		YoloSettingsJQ:  "",
		MCPServersKey:   "mcpServers",
		MCPConfigFile:   ".gemini/settings.json",
		MCPConfigFormat: "json",
		TrustedDirsFile: "trustedFolders.json",

		ResumeFlags:              []string{"--resume", "-r"},
		SkipPermsFlag:            "--approval-mode=yolo",
		ContinueArgs:             []string{"--resume", "latest"},
		ModelFlag:                "--model",
		HistoryMountsProjectDirs: true,
		ProgressArgs:             []string{"--output-format", "stream-json"},
		ProgressConflictFlag:     "--output-format",

		ContainerHostname: "gemini-cli",
		ContainerEnv: map[string]string{
			// shouldLaunchBrowser() in secure-browser-launcher.js returns false when
			// DEBIAN_FRONTEND=noninteractive (set by Dockerfile) or when no display
			// variable is set on Linux. Both must be overridden so Gemini calls xdg-open
			// (our broker shim) instead of just printing the URL.
			"DEBIAN_FRONTEND":    "",   // unset the Dockerfile build-time value
			"DISPLAY":            ":0", // fake display → hasDisplay = true on Linux
			"COLORTERM":          "truecolor",
			"NODE_COMPILE_CACHE": "/usr/local/lib/node_modules/@google/gemini-cli/.v8-compile-cache",
			// Skip sandbox detection and child-process relaunch. Without this,
			// Gemini always forks a child process, loading 506MB of modules twice.
			"SANDBOX": "true",
			// Disable OpenTelemetry SDK to prevent gRPC connections that bypass
			// the HTTP proxy and cause iptables-blocked SYN timeouts (~60s each).
			"OTEL_SDK_DISABLED": "true",
		},
		InitSettingsJQ: `.general.enableAutoUpdate = false | .general.enableAutoUpdateNotification = false`,
		StopHookEvent:  "SessionEnd",

		// Persist auth state and identity so subsequent runs skip OAuth + re-onboarding.
		// settings.json: contains security.auth.selectedType (skips auth-type prompt).
		// google_accounts.json: authenticated Google account identity.
		// installation_id: unique instance ID used by the onboarding LRO check.
		// projects.json: workspace → project name mapping (skips project name prompt).
		PersistFiles: []string{"settings.json", "google_accounts.json", "installation_id", "projects.json"},
	}
}

// OllamaProvider returns a Provider that uses Codex as the harness while
// directing model requests to a locally hosted Ollama server.
func OllamaProvider() *Provider {
	p := CodexProvider()
	ollamaHost := ollamaHostURL()
	p.Name = "ollama"
	p.DisplayName = "Ollama (Codex)"
	p.APIKeyEnv = ""
	p.BaseURLEnv = ""
	p.FirewallDomains = nil
	p.ContainerEnv = map[string]string{
		"CODEX_OSS_BASE_URL": envOrDefault("CODEX_OSS_BASE_URL", ollamaOpenAIBaseURL(ollamaHost)),
		"OLLAMA_HOST":        ollamaHost,
		"OPENAI_API_KEY":     envOrDefault("OPENAI_API_KEY", "sk-local-0"),
	}
	p.DockerArgs = []string{"--add-host", "host.docker.internal:host-gateway"}
	p.DefaultArgs = []string{"--oss", "--local-provider", "ollama"}
	p.SkipCredentials = true
	p.LocalModelSource = "ollama"
	return p
}

func (p *Provider) ApplyPolicy(policy ProviderPolicy) {
	if p == nil {
		return
	}
	if policy.Model != "" {
		p.DefaultModel = policy.Model
	}
	if p.Name == "claude" && canonicalProviderBackend(policy.Backend) == "openai" {
		p.DisplayName = "Claude (OpenAI proxy)"
		p.ContainerEnv = ensureStringMap(p.ContainerEnv)
		p.ContainerEnv["ANTHROPIC_API_KEY"] = envOrDefault("ANTHROPIC_API_KEY", "sk-proxy-0")
		p.ContainerEnv["ANTHROPIC_DEFAULT_FABLE_MODEL"] = "fable[1m]"
		p.ContainerEnv["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "opus[1m]"
		p.ContainerEnv["ANTHROPIC_DEFAULT_SONNET_MODEL"] = "sonnet[1m]"
		p.ContainerEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "haiku"
		p.ContainerEnv["ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES"] = "effort,xhigh_effort,max_effort,adaptive_thinking,interleaved_thinking"
		p.ContainerEnv["ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES"] = "effort,xhigh_effort,max_effort,adaptive_thinking,interleaved_thinking"
		p.ContainerEnv["ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES"] = "effort,xhigh_effort,adaptive_thinking,interleaved_thinking"
		p.ContainerEnv["ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES"] = "effort,adaptive_thinking"
		if policy.Endpoint == "" {
			endpoint := managedClaudeOpenAIProxyURL()
			if p.DefaultModel == "" {
				p.DefaultModel = defaultClaudeOpenAIProxyAlias()
			}
			p.ContainerEnv["ANTHROPIC_BASE_URL"] = endpoint
			if v := os.Getenv("OPENAI_API_KEY"); v != "" {
				p.ContainerEnv["OPENAI_API_KEY"] = v
			}
			if authPath := hostCodexAuthPath(); codexAuthFileHasManagedOpenAIToken(authPath) {
				p.ContainerEnv["MITTENS_OPENAI_AUTH_FILE"] = "/mnt/mittens-openai-auth.json"
				p.DockerArgs = appendMissingDockerArgPair(p.DockerArgs, "-v", authPath+":/mnt/mittens-openai-auth.json:ro")
			}
			if v := os.Getenv("OPENAI_BASE_URL"); v != "" {
				p.ContainerEnv["OPENAI_BASE_URL"] = v
				if domain := urlHostname(v); domain != "" {
					p.FirewallDomains = appendMissingString(p.FirewallDomains, domain)
				}
			}
			if v := os.Getenv("MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL"); v != "" {
				p.ContainerEnv["MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL"] = v
			}
			if v := os.Getenv("MITTENS_CLAUDE_OPENAI_REASONING_EFFORT"); v != "" {
				p.ContainerEnv["MITTENS_CLAUDE_OPENAI_REASONING_EFFORT"] = v
			}
			p.ContainerEnv["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
			p.ContainerEnv["API_TIMEOUT_MS"] = "3000000"
			p.ContainerEnv["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = "50000"
			p.ContainerEnv["CLAUDE_BASH_NO_LOGIN"] = "1"
			p.FirewallDomains = appendMissingString(p.FirewallDomains, "api.openai.com")
			p.FirewallDomains = appendMissingString(p.FirewallDomains, "chatgpt.com")
			p.ImageTagParts = appendMissingString(p.ImageTagParts, "openai-proxy")
			p.InstallCmd = p.InstallCmd + claudeOpenAIProxyInstallSuffix()
			p.ManagedProxyCmd = claudeOpenAIProxyCommand(policy.Model)
			p.ManagedProxyPort = 9223
		} else {
			endpoint := normalizeClaudeOpenAIProxyURL(policy.Endpoint)
			p.ContainerEnv["ANTHROPIC_BASE_URL"] = endpoint
			p.DockerArgs = appendMissingDockerArgPair(p.DockerArgs, "--add-host", "host.docker.internal:host-gateway")
			if hostPort := endpointFirewallHostPort(endpoint); hostPort != "" {
				p.FirewallHostPorts = appendMissingString(p.FirewallHostPorts, hostPort)
			}
		}
		p.SkipCredentials = true
	}
	if p.Name == "ollama" && policy.Endpoint != "" {
		host := normalizeOllamaURL(policy.Endpoint)
		p.ContainerEnv["OLLAMA_HOST"] = host
		p.ContainerEnv["CODEX_OSS_BASE_URL"] = envOrDefault("CODEX_OSS_BASE_URL", ollamaOpenAIBaseURL(host))
	}
}

func canonicalProviderBackend(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "claude", "anthropic", "native":
		return "claude"
	case "openai", "proxy", "openai-proxy":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(backend))
	}
}

func normalizeClaudeOpenAIProxyURL(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "http://host.docker.internal:9223"
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	return endpoint
}

func managedClaudeOpenAIProxyURL() string {
	return "http://127.0.0.1:9223"
}

func defaultClaudeOpenAIProxyAlias() string {
	return "opus"
}

func claudeOpenAIProxyInstallSuffix() string {
	return strings.Join([]string{
		` && git clone --depth 1 https://github.com/vibheksoni/UniClaudeProxy.git /opt/uniclaudeproxy && python3 -m venv /opt/uniclaudeproxy/.venv && /opt/uniclaudeproxy/.venv/bin/pip install --no-cache-dir -r /opt/uniclaudeproxy/requirements.txt && touch /opt/uniclaudeproxy/config.json /opt/uniclaudeproxy/debug.log && chmod 666 /opt/uniclaudeproxy/config.json /opt/uniclaudeproxy/debug.log && \`,
		`python3 - <<'PY'`,
		`from pathlib import Path`,
		`path = Path("/opt/uniclaudeproxy/app/main.py")`,
		`s = path.read_text()`,
		`if "import httpx" not in s:`,
		`    s = s.replace("import json\n", "import json\nimport httpx\n", 1)`,
		`s = s.replace("debug_logger.setLevel(logging.DEBUG)", "debug_logger.setLevel(logging.WARNING)")`,
		`provider = Path("/opt/uniclaudeproxy/app/providers/openai_provider.py")`,
		`ps = provider.read_text()`,
		`store_patch = '    if route.use_responses:\n        body["store"] = False\n\n'`,
		`if 'body["store"] = False' not in ps:`,
		`    old_store = '    if route.strip_tool_choice:\n        body.pop("tool_choice", None)'`,
		`    if old_store in ps:`,
		`        ps = ps.replace(old_store, store_patch + old_store, 1)`,
		`        provider.write_text(ps)`,
		`    elif "\n    return body" in ps:`,
		`        ps = ps.replace("\n    return body", "\n" + store_patch + "    return body", 1)`,
		`        provider.write_text(ps)`,
		`    else:`,
		`        print("[mittens] warning: could not patch UniClaudeProxy store=false request body", flush=True)`,
		`old = r"""    except Exception as e:`,
		`        logger.error("Provider request failed: %s\n%s", e, traceback.format_exc())`,
		`        return JSONResponse(`,
		`            status_code=502,`,
		`            content={`,
		`                "type": "error",`,
		`                "error": {"type": "api_error", "message": f"Provider error: {e}"},`,
		`            },`,
		`        )`,
		`"""`,
		`new = r"""    except httpx.HTTPStatusError as e:`,
		`        status = e.response.status_code if e.response is not None else 502`,
		`        logger.error("Provider request failed: %s\n%s", e, traceback.format_exc())`,
		`        if status in (401, 403):`,
		`            return JSONResponse(`,
		`                status_code=status,`,
		`                content={`,
		`                    "type": "error",`,
		`                    "error": {"type": "authentication_error", "message": "OpenAI upstream authentication failed. Check the configured OpenAI credential."},`,
		`                },`,
		`            )`,
		`        return JSONResponse(`,
		`            status_code=502,`,
		`            content={`,
		`                "type": "error",`,
		`                "error": {"type": "api_error", "message": f"Provider error: {e}"},`,
		`            },`,
		`        )`,
		`    except Exception as e:`,
		`        logger.error("Provider request failed: %s\n%s", e, traceback.format_exc())`,
		`        return JSONResponse(`,
		`            status_code=502,`,
		`            content={`,
		`                "type": "error",`,
		`                "error": {"type": "api_error", "message": f"Provider error: {e}"},`,
		`            },`,
		`        )`,
		`"""`,
		`if "except httpx.HTTPStatusError as e:" not in s:`,
		`    if old in s:`,
		`        s = s.replace(old, new, 1)`,
		`        path.write_text(s)`,
		`    else:`,
		`        print("[mittens] warning: could not patch UniClaudeProxy HTTP error handling", flush=True)`,
		`PY`,
	}, "\n")
}

func claudeOpenAIProxyCommand(modelAlias string) string {
	alias := strings.TrimSpace(modelAlias)
	if alias == "" {
		alias = defaultClaudeOpenAIProxyAlias()
	}
	aliasJSON := shellSingleQuote(alias)
	return strings.Join([]string{
		`set -eu`,
		`export api_key="${OPENAI_API_KEY:-}"`,
		`if [ -z "$api_key" ] && [ -n "${MITTENS_OPENAI_AUTH_FILE:-}" ] && [ -f "$MITTENS_OPENAI_AUTH_FILE" ]; then`,
		`  api_key="$(python3 - <<'PY'`,
		`import json, os`,
		`path = os.environ.get("MITTENS_OPENAI_AUTH_FILE", "")`,
		`try:`,
		`    with open(path, encoding="utf-8") as f:`,
		`        data = json.load(f)`,
		`except Exception:`,
		`    data = {}`,
		`tokens = data.get("tokens") if isinstance(data.get("tokens"), dict) else {}`,
		`source = ""`,
		`base_url = os.environ.get("OPENAI_BASE_URL") or "https://api.openai.com/v1"`,
		`account_id = ""`,
		`upstream_default = ""`,
		`reasoning_effort = ""`,
		`token = data.get("OPENAI_API_KEY") or ""`,
		`if token:`,
		`    source = "OPENAI_API_KEY in ~/.codex/auth.json"`,
		`else:`,
		`    if data.get("auth_mode") == "chatgpt":`,
		`        token = tokens.get("access_token") or data.get("access_token") or ""`,
		`        if token:`,
		`            source = "ChatGPT/Codex access_token in ~/.codex/auth.json"`,
		`            if "OPENAI_BASE_URL" not in os.environ:`,
		`                base_url = "https://chatgpt.com/backend-api/codex"`,
		`            account_id = tokens.get("account_id") or data.get("account_id") or ""`,
		`print(source)`,
		`print(token)`,
		`print(base_url)`,
		`print(account_id)`,
		`print(upstream_default)`,
		`print(reasoning_effort)`,
		`PY`,
		`)"`,
		`  api_key_source="$(printf '%s\n' "$api_key" | sed -n '1p')"`,
		`  openai_base_url="$(printf '%s\n' "$api_key" | sed -n '3p')"`,
		`  chatgpt_account_id="$(printf '%s\n' "$api_key" | sed -n '4p')"`,
		`  upstream_model_default="$(printf '%s\n' "$api_key" | sed -n '5p')"`,
		`  upstream_reasoning_effort="$(printf '%s\n' "$api_key" | sed -n '6p')"`,
		`  api_key="$(printf '%s\n' "$api_key" | sed -n '2p')"`,
		`fi`,
		`if [ -n "${OPENAI_API_KEY:-}" ]; then api_key_source="OPENAI_API_KEY"; fi`,
		`export api_key`,
		`export api_key_source`,
		`if [ -z "$api_key" ]; then echo "[mittens] OpenAI credential not found. Set OPENAI_API_KEY, add OPENAI_API_KEY to ~/.codex/auth.json, or run 'codex login' so ~/.codex/auth.json contains a ChatGPT access_token." >&2; exit 1; fi`,
		`export openai_base_url="${openai_base_url:-${OPENAI_BASE_URL:-https://api.openai.com/v1}}"`,
		`export chatgpt_account_id="${chatgpt_account_id:-}"`,
		`export upstream_model="${MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL:-${upstream_model_default:-}}"`,
		`export upstream_reasoning_effort="${MITTENS_CLAUDE_OPENAI_REASONING_EFFORT:-${upstream_reasoning_effort:-}}"`,
		`export alias_model=` + aliasJSON,
		`cd /opt/uniclaudeproxy`,
		`python3 - <<'PY'`,
		`import json, os`,
		`alias_model = os.environ["alias_model"]`,
		`upstream_override = os.environ.get("upstream_model") or ""`,
		`effort_override = os.environ.get("upstream_reasoning_effort") or ""`,
		`route_specs = {`,
		`    "fable": ("gpt-5.5", "xhigh"),`,
		`    "fable[1m]": ("gpt-5.5", "xhigh"),`,
		`    "claude-fable-5": ("gpt-5.5", "xhigh"),`,
		`    "claude-fable-5[1m]": ("gpt-5.5", "xhigh"),`,
		`    "opus": ("gpt-5.5", "medium"),`,
		`    "opus[1m]": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-8": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-8[1m]": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-7": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-7[1m]": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-6": ("gpt-5.5", "medium"),`,
		`    "claude-opus-4-6[1m]": ("gpt-5.5", "medium"),`,
		`    "sonnet": ("gpt-5.5", "low"),`,
		`    "sonnet[1m]": ("gpt-5.5", "low"),`,
		`    "claude-sonnet-4-6": ("gpt-5.5", "low"),`,
		`    "claude-sonnet-4-6[1m]": ("gpt-5.5", "low"),`,
		`    "claude-sonnet-4-5": ("gpt-5.5", "low"),`,
		`    "claude-sonnet-4-5[1m]": ("gpt-5.5", "low"),`,
		`    "haiku": ("gpt-5.4-mini", "low"),`,
		`    "claude-haiku-4-5": ("gpt-5.4-mini", "low"),`,
		`}`,
		`def route_key(alias, upstream, effort):`,
		`    return f"{alias}-{upstream}-{effort or 'default'}"`,
		`models = {}`,
		`provider_models = {}`,
		`for alias, (default_upstream, default_effort) in route_specs.items():`,
		`    upstream = upstream_override or default_upstream`,
		`    effort = effort_override or default_effort`,
		`    key = route_key(alias, upstream, effort)`,
		`    models[alias] = f"openai/{key}"`,
		`    model_config = {"name": key, "upstream_model_id": upstream, "responses": True}`,
		`    if effort:`,
		`        model_config["reasoning"] = {"effort": effort}`,
		`    provider_models[key] = model_config`,
		`if alias_model not in models:`,
		`    upstream = upstream_override or route_specs["opus"][0]`,
		`    effort = effort_override or route_specs["opus"][1]`,
		`    key = route_key(alias_model, upstream, effort)`,
		`    models[alias_model] = f"openai/{key}"`,
		`    provider_models[key] = {"name": key, "upstream_model_id": upstream, "responses": True}`,
		`    if effort:`,
		`        provider_models[key]["reasoning"] = {"effort": effort}`,
		`headers = {}`,
		`if os.environ.get("chatgpt_account_id"):`,
		`    headers["ChatGPT-Account-ID"] = os.environ["chatgpt_account_id"]`,
		`config = {`,
		`    "server": {"host": "127.0.0.1", "port": 9223, "local_only": True},`,
		`    "models": models,`,
		`    "providers": {`,
		`        "openai": {`,
		`            "provider_type": "openai",`,
		`            "api_key": os.environ["api_key"],`,
		`            "base_url": os.environ["openai_base_url"],`,
		`            "headers": headers,`,
		`            "models": provider_models,`,
		`        }`,
		`    },`,
		`}`,
		`with open("config.json", "w", encoding="utf-8") as f:`,
		`    json.dump(config, f, indent=2)`,
		`PY`,
		`exec /opt/uniclaudeproxy/.venv/bin/python -m uvicorn app.main:app --host 127.0.0.1 --port 9223`,
	}, "\n")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func hostCodexAuthPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, CodexProvider().ConfigDir, CodexProvider().CredentialFile)
}

func hasManagedOpenAICredentials() bool {
	if os.Getenv("OPENAI_API_KEY") != "" {
		return true
	}
	return codexAuthFileHasManagedOpenAIToken(hostCodexAuthPath())
}

func managedOpenAICredentialsError(providerDisplayName string) string {
	msg := "OpenAI credential is required for managed " + providerDisplayName + " proxy mode; set OPENAI_API_KEY, add an OPENAI_API_KEY field to ~/.codex/auth.json, or run 'codex login' so ~/.codex/auth.json contains a ChatGPT access_token."
	authPath := hostCodexAuthPath()
	if !codexAuthFileHasManagedOpenAIToken(authPath) && codexAuthFileHasOAuthToken(authPath) {
		msg += " Found ~/.codex/auth.json, but it does not contain a usable OPENAI_API_KEY or ChatGPT access_token."
	}
	msg += " To use native Claude instead, run: mittens policy set provider.backend claude"
	return msg
}

func codexAuthFileHasManagedOpenAIToken(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return codexAuthHasManagedOpenAIToken(data)
}

func codexAuthFileHasOpenAIAPIKey(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return codexAuthHasUsableOpenAIToken(data)
}

func codexAuthFileHasOAuthToken(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return codexAuthHasOAuthToken(data)
}

func codexAuthHasManagedOpenAIToken(data []byte) bool {
	return codexAuthHasUsableOpenAIToken(data) || codexAuthHasChatGPTAccessToken(data)
}

func codexAuthHasUsableOpenAIToken(data []byte) bool {
	var obj map[string]interface{}
	if json.Unmarshal(data, &obj) != nil {
		return false
	}
	if v, ok := obj["OPENAI_API_KEY"].(string); ok && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

func codexAuthHasChatGPTAccessToken(data []byte) bool {
	var obj map[string]interface{}
	if json.Unmarshal(data, &obj) != nil {
		return false
	}
	if strings.TrimSpace(toJSONString(obj["auth_mode"])) != "chatgpt" {
		return false
	}
	if jsonStringFieldNonEmpty(obj, "access_token") {
		return true
	}
	if tokens, ok := obj["tokens"].(map[string]interface{}); ok {
		return jsonStringFieldNonEmpty(tokens, "access_token")
	}
	return false
}

func codexAuthHasOAuthToken(data []byte) bool {
	var obj map[string]interface{}
	if json.Unmarshal(data, &obj) != nil {
		return false
	}
	if jsonStringFieldNonEmpty(obj, "access_token") || jsonStringFieldNonEmpty(obj, "id_token") {
		return true
	}
	if tokens, ok := obj["tokens"].(map[string]interface{}); ok {
		if jsonStringFieldNonEmpty(tokens, "access_token") || jsonStringFieldNonEmpty(tokens, "id_token") {
			return true
		}
	}
	return false
}

func jsonStringFieldNonEmpty(obj map[string]interface{}, field string) bool {
	v, ok := obj[field].(string)
	return ok && strings.TrimSpace(v) != ""
}

func toJSONString(value interface{}) string {
	v, _ := value.(string)
	return v
}

func endpointFirewallHostPort(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	if host == "" {
		return ""
	}
	return host + ":" + port
}

func urlHostname(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func ensureStringMap(in map[string]string) map[string]string {
	if in != nil {
		return in
	}
	return map[string]string{}
}

func appendMissingDockerArgPair(args []string, flag, value string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return args
		}
	}
	return append(args, flag, value)
}

func appendMissingString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func ollamaHostURL() string {
	if v := os.Getenv("MITTENS_OLLAMA_HOST"); v != "" {
		return normalizeOllamaURL(v)
	}
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		return normalizeOllamaURL(v)
	}
	return "http://host.docker.internal:11434"
}

func ollamaOpenAIBaseURL(host string) string {
	return strings.TrimRight(normalizeOllamaURL(host), "/") + "/v1"
}

func normalizeOllamaURL(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimRight(host, "/")
	if host == "" {
		return "http://host.docker.internal:11434"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	host = strings.TrimSuffix(host, "/v1")
	return host
}

// DefaultProvider returns the default provider (Claude).
func DefaultProvider() *Provider {
	return ClaudeProvider()
}

func canonicalProviderName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "claude", "anthropic":
		return "claude"
	case "codex", "openai":
		return "codex"
	case "gemini", "google":
		return "gemini"
	case "ollama", "local", "local-ollama":
		return "ollama"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}
