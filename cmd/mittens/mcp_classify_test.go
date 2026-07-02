package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

func TestClassifyMCPServer(t *testing.T) {
	cases := []struct {
		name     string
		server   mcpconfig.Server
		shape    mcpShape
		mode     string
		warnings bool
	}{
		{"remote", mcpconfig.Server{URL: "https://x.test/mcp"}, mcpShapeRemote, mcpModeDirect, false},
		{"npx", mcpconfig.Server{Command: "npx", Args: []string{"-y", "pkg"}}, mcpShapeStdioContainer, mcpModeDirect, false},
		{"host-noenv", mcpconfig.Server{Command: "/opt/tool/server"}, mcpShapeStdioHost, mcpModeMount, false},
		{"host-env", mcpconfig.Server{Command: "/opt/tool/server", Env: map[string]string{"T": "x"}}, mcpShapeStdioHost, mcpModeProxy, false},
		{"filesystem-broad", mcpconfig.Server{Command: "/usr/bin/mcp-server-filesystem", Env: map[string]string{"T": "x"}, Args: []string{"/etc"}}, mcpShapeStdioHost, mcpModeMount, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := classifyMCPServer(tc.server)
			if c.Shape != tc.shape {
				t.Errorf("shape = %q, want %q", c.Shape, tc.shape)
			}
			if c.RecommendedMode != tc.mode {
				t.Errorf("mode = %q, want %q", c.RecommendedMode, tc.mode)
			}
			if (len(c.Warnings) > 0) != tc.warnings {
				t.Errorf("warnings = %v, want any=%v", c.Warnings, tc.warnings)
			}
		})
	}
}

func TestExpandMCPValue(t *testing.T) {
	env := map[string]string{"TOKEN": "secret"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	got, injected, unresolved := expandMCPValue("Bearer ${TOKEN}", lookup)
	if got != "Bearer secret" || len(injected) != 1 || injected[0] != "TOKEN" || len(unresolved) != 0 {
		t.Fatalf("set var: got %q injected=%v unresolved=%v", got, injected, unresolved)
	}

	got, _, _ = expandMCPValue("${MISSING:-fallback}", lookup)
	if got != "fallback" {
		t.Fatalf("default: got %q", got)
	}

	got, _, unresolved = expandMCPValue("${MISSING}", lookup)
	if got != "${MISSING}" || len(unresolved) != 1 {
		t.Fatalf("unset no default should be untouched: got %q unresolved=%v", got, unresolved)
	}
}
