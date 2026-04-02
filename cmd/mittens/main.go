package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/SkrobyLabs/mittens/cmd/mittens/extensions/registry"

	// Blank imports trigger init() registration for extensions with resolvers.
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/aws"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/azure"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/docker"
	firewallext "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/firewall"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/gcp"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/gh"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/helm"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/kubectl"
	_ "github.com/SkrobyLabs/mittens/cmd/mittens/extensions/mcp"
)

// Set by -ldflags at build time (see Makefile).
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:                "mittens [flags] [-- claude-args...]",
		Short:              "Run Claude Code in an isolated Docker container",
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMain(args)
		},
	}

	// NOTE: "init" is NOT registered as a cobra subcommand.
	// With DisableFlagParsing: true, cobra's stripFlags() can't tell which
	// flags consume a value (e.g. --dir PATH), so any bare path
	// arg gets misidentified as a subcommand name and triggers
	// "unknown command" errors. Handling "init" in runMain's switch avoids
	// this entirely.

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", colorRed("[mittens]"), err)
		os.Exit(1)
	}
}

// runMain is the main entrypoint when no subcommand is given.
func runMain(args []string) error {
	// Handle subcommands and special flags manually since DisableFlagParsing is true.
	if len(args) > 0 {
		// Subcommands (first arg only).
		switch args[0] {
		case "init":
			return handleInit(args)
		case "help":
			return runHelp()
		case "logs":
			return runLogs(args[1:])
		case "clean":
			return runClean(args[1:])
		case "team":
			return handleTeam(args[1:])
		case "extension":
			return runExtension(args[1:])
		case "version":
			return runVersion(args[1:])
		}

		// Flag-style aliases (can appear anywhere before "--").
		if hasSubFlag(args, "--init") { //deprecated-delete-after:2026-05-01
			fmt.Fprintf(os.Stderr, "[mittens] warning: --init is deprecated, use \"mittens init\" instead\n")
			return handleInit(args)
		}
		if hasSubFlag(args, "--help") || hasSubFlag(args, "-h") {
			return runHelp()
		}
		if hasSubFlag(args, "--version") || hasSubFlag(args, "-V") {
			return runVersion(nil)
		}
	}

	// Pre-scan for --session (ephemeral config edit).
	sessionMode := hasSubFlag(args, "--session")
	if sessionMode {
		if hasSubFlag(args, "--no-config") {
			return fmt.Errorf("--session and --no-config cannot be used together")
		}
		if hasSubFlag(args, "--init") {
			return fmt.Errorf("--session and --init cannot be used together")
		}
	}

	app := &App{
		Provider:        DefaultProvider(),
		ImageName:       "mittens",
		ImageTag:        "latest",
		Yolo:            true,
		worktreeOrigins: make(map[string]string),
		worktreeRepos:   make(map[string]string),
	}

	// Load all extensions: bundled (disk-first, embed fallback) + user-installed.
	exts, err := loadExtensions()
	if err != nil {
		return fmt.Errorf("loading extensions: %w", err)
	}
	app.Extensions = exts

	// Set the default firewall.conf path for the firewall extension.
	// Also provide the embedded copy so the binary works standalone
	// (e.g. after "make install" to /usr/local/bin).
	firewallext.DefaultConfPath = filepath.Join(containerDir(), "firewall.conf")
	firewallext.EmbeddedConf = embeddedFirewallConf
	firewallext.EmbeddedDevConf = embeddedFirewallDevConf

	// 3. Load config: either ephemeral (--session wizard) or from disk.
	var userArgs []string
	var configArgs []string

	if sessionMode {
		userArgs, _ = LoadUserDefaults()

		ephemeralLines, err := wizardSession(app.Extensions)
		if err != nil {
			if err == huh.ErrUserAborted {
				fmt.Fprintln(os.Stderr, "\nCancelled.")
				return nil
			}
			return err
		}
		configArgs = splitConfigFlags(ephemeralLines)

		// Strip --session from args before merging.
		var filtered []string
		for _, a := range args {
			if a != "--session" {
				filtered = append(filtered, a)
			}
		}
		args = filtered
	} else {
		noConfig := false
		for _, a := range args {
			if a == "--no-config" {
				noConfig = true
				break
			}
		}

		if !noConfig {
			userArgs, _ = LoadUserDefaults()
			if len(userArgs) > 0 {
				logInfo("Loaded user defaults")
			}

			workspace := detectWorkspace()
			configArgs, err = LoadProjectConfig(workspace)
			if err != nil {
				return fmt.Errorf("loading project config: %w", err)
			}
			if len(configArgs) > 0 {
				logInfo("Loaded project config for %s", ProjectDir(workspace))
			}
		}
	}

	// 5. Merge: user defaults → project config → CLI args (last wins).
	merged := append(userArgs, configArgs...)
	merged = append(merged, args...)

	// 6. Resolve provider from merged flags (project config first, then CLI).
	provider, err := resolveProviderFromArgs(merged)
	if err != nil {
		return err
	}
	app.Provider = provider

	// 7. Parse all flags from the merged list.
	if err := app.ParseFlags(merged); err != nil {
		return err
	}

	app.NoConfig = hasSubFlag(args, "--no-config")

	// 8. Run.
	return app.Run()
}

