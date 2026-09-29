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
var wizardExcluded = map[string]bool{"firewall": true, "mcp": true}

// ---------------------------------------------------------------------------
// Main entry point
// ---------------------------------------------------------------------------

// runWizard runs the interactive TUI setup wizard. The extensions parameter
// is the loaded extension list from the embedded YAML manifests (so the wizard
// knows which extensions are available).
func runWizard(extensions []*registry.Extension) error {
	return runWizardForProfile(extensions, detectWorkspace(), "default")
}

// runWizardForProfile uses the same full configuration workflow for default
// and named profiles. Named profiles never use policy.yaml as scratch state.
func runWizardForProfile(extensions []*registry.Extension, workspace, profileName string) error {
	return runProjectWizardEditor(extensions, workspace, profileName)
}

// runWizardProfile edits an independent named snapshot without touching the
// default project configuration.
func runWizardProfile(workspace, name string) error {
	if err := validateProfileName(name, true); err != nil {
		return err
	}
	exts, err := loadExtensions()
	if err != nil {
		return err
	}
	return runWizardForProfile(exts, workspace, name)
}

// ---------------------------------------------------------------------------
// Session mode (ephemeral config edit)
// ---------------------------------------------------------------------------

// wizardSession runs the wizard in edit mode but does NOT persist changes.
// It returns the complete selected policy for the caller to use ephemerally.
// Returns huh.ErrUserAborted on Ctrl+C (not nil) so the caller can
// distinguish cancellation from an empty-but-valid config.
func wizardSession(extensions []*registry.Extension, profileName string) (*ProjectPolicy, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("--session requires an interactive terminal")
	}

	workspace := detectWorkspace()
	if profileName != "default" {
		if _, err := LoadNamedProfile(workspace, profileName); err != nil {
			if !strings.Contains(err.Error(), "not found") {
				return nil, err
			}
			base, _, baseErr := effectivePolicyForShow(workspace, extensions)
			if baseErr != nil {
				return nil, baseErr
			}
			if _, migrateErr := MigrateLegacyProfiles(workspace, base); migrateErr != nil {
				return nil, migrateErr
			}
			if _, err := LoadNamedProfile(workspace, profileName); err != nil {
				return nil, err
			}
		}
	}

	base, err := wizardInitialPolicy(workspace, profileName, extensions)
	if err != nil {
		return nil, err
	}
	result, err := runWizardEditor(base, wizardEditorConfig{
		Workspace: workspace, Profile: profileName, Mode: "session", Extensions: extensions,
	})
	if err != nil {
		return nil, err
	}
	return result.Policy, nil
}

// preserveUntouchedWizardPolicy retains boundaries the interactive wizard does
// not expose. This is especially important for named snapshots: editing one
// section must not silently reset credentials, host integrations, extra
// provider arguments, or execution controls that were not visited.
func preserveUntouchedWizardPolicy(existing, assembled *ProjectPolicy) (*ProjectPolicy, error) {
	if existing == nil {
		return assembled, nil
	}
	merged, err := clonePolicy(existing)
	if err != nil {
		return nil, err
	}
	merged.Provider = assembled.Provider
	if merged.Provider.Name == existing.Provider.Name {
		merged.Provider.Effort = existing.Provider.Effort
	}
	// provider.profile is only a legacy compatibility input. New complete
	// snapshots must never retain an outward reference to the legacy store.
	merged.Provider.Profile = ""
	merged.Workspace = assembled.Workspace
	merged.Network = assembled.Network
	if merged.Network.SSHEgress == nil {
		merged.Network.SSHEgress = existing.Network.SSHEgress
	}
	merged.Capabilities = assembled.Capabilities
	merged.MCP = assembled.MCP
	// The wizard currently controls these execution settings. Preserve the
	// remaining execution boundary fields unless a dedicated wizard step edits
	// them in a future change.
	merged.Execution.Yolo = assembled.Execution.Yolo
	merged.Execution.Worktree = assembled.Execution.Worktree
	return merged, nil
}

// ---------------------------------------------------------------------------
// Step 0: User-wide defaults
// ---------------------------------------------------------------------------

