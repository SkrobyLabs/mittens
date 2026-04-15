package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

const supervisedRuntimeReadyTimeout = 5 * time.Second

type supervisedDaemon struct {
	mu          sync.RWMutex
	paths       KitchenPaths
	project     ProjectPaths
	provider    string
	mittensPath string
	out         io.Writer
	socketPath  string
	pidPath     string
	brokerToken string
	poolToken   string
	cmd         *exec.Cmd
	waitCh      chan error
	client      pool.RuntimeAPI
	restartFn   func() error
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
	roles := make([]string, 0, len(cfg.RoleProviders))
	for role := range cfg.RoleProviders {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		policy := cfg.RoleProviders[role]
		role = normalizeRoutingRole(role)
		if err := collectServeProvidersForPolicy(seen, &providers, "roleProviders."+role, policy); err != nil {
			return nil, err
		}
	}
	seats := make([]string, 0, len(cfg.CouncilSeatProviders))
	for seat := range cfg.CouncilSeatProviders {
		seats = append(seats, seat)
	}
	sort.Strings(seats)
	for _, seat := range seats {
		policy := cfg.CouncilSeatProviders[seat]
		seat = normalizeCouncilSeat(seat)
		if seat == "" {
			continue
		}
		if err := collectServeProvidersForPolicy(seen, &providers, "councilSeatProviders."+seat, policy); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

func collectServeProvidersForPolicy(seen map[string]bool, providers *[]string, scope string, policy ProviderPolicy) error {
	for idx, providerName := range append(append([]string(nil), policy.Prefer...), policy.Fallback...) {
		provider, err := normalizeServeProvider(providerName)
		if err != nil {
			return fmt.Errorf("%s entry %d provider %q: %w", scope, idx, providerName, err)
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

	state := &supervisedDaemon{
		paths:       paths,
		project:     project,
		provider:    provider,
		mittensPath: mittensPath,
		out:         out,
		socketPath:  supervisedRuntimeSocketPath(paths, provider),
		pidPath:     supervisedRuntimePIDPath(paths, provider),
	}
	if err := state.start(); err != nil {
		return nil, err
	}
	return state, nil
}

func (d *supervisedDaemon) start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.startLocked()
}

func (d *supervisedDaemon) startLocked() error {
	if d == nil {
		return fmt.Errorf("supervised daemon not configured")
	}
	if d.restartFn != nil {
		return d.restartFn()
	}
	if strings.TrimSpace(d.mittensPath) == "" {
		return fmt.Errorf("mittens binary path must not be empty")
	}
	if err := cleanupSupervisedDaemon(d.paths, d.provider); err != nil {
		return err
	}

	cmd := exec.Command(d.mittensPath, "daemon", "--socket", d.socketPath, "--provider", d.provider)
	cmd.Env = append(os.Environ(),
		"MITTENS_POOL_SESSION="+supervisedPoolSession(d.project),
		"MITTENS_POOL_STATE_DIR="+supervisedPoolStateDir(d.project),
	)
	cmd.Stdout = d.out
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(d.pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o644); err != nil {
		_ = terminateProcess(cmd.Process.Pid, 3*time.Second)
		return err
	}
	d.cmd = cmd
	d.waitCh = waitCh
	d.brokerToken = ""
	d.poolToken = ""
	go func() {
		waitCh <- cmd.Wait()
	}()
	go streamDaemonOutput(stderr, d.out, d)

	ctx, cancel := context.WithTimeout(context.Background(), supervisedRuntimeReadyTimeout)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, d); err != nil {
		_ = terminateProcess(cmd.Process.Pid, 3*time.Second)
		_ = os.Remove(d.pidPath)
		_ = os.Remove(d.socketPath)
		return err
	}
	d.client = newRuntimeClient(d.socketPath, d.brokerToken, d.poolToken)
	return nil
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
	return d
}

func (d *supervisedDaemon) HostPool() []PoolKey {
	if d == nil {
		return nil
	}
	return []PoolKey{{Provider: d.provider}}
}

func (d *supervisedDaemon) Stop() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmd == nil || d.cmd.Process == nil {
		d.client = nil
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
	d.client = nil
	return err
}

func (d *supervisedDaemon) currentClient() pool.RuntimeAPI {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client
}

func (d *supervisedDaemon) restartIfUnavailable(err error) error {
	if d == nil || !isSupervisedRuntimeUnavailableError(err) {
		return err
	}
	if restartErr := d.start(); restartErr != nil {
		return fmt.Errorf("%w (restart: %v)", err, restartErr)
	}
	return nil
}

func isSupervisedRuntimeUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "dial unix") &&
		(strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "connection refused"))
}

