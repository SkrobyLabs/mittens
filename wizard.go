package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/Skroby/mittens/extensions/registry"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	wizardTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	wizardSuccess = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("82"))
	wizardDim     = lipgloss.NewStyle().Faint(true)
	wizardBold    = lipgloss.NewStyle().Bold(true)
)

// ---------------------------------------------------------------------------
// Extensions with dedicated wizard steps (excluded from the generic multi-select).
var wizardExcluded = map[string]bool{"firewall": true}

// ---------------------------------------------------------------------------
// Main entry point
// ---------------------------------------------------------------------------

// runWizard runs the interactive TUI setup wizard. The extensions parameter
// is the loaded extension list from the embedded YAML manifests (so the wizard
// knows which extensions are available).
func runWizard(extensions []*registry.Extension) error {

	// 1. Detect workspace.
	workspace := detectWorkspace()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("mittens project setup"))
	fmt.Fprintln(os.Stderr)

	// 2. Handle existing config.
	// Use raw lines (not split tokens) so that "parseExistingConfig" and
	// display both see whole lines like "--go 1.24" rather than ["--go","1.24"].
	existing, err := loadProjectConfigRaw(workspace)
	if err != nil {
		return fmt.Errorf("loading existing config: %w", err)
	}

	editMode := false
	var existDirs, existProviders, existExts, existFirewall, existOpts []string

	if len(existing) > 0 {
		configPath := filepath.Join(ConfigHome(), "projects", ProjectDir(workspace), "config")
		fmt.Fprintf(os.Stderr, "Existing config: %s\n\n", configPath)
		for _, line := range existing {
			fmt.Fprintln(os.Stderr, wizardDim.Render("  "+line))
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Existing configuration found").
			Options(
				huh.NewOption("Launch mittens", "launch"),
				huh.NewOption("Edit (keep defaults)", "edit"),
				huh.NewOption("Overwrite (start fresh)", "overwrite"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&action).
			Run(); err != nil {
			return gracefulAbort(err)
		}

		switch action {
		case "launch":
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("finding executable path: %w", err)
			}
			exe, _ = filepath.EvalSymlinks(exe)
			return execCommand(exe)
		case "cancel":
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		case "edit":
			editMode = true
			existDirs, existProviders, existExts, existFirewall, existOpts = parseExistingConfig(existing)
		}
		fmt.Fprintln(os.Stderr)
	}

	var configLines []string

	// ── Step 1: Provider ───────────────────────────────────────────────────
	providerLines, err := wizardProvider(editMode, existProviders)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, providerLines...)

	// ── Step 2: Extra directories ──────────────────────────────────────────
	dirLines, err := wizardDirs(workspace, editMode, existDirs)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, dirLines...)

	// ── Step 3+4: Extensions ───────────────────────────────────────────────
	extLines, err := wizardExtensions(extensions, editMode, existExts)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, extLines...)

	// ── Step 5: Firewall ───────────────────────────────────────────────────
	fwLines, err := wizardFirewall(editMode, existFirewall)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, fwLines...)

	// ── Step N: Options ────────────────────────────────────────────────────
	optLines, err := wizardOptions(editMode, existOpts)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, optLines...)

	// ── Write config ───────────────────────────────────────────────────────
	if err := SaveProjectConfig(workspace, configLines); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	configPath := filepath.Join(ConfigHome(), "projects", ProjectDir(workspace), "config")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Config saved to: "+configPath))
	fmt.Fprintln(os.Stderr)

	if len(configLines) > 0 {
		equiv := "mittens " + strings.Join(configLines, " ")
		fmt.Fprintln(os.Stderr, wizardDim.Render("Equivalent: "+equiv))
	} else {
		fmt.Fprintln(os.Stderr, wizardDim.Render("Equivalent: mittens (default settings)"))
	}
	fmt.Fprintln(os.Stderr)

	// ── Offer to run now ───────────────────────────────────────────────────
	runNow := true
	if err := huh.NewConfirm().
		Title("Run mittens now?").
		Value(&runNow).
		Run(); err != nil {
		return gracefulAbort(err)
	}

	if runNow {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("finding executable path: %w", err)
		}
		exe, _ = filepath.EvalSymlinks(exe)
		return execCommand(exe)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Step 2: Directories
// ---------------------------------------------------------------------------