// hasSubFlag checks whether a flag appears in the args (before "--").
func hasSubFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == flag {
			return true
		}
	}
	return false
}

// getSubFlagValue returns the value of a --flag value pair from args,
// or empty string if not found. Stops at "--".
func getSubFlagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == "--" {
			return ""
		}
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// handleInit dispatches the init subcommand (or --init flag) to the
// appropriate handler: help, defaults, profile, or the interactive wizard.
func handleInit(args []string) error {
	if hasSubFlag(args, "--help") || hasSubFlag(args, "-h") || (len(args) > 1 && args[1] == "help") {
		printInitHelp()
		return nil
	}
	if hasSubFlag(args, "--defaults") {
		return runInitDefaults()
	}
	if profileName := getSubFlagValue(args, "--profile"); profileName != "" {
		return runInitProfile(profileName, args)
	}
	return runInit()
}

func printInitHelp() {
	fmt.Println(`mittens init - Interactive project setup and configuration

Usage: mittens init [command]

Commands:
  (none)                        Interactive project setup wizard
  --defaults                    Edit user-wide defaults (provider, firewall, paste key)
  --profile NAME                Configure a model profile (model + effort)
  --profile NAME --delete       Delete a model profile
  --profile NAME --provider P   Configure profile for a specific provider (default: claude)

Examples:
  mittens init                          Set up a new project
  mittens init --defaults               Change default provider or firewall mode
  mittens init --profile planner        Create or edit the "planner" profile
  mittens init --profile fast           Create a "fast" profile (e.g. haiku, low effort)
  mittens init --profile planner --delete  Remove the "planner" profile`)
}

// runInitDefaults launches the user-wide defaults wizard directly.
func runInitDefaults() error {
	return wizardUserDefaults()
}

// loadExtensions discovers and loads all bundled and user-installed extensions.
func loadExtensions() ([]*registry.Extension, error) {
	home := homeDir()
	bundledDir := filepath.Join(runtimeRoot(), "extensions")
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	return registry.LoadAllExtensions(bundledDir, userExtDir, extensionYAMLs)
}

// runHelp loads extensions and prints the help text.
func runHelp() error {
	exts, err := loadExtensions()
	if err != nil {
		exts = nil // best-effort: show help without extensions
	}
	printHelp(exts)
	return nil
}

