package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
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

	// 0. First-run: set up user-wide defaults if they don't exist yet.
	if !UserDefaultsExist() {
		if err := wizardUserDefaults(); err != nil {
			return gracefulAbort(err)
		}
	}

	// 1. Detect workspace.
	workspace := detectWorkspace()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("mittens project setup"))
	fmt.Fprintln(os.Stderr)

	// 2. Handle existing policy/config.
	existing, source, err := loadWizardExistingConfig(workspace, extensions)
	if err != nil {
		return fmt.Errorf("loading existing config: %w", err)
	}

	editMode := false
	var existDirs, existProviders, existExts, existFirewall, existOpts, existExtraDomains []string

	if len(existing) > 0 {
		displayWizardExistingConfig(workspace, source, existing, extensions)
		existExtraDomains = loadWizardExtraDomains(workspace, extensions)

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

	// ── Step 4+5: Extensions ───────────────────────────────────────────────
	extLines, err := wizardExtensions(extensions, editMode, existExts)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, extLines...)

	// ── Step 4: Network boundary ───────────────────────────────────────────
	networkLines, extraDomains, err := wizardNetworkBoundary(editMode, existFirewall, existOpts, existExtraDomains)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, networkLines...)

	// ── Step N: Options ────────────────────────────────────────────────────
	optLines, err := wizardOptions(editMode, existOpts)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, optLines...)

	// ── Write structured project policy ────────────────────────────────────
	policy, err := PolicyFromLegacyFlags(splitConfigFlags(configLines), extensions)
	if err != nil {
		return fmt.Errorf("building policy: %w", err)
	}
	policy.Network.ExtraDomains = normalizeNetworkDomains(extraDomains)
	if err := SaveProjectPolicy(workspace, policy); err != nil {
		return fmt.Errorf("saving policy: %w", err)
	}

	configPath := projectPolicyPath(workspace)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Policy saved to: "+configPath))
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
// Session mode (ephemeral config edit)
// ---------------------------------------------------------------------------

// wizardSession runs the wizard in edit mode but does NOT persist changes.
// Returns the config lines for the caller to use as ephemeral config.
// Returns huh.ErrUserAborted on Ctrl+C (not nil) so the caller can
// distinguish cancellation from an empty-but-valid config.
func wizardSession(extensions []*registry.Extension) ([]string, []string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, nil, fmt.Errorf("--session requires an interactive terminal")
	}

	workspace := detectWorkspace()

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("mittens session settings"))
	fmt.Fprintln(os.Stderr, wizardDim.Render("Changes apply to this launch only and will not be saved."))
	fmt.Fprintln(os.Stderr)

	existing, source, err := loadWizardExistingConfig(workspace, extensions)
	if err != nil {
		return nil, nil, fmt.Errorf("loading existing config: %w", err)
	}

	editMode := false
	var existDirs, existProviders, existExts, existFirewall, existOpts, existExtraDomains []string

	if len(existing) > 0 {
		editMode = true
		existDirs, existProviders, existExts, existFirewall, existOpts = parseExistingConfig(existing)
		existExtraDomains = loadWizardExtraDomains(workspace, extensions)
		displayWizardExistingConfig(workspace, source, existing, extensions)
	}

	var configLines []string

	providerLines, err := wizardProvider(editMode, existProviders)
	if err != nil {
		return nil, nil, err
	}
	configLines = append(configLines, providerLines...)

	dirLines, err := wizardDirs(workspace, editMode, existDirs)
	if err != nil {
		return nil, nil, err
	}
	configLines = append(configLines, dirLines...)

	extLines, err := wizardExtensions(extensions, editMode, existExts)
	if err != nil {
		return nil, nil, err
	}
	configLines = append(configLines, extLines...)

	networkLines, extraDomains, err := wizardNetworkBoundary(editMode, existFirewall, existOpts, existExtraDomains)
	if err != nil {
		return nil, nil, err
	}
	configLines = append(configLines, networkLines...)

	optLines, err := wizardOptions(editMode, existOpts)
	if err != nil {
		return nil, nil, err
	}
	configLines = append(configLines, optLines...)

	fmt.Fprintln(os.Stderr)
	if len(configLines) > 0 {
		equiv := "mittens " + strings.Join(configLines, " ")
		fmt.Fprintln(os.Stderr, wizardDim.Render("Equivalent: "+equiv))
	} else {
		fmt.Fprintln(os.Stderr, wizardDim.Render("Equivalent: mittens (default settings)"))
	}
	fmt.Fprintln(os.Stderr)

	return configLines, extraDomains, nil
}

