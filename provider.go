package main

import "path/filepath"

// Provider holds all values that identify an AI assistant binary, its config
// layout, settings keys, and install command. Swapping the Provider lets
// mittens drive a different AI CLI without touching orchestration code.
type Provider struct {
	Name         string // short machine name, e.g. "claude"
	DisplayName  string // human-facing name, e.g. "Claude"
	Binary       string // CLI binary name, e.g. "claude"
	Username     string // container username, e.g. "claude"
	InstallCmd   string // shell command to install the CLI in the image
	APIKeyEnv    string // env var name for the API key, e.g. "ANTHROPIC_API_KEY"
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

	// CLI flags
	ResumeFlags   []string // flags that mean "resume session", e.g. ["--continue", "-c", "--resume", "-r"]
	SkipPermsFlag string   // flag to skip permission prompts, e.g. "--dangerously-skip-permissions"
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
		Name:        "claude",
		DisplayName: "Claude",
		Binary:      "claude",
		Username:    "claude",
		InstallCmd:     `curl -fsSL https://claude.ai/install.sh | bash && cp -L /root/.local/bin/claude /usr/local/bin/claude && chmod +x /usr/local/bin/claude && /usr/local/bin/claude --version`,
		APIKeyEnv:      "ANTHROPIC_API_KEY",
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

		ResumeFlags:   []string{"--continue", "-c", "--resume", "-r"},
		SkipPermsFlag: "--dangerously-skip-permissions",
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

		ConfigSubdirs: []string{},
		ConfigFiles:   []string{"config.toml"},

		PluginDir:   "",
		PluginFiles: []string{},

		TrustedDirsKey: "",
		YoloKey:        "",
		MCPServersKey:  "",

		ResumeFlags:   []string{"--resume", "-r", "--continue", "-l"},
		SkipPermsFlag: "--dangerously-bypass-approvals-and-sandbox",
	}
}

// DefaultProvider returns the default provider (Claude).
func DefaultProvider() *Provider {
	return ClaudeProvider()
}
