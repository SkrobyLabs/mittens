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

// configToDisplay maps config provider names (e.g. "anthropic") to TUI
// display names.
func configToDisplay(provider string) string {
	if provider == "anthropic" {
		return "claude"
	}
	return provider
}

// displayToConfig maps TUI display names back to config provider names.
func displayToConfig(provider string) string {
	if provider == "claude" {
		return "anthropic"
	}
	return provider
}

// roleFormValues holds the TUI state for one complexity level.
type roleFormValues struct {
	PrimaryProvider  string
	PrimaryModel     string
	FallbackProvider string
	FallbackModel    string
}

const configureSaveAndExit = "__save_and_exit__"

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

	if err := SaveKitchenConfigFile(paths.ConfigPath, &cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Configuration saved to %s\n", paths.ConfigPath)
	fmt.Fprintln(os.Stdout, "Routing changes take effect the next time `kitchen serve` starts.")
	return nil
}

func runConfigureRole(cfg *KitchenConfig, role string) error {
	role = normalizeRoutingRole(role)
	if role == plannerTaskRole {
		return runConfigurePlanner(cfg)
	}
	if role == defaultRoutingRole {
		routing, err := editRoleRouting(role, effectiveRoutingForRole(*cfg, role))
		if err != nil {
			return err
		}
		setRoutingForRole(cfg, role, routing)
		return nil
	}

	action := "edit"
	options := []huh.Option[string]{
		huh.NewOption[string]("Edit role default and overrides", "edit"),
		huh.NewOption[string]("Use default routing", "inherit"),
		huh.NewOption[string]("Back", "back"),
	}
	if err := huh.NewSelect[string]().
		Title(routingRoleDisplayName(role)).
		Description(routingRoleStatus(cfg, role)).
		Options(options...).
		Value(&action).
		Run(); err != nil {
		return err
	}
	switch action {
	case "back":
		return nil
	case "inherit":
		clearRoutingForRole(cfg, role)
		return nil
	}

	defaultRule, overrides, err := editRoleDefaultsAndOverrides(*cfg, role)
	if err != nil {
		return err
	}
	setRoleDefault(cfg, role, defaultRule)
	setRoleComplexityOverrides(cfg, role, overrides)
	return nil
}

