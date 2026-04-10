package main

import "testing"

func TestConfigureMenuOrdering(t *testing.T) {
	defaultItems := defaultMenuItems()
	if len(defaultItems) != len(allComplexities)+1 {
		t.Fatalf("defaultMenuItems = %+v, want complexities and back", defaultItems)
	}
	for i, complexity := range allComplexities {
		if got := defaultItems[i].Label; got != complexityMenuLabel(complexity) {
			t.Fatalf("defaultMenuItems[%d] = %q, want %q", i, got, complexityMenuLabel(complexity))
		}
	}
	if defaultItems[len(defaultItems)-1].Value != configureActionBack {
		t.Fatalf("defaultMenuItems last = %+v, want back", defaultItems[len(defaultItems)-1])
	}

	roleItems := roleMenuItems()
	if len(roleItems) < 4 {
		t.Fatalf("roleMenuItems = %+v, want defaults, complexities, and back", roleItems)
	}
	if roleItems[0].Value != configureActionInherit || roleItems[1].Value != configureActionDefault {
		t.Fatalf("roleMenuItems start = %+v, want inherit then default", roleItems[:2])
	}
	if roleItems[len(roleItems)-1].Value != configureActionBack {
		t.Fatalf("roleMenuItems last = %+v, want back", roleItems[len(roleItems)-1])
	}
	if got := roleItems[len(roleItems)-2].Label; got != "complexity: critical" {
		t.Fatalf("roleMenuItems penultimate = %q, want complexity: critical", got)
	}

	plannerItems := plannerMenuItems(DefaultKitchenConfig())
	if len(plannerItems) < 6 {
		t.Fatalf("plannerMenuItems = %+v, want defaults, seats, complexities, and back", plannerItems)
	}
	if plannerItems[0].Value != configureActionInherit || plannerItems[1].Value != configureActionDefault {
		t.Fatalf("plannerMenuItems start = %+v, want inherit then default", plannerItems[:2])
	}
	if plannerItems[2].Value != configureActionSeatA || plannerItems[3].Value != configureActionSeatB {
		t.Fatalf("plannerMenuItems seats = %+v, want seat A then seat B", plannerItems[2:4])
	}
	if plannerItems[len(plannerItems)-1].Value != configureActionBack {
		t.Fatalf("plannerMenuItems last = %+v, want back", plannerItems[len(plannerItems)-1])
	}

	seatItems := councilSeatMenuItems()
	if seatItems[0].Value != configureActionInherit || seatItems[1].Value != configureActionDefault {
		t.Fatalf("seatMenuItems start = %+v, want inherit then default", seatItems[:2])
	}
	if seatItems[len(seatItems)-1].Value != configureActionBack {
		t.Fatalf("seatMenuItems last = %+v, want back", seatItems[len(seatItems)-1])
	}
	if got := seatItems[len(seatItems)-2].Label; got != "complexity: critical" {
		t.Fatalf("seatMenuItems penultimate = %q, want complexity: critical", got)
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

func TestRoutingRuleEditorPreservesTailEntries(t *testing.T) {
	initial := RoutingRule{
		Prefer: []PoolKey{
			{Provider: "anthropic", Model: "sonnet"},
			{Provider: "anthropic", Model: "opus"},
		},
		Fallback: []PoolKey{
			{Provider: "openai", Model: "gpt-5.4"},
			{Provider: "gemini", Model: "gemini-3-flash-preview"},
			{Provider: "openai", Model: "o4-mini"},
		},
	}

	values := routeEditorValuesFromRule(initial)
	values.Primary.Model = "sonnet-updated"
	rule, err := routingRuleFromEditorValues(values)
	if err != nil {
		t.Fatalf("routingRuleFromEditorValues: %v", err)
	}
	if len(rule.Prefer) != 2 || rule.Prefer[1].Model != "opus" {
		t.Fatalf("prefer = %+v, want preserved tail entry", rule.Prefer)
	}
	if len(rule.Fallback) != 3 || rule.Fallback[2].Model != "o4-mini" {
		t.Fatalf("fallback = %+v, want preserved tail entry", rule.Fallback)
	}
	if got := rule.Prefer[0].Model; got != "sonnet-updated" {
		t.Fatalf("prefer[0].model = %q, want updated value", got)
	}
}

func TestRoutingRuleFromEditorValuesRejectsPartialFallback(t *testing.T) {
	values := routeEditorValues{
		Primary: routeSlotValues{
			DisplayProvider: "claude",
			Model:           "sonnet",
			ConfigProvider:  "anthropic",
		},
		FallbackOne: routeSlotValues{
			DisplayProvider: "codex",
			ConfigProvider:  "openai",
		},
	}
	if _, err := routingRuleFromEditorValues(values); err == nil {
		t.Fatal("routingRuleFromEditorValues unexpectedly accepted partial fallback")
	}
}

func TestApplyRoleSelectionsPreserveInheritanceSparsity(t *testing.T) {
	cfg := DefaultKitchenConfig()
	inheritedDefault := inheritedRoleDefaultRule(cfg)
	applyRoleDefaultSelection(&cfg, "reviewer", inheritedDefault)
	if _, ok := cfg.RoleDefaults["reviewer"]; ok {
		t.Fatalf("role default = %+v, want inherit", cfg.RoleDefaults["reviewer"])
	}

	override := cloneRoutingRule(inheritedDefault)
	override.Fallback = append(override.Fallback, PoolKey{Provider: "anthropic", Model: "opus"})
	applyRoleDefaultSelection(&cfg, "reviewer", override)
	if got := cfg.RoleDefaults["reviewer"]; !routingRuleEqual(got, override) {
		t.Fatalf("role default = %+v, want %+v", got, override)
	}

	inheritedComplexity := inheritedRoleComplexityRule(cfg, "reviewer", ComplexityHigh)
	applyRoleComplexitySelection(&cfg, "reviewer", ComplexityHigh, inheritedComplexity)
	if len(cfg.RoleRouting["reviewer"]) != 0 {
		t.Fatalf("role routing = %+v, want no explicit complexity override", cfg.RoleRouting["reviewer"])
	}

	complexityOverride := cloneRoutingRule(inheritedComplexity)
	complexityOverride.Prefer[0].Model = "gpt-5.4"
	complexityOverride.Prefer[0].Provider = "openai"
	applyRoleComplexitySelection(&cfg, "reviewer", ComplexityHigh, complexityOverride)
	if got := cfg.RoleRouting["reviewer"][ComplexityHigh]; !routingRuleEqual(got, complexityOverride) {
		t.Fatalf("role routing override = %+v, want %+v", got, complexityOverride)
	}

	clearRoleComplexityOverride(&cfg, "reviewer", ComplexityHigh)
	if len(cfg.RoleRouting["reviewer"]) != 0 {
		t.Fatalf("role routing after clear = %+v, want sibling-free cleanup", cfg.RoleRouting["reviewer"])
	}
}

func TestApplyCouncilSeatSelectionsPreserveInheritanceSparsity(t *testing.T) {
	cfg := DefaultKitchenConfig()
	inheritedDefault := inheritedCouncilSeatDefaultRule(cfg, "A")
	applyCouncilSeatDefaultSelection(&cfg, "A", inheritedDefault)
	if _, ok := cfg.CouncilSeats["A"]; ok {
		t.Fatalf("seat default = %+v, want inherit", cfg.CouncilSeats["A"])
	}

	seatDefault := cloneRoutingRule(inheritedDefault)
	seatDefault.Prefer[0].Model = "gpt-5.4"
	seatDefault.Prefer[0].Provider = "openai"
	applyCouncilSeatDefaultSelection(&cfg, "A", seatDefault)
	if got := cfg.CouncilSeats["A"].Default; !routingRuleEqual(got, seatDefault) {
		t.Fatalf("seat default = %+v, want %+v", got, seatDefault)
	}

	inheritedComplexity := inheritedCouncilSeatComplexityRule(cfg, "A", ComplexityLow)
	applyCouncilSeatComplexitySelection(&cfg, "A", ComplexityLow, inheritedComplexity)
	if len(cfg.CouncilSeats["A"].Routing) != 0 {
		t.Fatalf("seat routing = %+v, want no explicit complexity override", cfg.CouncilSeats["A"].Routing)
	}

	seatOverride := cloneRoutingRule(inheritedComplexity)
	seatOverride.Fallback = append(seatOverride.Fallback, PoolKey{Provider: "anthropic", Model: "opus"})
	applyCouncilSeatComplexitySelection(&cfg, "A", ComplexityLow, seatOverride)
	if got := cfg.CouncilSeats["A"].Routing[ComplexityLow]; !routingRuleEqual(got, seatOverride) {
		t.Fatalf("seat override = %+v, want %+v", got, seatOverride)
	}

	clearCouncilSeatDefault(&cfg, "A")
	if len(cfg.CouncilSeats["A"].Routing) != 1 {
		t.Fatalf("seat config after default clear = %+v, want routing override retained", cfg.CouncilSeats["A"])
	}
	clearCouncilSeatComplexityOverride(&cfg, "A", ComplexityLow)
	if _, ok := cfg.CouncilSeats["A"]; ok {
		t.Fatalf("seat config after final clear = %+v, want seat removed", cfg.CouncilSeats["A"])
	}
}
