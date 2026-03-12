package main

import "testing"

func TestParseExistingConfig_SeparatesProviders(t *testing.T) {
	dirs, providers, exts, firewall, opts := parseExistingConfig([]string{
		"--dir /tmp/a",
		"--provider codex",
		"--provider claude",
		"--aws dev",
		"--firewall-dev",
		"--yolo",
	})

	if len(dirs) != 1 || dirs[0] != "--dir /tmp/a" {
		t.Fatalf("unexpected dirs: %v", dirs)
	}
	if len(providers) != 2 || providers[0] != "--provider codex" || providers[1] != "--provider claude" {
		t.Fatalf("unexpected providers: %v", providers)
	}
	if len(exts) != 1 || exts[0] != "--aws dev" {
		t.Fatalf("unexpected exts: %v", exts)
	}
	if len(firewall) != 1 || firewall[0] != "--firewall-dev" {
		t.Fatalf("unexpected firewall: %v", firewall)
	}
	if len(opts) != 1 || opts[0] != "--yolo" {
		t.Fatalf("unexpected opts: %v", opts)
	}
}

func TestParseProviderLines(t *testing.T) {
	selected, def := parseProviderLines([]string{
		"--provider codex",
		"--provider gemini",
	})

	if !selected["codex"] || !selected["gemini"] {
		t.Fatalf("expected selected codex and gemini, got %v", selected)
	}
	if def != "gemini" {
		t.Fatalf("expected default provider gemini, got %q", def)
	}
}