func runConfigurePlanner(cfg *KitchenConfig) error {
	for {
		selection := "planner"
		options := []huh.Option[string]{
			huh.NewOption[string]("Edit planner routing", "planner"),
			huh.NewOption[string](councilSeatMenuLabel(*cfg, "A"), "seat:A"),
			huh.NewOption[string](councilSeatMenuLabel(*cfg, "B"), "seat:B"),
			huh.NewOption[string]("Back", "back"),
		}
		if err := huh.NewSelect[string]().
			Title("planner").
			Description("Planner routing is the shared baseline for both council seats. Seat routing stores only the differences from planner.").
			Options(options...).
			Value(&selection).
			Run(); err != nil {
			return err
		}
		switch selection {
		case "back":
			return nil
		case "planner":
			action := "edit"
			if err := huh.NewSelect[string]().
				Title("planner").
				Description(routingRoleStatus(cfg, plannerTaskRole)).
				Options(
					huh.NewOption[string]("Edit role default and overrides", "edit"),
					huh.NewOption[string]("Use default routing", "inherit"),
					huh.NewOption[string]("Back", "back"),
				).
				Value(&action).
				Run(); err != nil {
				return err
			}
			switch action {
			case "back":
				continue
			case "inherit":
				clearRoutingForRole(cfg, plannerTaskRole)
				continue
			}
			defaultRule, overrides, err := editRoleDefaultsAndOverrides(*cfg, plannerTaskRole)
			if err != nil {
				return err
			}
			setRoleDefault(cfg, plannerTaskRole, defaultRule)
			setRoleComplexityOverrides(cfg, plannerTaskRole, overrides)
		case "seat:A":
			if err := runConfigureCouncilSeat(cfg, "A"); err != nil {
				return err
			}
		case "seat:B":
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
	action := "edit"
	if err := huh.NewSelect[string]().
		Title("Council seat "+seat).
		Description(councilSeatStatus(*cfg, seat)).
		Options(
			huh.NewOption[string]("Edit seat default and overrides", "edit"),
			huh.NewOption[string]("Use planner routing", "inherit"),
			huh.NewOption[string]("Back", "back"),
		).
		Value(&action).
		Run(); err != nil {
		return err
	}
	switch action {
	case "back":
		return nil
	case "inherit":
		clearCouncilSeatRoutingConfig(cfg, seat)
		return nil
	}
	seatCfg, err := editCouncilSeatDefaultsAndOverrides(*cfg, seat)
	if err != nil {
		return err
	}
	setCouncilSeatRoutingConfig(cfg, seat, seatCfg)
	return nil
}

func editRoleRouting(role string, routing map[Complexity]RoutingRule) (map[Complexity]RoutingRule, error) {
	values := roleFormValuesFromRouting(routing)

	groups := make([]*huh.Group, len(allComplexities))
	for i, c := range allComplexities {
		idx := i
		name := complexityDisplayName(c)

		groups[i] = huh.NewGroup(
			huh.NewSelect[string]().
				Title(name+" — Primary provider").
				Options(providerOptions(false)...).
				Value(&values[idx].PrimaryProvider),
			huh.NewInput().
				Title(name+" — Primary model").
				Value(&values[idx].PrimaryModel),
			huh.NewSelect[string]().
				Title(name+" — Fallback provider").
				Options(providerOptions(true)...).
				Value(&values[idx].FallbackProvider),
			huh.NewInput().
				Title(name+" — Fallback model").
				Value(&values[idx].FallbackModel),
		).Description(fmt.Sprintf("%s routing for %s complexity", routingRoleDisplayName(role), name))
	}

	if err := huh.NewForm(groups...).WithShowHelp(true).Run(); err != nil {
		return nil, err
	}
	return routingFromRoleFormValues(values), nil
}

func roleFormValuesFromRouting(routing map[Complexity]RoutingRule) []roleFormValues {
	values := make([]roleFormValues, len(allComplexities))
	for i, c := range allComplexities {
		rule := routing[c]
		if len(rule.Prefer) > 0 {
			values[i].PrimaryProvider = configToDisplay(rule.Prefer[0].Provider)
			values[i].PrimaryModel = rule.Prefer[0].Model
		}
		if len(rule.Fallback) > 0 {
			values[i].FallbackProvider = configToDisplay(rule.Fallback[0].Provider)
			values[i].FallbackModel = rule.Fallback[0].Model
		}
	}
	return values
}

func routingFromRoleFormValues(values []roleFormValues) map[Complexity]RoutingRule {
	routing := make(map[Complexity]RoutingRule, len(allComplexities))
	for i, c := range allComplexities {
		routing[c] = routingRuleFromFormValues(values[i])
	}
	return routing
}

func editRoleDefaultsAndOverrides(cfg KitchenConfig, role string) (RoutingRule, map[Complexity]RoutingRule, error) {
	effective := effectiveRoutingForRole(cfg, role)
	defaultRule, hasDefault := roleDefaultRule(cfg, role)
	defaultValues := roleFormValues{}
	if hasDefault {
		defaultValues = roleFormValuesFromRule(defaultRule)
	} else {
		defaultValues = roleFormValuesFromRule(effective[ComplexityMedium])
	}

	useRoleDefault := hasDefault
	if err := huh.NewConfirm().
		Title("Set a role-level default route?").
		Description("Applies one provider/model to this whole role, with optional per-complexity overrides afterward.").
		Value(&useRoleDefault).
		Run(); err != nil {
		return RoutingRule{}, nil, err
	}
	if useRoleDefault {
		rule, err := editSingleRoutingRule("Role default routing", defaultValues, false)
		if err != nil {
			return RoutingRule{}, nil, err
		}
		defaultRule = rule
	} else {
		defaultRule = RoutingRule{}
	}

	overrides := make(map[Complexity]RoutingRule)
	for _, complexity := range allComplexities {
		existingRule, hasExisting := cfg.RoleRouting[role][complexity]
		enabled := hasExisting
		if !enabled && !useRoleDefault && !routingRuleEqual(effective[complexity], cfg.Routing[complexity]) {
			enabled = true
		}
		overrideLabel := complexityDisplayName(complexity) + " override"
		if err := huh.NewConfirm().
			Title("Override " + complexityDisplayName(complexity) + " complexity?").
			Description("Leave off to use the role default or inherited shared default for this complexity.").
			Value(&enabled).
			Run(); err != nil {
			return RoutingRule{}, nil, err
		}
		if !enabled {
			continue
		}
		initial := effective[complexity]
		if hasExisting {
			initial = existingRule
		} else if useRoleDefault {
			initial = defaultRule
		}
		rule, err := editSingleRoutingRule(overrideLabel, roleFormValuesFromRule(initial), true)
		if err != nil {
			return RoutingRule{}, nil, err
		}
		overrides[complexity] = rule
	}

	return defaultRule, overrides, nil
}

func editCouncilSeatDefaultsAndOverrides(cfg KitchenConfig, seat string) (CouncilSeatRoutingConfig, error) {
	effective := effectiveRoutingForCouncilSeat(cfg, seat)
	seatCfg, _ := councilSeatRoutingConfig(cfg, seat)
	defaultValues := roleFormValuesFromRule(seatCfg.Default)
	if len(seatCfg.Default.Prefer) == 0 {
		defaultValues = roleFormValuesFromRule(effective[ComplexityMedium])
	}

	useSeatDefault := len(seatCfg.Default.Prefer) > 0
	if err := huh.NewConfirm().
		Title("Set a seat-level default route?").
		Description("Applies one provider/model to this council seat, with optional per-complexity overrides afterward.").
		Value(&useSeatDefault).
		Run(); err != nil {
		return CouncilSeatRoutingConfig{}, err
	}
	defaultRule := RoutingRule{}
	if useSeatDefault {
		rule, err := editSingleRoutingRule("Seat default routing", defaultValues, false)
		if err != nil {
			return CouncilSeatRoutingConfig{}, err
		}
		defaultRule = rule
	}

	overrides := make(map[Complexity]RoutingRule)
	for _, complexity := range allComplexities {
		existingRule, hasExisting := seatCfg.Routing[complexity]
		enabled := hasExisting
		plannerRouting := effectiveRoutingForRole(cfg, plannerTaskRole)
		if !enabled && !useSeatDefault && !routingRuleEqual(effective[complexity], plannerRouting[complexity]) {
			enabled = true
		}
		if err := huh.NewConfirm().
			Title("Override " + complexityDisplayName(complexity) + " complexity?").
			Description("Leave off to use the planner baseline or the seat default for this complexity.").
			Value(&enabled).
			Run(); err != nil {
			return CouncilSeatRoutingConfig{}, err
		}
		if !enabled {
			continue
		}
		initial := effective[complexity]
		if hasExisting {
			initial = existingRule
		} else if useSeatDefault {
			initial = defaultRule
		}
		rule, err := editSingleRoutingRule(complexityDisplayName(complexity)+" override", roleFormValuesFromRule(initial), true)
		if err != nil {
			return CouncilSeatRoutingConfig{}, err
		}
		overrides[complexity] = rule
	}

	return CouncilSeatRoutingConfig{Default: defaultRule, Routing: overrides}, nil
}

func editSingleRoutingRule(title string, values roleFormValues, includeNone bool) (RoutingRule, error) {
	if err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(title+" — Primary provider").
				Options(providerOptions(false)...).
				Value(&values.PrimaryProvider),
			huh.NewInput().
				Title(title+" — Primary model").
				Value(&values.PrimaryModel),
			huh.NewSelect[string]().
				Title(title+" — Fallback provider").
				Options(providerOptions(includeNone)...).
				Value(&values.FallbackProvider),
			huh.NewInput().
				Title(title+" — Fallback model").
				Value(&values.FallbackModel),
		),
	).WithShowHelp(true).Run(); err != nil {
		return RoutingRule{}, err
	}
	return routingRuleFromFormValues(values), nil
}

func roleFormValuesFromRule(rule RoutingRule) roleFormValues {
	var values roleFormValues
	if len(rule.Prefer) > 0 {
		values.PrimaryProvider = configToDisplay(rule.Prefer[0].Provider)
		values.PrimaryModel = rule.Prefer[0].Model
	}
	if len(rule.Fallback) > 0 {
		values.FallbackProvider = configToDisplay(rule.Fallback[0].Provider)
		values.FallbackModel = rule.Fallback[0].Model
	}
	return values
}

func routingRuleFromFormValues(v roleFormValues) RoutingRule {
	rule := RoutingRule{
		Prefer: []PoolKey{{
			Provider: displayToConfig(v.PrimaryProvider),
			Model:    strings.TrimSpace(v.PrimaryModel),
		}},
	}
	fbProvider := strings.TrimSpace(v.FallbackProvider)
	fbModel := strings.TrimSpace(v.FallbackModel)
	if fbProvider != "" && fbModel != "" {
		rule.Fallback = []PoolKey{{
			Provider: displayToConfig(fbProvider),
			Model:    fbModel,
		}}
	}
	return rule
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
		return "This role currently has custom per-complexity routing."
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
