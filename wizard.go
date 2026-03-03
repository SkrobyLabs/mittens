package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// Extension wizard descriptors — maps extension names to wizard presentation.
// ---------------------------------------------------------------------------

// extEntry describes one item in the extension multi-select.
type extEntry struct {
	label    string // human-readable label
	key      string // extension name  (e.g. "aws", "ssh")
	flag     string // config flag     (e.g. "--ssh", "--aws")
	needsCfg bool   // needs a follow-up configuration step
}

// knownExtEntries defines the order and presentation of extensions in the
// wizard. Only entries whose extension name is present in the loaded set
// will be shown.
var knownExtEntries = []extEntry{
	{label: "SSH agent forwarding (--ssh)", key: "ssh", flag: "--ssh"},
	{label: "GitHub CLI auth (--gh)", key: "gh", flag: "--gh"},
	{label: ".NET SDK (--dotnet)", key: "dotnet", flag: "--dotnet", needsCfg: true},
	{label: "Go SDK (--go)", key: "go", flag: "--go", needsCfg: true},
	{label: "Disable network firewall (--no-firewall)", key: "firewall", flag: "--no-firewall"},
	{label: "AWS credentials (--aws)", key: "aws", flag: "--aws", needsCfg: true},
	{label: "GCP credentials (--gcp)", key: "gcp", flag: "--gcp", needsCfg: true},
	{label: "Azure credentials (--azure)", key: "azure", flag: "--azure", needsCfg: true},
	{label: "Kubernetes contexts (--k8s)", key: "kubectl", flag: "--k8s", needsCfg: true},
	{label: "MCP server passthrough (--mcp)", key: "mcp", flag: "--mcp", needsCfg: true},
}

// ---------------------------------------------------------------------------
// Main entry point
// ---------------------------------------------------------------------------

// runWizard runs the interactive TUI setup wizard. The extensions parameter
// is the loaded extension list from the embedded YAML manifests (so the wizard
// knows which extensions are available).
func runWizard(extensions []*registry.Extension) error {
	// Build a set of loaded extension names for quick lookup.
	loaded := make(map[string]bool, len(extensions))
	for _, ext := range extensions {
		loaded[ext.Name] = true
	}

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
	var existDirs, existExts, existOpts []string

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
			existDirs, existExts, existOpts = parseExistingConfig(existing)
		}
		fmt.Fprintln(os.Stderr)
	}

	var configLines []string

	// ── Step 1: Extra directories ──────────────────────────────────────────
	dirLines, err := wizardDirs(workspace, editMode, existDirs)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, dirLines...)

	// ── Step 2+3: Extensions ───────────────────────────────────────────────
	extLines, err := wizardExtensions(loaded, editMode, existExts)
	if err != nil {
		return gracefulAbort(err)
	}
	configLines = append(configLines, extLines...)

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
	var runNow bool
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
// Step 1: Directories
// ---------------------------------------------------------------------------

func wizardDirs(workspace string, editMode bool, existDirs []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 1: Directories"))
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

	// Discover sibling git repos.
	parentDir := filepath.Dir(workspace)
	var siblings []string
	if entries, err := os.ReadDir(parentDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			full := filepath.Join(parentDir, e.Name())
			if full == workspace {
				continue
			}
			gitDir := filepath.Join(full, ".git")
			if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
				siblings = append(siblings, full)
			}
		}
		sort.Strings(siblings)
	}

	// Build a set of existing dir paths for pre-selection in edit mode.
	existPathSet := make(map[string]bool, len(existDirs))
	if editMode {
		for _, d := range existDirs {
			existPathSet[strings.TrimPrefix(d, "--dir ")] = true
		}
	}

	var selectedDirs []string

	// Multi-select from siblings.
	if len(siblings) > 0 {
		fmt.Fprintln(os.Stderr)
		var opts []huh.Option[string]
		for _, s := range siblings {
			opts = append(opts, huh.NewOption(s, s).Selected(existPathSet[s]))
		}

		// Pre-populate chosen with existing sibling paths so Value() matches.
		var chosen []string
		if editMode {
			for _, s := range siblings {
				if existPathSet[s] {
					chosen = append(chosen, s)
				}
			}
		}
		if err := huh.NewMultiSelect[string]().
			Title("Sibling directories with git repos").
			Options(opts...).
			Value(&chosen).
			Run(); err != nil {
			return nil, err
		}
		selectedDirs = append(selectedDirs, chosen...)
	}

	// Carry forward existing custom dirs (paths not in siblings).
	if editMode {
		siblingSet := make(map[string]bool, len(siblings))
		for _, s := range siblings {
			siblingSet[s] = true
		}
		for p := range existPathSet {
			if !siblingSet[p] {
				selectedDirs = append(selectedDirs, p)
				fmt.Fprintf(os.Stderr, "  Kept custom dir: %s\n", p)
			}
		}
	}

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
// Step 2+3: Extensions
// ---------------------------------------------------------------------------

