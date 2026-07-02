package main

import "testing"

func TestNormalizeMCP_MigratesCapability(t *testing.T) {
	p := defaultProjectPolicy()
	p.Capabilities = []CapabilityPolicy{
		{Name: "aws"},
		{Name: "mcp", Args: []string{"shortcut", "github"}},
	}
	p.applyDefaults()

	for _, c := range p.Capabilities {
		if c.Name == "mcp" {
			t.Fatalf("mcp capability should be migrated away: %#v", p.Capabilities)
		}
	}
	if len(p.MCP.Servers) != 2 {
		t.Fatalf("expected 2 migrated servers, got %#v", p.MCP.Servers)
	}
	for _, s := range p.MCP.Servers {
		if s.Mode != mcpModeDirect {
			t.Errorf("migrated server %q mode = %q, want direct", s.Name, s.Mode)
		}
	}
}

func TestNormalizeMCP_MigratesAllCapability(t *testing.T) {
	p := defaultProjectPolicy()
	p.Capabilities = []CapabilityPolicy{{Name: "mcp", All: true}}
	p.applyDefaults()
	if !p.MCP.All {
		t.Fatalf("expected MCP.All after migrating --mcp-all capability")
	}
}

func TestValidate_MCPModesAndPins(t *testing.T) {
	cases := []struct {
		name    string
		servers []MCPServerPolicy
		wantErr bool
	}{
		{"valid direct", []MCPServerPolicy{{Name: "a", Mode: "direct"}}, false},
		{"valid proxy+pin", []MCPServerPolicy{{Name: "a", Mode: "proxy", CommandPin: "sha256:x"}}, false},
		{"pin without proxy", []MCPServerPolicy{{Name: "a", Mode: "mount", CommandPin: "sha256:x"}}, true},
		{"invalid mode", []MCPServerPolicy{{Name: "a", Mode: "bogus"}}, true},
		{"empty name", []MCPServerPolicy{{Name: "", Mode: "direct"}}, true},
		{"duplicate", []MCPServerPolicy{{Name: "a", Mode: "direct"}, {Name: "a", Mode: "mount"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := defaultProjectPolicy()
			p.MCP.Servers = tc.servers
			err := p.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestToLegacyFlags_MCPRoundTrip(t *testing.T) {
	p := defaultProjectPolicy()
	p.MCP.Servers = []MCPServerPolicy{{Name: "shortcut", Mode: "proxy", CommandPin: "sha256:x"}, {Name: "gh", Mode: "direct"}}
	flags := p.ToLegacyFlags()
	found := false
	for i, f := range flags {
		if f == "--mcp" && i+1 < len(flags) && flags[i+1] == "shortcut,gh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --mcp shortcut,gh in %v", flags)
	}
}
