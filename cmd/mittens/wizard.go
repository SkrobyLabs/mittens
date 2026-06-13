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
	var existProviderConfig ProviderWizardConfig
	var existProviderState ProviderWizardState

	if len(existing) > 0 {
		displayWizardExistingConfig(workspace, source, existing, extensions)
		existExtraDomains = loadWizardExtraDomains(workspace, extensions)
		existProviderConfig = loadWizardProviderConfig(workspace, extensions)

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
			existProviderState = loadWizardProviderState(workspace, extensions, existProviders, existProviderConfig)
		}
		fmt.Fprintln(os.Stderr)
	}

	// ── Step 1: Provider ───────────────────────────────────────────────────
	providerLines, providerConfig, err := wizardProvider(workspace, editMode, existProviderState)
	if err != nil {
		return gracefulAbort(err)
	}

	// ── Step 2: Extra directories ──────────────────────────────────────────
	dirLines, err := wizardDirs(workspace, editMode, existDirs)
	if err != nil {
		return gracefulAbort(err)
	}

	// ── Step 4+5: Extensions ───────────────────────────────────────────────
	extLines, err := wizardExtensions(extensions, editMode, existExts)
	if err != nil {
		return gracefulAbort(err)
	}

	// ── Step 4: Network boundary ───────────────────────────────────────────
	networkLines, extraDomains, err := wizardNetworkBoundary(workspace, editMode, existFirewall, existOpts, existExtraDomains)
	if err != nil {
		return gracefulAbort(err)
	}

	// ── Step N: Options ────────────────────────────────────────────────────
	optLines, err := wizardOptions(editMode, existOpts)
	if err != nil {
		return gracefulAbort(err)
	}

	// ── Write structured project policy ────────────────────────────────────
	assembly := WizardAssemblyInput{
		ProviderLines:  providerLines,
		ProviderConfig: providerConfig,
		DirLines:       dirLines,
		ExtensionLines: extLines,
		NetworkLines:   networkLines,
		OptionLines:    optLines,
		ExtraDomains:   extraDomains,
	}
	policy, configLines, err := assembleWizardPolicy(assembly, extensions)
	if err != nil {
		return fmt.Errorf("building policy: %w", err)
	}
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
	var existProviderConfig ProviderWizardConfig
	var existProviderState ProviderWizardState

	if len(existing) > 0 {
		editMode = true
		existDirs, existProviders, existExts, existFirewall, existOpts = parseExistingConfig(existing)
		existExtraDomains = loadWizardExtraDomains(workspace, extensions)
		existProviderConfig = loadWizardProviderConfig(workspace, extensions)
		existProviderState = loadWizardProviderState(workspace, extensions, existProviders, existProviderConfig)
		displayWizardExistingConfig(workspace, source, existing, extensions)
	}

	providerLines, _, err := wizardProvider(workspace, editMode, existProviderState)
	if err != nil {
		return nil, nil, err
	}

	dirLines, err := wizardDirs(workspace, editMode, existDirs)
	if err != nil {
		return nil, nil, err
	}

	extLines, err := wizardExtensions(extensions, editMode, existExts)
	if err != nil {
		return nil, nil, err
	}

	// Session runs are ephemeral; pass an empty workspace to skip arming a
	// one-time learn pass for a "next run" that won't share this config.
	networkLines, extraDomains, err := wizardNetworkBoundary("", editMode, existFirewall, existOpts, existExtraDomains)
	if err != nil {
		return nil, nil, err
	}

	optLines, err := wizardOptions(editMode, existOpts)
	if err != nil {
		return nil, nil, err
	}
	configLines := wizardEquivalentLines(WizardAssemblyInput{
		ProviderLines:  providerLines,
		DirLines:       dirLines,
		ExtensionLines: extLines,
		NetworkLines:   networkLines,
		OptionLines:    optLines,
	})

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
	existingMounts := mountsFromDirLines(existDirs)
	currentMounts := existingMounts
	showMenu := editMode

	for {
		if showMenu {
			displayCurrentSetup(dirLinesFromMounts(currentMounts), "(no extra directories)")

			actionOptions := []huh.Option[string]{
				huh.NewOption("Change", "change"),
				huh.NewOption("Done", "done"),
			}
			if editMode {
				actionOptions = []huh.Option[string]{
					huh.NewOption("Keep", "keep"),
					huh.NewOption("Change", "change"),
				}
			}

			var action string
			if err := huh.NewSelect[string]().
				Title("Extra directories").
				Options(actionOptions...).
				Value(&action).
				Run(); err != nil {
				return nil, err
			}
			switch action {
			case "keep":
				fmt.Fprintln(os.Stderr)
				return dirLinesFromMounts(existingMounts), nil
			case "done":
				fmt.Fprintln(os.Stderr)
				return dirLinesFromMounts(currentMounts), nil
			}
		}

		// Build a map of existing dir paths for pre-selection in edit mode.
		existPathSet := mountPreselection(currentMounts)

		// Interactive directory browser starting at the workspace's parent.
		parentDir := filepath.Dir(workspace)
		fmt.Fprintln(os.Stderr)
		chosen, err := runDirPicker(parentDir, existPathSet, workspace)
		if err == errPickerCancelled {
			showMenu = true
			continue
		}
		if err != nil {
			return nil, err
		}

		fmt.Fprintln(os.Stderr)
		return dirLinesFromMounts(mountsFromDirSelections(chosen)), nil
	}
}

