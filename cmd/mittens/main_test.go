package main

import "testing"

func TestHasInitCommand_FirstArg(t *testing.T) {
	if !hasInitCommand([]string{"--init"}) {
		t.Fatal("expected --init to trigger init")
	}
	if !hasInitCommand([]string{"init"}) {
		t.Fatal("expected init to trigger init")
	}
}

func TestHasInitCommand_NonFirstArg(t *testing.T) {
	if !hasInitCommand([]string{"--verbose", "--init"}) {
		t.Fatal("expected non-leading --init to trigger init")
	}
	if !hasInitCommand([]string{"--provider", "codex", "init"}) {
		t.Fatal("expected non-leading init to trigger init")
	}
}

func TestHasInitCommand_AfterSeparator(t *testing.T) {
	if hasInitCommand([]string{"--", "--init"}) {
		t.Fatal("did not expect --init after -- to trigger init")
	}
	if hasInitCommand([]string{"--verbose", "--", "init"}) {
		t.Fatal("did not expect init after -- to trigger init")
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
