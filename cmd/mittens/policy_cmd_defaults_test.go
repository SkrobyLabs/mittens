package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setupPolicyCommandWorkspace(t *testing.T) string {
	t.Helper()
	t.Setenv("MITTENS_HOME", t.TempDir())
	workspace := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	return workspace
}

func saveMutationDefaults(t *testing.T, provider string) *ProjectPolicy {
	t.Helper()
	defaults := defaultProjectPolicy()
	defaults.Provider.Name = provider
	defaults.Network.Firewall = "dev"
	defaults.Workspace.Mounts = []PolicyMount{{Path: "/shared", Access: "ro"}}
	if err := SaveUserDefaultsPolicy(defaults); err != nil {
		t.Fatal(err)
	}
	return defaults
}

func saveLegacyDefaultProfile(t *testing.T, workspace string) {
	t.Helper()
	defaults := saveMutationDefaults(t, "claude")
	defaults.Provider.Profile = "fast"
	if err := SaveUserDefaultsPolicy(defaults); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfileConfig(workspace, &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{
		"claude": {
			"fast": {Model: "legacy-model", Effort: "high"},
		},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunPolicySetSeedsNewProjectFromUserDefaults(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	saveMutationDefaults(t, "codex")

	if err := runPolicySet([]string{"host.open_urls", "deny"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, source, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if source != PolicySourceV2 {
		t.Fatalf("source = %q, want %q", source, PolicySourceV2)
	}
	if policy.Provider.Name != "codex" || policy.Network.Firewall != "dev" {
		t.Fatalf("new policy did not preserve defaults: %+v", policy)
	}
	if len(policy.Workspace.Mounts) != 1 || policy.Workspace.Mounts[0].Path != "/shared" {
		t.Fatalf("mounts = %+v, want inherited /shared mount", policy.Workspace.Mounts)
	}
	if policy.Host.OpenURLs != "deny" {
		t.Fatalf("open_urls = %q, want deny", policy.Host.OpenURLs)
	}
}

func TestRunPolicyAllowSeedsNewProjectFromUserDefaults(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	saveMutationDefaults(t, "ollama")

	if err := runPolicyAllow([]string{"api.example.test"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, _, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "ollama" || policy.Network.Firewall != "dev" {
		t.Fatalf("new policy did not preserve defaults: %+v", policy)
	}
	if len(policy.Network.ExtraDomains) != 1 || policy.Network.ExtraDomains[0] != "api.example.test" {
		t.Fatalf("extra_domains = %v, want [api.example.test]", policy.Network.ExtraDomains)
	}
}

func TestRunPolicySetResolvesLegacyDefaultProfileBeforeSaving(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	saveLegacyDefaultProfile(t, workspace)

	if err := runPolicySet([]string{"provider.model", "selected-model"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, _, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Profile != "" {
		t.Fatalf("provider.profile = %q, want resolved profile", policy.Provider.Profile)
	}
	if policy.Provider.Model != "selected-model" {
		t.Fatalf("provider.model = %q, want explicit override", policy.Provider.Model)
	}
	if policy.Provider.Effort != "high" {
		t.Fatalf("provider.effort = %q, want inherited profile effort", policy.Provider.Effort)
	}
	defaults, _, err := LoadUserDefaultsPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Provider.Profile != "fast" {
		t.Fatalf("user default provider.profile = %q, want fast", defaults.Provider.Profile)
	}
	if changed, err := resolveLegacyProviderProfile(workspace, policy, false); err != nil || changed {
		t.Fatalf("saved policy re-resolved: changed=%t, err=%v", changed, err)
	}
	if policy.Provider.Model != "selected-model" {
		t.Fatalf("provider.model after resolution = %q, want explicit override", policy.Provider.Model)
	}
}

func TestRunPolicyAllowResolvesLegacyDefaultProfileBeforeSaving(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	saveLegacyDefaultProfile(t, workspace)

	if err := runPolicyAllow([]string{"api.example.test"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, _, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Profile != "" {
		t.Fatalf("provider.profile = %q, want resolved profile", policy.Provider.Profile)
	}
	if policy.Provider.Model != "legacy-model" || policy.Provider.Effort != "high" {
		t.Fatalf("provider settings = %+v, want resolved legacy profile", policy.Provider)
	}
	if len(policy.Network.ExtraDomains) != 1 || policy.Network.ExtraDomains[0] != "api.example.test" {
		t.Fatalf("extra_domains = %v, want [api.example.test]", policy.Network.ExtraDomains)
	}
}

func TestPolicyMutationsKeepExistingStandalonePolicies(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	saveMutationDefaults(t, "codex")
	existing := defaultProjectPolicy()
	existing.Provider.Name = "gemini"
	if err := SaveProjectPolicy(workspace, existing); err != nil {
		t.Fatal(err)
	}

	if err := runPolicyAllow([]string{"api.example.test"}, nil); err != nil {
		t.Fatal(err)
	}

	policy, _, err := LoadProjectPolicy(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Provider.Name != "gemini" {
		t.Fatalf("provider = %q, want existing gemini policy", policy.Provider.Name)
	}
}

func TestRunPolicySetRejectsInvalidUserDefaultsBeforeWritingProjectPolicy(t *testing.T) {
	workspace := setupPolicyCommandWorkspace(t)
	if err := os.WriteFile(UserDefaultsPolicyPath(), []byte("version: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runPolicySet([]string{"host.open_urls", "deny"}, nil); err == nil {
		t.Fatal("expected invalid defaults error")
	}
	if _, err := os.Stat(filepath.Join(ConfigHome(), "projects", ProjectDir(workspace), "policy.yaml")); !os.IsNotExist(err) {
		t.Fatalf("project policy was written after invalid defaults, stat err = %v", err)
	}
}
