package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestComplexityRouterResolveFiltersUnavailablePools(t *testing.T) {
	health, err := NewProviderHealth(filepath.Join(t.TempDir(), "provider_health.json"))
	if err != nil {
		t.Fatalf("NewProviderHealth: %v", err)
	}
	if err := health.MarkAuthFailure("anthropic/sonnet", time.Now().UTC()); err != nil {
		t.Fatalf("MarkAuthFailure: %v", err)
	}

	cfg := DefaultKitchenConfig()
	cfg.ProviderModels["anthropic"][ComplexityMedium] = "sonnet"
	cfg.ProviderModels["openai"][ComplexityMedium] = "codex"
	cfg.ProviderModels["anthropic"][ComplexityHigh] = "opus"

	router := NewComplexityRouter(cfg, health)
	got := router.Resolve(ComplexityMedium)
	if len(got) != 2 {
		t.Fatalf("len(resolve) = %d, want 2", len(got))
	}
	if got[0].Provider != "openai" || got[0].Model != "codex" {
		t.Fatalf("first pool = %+v, want openai/codex", got[0])
	}
	if got[1].Provider != "gemini" {
		t.Fatalf("second pool = %+v, want gemini fallback", got[1])
	}
}

func TestComplexityRouterResolveForRolePrefersRoleOverrideAndFallsBackToDefault(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders["reviewer"] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	router := NewComplexityRouter(cfg, nil)

	low := router.ResolveForRole("reviewer", ComplexityLow)
	if len(low) != 2 || low[0].Provider != "openai" {
		t.Fatalf("reviewer low routing = %+v, want openai then anthropic", low)
	}
	implementer := router.ResolveForRole("implementer", ComplexityHigh)
	if len(implementer) == 0 || implementer[0].Provider != "anthropic" {
		t.Fatalf("implementer routing = %+v, want inherited anthropic", implementer)
	}
}

func TestComplexityRouterResolveCouncilSeatUsesSeatRoutingWithPlannerFallback(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[plannerTaskRole] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	cfg.CouncilSeatProviders["B"] = ProviderPolicy{
		Prefer: []string{"gemini"},
	}
	router := NewComplexityRouter(cfg, nil)

	seatA := router.ResolveCouncilSeat("A", ComplexityMedium)
	if len(seatA) == 0 || seatA[0].Provider != "openai" {
		t.Fatalf("seat A routing = %+v, want planner fallback openai", seatA)
	}
	seatB := router.ResolveCouncilSeat("B", ComplexityMedium)
	if len(seatB) == 0 || seatB[0].Provider != "gemini" {
		t.Fatalf("seat B routing = %+v, want seat-specific gemini", seatB)
	}
}

func TestComplexityRouterEscalate(t *testing.T) {
	router := &ComplexityRouter{}
	next, ok := router.Escalate(ComplexityLow)
	if !ok || next != ComplexityMedium {
		t.Fatalf("Escalate(low) = %q, %v; want medium, true", next, ok)
	}
	if _, ok := router.Escalate(ComplexityCritical); ok {
		t.Fatal("Escalate(critical) should not escalate")
	}
}

func TestComplexityRouterResolveFallsBackToHostPoolWhenConfiguredPoolsMismatch(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer: []string{"anthropic"},
	}
	router := NewComplexityRouter(cfg, nil, PoolKey{Provider: "codex", Model: "gpt-5.4"})

	got := router.Resolve(ComplexityMedium)
	if len(got) != 1 {
		t.Fatalf("len(resolve) = %d, want 1", len(got))
	}
	if got[0].Provider != "codex" || got[0].Model != "gpt-5.4" {
		t.Fatalf("first pool = %+v, want codex/gpt-5.4", got[0])
	}
}

