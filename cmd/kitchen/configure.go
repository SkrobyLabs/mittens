package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
)

// complexityDisplayName returns a human-friendly label for a complexity level.
func complexityDisplayName(c Complexity) string {
	switch c {
	case ComplexityTrivial:
		return "Trivial"
	case ComplexityLow:
		return "Low"
	case ComplexityMedium:
		return "Medium"
	case ComplexityHigh:
		return "High"
	case ComplexityCritical:
		return "Critical"
	default:
		return string(c)
	}
}

func providerOptions(includeNone bool) []huh.Option[string] {
	var opts []huh.Option[string]
	if includeNone {
		opts = append(opts, huh.NewOption[string]("(none)", ""))
	}
	opts = append(opts,
		huh.NewOption[string]("claude", "claude"),
		huh.NewOption[string]("codex", "codex"),
		huh.NewOption[string]("gemini", "gemini"),
	)
	return opts
}

func configToDisplay(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "claude"
	case "openai", "codex":
		return "codex"
	case "google", "gemini":
		return "gemini"
	default:
		return provider
	}
}

func displayToConfig(provider, current string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	if current != "" && configToDisplay(current) == provider {
		return current
	}
	switch provider {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "gemini":
		return "gemini"
	default:
		return provider
	}
}

type configureMenuItem struct {
	Label string
	Value string
}

type policySlotValues struct {
	DisplayProvider string
	ConfigProvider  string
}

type providerPolicyEditorValues struct {
	Primary     policySlotValues
	FallbackOne policySlotValues
	FallbackTwo policySlotValues
}

const (
	configureSaveAndExit   = "__save_and_exit__"
	configureActionBack    = "back"
	configureActionModels  = "models"
	configureActionWorkers = "workers"
	configureActionEdit    = "edit"
	configureActionClear   = "clear"
	configureActionSeatA   = "seat:A"
	configureActionSeatB   = "seat:B"
)

func complexityMenuValue(c Complexity) string {
	return "complexity:" + string(c)
}

func parseComplexityMenuValue(v string) (Complexity, bool) {
	if !strings.HasPrefix(v, "complexity:") {
		return "", false
	}
	c := Complexity(strings.TrimPrefix(v, "complexity:"))
	if !isValidComplexity(c) {
		return "", false
	}
	return c, true
}

func complexityMenuLabel(c Complexity) string {
	return "complexity: " + string(c)
}

func configureMenuOptions(items []configureMenuItem) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(items))
	for _, item := range items {
		options = append(options, huh.NewOption[string](item.Label, item.Value))
	}
	return options
}

func providerModelMenuItems() []configureMenuItem {
	items := []configureMenuItem{
		{Label: "claude", Value: "anthropic"},
		{Label: "codex", Value: "openai"},
		{Label: "gemini", Value: "gemini"},
		{Label: "back", Value: configureActionBack},
	}
	return items
}

func modelComplexityMenuItems() []configureMenuItem {
	items := make([]configureMenuItem, 0, len(allComplexities)+1)
	for _, complexity := range allComplexities {
		items = append(items, configureMenuItem{
			Label: complexityMenuLabel(complexity),
			Value: complexityMenuValue(complexity),
		})
	}
	items = append(items, configureMenuItem{Label: "back", Value: configureActionBack})
	return items
}

func standardRoleMenuItems() []configureMenuItem {
	return []configureMenuItem{
		{Label: "edit provider policy", Value: configureActionEdit},
		{Label: "use default provider policy", Value: configureActionClear},
		{Label: "back", Value: configureActionBack},
	}
}

func plannerMenuItems(cfg KitchenConfig) []configureMenuItem {
	return []configureMenuItem{
		{Label: "edit planner provider policy", Value: configureActionEdit},
		{Label: councilSeatMenuLabel(cfg, "A"), Value: configureActionSeatA},
		{Label: councilSeatMenuLabel(cfg, "B"), Value: configureActionSeatB},
		{Label: "use default provider policy", Value: configureActionClear},
		{Label: "back", Value: configureActionBack},
	}
}

func councilSeatMenuItems() []configureMenuItem {
	return []configureMenuItem{
		{Label: "edit seat provider policy", Value: configureActionEdit},
		{Label: "use planner provider policy", Value: configureActionClear},
		{Label: "back", Value: configureActionBack},
	}
}

func policySlotValuesFromProvider(provider string) policySlotValues {
	return policySlotValues{
		DisplayProvider: configToDisplay(provider),
		ConfigProvider:  provider,
	}
}