func mountsFromDirLines(lines []string) []PolicyMount {
	var mounts []PolicyMount
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--dir-ro "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "--dir-ro "))
			if path != "" {
				mounts = append(mounts, PolicyMount{Path: path, Access: "ro"})
			}
		case strings.HasPrefix(line, "--dir "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "--dir "))
			if path != "" {
				mounts = append(mounts, PolicyMount{Path: path, Access: "rw"})
			}
		}
	}
	return mounts
}

func dirLinesFromMounts(mounts []PolicyMount) []string {
	var lines []string
	for _, mount := range mounts {
		path := strings.TrimSpace(mount.Path)
		if path == "" {
			continue
		}
		if mount.Access == "ro" {
			lines = append(lines, "--dir-ro "+path)
		} else {
			lines = append(lines, "--dir "+path)
		}
	}
	return lines
}

func mountPreselection(mounts []PolicyMount) map[string]bool {
	preselected := make(map[string]bool, len(mounts))
	for _, mount := range mounts {
		path := strings.TrimSpace(mount.Path)
		if path == "" {
			continue
		}
		preselected[path] = mount.Access == "ro"
	}
	return preselected
}

func mountsFromDirSelections(selections []dirMountSelection) []PolicyMount {
	var mounts []PolicyMount
	for _, selection := range selections {
		path := strings.TrimSpace(selection.Path)
		if path == "" {
			continue
		}
		access := "rw"
		if selection.ReadOnly {
			access = "ro"
		}
		mounts = append(mounts, PolicyMount{Path: path, Access: access})
	}
	return mounts
}

// ---------------------------------------------------------------------------
// Step 1: Provider
// ---------------------------------------------------------------------------

type ProviderWizardConfig struct {
	Backend  string
	Endpoint string
	Model    string
}

type ProviderWizardState struct {
	Selected []string
	Default  string
	Config   ProviderWizardConfig
}

func wizardProvider(workspace string, editMode bool, existing ProviderWizardState) ([]string, ProviderWizardConfig, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 1: Provider"))
	state := normalizeProviderWizardState(existing)

	if editMode {
		displayCurrentSetup(providerSetupLinesFromState(state), "Provider: claude (default)")

		var action string
		if err := huh.NewSelect[string]().
			Title("Provider").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, ProviderWizardConfig{}, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return state.ProviderLines(), state.Config, nil
		}
	}

	selectedSet := mapFromValues(state.Selected)
	selected := append([]string(nil), state.Selected...)
	providerOptions := []struct {
		name        string
		label       string
		description string
	}{
		{name: "claude", label: "Claude", description: "Anthropic Claude Code CLI"},
		{name: "codex", label: "Codex", description: "OpenAI Codex CLI"},
		{name: "gemini", label: "Gemini", description: "Google Gemini CLI"},
		{name: "ollama", label: "Ollama", description: "local Ollama via Codex harness"},
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
		return nil, ProviderWizardConfig{}, err
	}

	defaultChoice := state.Default
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
			case "ollama":
				label = "Ollama"
			}
			defaultOpts = append(defaultOpts, huh.NewOption(label, p))
		}
		if err := huh.NewSelect[string]().
			Title("Pick default provider").
			Options(defaultOpts...).
			Value(&defaultChoice).
			Run(); err != nil {
			return nil, ProviderWizardConfig{}, err
		}
	}

	config := ProviderWizardConfig{}
	existingConfig := ProviderWizardConfig{}
	if state.Default == defaultChoice {
		existingConfig = state.Config
	}
	switch defaultChoice {
	case "claude":
		var cfgErr error
		config, cfgErr = wizardClaudeProviderConfig(existingConfig)
		if cfgErr != nil {
			return nil, ProviderWizardConfig{}, cfgErr
		}
	case "ollama":
		var cfgErr error
		config, cfgErr = wizardOllamaProviderConfig(existingConfig)
		if cfgErr != nil {
			return nil, ProviderWizardConfig{}, cfgErr
		}
	}

	state = normalizeProviderWizardState(ProviderWizardState{
		Selected: selected,
		Default:  defaultChoice,
		Config:   config,
	})

	if err := maybeWizardCodexTrustProject(workspace, state.ProviderLines()); err != nil {
		return nil, ProviderWizardConfig{}, err
	}

	fmt.Fprintln(os.Stderr)
	return state.ProviderLines(), state.Config, nil
}

