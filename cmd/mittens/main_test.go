package main

import (
	"strings"
	"testing"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
)

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

func TestProviderByName_Aliases(t *testing.T) {
	cases := map[string]string{
		"openai":    "codex",
		"anthropic": "claude",
		"local":     "ollama",
		"":          "claude", // applyLaunchConfig relies on empty -> claude
	}
	for alias, want := range cases {
		p, err := providerByName(alias)
		if err != nil {
			t.Fatalf("providerByName(%q): %v", alias, err)
		}
		if p.Name != want {
			t.Fatalf("providerByName(%q) = %q, want %q", alias, p.Name, want)
		}
	}
}

func TestProviderByName_Unknown(t *testing.T) {
	if _, err := providerByName("nope"); err == nil {
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

func TestRejectDeprecatedLaunchPolicyFlags(t *testing.T) {
	exts := []*registry.Extension{{
		Name:  "aws",
		Flags: []registry.ExtensionFlag{{Name: "--aws"}},
	}}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--provider", "codex"}, exts); err == nil || !strings.Contains(err.Error(), "policy set provider.name") {
		t.Fatalf("expected provider policy error, got %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--aws"}, exts); err == nil || !strings.Contains(err.Error(), "aws capability") {
		t.Fatalf("expected extension policy error, got %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--worker"}, exts); err == nil || !strings.Contains(err.Error(), "policy set provider.profile") {
		t.Fatalf("expected legacy role policy error, got %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--verbose", "--", "--provider", "codex"}, exts); err != nil {
		t.Fatalf("provider args after separator should be forwarded: %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--session", "--no-build"}, exts); err != nil {
		t.Fatalf("runtime flags should still be accepted: %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--resume"}, exts); err == nil || !strings.Contains(err.Error(), "pass provider resume args after `--`") {
		t.Fatalf("expected resume pass-through error, got %v", err)
	}
	if err := rejectDeprecatedLaunchPolicyFlags([]string{"--", "--resume", "latest"}, exts); err != nil {
		t.Fatalf("resume args after separator should be forwarded: %v", err)
	}
}