// ---------------------------------------------------------------------------
// Step 0: User-wide defaults
// ---------------------------------------------------------------------------

func wizardUserDefaults() error {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardTitle.Render("User-wide defaults"))
	fmt.Fprintln(os.Stderr, wizardDim.Render("These defaults apply to all projects. You can override them per-project."))
	fmt.Fprintln(os.Stderr)

	// If defaults already exist, show them and offer edit/overwrite/cancel.
	existing, _ := loadUserDefaultsRaw()
	if len(existing) > 0 {
		fmt.Fprintf(os.Stderr, "Existing defaults: %s\n\n", UserDefaultsPath())
		for _, line := range existing {
			fmt.Fprintln(os.Stderr, wizardDim.Render("  "+line))
		}
		fmt.Fprintln(os.Stderr)

		var action string
		if err := huh.NewSelect[string]().
			Title("Existing user defaults found").
			Options(
				huh.NewOption("Keep current defaults", "keep"),
				huh.NewOption("Overwrite (start fresh)", "overwrite"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&action).
			Run(); err != nil {
			return err
		}
		switch action {
		case "keep":
			return nil
		case "cancel":
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return nil
		}
		fmt.Fprintln(os.Stderr)
	}

	// Parse existing defaults to pre-select current values.
	existProvider := "claude"
	existPasteKey := "meta+v"
	existFirewall := "strict"
	for _, line := range existing {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			switch fields[0] {
			case "--provider":
				existProvider = fields[1]
			case "--image-paste-key":
				existPasteKey = fields[1]
			}
		}
		if len(fields) >= 1 {
			switch fields[0] {
			case "--firewall-dev":
				existFirewall = "dev"
			case "--no-firewall":
				existFirewall = "off"
			}
		}
	}

	var lines []string

	// 1. Default provider.
	provider := existProvider
	if err := huh.NewSelect[string]().
		Title("Default AI provider").
		Options(
			huh.NewOption("Claude (Anthropic)", "claude"),
			huh.NewOption("Codex (OpenAI)", "codex"),
			huh.NewOption("Gemini (Google)", "gemini"),
		).
		Value(&provider).
		Run(); err != nil {
		return err
	}
	if provider != "claude" {
		lines = append(lines, "--provider "+provider)
	}

	// 2. Clipboard paste key (WSL only — on macOS/Linux meta+v is always correct).
	pasteKey := existPasteKey
	if isWSL() {
		if err := huh.NewSelect[string]().
			Title("Image paste keybinding").
			Description("meta+v = Alt+V (no terminal changes needed), ctrl+v = Ctrl+V (requires Windows Terminal rebind)").
			Options(
				huh.NewOption("Alt+V (meta+v) — default, no terminal changes", "meta+v"),
				huh.NewOption("Ctrl+V (ctrl+v) — needs Windows Terminal rebind", "ctrl+v"),
			).
			Value(&pasteKey).
			Run(); err != nil {
			return err
		}
		if pasteKey == "ctrl+v" {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  ⚠ Windows Terminal intercepts Ctrl+V for text paste.")
			fmt.Fprintln(os.Stderr, "    To use Ctrl+V for image paste inside mittens, rebind")
			fmt.Fprintln(os.Stderr, "    the paste action in Windows Terminal settings to another")
			fmt.Fprintln(os.Stderr, "    shortcut (e.g. Ctrl+Shift+V).")
			fmt.Fprintln(os.Stderr)
			lines = append(lines, "--image-paste-key "+pasteKey)
		}
	}

	// 3. Default firewall mode.
	fwMode := existFirewall
	if err := huh.NewSelect[string]().
		Title("Default firewall mode").
		Options(
			huh.NewOption("Strict — git, registries, package managers only", "strict"),
			huh.NewOption("Developer-friendly (--firewall-dev) — adds cloud APIs, apt, CDN", "dev"),
			huh.NewOption("Disabled (--no-firewall) — allow all outbound traffic", "off"),
		).
		Value(&fwMode).
		Run(); err != nil {
		return err
	}
	switch fwMode {
	case "dev":
		lines = append(lines, "--firewall-dev")
	case "off":
		lines = append(lines, "--no-firewall")
	}

	if err := SaveUserDefaults(lines); err != nil {
		return fmt.Errorf("saving user defaults: %w", err)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("User defaults saved to: "+UserDefaultsPath()))
	fmt.Fprintln(os.Stderr)
	if len(lines) > 0 {
		for _, l := range lines {
			fmt.Fprintln(os.Stderr, wizardDim.Render("  "+l))
		}
		fmt.Fprintln(os.Stderr)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Profile setup (mittens init --profile NAME)
// ---------------------------------------------------------------------------

// wizardProfile configures a single model profile for the active provider.
func wizardProfile(workspace, profileName, providerName string) error {
	provider, err := providerByName(providerName)
	if err != nil {
		return err
	}

	pc, err := LoadProfileConfig(workspace)
	if err != nil {
		pc = &ProfileConfig{Profiles: map[string]map[string]ProfilePreset{}}
	}

	existing := ProfilePreset{}
	if providerProfiles, ok := pc.Profiles[provider.Name]; ok {
		if p, ok := providerProfiles[profileName]; ok {
			existing = p
		}
	}

	fmt.Fprintln(os.Stderr, wizardBold.Render(fmt.Sprintf("Configure profile %q for %s", profileName, provider.DisplayName)))
	fmt.Fprintln(os.Stderr)

	model := existing.Model
	if err := huh.NewInput().
		Title("Model").
		Placeholder("e.g. opus, haiku, sonnet").
		Value(&model).
		Run(); err != nil {
		return gracefulAbort(err)
	}
	existing.Model = strings.TrimSpace(model)

	if effortEnabled(provider) {
		effort := existing.Effort
		if err := huh.NewSelect[string]().
			Title("Effort").
			Options(
				huh.NewOption("(none)", ""),
				huh.NewOption("low", "low"),
				huh.NewOption("medium", "medium"),
				huh.NewOption("high", "high"),
				huh.NewOption("max", "max"),
			).
			Value(&effort).
			Run(); err != nil {
			return gracefulAbort(err)
		}
		existing.Effort = effort
	}

	if pc.Profiles == nil {
		pc.Profiles = map[string]map[string]ProfilePreset{}
	}
	if pc.Profiles[provider.Name] == nil {
		pc.Profiles[provider.Name] = map[string]ProfilePreset{}
	}
	pc.Profiles[provider.Name][profileName] = existing

	if err := SaveProfileConfig(workspace, pc); err != nil {
		return fmt.Errorf("saving profile config: %w", err)
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, wizardSuccess.Render(fmt.Sprintf("Profile %q saved (model=%s, effort=%s)", profileName, existing.Model, existing.Effort)))
	return nil
}

// ---------------------------------------------------------------------------
// Step 3: Directories
// ---------------------------------------------------------------------------

func wizardDirs(workspace string, editMode bool, existDirs []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 3: Directories"))
	fmt.Fprintf(os.Stderr, "Primary workspace: %s\n", workspace)

	// In edit mode, offer to keep existing directories.
	if editMode {
		displayCurrentSetup(existDirs, "(no extra directories)")

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

	// Build a map of existing dir paths for pre-selection in edit mode.
	existPathSet := make(map[string]bool, len(existDirs))
	if editMode {
		for _, d := range existDirs {
			switch {
			case strings.HasPrefix(d, "--dir-ro "):
				existPathSet[strings.TrimPrefix(d, "--dir-ro ")] = true
			case strings.HasPrefix(d, "--dir "):
				existPathSet[strings.TrimPrefix(d, "--dir ")] = false
			}
		}
	}

	var selectedDirs []dirMountSelection

	// Interactive directory browser starting at the workspace's parent.
	parentDir := filepath.Dir(workspace)
	fmt.Fprintln(os.Stderr)
	chosen, err := runDirPicker(parentDir, existPathSet, workspace)
	if err != nil {
		return nil, err
	}
	selectedDirs = append(selectedDirs, chosen...)

	var lines []string
	for _, d := range selectedDirs {
		if d.ReadOnly {
			lines = append(lines, "--dir-ro "+d.Path)
		} else {
			lines = append(lines, "--dir "+d.Path)
		}
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
		displayCurrentSetup(existProviders, "Provider: claude (default)")

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
		displayCurrentSetup(existExts, "(no extensions)")

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
	"aws": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("aws", "--aws", "--aws-all", "AWS credentials", "Select AWS profiles")
	},
	"gcp": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("gcp", "--gcp", "--gcp-all", "GCP credentials", "Select GCP profiles")
	},
	"azure": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("azure", "--azure", "--azure-all", "Azure credentials", "Select Azure profiles")
	},
	"kubectl": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("kubectl", "--k8s", "", "Kubernetes contexts", "Select Kubernetes contexts")
	},
	"mcp": func(_ *registry.Extension) ([]string, error) {
		return configureCloud("mcp", "--mcp", "--mcp-all", "MCP server passthrough", "Select MCP servers")
	},
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

