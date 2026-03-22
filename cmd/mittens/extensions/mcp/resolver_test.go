package mcp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// readMCPDomainNames
// ---------------------------------------------------------------------------

func TestReadMCPDomainNames(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "mcp-domains.conf")
	content := `# MCP server domain mappings
shortcut=api.shortcut.com,shortcut.com
github=api.github.com
# blank line below

empty-line-test=example.com
`
	os.WriteFile(f, []byte(content), 0644)

	names, err := readMCPDomainNames(f)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"shortcut", "github", "empty-line-test"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestReadMCPDomainNames_Missing(t *testing.T) {
	_, err := readMCPDomainNames("/nonexistent/mcp-domains.conf")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// readMCPServerKeys
// ---------------------------------------------------------------------------

func TestReadMCPServerKeys_ClaudeJSON(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, ".claude.json")
	content := `{
	"mcpServers": {
		"shortcut": {"command": "npx", "args": ["shortcut"]},
		"github": {"command": "npx", "args": ["github"]}
	},
	"otherKey": "value"
}`
	os.WriteFile(f, []byte(content), 0644)

	names, err := readMCPServerKeys(f)
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(names)
	want := []string{"github", "shortcut"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestReadMCPServerKeys_NoMCPKey(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, ".claude.json")
	os.WriteFile(f, []byte(`{"otherKey": "value"}`), 0644)

	names, err := readMCPServerKeys(f)
	if err != nil {
		t.Fatal(err)
	}
	if names != nil {
		t.Errorf("expected nil, got %v", names)
	}
}

func TestReadMCPServerKeys_MCPJSON(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, ".mcp.json")
	content := `{
	"mcpServers": {
		"local-server": {"command": "python", "args": ["server.py"]}
	}
}`
	os.WriteFile(f, []byte(content), 0644)

	names, err := readMCPServerKeys(f)
	if err != nil {
		t.Fatal(err)
	}

	if len(names) != 1 || names[0] != "local-server" {
		t.Errorf("got %v, want [local-server]", names)
	}
}

func TestReadMCPServerKeys_Missing(t *testing.T) {
	_, err := readMCPServerKeys("/nonexistent/.mcp.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ---------------------------------------------------------------------------
// setup — full resolver flow: sets MITTENS_MCP env var
// ---------------------------------------------------------------------------

func TestSetup_SelectedServers(t *testing.T) {
	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "mcp",
		Enabled: true,
		RawArg:  "shortcut,github",
		Args:    []string{"shortcut", "github"},
	}
	ctx := &registry.SetupContext{
		Home:          t.TempDir(),
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
	if !strings.Contains(joined, "MITTENS_MCP=shortcut,github") {
		t.Errorf("expected MITTENS_MCP env var, got: %v", dockerArgs)
	}
}

func TestSetup_AllMode_MCP(t *testing.T) {
	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "mcp",
		Enabled: true,
		AllMode: true,
	}
	ctx := &registry.SetupContext{
		Home:          t.TempDir(),
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
	if !strings.Contains(joined, "MITTENS_MCP=__all__") {
		t.Errorf("AllMode should set __all__ sentinel, got: %v", dockerArgs)
	}
}

func TestSetup_NoServers(t *testing.T) {
	var dockerArgs []string
	var firewallExtra []string
	var tempDirs []string

	ext := &registry.Extension{
		Name:    "mcp",
		Enabled: true,
		Args:    nil,
		RawArg:  "",
	}
	ctx := &registry.SetupContext{
		Home:          t.TempDir(),
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
		t.Errorf("expected no docker args when no servers, got: %v", dockerArgs)
	}
}