func providerLinesUseCodexHarness(lines []string) bool {
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "--provider codex", "--provider ollama":
			return true
		}
	}
	return false
}

func maybeWizardCodexTrustProject(workspace string, providerLines []string) error {
	if !providerLinesUseCodexHarness(providerLines) {
		return nil
	}
	return wizardCodexTrustProject(workspace)
}

func wizardCodexTrustProject(workspace string) error {
	trust := true
	if err := huh.NewConfirm().
		Title("Trust this project in Codex config?").
		Description("This skips Codex's startup trust prompt for this workspace when using Codex or Ollama.").
		Value(&trust).
		Run(); err != nil {
		return err
	}
	if !trust {
		fmt.Fprintln(os.Stderr)
		return nil
	}
	configPath, err := trustCodexProject(workspace)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Codex project trust saved to: "+configPath))
	fmt.Fprintln(os.Stderr)
	return nil
}

func wizardOllamaProviderConfig(existing ProviderWizardConfig) (ProviderWizardConfig, error) {
	cfg := existing
	if cfg.Endpoint == "" {
		cfg.Endpoint = ollamaHostURL()
	}
	if cfg.Model == "" {
		cfg.Model = detectOllamaModel()
	}

	endpoint := cfg.Endpoint
	model := cfg.Model
	if err := huh.NewInput().
		Title("Ollama endpoint").
		Description("Use host.docker.internal for Ollama running on this Mac, or a LAN host/IP for a remote server.").
		Placeholder("http://host.docker.internal:11434").
		Value(&endpoint).
		Run(); err != nil {
		return ProviderWizardConfig{}, err
	}
	if err := huh.NewInput().
		Title("Ollama model").
		Placeholder("qwen3-coder:30b").
		Value(&model).
		Run(); err != nil {
		return ProviderWizardConfig{}, err
	}
	cfg.Endpoint = normalizeOllamaURL(endpoint)
	cfg.Model = strings.TrimSpace(model)
	return cfg, nil
}

func wizardClaudeProviderConfig(existing ProviderWizardConfig) (ProviderWizardConfig, error) {
	cfg := ProviderWizardConfig{
		Backend: canonicalProviderBackend(existing.Backend),
	}
	if cfg.Backend == "" {
		cfg.Backend = "claude"
	}
	if cfg.Backend != "openai" {
		cfg.Backend = "claude"
	}
	if existing.Endpoint != "" {
		cfg.Endpoint = existing.Endpoint
	}
	if existing.Model != "" {
		cfg.Model = existing.Model
	}

	backend := cfg.Backend
	if err := huh.NewSelect[string]().
		Title("Claude backend").
		Options(
			huh.NewOption("Claude (Anthropic)", "claude"),
			huh.NewOption("OpenAI via Anthropic-compatible proxy", "openai"),
		).
		Value(&backend).
		Run(); err != nil {
		return ProviderWizardConfig{}, err
	}
	cfg.Backend = backend
	if backend != "openai" {
		cfg.Endpoint = ""
		cfg.Model = ""
		return cfg, nil
	}

	proxyMode := "managed"
	if cfg.Endpoint != "" {
		proxyMode = "external"
	}
	if err := huh.NewSelect[string]().
		Title("OpenAI proxy").
		Options(
			huh.NewOption("Managed inside the Mittens container", "managed"),
			huh.NewOption("External/custom endpoint", "external"),
		).
		Value(&proxyMode).
		Run(); err != nil {
		return ProviderWizardConfig{}, err
	}
	model := cfg.Model
	if proxyMode == "external" {
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = normalizeClaudeOpenAIProxyURL("")
		}
		if err := huh.NewInput().
			Title("OpenAI proxy endpoint").
			Description("Must expose Anthropic Messages API for Claude Code; the proxy itself talks to OpenAI.").
			Placeholder("http://host.docker.internal:9223").
			Value(&endpoint).
			Run(); err != nil {
			return ProviderWizardConfig{}, err
		}
		cfg.Endpoint = normalizeClaudeOpenAIProxyURL(endpoint)
	} else {
		cfg.Endpoint = ""
	}
	if err := huh.NewInput().
		Title("Claude model alias").
		Description("Optional Claude-facing alias. Managed proxy maps opus to gpt-5.5 medium, sonnet to gpt-5.5 low, fable to xhigh, and haiku to a fast mini route.").
		Placeholder("opus").
		Value(&model).
		Run(); err != nil {
		return ProviderWizardConfig{}, err
	}
	cfg.Model = strings.TrimSpace(model)
	return cfg, nil
}