// configureCloud handles extension configuration with a "Skip / Select / All"
// pattern. When allFlag is empty, the "All" option is omitted.
func configureCloud(name, flag, allFlag, title, selectTitle string) ([]string, error) {
	var action string

	selectOpts := []huh.Option[string]{
		huh.NewOption("Select", "select"),
	}
	if allFlag != "" {
		selectOpts = append(selectOpts, huh.NewOption("All ("+allFlag+")", "all"))
	}
	selectOpts = append(selectOpts, huh.NewOption("Skip", "skip"))

	if err := huh.NewSelect[string]().
		Title(title).
		Options(selectOpts...).
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
		fmt.Fprintf(os.Stderr, "  No %s items found.\n", name)
		return nil, nil
	}

	var opts []huh.Option[string]
	for _, item := range items {
		opts = append(opts, huh.NewOption(item.Label, item.Value))
	}

	var chosen []string
	if err := huh.NewMultiSelect[string]().
		Title(selectTitle).
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

// ---------------------------------------------------------------------------
// Step 4: Network boundary
// ---------------------------------------------------------------------------

func wizardNetworkBoundary(editMode bool, existFirewall, existOpts, existExtraDomains []string) ([]string, []string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 4: Network boundary"))

	if editMode {
		displayCurrentSetup(existingNetworkLines(existFirewall, existOpts, existExtraDomains), "Network: bridge + strict firewall (default)")

		var action string
		if err := huh.NewSelect[string]().
			Title("Network boundary").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return appendNetworkLines(existFirewall, existOpts), existExtraDomains, nil
		}
	}

	fmt.Fprintln(os.Stderr)

	defaultBoundary := "bridge-firewall"
	if hasLine(existOpts, "--network-host") {
		defaultBoundary = "host"
	} else if hasLine(existFirewall, "--no-firewall") {
		defaultBoundary = "bridge-open"
	}

	boundary := defaultBoundary
	if err := huh.NewSelect[string]().
		Title("Network boundary").
		Options(
			huh.NewOption("Bridge + firewall allowlist (recommended)", "bridge-firewall"),
			huh.NewOption("Bridge + unrestricted outbound HTTP(S)", "bridge-open"),
			huh.NewOption("Host network (least isolated, for local/VPN services)", "host"),
		).
		Value(&boundary).
		Run(); err != nil {
		return nil, nil, err
	}
	fmt.Fprintln(os.Stderr)

	switch boundary {
	case "bridge-open":
		return []string{"--no-firewall"}, nil, nil
	case "host":
		return []string{"--network-host", "--no-firewall"}, nil, nil
	}

	firewallLines, err := wizardFirewallMode(existFirewall)
	if err != nil {
		return nil, nil, err
	}
	extraDomains, err := wizardFirewallExtraDomains(existExtraDomains)
	if err != nil {
		return nil, nil, err
	}
	return firewallLines, extraDomains, nil
}