// runVersion handles the top-level `version` command, including optional JSON output.
func runVersion(args []string) error {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if arg == "--help" || arg == "-h" {
				fmt.Println("Usage: mittens version [--json]")
				fmt.Println("  --json   Output version information as JSON")
				return nil
			}
			return fmt.Errorf("unknown flag %q for \"mittens version\" (supported: --json)", arg)
		}
	}

	return printVersionOutput(jsonOutput)
}

func resolveProviderFromArgs(args []string) (*Provider, error) {
	provider := DefaultProvider()
	for i := 0; i < len(args); i++ {
		if args[i] != "--provider" {
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
			return nil, fmt.Errorf("--provider requires an argument")
		}
		p, err := providerByName(args[i+1])
		if err != nil {
			return nil, err
		}
		provider = p
		i++
	}
	return provider, nil
}

func providerByName(name string) (*Provider, error) {
	switch canonicalProviderName(name) {
	case "claude":
		return ClaudeProvider(), nil
	case "codex":
		return CodexProvider(), nil
	case "gemini":
		return GeminiProvider(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (available: claude, codex, gemini)", name)
	}
}

// runLogs shows or follows the broker log file.
// Usage: mittens logs [-f|--follow]
func runLogs(args []string) error {
	logPath := filepath.Join(homeDir(), ".mittens", "logs", "broker.log")

	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		fmt.Println("No logs yet. Start a mittens session first.")
		return nil
	}

	follow := false
	for _, a := range args {
		switch a {
		case "-f", "--follow":
			follow = true
		default:
			return fmt.Errorf("unknown flag %q for \"mittens logs\" (only -f/--follow is supported)", a)
		}
	}

	if follow {
		cmd := exec.Command("tail", "-f", logPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	_, _ = os.Stdout.Write(data)
	return nil
}

// runInit launches the interactive TUI setup wizard.
func runInit() error {
	exts, err := loadExtensions()
	if err != nil {
		return fmt.Errorf("loading extensions for wizard: %w", err)
	}
	return runWizard(exts)
}

// runInitProfile configures or deletes a model profile.
func runInitProfile(profileName string, args []string) error {
	providerName := "claude"
	if v := getSubFlagValue(args, "--provider"); v != "" {
		providerName = v
	}
	workspace := detectWorkspace()

	if hasSubFlag(args, "--delete") {
		return deleteProfile(workspace, profileName, providerName)
	}
	return wizardProfile(workspace, profileName, providerName)
}

// deleteProfile removes a named profile for the given provider and workspace.
func deleteProfile(workspace, profileName, providerName string) error {
	pc, err := LoadProfileConfig(workspace)
	if err != nil {
		return err
	}
	providerProfiles := pc.Profiles[providerName]
	if _, ok := providerProfiles[profileName]; !ok {
		return fmt.Errorf("profile %q not found for provider %s", profileName, providerName)
	}
	delete(providerProfiles, profileName)
	if len(providerProfiles) == 0 {
		delete(pc.Profiles, providerName)
	}
	if err := SaveProfileConfig(workspace, pc); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Deleted profile %q for %s\n", profileName, providerName)
	return nil
}

// effectiveCwd returns the working directory, preferring the shim-provided
// MITTENS_WSL_CWD over os.Getwd(). On WSL2 with Docker Desktop, the kernel
// can resolve the cwd through internal bind-mount paths that lose the
// original location; the shim knows the correct WSL path.  When no shim
// is involved, bind-mount paths are resolved via /proc/self/mountinfo.
func effectiveCwd() string {
	if v := os.Getenv("MITTENS_WSL_CWD"); v != "" {
		return v
	}
	cwd, _ := os.Getwd()
	return resolveWSLBindMount(cwd)
}

// detectWorkspace returns the git root of the current directory, or cwd.
// The result is passed through resolveWSLBindMount so that Docker Desktop's
// internal bind-mount paths are mapped back to stable WSL paths.
func detectWorkspace() string {
	out, err := captureCommand("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return effectiveCwd()
	}
	return resolveWSLBindMount(out)
}
