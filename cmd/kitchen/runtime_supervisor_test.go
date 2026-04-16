package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type supervisedRuntimeStub struct {
	listFn         func(context.Context, string) ([]pool.ContainerInfo, error)
	spawnFn        func(context.Context, pool.WorkerSpec) (string, string, error)
	killFn         func(context.Context, string) error
	recycleFn      func(context.Context, string) error
	activityFn     func(context.Context, string) (*pool.WorkerActivity, error)
	transcriptFn   func(context.Context, string) ([]pool.WorkerActivityRecord, error)
	subscribeFn    func(context.Context) (<-chan pool.RuntimeEvent, error)
	submitAssignFn func(context.Context, string, pool.Assignment) error
}

func (s *supervisedRuntimeStub) SpawnWorker(ctx context.Context, spec pool.WorkerSpec) (string, string, error) {
	if s.spawnFn != nil {
		return s.spawnFn(ctx, spec)
	}
	return "", "", nil
}

func (s *supervisedRuntimeStub) KillWorker(ctx context.Context, workerID string) error {
	if s.killFn != nil {
		return s.killFn(ctx, workerID)
	}
	return nil
}

func (s *supervisedRuntimeStub) ListContainers(ctx context.Context, sessionID string) ([]pool.ContainerInfo, error) {
	if s.listFn != nil {
		return s.listFn(ctx, sessionID)
	}
	return nil, nil
}

func (s *supervisedRuntimeStub) RecycleWorker(ctx context.Context, workerID string) error {
	if s.recycleFn != nil {
		return s.recycleFn(ctx, workerID)
	}
	return nil
}

func (s *supervisedRuntimeStub) GetWorkerActivity(ctx context.Context, workerID string) (*pool.WorkerActivity, error) {
	if s.activityFn != nil {
		return s.activityFn(ctx, workerID)
	}
	return nil, nil
}

func (s *supervisedRuntimeStub) GetWorkerTranscript(ctx context.Context, workerID string) ([]pool.WorkerActivityRecord, error) {
	if s.transcriptFn != nil {
		return s.transcriptFn(ctx, workerID)
	}
	return nil, nil
}

func (s *supervisedRuntimeStub) SubscribeEvents(ctx context.Context) (<-chan pool.RuntimeEvent, error) {
	if s.subscribeFn != nil {
		return s.subscribeFn(ctx)
	}
	ch := make(chan pool.RuntimeEvent)
	close(ch)
	return ch, nil
}

func (s *supervisedRuntimeStub) SubmitAssignment(ctx context.Context, workerID string, assignment pool.Assignment) error {
	if s.submitAssignFn != nil {
		return s.submitAssignFn(ctx, workerID, assignment)
	}
	return nil
}

