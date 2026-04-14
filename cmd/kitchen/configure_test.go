package main

import "testing"

func TestConfigureMenuOrdering(t *testing.T) {
	modelItems := providerModelMenuItems()
	if len(modelItems) != 4 {
		t.Fatalf("providerModelMenuItems = %+v, want 3 providers and back", modelItems)
	}
	if modelItems[len(modelItems)-1].Value != configureActionBack {
		t.Fatalf("providerModelMenuItems last = %+v, want back", modelItems[len(modelItems)-1])
	}

	roleItems := standardRoleMenuItems()
	if roleItems[0].Value != configureActionEdit || roleItems[1].Value != configureActionClear {
		t.Fatalf("standardRoleMenuItems start = %+v, want edit then clear", roleItems[:2])
	}
	if roleItems[len(roleItems)-1].Value != configureActionBack {
		t.Fatalf("standardRoleMenuItems last = %+v, want back", roleItems[len(roleItems)-1])
	}

	plannerItems := plannerMenuItems(DefaultKitchenConfig())
	if plannerItems[0].Value != configureActionEdit {
		t.Fatalf("plannerMenuItems first = %+v, want edit", plannerItems[0])
	}
	if plannerItems[1].Value != configureActionSeatA || plannerItems[2].Value != configureActionSeatB {
		t.Fatalf("plannerMenuItems seats = %+v, want seat A then seat B", plannerItems[1:3])
	}

	seatItems := councilSeatMenuItems()
	if seatItems[0].Value != configureActionEdit || seatItems[1].Value != configureActionClear {
		t.Fatalf("councilSeatMenuItems start = %+v, want edit then clear", seatItems[:2])
	}
	if seatItems[len(seatItems)-1].Value != configureActionBack {
		t.Fatalf("councilSeatMenuItems last = %+v, want back", seatItems[len(seatItems)-1])
	}
}

func TestConfigureProviderAliasesRoundTrip(t *testing.T) {
	if got := configToDisplay("anthropic"); got != "claude" {
		t.Fatalf("configToDisplay(anthropic) = %q, want claude", got)
	}
	if got := configToDisplay("openai"); got != "codex" {
		t.Fatalf("configToDisplay(openai) = %q, want codex", got)
	}
	if got := configToDisplay("google"); got != "gemini" {
		t.Fatalf("configToDisplay(google) = %q, want gemini", got)
	}
	if got := displayToConfig("codex", "openai"); got != "openai" {
		t.Fatalf("displayToConfig(codex, openai) = %q, want openai", got)
	}
	if got := displayToConfig("gemini", "google"); got != "google" {
		t.Fatalf("displayToConfig(gemini, google) = %q, want google", got)
	}
	if got := displayToConfig("codex", ""); got != "openai" {
		t.Fatalf("displayToConfig(codex, empty) = %q, want openai", got)
	}
}

func TestProviderPolicyFromEditorValuesRejectsMissingPrimary(t *testing.T) {
	values := providerPolicyEditorValues{}
	if _, err := providerPolicyFromEditorValues(values); err == nil {
		t.Fatal("providerPolicyFromEditorValues unexpectedly accepted missing primary")
	}
}

func TestApplyRoleProviderSelectionsPreserveInheritanceSparsity(t *testing.T) {
	cfg := DefaultKitchenConfig()
	inherited := inheritedRoleProviderPolicy(cfg, "reviewer")
	applyRoleProviderPolicySelection(&cfg, "reviewer", inherited)
	if _, ok := cfg.RoleProviders["reviewer"]; ok {
		t.Fatalf("reviewer policy = %+v, want inherit", cfg.RoleProviders["reviewer"])
	}

	override := ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	applyRoleProviderPolicySelection(&cfg, "reviewer", override)
	if got := cfg.RoleProviders["reviewer"]; !providerPolicyEqual(got, override) {
		t.Fatalf("reviewer policy = %+v, want %+v", got, override)
	}

	clearProviderPolicyForRole(&cfg, "reviewer")
	if _, ok := cfg.RoleProviders["reviewer"]; ok {
		t.Fatalf("reviewer policy after clear = %+v, want removed", cfg.RoleProviders["reviewer"])
	}
}

func TestApplyCouncilSeatSelectionsPreserveInheritanceSparsity(t *testing.T) {
	cfg := DefaultKitchenConfig()
	cfg.RoleProviders[plannerTaskRole] = ProviderPolicy{
		Prefer:   []string{"openai"},
		Fallback: []string{"anthropic"},
	}
	inherited := inheritedCouncilSeatProviderPolicy(cfg, "A")
	applyCouncilSeatProviderPolicySelection(&cfg, "A", inherited)
	if _, ok := cfg.CouncilSeatProviders["A"]; ok {
		t.Fatalf("seat policy = %+v, want inherit", cfg.CouncilSeatProviders["A"])
	}

	override := ProviderPolicy{
		Prefer:   []string{"gemini"},
		Fallback: []string{"anthropic"},
	}
	applyCouncilSeatProviderPolicySelection(&cfg, "A", override)
	if got := cfg.CouncilSeatProviders["A"].Prefer[0]; got != "gemini" {
		t.Fatalf("seat policy prefer[0] = %q, want gemini", got)
	}
	if len(cfg.CouncilSeatProviders["A"].Fallback) != 0 {
		t.Fatalf("seat fallback override = %+v, want pruned inherited fallback", cfg.CouncilSeatProviders["A"].Fallback)
	}

	clearCouncilSeatProviderPolicy(&cfg, "A")
	if _, ok := cfg.CouncilSeatProviders["A"]; ok {
		t.Fatalf("seat policy after clear = %+v, want removed", cfg.CouncilSeatProviders["A"])
	}
}
