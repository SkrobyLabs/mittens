package gh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Skroby/mittens/extensions/registry"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// enrichHostsWithTokens
// ---------------------------------------------------------------------------

func TestEnrichHostsWithTokens_InjectsTokens(t *testing.T) {
	// This test requires `gh` on PATH and real keyring access.
	// Skip if gh is not available.
	if _, err := findGH(); err != nil {
		t.Skip("gh not available on PATH")
	}

	// Build a hosts.yml that mirrors the real structure.
	// The users block has nil values, just like gh 2.x writes.
	hosts := map[string]interface{}{
		"github.com": map[string]interface{}{
			"git_protocol": "https",
			"user":         "",
			"users": map[string]interface{}{
				"__test_nonexistent_user__": nil,
			},
		},
	}

	tmp := t.TempDir()
	hostsPath := filepath.Join(tmp, "hosts.yml")
	data, _ := yaml.Marshal(hosts)
	os.WriteFile(hostsPath, data, 0644)

	// Should fail gracefully (no such user in keyring).
	err := enrichHostsWithTokens(hostsPath)
	if err == nil {
		t.Error("expected error for nonexistent user, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "no tokens could be extracted") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnrichHostsWithTokens_NoUsersBlock(t *testing.T) {
	// hosts.yml without a "users" key should be handled gracefully.
	hosts := map[string]interface{}{
		"github.com": map[string]interface{}{
			"git_protocol": "https",
			"user":         "testuser",
		},
	}

	tmp := t.TempDir()
	hostsPath := filepath.Join(tmp, "hosts.yml")
	data, _ := yaml.Marshal(hosts)
	os.WriteFile(hostsPath, data, 0644)

	err := enrichHostsWithTokens(hostsPath)
	// Should fail with "no tokens" since there's no users block.
	if err == nil {
		t.Error("expected error for missing users block")
	}
}

func TestEnrichHostsWithTokens_MissingFile(t *testing.T) {
	err := enrichHostsWithTokens("/nonexistent/hosts.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestEnrichHostsWithTokens_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	hostsPath := filepath.Join(tmp, "hosts.yml")
	os.WriteFile(hostsPath, []byte("not: valid: yaml: ["), 0644)

	err := enrichHostsWithTokens(hostsPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

// ---------------------------------------------------------------------------
// setup — full resolver flow
// ---------------------------------------------------------------------------

func TestSetup_NoGHConfig(t *testing.T) {
	// When ~/.config/gh doesn't exist, setup should be a no-op.
	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{Name: "gh", Enabled: true}
	ctx := &registry.SetupContext{
		Home:          t.TempDir(), // no .config/gh here
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	if err := setup(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dockerArgs) != 0 {
		t.Errorf("expected no docker args, got %v", dockerArgs)
	}
}

func TestSetup_WithGHConfig(t *testing.T) {
	// Create a fake gh config directory.
	home := t.TempDir()
	ghDir := filepath.Join(home, ".config", "gh")
	os.MkdirAll(ghDir, 0755)

	hostsContent := `github.com:
    git_protocol: https
    user: testuser
    users:
        testuser:
`
	os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte(hostsContent), 0644)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string
	staging := t.TempDir()

	ext := &registry.Extension{Name: "gh", Enabled: true}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	err := setup(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have added a volume mount for the staging directory.
	if len(dockerArgs) != 2 {
		t.Fatalf("expected 2 docker args (-v, path), got %v", dockerArgs)
	}
	if dockerArgs[0] != "-v" {
		t.Errorf("expected -v flag, got %q", dockerArgs[0])
	}
	if !strings.HasSuffix(dockerArgs[1], ":/home/claude/.config/gh:ro") {
		t.Errorf("expected gh mount, got %q", dockerArgs[1])
	}

	// hosts.yml should exist in staging (copied from the gh dir).
	stagedHosts := filepath.Join(staging, "hosts.yml")
	if _, err := os.Stat(stagedHosts); err != nil {
		t.Errorf("hosts.yml not found in staging: %v", err)
	}
}

// findGH is a test helper that checks if gh is on PATH.
func findGH() (string, error) {
	return findInPath("gh")
}

func findInPath(name string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}