// wizardUserDefaults uses the same draft editor as project setup and only
// persists the baseline after an explicit save.
func wizardUserDefaults() error {
	extensions, err := loadExtensions()
	if err != nil {
		return err
	}
	current, _, err := LoadUserDefaultsPolicy(extensions)
	if err != nil {
		return err
	}
	if current == nil {
		current = defaultProjectPolicy()
	}
	result, err := runWizardEditor(current, wizardEditorConfig{
		Workspace: homeDir(), Mode: "defaults", Extensions: extensions,
	})
	if err != nil {
		return gracefulAbort(err)
	}
	if err := SaveUserDefaultsPolicy(result.Policy); err != nil {
		return fmt.Errorf("saving user defaults: %w", err)
	}
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("User defaults saved to: "+UserDefaultsPolicyPath()))
	return nil
}

// userDefaultsSourcePath returns the on-disk path for the loaded defaults so the
// wizard reports where the current baseline lives (structured vs legacy).
func userDefaultsSourcePath(source PolicySource) string {
	if source == PolicySourceLegacy {
		return UserDefaultsPath()
	}
	return UserDefaultsPolicyPath()
}

// wizardImagePasteKey prompts for the WSL image paste keybinding, returning the
// equivalent option line(s). On non-WSL hosts meta+v is always correct, so it
// prompts nothing but preserves any existing non-default value.
func wizardImagePasteKey(existing string) ([]string, error) {
	if !isWSL() {
		if existing != "" && existing != "meta+v" {
			return []string{"--image-paste-key " + existing}, nil
		}
		return nil, nil
	}
	pasteKey := existing
	if pasteKey == "" {
		pasteKey = "meta+v"
	}
	if err := runWizardField(huh.NewSelect[string]().
		Title("Image paste keybinding").
		Description("meta+v = Alt+V (no terminal changes needed), ctrl+v = Ctrl+V (requires Windows Terminal rebind)").
		Options(
			huh.NewOption("Alt+V (meta+v) — default, no terminal changes", "meta+v"),
			huh.NewOption("Ctrl+V (ctrl+v) — needs Windows Terminal rebind", "ctrl+v"),
		).
		Value(&pasteKey)); err != nil {
		return nil, err
	}
	if pasteKey == "ctrl+v" {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  ⚠ Windows Terminal intercepts Ctrl+V for text paste.")
		fmt.Fprintln(os.Stderr, "    To use Ctrl+V for image paste inside mittens, rebind")
		fmt.Fprintln(os.Stderr, "    the paste action in Windows Terminal settings to another")
		fmt.Fprintln(os.Stderr, "    shortcut (e.g. Ctrl+Shift+V).")
		fmt.Fprintln(os.Stderr)
		return []string{"--image-paste-key ctrl+v"}, nil
	}
	return nil, nil
}

// wizardSeed holds the pre-selection state the interactive wizard steps consume,
// derived from a base policy — a project policy for edit mode, or the user
// defaults baseline for init-from-default pre-seeding.
type wizardSeed struct {
	dirs          []string
	exts          []string
	firewall      []string
	opts          []string
	extraDomains  []string
	mcpServers    []MCPServerPolicy
	mcpAll        bool
	providerState ProviderWizardState
}

// wizardSeedFromPolicy derives wizard pre-selection state from a base policy. It
// reuses the same legacy-line categorisation the edit path uses so seeding is
// identical whether the base is a project policy or the user defaults baseline.
func wizardSeedFromPolicy(policy *ProjectPolicy) wizardSeed {
	if policy == nil {
		return wizardSeed{}
	}
	lines := legacyArgsToConfigLines(policy.ToLegacyFlags())
	dirs, _, exts, firewall, opts := parseExistingConfig(lines)
	exts, _ = splitMCPLines(exts)
	return wizardSeed{
		dirs:          dirs,
		exts:          filterNonExtensionLines(exts),
		firewall:      firewall,
		opts:          opts,
		extraDomains:  append([]string(nil), policy.Network.ExtraDomains...),
		mcpServers:    append([]MCPServerPolicy(nil), policy.MCP.Servers...),
		mcpAll:        policy.MCP.All,
		providerState: providerWizardStateFromPolicy(policy.Provider),
	}
}