func wizardExtensions(loaded map[string]bool, editMode bool, existExts []string) ([]string, error) {
	fmt.Fprintln(os.Stderr, wizardBold.Render("Step 2: Extensions"))

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

	// Build a set of existing extension keys for pre-selection in edit mode.
	existExtSet := make(map[string]bool)
	if editMode {
		for _, line := range existExts {
			for _, e := range knownExtEntries {
				if line == e.flag || strings.HasPrefix(line, e.flag+" ") {
					existExtSet[e.key] = true
					break
				}
			}
		}
	}

	// Build options from the known entries, filtered by what is loaded.
	var opts []huh.Option[string]
	for _, e := range knownExtEntries {
		if loaded[e.key] {
			opts = append(opts, huh.NewOption(e.label, e.key).Selected(existExtSet[e.key]))
		}
	}

	if len(opts) == 0 {
		fmt.Fprintln(os.Stderr, "  No extensions available.")
		fmt.Fprintln(os.Stderr)
		return nil, nil
	}

	// Pre-populate chosen with existing extension keys so Value() matches.
	var chosen []string
	if editMode {
		for _, e := range knownExtEntries {
			if loaded[e.key] && existExtSet[e.key] {
				chosen = append(chosen, e.key)
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
	var configLines []string
	var needsCfg []string

	chosenSet := make(map[string]bool, len(chosen))
	for _, k := range chosen {
		chosenSet[k] = true
	}

	for _, e := range knownExtEntries {
		if !chosenSet[e.key] {
			continue
		}
		if e.needsCfg {
			needsCfg = append(needsCfg, e.key)
		} else {
			configLines = append(configLines, e.flag)
		}
	}

	// Step 3: Configure extensions that need it.
	if len(needsCfg) > 0 {
		fmt.Fprintln(os.Stderr, wizardBold.Render("Step 3: Configure selected extensions"))
		fmt.Fprintln(os.Stderr)

		for _, key := range needsCfg {
			lines, err := configureExtension(key)
			if err != nil {
				return nil, err
			}
			configLines = append(configLines, lines...)
		}
	}

	return configLines, nil
}

// configureExtension runs the sub-configuration step for a single extension.
func configureExtension(key string) ([]string, error) {
	switch key {
	case "dotnet":
		return configureDotnet()
	case "go":
		return configureGo()
	case "aws":
		return configureCloud("aws", "--aws", "--aws-all")
	case "gcp":
		return configureCloud("gcp", "--gcp", "--gcp-all")
	case "azure":
		return configureCloud("azure", "--azure", "--azure-all")
	case "kubectl":
		return configureKubectl()
	case "mcp":
		return configureMCP()
	default:
		return nil, nil
	}
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
		Title(strings.ToUpper(name) + " credentials").
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

	dind := optSet["--dind"]
	if err := huh.NewConfirm().
		Title("Docker-in-Docker? (--dind)").
		Value(&dind).
		Run(); err != nil {
		return nil, err
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
	if dind {
		lines = append(lines, "--dind")
	}
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
// extensions, and options.
func parseExistingConfig(lines []string) (dirs, exts, opts []string) {
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--dir "):
			dirs = append(dirs, line)
		case line == "--dind" || line == "--yolo" || line == "--network-host" || line == "--worktree":
			opts = append(opts, line)
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