func TestNormalizeServeProvider(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "claude", want: "claude", ok: true},
		{in: "anthropic", want: "claude", ok: true},
		{in: "codex", want: "codex", ok: true},
		{in: "openai", want: "codex", ok: true},
		{in: "gemini", want: "gemini", ok: true},
		{in: "google", want: "gemini", ok: true},
		{in: "nope", ok: false},
	}
	for _, tc := range tests {
		got, err := normalizeServeProvider(tc.in)
		if tc.ok {
			if err != nil {
				t.Fatalf("normalizeServeProvider(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeServeProvider(%q) = %q, want %q", tc.in, got, tc.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("normalizeServeProvider(%q) unexpectedly succeeded with %q", tc.in, got)
		}
	}
}

func TestParseDaemonStartupLine(t *testing.T) {
	state := &supervisedDaemon{}
	parseDaemonStartupLine("MITTENS_BROKER_TOKEN=broker-secret", state)
	parseDaemonStartupLine("MITTENS_POOL_TOKEN=pool-secret", state)
	if state.brokerToken != "broker-secret" {
		t.Fatalf("brokerToken = %q, want broker-secret", state.brokerToken)
	}
	if state.poolToken != "pool-secret" {
		t.Fatalf("poolToken = %q, want pool-secret", state.poolToken)
	}
}

func TestCleanupSupervisedDaemonRemovesStaleFiles(t *testing.T) {
	paths := newKitchenTestPaths(t)
	project, err := paths.Project("/tmp/example-repo")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	provider := "codex"
	if err := os.MkdirAll(runtimeSupervisorDir(paths), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	pidPath := supervisedRuntimePIDPath(paths, project, provider)
	socketPath := supervisedRuntimeSocketPath(paths, project, provider)
	if err := os.WriteFile(pidPath, []byte("0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile socket: %v", err)
	}
	if err := cleanupSupervisedDaemon(paths, project, provider); err != nil {
		t.Fatalf("cleanupSupervisedDaemon: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file still exists, stat err=%v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists, stat err=%v", err)
	}
}

func TestSupervisedRuntimePathsAreProjectScoped(t *testing.T) {
	paths := newKitchenTestPaths(t)
	projectA, err := paths.Project("/tmp/repo-a")
	if err != nil {
		t.Fatalf("Project repo-a: %v", err)
	}
	projectB, err := paths.Project("/tmp/repo-b")
	if err != nil {
		t.Fatalf("Project repo-b: %v", err)
	}
	provider := "claude"

	sockA := supervisedRuntimeSocketPath(paths, projectA, provider)
	sockB := supervisedRuntimeSocketPath(paths, projectB, provider)
	if sockA == sockB {
		t.Fatalf("socket paths collide: %q", sockA)
	}

	pidA := supervisedRuntimePIDPath(paths, projectA, provider)
	pidB := supervisedRuntimePIDPath(paths, projectB, provider)
	if pidA == pidB {
		t.Fatalf("pid paths collide: %q", pidA)
	}
}

func TestCleanupSupervisedDaemonDoesNotRemoveOtherProjectFiles(t *testing.T) {
	paths := newKitchenTestPaths(t)
	projectA, err := paths.Project("/tmp/repo-a")
	if err != nil {
		t.Fatalf("Project repo-a: %v", err)
	}
	projectB, err := paths.Project("/tmp/repo-b")
	if err != nil {
		t.Fatalf("Project repo-b: %v", err)
	}
	provider := "claude"

	if err := os.MkdirAll(runtimeSupervisorDir(paths), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write stale files for both projects.
	for _, p := range []ProjectPaths{projectA, projectB} {
		pid := supervisedRuntimePIDPath(paths, p, provider)
		sock := supervisedRuntimeSocketPath(paths, p, provider)
		if err := os.WriteFile(pid, []byte("0\n"), 0o644); err != nil {
			t.Fatalf("WriteFile pid %s: %v", p.Key, err)
		}
		if err := os.WriteFile(sock, []byte("stale"), 0o644); err != nil {
			t.Fatalf("WriteFile sock %s: %v", p.Key, err)
		}
	}

	// Clean up only project A.
	if err := cleanupSupervisedDaemon(paths, projectA, provider); err != nil {
		t.Fatalf("cleanupSupervisedDaemon: %v", err)
	}

	// Project A files must be gone.
	if _, err := os.Stat(supervisedRuntimePIDPath(paths, projectA, provider)); !os.IsNotExist(err) {
		t.Fatal("project A pid file still exists after cleanup")
	}
	if _, err := os.Stat(supervisedRuntimeSocketPath(paths, projectA, provider)); !os.IsNotExist(err) {
		t.Fatal("project A socket file still exists after cleanup")
	}

	// Project B files must be untouched.
	if _, err := os.Stat(supervisedRuntimePIDPath(paths, projectB, provider)); err != nil {
		t.Fatalf("project B pid file missing after cleanup of A: %v", err)
	}
	if _, err := os.Stat(supervisedRuntimeSocketPath(paths, projectB, provider)); err != nil {
		t.Fatalf("project B socket file missing after cleanup of A: %v", err)
	}
}

func TestWaitForSupervisedDaemonReady(t *testing.T) {
	socketPath, closeFn := startUnixRuntimeTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer closeFn()

	state := &supervisedDaemon{
		socketPath:  socketPath,
		brokerToken: "broker-secret",
		poolToken:   "pool-secret",
		waitCh:      make(chan error, 1),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, state); err != nil {
		t.Fatalf("waitForSupervisedDaemonReady: %v", err)
	}
}

func TestWaitForSupervisedDaemonReadyFailsWhenProcessExits(t *testing.T) {
	state := &supervisedDaemon{
		socketPath:  "/tmp/nowhere.sock",
		brokerToken: "broker-secret",
		poolToken:   "pool-secret",
		waitCh:      make(chan error, 1),
	}
	state.waitCh <- context.DeadlineExceeded
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForSupervisedDaemonReady(ctx, state); err == nil {
		t.Fatal("waitForSupervisedDaemonReady unexpectedly succeeded")
	}
}

func TestConfiguredServeProvidersDeduplicatesAndNormalizesRoutingProviders(t *testing.T) {
	providers, err := configuredServeProviders(KitchenConfig{
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {
				Prefer:   []string{"anthropic"},
				Fallback: []string{"openai", "claude"},
			},
			"reviewer": {
				Prefer:   []string{"codex"},
				Fallback: []string{"gemini"},
			},
			"implementer": {
				Prefer: []string{"anthropic"},
			},
		},
		CouncilSeatProviders: map[string]ProviderPolicy{
			"B": {Prefer: []string{"google"}},
		},
	})
	if err != nil {
		t.Fatalf("configuredServeProviders: %v", err)
	}
	want := []string{"claude", "codex", "gemini"}
	if len(providers) != len(want) {
		t.Fatalf("providers = %v, want %v", providers, want)
	}
	for i := range want {
		if providers[i] != want[i] {
			t.Fatalf("providers = %v, want %v", providers, want)
		}
	}
}

func TestConfiguredServeProvidersIncludesCouncilSeatOnlyProviders(t *testing.T) {
	providers, err := configuredServeProviders(KitchenConfig{
		RoleProviders: map[string]ProviderPolicy{
			defaultRoutingRole: {Prefer: []string{"anthropic"}},
		},
		CouncilSeatProviders: map[string]ProviderPolicy{
			"B": {Prefer: []string{"openai"}},
		},
	})
	if err != nil {
		t.Fatalf("configuredServeProviders: %v", err)
	}
	if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
		t.Fatalf("providers = %v, want [claude codex]", providers)
	}
}

func TestSupervisedDaemonListContainersRestartsOnUnavailableSocket(t *testing.T) {
	unavailable := errors.New(`list containers: Get "http://runtime/v1/workers?sessionId=kitchen-test": dial unix /tmp/claude.sock: connect: no such file or directory`)
	healthy := &supervisedRuntimeStub{
		listFn: func(_ context.Context, sessionID string) ([]pool.ContainerInfo, error) {
			if sessionID != "kitchen-test" {
				t.Fatalf("sessionID = %q, want kitchen-test", sessionID)
			}
			return []pool.ContainerInfo{{WorkerID: "w-1", ContainerID: "c-1", State: "running", Status: pool.WorkerIdle}}, nil
		},
	}
	d := &supervisedDaemon{
		client: &supervisedRuntimeStub{
			listFn: func(context.Context, string) ([]pool.ContainerInfo, error) {
				return nil, unavailable
			},
		},
	}
	restarts := 0
	d.restartFn = func() error {
		restarts++
		d.client = healthy
		return nil
	}

	containers, err := d.ListContainers(context.Background(), "kitchen-test")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("restart count = %d, want 1", restarts)
	}
	if len(containers) != 1 || containers[0].WorkerID != "w-1" {
		t.Fatalf("containers = %+v, want worker w-1", containers)
	}
}