// filterNonExtensionLines drops option flags that parseExistingConfig lumps into
// the extension bucket (they are handled by dedicated steps, not the extension
// picker).
func filterNonExtensionLines(lines []string) []string {
	var out []string
	for _, line := range lines {
		switch configLineFlag(line) {
		case "--image-paste-key", "--name":
			continue
		}
		out = append(out, line)
	}
	return out
}

// defaultsSeed loads the user defaults baseline as wizard seed state for
// init-from-default (a new project, or "overwrite / start fresh"). editMode is
// true only when a non-empty baseline exists, so the wizard pre-selects without
// a spurious "existing configuration" prompt.
func defaultsSeed(extensions []*registry.Extension) (wizardSeed, bool) {
	policy, _, err := LoadUserDefaultsPolicy(extensions)
	if err != nil || policy == nil {
		return wizardSeed{}, false
	}
	return wizardSeedFromPolicy(policy), true
}

// wizardEditSeed builds seed state for editing an existing project policy. It
// reads structured MCP/provider details from the saved policy where available
// and categorises the rest from the existing config lines.
func wizardEditSeed(workspace string, extensions []*registry.Extension, existing []string) wizardSeed {
	dirs, providers, exts, firewall, opts := parseExistingConfig(existing)
	exts, _ = splitMCPLines(exts)
	mcpServers, mcpAll := loadWizardMCP(workspace, extensions)
	providerConfig := loadWizardProviderConfig(workspace, extensions)
	return wizardSeed{
		dirs:          dirs,
		exts:          exts,
		firewall:      firewall,
		opts:          opts,
		extraDomains:  loadWizardExtraDomains(workspace, extensions),
		mcpServers:    mcpServers,
		mcpAll:        mcpAll,
		providerState: loadWizardProviderState(workspace, extensions, providers, providerConfig),
	}
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
	if err := runWizardField(huh.NewInput().
		Title("Model").
		Placeholder("e.g. opus, haiku, sonnet").
		Value(&model)); err != nil {
		return gracefulAbort(err)
	}
	existing.Model = strings.TrimSpace(model)

	if effortEnabled(provider) {
		effort := existing.Effort
		if err := runWizardField(huh.NewSelect[string]().
			Title("Effort").
			Options(
				huh.NewOption("(none)", ""),
				huh.NewOption("low", "low"),
				huh.NewOption("medium", "medium"),
				huh.NewOption("high", "high"),
				huh.NewOption("max", "max"),
			).
			Value(&effort)); err != nil {
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
	fmt.Fprintln(os.Stderr, wizardSuccess.Render(fmt.Sprintf("Model preset %q saved (model=%s, effort=%s)", profileName, existing.Model, existing.Effort)))
	return nil
}

// ---------------------------------------------------------------------------
// Step 2: Directories
// ---------------------------------------------------------------------------

func wizardDirs(workspace string, editMode bool, existDirs []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Directories"))
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
				if len(currentMounts) > 0 {
					actionOptions = append(actionOptions, huh.NewOption("Remove", "remove"))
				}
			}

			var action string
			if err := runWizardField(huh.NewSelect[string]().
				Title("Extra directories").
				Options(actionOptions...).
				Value(&action)); err != nil {
				return nil, err
			}
			switch action {
			case "keep":
				fmt.Fprintln(os.Stderr)
				return dirLinesFromMounts(existingMounts), nil
			case "remove":
				remaining, err := wizardRemoveDirs(currentMounts)
				if err != nil {
					return nil, err
				}
				fmt.Fprintln(os.Stderr)
				return dirLinesFromMounts(remaining), nil
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
			if !editMode {
				return nil, err
			}
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

func wizardRemoveDirs(current []PolicyMount) ([]PolicyMount, error) {
	remainingPaths := make([]string, 0, len(current))
	options := make([]huh.Option[string], 0, len(current))
	for _, mount := range current {
		path := strings.TrimSpace(mount.Path)
		if path == "" {
			continue
		}
		remainingPaths = append(remainingPaths, path)
		options = append(options, huh.NewOption(path, path).Selected(true))
	}

	if err := runWizardField(huh.NewMultiSelect[string]().
		Title("Included extra directories").
		Description("Uncheck directories to remove them.").
		Options(options...).
		Value(&remainingPaths)); err != nil {
		return nil, err
	}

	return keepDirectoryMounts(current, remainingPaths), nil
}

func keepDirectoryMounts(current []PolicyMount, paths []string) []PolicyMount {
	keep := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		keep[strings.TrimSpace(path)] = struct{}{}
	}

	remaining := make([]PolicyMount, 0, len(current))
	for _, mount := range current {
		if _, ok := keep[strings.TrimSpace(mount.Path)]; ok {
			remaining = append(remaining, mount)
		}
	}
	return remaining
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
	fmt.Fprintln(os.Stderr, wizardBold.Render("Provider"))
	state := normalizeProviderWizardState(existing)

	if editMode {
		displayCurrentSetup(providerSetupLinesFromState(state), "Provider: claude (default)")

		var action string
		if err := runWizardField(huh.NewSelect[string]().
			Title("Provider").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action)); err != nil {
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

	if err := runWizardField(huh.NewMultiSelect[string]().
		Title("Select AI CLI providers to support").
		Options(opts...).
		Value(&selected)); err != nil {
		return nil, ProviderWizardConfig{}, err
	}
	selected = normalizeProviderSelection(selected, state.Default)

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
		if err := runWizardField(huh.NewSelect[string]().
			Title("Pick default provider").
			Options(defaultOpts...).
			Value(&defaultChoice)); err != nil {
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
	default:
		// Providers without a configuration subform still retain saved model
		// and endpoint settings when the provider selection is unchanged.
		config = existingConfig
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
	if workspace == "" || !providerLinesUseCodexHarness(providerLines) {
		return nil
	}
	return wizardCodexTrustProject(workspace)
}

func wizardCodexTrustProject(workspace string) error {
	trust := true
	if err := runWizardField(huh.NewConfirm().
		Title("Trust this project in Codex config?").
		Description("This skips Codex's startup trust prompt for this workspace when using Codex or Ollama.").
		Value(&trust)); err != nil {
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
	if err := runWizardField(huh.NewInput().
		Title("Ollama endpoint").
		Description("Use host.docker.internal for Ollama running on this Mac, or a LAN host/IP for a remote server.").
		Placeholder("http://host.docker.internal:11434").
		Value(&endpoint)); err != nil {
		return ProviderWizardConfig{}, err
	}
	if err := runWizardField(huh.NewInput().
		Title("Ollama model").
		Placeholder("qwen3-coder:30b").
		Value(&model)); err != nil {
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
	if err := runWizardField(huh.NewSelect[string]().
		Title("Claude backend").
		Options(
			huh.NewOption("Claude (Anthropic)", "claude"),
			huh.NewOption("OpenAI via Anthropic-compatible proxy", "openai"),
		).
		Value(&backend)); err != nil {
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
	if err := runWizardField(huh.NewSelect[string]().
		Title("OpenAI proxy").
		Options(
			huh.NewOption("Managed inside the Mittens container", "managed"),
			huh.NewOption("External/custom endpoint", "external"),
		).
		Value(&proxyMode)); err != nil {
		return ProviderWizardConfig{}, err
	}
	model := cfg.Model
	if proxyMode == "external" {
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = normalizeClaudeOpenAIProxyURL("")
		}
		if err := runWizardField(huh.NewInput().
			Title("OpenAI proxy endpoint").
			Description("Must expose Anthropic Messages API for Claude Code; the proxy itself talks to OpenAI.").
			Placeholder("http://host.docker.internal:9223").
			Value(&endpoint)); err != nil {
			return ProviderWizardConfig{}, err
		}
		cfg.Endpoint = normalizeClaudeOpenAIProxyURL(endpoint)
	} else {
		cfg.Endpoint = ""
	}
	if err := runWizardField(huh.NewInput().
		Title("Claude model alias").
		Description("Optional Claude-facing alias. Managed proxy maps fable to gpt-6-astra medium, opus to gpt-5.6-sol high, sonnet to gpt-5.6-terra medium, and haiku to gpt-5.6-luna low.").
		Placeholder("opus").
		Value(&model)); err != nil {
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

func normalizeProviderSelection(selected []string, fallbackDefault string) []string {
	out := make([]string, 0, len(selected))
	seen := map[string]bool{}
	for _, provider := range selected {
		provider = strings.TrimSpace(provider)
		if !isWizardProvider(provider) || seen[provider] {
			continue
		}
		out = append(out, provider)
		seen[provider] = true
	}
	if len(out) > 0 {
		return out
	}
	fallbackDefault = strings.TrimSpace(fallbackDefault)
	if isWizardProvider(fallbackDefault) {
		return []string{fallbackDefault}
	}
	return []string{"claude"}
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
// Step 3: Extensions
// ---------------------------------------------------------------------------

func wizardExtensions(extensions []*registry.Extension, editMode bool, existExts []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Extensions"))

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
		if err := runWizardField(huh.NewSelect[string]().
			Title("Extensions").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Edit", "edit"),
			).
			Value(&action)); err != nil {
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
		if err := runWizardField(huh.NewSelect[string]().
			Title("Extensions").
			Options(actionOptions...).
			Value(&action)); err != nil {
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
		return configureCloud("mcp", "--mcp", "--mcp-all", "MCP server passthrough (experimental)", "Select MCP servers", existing)
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
				if err := runWizardField(huh.NewMultiSelect[string]().
					Title(ext.Description).
					Options(opts...).
					Value(&vals)); err != nil {
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
				if err := runWizardField(huh.NewSelect[string]().
					Title(ext.Description).
					Options(opts...).
					Value(&val)); err != nil {
					return nil, err
				}
				lines = append(lines, f.Name+" "+val)
			}
		case "csv":
			val := existingValue
			if err := runWizardField(huh.NewInput().
				Title(ext.Description + " (comma-separated)").
				Value(&val)); err != nil {
				return nil, err
			}
			val = strings.TrimSpace(val)
			if val != "" {
				lines = append(lines, f.Name+" "+val)
			}
		case "path":
			val := existingValue
			if err := runWizardField(huh.NewInput().
				Title(ext.Description + " (path)").
				Value(&val)); err != nil {
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
	if err := runWizardField(huh.NewMultiSelect[string]().
		Title(".NET SDK versions").
		Options(
			huh.NewOption("LTS (latest long-term support)", "lts").Selected(existingSet["lts"]),
			huh.NewOption(".NET 8", "8").Selected(existingSet["8"]),
			huh.NewOption(".NET 9", "9").Selected(existingSet["9"]),
			huh.NewOption(".NET 10", "10").Selected(existingSet["10"]),
		).
		Value(&versions)); err != nil {
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
	skipLabel := "Skip"
	if len(existing) > 0 {
		skipLabel = "Remove from project"
	}
	selectOpts = append(selectOpts, huh.NewOption(skipLabel, "skip"))

	if err := runWizardField(huh.NewSelect[string]().
		Title(title).
		Options(selectOpts...).
		Value(&action)); err != nil {
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
		fmt.Fprintf(os.Stderr, "  Cannot list %s: no local resolver. Keeping the current selection.\n", name)
		return preserveCloudConfig(existing, flag), nil
	}

	items, err := resolver()
	items, preserveExisting := cloudSelectionItems(items, selected, err)
	if preserveExisting {
		fmt.Fprintf(os.Stderr, "  Cannot list %s: %v. Keeping the current selection.\n", name, err)
		return preserveCloudConfig(existing, flag), nil
	}
	if len(items) == 0 {
		fmt.Fprintf(os.Stderr, "  No %s items found.\n", name)
		return nil, nil
	}

	var opts []huh.Option[string]
	selectedSet := mapFromValues(selected)
	for _, item := range items {
		opts = append(opts, huh.NewOption(item.Label, item.Value).Selected(selectedSet[item.Value]))
	}

	chosen := append([]string(nil), selected...)
	if err := runWizardField(huh.NewMultiSelect[string]().
		Title(selectTitle).
		Options(opts...).
		Value(&chosen)); err != nil {
		return nil, err
	}

	if len(chosen) == 0 {
		return nil, nil
	}
	csv := strings.Join(chosen, ",")
	return []string{flag + " " + csv}, nil
}

// cloudSelectionItems makes saved values visible even when they cannot be
// discovered locally. A discovery error is distinct from a valid empty list:
// callers keep the existing configuration on an error, while an empty list can
// be intentionally left unconfigured.
func cloudSelectionItems(discovered []registry.ListItem, saved []string, discoveryErr error) ([]registry.ListItem, bool) {
	if discoveryErr != nil {
		return nil, true
	}

	seen := make(map[string]struct{}, len(discovered)+len(saved))
	items := make([]registry.ListItem, 0, len(discovered)+len(saved))
	for _, item := range discovered {
		item.Value = strings.TrimSpace(item.Value)
		if item.Value == "" {
			continue
		}
		if _, ok := seen[item.Value]; ok {
			continue
		}
		seen[item.Value] = struct{}{}
		items = append(items, item)
	}
	for _, value := range saved {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		items = append(items, registry.ListItem{
			Label: value + " — unavailable locally (saved selection)",
			Value: value,
		})
	}
	if len(items) == 0 {
		return nil, false
	}
	return items, false
}

func preserveCloudConfig(existing []string, flag string) []string {
	if len(existing) > 0 {
		return append([]string(nil), existing...)
	}
	return []string{flag}
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
// Step 5: Network boundary
// ---------------------------------------------------------------------------

func wizardNetworkBoundary(workspace string, editMode bool, existFirewall, existOpts, existExtraDomains []string) ([]string, []string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Network"))
	state := networkWizardStateFromLines(existFirewall, existOpts, existExtraDomains)

	if editMode {
		displayCurrentSetup(existingNetworkLinesFromState(state), "Network: bridge + strict firewall (default)")

		var action string
		if err := runWizardField(huh.NewSelect[string]().
			Title("Network boundary").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action)); err != nil {
			return nil, nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return networkLinesFromState(state), state.ExtraDomains, nil
		}
	}

	fmt.Fprintln(os.Stderr)

	boundary := boundaryModeFromNetworkState(state)
	if err := runWizardField(huh.NewSelect[string]().
		Title("Network boundary").
		Options(
			huh.NewOption("Bridge + firewall allowlist (recommended)", "bridge-firewall"),
			huh.NewOption("Bridge + unrestricted outbound HTTP(S)", "bridge-open"),
			huh.NewOption("Host network (least isolated, for local/VPN services)", "host"),
		).
		Value(&boundary)); err != nil {
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
	if err := runWizardField(huh.NewConfirm().
		Title("Discover required domains on your next run?").
		Description("Arms a one-time learn pass: the next run records domains used\noutside the allowlist and offers to add them, then reverts to\nenforcing. Run a representative build to populate the allowlist.").
		Affirmative("Arm").
		Negative("Skip").
		Value(&arm)); err != nil {
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
	if err := runWizardField(huh.NewSelect[string]().
		Title("Firewall allowlist").
		Options(
			huh.NewOption("Strict (default) - git, registries, package managers only", "strict"),
			huh.NewOption("Developer-friendly - adds cloud APIs, apt, CDN", "dev"),
			huh.NewOption("Custom file - provide your own whitelist", "custom"),
		).
		Value(&mode)); err != nil {
		return NetworkPolicy{}, err
	}
	fmt.Fprintln(os.Stderr)

	switch mode {
	case "dev":
		return NetworkPolicy{Mode: "bridge", Firewall: "dev"}, nil
	case "custom":
		path := existing.CustomConfig
		if err := runWizardField(huh.NewInput().
			Title("Path to custom whitelist file").
			Placeholder("/path/to/firewall.conf").
			Value(&path)); err != nil {
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
	if err := runWizardField(huh.NewInput().
		Title("Additional allowed domains (comma-separated, optional)").
		Placeholder("*.apps.example.test, api.example.com").
		Value(&value)); err != nil {
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
		if err := runWizardField(huh.NewSelect[string]().
			Title("Options").
			Options(
				huh.NewOption("Keep", "keep"),
				huh.NewOption("Change", "change"),
			).
			Value(&action)); err != nil {
			return nil, err
		}
		if action == "keep" {
			fmt.Fprintln(os.Stderr)
			return optionLinesFromState(state), nil
		}
	}

	fmt.Fprintln(os.Stderr)

	yolo := boolValue(state.Execution.Yolo, true)
	if err := runWizardField(huh.NewConfirm().
		Title("Run without approval prompts?").
		Value(&yolo)); err != nil {
		return nil, err
	}

	worktree := state.Execution.Worktree
	if err := runWizardField(huh.NewConfirm().
		Title("Work in a separate Git worktree?").
		Value(&worktree)); err != nil {
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
	MCPLines       []string
	MCPServers     []MCPServerPolicy
	MCPAll         bool
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
	// Structured MCP selections carry modes and pins that legacy --mcp lines
	// cannot; override the migrated (direct-only) result when present.
	if input.MCPAll || len(input.MCPServers) > 0 {
		policy.MCP = MCPPolicy{All: input.MCPAll, Servers: input.MCPServers}
	}
	return policy, lines, nil
}

func wizardEquivalentLines(input WizardAssemblyInput) []string {
	var lines []string
	lines = append(lines, input.ProviderLines...)
	lines = append(lines, input.DirLines...)
	lines = append(lines, input.ExtensionLines...)
	lines = append(lines, input.MCPLines...)
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

func splitMCPLines(lines []string) (rest, mcp []string) {
	for _, line := range lines {
		flag := configLineFlag(line)
		if flag == "--mcp" || flag == "--mcp-all" {
			mcp = append(mcp, line)
			continue
		}
		rest = append(rest, line)
	}
	return rest, mcp
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
		return "Approval prompts: enabled"
	case line == "--yolo":
		return "Approval prompts: disabled"
	case line == "--network-host":
		return "Network: host"
	case line == "--worktree":
		return "Parallel isolation: git worktree"
	case strings.HasPrefix(line, "network.extra_domain "):
		return "Allowed domain: " + strings.TrimSpace(strings.TrimPrefix(line, "network.extra_domain "))
	case line == "option.yolo enabled":
		return "Approval prompts: disabled"
	case line == "option.yolo disabled":
		return "Approval prompts: enabled"
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

func loadWizardExistingProfileConfig(workspace, name string, extensions []*registry.Extension) ([]string, PolicySource, error) {
	if name == "default" {
		return loadWizardExistingConfig(workspace, extensions)
	}
	p, err := LoadNamedProfile(workspace, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, PolicySourceNone, nil
		}
		return nil, PolicySourceNone, err
	}
	return legacyArgsToConfigLines(p.ToLegacyFlags()), PolicySourceV2, nil
}

func loadWizardProfilePolicy(workspace, name string, extensions []*registry.Extension) (*ProjectPolicy, error) {
	if name == "default" {
		p, _, err := LoadProjectPolicy(workspace, extensions)
		return p, err
	}
	p, err := LoadNamedProfile(workspace, name)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return nil, nil
	}
	return p, err
}

func displayWizardExistingProfileConfig(workspace, name string, source PolicySource, lines []string, extensions []*registry.Extension) {
	if name == "default" {
		displayWizardExistingConfig(workspace, source, lines, extensions)
		return
	}
	if source != PolicySourceV2 {
		return
	}
	fmt.Fprintf(os.Stderr, "Existing profile %q: %s\n\n", name, profilesPolicyPath(workspace))
	if policy, err := LoadNamedProfile(workspace, name); err == nil {
		fmt.Fprint(os.Stderr, renderWizardBoundary(launchSummaryFromPolicy(policy, workspace, name)))
	}
	fmt.Fprintln(os.Stderr)
}

func displayWizardExistingConfig(workspace string, source PolicySource, lines []string, extensions []*registry.Extension) {
	switch source {
	case PolicySourceV2:
		fmt.Fprintf(os.Stderr, "Existing policy: %s\n\n", projectPolicyPath(workspace))
		if policy, _, err := LoadProjectPolicy(workspace, extensions); err == nil && policy != nil {
			fmt.Fprint(os.Stderr, renderWizardBoundary(launchSummaryFromPolicy(policy, workspace)))
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

func renderWizardBoundary(summary LaunchSummary) string {
	// Lipgloss pads multiline blocks to their widest line. Style each line
	// separately so long mount lists don't turn padding into blank terminal rows.
	lines := strings.Split(summary.Render(), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = wizardDim.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}
