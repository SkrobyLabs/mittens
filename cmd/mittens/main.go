package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
			if hasSubFlag(args, "--defaults") {
				return runInitDefaults()
			}
			return runInit()
		case "help":
			return runHelp()
		case "logs":
			return runLogs(args[1:])
		case "clean":
			return runClean(args[1:])
		case "extension":
			return runExtension(args[1:])
		}

		// Flag-style aliases (can appear anywhere before "--").
		if hasSubFlag(args, "--init") {
			if hasSubFlag(args, "--defaults") {
				return runInitDefaults()
			}
			return runInit()
		}
		if hasSubFlag(args, "--help") || hasSubFlag(args, "-h") {
			return runHelp()
		}
		if hasSubFlag(args, "--version") || hasSubFlag(args, "-V") {
			fmt.Printf("mittens %s (commit: %s, built: %s)\n", version, commit, date)
			return nil
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
	home := homeDir()
	bundledDir := filepath.Join(runtimeRoot(), "extensions")
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	exts, err := registry.LoadAllExtensions(bundledDir, userExtDir, extensionYAMLs)
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

	// 3. Pre-scan for --no-config (needed before loading project config).
	noConfig := false
	for _, a := range args {
		if a == "--no-config" {
			noConfig = true
			break
		}
	}

	// 4. Load user defaults and project config unless --no-config.
	var userArgs []string
	var configArgs []string
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

	app.NoConfig = noConfig

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

// runInitDefaults launches the user-wide defaults wizard directly.
func runInitDefaults() error {
	return wizardUserDefaults()
}

// runHelp loads extensions and prints the help text.
func runHelp() error {
	home := homeDir()
	bundledDir := filepath.Join(runtimeRoot(), "extensions")
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	exts, err := registry.LoadAllExtensions(bundledDir, userExtDir, extensionYAMLs)
	if err != nil {
		exts = nil // best-effort: show help without extensions
	}
	printHelp(exts)
	return nil
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
	switch name {
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
		if a == "-f" || a == "--follow" {
			follow = true
		}
	}

	if follow {
		cmd := exec.Command("tail", "-f", logPath)
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
	home := homeDir()
	bundledDir := filepath.Join(runtimeRoot(), "extensions")
	userExtDir := filepath.Join(home, ".mittens", "extensions")
	exts, err := registry.LoadAllExtensions(bundledDir, userExtDir, extensionYAMLs)
	if err != nil {
		return fmt.Errorf("loading extensions for wizard: %w", err)
	}
	return runWizard(exts)
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
