package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"
)

type wizardEditorConfig struct {
	Workspace, Profile, Mode string // project, defaults, or session
	Extensions               []*registry.Extension
	New                      bool
	Unchanged                bool
	Focus                    string
}

type wizardDraft struct {
	Policy                                  *ProjectPolicy
	TrustCodex, Learn, SaveDefaults, Launch bool
	SkipSave                                bool
}

// Only the caller persists a completed draft. Each section gets an isolated
// copy, so returning from a half-completed picker never changes the draft.
type wizardEditorUI struct {
	Choose  func(wizardDraft, wizardEditorConfig) (string, error)
	Edit    func(string, *wizardDraft, wizardEditorConfig) error
	Review  func(*ProjectPolicy, wizardDraft, wizardEditorConfig, bool) (bool, error)
	Details func(*ProjectPolicy, wizardEditorConfig) error
	Reset   func(wizardEditorConfig) (*ProjectPolicy, error)
}

func wizardInitialPolicy(workspace, profile string, extensions []*registry.Extension) (*ProjectPolicy, error) {
	policy, err := loadWizardProfilePolicy(workspace, profile, extensions)
	if err != nil && !(profile != "default" && strings.Contains(err.Error(), "not found")) {
		return nil, err
	}
	if policy == nil {
		if profile != "default" {
			policy, _, err = effectivePolicyForShow(workspace, extensions)
		} else {
			policy, err = defaultPolicyForMutation(workspace, extensions)
		}
		if err != nil {
			return nil, err
		}
	}
	policy, err = clonePolicy(policy)
	if err != nil {
		return nil, err
	}
	if _, err := resolveLegacyProviderProfile(workspace, policy, false); err != nil {
		return nil, err
	}
	return policy, nil
}

func runProjectWizardEditor(extensions []*registry.Extension, workspace, profile string) error {
	_, source, err := loadWizardExistingProfileConfig(workspace, profile, extensions)
	if err != nil {
		return err
	}
	base, err := wizardInitialPolicy(workspace, profile, extensions)
	if err != nil {
		return err
	}
	result, err := runWizardEditor(base, wizardEditorConfig{
		Workspace: workspace, Profile: profile, Mode: "project", Extensions: extensions, New: source == PolicySourceNone,
	})
	if err != nil {
		return gracefulAbort(err)
	}
	if !result.SkipSave {
		if err := saveWizardProject(workspace, profile, result); err != nil {
			return err
		}
	}
	if result.Launch {
		return launchWizardProject(profile)
	}
	return nil
}

func launchWizardProject(profile string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if profile != "default" {
		return execCommand(exe, "--profile", profile)
	}
	return execCommand(exe)
}

func saveWizardProject(workspace, profile string, draft wizardDraft) error {
	if err := SaveNamedProfile(workspace, profile, draft.Policy); err != nil {
		return fmt.Errorf("saving project configuration: %w", err)
	}
	path := projectPolicyPath(workspace)
	if profile != "default" {
		path = profilesPolicyPath(workspace)
	}
	fmt.Fprintln(os.Stderr, wizardSuccess.Render("Project configuration saved to: "+path))
	if draft.SaveDefaults {
		if err := SaveUserDefaultsPolicy(draft.Policy); err != nil {
			return fmt.Errorf("project saved, but saving user defaults failed: %w", err)
		}
		fmt.Fprintln(os.Stderr, "User defaults saved to: "+UserDefaultsPolicyPath())
	}
	if draft.TrustCodex {
		if _, err := trustCodexProject(workspace); err != nil {
			return fmt.Errorf("project saved, but saving Codex project trust failed: %w", err)
		}
	}
	if draft.Learn {
		if err := armLearnPass(workspace); err != nil {
			return fmt.Errorf("project saved, but arming domain discovery failed: %w", err)
		}
	}
	return nil
}