func providerPolicyEditorValuesFromPolicy(policy ProviderPolicy) providerPolicyEditorValues {
	values := providerPolicyEditorValues{}
	if len(policy.Prefer) > 0 {
		values.Primary = policySlotValuesFromProvider(policy.Prefer[0])
	}
	if len(policy.Fallback) > 0 {
		values.FallbackOne = policySlotValuesFromProvider(policy.Fallback[0])
	}
	if len(policy.Fallback) > 1 {
		values.FallbackTwo = policySlotValuesFromProvider(policy.Fallback[1])
	}
	return values
}

func validatePolicyEditorValues(values providerPolicyEditorValues) error {
	if strings.TrimSpace(values.Primary.DisplayProvider) == "" {
		return fmt.Errorf("primary provider is required")
	}
	for _, slot := range []policySlotValues{values.FallbackOne, values.FallbackTwo} {
		if slot.DisplayProvider == "" {
			continue
		}
	}
	return nil
}

func providerPolicyFromEditorValues(values providerPolicyEditorValues) (ProviderPolicy, error) {
	if err := validatePolicyEditorValues(values); err != nil {
		return ProviderPolicy{}, err
	}
	policy := ProviderPolicy{
		Prefer: []string{displayToConfig(values.Primary.DisplayProvider, values.Primary.ConfigProvider)},
	}
	if provider := strings.TrimSpace(values.FallbackOne.DisplayProvider); provider != "" {
		policy.Fallback = append(policy.Fallback, displayToConfig(provider, values.FallbackOne.ConfigProvider))
	}
	if provider := strings.TrimSpace(values.FallbackTwo.DisplayProvider); provider != "" {
		policy.Fallback = append(policy.Fallback, displayToConfig(provider, values.FallbackTwo.ConfigProvider))
	}
	canonical, err := canonicalizeProviderPolicy(policy)
	if err != nil {
		return ProviderPolicy{}, err
	}
	if len(canonical.Prefer) == 0 {
		return ProviderPolicy{}, fmt.Errorf("primary provider is required")
	}
	return canonical, nil
}

func providerPolicySummary(policy ProviderPolicy) string {
	parts := make([]string, 0, len(policy.Prefer)+len(policy.Fallback))
	for _, provider := range policy.Prefer {
		parts = append(parts, configToDisplay(provider))
	}
	for _, provider := range policy.Fallback {
		parts = append(parts, configToDisplay(provider))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " -> ")
}

func providerModelSummary(cfg KitchenConfig, provider string) string {
	models := cfg.ProviderModels[provider]
	parts := make([]string, 0, len(allComplexities))
	for _, complexity := range allComplexities {
		model := strings.TrimSpace(models[complexity])
		if model == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", complexity, model))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

func editProviderPolicyForm(title string, initial ProviderPolicy) (ProviderPolicy, error) {
	values := providerPolicyEditorValuesFromPolicy(initial)
	for {
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(title+" — Primary provider").
					Options(providerOptions(false)...).
					Value(&values.Primary.DisplayProvider),
				huh.NewSelect[string]().
					Title(title+" — Fallback 1 provider").
					Options(providerOptions(true)...).
					Value(&values.FallbackOne.DisplayProvider),
				huh.NewSelect[string]().
					Title(title+" — Fallback 2 provider").
					Options(providerOptions(true)...).
					Value(&values.FallbackTwo.DisplayProvider),
			),
		).WithShowHelp(true).Run(); err != nil {
			return ProviderPolicy{}, err
		}
		policy, err := providerPolicyFromEditorValues(values)
		if err == nil {
			return policy, nil
		}
		fmt.Fprintln(os.Stderr, err)
	}
}

func inheritedRoleProviderPolicy(cfg KitchenConfig, role string) ProviderPolicy {
	if role == defaultRoutingRole {
		return cloneProviderPolicy(cfg.RoleProviders[defaultRoutingRole])
	}
	return effectiveProviderPolicyForRole(cfg, defaultRoutingRole)
}

func inheritedCouncilSeatProviderPolicy(cfg KitchenConfig, seat string) ProviderPolicy {
	return effectiveProviderPolicyForRole(cfg, plannerTaskRole)
}

func applyRoleProviderPolicySelection(cfg *KitchenConfig, role string, policy ProviderPolicy) {
	role = normalizeRoutingRole(role)
	if role != defaultRoutingRole && providerPolicyEqual(policy, inheritedRoleProviderPolicy(*cfg, role)) {
		clearProviderPolicyForRole(cfg, role)
		return
	}
	setRoleProviderPolicy(cfg, role, policy)
}

