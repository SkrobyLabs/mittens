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

	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "sonnet"},
					{Provider: "openai", Model: "codex"},
				},
				Fallback: []PoolKey{
					{Provider: "anthropic", Model: "opus"},
				},
			},
		},
	}, health)

	got := router.Resolve(ComplexityMedium)
	if len(got) != 2 {
		t.Fatalf("len(resolve) = %d, want 2", len(got))
	}
	if got[0].Provider != "openai" || got[0].Model != "codex" {
		t.Fatalf("first pool = %+v, want openai/codex", got[0])
	}
	if got[1].Provider != "anthropic" || got[1].Model != "opus" {
		t.Fatalf("second pool = %+v, want anthropic/opus", got[1])
	}
}

func TestComplexityRouterResolveForRolePrefersRoleOverrideAndFallsBackToDefault(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityLow: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
			},
			ComplexityHigh: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}},
			},
		},
		RoleDefaults: map[string]RoutingRule{
			"reviewer": {
				Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
			},
		},
		RoleRouting: map[string]map[Complexity]RoutingRule{
			"reviewer": {
				ComplexityHigh: {
					Prefer: []PoolKey{{Provider: "anthropic", Model: "opus"}},
				},
			},
		},
	}, nil)

	low := router.ResolveForRole("reviewer", ComplexityLow)
	if len(low) != 1 || low[0].Provider != "openai" {
		t.Fatalf("reviewer low routing = %+v, want role default openai route", low)
	}
	high := router.ResolveForRole("reviewer", ComplexityHigh)
	if len(high) != 1 || high[0].Provider != "anthropic" {
		t.Fatalf("reviewer high routing = %+v, want anthropic complexity override", high)
	}
}

func TestComplexityRouterResolveCouncilSeatUsesSeatRoutingWithPlannerFallback(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{{Provider: "anthropic", Model: "sonnet"}},
			},
		},
		CouncilSeats: map[string]CouncilSeatRoutingConfig{
			"B": {
				Default: RoutingRule{
					Prefer: []PoolKey{{Provider: "openai", Model: "gpt-5.4"}},
				},
			},
		},
	}, nil)

	seatA := router.ResolveCouncilSeat("A", ComplexityMedium)
	if len(seatA) != 1 || seatA[0].Provider != "anthropic" {
		t.Fatalf("seat A routing = %+v, want planner fallback anthropic", seatA)
	}
	seatB := router.ResolveCouncilSeat("B", ComplexityMedium)
	if len(seatB) != 1 || seatB[0].Provider != "openai" {
		t.Fatalf("seat B routing = %+v, want seat-specific openai", seatB)
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
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "sonnet"},
				},
			},
		},
	}, nil, PoolKey{Provider: "codex", Model: "gpt-5.4"})

	got := router.Resolve(ComplexityMedium)
	if len(got) != 1 {
		t.Fatalf("len(resolve) = %d, want 1", len(got))
	}
	if got[0].Provider != "codex" || got[0].Model != "gpt-5.4" {
		t.Fatalf("first pool = %+v, want codex/gpt-5.4", got[0])
	}
}

func TestComplexityRouterResolveKeepsMatchingConfiguredProviderAheadOfFallback(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "codex", Model: "gpt-5.4"},
					{Provider: "anthropic", Model: "sonnet"},
				},
			},
		},
	}, nil, PoolKey{Provider: "codex", Model: "gpt-5.4"})

	got := router.Resolve(ComplexityMedium)
	if len(got) != 2 {
		t.Fatalf("len(resolve) = %d, want 2", len(got))
	}
	if got[0].Provider != "codex" || got[0].Model != "gpt-5.4" {
		t.Fatalf("first pool = %+v, want codex/gpt-5.4", got[0])
	}
}

func TestComplexityRouterResolveFiltersToConfiguredSupportedProvidersAcrossMultipleHostPools(t *testing.T) {
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "sonnet"},
					{Provider: "openai", Model: "gpt-5.4"},
					{Provider: "google", Model: "gemini-2.5-pro"},
				},
			},
		},
	}, nil,
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
	router := NewComplexityRouter(KitchenConfig{
		Routing: map[Complexity]RoutingRule{
			ComplexityMedium: {
				Prefer: []PoolKey{
					{Provider: "anthropic", Model: "sonnet"},
				},
			},
		},
	}, nil,
		PoolKey{Provider: "codex"},
		PoolKey{Provider: "gemini"},
	)

	got := router.Resolve(ComplexityMedium)
	if len(got) != 0 {
		t.Fatalf("resolve = %+v, want no supported pools", got)
	}
}