func runWizardEditor(base *ProjectPolicy, cfg wizardEditorConfig) (wizardDraft, error) {
	return runWizardEditorWithUI(base, cfg, wizardEditorUI{
		Choose: chooseWizardAction, Edit: editWizardSection, Review: reviewWizardDraft,
		Details: func(policy *ProjectPolicy, cfg wizardEditorConfig) error {
			_, err := viewWizardText("Configuration details", renderWizardDetailsWithWidth(policy, cfg.Workspace, 0), "")
			return err
		},
		Reset: func(cfg wizardEditorConfig) (*ProjectPolicy, error) {
			if cfg.Mode == "defaults" {
				return defaultProjectPolicy(), nil
			}
			return defaultPolicyForMutation(cfg.Workspace, cfg.Extensions)
		},
	})
}

func runWizardEditorWithUI(base *ProjectPolicy, cfg wizardEditorConfig, ui wizardEditorUI) (wizardDraft, error) {
	policy, err := clonePolicy(base)
	if err != nil {
		return wizardDraft{}, err
	}
	draft := wizardDraft{Policy: policy}
	for {
		cfg.Unchanged = wizardValuesEqual(base, draft.Policy) && !draft.TrustCodex && !draft.Learn && !draft.SaveDefaults
		action, err := ui.Choose(draft, cfg)
		if err != nil {
			return wizardDraft{}, err
		}
		switch action {
		case "launch-existing":
			if cfg.Mode == "project" && !cfg.New && cfg.Unchanged {
				draft.Launch, draft.SkipSave = true, true
				return draft, nil
			}
		case "cancel":
			return wizardDraft{}, huh.ErrUserAborted
		case "save", "launch", "apply":
			if err := draft.Policy.Validate(); err != nil {
				logWarn("Cannot save configuration: %v", err)
				continue
			}
			ok, err := ui.Review(base, draft, cfg, true)
			if errors.Is(err, huh.ErrUserAborted) {
				continue
			}
			if err != nil {
				return wizardDraft{}, err
			}
			if ok {
				draft.Launch = action == "launch"
				return draft, nil
			}
		case "review":
			_, err := ui.Review(base, draft, cfg, false)
			if err != nil && !errors.Is(err, huh.ErrUserAborted) {
				return wizardDraft{}, err
			}
		case "details":
			if err := ui.Details(draft.Policy, cfg); err != nil && !errors.Is(err, huh.ErrUserAborted) {
				return wizardDraft{}, err
			}
		case "defaults":
			draft.SaveDefaults = !draft.SaveDefaults
		case "reset":
			policy, err := ui.Reset(cfg)
			if err != nil {
				logWarn("Could not load defaults: %v", err)
				continue
			}
			policy, err = clonePolicy(policy)
			if err != nil {
				return wizardDraft{}, err
			}
			draft = wizardDraft{Policy: policy, SaveDefaults: draft.SaveDefaults}
		default:
			cfg.Focus = action
			candidate := draft
			candidate.Policy, err = clonePolicy(draft.Policy)
			if err != nil {
				return wizardDraft{}, err
			}
			if err := ui.Edit(action, &candidate, cfg); err != nil {
				if !errors.Is(err, huh.ErrUserAborted) && !errors.Is(err, errPickerCancelled) {
					logWarn("Changes were not applied: %v", err)
				}
				continue
			}
			if err := candidate.Policy.Validate(); err != nil {
				logWarn("Changes were not applied: %v", err)
				continue
			}
			draft = candidate
		}
	}
}

// Use a form keymap, rather than a field keymap which huh.Run replaces.
func runWizardField(field huh.Field) error {
	keys := huh.NewDefaultKeyMap()
	keys.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "back"))
	return huh.NewForm(huh.NewGroup(field)).WithKeyMap(keys).Run()
}