func providerSetupLines(providerLines []string, cfg ProviderWizardConfig) []string {
	lines := append([]string(nil), providerLines...)
	if cfg.Backend != "" && cfg.Backend != "claude" {
		lines = append(lines, "provider.backend "+cfg.Backend)
	}
	if cfg.Endpoint != "" {
		lines = append(lines, "provider.endpoint "+cfg.Endpoint)
	}
	if cfg.Model != "" {
		lines = append(lines, "provider.model "+cfg.Model)
	}
	return lines
}

func providerSetupLinesFromState(state ProviderWizardState) []string {
	state = normalizeProviderWizardState(state)
	return providerSetupLines(state.ProviderLines(), state.Config)
}

func providerWizardStateFromPolicy(policy ProviderPolicy) ProviderWizardState {
	name := strings.TrimSpace(policy.Name)
	if name == "" {
		name = "claude"
	}
	return normalizeProviderWizardState(ProviderWizardState{
		Selected: []string{name},
		Default:  name,
		Config: ProviderWizardConfig{
			Backend:  policy.Backend,
			Endpoint: policy.Endpoint,
			Model:    policy.Model,
		},
	})
}

func providerWizardStateFromLines(lines []string, cfg ProviderWizardConfig) ProviderWizardState {
	selectedSet, defaultProvider := parseProviderLines(lines)
	var selected []string
	for _, provider := range providerNames() {
		if selectedSet[provider] {
			selected = append(selected, provider)
		}
	}
	return normalizeProviderWizardState(ProviderWizardState{
		Selected: selected,
		Default:  defaultProvider,
		Config:   cfg,
	})
}

func normalizeProviderWizardState(state ProviderWizardState) ProviderWizardState {
	selected := make([]string, 0, len(state.Selected))
	seen := map[string]bool{}
	for _, provider := range state.Selected {
		provider = strings.TrimSpace(provider)
		if !isWizardProvider(provider) || seen[provider] {
			continue
		}
		selected = append(selected, provider)
		seen[provider] = true
	}
	defaultProvider := strings.TrimSpace(state.Default)
	if !isWizardProvider(defaultProvider) {
		defaultProvider = ""
	}
	if defaultProvider == "" {
		defaultProvider = "claude"
	}
	if !seen[defaultProvider] {
		selected = append(selected, defaultProvider)
		seen[defaultProvider] = true
	}
	if len(selected) == 0 {
		selected = []string{"claude"}
		defaultProvider = "claude"
	}
	if !seen[defaultProvider] {
		defaultProvider = selected[0]
	}
	return ProviderWizardState{
		Selected: selected,
		Default:  defaultProvider,
		Config:   state.Config,
	}
}

func (state ProviderWizardState) ProviderLines() []string {
	state = normalizeProviderWizardState(state)
	var lines []string
	for _, provider := range state.Selected {
		if provider == state.Default {
			continue
		}
		lines = append(lines, "--provider "+provider)
	}
	lines = append(lines, "--provider "+state.Default)
	return lines
}

func providerNames() []string {
	return []string{"claude", "codex", "gemini", "ollama"}
}

func isWizardProvider(provider string) bool {
	for _, known := range providerNames() {
		if provider == known {
			return true
		}
	}
	return false
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
		case "claude", "codex", "gemini", "ollama":
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

	available := wizardAvailableExtensions(extensions)
	if len(available) == 0 {
		fmt.Fprintln(os.Stderr, "  No extensions available.")
		fmt.Fprintln(os.Stderr)
		return nil, nil
	}

	lines := append([]string(nil), existExts...)

	if editMode {
		displayCurrentSetup(lines, "(no extensions)")

		var action string
		if err := huh.NewSelect[string]().
			Title("Extensions").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Edit", "edit"),
			).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return lines, nil
		}
	}

	return editExtensionLines(available, lines)
}

func wizardAvailableExtensions(extensions []*registry.Extension) []*registry.Extension {
	var available []*registry.Extension
	for _, ext := range extensions {
		if wizardExcluded[ext.Name] {
			continue
		}
		available = append(available, ext)
	}
	return available
}

