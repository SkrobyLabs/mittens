package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const supervisedRuntimeReadyTimeout = 5 * time.Second

type supervisedDaemon struct {
	provider    string
	socketPath  string
	pidPath     string
	brokerToken string
	poolToken   string
	cmd         *exec.Cmd
	waitCh      chan error
}

func supervisedPoolSession(project ProjectPaths) string {
	return "kitchen-" + strings.TrimSpace(project.Key)
}

func supervisedPoolStateDir(project ProjectPaths) string {
	return filepath.Join(project.PoolsDir, defaultPoolStateName)
}

func configuredServeProviders(cfg KitchenConfig) ([]string, error) {
	seen := make(map[string]bool)
	var providers []string
	if err := collectServeProviders(seen, &providers, "routing", cfg.Routing); err != nil {
		return nil, err
	}
	for role, rule := range cfg.RoleDefaults {
		role = normalizeRoutingRole(role)
		if err := collectServeProvidersForRule(seen, &providers, "roleDefaults."+role, rule); err != nil {
			return nil, err
		}
	}
	for role, routing := range cfg.RoleRouting {
		role = normalizeRoutingRole(role)
		if err := collectServeProviders(seen, &providers, "roleRouting."+role, routing); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

func collectServeProviders(seen map[string]bool, providers *[]string, scope string, routing map[Complexity]RoutingRule) error {
	for _, complexity := range allComplexities {
		rule, ok := routing[complexity]
		if !ok {
			continue
		}
		keys := append([]PoolKey(nil), rule.Prefer...)
		keys = append(keys, rule.Fallback...)
		for _, key := range keys {
			provider, err := normalizeServeProvider(key.Provider)
			if err != nil {
				return fmt.Errorf("%s.%s provider %q: %w", scope, complexity, key.Provider, err)
			}
			if !seen[provider] {
				seen[provider] = true
				*providers = append(*providers, provider)
			}
		}
	}
	return nil
}

func collectServeProvidersForRule(seen map[string]bool, providers *[]string, scope string, rule RoutingRule) error {
	keys := append([]PoolKey(nil), rule.Prefer...)
	keys = append(keys, rule.Fallback...)
	for _, key := range keys {
		provider, err := normalizeServeProvider(key.Provider)
		if err != nil {
			return fmt.Errorf("%s provider %q: %w", scope, key.Provider, err)
		}
		if !seen[provider] {
			seen[provider] = true
			*providers = append(*providers, provider)
		}
	}
	return nil
}

func normalizeServeProvider(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "anthropic":
		return "claude", nil
	case "codex", "openai":
		return "codex", nil
	case "gemini", "google":
		return "gemini", nil
	case "":
		return "", fmt.Errorf("provider must not be empty")
	default:
		return "", fmt.Errorf("unknown provider %q (available: claude, codex, gemini)", name)
	}
}

func runtimeSupervisorDir(paths KitchenPaths) string {
	return filepath.Join(paths.HomeDir, "runtime")
}

func supervisedRuntimeSocketPath(paths KitchenPaths, provider string) string {
	return filepath.Join(runtimeSupervisorDir(paths), provider+".sock")
}

func supervisedRuntimePIDPath(paths KitchenPaths, provider string) string {
	return filepath.Join(runtimeSupervisorDir(paths), provider+".pid")
}

func resolveMittensBinary() (string, error) {
	self, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(self), "mittens")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return exec.LookPath("mittens")
}