func chooseWizardAction(draft wizardDraft, cfg wizardEditorConfig) (string, error) {
	rows := wizardOverviewRows(draft.Policy, cfg.Workspace)
	sections := []string{"provider", "directories", "extensions", "mcp", "network", "execution"}
	var opts []huh.Option[string]
	for i, section := range sections {
		opts = append(opts, huh.NewOption(rows[i], section))
	}
	if cfg.Mode == "defaults" && isWSL() {
		opts = append(opts, huh.NewOption("Image paste key", "paste"))
	}
	opts = append(opts, huh.NewOption("View all details", "details"), huh.NewOption("Review changes", "review"))
	resetLabel := "Replace draft with recommended settings"
	if cfg.Mode != "defaults" && UserDefaultsExist() {
		resetLabel = "Replace draft with user defaults"
	}
	opts = append(opts, huh.NewOption(resetLabel, "reset"))
	if cfg.Mode == "project" {
		if !cfg.New && cfg.Unchanged {
			opts = append(opts, huh.NewOption("Launch saved configuration", "launch-existing"))
		}
		label := "Also save as user defaults: off"
		if draft.SaveDefaults {
			label = "Also save as user defaults: on"
		}
		opts = append(opts, huh.NewOption(label, "defaults"), huh.NewOption("Save and launch", "launch"))
	}
	if cfg.Mode == "session" {
		opts = append(opts, huh.NewOption("Apply to this launch", "apply"))
	} else {
		opts = append(opts, huh.NewOption("Save and exit", "save"))
	}
	opts = append(opts, huh.NewOption("Discard and exit", "cancel"))
	title := "mittens project setup · " + cfg.Profile
	description := cfg.Workspace + "\nChoose a section to edit. Changes remain a draft until you save."
	if cfg.New {
		seedLabel := "recommended settings"
		if cfg.Profile != "default" {
			seedLabel = "current project settings"
		} else if UserDefaultsExist() {
			seedLabel = "user defaults"
		}
		description += "\nStarting from " + seedLabel + ". Save as-is or customize a section."
	}
	if cfg.Mode == "defaults" {
		title = "User defaults"
		description = "These settings seed new projects. Existing project configurations keep their settings."
	} else if cfg.Mode == "session" {
		title = "Session settings · " + cfg.Profile
		description = cfg.Workspace + "\nChanges apply to this launch only; nothing will be saved."
	}
	action := valueOr(cfg.Focus, "provider")
	if cfg.New && cfg.Focus == "" {
		action = "launch"
	}
	return runWizardMenu(title, description, opts, action)
}

func editWizardSection(section string, draft *wizardDraft, cfg wizardEditorConfig) error {
	p := draft.Policy
	seed := wizardSeedFromPolicy(p)
	switch section {
	case "provider":
		lines, config, err := wizardProvider("", false, seed.providerState)
		if err != nil {
			return err
		}
		updated, _, err := assembleWizardPolicy(WizardAssemblyInput{ProviderLines: lines, ProviderConfig: config}, cfg.Extensions)
		if err != nil {
			return err
		}
		if updated.Provider.Name == p.Provider.Name {
			updated.Provider.Effort = p.Provider.Effort
		}
		p.Provider = updated.Provider
		draft.TrustCodex = false
		if cfg.Mode == "project" && providerLinesUseCodexHarness(lines) {
			if err := runWizardField(huh.NewConfirm().Title("Trust this project in Codex on save?").Description("Skips the Codex startup trust prompt for this workspace.").Value(&draft.TrustCodex)); err != nil {
				return err
			}
		}
	case "directories":
		lines, err := wizardDirs(cfg.Workspace, false, seed.dirs)
		if err != nil {
			return err
		}
		p.Workspace.Mounts = mountsFromDirLines(lines)
	case "extensions":
		lines, err := editExtensionLines(wizardAvailableExtensions(cfg.Extensions), seed.exts)
		if err != nil {
			return err
		}
		updated, err := PolicyFromLegacyFlags(splitConfigFlags(lines), cfg.Extensions)
		if err != nil {
			return err
		}
		// Unknown extensions cannot be edited by this installation. Keep them.
		editable := map[string]bool{}
		for _, ext := range wizardAvailableExtensions(cfg.Extensions) {
			editable[ext.Name] = true
		}
		for _, cap := range p.Capabilities {
			if !editable[cap.Name] {
				updated.Capabilities = append(updated.Capabilities, cap)
			}
		}
		p.Capabilities = updated.Capabilities
		if editable["docker"] {
			p.Execution.Docker = updated.Execution.Docker
		}
	case "mcp":
		servers, all, err := wizardMCP(false, p.MCP.Servers, p.MCP.All, p.Provider.Name, cfg.Workspace)
		if err != nil {
			return err
		}
		p.MCP = MCPPolicy{Servers: servers, All: all}
	case "network":
		lines, domains, err := wizardNetworkBoundary("", false, seed.firewall, seed.opts, seed.extraDomains)
		if err != nil {
			return err
		}
		updated := networkWizardStateFromLines(lines, lines, domains)
		updated.Network.SSHEgress = p.Network.SSHEgress
		p.Network = updated.Network
		p.Execution.NetworkHost = updated.Network.Mode == "host"
		draft.Learn = false
		if cfg.Mode == "project" && (p.Network.Firewall == "strict" || p.Network.Firewall == "custom") {
			if err := runWizardField(huh.NewConfirm().Title("Discover required domains on the next run?").Description("After saving, one launch will record domains outside the allowlist and offer to add them.").Value(&draft.Learn)); err != nil {
				return err
			}
		}
	case "execution":
		lines, err := wizardOptions(false, seed.opts)
		if err != nil {
			return err
		}
		state := optionWizardStateFromLines(lines)
		p.Execution.Yolo, p.Execution.Worktree = state.Execution.Yolo, state.Execution.Worktree
		p.Workspace.Mode = "direct"
		if state.Execution.Worktree {
			p.Workspace.Mode = "worktree"
		}
	case "paste":
		lines, err := wizardImagePasteKey(p.Options["image_paste_key"])
		if err != nil {
			return err
		}
		if p.Options == nil {
			p.Options = map[string]string{}
		}
		delete(p.Options, "image_paste_key")
		if len(lines) > 0 {
			p.Options["image_paste_key"] = strings.TrimPrefix(lines[0], "--image-paste-key ")
		}
	default:
		return fmt.Errorf("unknown wizard section %q", section)
	}
	return nil
}