func wizardDirs(workspace string, editMode bool, existDirs []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 2: Directories"))
	fmt.Fprintf(os.Stderr, "Primary workspace: %s\n", workspace)

	// In edit mode, offer to keep existing directories.
	if editMode {
		if len(existDirs) > 0 {
			fmt.Fprintln(os.Stderr, "\nCurrently configured:")
			for _, d := range existDirs {
				fmt.Fprintln(os.Stderr, "  "+d)
			}
		} else {
			fmt.Fprintln(os.Stderr, "  (no extra directories)")
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Extra directories").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existDirs, nil
		}
	}

	// Build a set of existing dir paths for pre-selection in edit mode.
	existPathSet := make(map[string]bool, len(existDirs))
	if editMode {
		for _, d := range existDirs {
			existPathSet[strings.TrimPrefix(d, "--dir ")] = true
		}
	}

	var selectedDirs []string

	// Interactive directory browser starting at the workspace's parent.
	parentDir := filepath.Dir(workspace)
	fmt.Fprintln(os.Stderr)
	chosen, err := runDirPicker(parentDir, existPathSet)
	if err != nil {
		return nil, err
	}
	selectedDirs = append(selectedDirs, chosen...)

	// Custom path entry loop.
	for {
		var custom string
		if err := huh.NewInput().
			Title("Add custom directory path (leave empty to continue)").
			Placeholder("/path/to/directory").
			Value(&custom).
			Run(); err != nil {
			return nil, err
		}
		custom = strings.TrimSpace(custom)
		if custom == "" {
			break
		}
		abs, err := filepath.Abs(custom)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Could not resolve path: %v\n", err)
			continue
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			fmt.Fprintf(os.Stderr, "  Directory not found: %s\n", abs)
			continue
		}
		selectedDirs = append(selectedDirs, abs)
	}

	var lines []string
	for _, d := range selectedDirs {
		lines = append(lines, "--dir "+d)
	}

	fmt.Fprintln(os.Stderr)
	return lines, nil
}

// ---------------------------------------------------------------------------
// Step 1: Provider
// ---------------------------------------------------------------------------

func wizardProvider(editMode bool, existProviders []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 1: Provider"))

	if editMode {
		if len(existProviders) > 0 {
			fmt.Fprintln(os.Stderr, "\nCurrently configured:")
			for _, p := range existProviders {
				fmt.Fprintln(os.Stderr, "  "+p)
			}
		} else {
			fmt.Fprintln(os.Stderr, "  --provider claude (default)")
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Provider").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existProviders, nil
		}
	}

	selectedSet, defaultProvider := parseProviderLines(existProviders)
	if defaultProvider == "" {
		defaultProvider = "claude"
	}

	var selected []string
	providerOptions := []struct {
		name        string
		label       string
		description string
	}{
		{name: "claude", label: "Claude", description: "Anthropic Claude Code CLI"},
		{name: "codex", label: "Codex", description: "OpenAI Codex CLI"},
		{name: "gemini", label: "Gemini", description: "Google Gemini CLI"},
	}
	var opts []huh.Option[string]
	for _, p := range providerOptions {
		opts = append(opts, huh.NewOption(p.label+"  "+p.description, p.name).Selected(selectedSet[p.name]))
	}

	if err := huh.NewMultiSelect[string]().
		Title("Select AI CLI providers to support").
		Options(opts...).
		Value(&selected).
		Run(); err != nil {
		return nil, err
	}

	if len(selected) == 0 {
		selected = []string{"claude"}
	}

	defaultChoice := defaultProvider
	containsDefault := false
	for _, p := range selected {
		if p == defaultChoice {
			containsDefault = true
			break
		}
	}
	if !containsDefault {
		defaultChoice = selected[0]
	}

	if len(selected) > 1 {
		var defaultOpts []huh.Option[string]
		for _, p := range selected {
			label := p
			switch p {
			case "claude":
				label = "Claude"
			case "codex":
				label = "Codex"
			case "gemini":
				label = "Gemini"
			}
			defaultOpts = append(defaultOpts, huh.NewOption(label, p))
		}
		if err := huh.NewSelect[string]().
			Title("Pick default provider").
			Options(defaultOpts...).
			Value(&defaultChoice).
			Run(); err != nil {
			return nil, err
		}
	}

	var lines []string
	for _, p := range selected {
		if p == defaultChoice {
			continue
		}
		lines = append(lines, "--provider "+p)
	}
	lines = append(lines, "--provider "+defaultChoice)

	fmt.Fprintln(os.Stderr)
	return lines, nil
}

