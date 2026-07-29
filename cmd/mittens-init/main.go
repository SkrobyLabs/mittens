// mittens-init is the container-side entrypoint binary.
// It runs as PID 1 and handles two-phase container setup:
//   - Phase 1 (root): DinD, Docker socket, FQDN-filtering proxy, iptables, privilege drop
//   - Phase 2 (user): config staging, JSON settings, trusted dirs, hooks, credential sync
//
// It also provides busybox-style symlink dispatch for xdg-open (URL forwarding),
// xclip (clipboard), and notify.sh (notifications) — all communicating with the
// host-side broker.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	// Internal proxy subprocess mode — runs the forward proxy and blocks forever.
	// Started by phase 1 as a child process so the proxy survives the
	// syscall.Exec privilege drop.
	if os.Getenv("MITTENS_PROXY_MODE") == "1" {
		runProxyMain()
		return
	}

	// Internal credsync subprocess mode — runs the credential sync daemon.
	// Started by phase 2 as a child process so it survives the
	// syscall.Exec that launches the AI CLI.
	if os.Getenv("MITTENS_CREDSYNC_MODE") == "1" {
		runCredsyncMain()
		return
	}

	// Busybox-style dispatch based on argv[0] basename.
	base := filepath.Base(os.Args[0])
	switch base {
	case "xdg-open":
		os.Exit(runOpenURL())
	case "xclip":
		os.Exit(runClipboard())
	case "notify.sh":
		os.Exit(runNotify())
	case "clipboard-x11-sync.sh":
		os.Exit(runX11ClipboardSync())
	case "mittens-mcp-proxy":
		os.Exit(runMCPProxy())
	}

	// Default: run as container entrypoint.
	if err := runEntrypoint(); err != nil {
		fmt.Fprintf(os.Stderr, "[mittens-init] %v\n", err)
		os.Exit(1)
	}
}

func runEntrypoint() error {
	cfg := loadConfig()

	if os.Getuid() == 0 {
		// Phase 1: root-level setup, then re-exec as AI user.
		return runPhase1(cfg)
	}

	// Phase 2: user-level setup, then exec the AI CLI.
	return runPhase2(cfg)
}

func logInfo(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[mittens] "+format+"\n", args...)
}

func logWarn(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[mittens] Warning: "+format+"\n", args...)
}

func logStartupDuration(cfg *config, label string, started time.Time) {
	if cfg.Verbose {
		logInfo("Startup timing: %s took %s", label, time.Since(started).Round(time.Millisecond))
	}
}

func logContainerStartDelay(cfg *config) {
	if !cfg.Verbose {
		return
	}
	raw := os.Getenv("MITTENS_LAUNCH_STARTED_NS")
	startedNS, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || startedNS <= 0 {
		return
	}
	logInfo("Startup timing: Docker create/start took %s", time.Since(time.Unix(0, startedNS)).Round(time.Millisecond))
}
