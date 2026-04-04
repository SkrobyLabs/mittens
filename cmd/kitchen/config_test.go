package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKitchenConfigMissingReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	cfg, err := LoadKitchenConfig(path)
	if err != nil {
		t.Fatalf("LoadKitchenConfig: %v", err)
	}
	if got := cfg.Concurrency.MaxWorkersTotal; got != 12 {
		t.Fatalf("MaxWorkersTotal = %d, want 12", got)
	}
	if len(cfg.Routing[ComplexityMedium].Fallback) != 1 {
		t.Fatalf("medium fallback = %+v, want one entry", cfg.Routing[ComplexityMedium].Fallback)
	}
	if got := cfg.Snapshots.PlanHistoryLimit; got != defaultPlanProgressHistoryLimit {
		t.Fatalf("PlanHistoryLimit = %d, want %d", got, defaultPlanProgressHistoryLimit)
	}
}

func TestLoadKitchenConfigParsesOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
routing:
  trivial:
    prefer:
      - provider: openai
        model: codex-spark
  low:
    prefer:
      - provider: anthropic
        model: sonnet
  medium:
    prefer:
      - provider: anthropic
        model: sonnet
  high:
    prefer:
      - provider: anthropic
        model: opus
  critical:
    prefer:
      - provider: anthropic
        model: opus
concurrency:
  maxActiveLineages: 2
  maxPlanningWorkers: 1
  maxWorkersTotal: 3
  maxWorkersPerPool: 2
  maxWorkersPerLineage: 1
  maxIdlePerPool: 0
failure_policy:
  auth:
    action: try_next_provider
    cooldown: 30s
snapshots:
  planHistoryLimit: 5
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadKitchenConfig(path)
	if err != nil {
		t.Fatalf("LoadKitchenConfig: %v", err)
	}
	if got := cfg.Routing[ComplexityTrivial].Prefer[0].Provider; got != "openai" {
		t.Fatalf("provider = %q, want openai", got)
	}
	if got := cfg.Concurrency.MaxWorkersTotal; got != 3 {
		t.Fatalf("MaxWorkersTotal = %d, want 3", got)
	}
	if got := cfg.FailurePolicy["auth"].Cooldown; got != "30s" {
		t.Fatalf("auth cooldown = %q, want 30s", got)
	}
	if got := cfg.Snapshots.PlanHistoryLimit; got != 5 {
		t.Fatalf("PlanHistoryLimit = %d, want 5", got)
	}
}

func TestKitchenConfigValidateRejectsNegativeSnapshotHistoryLimit(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.Snapshots.PlanHistoryLimit = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "snapshots.planHistoryLimit") {
		t.Fatalf("Validate err = %v, want snapshot history limit failure", err)
	}
}

func TestKitchenPathsProjectKeyIsStable(t *testing.T) {
	paths := KitchenPaths{ProjectsDir: t.TempDir()}
	projectA, err := paths.Project("/tmp/example/repo")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := paths.Project("/tmp/example/repo")
	if err != nil {
		t.Fatal(err)
	}
	if projectA.Key != projectB.Key {
		t.Fatalf("project key mismatch: %q vs %q", projectA.Key, projectB.Key)
	}
	if projectA.PlansDir == "" || projectA.LineagesDir == "" || projectA.PoolsDir == "" {
		t.Fatalf("project paths not populated: %+v", projectA)
	}
}