func wizardFirewallMode(existFirewall []string) ([]string, error) {
	mode := existingFirewallMode(existFirewall)
	if err := huh.NewSelect[string]().
		Title("Firewall allowlist").
		Options(
			huh.NewOption("Strict (default) - git, registries, package managers only", "strict"),
			huh.NewOption("Developer-friendly - adds cloud APIs, apt, CDN", "dev"),
			huh.NewOption("Custom file - provide your own whitelist", "custom"),
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
		path := existingCustomFirewallPath(existFirewall)
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
	default:
		return nil, nil
	}
}

func wizardFirewallExtraDomains(existing []string) ([]string, error) {
	value := strings.Join(existing, ", ")
	if err := huh.NewInput().
		Title("Additional allowed domains (comma-separated, optional)").
		Placeholder("*.apps.example.test, api.example.com").
		Value(&value).
		Run(); err != nil {
		return nil, err
	}
	return normalizeNetworkDomains(parsePolicyList(value)), nil
}

// ---------------------------------------------------------------------------
// Step N: Options
// ---------------------------------------------------------------------------

func wizardOptions(editMode bool, existOpts []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Options"))

	if editMode {
		displayCurrentSetup(displayOptionSetupLines(existOpts), "")

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

	yolo := !optSet["--no-yolo"]
	if err := huh.NewConfirm().
		Title("YOLO mode (skip permission prompts)? (default: yes, --no-yolo to disable)").
		Value(&yolo).
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
	if !yolo {
		lines = append(lines, "--no-yolo")
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
		case strings.HasPrefix(line, "--dir ") || strings.HasPrefix(line, "--dir-ro "):
			dirs = append(dirs, line)
		case strings.HasPrefix(line, "--provider "):
			providers = append(providers, line)
		case line == "--yolo" || line == "--no-yolo" || line == "--network-host" || line == "--worktree":
			opts = append(opts, line)
		case line == "--worker" || line == "--planner": // legacy, ignored //legacy-delete-after:2026-04-21
			continue
		case line == "--firewall-dev" || line == "--no-firewall" || strings.HasPrefix(line, "--firewall "):
			firewall = append(firewall, line)
		default:
			exts = append(exts, line)
		}
	}
	return
}

