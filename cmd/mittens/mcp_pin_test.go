package main

import (
	"testing"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

// TestMCPCommandPin_FixedVector locks the canonicalization so it cannot drift
// silently. The expected digest is sha256 of the compact JSON:
//
//	{"command":"npx","args":["-y","@shortcut/mcp"],"envKeys":["A_TOKEN","B_TOKEN"]}
func TestMCPCommandPin_FixedVector(t *testing.T) {
	s := mcpconfig.Server{
		Command: "npx",
		Args:    []string{"-y", "@shortcut/mcp"},
		// Deliberately unsorted map; pin must sort keys.
		Env: map[string]string{"B_TOKEN": "2", "A_TOKEN": "1"},
	}
	const want = "sha256:d41531f4e20cbd2b869f6192c56608e2470f2e58a84db9528d1d7eca6c4f3240"
	if got := mcpCommandPin(s); got != want {
		t.Fatalf("pin drift:\n got %s\nwant %s", got, want)
	}
}

func TestMCPCommandPin_NilArgsEqualsEmpty(t *testing.T) {
	a := mcpCommandPin(mcpconfig.Server{Command: "x"})
	b := mcpCommandPin(mcpconfig.Server{Command: "x", Args: []string{}})
	if a != b {
		t.Fatalf("nil vs empty args produced different pins: %s vs %s", a, b)
	}
}