func reviewWizardDraft(base *ProjectPolicy, draft wizardDraft, cfg wizardEditorConfig, saving bool) (bool, error) {
	lines := wizardPolicyChanges(base, draft.Policy)
	if len(lines) == 0 {
		lines = []string{"No settings changed."}
	}
	if cfg.New {
		lines = append([]string{"Create a project configuration from these settings."}, lines...)
	}
	if draft.TrustCodex {
		lines = append(lines, "Trust this workspace in Codex.")
	}
	if draft.Learn {
		lines = append(lines, "Discover domains on the next launch (temporarily allow traffic outside the allowlist).")
	}
	if draft.SaveDefaults {
		lines = append(lines, "Also replace user defaults with this configuration, including its directory and credential selections.")
	}
	if !saving {
		return viewWizardText("Changes in this draft", strings.Join(lines, "\n"), "")
	}
	title, acceptLabel := "Review before saving", "save"
	if cfg.Mode == "session" {
		title, acceptLabel = "Review session settings", "apply"
	}
	content := strings.Join(wizardOverviewRows(draft.Policy, cfg.Workspace), "\n") + "\n\nChanges:\n" + strings.Join(lines, "\n")
	return viewWizardText(title, content, acceptLabel)
}

func wizardPolicyChanges(before, after *ProjectPolicy) []string {
	var out []string
	if !wizardValuesEqual(before.Provider, after.Provider) {
		out = append(out, fmt.Sprintf("Provider: %s → %s", wizardProviderLabel(before.Provider), wizardProviderLabel(after.Provider)))
	}
	for _, mount := range before.Workspace.Mounts {
		if !containsWizardMount(after.Workspace.Mounts, mount) {
			out = append(out, "Remove directory: "+mount.Path+" ("+mount.Access+")")
		}
	}
	for _, mount := range after.Workspace.Mounts {
		if !containsWizardMount(before.Workspace.Mounts, mount) {
			out = append(out, "Add directory: "+mount.Path+" ("+mount.Access+")")
		}
	}
	if !wizardValuesEqual(before.Network, after.Network) {
		old, next := launchSummaryFromPolicy(before, ""), launchSummaryFromPolicy(after, "")
		out = append(out, "Network: "+old.Network+" → "+next.Network)
		if before.Network.CustomConfig != after.Network.CustomConfig {
			out = append(out, "Firewall file: "+valueOr(before.Network.CustomConfig, "none")+" → "+valueOr(after.Network.CustomConfig, "none"))
		}
		out = append(out, wizardListChanges("allowed domain", before.Network.ExtraDomains, after.Network.ExtraDomains)...)
		if boolValue(before.Network.SSHEgress, true) != boolValue(after.Network.SSHEgress, true) {
			out = append(out, fmt.Sprintf("Allow SSH egress: %t → %t", boolValue(before.Network.SSHEgress, true), boolValue(after.Network.SSHEgress, true)))
		}
	}
	if !wizardValuesEqual(before.Capabilities, after.Capabilities) {
		out = append(out, wizardListChanges("extension", wizardCapabilityItems(before.Capabilities), wizardCapabilityItems(after.Capabilities))...)
	}
	if !wizardValuesEqual(before.MCP, after.MCP) {
		out = append(out, wizardListChanges("MCP server", mcpServersFromPolicy(before), mcpServersFromPolicy(after))...)
		for _, next := range after.MCP.Servers {
			for _, old := range before.MCP.Servers {
				if old.Name == next.Name && old.CommandPin != next.CommandPin {
					out = append(out, "Update command approval: "+next.Name)
				}
			}
		}
	}
	if !wizardValuesEqual(before.Credentials, after.Credentials) {
		out = append(out, "Credential access: "+strings.Join(credentialsFromPolicy(before), ", ")+" → "+strings.Join(credentialsFromPolicy(after), ", "))
	}
	if !wizardValuesEqual(before.Host, after.Host) {
		out = append(out, "Host integrations: "+strings.Join(hostIntegrationsFromPolicy(before), ", ")+" → "+strings.Join(hostIntegrationsFromPolicy(after), ", "))
	}
	if !wizardValuesEqual(before.Execution, after.Execution) {
		out = append(out, "Execution: "+strings.Join(wizardExecutionItems(before), ", ")+" → "+strings.Join(wizardExecutionItems(after), ", "))
	}
	if historyFromPolicy(before) != historyFromPolicy(after) {
		out = append(out, "History: "+historyFromPolicy(before)+" → "+historyFromPolicy(after))
	}
	if !wizardValuesEqual(before.Options, after.Options) {
		out = append(out, "Other options updated.")
	}
	if !wizardValuesEqual(before.ExtraArgs, after.ExtraArgs) {
		out = append(out, "Additional provider arguments updated.")
	}
	if before.Workspace.Mode != after.Workspace.Mode {
		out = append(out, "Workspace: "+before.Workspace.Mode+" → "+after.Workspace.Mode)
	}
	return out
}

