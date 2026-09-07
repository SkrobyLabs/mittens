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
		{"node-local-script", mcpconfig.Server{Command: "node", Args: []string{"/opt/tool/server.js"}}, mcpShapeStdioHost, mcpModeMount, false},
		{"python-local-script-env", mcpconfig.Server{Command: "python", Args: []string{"/opt/tool/server.py"}, Env: map[string]string{"T": "x"}}, mcpShapeStdioHost, mcpModeProxy, false},
		{"remote-with-local-command", mcpconfig.Server{URL: "https://x.test/mcp", Command: "/opt/tool/server"}, mcpShapeRemote, mcpModeDirect, false},
		{"node-log-path", mcpconfig.Server{Command: "node", Args: []string{"server", "--log-file", "/tmp/server.log"}}, mcpShapeStdioContainer, mcpModeDirect, false},
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

func TestAutomaticMCPModeRequiresExplicitHostExecution(t *testing.T) {
	server := mcpconfig.Server{Command: "/opt/tool/server", Env: map[string]string{"TOKEN": "value"}}
	if got := automaticMCPMode(server); got != mcpModeMount {
		t.Fatalf("Auto = %q, want mount without granting host execution", got)
	}
	if got := automaticMCPMode(mcpconfig.Server{URL: "https://example.test/mcp"}); got != mcpModeDirect {
		t.Fatalf("remote Auto = %q, want direct", got)
	}
}
