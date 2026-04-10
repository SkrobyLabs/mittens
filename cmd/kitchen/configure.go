package main

import (
	"fmt"
	"os"
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

// providerOptions returns huh select options for providers.
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

// configToDisplay maps config provider names to TUI display names.
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

func displayToCanonicalConfig(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
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

func displayToConfig(provider, current string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	current = strings.TrimSpace(current)
	if current != "" && configToDisplay(current) == provider {
		return current
	}
	return displayToCanonicalConfig(provider)
}

type configureMenuItem struct {
	Label string
	Value string
}

type routeSlotValues struct {
	DisplayProvider string
	Model           string
	ConfigProvider  string
}

type routeEditorValues struct {
	Primary      routeSlotValues
	FallbackOne  routeSlotValues
	FallbackTwo  routeSlotValues
	preferTail   []PoolKey
	fallbackTail []PoolKey
}

type routingTargetAction string

const (
	configureSaveAndExit                         = "__save_and_exit__"
	configureActionBack                          = "back"
	configureActionInherit                       = "inherit"
	configureActionDefault                       = "default"
	configureActionEdit                          = "edit"
	configureActionSeatA                         = "seat:A"
	configureActionSeatB                         = "seat:B"
	routingTargetActionSave  routingTargetAction = "save"
	routingTargetActionClear routingTargetAction = "clear"
	routingTargetActionBack  routingTargetAction = "back"
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

func defaultMenuItems() []configureMenuItem {
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

func roleMenuItems() []configureMenuItem {
	items := []configureMenuItem{
		{Label: "use default routing", Value: configureActionInherit},
		{Label: "role default", Value: configureActionDefault},
	}
	for _, complexity := range allComplexities {
		items = append(items, configureMenuItem{
			Label: complexityMenuLabel(complexity),
			Value: complexityMenuValue(complexity),
		})
	}
	items = append(items, configureMenuItem{Label: "back", Value: configureActionBack})
	return items
}

func plannerMenuItems(cfg KitchenConfig) []configureMenuItem {
	items := []configureMenuItem{
		{Label: "use default routing", Value: configureActionInherit},
		{Label: "role default", Value: configureActionDefault},
		{Label: councilSeatMenuLabel(cfg, "A"), Value: configureActionSeatA},
		{Label: councilSeatMenuLabel(cfg, "B"), Value: configureActionSeatB},
	}
	for _, complexity := range allComplexities {
		items = append(items, configureMenuItem{
			Label: complexityMenuLabel(complexity),
			Value: complexityMenuValue(complexity),
		})
	}
	items = append(items, configureMenuItem{Label: "back", Value: configureActionBack})
	return items
}

func councilSeatMenuItems() []configureMenuItem {
	items := []configureMenuItem{
		{Label: "use planner routing", Value: configureActionInherit},
		{Label: "seat default", Value: configureActionDefault},
	}
	for _, complexity := range allComplexities {
		items = append(items, configureMenuItem{
			Label: complexityMenuLabel(complexity),
			Value: complexityMenuValue(complexity),
		})
	}
	items = append(items, configureMenuItem{Label: "back", Value: configureActionBack})
	return items
}

func routeSlotValuesFromKey(key PoolKey) routeSlotValues {
	return routeSlotValues{
		DisplayProvider: configToDisplay(key.Provider),
		Model:           key.Model,
		ConfigProvider:  key.Provider,
	}
}

func routeEditorValuesFromRule(rule RoutingRule) routeEditorValues {
	values := routeEditorValues{}
	if len(rule.Prefer) > 0 {
		values.Primary = routeSlotValuesFromKey(rule.Prefer[0])
	}
	if len(rule.Prefer) > 1 {
		values.preferTail = append([]PoolKey(nil), rule.Prefer[1:]...)
	}
	if len(rule.Fallback) > 0 {
		values.FallbackOne = routeSlotValuesFromKey(rule.Fallback[0])
	}
	if len(rule.Fallback) > 1 {
		values.FallbackTwo = routeSlotValuesFromKey(rule.Fallback[1])
	}
	if len(rule.Fallback) > 2 {
		values.fallbackTail = append([]PoolKey(nil), rule.Fallback[2:]...)
	}
	return values
}

func validateRouteEditorSlot(name string, slot routeSlotValues, required bool) error {
	provider := strings.TrimSpace(slot.DisplayProvider)
	model := strings.TrimSpace(slot.Model)
	if required {
		if provider == "" || model == "" {
			return fmt.Errorf("%s provider and model are required", name)
		}
		return nil
	}
	if (provider == "") != (model == "") {
		return fmt.Errorf("%s provider and model must both be set or both be empty", name)
	}
	return nil
}

func routingRuleFromEditorValues(values routeEditorValues) (RoutingRule, error) {
	if err := validateRouteEditorSlot("primary route", values.Primary, true); err != nil {
		return RoutingRule{}, err
	}
	if err := validateRouteEditorSlot("fallback 1", values.FallbackOne, false); err != nil {
		return RoutingRule{}, err
	}
	if err := validateRouteEditorSlot("fallback 2", values.FallbackTwo, false); err != nil {
		return RoutingRule{}, err
	}

	rule := RoutingRule{
		Prefer: []PoolKey{{
			Provider: displayToConfig(values.Primary.DisplayProvider, values.Primary.ConfigProvider),
			Model:    strings.TrimSpace(values.Primary.Model),
		}},
	}
	if provider := strings.TrimSpace(values.FallbackOne.DisplayProvider); provider != "" {
		rule.Fallback = append(rule.Fallback, PoolKey{
			Provider: displayToConfig(provider, values.FallbackOne.ConfigProvider),
			Model:    strings.TrimSpace(values.FallbackOne.Model),
		})
	}
	if provider := strings.TrimSpace(values.FallbackTwo.DisplayProvider); provider != "" {
		rule.Fallback = append(rule.Fallback, PoolKey{
			Provider: displayToConfig(provider, values.FallbackTwo.ConfigProvider),
			Model:    strings.TrimSpace(values.FallbackTwo.Model),
		})
	}
	if len(values.preferTail) > 0 {
		rule.Prefer = append(rule.Prefer, values.preferTail...)
	}
	if len(values.fallbackTail) > 0 {
		rule.Fallback = append(rule.Fallback, values.fallbackTail...)
	}
	return rule, nil
}

func routingRuleSummary(rule RoutingRule) string {
	var parts []string
	for _, key := range rule.Prefer {
		parts = append(parts, fmt.Sprintf("%s/%s", configToDisplay(key.Provider), key.Model))
	}
	for _, key := range rule.Fallback {
		parts = append(parts, fmt.Sprintf("%s/%s", configToDisplay(key.Provider), key.Model))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " -> ")
}

func inheritedRouteDescription(prefix string, rule RoutingRule) string {
	return prefix + ": " + routingRuleSummary(rule)
}

func editRoutingRuleForm(title string, initial RoutingRule, allowNone bool) (RoutingRule, error) {
	values := routeEditorValuesFromRule(initial)
	for {
		if err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(title+" — Primary provider").
					Options(providerOptions(false)...).
					Value(&values.Primary.DisplayProvider),
				huh.NewInput().
					Title(title+" — Primary model").
					Value(&values.Primary.Model),
				huh.NewSelect[string]().
					Title(title+" — Fallback 1 provider").
					Options(providerOptions(allowNone)...).
					Value(&values.FallbackOne.DisplayProvider),
				huh.NewInput().
					Title(title+" — Fallback 1 model").
					Value(&values.FallbackOne.Model),
				huh.NewSelect[string]().
					Title(title+" — Fallback 2 provider").
					Options(providerOptions(allowNone)...).
					Value(&values.FallbackTwo.DisplayProvider),
				huh.NewInput().
					Title(title+" — Fallback 2 model").
					Value(&values.FallbackTwo.Model),
			),
		).WithShowHelp(true).Run(); err != nil {
			return RoutingRule{}, err
		}
		rule, err := routingRuleFromEditorValues(values)
		if err == nil {
			return rule, nil
		}
		fmt.Fprintln(os.Stderr, err)
	}
}

func inheritedRoleDefaultRule(cfg KitchenConfig) RoutingRule {
	return cloneRoutingRule(cfg.Routing[ComplexityMedium])
}

func inheritedRoleComplexityRule(cfg KitchenConfig, role string, complexity Complexity) RoutingRule {
	if rule, ok := roleDefaultRule(cfg, role); ok {
		return cloneRoutingRule(rule)
	}
	return cloneRoutingRule(cfg.Routing[complexity])
}

func inheritedCouncilSeatDefaultRule(cfg KitchenConfig, seat string) RoutingRule {
	return cloneRoutingRule(effectiveRoutingForRole(cfg, plannerTaskRole)[ComplexityMedium])
}

func inheritedCouncilSeatComplexityRule(cfg KitchenConfig, seat string, complexity Complexity) RoutingRule {
	seatCfg, ok := councilSeatRoutingConfig(cfg, seat)
	if ok && len(seatCfg.Default.Prefer) > 0 {
		return cloneRoutingRule(seatCfg.Default)
	}
	return cloneRoutingRule(effectiveRoutingForRole(cfg, plannerTaskRole)[complexity])
}

func applyRoleDefaultSelection(cfg *KitchenConfig, role string, rule RoutingRule) {
	if routingRuleEqual(rule, inheritedRoleDefaultRule(*cfg)) {
		setRoleDefault(cfg, role, RoutingRule{})
		return
	}
	setRoleDefault(cfg, role, rule)
}

func applyRoleComplexitySelection(cfg *KitchenConfig, role string, complexity Complexity, rule RoutingRule) {
	if routingRuleEqual(rule, inheritedRoleComplexityRule(*cfg, role, complexity)) {
		clearRoleComplexityOverride(cfg, role, complexity)
		return
	}
	setRoleComplexityOverride(cfg, role, complexity, rule)
}

func applyCouncilSeatDefaultSelection(cfg *KitchenConfig, seat string, rule RoutingRule) {
	if routingRuleEqual(rule, inheritedCouncilSeatDefaultRule(*cfg, seat)) {
		clearCouncilSeatDefault(cfg, seat)
		return
	}
	setCouncilSeatDefault(cfg, seat, rule)
}

func applyCouncilSeatComplexitySelection(cfg *KitchenConfig, seat string, complexity Complexity, rule RoutingRule) {
	if routingRuleEqual(rule, inheritedCouncilSeatComplexityRule(*cfg, seat, complexity)) {
		clearCouncilSeatComplexityOverride(cfg, seat, complexity)
		return
	}
	setCouncilSeatComplexityOverride(cfg, seat, complexity, rule)
}

func runRoutingTargetMenu(title, description string, current RoutingRule, clearLabel string, allowClear bool) (routingTargetAction, RoutingRule, error) {
	action := configureActionEdit
	options := []huh.Option[string]{
		huh.NewOption[string]("edit route", configureActionEdit),
	}
	if allowClear {
		options = append(options, huh.NewOption[string](clearLabel, string(routingTargetActionClear)))
	}
	options = append(options, huh.NewOption[string]("back", configureActionBack))
	if err := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(options...).
		Value(&action).
		Run(); err != nil {
		return routingTargetActionBack, RoutingRule{}, err
	}
	switch action {
	case configureActionBack:
		return routingTargetActionBack, RoutingRule{}, nil
	case string(routingTargetActionClear):
		return routingTargetActionClear, RoutingRule{}, nil
	}
	rule, err := editRoutingRuleForm(title, current, true)
	if err != nil {
		return routingTargetActionBack, RoutingRule{}, err
	}
	return routingTargetActionSave, rule, nil
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

	fmt.Fprintln(os.Stderr, "Select a role to configure. `default` defines the shared per-complexity routing; other roles only store the differences from that default.")
	fmt.Fprintln(os.Stderr)

	for {
		selection := defaultRoutingRole
		if err := huh.NewSelect[string]().
			Title("Choose role").
			Options(routingRoleOptions(cfg)...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		if selection == configureSaveAndExit {
			break
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

func runConfigureRole(cfg *KitchenConfig, role string) error {
	role = normalizeRoutingRole(role)
	switch role {
	case defaultRoutingRole:
		return runConfigureDefault(cfg)
	case plannerTaskRole:
		return runConfigurePlanner(cfg)
	default:
		return runConfigureStandardRole(cfg, role)
	}
}

func runConfigureDefault(cfg *KitchenConfig) error {
	for {
		selection := complexityMenuValue(ComplexityMedium)
		if err := huh.NewSelect[string]().
			Title("default").
			Description("Shared per-complexity routing used as the baseline for all non-default roles.").
			Options(configureMenuOptions(defaultMenuItems())...).
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
		current := cloneRoutingRule(cfg.Routing[complexity])
		action, rule, err := runRoutingTargetMenu(
			complexityMenuLabel(complexity),
			inheritedRouteDescription("Shared default route", current),
			current,
			"",
			false,
		)
		if err != nil {
			return err
		}
		if action == routingTargetActionSave {
			setRoutingComplexity(cfg, complexity, rule)
		}
	}
}

func runConfigureStandardRole(cfg *KitchenConfig, role string) error {
	for {
		selection := configureActionDefault
		if err := huh.NewSelect[string]().
			Title(routingRoleDisplayName(role)).
			Description(routingRoleStatus(cfg, role)).
			Options(configureMenuOptions(roleMenuItems())...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case configureActionBack:
			return nil
		case configureActionInherit:
			clearRoutingForRole(cfg, role)
		case configureActionDefault:
			if err := runConfigureRoleDefaultTarget(cfg, role); err != nil {
				return err
			}
		default:
			complexity, ok := parseComplexityMenuValue(selection)
			if !ok {
				continue
			}
			if err := runConfigureRoleComplexityTarget(cfg, role, complexity); err != nil {
				return err
			}
		}
	}
}

func runConfigurePlanner(cfg *KitchenConfig) error {
	for {
		selection := configureActionDefault
		if err := huh.NewSelect[string]().
			Title("planner").
			Description("Planner routing is the shared baseline for both council seats. Seat routing stores only the differences from planner.").
			Options(configureMenuOptions(plannerMenuItems(*cfg))...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case configureActionBack:
			return nil
		case configureActionInherit:
			clearRoutingForRole(cfg, plannerTaskRole)
		case configureActionDefault:
			if err := runConfigureRoleDefaultTarget(cfg, plannerTaskRole); err != nil {
				return err
			}
		case configureActionSeatA:
			if err := runConfigureCouncilSeat(cfg, "A"); err != nil {
				return err
			}
		case configureActionSeatB:
			if err := runConfigureCouncilSeat(cfg, "B"); err != nil {
				return err
			}
		default:
			complexity, ok := parseComplexityMenuValue(selection)
			if !ok {
				continue
			}
			if err := runConfigureRoleComplexityTarget(cfg, plannerTaskRole, complexity); err != nil {
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
		selection := configureActionDefault
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
		case configureActionInherit:
			clearCouncilSeatRoutingConfig(cfg, seat)
		case configureActionDefault:
			if err := runConfigureCouncilSeatDefaultTarget(cfg, seat); err != nil {
				return err
			}
		default:
			complexity, ok := parseComplexityMenuValue(selection)
			if !ok {
				continue
			}
			if err := runConfigureCouncilSeatComplexityTarget(cfg, seat, complexity); err != nil {
				return err
			}
		}
	}
}

func runConfigureRoleDefaultTarget(cfg *KitchenConfig, role string) error {
	current, ok := roleDefaultRule(*cfg, role)
	desc := ""
	if ok {
		desc = inheritedRouteDescription("Current role default", current)
	} else {
		current = inheritedRoleDefaultRule(*cfg)
		desc = inheritedRouteDescription("Currently inherits shared route", current)
	}
	action, rule, err := runRoutingTargetMenu("role default", desc, current, "use shared default", true)
	if err != nil {
		return err
	}
	switch action {
	case routingTargetActionSave:
		applyRoleDefaultSelection(cfg, role, rule)
	case routingTargetActionClear:
		setRoleDefault(cfg, role, RoutingRule{})
	}
	return nil
}

func runConfigureRoleComplexityTarget(cfg *KitchenConfig, role string, complexity Complexity) error {
	current, ok := cfg.RoleRouting[normalizeRoutingRole(role)][complexity]
	desc := ""
	if ok {
		desc = inheritedRouteDescription("Current override", current)
	} else {
		current = inheritedRoleComplexityRule(*cfg, role, complexity)
		desc = inheritedRouteDescription("Currently inherits route", current)
	}
	action, rule, err := runRoutingTargetMenu(complexityMenuLabel(complexity), desc, current, "use inherited routing", true)
	if err != nil {
		return err
	}
	switch action {
	case routingTargetActionSave:
		applyRoleComplexitySelection(cfg, role, complexity, rule)
	case routingTargetActionClear:
		clearRoleComplexityOverride(cfg, role, complexity)
	}
	return nil
}

func runConfigureCouncilSeatDefaultTarget(cfg *KitchenConfig, seat string) error {
	currentSeatCfg, ok := councilSeatRoutingConfig(*cfg, seat)
	current := currentSeatCfg.Default
	desc := ""
	if ok && len(current.Prefer) > 0 {
		desc = inheritedRouteDescription("Current seat default", current)
	} else {
		current = inheritedCouncilSeatDefaultRule(*cfg, seat)
		desc = inheritedRouteDescription("Currently inherits planner route", current)
	}
	action, rule, err := runRoutingTargetMenu("seat default", desc, current, "use planner routing", true)
	if err != nil {
		return err
	}
	switch action {
	case routingTargetActionSave:
		applyCouncilSeatDefaultSelection(cfg, seat, rule)
	case routingTargetActionClear:
		clearCouncilSeatDefault(cfg, seat)
	}
	return nil
}

func runConfigureCouncilSeatComplexityTarget(cfg *KitchenConfig, seat string, complexity Complexity) error {
	seatCfg, _ := councilSeatRoutingConfig(*cfg, seat)
	current, ok := seatCfg.Routing[complexity]
	desc := ""
	if ok {
		desc = inheritedRouteDescription("Current seat override", current)
	} else {
		current = inheritedCouncilSeatComplexityRule(*cfg, seat, complexity)
		desc = inheritedRouteDescription("Currently inherits route", current)
	}
	action, rule, err := runRoutingTargetMenu(complexityMenuLabel(complexity), desc, current, "use inherited routing", true)
	if err != nil {
		return err
	}
	switch action {
	case routingTargetActionSave:
		applyCouncilSeatComplexitySelection(cfg, seat, complexity, rule)
	case routingTargetActionClear:
		clearCouncilSeatComplexityOverride(cfg, seat, complexity)
	}
	return nil
}

func routingRoleOptions(cfg KitchenConfig) []huh.Option[string] {
	roles := configurableKitchenRoles()
	options := make([]huh.Option[string], 0, len(roles)+1)
	for _, role := range roles {
		options = append(options, huh.NewOption[string](routingRoleMenuLabel(cfg, role), role))
	}
	options = append(options, huh.NewOption[string]("Save and exit", configureSaveAndExit))
	return options
}

func routingRoleMenuLabel(cfg KitchenConfig, role string) string {
	label := routingRoleDisplayName(role)
	switch {
	case role == defaultRoutingRole:
		return label + " (shared baseline)"
	case roleHasRoutingOverride(cfg, role):
		return label + " (custom)"
	default:
		return label + " (inherits default)"
	}
}

func routingRoleDisplayName(role string) string {
	switch normalizeRoutingRole(role) {
	case defaultRoutingRole:
		return "default"
	case plannerTaskRole:
		return "planner"
	case "implementer":
		return "implementer"
	case "reviewer":
		return "reviewer"
	case lineageFixMergeRole:
		return "lineage-fix-merge"
	default:
		return normalizeRoutingRole(role)
	}
}

func routingRoleStatus(cfg *KitchenConfig, role string) string {
	if cfg == nil {
		return ""
	}
	if roleHasRoutingOverride(*cfg, role) {
		return "This role currently has custom routing."
	}
	return "This role currently inherits the shared default routing."
}

func councilSeatMenuLabel(cfg KitchenConfig, seat string) string {
	seat = normalizeCouncilSeat(seat)
	if councilSeatHasRoutingOverride(cfg, seat) {
		return "Seat " + seat + " (custom)"
	}
	return "Seat " + seat + " (inherits planner)"
}

func councilSeatStatus(cfg KitchenConfig, seat string) string {
	seat = normalizeCouncilSeat(seat)
	if councilSeatHasRoutingOverride(cfg, seat) {
		return "This seat currently has custom routing on top of the planner baseline."
	}
	return "This seat currently inherits the planner routing."
}