func containsWizardMount(mounts []PolicyMount, target PolicyMount) bool {
	for _, mount := range mounts {
		if mount == target {
			return true
		}
	}
	return false
}

func wizardProviderLabel(p ProviderPolicy) string {
	parts := []string{p.Name}
	for _, value := range []string{p.Backend, p.Model, p.Effort, p.Endpoint} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

// YAML equality treats nil and empty collections alike, matching persisted policy semantics.
func wizardValuesEqual(a, b any) bool {
	left, err := yaml.Marshal(a)
	if err != nil {
		return false
	}
	right, err := yaml.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func wizardListChanges(label string, before, after []string) []string {
	var changes []string
	old, next := mapFromValues(before), mapFromValues(after)
	for _, value := range before {
		if value != "none" && !next[value] {
			changes = append(changes, "Remove "+label+": "+value)
		}
	}
	for _, value := range after {
		if value != "none" && !old[value] {
			changes = append(changes, "Add "+label+": "+value)
		}
	}
	return changes
}

func wizardCapabilityItems(caps []CapabilityPolicy) []string {
	var items []string
	for _, cap := range caps {
		if cap.All {
			items = append(items, cap.Name+": all configured")
		} else if len(cap.Args) > 0 {
			for _, arg := range cap.Args {
				items = append(items, cap.Name+": "+arg)
			}
		} else {
			items = append(items, cap.Name)
		}
	}
	return items
}
