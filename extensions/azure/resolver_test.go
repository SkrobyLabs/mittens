package azure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// stripBOM
// ---------------------------------------------------------------------------

func TestStripBOM(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"with BOM", []byte{0xEF, 0xBB, 0xBF, '{', '}'}, "{}"},
		{"without BOM", []byte("{}"), "{}"},
		{"empty input", []byte{}, ""},
		{"short input 1 byte", []byte{0xEF}, string([]byte{0xEF})},
		{"short input 2 bytes", []byte{0xEF, 0xBB}, string([]byte{0xEF, 0xBB})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripBOM(tc.input)
			if string(got) != tc.want {
				t.Errorf("stripBOM() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// filterAzureProfile (filesystem-backed)
// ---------------------------------------------------------------------------

func TestFilterAzureProfile(t *testing.T) {
	tmp := t.TempDir()

	profile := map[string]interface{}{
		"installationId": "test-id",
		"subscriptions": []interface{}{
			map[string]interface{}{
				"name":      "prod",
				"id":        "prod-id",
				"isDefault": true,
				"state":     "Enabled",
			},
			map[string]interface{}{
				"name":      "dev",
				"id":        "dev-id",
				"isDefault": false,
				"state":     "Enabled",
			},
			map[string]interface{}{
				"name":      "staging",
				"id":        "staging-id",
				"isDefault": false,
				"state":     "Enabled",
			},
		},
	}
	srcData, _ := json.Marshal(profile)
	srcPath := filepath.Join(tmp, "azureProfile.json")
	os.WriteFile(srcPath, srcData, 0644)

	destPath := filepath.Join(tmp, "filtered.json")
	if err := filterAzureProfile(srcPath, destPath, []string{"dev", "staging"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	subs, ok := result["subscriptions"].([]interface{})
	if !ok {
		t.Fatal("subscriptions not an array")
	}
	if len(subs) != 2 {
		t.Fatalf("got %d subscriptions, want 2", len(subs))
	}

	// First should be default.
	first := subs[0].(map[string]interface{})
	if first["name"] != "dev" {
		t.Errorf("first sub name = %v, want dev", first["name"])
	}
	if first["isDefault"] != true {
		t.Errorf("first sub isDefault = %v, want true", first["isDefault"])
	}

	// installationId preserved.
	if result["installationId"] != "test-id" {
		t.Errorf("installationId lost")
	}
}

func TestFilterAzureProfile_WithBOM(t *testing.T) {
	tmp := t.TempDir()

	profile := map[string]interface{}{
		"subscriptions": []interface{}{
			map[string]interface{}{
				"name":      "only",
				"id":        "only-id",
				"isDefault": false,
			},
		},
	}
	srcData, _ := json.Marshal(profile)
	// Prepend BOM.
	bomData := append([]byte{0xEF, 0xBB, 0xBF}, srcData...)
	srcPath := filepath.Join(tmp, "bom.json")
	os.WriteFile(srcPath, bomData, 0644)

	destPath := filepath.Join(tmp, "filtered.json")
	if err := filterAzureProfile(srcPath, destPath, []string{"only"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to parse filtered output: %v", err)
	}
}

// ---------------------------------------------------------------------------
// setup — full resolver flow: filtered profile + supporting files + docker args
// ---------------------------------------------------------------------------

// newAzureFixture creates a realistic ~/.azure directory in a temp dir.
func newAzureFixture(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	azureDir := filepath.Join(home, ".azure")
	os.MkdirAll(azureDir, 0755)

	profile := map[string]interface{}{
		"installationId": "install-1",
		"subscriptions": []interface{}{
			map[string]interface{}{
				"name": "prod", "id": "prod-id",
				"isDefault": true, "state": "Enabled",
			},
			map[string]interface{}{
				"name": "dev", "id": "dev-id",
				"isDefault": false, "state": "Enabled",
			},
			map[string]interface{}{
				"name": "staging", "id": "staging-id",
				"isDefault": false, "state": "Enabled",
			},
		},
	}
	data, _ := json.MarshalIndent(profile, "", "  ")
	os.WriteFile(filepath.Join(azureDir, "azureProfile.json"), data, 0644)

	// Supporting files.
	os.WriteFile(filepath.Join(azureDir, "msal_token_cache.json"), []byte(`{"tokens":"data"}`), 0600)
	os.WriteFile(filepath.Join(azureDir, "az.json"), []byte(`{"cli":"config"}`), 0600)

	return home
}

func TestSetup_FilteredSubscriptions(t *testing.T) {
	home := newAzureFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "azure",
		Enabled: true,
		Args:    []string{"dev", "staging"},
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
	if !strings.Contains(joined, staging+":/home/claude/.azure:ro") {
		t.Errorf("docker args missing mount, got: %v", dockerArgs)
	}

	// Filtered profile should have only dev and staging.
	data, err := os.ReadFile(filepath.Join(staging, "azureProfile.json"))
	if err != nil {
		t.Fatal("filtered azureProfile.json missing")
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	subs := result["subscriptions"].([]interface{})
	if len(subs) != 2 {
		t.Fatalf("got %d subscriptions, want 2", len(subs))
	}

	// First should be marked as default.
	first := subs[0].(map[string]interface{})
	if first["name"] != "dev" {
		t.Errorf("first sub = %v, want dev", first["name"])
	}
	if first["isDefault"] != true {
		t.Error("first sub should be default")
	}

	// Second should not be default.
	second := subs[1].(map[string]interface{})
	if second["isDefault"] != false {
		t.Error("second sub should not be default")
	}

	// installationId preserved.
	if result["installationId"] != "install-1" {
		t.Error("installationId lost")
	}
}

func TestSetup_SupportingFilesCopied(t *testing.T) {
	home := newAzureFixture(t)
	staging := t.TempDir()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "azure",
		Enabled: true,
		Args:    []string{"prod"},
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

	// Supporting files should be copied.
	for _, name := range []string{"msal_token_cache.json", "az.json"} {
		if _, err := os.Stat(filepath.Join(staging, name)); err != nil {
			t.Errorf("supporting file %q should be copied: %v", name, err)
		}
	}

	// Files that don't exist in fixture should not cause errors.
	if _, err := os.Stat(filepath.Join(staging, "clouds.config")); !os.IsNotExist(err) {
		t.Error("clouds.config should not exist (wasn't in fixture)")
	}
}

func TestSetup_AllMode_Azure(t *testing.T) {
	home := newAzureFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "azure",
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
	azureDir := filepath.Join(home, ".azure")
	if !strings.Contains(joined, azureDir+":/home/claude/.azure:ro") {
		t.Errorf("AllMode should mount entire azure dir, got: %v", dockerArgs)
	}
}

func TestSetup_NoSubscriptions(t *testing.T) {
	home := newAzureFixture(t)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "azure",
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
		t.Errorf("expected no docker args when no subscriptions, got: %v", dockerArgs)
	}
}
