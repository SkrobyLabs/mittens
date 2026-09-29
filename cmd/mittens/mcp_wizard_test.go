package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMCPDiscoveryUsesSelectedHarness(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	files := map[string]string{
		filepath.Join(home, ".claude.json"):               `{"mcpServers":{"claude-only":{"command":"claude-helper"}}}`,
		filepath.Join(home, ".codex", "config.toml"):      "[mcp_servers.codex-only]\nurl = \"https://example.test/mcp\"\n",
		filepath.Join(home, ".codex", "mcp-domains.conf"): "domain-only=example.test\n",
		filepath.Join(workspace, ".mcp.json"):             `{"mcpServers":{"workspace-only":{"command":"workspace-helper"}}}`,
	}
	for path, data := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		provider *Provider
		want     []string
	}{
		{CodexProvider(), []string{"codex-only"}},
		{ClaudeProvider(), []string{"claude-only", "workspace-only"}},
	} {
		t.Run(tc.provider.Name, func(t *testing.T) {
			servers := readMCPServers(tc.provider, home, workspace)
			if got := discoverMCPNames(servers, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("discovered %v, want %v", got, tc.want)
			}
		})
	}
	servers := readMCPServers(CodexProvider(), home, workspace)
	saved := []MCPServerPolicy{{Name: "claude-only", Mode: mcpModeDirect}, {Name: "codex-only", Mode: mcpModeMount}}
	if got := discoverMCPNames(servers, saved); !reflect.DeepEqual(got, []string{"claude-only", "codex-only"}) {
		t.Fatalf("saved selection unavailable for removal: %v", got)
	}
	if got := missingMCPServerNames(servers, saved); !reflect.DeepEqual(got, []string{"claude-only"}) {
		t.Fatalf("missing servers = %v", got)
	}
}

func TestRetainedUnavailableMCPPolicyPreservesApprovedPolicy(t *testing.T) {
	existing := MCPServerPolicy{Name: "legacy", Mode: mcpModeProxy, CommandPin: "sha256:approved"}
	entry, keep := retainedUnavailableMCPPolicy(map[string]MCPServerPolicy{"legacy": existing}, "legacy", false)
	if !keep || !reflect.DeepEqual(entry, existing) {
		t.Fatalf("unavailable selection = (%#v, %t), want (%#v, true)", entry, keep, existing)
	}

	if _, keep := retainedUnavailableMCPPolicy(map[string]MCPServerPolicy{"legacy": existing}, "legacy", true); keep {
		t.Fatal("configured server should continue through mode selection")
	}
	if _, keep := retainedUnavailableMCPPolicy(map[string]MCPServerPolicy{}, "new", false); keep {
		t.Fatal("new unavailable server should not be retained")
	}
	if !retainMCPAllWithoutDiscovery(true, nil) {
		t.Fatal("saved all selection should survive an empty discovery")
	}
	if retainMCPAllWithoutDiscovery(false, nil) || retainMCPAllWithoutDiscovery(true, []string{"configured"}) {
		t.Fatal("only an empty discovery should retain a saved all selection")
	}
}