func cleanupSupervisedDaemon(paths KitchenPaths, provider string) error {
	pidPath := supervisedRuntimePIDPath(paths, provider)
	socketPath := supervisedRuntimeSocketPath(paths, provider)

	data, err := os.ReadFile(pidPath)
	if err == nil {
		pid := strings.TrimSpace(string(data))
		if pid != "" {
			if n, convErr := strconvAtoi(pid); convErr == nil && n > 0 && processAlive(n) {
				_ = terminateProcess(n, 3*time.Second)
			}
		}
	}
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func startSupervisedDaemon(paths KitchenPaths, project ProjectPaths, provider, mittensPath string, out io.Writer) (*supervisedDaemon, error) {
	provider, err := normalizeServeProvider(provider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(mittensPath) == "" {
		return nil, fmt.Errorf("mittens binary path must not be empty")
	}
	if err := os.MkdirAll(runtimeSupervisorDir(paths), 0o755); err != nil {
		return nil, err
	}
	if err := cleanupSupervisedDaemon(paths, provider); err != nil {
		return nil, err
	}

	socketPath := supervisedRuntimeSocketPath(paths, provider)
	pidPath := supervisedRuntimePIDPath(paths, provider)
	cmd := exec.Command(mittensPath, "daemon", "--socket", socketPath, "--provider", provider)
	cmd.Env = append(os.Environ(),
		"MITTENS_POOL_SESSION="+supervisedPoolSession(project),
		"MITTENS_POOL_STATE_DIR="+supervisedPoolStateDir(project),
	)
	cmd.Stdout = out
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	state := &supervisedDaemon{
		provider:   provider,
		socketPath: socketPath,
		pidPath:    pidPath,
		cmd:        cmd,
		waitCh:     make(chan error, 1),
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644); err != nil {
		_ = terminateProcess(cmd.Process.Pid, 3*time.Second)
		return nil, err
	}
	go func() {
		state.waitCh <- cmd.Wait()
	}()
	go streamDaemonOutput(stderr, out, state)

	ctx, cancel := context.WithTimeout(context.Background(), supervisedRuntimeReadyTimeout)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, state); err != nil {
		_ = state.Stop()
		return nil, err
	}
	return state, nil
}

func streamDaemonOutput(r io.Reader, out io.Writer, state *supervisedDaemon) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		parseDaemonStartupLine(line, state)
		fmt.Fprintln(out, line)
	}
}

func parseDaemonStartupLine(line string, state *supervisedDaemon) {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "MITTENS_BROKER_TOKEN="):
		state.brokerToken = strings.TrimSpace(strings.TrimPrefix(line, "MITTENS_BROKER_TOKEN="))
	case strings.HasPrefix(line, "MITTENS_POOL_TOKEN="):
		state.poolToken = strings.TrimSpace(strings.TrimPrefix(line, "MITTENS_POOL_TOKEN="))
	}
}

func waitForSupervisedDaemonReady(ctx context.Context, state *supervisedDaemon) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if strings.TrimSpace(state.brokerToken) != "" && strings.TrimSpace(state.poolToken) != "" && strings.TrimSpace(state.socketPath) != "" {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", state.socketPath)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for supervised daemon readiness")
		case err := <-state.waitCh:
			if err == nil {
				err = fmt.Errorf("supervised daemon exited before readiness")
			}
			return err
		case <-ticker.C:
		}
	}
}

func (d *supervisedDaemon) RuntimeClient() pool.RuntimeAPI {
	if d == nil {
		return nil
	}
	return newRuntimeClient(d.socketPath, d.brokerToken, d.poolToken)
}

func (d *supervisedDaemon) HostPool() []PoolKey {
	if d == nil {
		return nil
	}
	return []PoolKey{{Provider: d.provider}}
}

func (d *supervisedDaemon) Stop() error {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return nil
	}
	pid := d.cmd.Process.Pid
	err := terminateProcess(pid, 3*time.Second)
	select {
	case waitErr := <-d.waitCh:
		if err == nil {
			err = waitErr
		}
	default:
	}
	if rmErr := os.Remove(d.pidPath); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}
	if rmErr := os.Remove(d.socketPath); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}
	return err
}

func terminateProcess(pid int, grace time.Duration) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !isProcessDone(err) {
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil && !isProcessDone(err) {
		return err
	}
	return nil
}

func isProcessDone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "process already finished") || strings.Contains(msg, "no such process")
}

func strconvAtoi(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func (d *supervisedDaemon) String() string {
	if d == nil {
		return ""
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "provider=%s socket=%s", d.provider, d.socketPath)
	return buf.String()
}
