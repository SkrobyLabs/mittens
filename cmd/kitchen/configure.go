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

func runConfigure() error {
	paths, err := DefaultKitchenPaths()
	if err != nil {
		return err
	}
	cfg, err := LoadKitchenConfig(paths.ConfigPath)
	if err != nil {
		return err
	}

	// Pre-fill form values from current config.
	values := make([]roleFormValues, len(allComplexities))
	for i, c := range allComplexities {
		rule := cfg.Routing[c]
		if len(rule.Prefer) > 0 {
			values[i].PrimaryProvider = configToDisplay(rule.Prefer[0].Provider)
			values[i].PrimaryModel = rule.Prefer[0].Model
		}
		if len(rule.Fallback) > 0 {
			values[i].FallbackProvider = configToDisplay(rule.Fallback[0].Provider)
			values[i].FallbackModel = rule.Fallback[0].Model
		}
	}

	// Build one huh.Group per complexity level.
	groups := make([]*huh.Group, len(allComplexities))
	for i, c := range allComplexities {
		idx := i // capture for closures
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
		).Description(fmt.Sprintf("Configure %s complexity role", name))
	}

	form := huh.NewForm(groups...).
		WithShowHelp(true)

	fmt.Fprintln(os.Stderr, "Configure provider and model for each task complexity role.")
	fmt.Fprintln(os.Stderr)

	if err := form.Run(); err != nil {
		return err
	}

	// Build routing map from form values.
	routing := make(map[Complexity]RoutingRule, len(allComplexities))
	for i, c := range allComplexities {
		v := values[i]
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
		routing[c] = rule
	}

	cfg.Routing = routing

	if err := SaveKitchenConfigFile(paths.ConfigPath, &cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Configuration saved to %s\n", paths.ConfigPath)
	return nil
}