func loadWizardExtraDomains(workspace string, extensions []*registry.Extension) []string {
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil || policy == nil || source != PolicySourceV2 {
		return nil
	}
	return append([]string(nil), policy.Network.ExtraDomains...)
}

func existingNetworkLines(firewall, opts, extraDomains []string) []string {
	lines := appendNetworkLines(firewall, opts)
	for _, domain := range extraDomains {
		lines = append(lines, "network.extra_domain "+domain)
	}
	return lines
}

func appendNetworkLines(firewall, opts []string) []string {
	var lines []string
	if hasLine(opts, "--network-host") {
		lines = append(lines, "--network-host")
	}
	lines = append(lines, firewall...)
	return lines
}

func hasLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func existingFirewallMode(lines []string) string {
	for _, line := range lines {
		switch {
		case line == "--firewall-dev":
			return "dev"
		case strings.HasPrefix(line, "--firewall "):
			return "custom"
		}
	}
	return "strict"
}

func existingCustomFirewallPath(lines []string) string {
	for _, line := range lines {
		if strings.HasPrefix(line, "--firewall ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "--firewall "))
		}
	}
	return ""
}

func displayOptionSetupLines(lines []string) []string {
	optSet := make(map[string]bool, len(lines))
	for _, line := range lines {
		optSet[line] = true
	}
	out := []string{"option.yolo enabled"}
	if optSet["--no-yolo"] {
		out[0] = "option.yolo disabled"
	}
	if optSet["--worktree"] {
		out = append(out, "option.worktree enabled")
	} else {
		out = append(out, "option.worktree disabled")
	}
	return out
}

func displayCurrentSetup(lines []string, empty string) {
	fmt.Fprintln(os.Stderr, "\nCurrent setup:")
	if len(lines) == 0 {
		fmt.Fprintln(os.Stderr, "  "+empty)
		fmt.Fprintln(os.Stderr)
		return
	}
	for _, line := range lines {
		fmt.Fprintln(os.Stderr, "  "+formatCurrentSetupLine(line))
	}
	fmt.Fprintln(os.Stderr)
}

