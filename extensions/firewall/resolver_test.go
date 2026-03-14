package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// readFirewallDomains
// ---------------------------------------------------------------------------

func TestReadFirewallDomains(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "firewall.conf")
	content := `# This is a comment
api.github.com
registry.npmjs.org

# Another comment
pypi.org
example.com  # inline comment
`
	os.WriteFile(f, []byte(content), 0644)

	domains, err := readFirewallDomains(f)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"api.github.com", "registry.npmjs.org", "pypi.org", "example.com"}
	if len(domains) != len(want) {
		t.Fatalf("got %v, want %v", domains, want)
	}
	for i, d := range domains {
		if d != want[i] {
			t.Errorf("domains[%d] = %q, want %q", i, d, want[i])
		}
	}
}

func TestReadFirewallDomains_Empty(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "empty.conf")
	os.WriteFile(f, []byte("# only comments\n\n# nothing\n"), 0644)

	domains, err := readFirewallDomains(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 0 {
		t.Errorf("expected empty, got %v", domains)
	}
}

func TestReadFirewallDomains_Missing(t *testing.T) {
	_, err := readFirewallDomains("/nonexistent/firewall.conf")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// resolveConfPath
// ---------------------------------------------------------------------------

func TestResolveConfPath(t *testing.T) {
	// Save and restore DefaultConfPath.
	origDefault := DefaultConfPath
	defer func() { DefaultConfPath = origDefault }()

	t.Run("custom path takes precedence", func(t *testing.T) {
		DefaultConfPath = "/some/default"
		got := resolveConfPath("/custom/path")
		if got != "/custom/path" {
			t.Errorf("got %q, want /custom/path", got)
		}
	})

	t.Run("DefaultConfPath used when no custom", func(t *testing.T) {
		DefaultConfPath = "/some/default"
		got := resolveConfPath("")
		if got != "/some/default" {
			t.Errorf("got %q, want /some/default", got)
		}
	})

	t.Run("empty default falls through", func(t *testing.T) {
		DefaultConfPath = ""
		got := resolveConfPath("")
		// Fallback is /etc/mittens/firewall.conf which likely doesn't exist in test,
		// so we expect empty string.
		if got != "" {
			t.Errorf("got %q, want empty (fallback doesn't exist)", got)
		}
	})
}

// ---------------------------------------------------------------------------
// setup — full resolver flow: mounts conf file + sets env var
// ---------------------------------------------------------------------------

func TestSetup_DefaultConf(t *testing.T) {
	tmp := t.TempDir()
	confPath := filepath.Join(tmp, "firewall.conf")
	os.WriteFile(confPath, []byte("api.github.com\n"), 0644)

	origDefault := DefaultConfPath
	DefaultConfPath = confPath
	defer func() { DefaultConfPath = origDefault }()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "firewall",
		Enabled: true,
	}
	ctx := &registry.SetupContext{
		Home:          tmp,
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

	// Should mount the conf file read-only.
	if !strings.Contains(joined, confPath+":/mnt/claude-config/firewall.conf:ro") {
		t.Errorf("docker args missing conf mount, got: %v", dockerArgs)
	}

	// Should set MITTENS_FIREWALL=true.
	if !strings.Contains(joined, "MITTENS_FIREWALL=true") {
		t.Errorf("docker args missing MITTENS_FIREWALL, got: %v", dockerArgs)
	}
}

func TestSetup_CustomConfPath(t *testing.T) {
	tmp := t.TempDir()
	customPath := filepath.Join(tmp, "custom.conf")
	os.WriteFile(customPath, []byte("example.com\n"), 0644)

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "firewall",
		Enabled: true,
		RawArg:  customPath, // user passed --firewall /path/to/custom.conf
	}
	ctx := &registry.SetupContext{
		Home:          tmp,
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
	if !strings.Contains(joined, customPath+":/mnt/claude-config/firewall.conf:ro") {
		t.Errorf("should use custom path, got: %v", dockerArgs)
	}
}

func TestSetup_MissingConf(t *testing.T) {
	origDefault := DefaultConfPath
	DefaultConfPath = ""
	defer func() { DefaultConfPath = origDefault }()

	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "firewall",
		Enabled: true,
	}
	ctx := &registry.SetupContext{
		Home:          t.TempDir(),
		Extension:     ext,
		DockerArgs:    &dockerArgs,
		FirewallExtra: &firewallExtra,
		TempDirs:      &tempDirs,
		StagingDir:    t.TempDir(),
	}

	err := setup(ctx)
	if err == nil {
		t.Error("expected error when no conf file available")
	}
}
