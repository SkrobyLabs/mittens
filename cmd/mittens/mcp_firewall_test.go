package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

func mcpExtApp(servers []MCPServerPolicy, all bool, workspace string) (*App, *registry.Extension) {
	ext := &registry.Extension{Name: "mcp"}
	return &App{
		Provider:   ClaudeProvider(),
		Workspace:  workspace,
		Extensions: []*registry.Extension{ext},
		MCPServers: servers,
		MCPAll:     all,
	}, ext
}

// TestConfigureMCPExtension_ExcludesProxy proves a proxied server is never in
// the firewall whitelist selection (its traffic egresses from the host).
func TestConfigureMCPExtension_ExcludesProxy(t *testing.T) {
	app, ext := mcpExtApp([]MCPServerPolicy{
		{Name: "direct1", Mode: mcpModeDirect},
		{Name: "proxied", Mode: mcpModeProxy},
	}, false, t.TempDir())
	app.configureMCPExtension()

	if ext.AllMode {
		t.Fatal("did not expect AllMode")
	}
	if strings.Contains(ext.RawArg, "proxied") {
		t.Fatalf("proxied server must be excluded from firewall selection, got %q", ext.RawArg)
	}
	if !strings.Contains(ext.RawArg, "direct1") {
		t.Fatalf("direct1 should be whitelisted, got %q", ext.RawArg)
	}
}

// TestConfigureMCPExtension_AllExpandsExcludingProxy proves that with "all" and a
// proxy server, the __all__ sentinel is expanded host-side to the explicit
// non-proxy names so the container never re-whitelists the proxied server.
func TestConfigureMCPExtension_AllExpandsExcludingProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
	  "mcpServers": {
	    "keepme": {"url": "https://a.test/mcp"},
	    "proxied": {"command": "/opt/x", "args": []}
	  }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app, ext := mcpExtApp([]MCPServerPolicy{{Name: "proxied", Mode: mcpModeProxy}}, true, t.TempDir())
	app.configureMCPExtension()

	if ext.AllMode {
		t.Fatal("with a proxy server, __all__ must be expanded, not passed as AllMode")
	}
	if strings.Contains(ext.RawArg, "proxied") {
		t.Fatalf("proxied excluded from expanded whitelist, got %q", ext.RawArg)
	}
	if !strings.Contains(ext.RawArg, "keepme") {
		t.Fatalf("non-proxy server should remain whitelisted, got %q", ext.RawArg)
	}
}

// TestConfigureMCPExtension_AllNoProxyUsesSentinel keeps the __all__ sentinel
// when no proxy servers are present.
func TestConfigureMCPExtension_AllNoProxyUsesSentinel(t *testing.T) {
	app, ext := mcpExtApp(nil, true, t.TempDir())
	app.configureMCPExtension()
	if !ext.AllMode {
		t.Fatal("expected AllMode sentinel when no proxy servers")
	}
}
