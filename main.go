package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Skroby/mittens/extensions/registry"

	// Blank imports trigger init() registration for extensions with resolvers.
	_ "github.com/Skroby/mittens/extensions/aws"
	_ "github.com/Skroby/mittens/extensions/azure"
	firewallext "github.com/Skroby/mittens/extensions/firewall"
	_ "github.com/Skroby/mittens/extensions/gcp"
	_ "github.com/Skroby/mittens/extensions/gh"
	_ "github.com/Skroby/mittens/extensions/kubectl"
	_ "github.com/Skroby/mittens/extensions/mcp"
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
	// flags consume a value (e.g. --channel-sock PATH), so any bare path
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
		switch args[0] {
		case "init", "--init":
			return runInit()
		case "logs":
			return runLogs(args[1:])
		case "clean":
			return runClean(args[1:])
		case "--version", "-V":
			fmt.Printf("mittens %s (commit: %s, built: %s)\n", version, commit, date)
			return nil
		}
	}

	app := &App{
		ImageName:       "mittens",
		ImageTag:        "latest",
		worktreeOrigins: make(map[string]string),
		worktreeRepos:   make(map[string]string),
	}

	// 1. Load built-in extensions from embedded YAML.
	exts, err := registry.LoadExtensions(extensionYAMLs)
	if err != nil {
		return fmt.Errorf("loading extensions: %w", err)
	}

	// 2. Load external (subprocess) extensions from ~/.mittens/extensions/.
	home := homeDir()
	extDir := filepath.Join(home, ".mittens", "extensions")
	externals, err := registry.LoadExternalExtensions(extDir)
	if err != nil {
		logWarn("Loading external extensions: %v", err)
	}
	exts = append(exts, externals...)
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

	// 4. Load project config unless --no-config.
	var configArgs []string
	if !noConfig {
		workspace := detectWorkspace()
		configArgs, err = LoadProjectConfig(workspace)
		if err != nil {
			return fmt.Errorf("loading project config: %w", err)
		}
		if len(configArgs) > 0 {
			logInfo("Loaded project config for %s", ProjectDir(workspace))
		}
	}

	// 5. Merge: config flags first, then CLI flags (last-wins semantics).
	merged := append(configArgs, args...)

	// 6. Parse all flags from the merged list.
	if err := app.ParseFlags(merged); err != nil {
		return err
	}

	app.NoConfig = noConfig

	// 7. Run.
	return app.Run()
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
	exts, err := registry.LoadExtensions(extensionYAMLs)
	if err != nil {
		return fmt.Errorf("loading extensions for wizard: %w", err)
	}
	return runWizard(exts)
}

// detectWorkspace returns the git root of the current directory, or cwd.
func detectWorkspace() string {
	out, err := captureCommand("git", "rev-parse", "--show-toplevel")
	if err != nil {
		cwd, _ := os.Getwd()
		return cwd
	}
	return out
}
