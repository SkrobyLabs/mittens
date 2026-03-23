package main

import "testing"

func TestHasSubFlag_Init(t *testing.T) {
	if !hasSubFlag([]string{"--init"}, "--init") {
		t.Fatal("expected --init to match")
	}
	if !hasSubFlag([]string{"--verbose", "--init"}, "--init") {
		t.Fatal("expected non-leading --init to match")
	}
}

func TestHasSubFlag_AfterSeparator(t *testing.T) {
	if hasSubFlag([]string{"--", "--init"}, "--init") {
		t.Fatal("did not expect --init after -- to match")
	}
	if hasSubFlag([]string{"--verbose", "--", "--init"}, "--init") {
		t.Fatal("did not expect --init after -- to match")
	}
}

func TestResolveProviderFromArgs_Default(t *testing.T) {
	p, err := resolveProviderFromArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "claude" {
		t.Fatalf("expected default provider claude, got %q", p.Name)
	}
}

func TestResolveProviderFromArgs_LastWins(t *testing.T) {
	p, err := resolveProviderFromArgs([]string{"--provider", "claude", "--provider", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "codex" {
		t.Fatalf("expected last provider codex, got %q", p.Name)
	}
}

func TestResolveProviderFromArgs_MissingArg(t *testing.T) {
	_, err := resolveProviderFromArgs([]string{"--provider"})
	if err == nil {
		t.Fatal("expected missing argument error")
	}
}

func TestResolveProviderFromArgs_Unknown(t *testing.T) {
	_, err := resolveProviderFromArgs([]string{"--provider", "nope"})
	if err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestHasSubFlag_Session(t *testing.T) {
	if !hasSubFlag([]string{"--session"}, "--session") {
		t.Fatal("expected --session to match")
	}
	if !hasSubFlag([]string{"--verbose", "--session"}, "--session") {
		t.Fatal("expected --session with other flags to match")
	}
	if hasSubFlag([]string{"--", "--session"}, "--session") {
		t.Fatal("did not expect --session after -- to match")
	}
}

func TestSessionConflictsDetected(t *testing.T) {
	// --session + --no-config should both be detectable
	args := []string{"--session", "--no-config"}
	if !hasSubFlag(args, "--session") || !hasSubFlag(args, "--no-config") {
		t.Fatal("expected both --session and --no-config to be detected")
	}

	// --session + --init should both be detectable
	args = []string{"--session", "--init"}
	if !hasSubFlag(args, "--session") || !hasSubFlag(args, "--init") {
		t.Fatal("expected both --session and --init to be detected")
	}
}
