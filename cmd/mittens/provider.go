package main

import (
	"os"
	"path/filepath"
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
	FirewallDomains []string // domains the AI CLI needs to reach

	// Config subdirs and files to copy into the container
	ConfigSubdirs []string // e.g. ["skills", "hooks", "agents", "output-styles"]
	ConfigFiles   []string // e.g. ["settings.json", "settings.local.json", "CLAUDE.md", "statusline.sh"]

	// Plugin layout
	PluginDir   string   // plugin directory name inside ConfigDir, e.g. "plugins"
	PluginFiles []string // plugin config files to copy, e.g. ["installed_plugins.json", ...]

	// Settings keys (used in jq operations inside the container)
	TrustedDirsKey string // e.g. "trustedDirectories"
	YoloKey        string // e.g. "skipDangerousModePermissionPrompt"
	MCPServersKey  string // e.g. "mcpServers"

	// Files (relative to ConfigDir) to persist: copied in on start, copied back on exit.
	// Used for provider state files that must survive between runs (e.g. Gemini auth state).
	PersistFiles []string

	// CLI flags
	ResumeFlags              []string // flags that mean "resume session", e.g. ["--continue", "-c", "--resume", "-r"]
	SkipPermsFlag            string   // flag to skip permission prompts, e.g. "--dangerously-skip-permissions"
	ContinueArgs             []string // args to prepend when resuming latest session, e.g. ["--continue"] or ["--resume", "latest"]
	TrustedDirsFile          string   // separate JSON array file for trusted dirs (Gemini); empty = unused
	HistoryMountsWholeConfig bool     // mount the provider config dir directly when history is enabled
	ModelFlag                string
	EffortFlag               string
	EffortTemplate           string

	// Container settings
	ContainerHostname string            // fixed Docker hostname; empty = Docker default. Required when credential file encryption is hostname-dependent (e.g. Gemini).
	ContainerEnv      map[string]string // extra env vars injected at docker run time; empty value = unset the var.
	InitSettingsJQ    string            // jq expression applied to settings.json once after all other setup; empty = unused.
	StopHookEvent     string            // hook event name for session end, e.g. "Stop" (Claude) or "SessionEnd" (Gemini); empty = skip stop hook.
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

// StagingConfigDir returns the staging mount path for the config directory.
func (p *Provider) StagingConfigDir() string {
	return filepath.Join("/mnt/claude-config", p.ConfigDir)
}

// StagingCredentialPath returns the staging mount path for the credential file.
func (p *Provider) StagingCredentialPath() string {
	return filepath.Join("/mnt/claude-config", p.CredentialFile)
}

// StagingUserPrefsPath returns the staging mount path for the user prefs file.
func (p *Provider) StagingUserPrefsPath() string {
	return filepath.Join("/mnt/claude-config", p.UserPrefsFile)
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

// ClaudeProvider returns a Provider configured for Claude Code with all
// current hardcoded values. This is the only provider implemented today.
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

		TrustedDirsKey: "trustedDirectories",
		YoloKey:        "skipDangerousModePermissionPrompt",
		MCPServersKey:  "mcpServers",

		ResumeFlags:     []string{"--continue", "-c", "--resume", "-r"},
		SkipPermsFlag:   "--dangerously-skip-permissions",
		ContinueArgs:    []string{"--continue"},
		TrustedDirsFile: "",
		StopHookEvent:   "Stop",
		ModelFlag:  "--model",
		EffortFlag: "--effort",
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

		TrustedDirsKey: "",
		YoloKey:        "",
		MCPServersKey:  "",

		ResumeFlags:              []string{"--resume", "-r", "--continue", "-l"},
		SkipPermsFlag:            "--dangerously-bypass-approvals-and-sandbox",
		ContinueArgs:             []string{"--resume", "latest"},
		TrustedDirsFile:          "",
		HistoryMountsWholeConfig: true,
		ModelFlag:  "--model",
		EffortFlag: "",
		// Codex expects reasoning effort via -c key-value configuration.
		EffortTemplate: "-c model_reasoning_effort=%s",
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
		YoloKey:         "",
		MCPServersKey:   "mcpServers",
		TrustedDirsFile: "trustedFolders.json",

		ResumeFlags:   []string{"--resume", "-r"},
		SkipPermsFlag: "--approval-mode=yolo",
		ContinueArgs:  []string{"--resume", "latest"},
		ModelFlag: "--model",

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

// DefaultProvider returns the default provider (Claude).
func DefaultProvider() *Provider {
	return ClaudeProvider()
}