func TestSupervisedDaemonSpawnWorkerRestartsWhenHealthCheckFails(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}

	healthy := &supervisedRuntimeStub{
		spawnFn: func(_ context.Context, spec pool.WorkerSpec) (string, string, error) {
			if spec.ID != "w-1" {
				t.Fatalf("spec.ID = %q, want w-1", spec.ID)
			}
			return "worker-1", "container-1", nil
		},
	}
	d := &supervisedDaemon{
		pidPath:    pidPath,
		socketPath: filepath.Join(tmp, "runtime.sock"),
		client: &supervisedRuntimeStub{
			spawnFn: func(context.Context, pool.WorkerSpec) (string, string, error) {
				t.Fatal("stale client should not be used")
				return "", "", nil
			},
		},
	}
	restarts := 0
	d.restartFn = func() error {
		restarts++
		d.client = healthy
		return nil
	}

	name, id, err := d.SpawnWorker(context.Background(), pool.WorkerSpec{ID: "w-1"})
	if err != nil {
		t.Fatalf("SpawnWorker: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("restart count = %d, want 1", restarts)
	}
	if name != "worker-1" || id != "container-1" {
		t.Fatalf("spawn result = (%q, %q), want (worker-1, container-1)", name, id)
	}
}

func TestSupervisedDaemonListContainersRestartsWhenSocketRefusesConnections(t *testing.T) {
	tmp := t.TempDir()
	socketPath := filepath.Join(tmp, "runtime.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	_ = ln.Close()

	pidPath := filepath.Join(tmp, "daemon.pid")
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		t.Fatalf("WriteFile pid: %v", err)
	}

	healthy := &supervisedRuntimeStub{
		listFn: func(_ context.Context, sessionID string) ([]pool.ContainerInfo, error) {
			if sessionID != "kitchen-test" {
				t.Fatalf("sessionID = %q, want kitchen-test", sessionID)
			}
			return []pool.ContainerInfo{{WorkerID: "w-2", ContainerID: "c-2", State: "running", Status: pool.WorkerIdle}}, nil
		},
	}
	d := &supervisedDaemon{
		pidPath:    pidPath,
		socketPath: socketPath,
		client: &supervisedRuntimeStub{
			listFn: func(context.Context, string) ([]pool.ContainerInfo, error) {
				t.Fatal("stale client should not be used")
				return nil, nil
			},
		},
	}
	restarts := 0
	d.restartFn = func() error {
		restarts++
		d.client = healthy
		d.pidPath = ""
		d.socketPath = ""
		return nil
	}

	containers, err := d.ListContainers(context.Background(), "kitchen-test")
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if restarts != 1 {
		t.Fatalf("restart count = %d, want 1", restarts)
	}
	if len(containers) != 1 || containers[0].WorkerID != "w-2" {
		t.Fatalf("containers = %+v, want worker w-2", containers)
	}
}

