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
	if len(cfg.Routing[ComplexityMedium].Fallback) != 2 {
		t.Fatalf("medium fallback = %+v, want two entries (gemini then opus)", cfg.Routing[ComplexityMedium].Fallback)
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
roleRouting:
  reviewer:
    high:
      prefer:
        - provider: openai
          model: gpt-5.4
roleDefaults:
  implementer:
    prefer:
      - provider: anthropic
        model: opus
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
	if got := cfg.RoleRouting["reviewer"][ComplexityHigh].Prefer[0].Provider; got != "openai" {
		t.Fatalf("reviewer high provider = %q, want openai", got)
	}
	if got := cfg.RoleDefaults["implementer"].Prefer[0].Model; got != "opus" {
		t.Fatalf("implementer default model = %q, want opus", got)
	}
}

func TestKitchenConfigValidateRejectsNegativeSnapshotHistoryLimit(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.Snapshots.PlanHistoryLimit = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "snapshots.planHistoryLimit") {
		t.Fatalf("Validate err = %v, want snapshot history limit failure", err)
	}
}

func TestEffectiveRoutingForRoleFallsBackToDefault(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleDefaults["reviewer"] = RoutingRule{
		Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
	}
	cfg.RoleRouting["reviewer"] = map[Complexity]RoutingRule{
		ComplexityHigh: {
			Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}},
		},
	}

	routing := effectiveRoutingForRole(cfg, "reviewer")
	if got := routing[ComplexityLow].Prefer[0].Provider; got != "openai" {
		t.Fatalf("reviewer low provider = %q, want role default openai", got)
	}
	if got := routing[ComplexityHigh].Prefer[0].Provider; got != "anthropic" {
		t.Fatalf("reviewer high provider = %q, want anthropic override", got)
	}
}

func TestSetRoleDefaultAndOverrides(t *testing.T) {
	cfg := DefaultKitchenConfig()
	setRoleDefault(&cfg, "reviewer", RoutingRule{
		Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
	})
	setRoleComplexityOverrides(&cfg, "reviewer", map[Complexity]RoutingRule{
		ComplexityHigh: {
			Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}},
		},
	})
	if got := cfg.RoleDefaults["reviewer"].Prefer[0].Provider; got != "openai" {
		t.Fatalf("reviewer default provider = %q, want openai", got)
	}
	if len(cfg.RoleRouting["reviewer"]) != 1 {
		t.Fatalf("reviewer overrides = %+v, want exactly one override", cfg.RoleRouting["reviewer"])
	}
	if got := cfg.RoleRouting["reviewer"][ComplexityHigh].Prefer[0].Provider; got != "anthropic" {
		t.Fatalf("reviewer high provider = %q, want anthropic", got)
	}

	clearRoutingForRole(&cfg, "reviewer")
	if _, ok := cfg.RoleDefaults["reviewer"]; ok {
		t.Fatalf("reviewer default = %+v, want cleared default", cfg.RoleDefaults["reviewer"])
	}
	if _, ok := cfg.RoleRouting["reviewer"]; ok {
		t.Fatalf("reviewer overrides = %+v, want cleared overrides", cfg.RoleRouting["reviewer"])
	}
}

func TestEffectiveRoutingForCouncilSeatFallsBackToPlannerAndOverrides(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleDefaults[plannerTaskRole] = RoutingRule{
		Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
	}
	cfg.CouncilSeats["B"] = CouncilSeatRoutingConfig{
		Default: RoutingRule{
			Prefer: []PoolKey{{Provider: "google", Model: "gemini-2.5-pro"}},
		},
		Routing: map[Complexity]RoutingRule{
			ComplexityTrivial: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "haiku"}},
			},
		},
	}

	seatA := effectiveRoutingForCouncilSeat(cfg, "A")
	if got := seatA[ComplexityMedium].Prefer[0].Provider; got != "openai" {
		t.Fatalf("seat A provider = %q, want planner fallback openai", got)
	}
	seatB := effectiveRoutingForCouncilSeat(cfg, "B")
	if got := seatB[ComplexityMedium].Prefer[0].Provider; got != "google" {
		t.Fatalf("seat B medium provider = %q, want seat default google", got)
	}
	if got := seatB[ComplexityTrivial].Prefer[0].Provider; got != "anthropic" {
		t.Fatalf("seat B trivial provider = %q, want anthropic override", got)
	}
}

func TestKitchenConfigValidateRejectsInvalidCouncilSeatKey(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.CouncilSeats["C"] = CouncilSeatRoutingConfig{
		Default: RoutingRule{
			Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "councilSeats.C") {
		t.Fatalf("Validate err = %v, want invalid council seat failure", err)
	}
}

func TestKitchenConfigValidateRejectsDefaultRoleRouting(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleRouting[defaultRoutingRole] = map[Complexity]RoutingRule{
		ComplexityLow: {
			Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "roleRouting.default") {
		t.Fatalf("Validate err = %v, want reserved default role routing failure", err)
	}
}

func TestKitchenConfigValidateRejectsDefaultRoleDefault(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleDefaults[defaultRoutingRole] = RoutingRule{
		Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "roleDefaults.default") {
		t.Fatalf("Validate err = %v, want reserved default role default failure", err)
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