func editExtensionLines(available []*registry.Extension, lines []string) ([]string, error) {
	for {
		displayCurrentSetup(lines, "(no extensions)")

		actionOptions := []huh.Option[string]{
			huh.NewOption("Add/change extension", "upsert"),
		}
		if len(configuredExtensions(available, lines)) > 0 {
			actionOptions = append(actionOptions, huh.NewOption("Remove extension", "remove"))
		}
		actionOptions = append(actionOptions, huh.NewOption("Done", "done"))

		var action string
		if err := huh.NewSelect[string]().
			Title("Extensions").
			Options(actionOptions...).
			Value(&action).
			Run(); err != nil {
			return nil, err
		}

		switch action {
		case "done":
			fmt.Fprintln(os.Stderr)
			return lines, nil
		case "remove":
			ext, err := selectExtension("Remove extension", configuredExtensions(available, lines), nil)
			if err == errPickerCancelled {
				continue
			}
			if err != nil {
				return nil, err
			}
			lines = removeExtensionLines(ext, lines)
		case "upsert":
			ext, err := selectExtension("Add/change extension", available, configuredExtensionSet(available, lines))
			if err == errPickerCancelled {
				continue
			}
			if err != nil {
				return nil, err
			}
			existing := extensionLinesFor(ext, lines)
			replacement, err := configureSelectedExtension(ext, existing)
			if err != nil {
				return nil, err
			}
			lines = upsertExtensionLines(ext, lines, replacement)
		}
	}
}

func selectExtension(title string, extensions []*registry.Extension, selected map[string]bool) (*registry.Extension, error) {
	return runExtensionPicker(title, extensions, selected)
}

func configuredExtensions(available []*registry.Extension, lines []string) []*registry.Extension {
	var out []*registry.Extension
	for _, ext := range available {
		if len(extensionLinesFor(ext, lines)) > 0 {
			out = append(out, ext)
		}
	}
	return out
}

func configuredExtensionSet(available []*registry.Extension, lines []string) map[string]bool {
	set := make(map[string]bool)
	for _, ext := range configuredExtensions(available, lines) {
		set[ext.Name] = true
	}
	return set
}

func configureSelectedExtension(ext *registry.Extension, existing []string) ([]string, error) {
	if extNeedsCfg(ext) {
		return configureExtension(ext, existing)
	}
	flag := extPrimaryFlag(ext)
	if flag == "" {
		return nil, nil
	}
	return []string{flag}, nil
}

func extensionFlagSet(ext *registry.Extension) map[string]bool {
	set := make(map[string]bool, len(ext.Flags))
	for _, flag := range ext.Flags {
		set[flag.Name] = true
	}
	return set
}