func TestSupervisedDaemonListContainersDoesNotRestartOnNonSocketError(t *testing.T) {
	expected := errors.New("list containers: HTTP 403: forbidden")
	d := &supervisedDaemon{
		client: &supervisedRuntimeStub{
			listFn: func(context.Context, string) ([]pool.ContainerInfo, error) {
				return nil, expected
			},
		},
	}
	restarts := 0
	d.restartFn = func() error {
		restarts++
		return nil
	}

	_, err := d.ListContainers(context.Background(), "kitchen-test")
	if !errors.Is(err, expected) {
		t.Fatalf("ListContainers error = %v, want %v", err, expected)
	}
	if restarts != 0 {
		t.Fatalf("restart count = %d, want 0", restarts)
	}
}

func TestSupervisedPoolStateUsesKitchenProjectPoolDir(t *testing.T) {
	paths := newKitchenTestPaths(t)
	project, err := paths.Project("/tmp/example-repo")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if got := supervisedPoolSession(project); got != "kitchen-"+project.Key {
		t.Fatalf("supervisedPoolSession = %q, want %q", got, "kitchen-"+project.Key)
	}
	if got := supervisedPoolStateDir(project); got != filepath.Join(project.PoolsDir, defaultPoolStateName) {
		t.Fatalf("supervisedPoolStateDir = %q, want %q", got, filepath.Join(project.PoolsDir, defaultPoolStateName))
	}
}