func (d *supervisedDaemon) SpawnWorker(ctx context.Context, spec pool.WorkerSpec) (string, string, error) {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return "", "", err
		}
		client = d.currentClient()
	}
	name, id, err := client.SpawnWorker(ctx, spec)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return "", "", restartErr
	}
	if err == nil {
		return name, id, nil
	}
	return d.currentClient().SpawnWorker(ctx, spec)
}

func (d *supervisedDaemon) KillWorker(ctx context.Context, workerID string) error {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return err
		}
		client = d.currentClient()
	}
	err := client.KillWorker(ctx, workerID)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return restartErr
	}
	if err == nil {
		return nil
	}
	return d.currentClient().KillWorker(ctx, workerID)
}

func (d *supervisedDaemon) ListContainers(ctx context.Context, sessionID string) ([]pool.ContainerInfo, error) {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return nil, err
		}
		client = d.currentClient()
	}
	containers, err := client.ListContainers(ctx, sessionID)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return nil, restartErr
	}
	if err == nil {
		return containers, nil
	}
	return d.currentClient().ListContainers(ctx, sessionID)
}

func (d *supervisedDaemon) RecycleWorker(ctx context.Context, workerID string) error {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return err
		}
		client = d.currentClient()
	}
	err := client.RecycleWorker(ctx, workerID)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return restartErr
	}
	if err == nil {
		return nil
	}
	return d.currentClient().RecycleWorker(ctx, workerID)
}

func (d *supervisedDaemon) GetWorkerActivity(ctx context.Context, workerID string) (*pool.WorkerActivity, error) {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return nil, err
		}
		client = d.currentClient()
	}
	activity, err := client.GetWorkerActivity(ctx, workerID)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return nil, restartErr
	}
	if err == nil {
		return activity, nil
	}
	return d.currentClient().GetWorkerActivity(ctx, workerID)
}

func (d *supervisedDaemon) GetWorkerTranscript(ctx context.Context, workerID string) ([]pool.WorkerActivityRecord, error) {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return nil, err
		}
		client = d.currentClient()
	}
	transcript, err := client.GetWorkerTranscript(ctx, workerID)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return nil, restartErr
	}
	if err == nil {
		return transcript, nil
	}
	return d.currentClient().GetWorkerTranscript(ctx, workerID)
}

func (d *supervisedDaemon) SubscribeEvents(ctx context.Context) (<-chan pool.RuntimeEvent, error) {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return nil, err
		}
		client = d.currentClient()
	}
	events, err := client.SubscribeEvents(ctx)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return nil, restartErr
	}
	if err == nil {
		return events, nil
	}
	return d.currentClient().SubscribeEvents(ctx)
}

func (d *supervisedDaemon) SubmitAssignment(ctx context.Context, workerID string, assignment pool.Assignment) error {
	client := d.currentClient()
	if client == nil {
		if err := d.start(); err != nil {
			return err
		}
		client = d.currentClient()
	}
	err := client.SubmitAssignment(ctx, workerID, assignment)
	if restartErr := d.restartIfUnavailable(err); restartErr != nil {
		return restartErr
	}
	if err == nil {
		return nil
	}
	return d.currentClient().SubmitAssignment(ctx, workerID, assignment)
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