func formatCurrentSetupLine(line string) string {
	switch {
	case strings.HasPrefix(line, "--provider "):
		return "Provider: " + strings.TrimSpace(strings.TrimPrefix(line, "--provider "))
	case strings.HasPrefix(line, "--dir-ro "):
		return "Extra directory: " + strings.TrimSpace(strings.TrimPrefix(line, "--dir-ro ")) + " (read-only)"
	case strings.HasPrefix(line, "--dir "):
		return "Extra directory: " + strings.TrimSpace(strings.TrimPrefix(line, "--dir ")) + " (read/write)"
	case line == "--firewall-dev":
		return "Firewall: dev"
	case line == "--no-firewall":
		return "Firewall: disabled"
	case strings.HasPrefix(line, "--firewall "):
		return "Firewall: custom file " + strings.TrimSpace(strings.TrimPrefix(line, "--firewall "))
	case line == "--no-yolo":
		return "YOLO mode: disabled"
	case line == "--yolo":
		return "YOLO mode: enabled"
	case line == "--network-host":
		return "Network: host"
	case line == "--worktree":
		return "Parallel isolation: git worktree"
	case strings.HasPrefix(line, "network.extra_domain "):
		return "Allowed domain: " + strings.TrimSpace(strings.TrimPrefix(line, "network.extra_domain "))
	case line == "option.yolo enabled":
		return "YOLO mode: enabled"
	case line == "option.yolo disabled":
		return "YOLO mode: disabled"
	case line == "option.worktree enabled":
		return "Parallel isolation: git worktree"
	case line == "option.worktree disabled":
		return "Parallel isolation: disabled"
	case strings.HasPrefix(line, "--"):
		name, value, _ := strings.Cut(strings.TrimPrefix(line, "--"), " ")
		name = strings.ReplaceAll(name, "-", " ")
		if strings.TrimSpace(value) == "" {
			return titleLabel(name) + ": enabled"
		}
		return titleLabel(name) + ": " + strings.TrimSpace(value)
	default:
		return line
	}
}

func titleLabel(value string) string {
	parts := strings.Fields(value)
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func loadWizardExistingConfig(workspace string, extensions []*registry.Extension) ([]string, PolicySource, error) {
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil {
		return nil, PolicySourceNone, err
	}
	if policy == nil {
		return nil, PolicySourceNone, nil
	}
	if source == PolicySourceLegacy {
		lines, err := readConfigLines(projectConfigPath(workspace))
		if err != nil {
			return nil, PolicySourceNone, err
		}
		return lines, source, nil
	}
	return legacyArgsToConfigLines(policy.ToLegacyFlags()), source, nil
}

func displayWizardExistingConfig(workspace string, source PolicySource, lines []string, extensions []*registry.Extension) {
	switch source {
	case PolicySourceV2:
		fmt.Fprintf(os.Stderr, "Existing policy: %s\n\n", projectPolicyPath(workspace))
		if policy, _, err := LoadProjectPolicy(workspace, extensions); err == nil && policy != nil {
			fmt.Fprint(os.Stderr, wizardDim.Render(launchSummaryFromPolicy(policy, workspace).Render()))
		}
	case PolicySourceLegacy:
		fmt.Fprintf(os.Stderr, "Existing legacy config: %s\n", projectConfigPath(workspace))
		fmt.Fprintf(os.Stderr, "%s\n\n", wizardDim.Render("This will be saved as policy.yaml when you finish setup."))
		for _, line := range lines {
			fmt.Fprintln(os.Stderr, wizardDim.Render("  "+line))
		}
	default:
		return
	}
	fmt.Fprintln(os.Stderr)
}

// gracefulAbort handles huh interrupt errors (Ctrl+C) cleanly.
func gracefulAbort(err error) error {
	if err == huh.ErrUserAborted {
		fmt.Fprintln(os.Stderr, "\nCancelled.")
		return nil
	}
	return err
}