func configLineFlag(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func extensionLinesFor(ext *registry.Extension, lines []string) []string {
	flags := extensionFlagSet(ext)
	var out []string
	for _, line := range lines {
		if flags[configLineFlag(line)] {
			out = append(out, line)
		}
	}
	return out
}

func removeExtensionLines(ext *registry.Extension, lines []string) []string {
	flags := extensionFlagSet(ext)
	var out []string
	for _, line := range lines {
		if !flags[configLineFlag(line)] {
			out = append(out, line)
		}
	}
	return out
}

func upsertExtensionLines(ext *registry.Extension, lines, replacement []string) []string {
	out := removeExtensionLines(ext, lines)
	out = append(out, replacement...)
	return out
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
var customConfigurers = map[string]func(*registry.Extension, []string) ([]string, error){
	"dotnet": func(_ *registry.Extension, existing []string) ([]string, error) { return configureDotnet(existing) },
	"aws": func(_ *registry.Extension, existing []string) ([]string, error) {
		return configureCloud("aws", "--aws", "--aws-all", "AWS credentials", "Select AWS profiles", existing)
	},
	"gcp": func(_ *registry.Extension, existing []string) ([]string, error) {
		return configureCloud("gcp", "--gcp", "--gcp-all", "GCP credentials", "Select GCP profiles", existing)
	},
	"azure": func(_ *registry.Extension, existing []string) ([]string, error) {
		return configureCloud("azure", "--azure", "--azure-all", "Azure credentials", "Select Azure profiles", existing)
	},
	"kubectl": func(_ *registry.Extension, existing []string) ([]string, error) {
		return configureCloud("kubectl", "--k8s", "", "Kubernetes contexts", "Select Kubernetes contexts", existing)
	},
	"mcp": func(_ *registry.Extension, existing []string) ([]string, error) {
		return configureCloud("mcp", "--mcp", "--mcp-all", "MCP server passthrough", "Select MCP servers", existing)
	},
}

// configureExtension runs the sub-configuration step for a single extension.
// Uses custom handlers where registered, otherwise auto-generates prompts from flag metadata.
func configureExtension(ext *registry.Extension, existing []string) ([]string, error) {
	if fn, ok := customConfigurers[ext.Name]; ok {
		return fn(ext, existing)
	}
	return configureExtensionGeneric(ext, existing)
}

// configureExtensionGeneric auto-generates wizard prompts from extension flag metadata.
func configureExtensionGeneric(ext *registry.Extension, existing []string) ([]string, error) {
	var lines []string
	for _, f := range ext.Flags {
		existingValue := existingFlagValue(existing, f.Name)
		switch f.Arg {
		case "enum":
			if f.Multi {
				vals := parsePolicyList(existingValue)
				existingSet := mapFromValues(vals)
				var opts []huh.Option[string]
				for _, v := range f.EnumValues {
					opts = append(opts, huh.NewOption(v, v).Selected(existingSet[v]))
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
				val := existingValue
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
			val := existingValue
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
			val := existingValue
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

func configureDotnet(existing []string) ([]string, error) {
	versions := existingDotnetVersions(existing)
	existingSet := mapFromValues(versions)
	if err := huh.NewMultiSelect[string]().
		Title(".NET SDK versions").
		Options(
			huh.NewOption("LTS (latest long-term support)", "lts").Selected(existingSet["lts"]),
			huh.NewOption(".NET 8", "8").Selected(existingSet["8"]),
			huh.NewOption(".NET 9", "9").Selected(existingSet["9"]),
			huh.NewOption(".NET 10", "10").Selected(existingSet["10"]),
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

func existingDotnetVersions(lines []string) []string {
	value := existingFlagValue(lines, "--dotnet")
	if value == "" {
		if hasLine(lines, "--dotnet") {
			return []string{"lts"}
		}
		return nil
	}
	return parsePolicyList(value)
}

// configureCloud handles extension configuration with a "Skip / Select / All"
// pattern. When allFlag is empty, the "All" option is omitted.
func configureCloud(name, flag, allFlag, title, selectTitle string, existing []string) ([]string, error) {
	action, selected := existingCloudConfig(existing, flag, allFlag)

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
	selectedSet := mapFromValues(selected)
	for _, item := range items {
		opts = append(opts, huh.NewOption(item.Label, item.Value).Selected(selectedSet[item.Value]))
	}

	chosen := append([]string(nil), selected...)
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

func existingCloudConfig(lines []string, flag, allFlag string) (string, []string) {
	if allFlag != "" && hasLine(lines, allFlag) {
		return "all", nil
	}
	value := existingFlagValue(lines, flag)
	if value == "" {
		return "select", nil
	}
	return "select", parsePolicyList(value)
}

func existingFlagValue(lines []string, flag string) string {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != flag {
			continue
		}
		if len(fields) == 1 {
			return ""
		}
		return strings.TrimSpace(strings.TrimPrefix(line, flag))
	}
	return ""
}

func mapFromValues(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// Step 4: Network boundary
// ---------------------------------------------------------------------------

func wizardNetworkBoundary(workspace string, editMode bool, existFirewall, existOpts, existExtraDomains []string) ([]string, []string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 4: Network boundary"))
	state := networkWizardStateFromLines(existFirewall, existOpts, existExtraDomains)

	if editMode {
		displayCurrentSetup(existingNetworkLinesFromState(state), "Network: bridge + strict firewall (default)")

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
			return networkLinesFromState(state), state.ExtraDomains, nil
		}
	}

	fmt.Fprintln(os.Stderr)

	boundary := boundaryModeFromNetworkState(state)
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
		state = NetworkWizardState{
			Network: NetworkPolicy{
				Mode:     "bridge",
				Firewall: "disabled",
			},
		}
		return networkLinesFromState(state), nil, nil
	case "host":
		state = NetworkWizardState{
			Network: NetworkPolicy{
				Mode:     "host",
				Firewall: "disabled",
			},
			Execution: ExecutionPolicy{
				NetworkHost: true,
			},
		}
		return networkLinesFromState(state), nil, nil
	}

	network, err := wizardFirewallMode(state.Network)
	if err != nil {
		return nil, nil, err
	}
	extraDomains, err := wizardFirewallExtraDomains(state.ExtraDomains)
	if err != nil {
		return nil, nil, err
	}
	state = NetworkWizardState{Network: network, ExtraDomains: extraDomains}

	// Offer a one-time discovery pass for enforcing modes, where predicting the
	// allowlist by hand is the usual friction. Arming writes a sentinel the next
	// launch consumes; it does not persist an unenforced mode into policy.yaml.
	if workspace != "" && (network.Firewall == "strict" || network.Firewall == "custom") {
		if err := wizardOfferLearnArm(workspace); err != nil {
			return nil, nil, err
		}
	}

	return networkLinesFromState(state), state.ExtraDomains, nil
}

// wizardOfferLearnArm asks whether to arm a one-time firewall-learn pass and
// writes the sentinel if so.
func wizardOfferLearnArm(workspace string) error {
	arm := false
	if err := huh.NewConfirm().
		Title("Discover required domains on your next run?").
		Description("Arms a one-time learn pass: the next run records domains used\noutside the allowlist and offers to add them, then reverts to\nenforcing. Run a representative build to populate the allowlist.").
		Affirmative("Arm").
		Negative("Skip").
		Value(&arm).
		Run(); err != nil {
		return err
	}
	if !arm {
		return nil
	}
	if err := armLearnPass(workspace); err != nil {
		return fmt.Errorf("arming firewall-learn pass: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Armed: your next mittens run will discover and offer to add required domains.")
	return nil
}

func wizardFirewallMode(existing NetworkPolicy) (NetworkPolicy, error) {
	mode := firewallModeFromNetworkPolicy(existing)
	if err := huh.NewSelect[string]().
		Title("Firewall allowlist").
		Options(
			huh.NewOption("Strict (default) - git, registries, package managers only", "strict"),
			huh.NewOption("Developer-friendly - adds cloud APIs, apt, CDN", "dev"),
			huh.NewOption("Custom file - provide your own whitelist", "custom"),
		).
		Value(&mode).
		Run(); err != nil {
		return NetworkPolicy{}, err
	}
	fmt.Fprintln(os.Stderr)

	switch mode {
	case "dev":
		return NetworkPolicy{Mode: "bridge", Firewall: "dev"}, nil
	case "custom":
		path := existing.CustomConfig
		if err := huh.NewInput().
			Title("Path to custom whitelist file").
			Placeholder("/path/to/firewall.conf").
			Value(&path).
			Run(); err != nil {
			return NetworkPolicy{}, err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			return NetworkPolicy{Mode: "bridge", Firewall: "strict"}, nil
		}
		return NetworkPolicy{Mode: "bridge", Firewall: "custom", CustomConfig: path}, nil
	default:
		return NetworkPolicy{Mode: "bridge", Firewall: "strict"}, nil
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
	state := optionWizardStateFromLines(existOpts)

	if editMode {
		displayCurrentSetup(displayOptionSetupLinesFromState(state), "")

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
			return optionLinesFromState(state), nil
		}
	}

	fmt.Fprintln(os.Stderr)

	yolo := boolValue(state.Execution.Yolo, true)
	if err := huh.NewConfirm().
		Title("YOLO mode (skip permission prompts)? (default: yes, --no-yolo to disable)").
		Value(&yolo).
		Run(); err != nil {
		return nil, err
	}

	worktree := state.Execution.Worktree
	if err := huh.NewConfirm().
		Title("Parallel agent isolation (git worktree)? (--worktree)").
		Value(&worktree).
		Run(); err != nil {
		return nil, err
	}

	state.Execution.Yolo = boolPtr(yolo)
	state.Execution.Worktree = worktree

	fmt.Fprintln(os.Stderr)
	return optionLinesFromState(state), nil
}

type NetworkWizardState struct {
	Network      NetworkPolicy
	Execution    ExecutionPolicy
	ExtraDomains []string
}

type OptionWizardState struct {
	Execution ExecutionPolicy
}

func networkWizardStateFromLines(firewall, opts, extraDomains []string) NetworkWizardState {
	state := NetworkWizardState{
		Network: NetworkPolicy{
			Mode:     "bridge",
			Firewall: "strict",
		},
		ExtraDomains: append([]string(nil), extraDomains...),
	}
	for _, line := range firewall {
		switch {
		case line == "--firewall-dev":
			state.Network.Firewall = "dev"
		case line == "--no-firewall":
			state.Network.Firewall = "disabled"
		case strings.HasPrefix(line, "--firewall "):
			state.Network.Firewall = "custom"
			state.Network.CustomConfig = strings.TrimSpace(strings.TrimPrefix(line, "--firewall "))
		}
	}
	if hasLine(opts, "--network-host") {
		state.Network.Mode = "host"
		state.Execution.NetworkHost = true
	}
	return state
}

func networkLinesFromState(state NetworkWizardState) []string {
	var lines []string
	if state.Network.Mode == "host" || state.Execution.NetworkHost {
		lines = append(lines, "--network-host")
	}
	switch state.Network.Firewall {
	case "disabled":
		lines = append(lines, "--no-firewall")
	case "dev":
		lines = append(lines, "--firewall-dev")
	case "custom":
		path := strings.TrimSpace(state.Network.CustomConfig)
		if path != "" {
			lines = append(lines, "--firewall "+path)
		}
	}
	return lines
}

func existingNetworkLinesFromState(state NetworkWizardState) []string {
	lines := networkLinesFromState(state)
	for _, domain := range state.ExtraDomains {
		lines = append(lines, "network.extra_domain "+domain)
	}
	return lines
}

func boundaryModeFromNetworkState(state NetworkWizardState) string {
	if state.Network.Mode == "host" || state.Execution.NetworkHost {
		return "host"
	}
	if state.Network.Firewall == "disabled" {
		return "bridge-open"
	}
	return "bridge-firewall"
}

func firewallModeFromNetworkPolicy(policy NetworkPolicy) string {
	switch policy.Firewall {
	case "dev":
		return "dev"
	case "custom":
		return "custom"
	default:
		return "strict"
	}
}

func optionWizardStateFromLines(lines []string) OptionWizardState {
	yolo := true
	state := OptionWizardState{Execution: ExecutionPolicy{Yolo: &yolo}}
	for _, line := range lines {
		switch line {
		case "--no-yolo":
			disabled := false
			state.Execution.Yolo = &disabled
		case "--yolo":
			enabled := true
			state.Execution.Yolo = &enabled
		case "--worktree":
			state.Execution.Worktree = true
		}
	}
	return state
}

func optionLinesFromState(state OptionWizardState) []string {
	var lines []string
	if !boolValue(state.Execution.Yolo, true) {
		lines = append(lines, "--no-yolo")
	}
	if state.Execution.Worktree {
		lines = append(lines, "--worktree")
	}
	return lines
}

func displayOptionSetupLinesFromState(state OptionWizardState) []string {
	yoloLine := "option.yolo enabled"
	if !boolValue(state.Execution.Yolo, true) {
		yoloLine = "option.yolo disabled"
	}
	worktreeLine := "option.worktree disabled"
	if state.Execution.Worktree {
		worktreeLine = "option.worktree enabled"
	}
	return []string{yoloLine, worktreeLine}
}

type WizardAssemblyInput struct {
	ProviderLines  []string
	ProviderConfig ProviderWizardConfig
	DirLines       []string
	ExtensionLines []string
	NetworkLines   []string
	OptionLines    []string
	ExtraDomains   []string
}

func assembleWizardPolicy(input WizardAssemblyInput, extensions []*registry.Extension) (*ProjectPolicy, []string, error) {
	lines := wizardEquivalentLines(input)
	policy, err := PolicyFromLegacyFlags(splitConfigFlags(lines), extensions)
	if err != nil {
		return nil, nil, err
	}
	if input.ProviderConfig.Backend != "claude" {
		policy.Provider.Backend = input.ProviderConfig.Backend
	}
	policy.Provider.Endpoint = input.ProviderConfig.Endpoint
	policy.Provider.Model = input.ProviderConfig.Model
	policy.Network.ExtraDomains = normalizeNetworkDomains(input.ExtraDomains)
	return policy, lines, nil
}

func wizardEquivalentLines(input WizardAssemblyInput) []string {
	var lines []string
	lines = append(lines, input.ProviderLines...)
	lines = append(lines, input.DirLines...)
	lines = append(lines, input.ExtensionLines...)
	lines = append(lines, input.NetworkLines...)
	lines = append(lines, input.OptionLines...)
	return lines
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

func loadWizardProviderConfig(workspace string, extensions []*registry.Extension) ProviderWizardConfig {
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err != nil || policy == nil || source != PolicySourceV2 {
		return ProviderWizardConfig{}
	}
	return ProviderWizardConfig{
		Backend:  policy.Provider.Backend,
		Endpoint: policy.Provider.Endpoint,
		Model:    policy.Provider.Model,
	}
}

func loadWizardProviderState(workspace string, extensions []*registry.Extension, providerLines []string, cfg ProviderWizardConfig) ProviderWizardState {
	policy, source, err := LoadProjectPolicy(workspace, extensions)
	if err == nil && policy != nil && source == PolicySourceV2 {
		return providerWizardStateFromPolicy(policy.Provider)
	}
	return providerWizardStateFromLines(providerLines, cfg)
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
	case strings.HasPrefix(line, "provider.endpoint "):
		return "Provider endpoint: " + strings.TrimSpace(strings.TrimPrefix(line, "provider.endpoint "))
	case strings.HasPrefix(line, "provider.model "):
		return "Provider model: " + strings.TrimSpace(strings.TrimPrefix(line, "provider.model "))
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