func parseProviderLines(lines []string) (selected map[string]bool, defaultProvider string) {
	selected = make(map[string]bool)
	defaultProvider = ""
	for _, line := range lines {
		if !strings.HasPrefix(line, "--provider ") {
			continue
		}
		p := strings.TrimSpace(strings.TrimPrefix(line, "--provider "))
		if p == "" {
			continue
		}
		switch p {
		case "claude", "codex", "gemini":
			selected[p] = true
			defaultProvider = p
		}
	}
	return selected, defaultProvider
}

// ---------------------------------------------------------------------------
// Step 3+4: Extensions
// ---------------------------------------------------------------------------

func wizardExtensions(extensions []*registry.Extension, editMode bool, existExts []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 3: Extensions"))

	// In edit mode, offer to keep existing extensions.
	if editMode {
		if len(existExts) > 0 {
			fmt.Fprintln(os.Stderr, "\nCurrently configured:")
			for _, e := range existExts {
				fmt.Fprintln(os.Stderr, "  "+e)
			}
		} else {
			fmt.Fprintln(os.Stderr, "  (no extensions)")
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Extensions").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existExts, nil
		}
	}

	// Build a set of existing extension names for pre-selection in edit mode.
	existExtSet := make(map[string]bool)
	if editMode {
		for _, line := range existExts {
			for _, ext := range extensions {
				if wizardExcluded[ext.Name] {
					continue
				}
				flag := extPrimaryFlag(ext)
				if flag != "" && (line == flag || strings.HasPrefix(line, flag+" ")) {
					existExtSet[ext.Name] = true
					break
				}
			}
		}
	}

	// Build options from loaded extensions (excluding those with dedicated wizard steps).
	var opts []huh.Option[string]
	var available []*registry.Extension
	for _, ext := range extensions {
		if wizardExcluded[ext.Name] {
			continue
		}
		flag := extPrimaryFlag(ext)
		label := flag + "  " + ext.Description
		available = append(available, ext)
		opts = append(opts, huh.NewOption(label, ext.Name).Selected(existExtSet[ext.Name]))
	}

	if len(opts) == 0 {
		fmt.Fprintln(os.Stderr, "  No extensions available.")
		fmt.Fprintln(os.Stderr)
		return nil, nil
	}

	// Pre-populate chosen with existing extension keys so Value() matches.
	var chosen []string
	if editMode {
		for _, ext := range available {
			if existExtSet[ext.Name] {
				chosen = append(chosen, ext.Name)
			}
		}
	}
	if err := huh.NewMultiSelect[string]().
		Title("Select extensions to enable").
		Options(opts...).
		Value(&chosen).
		Run(); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr)

	// Separate simple flags from those needing configuration.
	chosenSet := make(map[string]bool, len(chosen))
	for _, k := range chosen {
		chosenSet[k] = true
	}

	// Build extension lookup for configuration step.
	extMap := make(map[string]*registry.Extension, len(available))
	for _, ext := range available {
		extMap[ext.Name] = ext
	}

	var configLines []string
	var needsCfg []*registry.Extension

	for _, ext := range available {
		if !chosenSet[ext.Name] {
			continue
		}
		if extNeedsCfg(ext) {
			needsCfg = append(needsCfg, ext)
		} else {
			flag := extPrimaryFlag(ext)
			if flag != "" {
				configLines = append(configLines, flag)
			}
		}
	}

	// Step 3: Configure extensions that need it.
	if len(needsCfg) > 0 {
		fmt.Fprintln(os.Stderr, wizardBold.Render("Step 3: Configure selected extensions"))
		fmt.Fprintln(os.Stderr)

		for _, ext := range needsCfg {
			lines, err := configureExtension(ext)
			if err != nil {
				return nil, err
			}
			configLines = append(configLines, lines...)
		}
	}

	return configLines, nil
}

// extPrimaryFlag returns the first non-negation flag name for an extension.
func extPrimaryFlag(ext *registry.Extension) string {
	for _, f := range ext.Flags {
		if !strings.HasPrefix(f.Name, "--no-") {
			return f.Name
		}
	}
	return ""
}

