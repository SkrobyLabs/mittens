package main

import (
	"reflect"
	"testing"
)

func TestClaudeProvider_AllFieldsNonEmpty(t *testing.T) {
	// Fields that are intentionally empty for ClaudeProvider (used by other providers).
	optionalFields := map[string]bool{
		"TrustedDirsFile":          true, // Gemini-only: separate trusted dirs file
		"ContainerHostname":        true, // Gemini-only: fixed Docker hostname
		"InitSettingsJQ":           true, // Gemini-only: post-init settings patch
		"PersistFiles":             true, // Gemini-only: state files to survive between runs
		"HistoryMountsWholeConfig": true, // Codex-only: mount whole config dir for history
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
		case reflect.Slice:
			if f.Len() == 0 {
				t.Errorf("field %s is empty slice", name)
			}
		}
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
		{"StagingConfigDir", p.StagingConfigDir(), "/mnt/claude-config/.claude"},
		{"StagingCredentialPath", p.StagingCredentialPath(), "/mnt/claude-config/.credentials.json"},
		{"StagingUserPrefsPath", p.StagingUserPrefsPath(), "/mnt/claude-config/.claude.json"},
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
		"MCPServersKey":   p.MCPServersKey,
	}
	for name, val := range intentionallyEmpty {
		if val != "" {
			t.Errorf("CodexProvider().%s = %q, want empty", name, val)
		}
	}

	if len(p.FirewallDomains) == 0 {
		t.Error("CodexProvider().FirewallDomains is empty")
	}
	if len(p.ResumeFlags) == 0 {
		t.Error("CodexProvider().ResumeFlags is empty")
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
