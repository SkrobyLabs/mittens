package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeProvider_AllFieldsNonEmpty(t *testing.T) {
	// Fields that are intentionally empty for ClaudeProvider (used by other providers).
	optionalFields := map[string]bool{
		"TrustedDirsFile":          true, // Gemini-only: separate trusted dirs file
		"ContainerHostname":        true, // Gemini-only: fixed Docker hostname
		"ContainerEnv":             true, // optional runtime env overrides
		"DockerArgs":               true, // provider-specific docker args
		"FirewallHostPorts":        true, // local proxy/provider endpoints
		"ImageTagParts":            true, // provider policy/runtime image variants
		"DefaultArgs":              true, // provider-specific default CLI args
		"ManagedProxyCmd":          true, // optional in-container provider proxy
		"ManagedProxyPort":         true, // optional in-container provider proxy
		"InitSettingsJQ":           true, // Gemini-only: post-init settings patch
		"SkipCredentials":          true, // local/third-party providers
		"LocalModelSource":         true, // local provider model detection
		"DefaultModel":             true, // provider/model policy default
		"PersistFiles":             true, // Gemini-only: state files to survive between runs
		"PersistDirs":              true, // provider-specific runtime persistence
		"PersistGlobs":             true, // provider-specific runtime persistence
		"LiveMountFiles":           true, // provider-specific direct runtime mounts
		"LiveMountDirs":            true, // provider-specific direct runtime mounts
		"HistoryMountsWholeConfig": true, // Codex-only: mount whole config dir for history
		"HistoryMountsProjectDirs": true, // provider-specific history strategy
		"EffortTemplate":           true, // some providers don't use template mode
	}
	p := ClaudeProvider()
	v := reflect.ValueOf(*p)
	ty := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := ty.Field(i).Name
		if optionalFields[name] {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			if f.String() == "" {
				t.Errorf("field %s is empty", name)
			}
		case reflect.Map:
			if f.Len() == 0 && name != "RoleDefaults" {
				t.Errorf("field %s is empty map", name)
			}
		case reflect.Slice:
			if f.Len() == 0 {
				t.Errorf("field %s is empty slice", name)
			}
		}
	}

	if p.ModelFlag == "" {
		t.Error("ClaudeProvider ModelFlag should be set")
	}
}

func TestProvider_HomePath(t *testing.T) {
	p := ClaudeProvider()
	if got := p.HomePath(); got != "/home/claude" {
		t.Errorf("HomePath() = %q, want /home/claude", got)
	}
}

func TestProvider_ContainerConfigDir(t *testing.T) {
	p := ClaudeProvider()
	if got := p.ContainerConfigDir(); got != "/home/claude/.claude" {
		t.Errorf("ContainerConfigDir() = %q, want /home/claude/.claude", got)
	}
}

func TestProvider_ContainerCredentialPath(t *testing.T) {
	p := ClaudeProvider()
	want := "/home/claude/.claude/.credentials.json"
	if got := p.ContainerCredentialPath(); got != want {
		t.Errorf("ContainerCredentialPath() = %q, want %q", got, want)
	}
}

func TestProvider_HostConfigDir(t *testing.T) {
	p := ClaudeProvider()
	want := "/Users/test/.claude"
	if got := p.HostConfigDir("/Users/test"); got != want {
		t.Errorf("HostConfigDir() = %q, want %q", got, want)
	}
}

func TestProvider_HostCredentialPath(t *testing.T) {
	p := ClaudeProvider()
	want := "/Users/test/.claude/.credentials.json"
	if got := p.HostCredentialPath("/Users/test"); got != want {
		t.Errorf("HostCredentialPath() = %q, want %q", got, want)
	}
}

func TestProvider_HostUserPrefsPath(t *testing.T) {
	p := ClaudeProvider()
	want := "/Users/test/.claude.json"
	if got := p.HostUserPrefsPath("/Users/test"); got != want {
		t.Errorf("HostUserPrefsPath() = %q, want %q", got, want)
	}
}

func TestProvider_StagingPaths(t *testing.T) {
	p := ClaudeProvider()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"StagingConfigDir", p.StagingConfigDir(), "/mnt/mittens-staging/.claude"},
		{"StagingCredentialPath", p.StagingCredentialPath(), "/mnt/mittens-staging/.credentials.json"},
		{"StagingUserPrefsPath", p.StagingUserPrefsPath(), "/mnt/mittens-staging/.claude.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestProvider_IsResumeFlag(t *testing.T) {
	p := ClaudeProvider()

	positive := []string{"--continue", "-c", "--resume", "-r"}
	for _, f := range positive {
		if !p.IsResumeFlag(f) {
			t.Errorf("IsResumeFlag(%q) = false, want true", f)
		}
	}

	negative := []string{"--verbose", "--model", "resume", "-v", ""}
	for _, f := range negative {
		if p.IsResumeFlag(f) {
			t.Errorf("IsResumeFlag(%q) = true, want false", f)
		}
	}
}

