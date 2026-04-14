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
	for _, complexity := range allComplexities {
		if got := cfg.ProviderModels["anthropic"][complexity]; got != "sonnet" {
			t.Fatalf("anthropic.%s = %q, want sonnet", complexity, got)
		}
		if got := cfg.ProviderModels["openai"][complexity]; got != "gpt-5.4" {
			t.Fatalf("openai.%s = %q, want gpt-5.4", complexity, got)
		}
		if got := cfg.ProviderModels["gemini"][complexity]; got != "gemini-3-flash-preview" {
			t.Fatalf("gemini.%s = %q, want gemini-3-flash-preview", complexity, got)
		}
	}
	defaultPolicy := cfg.RoleProviders[defaultRoutingRole]
	if len(defaultPolicy.Prefer) != 1 || defaultPolicy.Prefer[0] != "anthropic" {
		t.Fatalf("default prefer = %+v, want anthropic", defaultPolicy.Prefer)
	}
	if len(defaultPolicy.Fallback) != 2 || defaultPolicy.Fallback[0] != "openai" || defaultPolicy.Fallback[1] != "gemini" {
		t.Fatalf("default fallback = %+v, want openai then gemini", defaultPolicy.Fallback)
	}
	if got := cfg.Snapshots.PlanHistoryLimit; got != defaultPlanProgressHistoryLimit {
		t.Fatalf("PlanHistoryLimit = %d, want %d", got, defaultPlanProgressHistoryLimit)
	}
}

func TestLoadKitchenConfigParsesOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
providerModels:
  anthropic:
    trivial: haiku
    low: sonnet
    medium: sonnet
    high: opus
    critical: opus
  openai:
    trivial: gpt-5.4-mini
    low: gpt-5.4
    medium: gpt-5.4
    high: gpt-5.4
    critical: gpt-5.4
  gemini:
    trivial: gemini-3-flash-preview
    low: gemini-3-flash-preview
    medium: gemini-3-flash-preview
    high: gemini-3-flash-preview
    critical: gemini-3-flash-preview
roleProviders:
  default:
    prefer: [openai]
    fallback: [anthropic, gemini]
  reviewer:
    prefer: [anthropic]
    fallback: [openai]
councilSeatProviders:
  b:
    prefer: [gemini]
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
	if got := cfg.ProviderModels["anthropic"][ComplexityTrivial]; got != "haiku" {
		t.Fatalf("anthropic.trivial = %q, want haiku", got)
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
	if got := cfg.RoleProviders["reviewer"].Fallback[0]; got != "openai" {
		t.Fatalf("reviewer fallback[0] = %q, want openai", got)
	}
	if got := cfg.CouncilSeatProviders["B"].Prefer[0]; got != "gemini" {
		t.Fatalf("seat B prefer[0] = %q, want gemini", got)
	}
}

func TestLoadKitchenConfigRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
routing:
  medium:
    prefer:
      - provider: anthropic
        model: sonnet
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadKitchenConfig(path); err == nil || !strings.Contains(err.Error(), "field routing not found") {
		t.Fatalf("LoadKitchenConfig err = %v, want strict unknown field failure", err)
	}
}

func TestKitchenConfigValidateRejectsNegativeSnapshotHistoryLimit(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.Snapshots.PlanHistoryLimit = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "snapshots.planHistoryLimit") {
		t.Fatalf("Validate err = %v, want snapshot history limit failure", err)
	}
}

func TestLoadKitchenConfigPreservesDefaultMaxIdleWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`
providerModels:
  anthropic:
    trivial: sonnet
    low: sonnet
    medium: sonnet
    high: sonnet
    critical: sonnet
roleProviders:
  default:
    prefer: [anthropic]
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadKitchenConfig(path)
	if err != nil {
		t.Fatalf("LoadKitchenConfig: %v", err)
	}
	if got := cfg.Concurrency.MaxIdlePerPool; got != DefaultKitchenConfig().Concurrency.MaxIdlePerPool {
		t.Fatalf("MaxIdlePerPool = %d, want default %d when omitted", got, DefaultKitchenConfig().Concurrency.MaxIdlePerPool)
	}
}

func TestEffectiveProviderPolicyForRoleFallsBackToDefault(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders["reviewer"] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}

	reviewer := effectiveProviderPolicyForRole(cfg, "reviewer")
	if got := reviewer.Prefer[0]; got != "openai" {
		t.Fatalf("reviewer prefer[0] = %q, want openai", got)
	}
	implementer := effectiveProviderPolicyForRole(cfg, "implementer")
	if got := implementer.Prefer[0]; got != "anthropic" {
		t.Fatalf("implementer prefer[0] = %q, want anthropic", got)
	}
}

func TestSetRoleProviderPolicyAndClear(t *testing.T) {
	cfg := DefaultKitchenConfig()
	setRoleProviderPolicy(&cfg, "reviewer", ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	})
	if got := cfg.RoleProviders["reviewer"].Prefer[0]; got != "openai" {
		t.Fatalf("reviewer prefer[0] = %q, want openai", got)
	}

	clearProviderPolicyForRole(&cfg, "reviewer")
	if _, ok := cfg.RoleProviders["reviewer"]; ok {
		t.Fatalf("reviewer policy = %+v, want cleared", cfg.RoleProviders["reviewer"])
	}
}

func TestEffectiveProviderPolicyForCouncilSeatFallsBackToPlannerAndOverrides(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[plannerTaskRole] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	cfg.CouncilSeatProviders["B"] = ProviderPolicy{
		Prefer: []string{"gemini"},
	}

	seatA := effectiveProviderPolicyForCouncilSeat(cfg, "A")
	if got := seatA.Prefer[0]; got != "openai" {
		t.Fatalf("seat A prefer[0] = %q, want planner openai", got)
	}
	seatB := effectiveProviderPolicyForCouncilSeat(cfg, "B")
	if got := seatB.Prefer[0]; got != "gemini" {
		t.Fatalf("seat B prefer[0] = %q, want seat override gemini", got)
	}
	if got := seatB.Fallback[0]; got != "anthropic" {
		t.Fatalf("seat B fallback[0] = %q, want inherited anthropic", got)
	}
}

func TestKitchenConfigValidateRejectsInvalidCouncilSeatKey(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.CouncilSeatProviders["C"] = ProviderPolicy{
		Prefer: []string{"openai"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "councilSeatProviders.C") {
		t.Fatalf("Validate err = %v, want invalid council seat failure", err)
	}
}

func TestKitchenConfigValidateRejectsMissingDefaultRolePolicy(t *testing.T) {
	cfg := DefaultKitchenConfig()
	delete(cfg.RoleProviders, defaultRoutingRole)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "roleProviders.default.prefer") {
		t.Fatalf("Validate err = %v, want missing default role provider failure", err)
	}
}

func TestKitchenConfigValidateRejectsNonCanonicalProviderNames(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders["reviewer"] = ProviderPolicy{
		Prefer: []string{"codex"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "canonical provider names") {
		t.Fatalf("Validate err = %v, want canonical provider name failure", err)
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