// extNeedsCfg returns true if the extension has any flag that requires an argument.
func extNeedsCfg(ext *registry.Extension) bool {
	for _, f := range ext.Flags {
		if f.Arg != "" && f.Arg != "none" {
			return true
		}
	}
	return false
}

// customConfigurers maps extension names to custom wizard configuration functions.
// Extensions not in this map get auto-generated prompts from their flag metadata.
var customConfigurers = map[string]func(*registry.Extension) ([]string, error){
	"dotnet": func(_ *registry.Extension) ([]string, error) { return configureDotnet() },
	"aws":    func(_ *registry.Extension) ([]string, error) { return configureCloud("aws", "--aws", "--aws-all") },
	"gcp":    func(_ *registry.Extension) ([]string, error) { return configureCloud("gcp", "--gcp", "--gcp-all") },
	"azure": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("azure", "--azure", "--azure-all")
	},
	"kubectl": func(_ *registry.Extension) ([]string, error) { return configureKubectl() },
	"mcp":     func(_ *registry.Extension) ([]string, error) { return configureMCP() },
}

// configureExtension runs the sub-configuration step for a single extension.
// Uses custom handlers where registered, otherwise auto-generates prompts from flag metadata.
func configureExtension(ext *registry.Extension) ([]string, error) {
	if fn, ok := customConfigurers[ext.Name]; ok {
		return fn(ext)
	}
	return configureExtensionGeneric(ext)
}

// configureExtensionGeneric auto-generates wizard prompts from extension flag metadata.
func configureExtensionGeneric(ext *registry.Extension) ([]string, error) {
	var lines []string
	for _, f := range ext.Flags {
		switch f.Arg {
		case "enum":
			if f.Multi {
				var vals []string
				var opts []huh.Option[string]
				for _, v := range f.EnumValues {
					opts = append(opts, huh.NewOption(v, v))
				}
				if err := huh.NewMultiSelect[string]().
					Title(ext.Description).
					Options(opts...).
					Value(&vals).
					Run(); err != nil {
					return nil, err
				}
				if len(vals) > 0 {
					lines = append(lines, f.Name+" "+strings.Join(vals, ","))
				}
			} else {
				var val string
				var opts []huh.Option[string]
				for _, v := range f.EnumValues {
					opts = append(opts, huh.NewOption(v, v))
				}
				if err := huh.NewSelect[string]().
					Title(ext.Description).
					Options(opts...).
					Value(&val).
					Run(); err != nil {
					return nil, err
				}
				lines = append(lines, f.Name+" "+val)
			}
		case "csv":
			var val string
			if err := huh.NewInput().
				Title(ext.Description + " (comma-separated)").
				Value(&val).
				Run(); err != nil {
				return nil, err
			}
			val = strings.TrimSpace(val)
			if val != "" {
				lines = append(lines, f.Name+" "+val)
			}
		case "path":
			var val string
			if err := huh.NewInput().
				Title(ext.Description + " (path)").
				Value(&val).
				Run(); err != nil {
				return nil, err
			}
			val = strings.TrimSpace(val)
			if val != "" {
				lines = append(lines, f.Name+" "+val)
			}
		}
	}
	return lines, nil
}

func configureDotnet() ([]string, error) {
	var versions []string
	if err := huh.NewMultiSelect[string]().
		Title(".NET SDK versions").
		Options(
			huh.NewOption("LTS (latest long-term support)", "lts"),
			huh.NewOption(".NET 8", "8"),
			huh.NewOption(".NET 9", "9"),
			huh.NewOption(".NET 10", "10"),
		).
		Value(&versions).
		Run(); err != nil {
		return nil, err
	}

	// Filter out "lts" when specific versions are also selected.
	var specific []string
	for _, v := range versions {
		if v != "lts" {
			specific = append(specific, v)
		}
	}

	if len(specific) == 0 {
		return []string{"--dotnet"}, nil
	}
	return []string{"--dotnet " + strings.Join(specific, ",")}, nil
}

func configureGo() ([]string, error) {
	var version string
	if err := huh.NewSelect[string]().
		Title("Go SDK version").
		Options(
			huh.NewOption("Go 1.23", "1.23"),
			huh.NewOption("Go 1.24", "1.24"),
		).
		Value(&version).
		Run(); err != nil {
		return nil, err
	}

	return []string{"--go " + version}, nil
}

