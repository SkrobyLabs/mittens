package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProviderHealthClearsExpiredCooldownOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider_health.json")
	expired := time.Now().UTC().Add(-time.Minute)
	if err := writeJSONAtomic(path, map[string]HealthEntry{
		"anthropic/sonnet": {CooldownUntil: &expired},
	}); err != nil {
		t.Fatalf("writeJSONAtomic: %v", err)
	}

	health, err := NewProviderHealth(path)
	if err != nil {
		t.Fatalf("NewProviderHealth: %v", err)
	}
	if !health.Available("anthropic/sonnet", time.Now().UTC()) {
		t.Fatal("expected expired cooldown to be cleared on load")
	}
	if len(health.Snapshot()) != 0 {
		t.Fatalf("snapshot = %+v, want empty after cleanup", health.Snapshot())
	}
}

func TestProviderHealthAuthFailurePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider_health.json")
	now := time.Now().UTC()

	health, err := NewProviderHealth(path)
	if err != nil {
		t.Fatalf("NewProviderHealth: %v", err)
	}
	if err := health.MarkAuthFailure("openai/codex", now); err != nil {
		t.Fatalf("MarkAuthFailure: %v", err)
	}

	reloaded, err := NewProviderHealth(path)
	if err != nil {
		t.Fatalf("NewProviderHealth(reload): %v", err)
	}
	if reloaded.Available("openai/codex", time.Now().UTC()) {
		t.Fatal("expected auth failure to remain unavailable after reload")
	}
}