func TestDefaultProvider_ReturnsClaude(t *testing.T) {
	p := DefaultProvider()
	if p.Name != "claude" {
		t.Errorf("DefaultProvider().Name = %q, want claude", p.Name)
	}
	if p.Binary != "claude" {
		t.Errorf("DefaultProvider().Binary = %q, want claude", p.Binary)
	}
}

func TestCanonicalProviderName(t *testing.T) {
	tests := map[string]string{
		"":            "claude",
		"claude":      "claude",
		"Anthropic":   "claude",
		"codex":       "codex",
		"openai":      "codex",
		"gemini":      "gemini",
		"google":      "gemini",
		"ollama":      "ollama",
		"local":       "ollama",
		"custom-ai":   "custom-ai",
		" CUSTOM-AI ": "custom-ai",
	}

	for input, want := range tests {
		if got := canonicalProviderName(input); got != want {
			t.Errorf("canonicalProviderName(%q) = %q, want %q", input, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// CodexProvider tests
// ---------------------------------------------------------------------------

func TestCodexProvider_FieldsPopulated(t *testing.T) {
	p := CodexProvider()

	// Required non-empty fields.
	required := map[string]string{
		"Name":           p.Name,
		"DisplayName":    p.DisplayName,
		"Binary":         p.Binary,
		"Username":       p.Username,
		"InstallCmd":     p.InstallCmd,
		"APIKeyEnv":      p.APIKeyEnv,
		"SettingsFormat": p.SettingsFormat,
		"ConfigDir":      p.ConfigDir,
		"CredentialFile": p.CredentialFile,
		"SettingsFile":   p.SettingsFile,
		"ProjectFile":    p.ProjectFile,
		"SkipPermsFlag":  p.SkipPermsFlag,
	}
	for name, val := range required {
		if val == "" {
			t.Errorf("CodexProvider().%s is empty", name)
		}
	}

	// Intentionally empty fields.
	intentionallyEmpty := map[string]string{
		"UserPrefsFile":   p.UserPrefsFile,
		"KeychainService": p.KeychainService,
		"PluginDir":       p.PluginDir,
		"TrustedDirsKey":  p.TrustedDirsKey,
		"YoloKey":         p.YoloKey,
	}
	for name, val := range intentionallyEmpty {
		if val != "" {
			t.Errorf("CodexProvider().%s = %q, want empty", name, val)
		}
	}

	if len(p.FirewallDomains) == 0 {
		t.Error("CodexProvider().FirewallDomains is empty")
	}
	if p.MCPServersKey != "mcp_servers" {
		t.Errorf("CodexProvider().MCPServersKey = %q, want mcp_servers", p.MCPServersKey)
	}
	if p.MCPConfigFile != ".codex/config.toml" {
		t.Errorf("CodexProvider().MCPConfigFile = %q, want .codex/config.toml", p.MCPConfigFile)
	}
	if p.MCPConfigFormat != "toml" {
		t.Errorf("CodexProvider().MCPConfigFormat = %q, want toml", p.MCPConfigFormat)
	}
	if len(p.PersistFiles) != 0 {
		t.Errorf("CodexProvider().PersistFiles = %v, want empty", p.PersistFiles)
	}
	if len(p.PersistDirs) != 0 {
		t.Errorf("CodexProvider().PersistDirs = %v, want empty", p.PersistDirs)
	}
	if len(p.PersistGlobs) != 0 {
		t.Errorf("CodexProvider().PersistGlobs = %v, want empty", p.PersistGlobs)
	}
	if len(p.LiveMountFiles) == 0 {
		t.Error("CodexProvider().LiveMountFiles is empty")
	}
	if len(p.LiveMountDirs) == 0 {
		t.Error("CodexProvider().LiveMountDirs is empty")
	}
	if len(p.ResumeFlags) == 0 {
		t.Error("CodexProvider().ResumeFlags is empty")
	}
	if p.HistoryMountsWholeConfig {
		t.Fatal("CodexProvider().HistoryMountsWholeConfig = true, want false")
	}
	if p.HistoryMountsProjectDirs {
		t.Fatal("CodexProvider().HistoryMountsProjectDirs = true, want false")
	}
	if p.EffortFlag != "" {
		t.Fatalf("CodexProvider().EffortFlag = %q, want empty", p.EffortFlag)
	}
	if p.EffortTemplate != "-c model_reasoning_effort=%s" {
		t.Fatalf("CodexProvider().EffortTemplate = %q, want %q", p.EffortTemplate, "-c model_reasoning_effort=%s")
	}
}

func TestCodexProvider_Paths(t *testing.T) {
	p := CodexProvider()

	if got := p.HomePath(); got != "/home/codex" {
		t.Errorf("HomePath() = %q, want /home/codex", got)
	}
	if got := p.ContainerConfigDir(); got != "/home/codex/.codex" {
		t.Errorf("ContainerConfigDir() = %q, want /home/codex/.codex", got)
	}
	if got := p.ContainerCredentialPath(); got != "/home/codex/.codex/auth.json" {
		t.Errorf("ContainerCredentialPath() = %q, want /home/codex/.codex/auth.json", got)
	}
	if got := p.HostConfigDir("/Users/test"); got != "/Users/test/.codex" {
		t.Errorf("HostConfigDir() = %q, want /Users/test/.codex", got)
	}
}

func TestCodexProvider_IsResumeFlag(t *testing.T) {
	p := CodexProvider()

	positive := []string{"--resume", "-r", "--continue", "-l"}
	for _, f := range positive {
		if !p.IsResumeFlag(f) {
			t.Errorf("IsResumeFlag(%q) = false, want true", f)
		}
	}

	negative := []string{"--verbose", "--model", "resume", "-v", "-c"}
	for _, f := range negative {
		if p.IsResumeFlag(f) {
			t.Errorf("IsResumeFlag(%q) = true, want false", f)
		}
	}
}

func TestOllamaProvider_UsesCodexHarness(t *testing.T) {
	p := OllamaProvider()

	if p.Name != "ollama" {
		t.Fatalf("Name = %q, want ollama", p.Name)
	}
	if p.Binary != "codex" {
		t.Fatalf("Binary = %q, want codex", p.Binary)
	}
	if p.Username != "codex" {
		t.Fatalf("Username = %q, want codex", p.Username)
	}
	if p.ConfigDir != ".codex" || p.SettingsFile != "config.toml" {
		t.Fatalf("Codex config layout not preserved: %s %s", p.ConfigDir, p.SettingsFile)
	}
	if !p.SkipCredentials {
		t.Fatal("SkipCredentials = false, want true")
	}
	if p.LocalModelSource != "ollama" {
		t.Fatalf("LocalModelSource = %q, want ollama", p.LocalModelSource)
	}
	if !reflect.DeepEqual(p.DefaultArgs, []string{"--oss", "--local-provider", "ollama"}) {
		t.Fatalf("DefaultArgs = %#v", p.DefaultArgs)
	}
}

func TestOllamaProvider_EndpointConfig(t *testing.T) {
	t.Setenv("MITTENS_OLLAMA_HOST", "10.0.1.50:11434")

	p := OllamaProvider()
	if got := p.ContainerEnv["OLLAMA_HOST"]; got != "http://10.0.1.50:11434" {
		t.Fatalf("OLLAMA_HOST = %q", got)
	}
	if got := p.ContainerEnv["CODEX_OSS_BASE_URL"]; got != "http://10.0.1.50:11434/v1" {
		t.Fatalf("CODEX_OSS_BASE_URL = %q", got)
	}
}

func TestOllamaOpenAIBaseURL_NormalizesV1(t *testing.T) {
	if got := ollamaOpenAIBaseURL("http://host.docker.internal:11434/v1"); got != "http://host.docker.internal:11434/v1" {
		t.Fatalf("ollamaOpenAIBaseURL = %q", got)
	}
}

func TestProviderApplyPolicy_OllamaEndpointAndModel(t *testing.T) {
	p := OllamaProvider()
	p.ApplyPolicy(ProviderPolicy{
		Endpoint: "10.0.1.50:11434",
		Model:    "qwen3-coder:30b",
	})

	if p.DefaultModel != "qwen3-coder:30b" {
		t.Fatalf("DefaultModel = %q", p.DefaultModel)
	}
	if got := p.ContainerEnv["OLLAMA_HOST"]; got != "http://10.0.1.50:11434" {
		t.Fatalf("OLLAMA_HOST = %q", got)
	}
	if got := p.ContainerEnv["CODEX_OSS_BASE_URL"]; got != "http://10.0.1.50:11434/v1" {
		t.Fatalf("CODEX_OSS_BASE_URL = %q", got)
	}
}

func TestProviderApplyPolicy_ClaudeOpenAIManagedBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.test/v1")
	t.Setenv("MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL", "gpt-test")
	t.Setenv("MITTENS_CLAUDE_OPENAI_REASONING_EFFORT", "high")

	p := ClaudeProvider()
	p.ApplyPolicy(ProviderPolicy{
		Backend: "openai",
		Model:   "claude-sonnet-4-6",
	})

	if p.DisplayName != "Claude (OpenAI proxy)" {
		t.Fatalf("DisplayName = %q", p.DisplayName)
	}
	if !p.SkipCredentials {
		t.Fatal("SkipCredentials = false, want true")
	}
	if got := p.ContainerEnv["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:9223" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
	for key, want := range map[string]string{
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "fable[1m]",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "opus[1m]",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet[1m]",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "haiku",
	} {
		if got := p.ContainerEnv[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{
		"ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES":  "effort,xhigh_effort,max_effort,adaptive_thinking,interleaved_thinking",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES":   "effort,xhigh_effort,max_effort,adaptive_thinking,interleaved_thinking",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES": "effort,xhigh_effort,adaptive_thinking,interleaved_thinking",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES":  "effort,adaptive_thinking",
	} {
		if got := p.ContainerEnv[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if got := p.ContainerEnv["OPENAI_API_KEY"]; got != "sk-openai-test" {
		t.Fatalf("OPENAI_API_KEY = %q", got)
	}
	if got := p.ContainerEnv["OPENAI_BASE_URL"]; got != "https://api.openai.test/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q", got)
	}
	if !containsString(p.FirewallDomains, "api.openai.test") {
		t.Fatalf("FirewallDomains missing custom OpenAI base URL host: %#v", p.FirewallDomains)
	}
	if !containsString(p.FirewallDomains, "chatgpt.com") {
		t.Fatalf("FirewallDomains missing ChatGPT Codex host: %#v", p.FirewallDomains)
	}
	if got := p.ContainerEnv["MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL"]; got != "gpt-test" {
		t.Fatalf("MITTENS_CLAUDE_OPENAI_UPSTREAM_MODEL = %q", got)
	}
	if got := p.ContainerEnv["MITTENS_CLAUDE_OPENAI_REASONING_EFFORT"]; got != "high" {
		t.Fatalf("MITTENS_CLAUDE_OPENAI_REASONING_EFFORT = %q", got)
	}
	if p.ManagedProxyCmd == "" || p.ManagedProxyPort != 9223 {
		t.Fatalf("managed proxy = (%q, %d)", p.ManagedProxyCmd, p.ManagedProxyPort)
	}
	if !reflect.DeepEqual(p.ImageTagParts, []string{"openai-proxy"}) {
		t.Fatalf("ImageTagParts = %#v", p.ImageTagParts)
	}
	if len(p.FirewallHostPorts) != 0 {
		t.Fatalf("FirewallHostPorts = %#v, want none for managed localhost proxy", p.FirewallHostPorts)
	}
}

func TestProviderApplyPolicy_ClaudeOpenAIManagedBackendMountsCodexAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"sk-from-auth"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := ClaudeProvider()
	p.ApplyPolicy(ProviderPolicy{Backend: "openai"})

	if got := p.ContainerEnv["MITTENS_OPENAI_AUTH_FILE"]; got != "/mnt/mittens-openai-auth.json" {
		t.Fatalf("MITTENS_OPENAI_AUTH_FILE = %q", got)
	}
	if !reflect.DeepEqual(p.DockerArgs, []string{"-v", authPath + ":/mnt/mittens-openai-auth.json:ro"}) {
		t.Fatalf("DockerArgs = %#v", p.DockerArgs)
	}
	if !hasManagedOpenAICredentials() {
		t.Fatal("expected managed OpenAI credentials from Codex auth API key")
	}
}

func TestProviderApplyPolicy_ClaudeOpenAIManagedBackendMountsCodexChatGPTAccessToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"chatgpt-token","id_token":"id-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p := ClaudeProvider()
	p.ApplyPolicy(ProviderPolicy{Backend: "openai"})

	if got := p.ContainerEnv["MITTENS_OPENAI_AUTH_FILE"]; got != "/mnt/mittens-openai-auth.json" {
		t.Fatalf("MITTENS_OPENAI_AUTH_FILE = %q", got)
	}
	if !reflect.DeepEqual(p.DockerArgs, []string{"-v", authPath + ":/mnt/mittens-openai-auth.json:ro"}) {
		t.Fatalf("DockerArgs = %#v", p.DockerArgs)
	}
	if !hasManagedOpenAICredentials() {
		t.Fatal("expected managed OpenAI credentials from Codex ChatGPT access token")
	}
	if _, ok := p.ContainerEnv["OPENAI_API_KEY"]; ok {
		t.Fatalf("OPENAI_API_KEY should not be injected when absent on host: %#v", p.ContainerEnv)
	}
}

func TestProviderApplyPolicy_ClaudeOpenAIManagedBackendDefaultsModelAlias(t *testing.T) {
	p := ClaudeProvider()
	p.ApplyPolicy(ProviderPolicy{Backend: "openai"})

	if p.DefaultModel != "opus" {
		t.Fatalf("DefaultModel = %q", p.DefaultModel)
	}
	if !strings.Contains(p.ManagedProxyCmd, "opus") {
		t.Fatalf("ManagedProxyCmd missing default alias: %q", p.ManagedProxyCmd)
	}
}

func TestClaudeOpenAIProxyCommandUsesCodexAuthWithoutPreflight(t *testing.T) {
	cmd := claudeOpenAIProxyCommand("claude-sonnet-4-6")
	for _, want := range []string{
		`tokens.get("access_token")`,
		`https://chatgpt.com/backend-api/codex`,
		`tokens.get("account_id")`,
		`upstream_default = ""`,
		`reasoning_effort = ""`,
		`MITTENS_CLAUDE_OPENAI_REASONING_EFFORT`,
		`"fable": ("gpt-5.5", "xhigh")`,
		`"fable[1m]": ("gpt-5.5", "xhigh")`,
		`"opus": ("gpt-5.5", "medium")`,
		`"opus[1m]": ("gpt-5.5", "medium")`,
		`"sonnet": ("gpt-5.5", "low")`,
		`"sonnet[1m]": ("gpt-5.5", "low")`,
		`"haiku": ("gpt-5.4-mini", "low")`,
		`"claude-sonnet-4-6": ("gpt-5.5", "low")`,
		`"claude-sonnet-4-6[1m]": ("gpt-5.5", "low")`,
		`model_config["reasoning"] = {"effort": effort}`,
		`"upstream_model_id": upstream`,
		`headers["ChatGPT-Account-ID"]`,
		`"headers": headers`,
		`"base_url": os.environ["openai_base_url"]`,
		`models[alias] = f"openai/{key}"`,
		`"models": provider_models`,
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("proxy command missing %q: %s", want, cmd)
		}
	}
	for _, unwanted := range []string{
		`base + "/models"`,
		"preflight",
		"was rejected by upstream OpenAI API",
		"gpt-4.1",
	} {
		if strings.Contains(cmd, unwanted) {
			t.Fatalf("proxy command should not contain %q: %s", unwanted, cmd)
		}
	}
}

func TestClaudeOpenAIProxyInstallSuffixMakesRuntimeFilesWritable(t *testing.T) {
	suffix := claudeOpenAIProxyInstallSuffix()
	for _, want := range []string{
		"touch /opt/uniclaudeproxy/config.json /opt/uniclaudeproxy/debug.log",
		"chmod 666 /opt/uniclaudeproxy/config.json /opt/uniclaudeproxy/debug.log",
		`body["store"] = False`,
		`elif "\n    return body" in ps:`,
	} {
		if !strings.Contains(suffix, want) {
			t.Fatalf("install suffix missing %q: %s", want, suffix)
		}
	}
	if strings.Contains(suffix, `raise SystemExit("UniClaudeProxy store patch target not found")`) {
		t.Fatalf("install suffix should not fail the Docker build when the store patch anchor moves: %s", suffix)
	}
}

func TestClaudeOpenAIProxyInstallSuffixPatchesRetryableAuthFailures(t *testing.T) {
	suffix := claudeOpenAIProxyInstallSuffix()
	for _, want := range []string{
		"import httpx",
		"debug_logger.setLevel(logging.WARNING)",
		"except httpx.HTTPStatusError as e:",
		"status in (401, 403)",
		"OpenAI upstream authentication failed. Check the configured OpenAI credential.",
	} {
		if !strings.Contains(suffix, want) {
			t.Fatalf("install suffix missing %q: %s", want, suffix)
		}
	}
}

func TestProviderApplyPolicy_ClaudeOpenAIExternalBackend(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	p := ClaudeProvider()
	p.ApplyPolicy(ProviderPolicy{
		Backend:  "openai",
		Endpoint: "127.0.0.1:9223",
		Model:    "claude-sonnet-4-6",
	})

	if p.DisplayName != "Claude (OpenAI proxy)" {
		t.Fatalf("DisplayName = %q", p.DisplayName)
	}
	if !p.SkipCredentials {
		t.Fatal("SkipCredentials = false, want true")
	}
	if p.DefaultModel != "claude-sonnet-4-6" {
		t.Fatalf("DefaultModel = %q", p.DefaultModel)
	}
	if got := p.ContainerEnv["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:9223" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
	if got := p.ContainerEnv["ANTHROPIC_API_KEY"]; got != "sk-proxy-0" {
		t.Fatalf("ANTHROPIC_API_KEY = %q", got)
	}
	if !reflect.DeepEqual(p.DockerArgs, []string{"--add-host", "host.docker.internal:host-gateway"}) {
		t.Fatalf("DockerArgs = %#v", p.DockerArgs)
	}
	if !reflect.DeepEqual(p.FirewallHostPorts, []string{"127.0.0.1:9223"}) {
		t.Fatalf("FirewallHostPorts = %#v", p.FirewallHostPorts)
	}
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func TestCodexAuthHasUsableOpenAIToken(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{name: "api key", data: `{"OPENAI_API_KEY":"sk-test"}`, want: true},
		{name: "null api key with id token", data: `{"OPENAI_API_KEY":null,"tokens":{"id_token":"tok"}}`, want: false},
		{name: "nested chatgpt access token", data: `{"auth_mode":"chatgpt","tokens":{"access_token":"tok"}}`, want: false},
		{name: "nested id token", data: `{"tokens":{"id_token":"tok"}}`, want: false},
		{name: "root access token", data: `{"access_token":"tok"}`, want: false},
		{name: "root id token", data: `{"id_token":"tok"}`, want: false},
		{name: "missing", data: `{"tokens":{}}`, want: false},
		{name: "invalid", data: `{`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexAuthHasUsableOpenAIToken([]byte(tc.data)); got != tc.want {
				t.Fatalf("codexAuthHasUsableOpenAIToken = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexAuthHasManagedOpenAIToken(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{name: "api key", data: `{"OPENAI_API_KEY":"sk-test"}`, want: true},
		{name: "chatgpt nested access token", data: `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"tok","id_token":"id"}}`, want: true},
		{name: "chatgpt root access token", data: `{"auth_mode":"chatgpt","access_token":"tok"}`, want: true},
		{name: "id token only", data: `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id"}}`, want: false},
		{name: "non chatgpt access token", data: `{"tokens":{"access_token":"tok"}}`, want: false},
		{name: "invalid", data: `{`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexAuthHasManagedOpenAIToken([]byte(tc.data)); got != tc.want {
				t.Fatalf("codexAuthHasManagedOpenAIToken = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexAuthHasOAuthToken(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{name: "api key only", data: `{"OPENAI_API_KEY":"sk-test"}`, want: false},
		{name: "null api key with id token", data: `{"OPENAI_API_KEY":null,"tokens":{"id_token":"tok"}}`, want: true},
		{name: "nested access token", data: `{"tokens":{"access_token":"tok"}}`, want: true},
		{name: "nested id token", data: `{"tokens":{"id_token":"tok"}}`, want: true},
		{name: "root access token", data: `{"access_token":"tok"}`, want: true},
		{name: "root id token", data: `{"id_token":"tok"}`, want: true},
		{name: "empty token", data: `{"tokens":{"access_token":""}}`, want: false},
		{name: "empty id token", data: `{"tokens":{"id_token":""}}`, want: false},
		{name: "invalid", data: `{`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexAuthHasOAuthToken([]byte(tc.data)); got != tc.want {
				t.Fatalf("codexAuthHasOAuthToken = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestManagedOpenAICredentialsErrorSuggestsLoginAndNativeClaudeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	authPath := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"id_token":"id-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	msg := managedOpenAICredentialsError("Claude (OpenAI proxy)")
	for _, want := range []string{
		"codex login",
		"ChatGPT access_token",
		"does not contain a usable",
		"mittens policy set provider.backend claude",
		"OPENAI_API_KEY",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %s", want, msg)
		}
	}
}