// configureCloud handles aws/gcp/azure extension configuration with a
// "Skip / Select / All" pattern.
func configureCloud(name, flag, allFlag string) ([]string, error) {
	var action string
	if err := huh.NewSelect[string]().
		Title(strings.ToUpper(name)+" credentials").
		Options(
			huh.NewOption("Select profiles", "select"),
			huh.NewOption("All ("+allFlag+")", "all"),
			huh.NewOption("Skip", "skip"),
		).
		Value(&action).
		Run(); err != nil {
		return nil, err
	}

	switch action {
	case "all":
		return []string{allFlag}, nil
	case "skip":
		return nil, nil
	}

	// "select" — use list resolver to get available items.
	resolver := registry.GetListResolver(name)
	if resolver == nil {
		fmt.Fprintf(os.Stderr, "  No list resolver for %s, skipping selection.\n", name)
		return []string{flag}, nil
	}

	items, err := resolver()
	if err != nil || len(items) == 0 {
		fmt.Fprintf(os.Stderr, "  No %s profiles found.\n", name)
		return nil, nil
	}

	var opts []huh.Option[string]
	for _, item := range items {
		opts = append(opts, huh.NewOption(item, item))
	}

	var chosen []string
	if err := huh.NewMultiSelect[string]().
		Title("Select " + name + " profiles").
		Options(opts...).
		Value(&chosen).
		Run(); err != nil {
		return nil, err
	}

	if len(chosen) == 0 {
		return nil, nil
	}
	csv := strings.Join(chosen, ",")
	return []string{flag + " " + csv}, nil
}

func configureKubectl() ([]string, error) {
	var action string
	if err := huh.NewSelect[string]().
		Title("Kubernetes contexts").
		Options(
			huh.NewOption("Select contexts", "select"),
			huh.NewOption("Skip", "skip"),
		).
		Value(&action).
		Run(); err != nil {
		return nil, err
	}

	if action == "skip" {
		return nil, nil
	}

	resolver := registry.GetListResolver("kubectl")
	if resolver == nil {
		fmt.Fprintln(os.Stderr, "  No kubectl list resolver, skipping selection.")
		return nil, nil
	}

	contexts, err := resolver()
	if err != nil || len(contexts) == 0 {
		fmt.Fprintln(os.Stderr, "  No kubectl contexts found.")
		return nil, nil
	}

	var opts []huh.Option[string]
	for _, ctx := range contexts {
		opts = append(opts, huh.NewOption(ctx, ctx))
	}

	var chosen []string
	if err := huh.NewMultiSelect[string]().
		Title("Select Kubernetes contexts").
		Options(opts...).
		Value(&chosen).
		Run(); err != nil {
		return nil, err
	}

	if len(chosen) == 0 {
		return nil, nil
	}
	csv := strings.Join(chosen, ",")
	return []string{"--k8s " + csv}, nil
}

func configureMCP() ([]string, error) {
	var action string
	if err := huh.NewSelect[string]().
		Title("MCP server passthrough").
		Options(
			huh.NewOption("Select servers", "select"),
			huh.NewOption("All (--mcp-all)", "all"),
			huh.NewOption("Skip", "skip"),
		).
		Value(&action).
		Run(); err != nil {
		return nil, err
	}

	switch action {
	case "all":
		return []string{"--mcp-all"}, nil
	case "skip":
		return nil, nil
	}

	resolver := registry.GetListResolver("mcp")
	if resolver == nil {
		fmt.Fprintln(os.Stderr, "  No MCP list resolver, skipping selection.")
		return nil, nil
	}

	servers, err := resolver()
	if err != nil || len(servers) == 0 {
		fmt.Fprintln(os.Stderr, "  No MCP servers found.")
		return nil, nil
	}

	var opts []huh.Option[string]
	for _, s := range servers {
		opts = append(opts, huh.NewOption(s, s))
	}

	var chosen []string
	if err := huh.NewMultiSelect[string]().
		Title("Select MCP servers").
		Options(opts...).
		Value(&chosen).
		Run(); err != nil {
		return nil, err
	}

	if len(chosen) == 0 {
		return nil, nil
	}
	csv := strings.Join(chosen, ",")
	return []string{"--mcp " + csv}, nil
}