func TestComplexityRouterResolveKeepsMatchingConfiguredProviderAheadOfFallback(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer:   []string{"openai", "anthropic"},
		Fallback: nil,
	}
	router := NewComplexityRouter(cfg, nil, PoolKey{Provider: "codex", Model: "gpt-5.4"})

	got := router.Resolve(ComplexityMedium)
	if len(got) != 2 {
		t.Fatalf("len(resolve) = %d, want 2", len(got))
	}
	if got[0].Provider != "openai" || got[0].Model != "gpt-5.4" {
		t.Fatalf("first pool = %+v, want openai/gpt-5.4", got[0])
	}
}

func TestComplexityRouterResolveFiltersToConfiguredSupportedProvidersAcrossMultipleHostPools(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer: []string{"anthropic", "openai", "gemini"},
	}
	router := NewComplexityRouter(cfg, nil,
		PoolKey{Provider: "claude"},
		PoolKey{Provider: "codex"},
	)

	got := router.Resolve(ComplexityMedium)
	if len(got) != 2 {
		t.Fatalf("len(resolve) = %d, want 2", len(got))
	}
	if got[0].Provider != "anthropic" || got[1].Provider != "openai" {
		t.Fatalf("resolve = %+v, want anthropic then openai only", got)
	}
}

func TestComplexityRouterResolveDoesNotFallbackToArbitraryProviderWhenMultipleHostPoolsMismatch(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer: []string{"anthropic"},
	}
	router := NewComplexityRouter(cfg, nil,
		PoolKey{Provider: "codex"},
		PoolKey{Provider: "gemini"},
	)

	got := router.Resolve(ComplexityMedium)
	if len(got) != 0 {
		t.Fatalf("resolve = %+v, want no supported pools", got)
	}
}

func TestComplexityRouterResolveForRoleOutcomeSingleHostPoolRespectsHealthExhaustion(t *testing.T) {
	health, err := NewProviderHealth(filepath.Join(t.TempDir(), "provider_health.json"))
	if err != nil {
		t.Fatalf("NewProviderHealth: %v", err)
	}
	if err := health.SetCooldown("anthropic/sonnet", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}

	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer: []string{"anthropic"},
	}
	router := NewComplexityRouter(cfg, health, PoolKey{Provider: "claude", Model: "sonnet"})

	got := router.ResolveForRoleOutcome(defaultRoutingRole, ComplexityMedium)
	if got.Availability != RouteTemporarilyExhausted {
		t.Fatalf("availability = %q, want %q", got.Availability, RouteTemporarilyExhausted)
	}
	if len(got.Keys) != 0 {
		t.Fatalf("keys = %+v, want no eligible keys while cooldown is active", got.Keys)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Provider != "anthropic" {
		t.Fatalf("candidates = %+v, want anthropic candidate preserved", got.Candidates)
	}
}

func TestComplexityRouterResolveForRoleOutcomeMultiHostPoolRespectsHealthExhaustion(t *testing.T) {
	health, err := NewProviderHealth(filepath.Join(t.TempDir(), "provider_health.json"))
	if err != nil {
		t.Fatalf("NewProviderHealth: %v", err)
	}
	until := time.Now().UTC().Add(time.Minute)
	if err := health.SetCooldown("openai/gpt-5.4", until); err != nil {
		t.Fatalf("SetCooldown openai: %v", err)
	}
	if err := health.SetCooldown("anthropic/sonnet", until); err != nil {
		t.Fatalf("SetCooldown anthropic: %v", err)
	}

	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[defaultRoutingRole] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	router := NewComplexityRouter(cfg, health, PoolKey{Provider: "codex"}, PoolKey{Provider: "claude"})

	got := router.ResolveForRoleOutcome(defaultRoutingRole, ComplexityMedium)
	if got.Availability != RouteTemporarilyExhausted {
		t.Fatalf("availability = %q, want %q", got.Availability, RouteTemporarilyExhausted)
	}
	if len(got.Keys) != 0 {
		t.Fatalf("keys = %+v, want no eligible keys while cooldown is active", got.Keys)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want openai and anthropic candidates preserved", got.Candidates)
	}
}
