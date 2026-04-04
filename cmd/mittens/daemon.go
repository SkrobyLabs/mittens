package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func runDaemon(args []string) error {
	socketPath, filteredArgs, err := parseDaemonArgs(args)
	if err != nil {
		return err
	}

	app, err := newConfiguredApp(filteredArgs)
	if err != nil {
		return err
	}
	if app == nil {
		return nil
	}
	app.PoolMode = true
	app.DaemonMode = true
	app.runtimeSock = socketPath
	return app.Run()
}

func parseDaemonArgs(args []string) (string, []string, error) {
	socketPath := defaultRuntimeSocketPath()
	filtered := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			printDaemonHelp()
			return "", nil, nil
		case "--socket":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--socket requires an argument")
			}
			socketPath = strings.TrimSpace(args[i+1])
			i++
		default:
			filtered = append(filtered, args[i])
		}
	}
	if socketPath == "" {
		return "", nil, fmt.Errorf("runtime socket path must not be empty")
	}
	return socketPath, filtered, nil
}

func defaultRuntimeSocketPath() string {
	return filepath.Join(ConfigHome(), "runtime.sock")
}

func printDaemonHelp() {
	fmt.Println(`mittens daemon - Start a Kitchen runtime daemon

Usage: mittens daemon [--socket PATH] [mittens-flags...]

Options:
  --socket PATH   Unix socket path for the runtime daemon (default: ~/.mittens/runtime.sock)

Examples:
  mittens daemon
  mittens daemon --socket /var/run/mittens.sock --provider codex --no-build`)
}

func (a *App) startRuntimeBroker() error {
	if a == nil || a.broker == nil {
		return fmt.Errorf("runtime daemon broker is not configured")
	}
	socketPath := strings.TrimSpace(a.runtimeSock)
	if socketPath == "" {
		socketPath = defaultRuntimeSocketPath()
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("create runtime socket dir: %w", err)
	}
	a.broker.sockPath = socketPath
	a.brokerSock = socketPath
	a.runtimeSock = socketPath
	go func() {
		if err := a.broker.Serve(); err != nil && err != http.ErrServerClosed {
			logWarn("Runtime daemon broker: %v", err)
		}
	}()
	return nil
}

func (a *App) runRuntimeDaemon() error {
	if a == nil || a.broker == nil {
		return fmt.Errorf("runtime daemon not configured")
	}
	if a.runtimeSock == "" {
		return fmt.Errorf("runtime daemon requires a socket path")
	}
	if a.brokerToken == "" || a.poolToken == "" {
		return fmt.Errorf("runtime daemon requires broker and pool tokens")
	}
	if err := writeRuntimeMetadata(runtimeMetadata{
		SocketPath:  a.runtimeSock,
		BrokerToken: a.brokerToken,
		PoolToken:   a.poolToken,
		SessionID:   a.poolSession,
		Provider:    a.Provider.Name,
		Model:       a.currentRuntimeModel(),
	}); err != nil {
		return fmt.Errorf("write runtime metadata: %w", err)
	}
	defer func() {
		_ = clearRuntimeMetadata()
	}()

	fmt.Fprintf(os.Stderr, "Mittens runtime daemon ready\n")
	fmt.Fprintf(os.Stderr, "MITTENS_RUNTIME_SOCKET=%s\n", a.runtimeSock)
	fmt.Fprintf(os.Stderr, "MITTENS_BROKER_TOKEN=%s\n", a.brokerToken)
	fmt.Fprintf(os.Stderr, "MITTENS_POOL_TOKEN=%s\n", a.poolToken)
	fmt.Fprintf(os.Stderr, "MITTENS_SESSION_ID=%s\n", a.poolSession)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	<-sigCh
	return nil
}
