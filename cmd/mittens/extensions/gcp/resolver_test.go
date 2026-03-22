package gcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/SkrobyLabs/mittens/internal/fileutil"
)

// ---------------------------------------------------------------------------
// listConfigs (filesystem-backed — uses temp dir as HOME)
// ---------------------------------------------------------------------------

func TestListConfigs(t *testing.T) {
	tmp := t.TempDir()

	// Override HOME so listConfigs() finds our fixture.
	t.Setenv("HOME", tmp)

	configsDir := filepath.Join(tmp, ".config", "gcloud", "configurations")
	os.MkdirAll(configsDir, 0755)

	// Create matching config files.
	os.WriteFile(filepath.Join(configsDir, "config_default"), []byte("[core]\naccount = a@b.com\n"), 0644)
	os.WriteFile(filepath.Join(configsDir, "config_prod"), []byte("[core]\naccount = p@b.com\n"), 0644)

	// Create non-matching files that should be ignored.
	os.WriteFile(filepath.Join(configsDir, "not-a-config"), []byte("nope"), 0644)
	os.MkdirAll(filepath.Join(configsDir, "subdir"), 0755)

	configs, err := listConfigs()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"default", "prod"}
	if len(configs) != len(want) {
		t.Fatalf("got %v, want %v", configs, want)
	}
	for i, c := range configs {
		if c != want[i] {
			t.Errorf("configs[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestListConfigs_NoDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	_, err := listConfigs()
	if err == nil {
		t.Error("expected error when configurations dir doesn't exist")
	}
}

// ---------------------------------------------------------------------------
// setup — full resolver flow: config staging + credential DBs + docker args
// ---------------------------------------------------------------------------

// newGCPFixture creates a realistic ~/.config/gcloud directory.
func newGCPFixture(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	gcloudDir := filepath.Join(home, ".config", "gcloud")
	configsDir := filepath.Join(gcloudDir, "configurations")
	os.MkdirAll(configsDir, 0755)

	os.WriteFile(filepath.Join(configsDir, "config_default"),
		[]byte("[core]\naccount = default@example.com\nproject = my-project\n"), 0644)
	os.WriteFile(filepath.Join(configsDir, "config_prod"),
		[]byte("[core]\naccount = prod@example.com\nproject = prod-project\n"), 0644)
	os.WriteFile(filepath.Join(configsDir, "config_dev"),
		[]byte("[core]\naccount = dev@example.com\nproject = dev-project\n"), 0644)

	// Credential databases.
	os.WriteFile(filepath.Join(gcloudDir, "credentials.db"), []byte("sqlite-data"), 0600)
	os.WriteFile(filepath.Join(gcloudDir, "access_tokens.db"), []byte("tokens-data"), 0600)

	// ADC file.
	os.WriteFile(filepath.Join(gcloudDir, "application_default_credentials.json"),
		[]byte(`{"client_id":"adc"}`), 0600)

	// Legacy credentials.
	legacyDir := filepath.Join(gcloudDir, "legacy_credentials")
	os.MkdirAll(legacyDir, 0755)
	os.WriteFile(filepath.Join(legacyDir, "old.json"), []byte(`{"key":"old"}`), 0600)

	return home
}

func TestSetup_FilteredConfigs(t *testing.T) {
	home := newGCPFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "gcp",
		Enabled: true,
		Args:    []string{"default", "prod"},
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    staging,
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	// Docker args should mount staging dir.
	joined := strings.Join(dockerArgs, " ")
	if !strings.Contains(joined, staging+":/home/claude/.config/gcloud:ro") {
		t.Errorf("docker args missing gcloud mount, got: %v", dockerArgs)
	}

	// Only requested configs should be staged.
	configsDir := filepath.Join(staging, "configurations")
	if !fileutil.FileExists(filepath.Join(configsDir, "config_default")) {
		t.Error("config_default should be staged")
	}
	if !fileutil.FileExists(filepath.Join(configsDir, "config_prod")) {
		t.Error("config_prod should be staged")
	}
	if fileutil.FileExists(filepath.Join(configsDir, "config_dev")) {
		t.Error("config_dev should NOT be staged")
	}

	// active_config should be set to the first arg.
	activeData, err := os.ReadFile(filepath.Join(staging, "active_config"))
	if err != nil {
		t.Fatal("active_config missing")
	}
	if strings.TrimSpace(string(activeData)) != "default" {
		t.Errorf("active_config = %q, want 'default'", strings.TrimSpace(string(activeData)))
	}

	// Credential DBs should be copied.
	if !fileutil.FileExists(filepath.Join(staging, "credentials.db")) {
		t.Error("credentials.db should be copied")
	}
	if !fileutil.FileExists(filepath.Join(staging, "access_tokens.db")) {
		t.Error("access_tokens.db should be copied")
	}

	// ADC file should be copied.
	if !fileutil.FileExists(filepath.Join(staging, "application_default_credentials.json")) {
		t.Error("application_default_credentials.json should be copied")
	}

	// Legacy credentials should be copied.
	if !fileutil.FileExists(filepath.Join(staging, "legacy_credentials", "old.json")) {
		t.Error("legacy_credentials should be copied")
	}
}

func TestSetup_AllMode_GCP(t *testing.T) {
	home := newGCPFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "gcp",
		Enabled: true,
		AllMode: true,
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(dockerArgs, " ")
	gcloudDir := filepath.Join(home, ".config", "gcloud")
	if !strings.Contains(joined, gcloudDir+":/home/claude/.config/gcloud:ro") {
		t.Errorf("AllMode should mount entire gcloud dir, got: %v", dockerArgs)
	}
}

func TestSetup_NoConfigs_GCP(t *testing.T) {
	home := newGCPFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "gcp",
		Enabled: true,
		Args:    nil,
	}
	ctx := &registry.SetupContext{
		Home:          home,
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}

	if len(dockerArgs) != 0 {
		t.Errorf("expected no docker args when no configs, got: %v", dockerArgs)
	}
}