// ---------------------------------------------------------------------------
// Step 3: Firewall
// ---------------------------------------------------------------------------

func wizardFirewall(editMode bool, existFirewall []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Firewall"))

	if editMode {
		if len(existFirewall) > 0 {
			fmt.Fprintln(os.Stderr, "\nCurrently configured:")
			for _, f := range existFirewall {
				fmt.Fprintln(os.Stderr, "  "+f)
			}
		} else {
			fmt.Fprintln(os.Stderr, "  (strict — default)")
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Firewall mode").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existFirewall, nil
		}
	}

	fmt.Fprintln(os.Stderr)

	var mode string
	if err := huh.NewSelect[string]().
		Title("Firewall mode").
		Options(
			huh.NewOption("Strict (default) — git, registries, package managers only", "strict"),
			huh.NewOption("Developer-friendly (--firewall-dev) — adds cloud APIs, apt, CDN", "dev"),
			huh.NewOption("Custom file — provide your own whitelist", "custom"),
			huh.NewOption("Disabled (--no-firewall) — allow all outbound traffic", "off"),
		).
		Value(&mode).
		Run(); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr)

	switch mode {
	case "dev":
		return []string{"--firewall-dev"}, nil
	case "custom":
		var path string
		if err := huh.NewInput().
			Title("Path to custom whitelist file").
			Placeholder("/path/to/firewall.conf").
			Value(&path).
			Run(); err != nil {
			return nil, err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return nil, nil
		}
		return []string{"--firewall " + path}, nil
	case "off":
		return []string{"--no-firewall"}, nil
	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
// Step N: Options
// ---------------------------------------------------------------------------

func wizardOptions(editMode bool, existOpts []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Options"))

	if editMode {
		if len(existOpts) > 0 {
			fmt.Fprintln(os.Stderr, "\nCurrently configured:")
			for _, o := range existOpts {
				fmt.Fprintln(os.Stderr, "  "+o)
			}
		} else {
			fmt.Fprintln(os.Stderr, "  (defaults)")
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Options").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return existOpts, nil
		}
	}

	fmt.Fprintln(os.Stderr)

	// Pre-fill from existing options in edit mode.
	optSet := make(map[string]bool, len(existOpts))
	for _, o := range existOpts {
		optSet[o] = true
	}

	yolo := optSet["--yolo"]
	if err := huh.NewConfirm().
		Title("YOLO mode (skip permission prompts)? (--yolo)").
		Value(&yolo).
		Run(); err != nil {
		return nil, err
	}

	networkMode := "bridge"
	if optSet["--network-host"] {
		networkMode = "host"
	}
	if err := huh.NewSelect[string]().
		Title("Network mode").
		Options(
			huh.NewOption("Bridge with firewall (default)", "bridge"),
			huh.NewOption("Host networking (--network-host)", "host"),
		).
		Value(&networkMode).
		Run(); err != nil {
		return nil, err
	}

	worktree := optSet["--worktree"]
	if err := huh.NewConfirm().
		Title("Parallel agent isolation (git worktree)? (--worktree)").
		Value(&worktree).
		Run(); err != nil {
		return nil, err
	}

	var lines []string
	if yolo {
		lines = append(lines, "--yolo")
	}
	if networkMode == "host" {
		lines = append(lines, "--network-host")
	}
	if worktree {
		lines = append(lines, "--worktree")
	}

	fmt.Fprintln(os.Stderr)
	return lines, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseExistingConfig categorises existing config lines into directories,
// providers, extensions, firewall, and options.
func parseExistingConfig(lines []string) (dirs, providers, exts, firewall, opts []string) {
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--dir "):
			dirs = append(dirs, line)
		case strings.HasPrefix(line, "--provider "):
			providers = append(providers, line)
		case line == "--yolo" || line == "--network-host" || line == "--worktree":
			opts = append(opts, line)
		case line == "--firewall-dev" || line == "--no-firewall" || strings.HasPrefix(line, "--firewall "):
			firewall = append(firewall, line)
		default:
			exts = append(exts, line)
		}
	}
	return
}

// gracefulAbort handles huh interrupt errors (Ctrl+C) cleanly.
func gracefulAbort(err error) error {
	if err == huh.ErrUserAborted {
		fmt.Fprintln(os.Stderr, "\nCancelled.")
		return nil
	}
	return err
}