func applyCouncilSeatProviderPolicySelection(cfg *KitchenConfig, seat string, policy ProviderPolicy) {
	inherited := inheritedCouncilSeatProviderPolicy(*cfg, seat)
	overlay := cloneProviderPolicy(policy)
	if slicesEqual(overlay.Prefer, inherited.Prefer) {
		overlay.Prefer = nil
	}
	if slicesEqual(overlay.Fallback, inherited.Fallback) {
		overlay.Fallback = nil
	}
	setCouncilSeatProviderPolicy(cfg, seat, overlay)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func runConfigure() error {
	paths, err := DefaultKitchenPaths()
	if err != nil {
		return err
	}
	cfg, err := LoadKitchenConfig(paths.ConfigPath)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Configure Kitchen routing in two layers: shared complexity models first, then per-role provider preference policy.")
	fmt.Fprintln(os.Stderr)

	for {
		selection := configureActionModels
		if err := huh.NewSelect[string]().
			Title("Choose section").
			Options(configureMenuOptions(routingRoleOptions(cfg))...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		if selection == configureSaveAndExit {
			break
		}
		if selection == configureActionModels {
			if err := runConfigureModels(&cfg); err != nil {
				return err
			}
			continue
		}
		if selection == configureActionWorkers {
			if err := runConfigureWorkers(&cfg); err != nil {
				return err
			}
			continue
		}
		if err := runConfigureRole(&cfg, selection); err != nil {
			return err
		}
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := SaveKitchenConfigFile(paths.ConfigPath, &cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Configuration saved to %s\n", paths.ConfigPath)
	fmt.Fprintln(os.Stdout, "Routing changes take effect the next time `kitchen serve` starts.")
	return nil
}

func runConfigureModels(cfg *KitchenConfig) error {
	for {
		selection := "anthropic"
		if err := huh.NewSelect[string]().
			Title("shared models").
			Description("Configure which model each provider uses for each complexity. Roles only choose providers, not models.").
			Options(configureMenuOptions(providerModelMenuItems())...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		if selection == configureActionBack {
			return nil
		}
		if err := runConfigureProviderModels(cfg, selection); err != nil {
			return err
		}
	}
}

func runConfigureWorkers(cfg *KitchenConfig) error {
	value := strconv.Itoa(cfg.Concurrency.MaxWorkersTotal)
	for {
		if err := huh.NewInput().
			Title("Maximum workers").
			Description("Set the total worker cap Kitchen may run at once across all lineages and roles.").
			Value(&value).
			Run(); err != nil {
			return err
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed <= 0 {
			fmt.Fprintln(os.Stderr, "maximum workers must be a positive integer")
			continue
		}
		cfg.Concurrency.MaxWorkersTotal = parsed
		return nil
	}
}

func runConfigureProviderModels(cfg *KitchenConfig, provider string) error {
	for {
		selection := complexityMenuValue(ComplexityMedium)
		if err := huh.NewSelect[string]().
			Title(configToDisplay(provider) + " models").
			Description(providerModelSummary(*cfg, provider)).
			Options(configureMenuOptions(modelComplexityMenuItems())...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		if selection == configureActionBack {
			return nil
		}
		complexity, ok := parseComplexityMenuValue(selection)
		if !ok {
			continue
		}
		model := strings.TrimSpace(cfg.ProviderModels[provider][complexity])
		if err := huh.NewInput().
			Title(fmt.Sprintf("%s — %s model", configToDisplay(provider), complexityDisplayName(complexity))).
			Value(&model).
			Run(); err != nil {
			return err
		}
		if err := setProviderModel(cfg, provider, complexity, model); err != nil {
			return err
		}
	}
}

func runConfigureRole(cfg *KitchenConfig, role string) error {
	role = normalizeRoutingRole(role)
	switch role {
	case plannerTaskRole:
		return runConfigurePlanner(cfg)
	default:
		return runConfigureStandardRole(cfg, role)
	}
}

func runConfigureStandardRole(cfg *KitchenConfig, role string) error {
	for {
		selection := configureActionEdit
		if err := huh.NewSelect[string]().
			Title(routingRoleDisplayName(role)).
			Description(routingRoleStatus(cfg, role)).
			Options(configureMenuOptions(standardRoleMenuItems())...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case configureActionBack:
			return nil
		case configureActionClear:
			clearProviderPolicyForRole(cfg, role)
		case configureActionEdit:
			current, ok := roleProviderPolicy(*cfg, role)
			if !ok {
				current = inheritedRoleProviderPolicy(*cfg, role)
			}
			policy, err := editProviderPolicyForm("provider policy", current)
			if err != nil {
				return err
			}
			applyRoleProviderPolicySelection(cfg, role, policy)
		}
	}
}

func runConfigurePlanner(cfg *KitchenConfig) error {
	for {
		selection := configureActionEdit
		if err := huh.NewSelect[string]().
			Title("planner").
			Description("Planner provider policy is the baseline for both council seats. Seats only store field-level overrides on top of planner.").
			Options(configureMenuOptions(plannerMenuItems(*cfg))...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case configureActionBack:
			return nil
		case configureActionClear:
			clearProviderPolicyForRole(cfg, plannerTaskRole)
		case configureActionEdit:
			current, ok := roleProviderPolicy(*cfg, plannerTaskRole)
			if !ok {
				current = inheritedRoleProviderPolicy(*cfg, plannerTaskRole)
			}
			policy, err := editProviderPolicyForm("planner provider policy", current)
			if err != nil {
				return err
			}
			applyRoleProviderPolicySelection(cfg, plannerTaskRole, policy)
		case configureActionSeatA:
			if err := runConfigureCouncilSeat(cfg, "A"); err != nil {
				return err
			}
		case configureActionSeatB:
			if err := runConfigureCouncilSeat(cfg, "B"); err != nil {
				return err
			}
		}
	}
}

func runConfigureCouncilSeat(cfg *KitchenConfig, seat string) error {
	seat = normalizeCouncilSeat(seat)
	if seat == "" {
		return nil
	}
	for {
		selection := configureActionEdit
		if err := huh.NewSelect[string]().
			Title("Council seat " + seat).
			Description(councilSeatStatus(*cfg, seat)).
			Options(configureMenuOptions(councilSeatMenuItems())...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case configureActionBack:
			return nil
		case configureActionClear:
			clearCouncilSeatProviderPolicy(cfg, seat)
		case configureActionEdit:
			current := effectiveProviderPolicyForCouncilSeat(*cfg, seat)
			policy, err := editProviderPolicyForm("seat "+seat+" provider policy", current)
			if err != nil {
				return err
			}
			applyCouncilSeatProviderPolicySelection(cfg, seat, policy)
		}
	}
}

func routingRoleOptions(cfg KitchenConfig) []configureMenuItem {
	items := []configureMenuItem{
		{Label: "shared models", Value: configureActionModels},
		{Label: workerLimitMenuLabel(cfg), Value: configureActionWorkers},
	}
	for _, role := range configurableKitchenRoles() {
		items = append(items, configureMenuItem{
			Label: routingRoleMenuLabel(cfg, role),
			Value: role,
		})
	}
	items = append(items, configureMenuItem{Label: "Save and exit", Value: configureSaveAndExit})
	return items
}

func workerLimitMenuLabel(cfg KitchenConfig) string {
	return fmt.Sprintf("worker limits (max=%d)", cfg.Concurrency.MaxWorkersTotal)
}

func routingRoleMenuLabel(cfg KitchenConfig, role string) string {
	label := routingRoleDisplayName(role)
	switch {
	case role == defaultRoutingRole:
		return label + " (shared provider baseline)"
	case roleHasProviderOverride(cfg, role):
		return label + " (custom)"
	default:
		return label + " (inherits default)"
	}
}

func routingRoleDisplayName(role string) string {
	switch normalizeRoutingRole(role) {
	case defaultRoutingRole:
		return "default provider policy"
	case plannerTaskRole:
		return "planner"
	case "implementer":
		return "implementer"
	case "reviewer":
		return "reviewer"
	case lineageFixMergeRole:
		return "lineage-fix-merge"
	case researcherTaskRole:
		return "researcher"
	default:
		return normalizeRoutingRole(role)
	}
}

func routingRoleStatus(cfg *KitchenConfig, role string) string {
	if cfg == nil {
		return ""
	}
	role = normalizeRoutingRole(role)
	if role == defaultRoutingRole {
		return "This provider policy is the shared baseline for all roles."
	}
	if roleHasProviderOverride(*cfg, role) {
		return "This role currently has a custom provider policy."
	}
	return "This role currently inherits the shared default provider policy."
}

func councilSeatMenuLabel(cfg KitchenConfig, seat string) string {
	seat = normalizeCouncilSeat(seat)
	if councilSeatHasProviderOverride(cfg, seat) {
		return "Seat " + seat + " (custom)"
	}
	return "Seat " + seat + " (inherits planner)"
}

func councilSeatStatus(cfg KitchenConfig, seat string) string {
	seat = normalizeCouncilSeat(seat)
	if councilSeatHasProviderOverride(cfg, seat) {
		return "This seat currently overrides part of the planner provider policy."
	}
	return "This seat currently inherits the planner provider policy."
}
